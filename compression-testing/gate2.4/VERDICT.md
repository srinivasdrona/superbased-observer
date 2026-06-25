# Gate 2.4 Verdict — Cache-Aware Compression Economics on SWE-bench Verified

**Date**: 2026-06-25
**Design**: 50 instances × 2 arms (ON/OFF compression) × 3 reps = 300 runs (150 per arm)
**Model**: azure__gpt-5.3-codex via proxy (ON: port 8842 / OFF: port 8841)
**Binary**: `bin/observer-2.4.exe`, clean build of committed `439ff1b` (cache-capture fix)
**Dataset**: SWE-bench Verified (princeton-nlp/SWE-bench_Verified)

> **What Gate 2.4 adds over Gate 2.3.** Same cohort, same base commits, same retries/reps/
> cost-limits (verified AST-identical to the 2.3 runner; only ports, output dirs, and the
> cache fix differ). The one new capability: the proxy now captures `cache_read_tokens` from
> the non-streaming Azure Responses API (`input_tokens_details.cached_tokens`), which Gate 2.3
> silently dropped (a parser bug — see 2.3 VERDICT Limitation #4, since corrected). Gate 2.4
> therefore produces the **first cache-aware token and cost accounting** for the compression
> A/B. Gate 2.3 stays frozen; 2.4 ran on the latest repo (`439ff1b`).

---

## Headline

Compression is **resolution-neutral and token-lighter (−8.7% input tokens), but — once cached
tokens are priced correctly — it is COST-NEUTRAL (−0.1%), not the −6.8% Gate 2.3 reported.**
The earlier cost edge was an artifact of cache-blind billing. With real cache pricing, the
input-token saving collapses (cached tokens are 10× cheaper, so saving them barely matters) and
is fully offset by the ON arm's +12% higher output, which is the dominant cost driver. Compression
is **safe to ship on resolution and footprint, but it does not save money on this model/workload.**

---

## 1. Primary result — resolve rate (harness)

Resolved / completed, summed over 3 reps (same convention as Gate 2.3 = resolved ÷ harness-completed):

| Batch | ON resolved | ON done | OFF resolved | OFF done |
|-------|-------------|---------|--------------|----------|
| 1 | 3 | 25 | 3 | 24 |
| 2 | 5 | 28 | 6 | 30 |
| 3 | 2 | 30 | 4 | 30 |
| 4 | 17 | 27 | 13 | 27 |
| 5 | 4 | 30 | 5 | 30 |
| **Total** | **31** | **140** | **31** | **141** |

- **resolved ÷ done:** ON **22.1%** (31/140) vs OFF **22.0%** (31/141) → delta **+0.1pp**.
- **resolved ÷ submitted (50×3=150):** both **20.7%** (31/150) → delta **0.0pp**.

Either denominator: **statistically indistinguishable from zero** (SE ≈ 3.5pp). This reproduces
Gate 2.3's ~22% resolve rate on a fresh repo build — a clean consistency check.

**VERDICT: PASS — non-inferior, dead-heat (Δ ≤ 0.1pp).**

---

## 2. Tokens — cache-aware, complete n=50 (observer DBs, all turns both arms)

| Metric | OFF | ON | Δ (ON vs OFF) |
|--------|-----|-----|---------------|
| input tokens (non-cached) | 1,508,057 | 1,381,143 | **−8.4%** |
| cache_read tokens | 10,064,512 | 9,180,672 | **−8.8%** |
| **total input (non-cached + cache)** | **11,572,569** | **10,561,815** | **−8.7%** |
| output tokens | 221,477 | 247,978 | **+12.0%** |
| all tokens (in + cache + out) | 11,794,046 | 10,809,793 | **−8.3%** |

**Cache is now captured** — the Gate 2.3 zero is gone. ~93% of turns log a cache read; cached
tokens are **~6.6× the non-cached input** (a long, stable agent prefix re-read every turn), so
they dominate the token mix. The input-token reduction (−8.7%) reproduces Gate 2.3's −9.1% — the
compression mechanism is unchanged. **But output moved the opposite way: ON emits +12.0% more
output tokens.** This sets up the cost result in §4.

> Minor caveat: these whole-DB sums include a handful (~6/arm) of warmup/pre-flight turns
> (~0.4% of tokens, near-symmetric across arms); they do not move any delta materially.

---

## 3. Compression mechanism — where the bytes go (ON arm, 9,814 events)

Compressible payload **59.8 MB → 34.9 MB = 41.7% reduction.** Attribution:

| Mechanism | Events | Per-event saving | Share of bytes saved |
|-----------|--------|------------------|----------------------|
| **stash** | 1,674 | 98.2% | **91.1%** |
| logs | 4,861 | 14.0% | 8.6% |
| code | 3,279 | 0.4% | 0.3% (effective no-op) |

**Stash (replacing large stale message bodies with markers) does ~91% of the work** — identical
to Gate 2.3. The byte-level compression is real and substantial; the question is whether it
translates to dollars (§4: it does not).

> **Split by outcome** — see [`SPLIT_ANALYSIS.md`](SPLIT_ANALYSIS.md). Compression is
> resolution-neutral (32.4% reduction on resolved runs vs 35.6% on unresolved) and its largest
> absolute effect lands on **unresolved** runs, where it tames a +56.6% agent-side context
> explosion down to −20.8% at the model boundary. Per-instance attribution via validated
> epoch-bucketing of the proxy DB (149/150 OFF, 146/150 ON runs match `api_calls` within ±1).

---

## 4. Cost — the central Gate 2.4 finding

**Pricing** (OpenAI official, gpt-5.3-codex Standard, per 1M tokens):
input **$1.75**, cached input **$0.175** (= 10% of input), output **$14.00** (= 8× input).

| Cost component | OFF | ON |
|----------------|-----|-----|
| input (non-cached) | $2.639 | $2.417 |
| cache_read | $1.761 | $1.607 |
| output | $3.101 | $3.472 |
| **TOTAL** | **$7.501** | **$7.495** |

**ON vs OFF cache-aware cost: −0.1% — cost-neutral.**

**Why the Gate 2.3 "−6.8% cheaper" disappears.** Billing these *same* 2.4 tokens the 2.3 way
(cached tokens charged at the full input rate, because 2.3 couldn't see them) reproduces a
**−6.0%** edge. The entire advantage was an accounting artifact. Once cached tokens are priced
at their true 10% rate, two things kill the saving:

1. **Cached tokens are 10× cheaper**, and they are the bulk of input (9–10M vs 1.4M non-cached).
   Compression saving 8.8% of a near-free bucket barely moves dollars.
2. **Output is 41% of total cost** ($14/1M, the most expensive token class) and the ON arm
   generates **+12% more of it** — wiping out the input-side saving entirely.

**Cost per resolved instance** (total ÷ 31 each): ON $0.2418 vs OFF $0.2420 — a wash, as expected
from a 0.1% cost delta over equal resolves.

---

## 5. The three-order hypothesis, judged (cache-aware)

| Order | Claim | Verdict |
|-------|-------|---------|
| **1st — compression works** | payload shrinks at the model boundary | ✅ **Yes** — 59.8 MB → 34.9 MB (41.7%); −8.7% total input tokens; `stash` does ~91% |
| **2nd — it saves tokens** | fewer tokens to the model | ✅ **Yes (tokens)** — −8.4% non-cached input, −8.8% cache, −8.7% total input, −8.3% all tokens |
| **3rd — it saves cost** | fewer dollars | ❌ **No (cost-neutral, −0.1%)** — cached tokens are 10× cheaper so input savings barely price in, and ON's +12% output (output = 41% of spend) cancels the rest |

**One-liner:** compression reliably shrinks the payload and the token count without costing any
resolutions — but on a cache-heavy, output-expensive model like gpt-5.3-codex, the token saving
**does not convert to a cost saving**. The "compression saves money" claim from Gate 2.3 was an
artifact of being blind to cache pricing.

---

## 6. Operational notes

- **Run health:** 300/300 runs produced a non-empty patch (0 empty); only **3 multi-attempt**
  runs (all ON, batch-1/2 shakeout: astropy-14369, matplotlib-25479, sympy-20438), each
  resolved on attempt 2. Far cleaner than 2.3's batch-1 retry storm.
- **Cache capture validated end-to-end** before launch: isolated live-probe + both 2.4 DBs
  showed warm `resp_` turns logging `cache_read_tokens=7808`; pre-flight guard confirmed caching
  active on both arms (cached=7808) before any batch.

---

## Known limitations

1. **Single model / single cache regime.** The cost verdict is specific to gpt-5.3-codex's price
   structure (cached input = 10% of input; output = 8× input) and to this workload's cache-heavy
   mix (cache ≈ 6.6× non-cached input). On a model with a smaller cache discount, less output, or
   a workload with a thinner cached prefix, compression's input saving could price in more.
2. **Output increase is real but unexplained at the token level.** ON emits +12% more output; in
   2.3 the extra output tracked +6.8% more turns (per-turn output was flat). The 2.4 totals are
   consistent with the same "more turns, flat per-turn" mechanism, but a per-turn decomposition
   was not re-run here (whole-DB turn counts include warmup turns). The direction — ON ≥ OFF
   output — is robust and is what drives the cost result.
3. **Resolve rate is at the noise floor** (~22%, SE ≈ 3.5pp): the run proves non-inferiority, not
   a precise resolution delta.
4. **Repo state differs from 2.3** (2.4 = `439ff1b`, latest; 2.3 = dirty `3c78587`, 17-June). The
   ~22% resolve match across both builds is reassuring, but the binaries are not byte-identical
   (2.4 includes later compression commits `3f5d7f3`, `6190f3b`). Accepted by design.

---

## Conclusion

Gate 2.4 confirms compression is **resolution-neutral** on SWE-bench Verified (Δ ≤ 0.1pp, a
dead heat at 31 resolved per arm) and **token-lighter** (−8.7% total input, 41.7% byte
compression, `stash` doing ~91% of it) — fully consistent with frozen Gate 2.3. Its new and
decisive contribution is the **cache-aware cost**: with cached tokens correctly priced at 10% of
input and output (the dominant cost class) running +12% higher under compression, the cost delta
is **−0.1% — neutral, not the −6.8% Gate 2.3 reported.** That earlier figure was a cache-blind
billing artifact, now corrected.

**Bottom line:** compression is safe to enable for **footprint and token-budget** reasons (smaller
payloads, −8.7% input tokens, no resolution cost), but it should **not** be sold as a cost
reduction on gpt-5.3-codex — on this model the dollars are a wash.

**Gate 2.4: CLOSED — PASS on resolution & tokens; cost-NEUTRAL (cache-aware).**
