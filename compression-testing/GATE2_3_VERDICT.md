# Gate 2.3 Verdict — Compression Non-Inferiority on SWE-bench Verified

**Date**: 2026-06-24 (revised — full n=50, timezone-corrected DB scoping; token/cost direction updated)
**Design**: 50 instances × 2 arms (ON/OFF compression) × 3 reps = 300 runs (150 per arm)
**Model**: azure__gpt-5.3-codex via proxy (ON: port 8832 / OFF: port 8831)
**Dataset**: SWE-bench Verified (princeton-nlp/SWE-bench_Verified)

---

## Headline

Compression is **non-inferior on resolve rate, modestly cheaper at the model boundary
(−9% model-side input tokens, lower cost in both meters), and systematically more
exploratory — but the extra exploration produced zero extra fixes.** Safe to ship: it
saves model-side tokens/cost and footprint at no resolve-rate cost. No measured
problem-solving benefit from "freed context" in this sample.

---

## 1. Primary result — resolve rate (harness)

| Batch | ON resolved | ON done | OFF resolved | OFF done |
|-------|-------------|---------|--------------|----------|
| 1 | 3 | 25 | 3 | 24 |
| 2 | 7 | 30 | 5 | 30 |
| 3 | 2 | 29 | 2 | 30 |
| 4 | 13 | 27 | 15 | 28 |
| 5 | 6 | 30 | 7 | 30 |
| **Total** | **31** | **141** | **32** | **142** |

ON **22.0%** vs OFF **22.5%** → delta **−0.5pp**. SE ≈ sqrt(0.22·0.78/141) ≈ **3.5pp**,
so delta = **0.14 SE — statistically indistinguishable from zero.**

**VERDICT: PASS — non-inferior with >10pp margin.**

---

## 2. Tokens, turns, cost (complete n=50)

Two vantage points, both at full n=50, now reconciled:

**Agent-side (pre-compression)** — from trajectory `model_stats`, all 300 runs:

| Metric | ON | OFF | Δ |
|--------|-----|------|---|
| runs | 150 | 150 | — |
| turns (api_calls) | 2,242 | 2,054 | +9.2% |
| context built / turn | 6,510 | 4,908 | +32.7% |

**Model-side (post-compression)** — from the observer DB, all 5 batches, same meter both
arms (correct UTC window `[06-23T07:30, 06-24T12:00]`):

| Metric | ON | OFF | Δ |
|--------|-----|------|---|
| turns (incl. patch retries) | 2,395 | 2,243 | +6.8% |
| input tokens / turn | **4,662** | **5,473** | **−14.8%** |
| total input tokens | 11.16M | 12.28M | **−9.1%** |
| total output tokens | 273.8K | 256.1K | +6.9% |
| cost (proxy meter, incl. retries) | $23.37 | $25.07 | −6.8% |
| cost (trajectory meter, final traj) | $7.60 | $7.68 | −1.0% |

**The mechanism is now clean and complete:**

- The SWE-agent proxy is transparent, so the agent stores full history in *both* arms.
  ON's trajectories are **heavier** per turn pre-compression (6,510 vs 4,908) — a
  consequence of running +9.2% more turns and accumulating more history.
- The proxy compresses ON's payload so that at the model boundary ON is **lighter** than
  OFF: **4,662 vs 5,473 input tokens/turn (−14.8%)**, and **−9.1% total input tokens**,
  *even though ON runs more turns*. Compression more than offsets ON's heavier exploration.
- **Cost is lower for ON in both meters** (−6.8% proxy / −1.0% trajectory). The two meters
  disagree on absolute magnitude (~3× — different price tables in the proxy vs litellm),
  so the meter-independent, robust result is the **token reduction**; the cost direction
  is consistently ON ≤ OFF.

---

## 3. Compression mechanism — where the savings come from (complete n=50, all 5 batches)

Request-level: across 2,395 ON turns the proxy shrank the compressible payload
**73.1 MB → 49.6 MB = 32.2% reduction.** Mechanism attribution from 9,074 compression
events:

| Mechanism | Events | Per-event saving | Share of bytes saved |
|-----------|--------|------------------|----------------------|
| **stash** | 1,712 | 98.1% | **94.0%** |
| logs | 4,538 | 11.1% | 5.7% |
| code | 2,824 | 0.4% | 0.3% (effective no-op) |

**Stash (replacing large stale message bodies with markers) does essentially all the
work.** The per-type `code` compressor is a no-op at this content mix; budget-based
message dropping never fired (threshold never hit).

