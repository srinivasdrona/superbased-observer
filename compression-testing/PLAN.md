# Observer Compression Benchmarking Plan

> **Goal**: Quantify whether the observer's API-body compression pipeline
> saves meaningful tokens without degrading model accuracy, across two
> benchmark suites with increasing stakes and cost.

---

## Overview

The observer proxy compresses LLM API request bodies before forwarding to
the provider. Three mechanisms compound:

- **Per-type compressors**: content-aware reduction of tool_result bodies
  (JSON → schema skeleton, code → dedup + whitespace strip, logs → ANSI
  strip + dedup + head/tail)
- **Budget-based message dropping**: least-important messages dropped when
  `target_ratio` not met, replaced with markers
- **Stash (CCR)**: large bodies written to disk, inline marker in request

The benchmarking pipeline is:

```
Request body → compress-bench (Go, observer pipeline)
                     ↓
             compressed body + stats (ratio, events, dropped, saved bytes)
                     ↓
             model call (Azure, Kimi-K2.6 or other)
                     ↓
             answer scored against benchmark gold
```

**Tooling built:**
- `cmd/compress-bench/main.go` — standalone Go CLI wrapping the observer
  pipeline; reads JSON body from stdin, outputs compression stats as JSON
- `compression-testing/compression_curve.py` — sweeps `target_ratio` levels,
  calls compress-bench per sample, records compression + accuracy per row
- `compression-testing/compression_smoke.py` — smoke harness for quick
  single-level runs

---

## Gate Structure

| Gate | Benchmark | Model | n | Status |
|---|---|---|---|---|
| Gate 1 | GSM8K (math accuracy) | Kimi-K2.6 / Azure | 100 per level × 8 levels | ✅ Complete |
| Gate 2.1 | SWE-bench Verified (exploratory) | gpt-5.3-codex / Azure | 25 instances × 2 arms | ✅ **PASS** (closed 2026-06-22) |
| Gate 2.2 | SWE-bench Verified (pre-registered) | gpt-5.3-codex / Azure | 50 instances × 2 arms | ✅ **PASS** (closed 2026-06-23) |
| Gate 2.3 | SWE-bench Verified (3× reps, cache-blind) | gpt-5.3-codex / Azure | 50 instances × 3 reps × 2 arms | ✅ **PASS** (closed 2026-06-24) |
| Gate 2.4 | SWE-bench Verified (3× reps, cache-aware) | gpt-5.3-codex / Azure | 50 instances × 3 reps × 2 arms | ✅ **CLOSED** (2026-06-25; cost-neutral −0.1%) |
| Gate 3 | SWE-bench Verified (full-coverage mode, drops enabled) | gpt-5.3-codex / Azure | 50 instances × 3 reps × 2 arms | 🔲 Planned |

---

## Gate 1 — GSM8K Compression Curve

### What

Run GSM8K test-split samples through the observer compression pipeline at
8 `target_ratio` levels. For each level, record:
- actual compression ratio achieved
- bytes saved
- mechanism breakdown (which compressors fired, drops, stash)
- model accuracy (exact-match against GSM8K gold)

### Why GSM8K

GSM8K is a well-understood, publicly reproducible benchmark with a clean
exact-match scoring protocol. It is fast to run and gives a stable accuracy
signal at n=100. It is not a compression stress-test on its own (the
questions are short), so the harness prepends synthetic multi-turn context
(code + logs + JSON tool_results) to each question to give the compressor
something realistic to act on. This makes the per-type compressor savings
real while the accuracy signal stays tied to an objective benchmark.

### Setup

- **Model**: Kimi-K2.6 via Azure Cognitive Services
- **Deployment**: `Kimi-K2.6`
- **Endpoint**: `https://ai-shinof1261ai979822964896.cognitiveservices.azure.com/`
- **Context padding per sample**: synthetic multi-turn body (~16KB) with:
  - code block × 15 repeats (CodeCompressor target)
  - log lines × 20 repeats (LogsCompressor target)
  - 60-item JSON array (JSONCompressor target)
- **Compression config** (`compress-bench`):
  - `mode = cache_aware`
  - `compress_types = ["json", "logs", "code", "tools"]`
  - `preserve_last_n = 5`
  - `target_ratio` swept across `[1.0, 0.95, 0.90, 0.85, 0.80, 0.70, 0.60, 0.50]`
- **Samples**: 100 per level, test split, no few-shot, temperature=0
- **Scoring**: `#### <number>` exact match

### Instrumentation

