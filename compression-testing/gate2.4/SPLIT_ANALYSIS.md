# Gate 2.4 — Compression Performance Split: Resolved vs Unresolved

**Date**: 2026-06-25
**Scope**: Gate 2.4 run `gate2_4_swe_20260625_015519` (300 runs: 50 instances × 2 arms × 3 reps).
**Question**: Does compression behave differently on the problems the agent *solves* versus
the ones it *fails*? Are the token/cost effects concentrated in either bucket?

> **Companion to** `VERDICT.md`. The verdict reports pooled deltas; this doc decomposes them by
> outcome. All model-side numbers come from the proxy DBs (`observer-{off,on}-2.4.db`,
> `api_turns`), the post-compression vantage that the proxy actually bills.

---

## Method & attribution

The proxy is stateless w.r.t. SWE-agent instances (`api_turns.session_id` is NULL), so per-turn
rows carry no instance label. We reconstruct the link by **time-bucketing**: within each arm the
50×3 runs execute strictly sequentially, so each proxy turn is assigned to the run whose
trajectory finished next (epoch-time bisect on `.traj` mtimes).

**Validation (this is what makes the split trustworthy, and defuses the IST/UTC timezone trap that
bit Gate 2.3):** the bucketed per-run turn counts are checked against each run's *independently
recorded* `info.model_stats.api_calls` from its `.traj`:

| arm | Σ bucketed turns | Σ api_calls | per-run \|Δturns\| ≤ 1 | surplus explained by |
|-----|------------------|-------------|------------------------|----------------------|
| OFF | 2000 | 1988 | 149 / 150 | ~12 warmup/preflight turns on instance #1 |
| ON  | 2283 | 2232 | 146 / 150 | warmup + 3 retried instances' 1st-attempt turns |

Because epoch timestamps are timezone-independent, and the per-run counts line up with an
orthogonal source, the bucketing is sound. Bucketed totals also conserve exactly to the pooled
figures in the verdict (post-input OFF 1,508,057 / ON 1,381,143; cache OFF 10,064,512 /
ON 9,180,672; output OFF 221,477 / ON 247,978).

**Outcome label**: each run (instance × rep × arm) is tagged *resolved* / *unresolved* from the
harness `resolved_ids`. Resolved runs: **31 / 150 in each arm** (the dead-heat from the verdict).
Buckets are per-run, so the resolved-ON set and resolved-OFF set are not identical instances — see
the composition caveat below.

---

## 1. Compression yield is resolution-neutral (within ON)

The cleanest cut — same arm, same vantage, just split by outcome. Compression bytes are an
ON-only quantity (`compression_original_bytes` → `compression_compressed_bytes`).

| ON bucket | runs | turns | original | compressed | **reduction** |
|-----------|------|-------|----------|------------|---------------|
| resolved   | 31  | 454  | 13.86 MB | 9.37 MB  | **32.4 %** |
| unresolved | 119 | 1829 | 57.60 MB | 37.10 MB | **35.6 %** |

Compression works on solved and unsolved problems alike — the reduction differs by only ~3 pp.
It is *slightly richer on unresolved* runs, which is mechanically expected: unsolved runs spin
longer, accumulating more of the highly-compressible bulk (git-stash snapshots, command logs) that
the compressor targets. There is **no outcome for which compression "switches off."**

---

## 2. Model-side input/turn: the saving is real in both buckets

Post-compression input per turn (what the model actually ingests), ON vs OFF, within each bucket:

| bucket | post-in/turn ON | post-in/turn OFF | **Δ input/turn** | cache/turn ON | cache/turn OFF | Δ cache/turn |
|--------|-----------------|------------------|------------------|---------------|----------------|--------------|
| resolved   | 656 | 776 | **−15.5 %** | 3,950 | 5,890 | −32.9 % |
| unresolved | 592 | 748 | **−20.8 %** | 4,039 | 4,791 | −15.7 % |

