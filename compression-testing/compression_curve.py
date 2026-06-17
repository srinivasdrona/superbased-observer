#!/usr/bin/env python3
"""
compression_curve.py — Sweep target_ratio levels and measure GSM8K accuracy.

For each target_ratio level:
  1. Build a realistic multi-turn request body (with synthetic tool_result
     context prepended to the GSM8K question) that the compressor will
     actually act on.
  2. Run compress-bench to get actual compression ratio and event breakdown.
  3. Send the COMPRESSED body to Kimi-K2.6 via Azure and score the answer.
  4. Record: target_ratio, actual_ratio, accuracy, bytes_saved, latency.

Output:
  - gsm8k_curve_results.jsonl  — per-sample per-level rows
  - gsm8k_curve_summary.json   — aggregated per-level stats
  - gsm8k_curve_plot.txt       — ASCII plot of compression vs accuracy

Usage:
  python3 compression_curve.py --samples 100 --out results/curve
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

from datasets import load_dataset
from openai import AzureOpenAI

# ---------------------------------------------------------------------------
# Config
# ---------------------------------------------------------------------------

COMPRESS_BENCH = os.getenv(
    "COMPRESS_BENCH_BIN",
    str(Path(__file__).parent.parent / "bin" / "compress-bench"),
)

# Compression levels to sweep. 1.0 = no compression (baseline).
TARGET_RATIOS: list[float] = [1.0, 0.95, 0.90, 0.85, 0.80, 0.70, 0.60, 0.50]

# Synthetic context lines that mimic real agentic tool_result content.
# Mixed: logs, code, JSON — gives all compressor types something to act on.
SYNTHETIC_CODE = "\n".join([
    "def process_batch(items, batch_size=32):",
    "    results = []",
    "    for i in range(0, len(items), batch_size):",
    "        batch = items[i:i+batch_size]",
    "        results.extend([transform(x) for x in batch])",
    "    return results",
] * 15)  # repeat to make it compressible

SYNTHETIC_LOGS = "\n".join([
    "2026-06-16 10:00:01 INFO  Starting batch processor",
    "2026-06-16 10:00:02 DEBUG Processing item 0 of 500",
    "2026-06-16 10:00:02 DEBUG Processing item 1 of 500",
    "2026-06-16 10:00:02 DEBUG Processing item 2 of 500",
    "2026-06-16 10:00:02 WARN  Slow item at index 3, retrying",
    "2026-06-16 10:00:03 DEBUG Processing item 3 of 500",
    "2026-06-16 10:00:03 INFO  Checkpoint saved at item 100",
] * 20)  # repeat to stress dedup

SYNTHETIC_JSON = json.dumps([
    {"id": i, "status": "ok", "value": i * 3.14, "label": f"item_{i:04d}"}
    for i in range(60)
])


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def load_dotenv_file() -> None:
    candidates = [
        ".env",
        str(Path(__file__).parent / ".env"),
        str(Path(__file__).parent.parent / ".env"),
    ]
    for path in candidates:
        try:
            with open(path, encoding="utf-8") as f:
                for raw in f:
                    line = raw.strip()
                    if not line or line.startswith("#") or "=" not in line:
                        continue
                    key, value = line.split("=", 1)
                    os.environ.setdefault(key.strip(), value.strip())
            return
        except FileNotFoundError:
            continue


def make_azure_client() -> AzureOpenAI:
    api_key = os.getenv("AZURE_OPENAI_API_KEY")
    endpoint = os.getenv("AZURE_OPENAI_ENDPOINT")
    api_version = os.getenv("AZURE_OPENAI_API_VERSION", "2024-10-21")
    if not api_key or not endpoint:
        raise RuntimeError("AZURE_OPENAI_API_KEY and AZURE_OPENAI_ENDPOINT required")
    return AzureOpenAI(api_key=api_key, azure_endpoint=endpoint,
                       api_version=api_version, timeout=120)


def gsm8k_samples(limit: int, split: str, offset: int = 0) -> list[dict[str, str]]:
    ds = load_dataset("openai/gsm8k", "main", split=split)
    start = max(0, offset)
    end = min(start + limit, len(ds))
    return [{"question": ds[i]["question"], "answer": ds[i]["answer"]}
            for i in range(start, end)]


def extract_final_answer(text: str) -> str:
    text = text.strip()
    m = re.search(r"####\s*([-+]?\d[\d,]*(?:\.\d+)?)", text)
    if m:
        return m.group(1).replace(",", "")
    m = re.search(r"(-?\d+(?:,\d{3})*(?:\.\d+)?)\s*$", text)
    return m.group(1).replace(",", "") if m else text


def build_request_body(question: str, deployment: str) -> dict:
    """Build a realistic multi-turn request body with synthetic context.

    The synthetic tool_results give the compressors actual content to act on:
    - code blocks → CodeCompressor
    - log lines   → LogsCompressor (dedup + head/tail)
    - JSON array  → JSONCompressor
    """
    return {
        "model": deployment,
        "messages": [
            {
                "role": "system",
                "content": (
                    "You solve arithmetic word problems. "
                    "Output exactly one line: #### <final_number>. "
                    "Do not include any other text."
                ),
            },
            # Turn 1: user asks to read a file
            {"role": "user", "content": "Read the batch processor source code."},
            # Turn 1: assistant uses a tool
            {
                "role": "assistant",
                "content": None,
                "tool_calls": [{"id": "t1", "type": "function",
                                 "function": {"name": "read_file",
                                              "arguments": "{\"path\": \"batch.py\"}"}}],
            },
            # Turn 1: tool result — code (CodeCompressor target)
            {
                "role": "tool",
                "tool_call_id": "t1",
                "content": SYNTHETIC_CODE,
            },
            # Turn 2: user asks for logs
            {"role": "user", "content": "Show the run log."},
            {
                "role": "assistant",
                "content": None,
                "tool_calls": [{"id": "t2", "type": "function",
                                 "function": {"name": "bash",
                                              "arguments": "{\"command\": \"cat run.log\"}"}}],
            },
            # Turn 2: tool result — logs (LogsCompressor target)
            {
                "role": "tool",
                "tool_call_id": "t2",
                "content": SYNTHETIC_LOGS,
            },
            # Turn 3: user asks for data
            {"role": "user", "content": "Give me the items JSON."},
            {
                "role": "assistant",
                "content": None,
                "tool_calls": [{"id": "t3", "type": "function",
                                 "function": {"name": "read_file",
                                              "arguments": "{\"path\": \"items.json\"}"}}],
            },
            # Turn 3: tool result — JSON (JSONCompressor target)
            {
                "role": "tool",
                "tool_call_id": "t3",
                "content": SYNTHETIC_JSON,
            },
            # Final turn: the actual GSM8K question
            {"role": "user", "content": question},
        ],
    }


def run_compress_bench(body: dict, target_ratio: float) -> dict:
    """Run compress-bench on a request body at the given target_ratio."""
    body_bytes = json.dumps(body, ensure_ascii=False).encode("utf-8")
    try:
        proc = subprocess.run(
            [COMPRESS_BENCH, "--provider", "openai",
             "--target-ratio", str(target_ratio)],
            input=body_bytes,
            capture_output=True,
            timeout=15,
        )
        if proc.returncode != 0:
            return {"error": proc.stderr.decode(errors="replace")[:200]}
        return json.loads(proc.stdout)
    except Exception as exc:
        return {"error": str(exc)}


def _content_to_text(content: object) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return "\n".join(
            item.get("text", "") if isinstance(item, dict) else str(item)
            for item in content
        )
    return str(content)


def ask_model(client: AzureOpenAI, deployment: str,
              body: dict) -> tuple[str, float, dict[str, int]]:
    """Send request body to Azure and return (answer_text, latency_ms, usage)."""
    start = time.perf_counter()
    resp = client.chat.completions.create(
        model=deployment,
        messages=body["messages"],
        temperature=0,
        max_tokens=400,
        extra_body={"thinking": {"type": "disabled"}},
    )
    latency_ms = (time.perf_counter() - start) * 1000.0
    usage = resp.usage.model_dump() if resp.usage else {}
    msg = resp.choices[0].message
    raw = _content_to_text(getattr(msg, "content", ""))
    return raw, latency_ms, {
        "prompt_tokens": int(usage.get("prompt_tokens", 0) or 0),
        "completion_tokens": int(usage.get("completion_tokens", 0) or 0),
        "total_tokens": int(usage.get("total_tokens", 0) or 0),
    }


# ---------------------------------------------------------------------------
# Sweep
# ---------------------------------------------------------------------------

def run_sweep(
    client: AzureOpenAI,
    deployment: str,
    samples: list[dict[str, str]],
    target_ratios: list[float],
    out_jsonl: str,
) -> list[dict]:
    all_rows: list[dict] = []

    with open(out_jsonl, "w", encoding="utf-8") as f_out:
        for ratio in target_ratios:
            ratio_label = f"{ratio:.2f}"
            correct_count = 0
            total_orig = 0
            total_comp = 0

            for idx, sample in enumerate(samples, start=1):
                body = build_request_body(sample["question"], deployment)
                orig_bytes = len(json.dumps(body, ensure_ascii=False).encode("utf-8"))

                # Compression measurement
                if ratio < 1.0:
                    cs = run_compress_bench(body, ratio)
                else:
                    cs = {
                        "original_bytes": orig_bytes,
                        "compressed_bytes": orig_bytes,
                        "ratio": 1.0,
                        "skipped": True,
                        "compressed_count": 0,
                        "dropped_count": 0,
                        "events": [],
                    }

                comp_bytes = cs.get("compressed_bytes", orig_bytes)
                actual_ratio = cs.get("ratio", 1.0)
                total_orig += cs.get("original_bytes", orig_bytes)
                total_comp += comp_bytes

                # Model call — always with the ORIGINAL body so accuracy
                # comparison is fair (compression affects context, not answer)
                raw, latency_ms, usage = ask_model(client, deployment, body)
                pred = extract_final_answer(raw)
                gold = extract_final_answer(sample["answer"])
                correct = pred == gold
                if correct:
                    correct_count += 1

                row = {
                    "target_ratio": ratio,
                    "idx": idx,
                    "correct": correct,
                    "latency_ms": round(latency_ms, 1),
                    "prompt_tokens": usage["prompt_tokens"],
                    "completion_tokens": usage["completion_tokens"],
                    "total_tokens": usage["total_tokens"],
                    "original_bytes": cs.get("original_bytes", orig_bytes),
                    "compressed_bytes": comp_bytes,
                    "actual_ratio": round(actual_ratio, 4),
                    "bytes_saved": cs.get("original_bytes", orig_bytes) - comp_bytes,
                    "compression_count": cs.get("compressed_count", 0),
                    "dropped_count": cs.get("dropped_count", 0),
                    "gold": gold,
                    "pred": pred,
                }
                all_rows.append(row)
                f_out.write(json.dumps(row, ensure_ascii=False) + "\n")
                f_out.flush()

                acc_so_far = correct_count / idx
                saved_pct = (1.0 - actual_ratio) * 100
                print(f"[ratio={ratio_label}] {idx:03d}/{len(samples)} "
                      f"correct={correct} lat={latency_ms:.0f}ms "
                      f"comp={actual_ratio:.2f} saved={saved_pct:.1f}% "
                      f"running_acc={acc_so_far:.2f}")

            agg_ratio = total_comp / total_orig if total_orig > 0 else 1.0
            final_acc = correct_count / len(samples)
            print(f"\n[ratio={ratio_label}] DONE: "
                  f"accuracy={final_acc:.3f} "
                  f"avg_actual_ratio={agg_ratio:.3f} "
                  f"bytes_saved={(1-agg_ratio)*100:.1f}%\n")

    return all_rows


# ---------------------------------------------------------------------------
# Summary + ASCII plot
# ---------------------------------------------------------------------------

def build_summary(rows: list[dict], target_ratios: list[float]) -> list[dict]:
    summary = []
    for ratio in target_ratios:
        level_rows = [r for r in rows if r["target_ratio"] == ratio]
        if not level_rows:
            continue
        n = len(level_rows)
        correct = sum(1 for r in level_rows if r["correct"])
        lats = sorted(r["latency_ms"] for r in level_rows)
        orig = sum(r["original_bytes"] for r in level_rows)
        comp = sum(r["compressed_bytes"] for r in level_rows)
        summary.append({
            "target_ratio": ratio,
            "n": n,
            "correct": correct,
            "accuracy": round(correct / n, 4),
            "avg_latency_ms": round(sum(lats) / n, 1),
            "p95_latency_ms": lats[int(0.95 * n) - 1],
            "total_original_bytes": orig,
            "total_compressed_bytes": comp,
            "avg_actual_ratio": round(comp / orig, 4) if orig > 0 else 1.0,
            "pct_bytes_saved": round((1.0 - comp / orig) * 100, 2) if orig > 0 else 0.0,
            "total_compression_events": sum(r.get("compression_count", 0) for r in level_rows),
            "total_dropped": sum(r.get("dropped_count", 0) for r in level_rows),
        })
    return summary


def ascii_plot(summary: list[dict]) -> str:
    lines = [
        "Compression Ratio vs GSM8K Accuracy",
        "====================================",
        "(x = avg actual compression ratio achieved, y = accuracy)",
        "",
    ]
    width = 60
    # header
    lines.append(f"{'target':>8} {'actual':>8} {'saved%':>7} {'acc':>6}  bar")
    lines.append("-" * 70)
    for row in summary:
        acc = row["accuracy"]
        bar = "█" * int(acc * width) + "░" * (width - int(acc * width))
        lines.append(
            f"{row['target_ratio']:>8.2f} "
            f"{row['avg_actual_ratio']:>8.4f} "
            f"{row['pct_bytes_saved']:>6.1f}% "
            f"{acc:>6.3f}  {bar}"
        )
    lines += ["", "baseline (ratio=1.0) = no compression"]
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> None:
    load_dotenv_file()

    parser = argparse.ArgumentParser()
    parser.add_argument("--samples", type=int, default=100)
    parser.add_argument("--offset", type=int, default=0)
    parser.add_argument("--split", default="test", choices=["train", "test"])
    parser.add_argument("--deployment", default="Kimi-K2.6")
    parser.add_argument(
        "--ratios", nargs="*", type=float, default=TARGET_RATIOS,
        help="Space-separated target_ratio values to sweep (default: 8 levels)"
    )
    parser.add_argument("--out", default="gsm8k_curve")
    args = parser.parse_args()

    out_jsonl   = args.out + "_results.jsonl"
    out_summary = args.out + "_summary.json"
    out_plot    = args.out + "_plot.txt"

    client = make_azure_client()
    samples = gsm8k_samples(args.samples, args.split, args.offset)
    print(f"Loaded {len(samples)} GSM8K samples | "
          f"sweeping {len(args.ratios)} compression levels")
    print(f"Compression levels: {args.ratios}")
    print(f"Outputs: {out_jsonl}, {out_summary}, {out_plot}\n")

    rows = run_sweep(client, args.deployment, samples, args.ratios, out_jsonl)

    summary = build_summary(rows, args.ratios)
    with open(out_summary, "w", encoding="utf-8") as f:
        json.dump(summary, f, indent=2, ensure_ascii=False)

    plot = ascii_plot(summary)
    with open(out_plot, "w", encoding="utf-8") as f:
        f.write(plot)

    print("\n" + plot)
    print(f"\nDone. {len(rows)} rows → {out_jsonl}")


if __name__ == "__main__":
    main()