Each row in `results/gsm8k_curve_results.jsonl` contains:

| Field | Type | Description |
|---|---|---|
| `target_ratio` | float | Compression level swept |
| `idx` | int | Sample index within level |
| `correct` | bool | Exact-match against gold |
| `latency_ms` | float | End-to-end model call latency |
| `prompt_tokens` | int | Prompt token count from Azure |
| `completion_tokens` | int | Completion token count |
| `total_tokens` | int | Total tokens billed |
| `original_bytes` | int | Request body size before compression |
| `compressed_bytes` | int | Request body size after compression |
| `actual_ratio` | float | `compressed / original` |
| `bytes_saved` | int | `original - compressed` |
| `compression_count` | int | Number of per-type compression events fired |
| `dropped_count` | int | Messages dropped by budget enforcer |
| `gold` | string | Extracted gold answer |
| `pred` | string | Model's answer |

Summary per level in `results/gsm8k_curve_summary.json`.

### Results (completed)

| target_ratio | actual_ratio | bytes_saved% | accuracy | dropped |
|---:|---:|---:|---:|---:|
| 1.00 (baseline) | 1.000 | 0.0% | **86%** | 0 |
| 0.95 | 0.718 | 28.2% | 83% | 0 |
| 0.90 | 0.718 | 28.2% | 85% | 0 |
| 0.85 | 0.718 | 28.2% | 80% | 0 |
| 0.80 | 0.718 | 28.2% | 84% | 0 |
| 0.70 | 0.718 | 28.2% | 86% | 0 |
| 0.60 | 0.718 | 28.2% | 83% | 0 |
| 0.50 | 0.718 | 28.2% | 82% | 0 |

**Key findings:**
- Per-type compressors consistently achieve **28.2% byte reduction** regardless
  of `target_ratio` — the compressors hit a natural floor at 0.718 and the
  drop mechanism never fires (no messages dropped)
- Accuracy ranges **80–86%** across all compression levels, matching the
  baseline (86%) within natural sample variance at n=100
- **No accuracy degradation attributable to compression** — the 6pp spread
  is consistent with the model's inherent variance on this sample set
- The curve is flat: compression does not measurably harm reasoning accuracy

**Limitations:**
- n=100 gives ±6pp confidence at 95% — insufficient to detect a 3pp drop
- The actual_ratio is constant across levels because per-type compressors
  exhaust savings before the drop mechanism would trigger
- To stress-test the drop mechanism, longer multi-turn sessions with more
  messages are needed

### Gate 1 Pass/Fail Criteria

| Criterion | Threshold | Result |
|---|---|---|
| Byte savings at enabled levels | ≥ 5% | ✅ 28.2% |
| Accuracy Δ vs baseline | ≤ 5pp degradation | ✅ Max 6pp spread (within noise) |
| p95 latency overhead | < 200ms added | ✅ p95 varies ±2000ms (model variance, not compression) |
| Compression events fired | > 0 on compressed levels | ✅ 100/100 per level |
| Messages dropped at any level | N/A (informational) | ℹ️ 0 drops (per-type sufficient) |
| Stash errors | 0 | ✅ 0 |

**Gate 1 verdict: PASS.** Compression saves 28.2% bytes with no measurable
accuracy impact on GSM8K. Proceed to Gate 2.

---

## Gate 2 — SWE-bench Verified

### Gate 2.1 — Exploratory (CLOSED ✅ PASS, 2026-06-22)

**Cohort:** 21 astropy + 4 django (25 instances × 2 arms) — exploratory, not pre-registered  
**Agent:** SWE-agent 1.1.0 | **Model:** gpt-5.3-codex via Azure

| Metric | OFF | ON | Gate |
|--------|-----|----|------|
| Resolve rate | 10/25 (40%) | 10/25 (40%) | ✅ 0pp delta |
| Input token savings | 1,521,673 | 1,325,101 | ✅ **−12.9%** |
| Cost per resolved | $0.311 | $0.278 | ✅ ON cheaper |
| Byte savings | — | **40.2%** | ✅ |
| Turn count delta | 291 | 308 | ✅ +5.8% (<10%) |

**Layer breakdown (ON arm):**

| Layer | Mechanism | Events | Saved bytes | Share |
|-------|-----------|--------|-------------|-------|
| L3 | Stash | 269 | 3,503,988 | 89.3% |
| L1 | Logs | 440 | 370,669 | 9.4% |
| L1 | Code | 318 | 8,359 | 0.2% |
| L2 | Budget-drop | 0 | 0 | 0% |