Compression lightens the model-side input footprint in **both** buckets (−16 % to −21 % per turn).
The effect is somewhat larger on unresolved runs, consistent with §1.

> **Composition caveat.** Resolved-ON and resolved-OFF are not the same 31 instances, so this ON-vs-OFF
> column compares slightly different instance mixes. The *within-ON* split (§1) and the *direction/sign*
> here are robust; treat the exact ON-vs-OFF percentages per bucket as indicative, not paired.

---

## 3. Agent-side (pre-compression) is where unresolved runs blow up

The trajectory `model_stats` records the **agent-side** count — the full, uncompressed context the
agent assembles each turn, *before* the proxy compresses it. This is the opposite vantage to §2 and
explains why compression has something to chew on:

| bucket | sent/run ON | sent/run OFF | Δ sent | recv/run ON | recv/run OFF | Δ recv | calls/run ON | calls/run OFF |
|--------|-------------|--------------|--------|-------------|--------------|--------|--------------|---------------|
| resolved   | 93,558  | 86,038 | +8.7 %  | 347 | 419 | −17.3 % | 14.6 | 14.2 |
| unresolved | 101,646 | 64,911 | **+56.6 %** | 424 | 430 | −1.4 % | 14.9 | 13.0 |

On **unresolved** problems the ON agent assembles **+56.6 %** more raw context than OFF — it
explores harder and accumulates more history before giving up. Compression is precisely what keeps
that explosion from reaching the model (§2 shows the same unresolved bucket is −20.8 % at the model
boundary). On **resolved** problems the agent-side inflation is mild (+8.7 %) and ON even emits
−17.3 % fewer output tokens — it reaches the fix a little more tersely.

---

## 4. Read-through to cost

Cost is dominated by output ($14/1M, 8× input; ~41 % of spend) and by the fact that the bulk of
input is *cached* (10× cheaper). The split shows **why pooled cost is neutral**:

- The big compression wins land on **cached input** (cache/turn down 16–33 %) and on **unresolved**
  runs — both of which are cheap or non-billable upside.
- On **resolved** runs, where money is well spent, ON's input saving is partly given back by run
  length, but output is *lower* (−17 %), so resolved-ON runs are modestly cheaper
  (litellm meter: 0.0487 vs 0.0555 / run).
- On **unresolved** runs the litellm meter is ~flat (0.0491 ON vs 0.0485 OFF): the enormous
  agent-side inflation is absorbed by compression + cache, netting to no cost change.

Net: compression's footprint savings are concentrated exactly where they convert least to dollars
(cached tokens, unsolved runs), which is the mechanical reason Gate 2.4's headline cost delta is
−0.1 %.

---

## 5. Bottom line

1. **Compression is resolution-neutral** — it fires, and fires hard (32–36 % byte reduction), on
   both solved and unsolved problems. Shipping it will not selectively starve the cases the agent
   can solve.
2. **Its largest absolute effect is on unresolved runs**, where it tames a +56 % agent-side context
   explosion down to a −21 % model-side input — a real safety rail against runaway context.
3. **None of this moves cost**, because the saved tokens are predominantly cached (10× cheaper) and
   the resolved bucket — where spend matters — sees only a modest net win, offset by run length.

This is the verdict's "safe to ship on footprint, cost-neutral on this model/workload" finding,
now shown to hold *within each outcome bucket*, not just on average.

---

### Reproduction

- Split/bucketing scripts: `g24_split.py`, `g24_bucket.py`, `g24_report.py` (epoch-bisect attribution
  + `api_calls` validation).
- Sources: `runs/gate2_4_swe_20260625_015519/rep_*/{on,off}/*/*.traj` (agent-side),
  `observer-{off,on}-2.4.db` `api_turns` (model-side, checkpointed copy).
- Outcome labels: `runs/.../state.json` → `harness[*].resolved_ids`.
