# Gate 2.1 Verdict — Exploratory SWE-agent n=25 Run

**Status:** ✅ CLOSED — PASS  
**Run date:** 2026-06-20 to 2026-06-22  
**Verdict computed:** 2026-06-22

> **Scope note:** This run was exploratory infrastructure validation using 21 astropy + 4 django instances
> (not the pre-registered `pilot_subset_verified_multifile_n50.txt` cohort). Harness was SWE-agent 1.1.0,
> not Agentless (original pre-reg). Results are valid for compression signal detection but are NOT
> the pre-registered Gate 2 study. That study runs as **Gate 2.2** with a balanced 50-instance cohort.

---

## Run configuration

| Parameter | Value |
|-----------|-------|
| Agent | SWE-agent 1.1.0 |
| Model | gpt-5.3-codex via Azure (32k output tokens) |
| Arms | OFF (port 8831) / ON-FULL (port 8832) |
| n | 25 instances × 2 arms = 50 trajectories |
| Cohort | 21 astropy + 4 django (exploratory, not pre-registered) |
| Observer DBs | `observer-off.db` (turns id 1000–1290) / `observer-on.db` (turns id 448–755) |
| Patches combined | `runs/n25_combined/predictions_off.jsonl`, `predictions_on.jsonl` |

---

## §A. Primary metrics

| Metric | OFF | ON | Delta | Threshold | Gate |
|--------|-----|----|-------|-----------|------|
| Resolve rate | 10/25 (40%) | 10/25 (40%) | **0pp** | ≥ −3pp | ✅ PASS |
| Input token savings | 1,521,673 tokens | 1,325,101 tokens | **−12.9%** | ≥ 10% | ✅ PASS |
| Cost per completed | $0.311/resolved | $0.278/resolved | **−10.6%** | ON ≤ OFF | ✅ PASS |
| Byte savings | — | 40.2% saved | — | ≥ 20% | ✅ PASS |

**Verdict: PASS** — all primary criteria met.

---

## §B. Secondary thresholds

| Threshold | Result | Gate |
|-----------|--------|------|
| Cache-read tokens preserved ≥ 90% | N/A — SWE-agent doesn't use prompt caching | — |
| Compression errors = 0 | 0 errors observed | ✅ |
| Turn count delta < 10% | 308 ON vs 291 OFF = **+5.8%** | ✅ PASS |
| ≥ 80% instances show byte savings | ~100% (stash fired avg 10.8×/instance) | ✅ PASS |

---

## §C. Layer-level compression breakdown (ON arm)

| Layer | Mechanism | Events | Original bytes | Saved bytes | Efficiency |
|-------|-----------|--------|---------------|-------------|------------|
| L3 | **Stash** | 269 | 3,569,800 | **3,503,988** | 98.2% |
| L1 | **Logs** | 440 | 1,452,336 | **370,669** | 25.5% |
| L1 | **Code** | 318 | 2,122,885 | **8,359** | 0.4% |
| L2 | Budget-drop | 0 | — | — | N/A |
| **Total** | | **1,028** | **9,767,820** | **3,923,316** | **40.2%** |

**Share of total savings:**
- Stash (L3): 89.3%
- Logs compressor (L1): 9.4%
- Code compressor (L1): 0.2%
- Budget-drop (L2): 0% (never triggered — SWE-agent contexts within budget threshold)

**Key finding:** Stash is the dominant compression mechanism for SWE-agent. Logs compressor provides meaningful secondary savings. Code compressor provides negligible savings (SWE-agent patches are unique; deduplication doesn't help). Budget-based message dropping never triggered — SWE-agent conversations stay within threshold even in longer runs.

---

## §D. Cost summary

| | OFF | ON |
|--|-----|----|
| API turns | 291 | 308 |
| Input tokens | 1,521,673 | 1,325,101 |
| Output tokens | 31,636 | 32,691 |
| Total cost (USD) | $3.11 | $2.78 |
| Cost per resolved | $0.311 | $0.278 |

**Net savings with compression ON: $0.33 (10.6% cheaper) for equal resolve rate.**

---

## §E. Paired discordance (resolution)

| | | OFF resolves | OFF doesn't |
|--|--|--|--|
| **ON resolves** | | 9 | 1 |
| **ON doesn't** | | 1 | 14 |

9 instances resolved by both; 1 instance resolved only by OFF (astropy-14539);
1 instance resolved only by ON (astropy-13579). Net discordance: 0. Within noise.

---

## §F. Infrastructure fixes discovered during this run

1. **`git clean -fdxq` before checkout** — SWE-agent's `get_reset_commands()` returns individual strings,
   not `&&`-joined strings. Monkey-patch inserts `"git clean -fdxq"` as separate list element before `"git checkout"`.
2. **Reset timeout 120s → 600s** — Django checkout (5,296 files over NTFS-mounted WSL2) takes 3–5 min.
3. **CRLF in shell scripts** — all WSL scripts use inline here-strings to avoid Windows CRLF.
4. **Missing astropy commits** — 4 commits not in local clone; fetched via `git fetch origin <sha>`.

---

## §G. Transition to Gate 2.2

This run served its purpose: validating the Observer+SWE-agent pipeline end-to-end.
The pre-registered cohort study runs as **Gate 2.2** with:
- Balanced 50-instance cohort (`gate2_2_subset_balanced_n50.txt`)
- 10 repos, django capped at 11, 98% multi-file patches
- Same infrastructure (SWE-agent 1.1.0, same proxy ports, same DB schema)
- Cohort file: `E:\swe-bench-3slot\artifacts\gate2_2_subset_balanced_n50.txt`