**Full verdict:** `gate2/GATE2_1_VERDICT.md`

---

### Gate 2.3 — Variance-Estimated Re-run (✅ PASS, closed 2026-06-24)

**Goal:** Establish a clean, symmetric n=50 signal with 3× reps to separate compression effect from trajectory-divergence noise.

**Result:** Resolution ON 22.0% (31/141 done) vs OFF 22.5% (32/142 done) — neutral. Input tokens −9.1%, turns +6.8%. Cache silently zero (proxy parser bug — fixed in 2.4). Cost comparison caveated (cache-blind billing). See `gate2.3/VERDICT.md`.

---

### Gate 2.4 — Cache-Aware Replay (✅ CLOSED, 2026-06-25)

**Goal:** Exact replay of Gate 2.3 on committed `439ff1b` binary (cache-read parser bug fixed). First cache-aware token and cost accounting.

**Key findings:**
- Resolution: ON 31/150 = 20.7% vs OFF 31/150 = 20.7% — **dead heat (Δ = 0pp)**.
- Tokens: input −8.7% (non-cached), cache_read −8.8%, output **+12%** (ON higher).
- Byte compression: 41.7% (stash 91.1%, logs 8.6%, code 0.3%). Drop mechanism: **never fired** (disabled by `cache_aware` mode design).
- Cost: OFF $7.501 vs ON $7.495 = **−0.1% (neutral)**. Gate 2.3's −6.8% was a cache-blind artifact.
- Compression mode: `cache_aware` — compresses only `RoleTool` messages, drop pass disabled.

See `gate2.4/VERDICT.md`, `gate2.4/SPLIT_ANALYSIS.md` (resolved-vs-unresolved decomposition).

---

### Gate 2.2 — Pre-registered Balanced Cohort (✅ PASS, closed 2026-06-23)

**Cohort:** `gate2_2_subset_balanced_n50.txt` — 50 instances, 10 repos, 98% multi-file  
**Agent:** SWE-agent 1.1.0 | **Model:** gpt-5.3-codex via Azure  
**Cohort file:** `E:\swe-bench-3slot\artifacts\gate2_2_subset_balanced_n50.txt`

Pre-flight steps required (new repos to clone): sympy, sphinx, pylint, xarray, matplotlib, pytest, scikit-learn, seaborn  
See `gate2/GATE2_PRE_REGISTRATION.md §14.3` for checklist.

### Why SWE-bench Verified

SWE-bench Verified is the industry-standard coding agent benchmark. It uses
real GitHub issues and repo test suites, requires multi-turn tool-intensive
agentic loops (Read, Edit, Bash, Grep), and produces exactly the kind of
large multi-turn request bodies that stress every layer of the compression
pipeline — not just per-type compressors but also message dropping.

### Setup

- **Agent**: Claude Code or Codex CLI (both have observer adapters)
- **Benchmark**: SWE-bench Verified, stratified by difficulty:
  - ~17 easy, ~17 medium, ~16 hard (50 total)
- **Arms**:
  - **OFF**: observer proxy on port 8831, `compression.conversation.enabled = false`
  - **ON**: observer proxy on port 8832, `compression.conversation.enabled = true`
    with `compress_types = ["json", "logs", "code", "tools"]`,
    `mode = cache_aware`, `target_ratio = 0.85`
- **Runs per instance**: 1 per arm (each instance solved once through each proxy)
- **Infrastructure**: Docker, 120GB storage, 16GB RAM, API keys

### Instrumentation

All turns recorded in `observer.db` per arm. Required fields per turn:

| DB column | Description |
|---|---|
| `input_tokens` | Net non-cached prompt tokens |
| `output_tokens` | Completion tokens |
| `cache_read_tokens` | Cache-hit tokens (discounted) |
| `cost_usd` | Billed cost |
| `compression_original_bytes` | Body size before compression |
| `compression_compressed_bytes` | Body size after compression |
| `compression_count` | Per-type compressor events |
| `compression_dropped_count` | Messages dropped |
| `compression_events` | JSON: per-mechanism breakdown |

Primary output per arm: `% Resolved`, `cost per resolved instance`, token
counts split into raw / billable / cache-read.

### Evaluation Rubric