> **Scoping note:** this breakdown is the full Gate 2.3 run (all 5 batches, correct UTC
> window). An earlier draft caveated it to "batches 1–3 only" on the belief that the live
> proxy hadn't checkpointed batches 4–5 — that was a **timezone error** (batch folder
> names are IST; DB timestamps are UTC). All 5 batches were in the DB the whole time;
> UTC-windowed turn counts match the trajectories exactly. A separate earlier
> "55.3% / 88.1 MB" figure was wrong-scoped (it summed Gate 2.2 + pre-batch traffic too).

---

## 4. Per-repo turn delta (ON − OFF, n=50)

| Repo | ON turns | OFF turns | Δ (ON−OFF) |
|------|----------|-----------|------------|
| django | 496 | 446 | **+50** |
| matplotlib | 221 | 187 | +34 |
| pylint | 285 | 259 | +26 |
| scikit-learn | 115 | 90 | +25 |
| sphinx | 330 | 309 | +21 |
| pytest | 102 | 86 | +16 |
| astropy | 166 | 153 | +13 |
| sympy | 310 | 303 | +7 |
| seaborn | 38 | 36 | +2 |
| xarray | 179 | 185 | **−6** |

ON > OFF in **9 of 10 repos** — broad, not driven by any single repo.

---

## 5. The extra turns are signal, not noise

ON > OFF turns in **every batch** and **every rep**:

| Split | ON | OFF | Δ |
|-------|-----|------|---|
| batch 1 | 506 | 456 | +50 |
| batch 2 | 455 | 406 | +49 |
| batch 3 | 424 | 404 | +20 |
| batch 4 | 453 | 392 | +61 |
| batch 5 | 404 | 396 | +8 |
| rep 1 | 731 | 641 | +90 |
| rep 2 | 756 | 720 | +36 |
| rep 3 | 755 | 693 | +62 |

This is **systematic and reproducible**. (An earlier note claiming the asymmetry
"reversed by batch / looked like noise" was an artifact of slicing the observer DB with
IST folder times against its UTC timestamps, which clipped batches 4–5. Once windowed
correctly in UTC, DB turn counts match the trajectories exactly and the trajectory data
is unambiguous: compression consistently lets the agent run longer.)

---

## 6. The three-order hypothesis, judged

1. **Compression works (1st order)** — ✅ **Confirmed.** Heavier ON payloads are
   compressed back below the OFF arm at the model boundary (4,662 vs 5,473 input
   tokens/turn); `stash` does ~94% of it.
2. **Token / cost savings (2nd order)** — ✅ **Confirmed.** Despite running +9.2% more
   turns, ON sends **−14.8% input tokens/turn and −9.1% total input tokens** than OFF at
   the model, and **costs less in both meters** (−6.8% proxy / −1.0% trajectory).
   Compression nets a real model-side efficiency gain — not just a proxy-layer byte count.
3. **Freed context → more turns → better reasoning (3rd order)** — 🟡 **Half true.** The
   mechanism is real and reproducible: compression keeps the model payload small, so the
   agent runs longer before hitting limits. **But those extra turns bought 0 extra
   resolutions (31 vs 32).** Capacity went up; productivity stayed flat.

**One-liner:** compression buys you a smaller, cheaper, longer-running agent at the same
success rate. The "freed context helps it reason" story has a real mechanism but no
measured payoff at this sample size.

---

## Known limitations

1. **Trials < 150/arm** (141 ON, 142 OFF): occasional harness-side evaluation failures,
   unrelated to compression.
2. **Cost meters disagree on absolute magnitude.** The proxy DB prices the run at
   ~$23–25; litellm's trajectory `instance_cost` at ~$7.60–7.68 (~3× apart, different
   price tables for gpt-5.3-codex). Both agree directionally (ON ≤ OFF); the
   meter-independent result is the token reduction. The DB cost also includes patch-retry
   turns (OFF had more of them), which the trajectory cost excludes.
3. **Single model**: results are specific to gpt-5.3-codex.

(All 5 batches are captured in the observer DB — verified by UTC-windowed turn counts
matching the trajectories exactly for batches 3/4/5. There is no data-coverage gap.)

---

## Conclusion

The observer compression proxy is **non-inferior on SWE-bench Verified resolve rate**
(−0.5pp, 0.14 SE), **modestly cheaper at the model boundary** (−9.1% model-side input
tokens; cost lower in both meters), and makes the agent **systematically more
exploratory** (+9.2% turns, 9/10 repos). It is **safe to enable**: it cuts model-side
tokens, cost, and footprint at no resolve-rate cost. It does **not**, in this sample,
convert the freed context into additional resolved instances.

**Gate 2.3: CLOSED — PASS (non-inferior, modest token/cost saving).**
