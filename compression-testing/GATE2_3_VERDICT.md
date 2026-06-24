# Gate 2.3 Verdict — Compression Non-Inferiority on SWE-bench Verified

**Date**: 2026-06-24 (revised — corrected token/cost scoping and turn analysis)
**Design**: 50 instances × 2 arms (ON/OFF compression) × 3 reps = 300 runs (150 per arm)
**Model**: azure__gpt-5.3-codex via proxy (ON: port 8832 / OFF: port 8831)
**Dataset**: SWE-bench Verified (princeton-nlp/SWE-bench_Verified)

---

## Headline

Compression is **non-inferior on resolve rate, cost-neutral, and systematically more
exploratory — but the extra exploration produced zero extra fixes.** Safe to ship for
footprint / context-headroom reasons. No measured problem-solving benefit from "freed
context" in this sample.

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

## 2. Tokens, turns, cost (n=50, from trajectory `model_stats`, all 300 runs, 0 missing)

| Metric | ON | OFF | Δ |
|--------|-----|------|---|
| runs | 150 | 150 | — |
| turns (api_calls) | 2,242 | 2,054 | **+9.2%** |
| context built / turn (agent-side) | 6,510 | 4,908 | +32.7% |
| **model-billed cost** | **$7.60** | **$7.68** | **−1.0% (tie)** |

Two vantage points matter and they tell different stories:

- **Agent-side (pre-compression)** = what SWE-agent assembles locally. The proxy is
  transparent, so the agent stores full history in *both* arms. ON's trajectories are
  heavier per turn (6,510 vs 4,908).
- **Model-billed (post-compression)** = what Azure actually charges. This is the
  `instance_cost` above. Here the arms are a **tie ($7.60 vs $7.68)**.

The proxy compresses ON's heavier payload back down before the model sees it, so the
extra agent-side context does **not** flow through to cost. But the freed headroom is
spent on **+9.2% more turns**, which nets cost back to flat.

---

## 3. Compression mechanism — where the savings come from (proxy-side, batches 1–3, 90 ON runs)

| Mechanism | Events | Per-event saving | Share of all savings |
|-----------|--------|------------------|----------------------|
| **stash** | 408 | 98.1% | **93.5%** |
| logs | 1,688 | 9.1% | 6.1% |
| code | 998 | 0.4% | 0.4% (effective no-op) |

**Stash (replacing large stale message bodies with markers) does essentially all the
work.** The per-type `code` compressor is a no-op at this content mix; budget-based
message dropping never fired (threshold never hit).

> **Correction to prior draft:** the earlier "55.3% byte savings / 88.1 MB / 13,053
> events" figure was **wrong-scoped** — it summed the entire observer DB, which also
> contains Gate 2.2 and pre-batch turns back to 06-22. Properly scoped to Gate 2.3, the
> mechanism breakdown is above. Byte savings are a **proxy-layer** metric and do **not**
> equal token/cost savings (see §2).

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
"reversed by batch / looked like noise" was an artifact of corrupted observer-DB turn
counts — 2× undercount plus time-window clipping. Clean trajectory data is unambiguous:
compression consistently lets the agent run longer.)

---

## 6. The three-order hypothesis, judged

1. **Compression works (1st order)** — ✅ **Confirmed.** Heavier ON payloads are
   compressed back below the OFF arm at the model boundary; `stash` does ~93% of it.
2. **Token / cost savings (2nd order)** — ⚠️ **Neutral.** Per-turn savings are real, but
   the freed headroom is spent on +9.2% more turns → **net model-billed cost is flat.**
   Byte savings ≠ cost savings.
3. **Freed context → more turns → better reasoning (3rd order)** — 🟡 **Half true.** The
   mechanism is real and reproducible: compression keeps the model payload small, so the
   agent runs longer before hitting limits. **But those extra turns bought 0 extra
   resolutions (31 vs 32).** Capacity went up; productivity stayed flat.

**One-liner:** compression buys you a smaller, longer-running agent at the same price and
the same success rate. The "freed context helps it reason" story has a real mechanism but
no measured payoff at this sample size.

---

## Known limitations

1. **Trials < 150/arm** (141 ON, 142 OFF): occasional harness-side evaluation failures,
   unrelated to compression.
2. **Compression mechanism breakdown is on batches 1–3** (90 ON runs): the live proxy
   held an uncheckpointed WAL for batches 4–5, so per-event compression rows past
   06-24 11:52 were not readable without stopping the proxy. Mechanism *ratios* are
   content-driven and stable across batches; resolve rate, tokens, turns, and cost are
   full n=50 (from trajectory files, not the DB).
3. **Single model**: results are specific to gpt-5.3-codex.

---

## Conclusion

The observer compression proxy is **non-inferior on SWE-bench Verified resolve rate**
(−0.5pp, 0.14 SE), **cost-neutral** at the model boundary ($7.60 vs $7.68), and makes the
agent **systematically more exploratory** (+9.2% turns, 9/10 repos). It is **safe to
enable** for context-headroom and footprint reasons. It does **not**, in this sample,
convert freed context into additional resolved instances.

**Gate 2.3: CLOSED — PASS (non-inferior, cost-neutral).**