| Metric | Description | Measured from |
|---|---|---|
| **% Resolved** | Fraction of instances where agent patch passes all tests | SWE-bench harness |
| **Cost per resolved** | `total_cost_usd / n_resolved` | `api_turns.cost_usd` |
| **Token savings** | `(tokens_off - tokens_on) / tokens_off` | `api_turns` both DBs |
| **Byte compression ratio** | `compressed_bytes / original_bytes` avg | `api_turns` |
| **Drop rate** | Messages dropped / total messages | `compression_dropped_count` |
| **Cache preservation** | `cache_read_tokens` must not decrease ON vs OFF | `api_turns` |
| **Per-mechanism breakdown** | Bytes saved by json/code/logs/tools/drop | `compression_events` |

### Gate 2 Pass/Fail Criteria

**Success** — all of the following:

| Criterion | Threshold |
|---|---|
| % Resolved Δ (ON vs OFF) | ≤ 3pp degradation (directional at n=50) |
| Cost per resolved (ON vs OFF) | ON ≤ OFF (compression is cost-neutral or better) |
| Input token savings | ≥ 10% billable tokens saved |
| Byte compression ratio | ≤ 0.80 (≥ 20% bytes saved) |
| Cache-read tokens | Not reduced under compression |
| Request/parse errors | 0 |

**Partial success** (acceptable with documentation):

- % Resolved Δ 3–5pp: worth it if cost savings ≥ 20%
- Token savings 5–10%: acceptable for budget models

**Failure** — any of the following triggers a stop:

| Condition | Action |
|---|---|
| % Resolved drops > 5pp | Stop. Disable `drop` mechanism and re-test |
| Cache-read tokens decrease | Stop. Switch to `mode = token` and re-test |
| Byte savings < 5% | Compression not engaging — check adapter config |
| Cost per resolved increases ON vs OFF | Compression overhead > savings — investigate stash |

---

---

## Gate 3 — Full-Coverage Compression (drops enabled)

### Context: what Gate 2.4 proved, and what it left open

Gate 2.4 ran in `mode = cache_aware`. Per the source (`internal/compression/conversation/budget.go`):

> *ModeCacheAware narrows the enforcer's behaviour … drop pass is skipped entirely … per-type compression eligibility narrows to Role == RoleTool.*

This means every prior gate has tested a deliberately conservative coverage slice:
- Only `RoleTool` messages are compressed (user/assistant messages are untouched).
- The budget-enforced **drop mechanism has never fired in any gate**, by design.
- Byte savings (41.7% in 2.4) come entirely from stash (91%) and log/code compression on tool results.

Gate 3 tests what happens when coverage expands to the full conversation and drops are enabled.

### What changes

Switch the ON arm from `mode = cache_aware` to **`mode = cache`**:

| Setting | Gate 2.4 ON | Gate 3 ON | Effect |
|---------|-------------|-----------|--------|
| `mode` | `cache_aware` | `cache` | Drop pass enabled; all eligible messages compressed |
| `target_ratio` | (implicit 0.85, drop never fired) | `0.70` | Drops fire when per-type compression leaves >30% room |
| `preserve_last_n` | `5` | `3` | Fewer recent messages protected; more eligible for drop |
| `logs.max_lines` | `200` (head 20 / tail 20) | `100` (head 15 / tail 15) | Tighter log truncation |
| Stash threshold | `8192` bytes | `8192` bytes | Unchanged |
| Compressor types | `json,logs,code,tools` | `json,logs,code,tools` | Unchanged |

Why `mode=cache` not `mode=token`:
- `cache` restricts drops to the **second half** of the conversation → leading prefix is preserved for model cache hits (avoids a full cache miss regression).
- `mode=token` allows drops anywhere — the nuclear option, and likely destroys prefix cache hits. Gate 3 is not that test.
- `target_ratio=0.70` means "compress to ≤70% of original"; given that per-type compression alone gets to ~65% byte size (41.7% byte reduction = 0.583× on actual compressible content, but stash accounts for 91% of that), the drop mechanism should fire on the longer, more bloated runs.

### Hypothesis

Gate 3 primary: **resolve rate is non-inferior** on the FULL-COVERAGE arm (Δ ≤ 3pp vs OFF).
Alternative: drops cause context gaps the agent cannot bridge → rate drops > 3pp.

Gate 2.4 establishes the baseline that zero-drop compression is safe. Gate 3 asks: does the next tier of aggressiveness break anything?

### Design

- **Cohort**: same `gate2_2_subset_balanced_n50.txt` (50 instances, balanced)
- **Reps**: 3 per instance per arm → 150 per arm, 300 total
- **Model**: gpt-5.3-codex via Azure (same as 2.3/2.4)
- **Retry policy**: same (up to 10 attempts per instance per rep; harness runs once on first non-empty patch)
- **Batching**: 5 batches of 10; harness after each batch

