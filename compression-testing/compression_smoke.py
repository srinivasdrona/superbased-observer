#!/usr/bin/env python3

"""Minimal compression smoke-test runner for GSM8K.

Run two or three arms against the same GSM8K slice and compare correctness,
latency, and token usage.

Requirements:
- `pip install openai datasets`
- a local observer proxy per arm, or a direct provider endpoint
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import time
from dataclasses import dataclass
from typing import Iterable

from datasets import load_dataset
from openai import AzureOpenAI, OpenAI


def load_dotenv_file() -> None:
    candidates = [
        ".env",
        os.path.join("..", ".env"),
        os.path.join(os.path.dirname(__file__), ".env"),
        os.path.join(os.path.dirname(__file__), "..", ".env"),
    ]
    for path in candidates:
        try:
            with open(path, "r", encoding="utf-8") as f:
                for raw in f:
                    line = raw.strip()
                    if not line or line.startswith("#") or "=" not in line:
                        continue
                    key, value = line.split("=", 1)
                    if value.startswith("'") and value.endswith("'"):
                        value = value[1:-1]
                    elif value.startswith('"') and value.endswith('"'):
                        value = value[1:-1]
                    os.environ.setdefault(key.strip(), value)
            return
        except FileNotFoundError:
            continue


@dataclass(frozen=True)
class Arm:
    name: str
    azure_endpoint_env: str
    azure_deployment: str
    model: str
    description: str
    azure_api_version_env: str = ""


ARMS: dict[str, Arm] = {
    # OFF: calls Azure directly, no compression.
    # Compression field in each row: original_bytes = prompt_bytes, compressed_bytes = prompt_bytes (ratio=1.0).
    "off": Arm(
        name="off",
        azure_endpoint_env="AZURE_OPENAI_ENDPOINT",
        azure_deployment="Kimi-K2.6",
        model="Kimi-K2.6",
        description="Compression disabled — direct Azure, baseline",
        azure_api_version_env="AZURE_OPENAI_API_VERSION",
    ),
    # ON: calls Azure directly but runs observer compression pipeline on request
    # body first, measures byte delta, then sends the COMPRESSED body to Azure.
    "on": Arm(
        name="on",
        azure_endpoint_env="AZURE_OPENAI_ENDPOINT",
        azure_deployment="Kimi-K2.6",
        model="Kimi-K2.6",
        description="Compression enabled — compress body before send",
        azure_api_version_env="AZURE_OPENAI_API_VERSION",
    ),
}


def extract_final_answer(text: str) -> str:
    text = text.strip()
    marker = re.search(r"####\s*([-+]?\d[\d,]*(?:\.\d+)?)", text)
    if marker:
        return marker.group(1).replace(",", "")
    m = re.search(r"(-?\d+(?:,\d{3})*(?:\.\d+)?)\s*$", text)
    if m:
        return m.group(1).replace(",", "")
    return text


def gsm8k_samples(limit: int, split: str, offset: int = 0) -> list[dict[str, str]]:
    ds = load_dataset("openai/gsm8k", "main", split=split)
    out: list[dict[str, str]] = []
    start = max(0, offset)
    end = min(start + limit, len(ds))
    for row in ds.select(range(start, end)):
        out.append({"question": row["question"], "answer": row["answer"]})
    return out


def gsm8k_few_shot_examples(count: int = 4) -> list[dict[str, str]]:
    ds = load_dataset("openai/gsm8k", "main", split="train")
    out: list[dict[str, str]] = []
    for row in ds.select(range(min(count, len(ds)))):
        out.append({"question": row["question"], "answer": row["answer"]})
    return out


COMPRESS_BENCH = os.getenv(
    "COMPRESS_BENCH_BIN",
    os.path.join(os.path.dirname(__file__), "..", "bin", "compress-bench"),
)


def measure_compression(request_body: dict) -> dict:
    """Run the observer compression pipeline on a request body and return stats."""
    body_bytes = json.dumps(request_body, ensure_ascii=False).encode("utf-8")
    try:
        result = subprocess.run(
            [COMPRESS_BENCH, "--provider", "openai"],
            input=body_bytes,
            capture_output=True,
            timeout=10,
        )
        if result.returncode != 0:
            return {"error": result.stderr.decode("utf-8", errors="replace")[:200]}
        return json.loads(result.stdout)
    except Exception as e:
        return {"error": str(e)}


def make_client(arm: Arm) -> tuple[OpenAI, str]:
    """Return an AzureOpenAI client wired to the real Azure endpoint.
    Both OFF and ON arms talk directly to Azure — compression impact is measured
    by calling compress-bench on the request body before sending.
    """
    api_key = os.getenv("AZURE_OPENAI_API_KEY")
    if not api_key:
        raise RuntimeError("missing env var: AZURE_OPENAI_API_KEY")
    endpoint = os.getenv("AZURE_OPENAI_ENDPOINT")
    if not endpoint:
        raise RuntimeError("missing env var: AZURE_OPENAI_ENDPOINT")
    api_version = os.getenv("AZURE_OPENAI_API_VERSION", "2024-10-21")
    client: OpenAI = AzureOpenAI(
        api_key=api_key,
        azure_endpoint=endpoint,
        api_version=api_version,
        timeout=120,
    )
    return client, arm.azure_deployment


def _content_to_text(content: object) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for item in content:
            if isinstance(item, dict):
                txt = item.get("text")
                if isinstance(txt, str):
                    parts.append(txt)
            elif isinstance(item, str):
                parts.append(item)
        return "\n".join(parts)
    return str(content)


def ask(client: OpenAI, model: str, question: str) -> tuple[str, float, dict[str, int]]:
    start = time.perf_counter()
    resp = client.chat.completions.create(
        model=model,
        messages=[
            {
                "role": "system",
                "content": (
                    "You solve arithmetic word problems. "
                    "Output exactly one line: #### <final_number>. "
                    "Do not include any other text."
                ),
            },
            {"role": "user", "content": question},
        ],
        temperature=0,
        max_tokens=400,
        extra_body={"thinking": {"type": "disabled"}},
    )
    latency_ms = (time.perf_counter() - start) * 1000.0
    usage = resp.usage.model_dump() if resp.usage else {}
    msg = resp.choices[0].message
    raw = _content_to_text(getattr(msg, "content", ""))
    if not raw:
        raw = _content_to_text(getattr(msg, "reasoning_content", ""))
    return raw, latency_ms, {
        "prompt_tokens": int(usage.get("prompt_tokens", 0) or 0),
        "completion_tokens": int(usage.get("completion_tokens", 0) or 0),
        "total_tokens": int(usage.get("total_tokens", 0) or 0),
    }


def evaluate_arm(arm: Arm, samples: Iterable[dict[str, str]], few_shot: int) -> list[dict[str, object]]:
    client, model_name = make_client(arm)
    rows: list[dict[str, object]] = []
    exemplars = gsm8k_few_shot_examples(few_shot) if few_shot > 0 else []
    for idx, sample in enumerate(samples, start=1):
        prompt = build_benchmark_prompt(exemplars, sample["question"])

        # Build the full request body shape that would go to the model.
        request_body = {
            "model": model_name,
            "messages": [
                {
                    "role": "system",
                    "content": (
                        "You solve arithmetic word problems. "
                        "Output exactly one line: #### <final_number>. "
                        "Do not include any other text."
                    ),
                },
                {"role": "user", "content": prompt},
            ],
        }

        # Measure compression on the ON arm before sending.
        comp_stats: dict[str, object] = {}
        if arm.name == "on":
            comp_stats = measure_compression(request_body)

        raw, latency_ms, usage = ask(client, model_name, prompt)
        pred = extract_final_answer(raw)
        gold = extract_final_answer(sample["answer"])
        correct = pred == gold

        prompt_bytes = len(prompt.encode("utf-8"))
        rows.append(
            {
                "idx": idx,
                "arm": arm.name,
                "correct": correct,
                "latency_ms": round(latency_ms, 1),
                "prompt_tokens": usage["prompt_tokens"],
                "completion_tokens": usage["completion_tokens"],
                "total_tokens": usage["total_tokens"],
                "prompt_bytes": prompt_bytes,
                "response_bytes": len(raw.encode("utf-8")),
                "compression_original_bytes": comp_stats.get("original_bytes", prompt_bytes),
                "compression_compressed_bytes": comp_stats.get("compressed_bytes", prompt_bytes),
                "compression_ratio": comp_stats.get("ratio", 1.0),
                "compression_skipped": comp_stats.get("skipped", True),
                "compression_count": comp_stats.get("compressed_count", 0),
                "compression_dropped": comp_stats.get("dropped_count", 0),
                "compression_events": comp_stats.get("events") or [],
                "gold": gold,
                "pred": pred,
                "raw": raw,
            }
        )
        saved = comp_stats.get("original_bytes", 0) - comp_stats.get("compressed_bytes", 0)
        comp_info = f" comp={comp_stats.get('ratio',1.0):.2f} saved={saved}B" if arm.name == "on" else ""
        print(f"[{arm.name}] {idx:03d} correct={correct} latency={latency_ms:.1f}ms{comp_info}")
    return rows


def build_benchmark_prompt(exemplars: list[dict[str, str]], question: str) -> str:
    if not exemplars:
        return question
    parts: list[str] = []
    for ex in exemplars:
        parts.append(
            f"Question: {ex['question']}\n"
            f"Answer: #### {extract_final_answer(ex['answer'])}\n"
        )
    parts.append(
        "Question: " + question + "\n"
        "Answer: ####"
    )
    return "\n".join(parts)


def summarize(rows: list[dict[str, object]]) -> dict[str, object]:
    n = len(rows)
    correct = sum(1 for r in rows if r["correct"])
    avg_latency = sum(float(r["latency_ms"]) for r in rows) / n if n else 0.0
    avg_prompt = sum(int(r["prompt_tokens"]) for r in rows) / n if n else 0.0
    avg_total = sum(int(r["total_tokens"]) for r in rows) / n if n else 0.0
    avg_prompt_bytes = sum(int(r.get("prompt_bytes", 0)) for r in rows) / n if n else 0.0
    avg_response_bytes = sum(int(r.get("response_bytes", 0)) for r in rows) / n if n else 0.0
    lats = sorted(float(r["latency_ms"]) for r in rows)
    p50 = lats[int(0.50 * n) - 1] if n else 0.0
    p95 = lats[int(0.95 * n) - 1] if n else 0.0
    p99 = lats[int(0.99 * n) - 1] if n else 0.0
    total_orig = sum(int(r.get("compression_original_bytes", r.get("prompt_bytes", 0))) for r in rows)
    total_comp = sum(int(r.get("compression_compressed_bytes", r.get("prompt_bytes", 0))) for r in rows)
    avg_ratio = (total_comp / total_orig) if total_orig > 0 else 1.0
    return {
        "n": n,
        "correct": correct,
        "accuracy": (correct / n) if n else 0.0,
        "avg_latency_ms": avg_latency,
        "p50_latency_ms": p50,
        "p95_latency_ms": p95,
        "p99_latency_ms": p99,
        "avg_prompt_tokens": avg_prompt,
        "avg_total_tokens": avg_total,
        "avg_prompt_bytes": avg_prompt_bytes,
        "avg_response_bytes": avg_response_bytes,
        "total_prompt_tokens": sum(int(r["prompt_tokens"]) for r in rows),
        "total_completion_tokens": sum(int(r["completion_tokens"]) for r in rows),
        "total_tokens": sum(int(r["total_tokens"]) for r in rows),
        "total_prompt_bytes": sum(int(r.get("prompt_bytes", 0)) for r in rows),
        "total_response_bytes": sum(int(r.get("response_bytes", 0)) for r in rows),
        "compression_original_bytes": total_orig,
        "compression_compressed_bytes": total_comp,
        "compression_bytes_saved": total_orig - total_comp,
        "compression_ratio": round(avg_ratio, 4),
        "compression_pct_saved": round((1.0 - avg_ratio) * 100, 2),
        "compression_count": sum(int(r.get("compression_count", 0)) for r in rows),
        "compression_dropped": sum(int(r.get("compression_dropped", 0)) for r in rows),
    }


def main() -> None:
    load_dotenv_file()
    parser = argparse.ArgumentParser()
    parser.add_argument("--arm", choices=sorted(ARMS.keys()), help="Run a single arm")
    parser.add_argument("--samples", type=int, default=20)
    parser.add_argument("--offset", type=int, default=0)
    parser.add_argument("--split", default="test", choices=["train", "test"])
    parser.add_argument("--out", default="gsm8k_results.jsonl")
    parser.add_argument("--arms", nargs="*", default=["off", "on"], choices=sorted(ARMS.keys()))
    parser.add_argument("--few-shot", type=int, default=0)
    parser.add_argument("--summary-out", default="")
    parser.add_argument("--azure-api-key", default=os.getenv("AZURE_OPENAI_API_KEY"))
    parser.add_argument("--azure-endpoint", default=os.getenv("AZURE_OPENAI_ENDPOINT"))
    parser.add_argument("--azure-api-version", default=os.getenv("AZURE_OPENAI_API_VERSION", "2024-10-21"))
    args = parser.parse_args()

    if args.azure_api_key:
        os.environ["AZURE_OPENAI_API_KEY"] = args.azure_api_key
    if args.azure_endpoint:
        os.environ["AZURE_OPENAI_ENDPOINT"] = args.azure_endpoint
    if args.azure_api_version:
        os.environ["AZURE_OPENAI_API_VERSION"] = args.azure_api_version

    if args.arm:
        arms = [ARMS[args.arm]]
    else:
        arms = [ARMS[name] for name in args.arms]
    samples = gsm8k_samples(args.samples, args.split, args.offset)

    print(f"Loaded {len(samples)} GSM8K samples from split={args.split} offset={args.offset}")

    all_rows: list[dict[str, object]] = []
    by_arm_summary: dict[str, dict[str, object]] = {}
    for arm in arms:
        print(f"\n=== ARM: {arm.name} ({arm.description}) ===")
        rows = evaluate_arm(arm, samples, args.few_shot)
        summary = summarize(rows)
        print(json.dumps(summary, indent=2))
        all_rows.extend(rows)
        by_arm_summary[arm.name] = summary

    with open(args.out, "w", encoding="utf-8") as f:
        for row in all_rows:
            f.write(json.dumps(row, ensure_ascii=False) + "\n")

    if args.summary_out:
        with open(args.summary_out, "w", encoding="utf-8") as f:
            json.dump(by_arm_summary, f, ensure_ascii=False, indent=2)

    print(f"\nWrote {len(all_rows)} rows to {args.out}")


if __name__ == "__main__":
    main()