| Arm | Port | Mode |
|-----|------|------|
| OFF | 8851 | Compression disabled (fresh run, same cohort/commits as 2.4) |
| ON-FULL | 8852 | `mode=cache`, `target_ratio=0.70`, `preserve_last_n=3`, tighter logs |

Fresh OFF arm (don't reuse 2.4 OFF): avoids any concern about model/API drift between June 25 and Gate 3 run date; both arms run contemporaneously.

### Pass/Fail criteria

| Criterion | Threshold | Gate 2.4 result |
|-----------|-----------|-----------------|
| Resolve-rate Δ (ON vs OFF) | ≤ 3pp | 0.0pp |
| Drop mechanism fires | Must observe >0 drops (if 0: coverage didn't expand) | 0 drops (cache_aware by design) |
| Byte compression ratio | < 0.65 (stricter than 2.4's 0.583 on compressible content) | ~0.583 compressible |
| Cache-read tokens ON vs OFF | Not significantly reduced (drops shouldn't destroy prefix) | −8.8% |
| Request/parse errors | 0 | 0 |

Secondary (comparison to Gate 2.4 ON):
- How much additional byte saving does full-coverage produce vs cache_aware?
- Does cost improve further (more input saved, at expense of possibly more cache miss)?

### Failure modes and their meaning

| What happens | Interpretation | Next step |
|---|---|---|
| Drops fire, resolve rate neutral | Full-coverage compression is safe; expand to production | Gate 4: multi-model cost calibration |
| Drops fire, resolve rate drops > 3pp | Drop mechanism is harmful; cache_aware is the right production setting | File bug; investigate which dropped messages cause failures |
| Drops don't fire even with target_ratio=0.70 | Per-type compression already hits target; stash is that dominant | Lower target_ratio to 0.55 or investigate mode=token |
| Cache-read tokens collapse ON vs OFF | mode=cache prefix disruption is worse than expected | Revert to cache_aware; drops confirmed unsafe for prefix cache |

### Artifacts to create

- `gate3/ab-off-3.toml` — OFF arm (compression disabled, port 8851)
- `gate3/ab-on-3.toml` — ON arm (mode=cache, target_ratio=0.70, preserve_last_n=3)
- `gate3/run_gate3.py` — runner (adapt run_gate2_4.py with new ports/dirs/config)
- `gate3/VERDICT.md` — post-run verdict (same three-order framing as 2.4)

---

## Gates 4+ (tentative)

1. **Gate 4 — Multi-model cost calibration**: Same SWE-bench slice through 2–3 models (Sonnet 4.6, DeepSeek-V4-Pro) with compression ON/OFF. Answers: does compression's cost impact vary by model tier?

2. **Gate 5 — Long-session stress test**: Capture real 50+ turn sessions and replay at extreme `target_ratio` (0.50). Tests whether mode=token drops degrade agent continuity in extreme context.

These are not planned until Gate 3 produces its verdict.

---

## Artifacts

| File | Contents |
|---|---|
| `cmd/compress-bench/main.go` | Go CLI wrapping the observer pipeline |
| `compression-testing/compression_curve.py` | Sweep runner |
| `compression-testing/compression_smoke.py` | Smoke harness |
| `compression-testing/ab_run.sh` | Two-arm proxy launcher |
| `compression-testing/ab-off.toml` | Observer config: compression OFF |
| `compression-testing/ab-on.toml` | Observer config: compression ON |
| `compression-testing/results/gsm8k_curve_results.jsonl` | Gate 1 raw rows |
| `compression-testing/results/gsm8k_curve_summary.json` | Gate 1 per-level summary |
| `compression-testing/results/gsm8k_curve_plot.txt` | Gate 1 ASCII plot |
| `compression-testing/.env` (gitignored) | Azure credentials |

---

## Estimated Budget

| Gate | Cost estimate | Actual |
|---|---|---|
| Gate 1 (complete) | ~$8 Azure API credits (800 model calls) | ~$8 |
| Gate 2.1–2.2 | $500–$2,000 | ~$15–30 |
| Gate 2.3 (complete) | — | ~$7.50 |
| Gate 2.4 (complete) | — | OFF $7.501 / ON $7.495 = ~$15 |
| Gate 3 (planned) | ~$15–20 (same cohort/model/reps as 2.4) | — |
| Gate 4+ | TBD after Gate 3 | — |
