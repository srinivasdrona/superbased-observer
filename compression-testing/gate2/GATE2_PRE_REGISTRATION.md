# Pre-registration: Observer Compression on SWE-bench Verified (Gate 2)

**Status**: **DRAFT** — awaiting operator freeze after §0 lock + all §10 build tasks GREEN  
**Drafted at**: 2026-06-19 IST  
**Author**: sdrona (drafted by Copilot)

This is the first test of whether **API request compression** (via the Observer proxy) saves meaningful tokens/cost **without degrading agent accuracy** on a multi-turn, tool-intensive coding benchmark. This tests a production middleware component designed for deployment in long-running Copilot sessions.

**Context from Gate 1 (GSM8K):** Observer compression achieved **28.2% byte savings** with **0 measurable accuracy impact** on a math benchmark (n=100, 8 target_ratio levels). Gate 2 stress-tests compression on a **real multi-turn coding workflow** with large tool outputs (Read, Edit, Bash, Grep) — the scenario compression was designed for.

---

## §0. Open decisions requiring operator lock before freeze

| # | Decision | Recommended default | Locked? |
|---|---|---|---|
| 0.1 | **Test set** | 50 curated instances (`pilot_subset_verified_multifile_n50.txt`) | ✅ **LOCKED 2026-06-19** |
| 0.2 | **Model** | `gpt-5.3-codex` via Azure | ✅ **LOCKED 2026-06-19** |
| 0.3 | **Harness** | Agentless (Gate 2.1); defer alternate harness to Gate 2.2 | ✅ **LOCKED 2026-06-19** |
| 0.4 | **Compression config** | Default mode (no target_ratio override); let observer decide | ✅ **LOCKED 2026-06-19** |
| 0.5 | **Retries** | 5 retries per repair phase | ✅ **LOCKED 2026-06-19** |
| 0.6 | **Minimum token savings (MESOI)** | 10% input tokens saved | ✅ **LOCKED 2026-06-19** |

**All §0 parameters are locked (2026-06-19).** Pending: observer build + config review + pre-flight smoke test (§10).

---

## §1. Hypotheses

**H1 (primary, cost)**: The Observer compression pipeline (per-type compressors + budget-based message dropping + stash) saves **≥10% input tokens** on Agentless's SWE-bench Verified workflow, measured by comparing Arm ON (compression enabled) vs Arm OFF (compression disabled) on byte-identical problem statements.

**H2 (primary, accuracy)**: Compression does NOT degrade agent completion rate by more than **3 percentage points** (1.5 instances at n=50), measured by paired discordance.

**H3 (cost-effectiveness)**: Cost per completed instance is **NOT higher** under compression (i.e., token savings ≥ compression overhead).

**H1-null**: Compression saves <10% tokens, or degrades accuracy >3pp, or increases cost per completed. This is a **reportable negative result** for the compression pipeline's design claim.

**H4 (mechanism, secondary)**: Token savings concentrate in **later turns** of multi-turn instances (where tool outputs accumulate), not early turns. Required for design story, not for H1–H3.

---

## §2. Experimental design

- **Benchmark**: SWE-bench Verified (500 instances total)
- **Sample**: `pilot_subset_verified_multifile_n50.txt`, n=50, curated for:
  - Difficulty: mix of `<15min`, `15min-1hr`, `1-4hr` fix times
  - Edit scope: multi-file preferred (stresses cumulative context compression)
  - **Rationale**: Multi-file instances → longer conversations → more compression opportunities
- **Harness**: Agentless v1.5 fork (4-phase: file_level → related_level → edit_location → repair)
  - **Gate 2.1**: Agentless (this run)
  - **Gate 2.2** (future): Alternate harness TBD (e.g., Copilot CLI autopilot mode)
- **Model**: `gpt-5.3-codex` via Azure Responses API
  - All model parameters (deployment, API version, temperature, reasoning effort, max_tokens) defined in `gate2_run_config.yaml` (§model)
- **Arms** (Gate 2.1 baseline run: 2 arms; Gate 2.2+ ablation: 5 total arms):
  
  **Gate 2.1 (baseline, this run):**
  - **Arm OFF (baseline)**: Agentless routes API calls through Observer proxy on port 8831 with **compression disabled**
    - Config: `ab-off.toml` (`compression.conversation.enabled = false`)
    - observer.db: `observer-off.db`
    - Results dir: `~/swe-bench-3slot-work/gate2_off/`
  - **Arm ON-FULL (treatment)**: Same Agentless, routes through port 8832 with **all 3 compression layers enabled**
    - Config: `ab-on-full.toml` (all layers: per-type + budget-drop + stash)
    - observer.db: `observer-on-full.db`
    - Results dir: `~/swe-bench-3slot-work/gate2_on_full/`
  
  **Gate 2.2+ (ablation study, conditional on Gate 2.1 PASS):**
  - **Arm ON-L1**: Per-type compressors ONLY (json/code/logs/tools), no budget-drop, no stash
  - **Arm ON-L2**: Budget-based message dropping ONLY, no per-type, no stash
  - **Arm ON-L3**: Stash (CCR) ONLY, no per-type, no budget-drop
  
  **Purpose of ablation:** Isolate which layer(s) provide cost savings without accuracy harm. Publish compression-vs-accuracy curves showing each layer's contribution.
- **Compression config** (Arm ON-FULL only; Gate 2.1):
  - `mode = cache_aware` (preserve LLM cache boundaries)
  - `compress_types = ["json", "logs", "code", "tools"]` (all per-type compressors enabled)
  - `preserve_last_n = 5` (always keep last 5 messages uncompressed)
  - **NO target_ratio override** — let observer decide naturally based on content
  - **Three layers active** (baseline for ablation study):
    1. **Layer 1 (L1): Per-type compressors**
       - JSON compressor: schema skeleton only (drop array values, keep structure)
       - Code compressor: dedup + whitespace strip
       - Logs compressor: ANSI strip + dedup + head/tail (keep first 20 + last 20 lines)
       - Tools compressor: content-aware reduction (context-specific heuristics)
    2. **Layer 2 (L2): Budget-based message dropping**
       - Least-important messages dropped if per-type compression insufficient
       - Replaced with `[Message dropped for compression]` markers
       - Importance ranking: system < old user < old assistant < recent messages
    3. **Layer 3 (L3): Stash (Content-Chunking-Repository)**
       - Large bodies (>threshold) written to disk
       - Inline marker in request: `[Stashed: <id>]`
       - Never read back (write-only optimization)
- **Retries**: 5 retries per repair phase (defined in `gate2_run_config.yaml` > retries, stress-tests compression under retry pressure)
- **Seeds**: Single seed per arm (cost constraint); multi-seed deferred to confirmatory
- **Pairing**: Exact (same instance ID, same gold patch, same harness config, same model)
- **Identical-inputs constraint (LOAD-BEARING)**: Both arms receive **byte-identical** problem statements and repo contexts. The only difference is compression ON vs OFF.

---

## §3. Primary endpoints and decision thresholds

### §3.1 Unit of analysis

- **Primary unit**: the **instance** (n=50)
- **Completion** = Agentless finished all 4 phases and wrote `output_0_processed.jsonl`
- **Resolution** = SWE-bench harness `report.json.resolved == true` (all fail_to_pass tests pass, patch applied cleanly)
  - NOTE: Completion ≠ Resolution. An instance can complete but produce a wrong patch.
  - We measure **completion rate** first (did compression break the agent?), then resolution rate (did it fix the bug?)

### §3.2 Primary metrics

1. **Completion rate delta**: `(completed_ON / 50) - (completed_OFF / 50)`
2. **Input token savings**: `(tokens_OFF - tokens_ON) / tokens_OFF * 100`
3. **Cost per completed**: `total_cost_USD / n_completed` (per arm)
4. **Byte compression ratio**: `compressed_bytes / original_bytes` (Arm ON only)

### §3.3 Decision buckets (mutually exclusive, evaluated in order)

| Bucket | Condition | Action |
|--------|-----------|--------|
| **FAIL** | Completion rate delta < -3pp (≤ -2 instances) **OR** Cost-per-completed increases | Stop. Compression degrades accuracy or increases cost. Disable and redesign. |
| **PARTIAL** | Completion delta ≥ -3pp AND (token savings 5-10% OR cost-per-completed neutral OR byte savings 10-20%) | Acceptable with caveats. Consider tuning compression config before production. |
| **PASS** | Completion delta ≥ -3pp AND token savings ≥ 10% AND cost-per-completed ≤ baseline AND byte savings ≥ 20% | Proceed to production. Compression saves meaningful cost without harm. |

**NOTE:** "Completion rate" is used for the accuracy gate (not resolution rate) because compression can break the agent's ability to FINISH, independent of correctness. Resolution rate is reported as a secondary diagnostic.

### §3.4 Secondary thresholds (gating; must all hold for PASS)

1. **Cache-read tokens preserved**: `cache_read_tokens_ON ≥ 0.9 * cache_read_tokens_OFF`
   - Compression should NOT destroy LLM cache savings
2. **No compression errors**: `compression_errors = 0` (stash writes, parse failures)
3. **Turn count delta**: `|avg_turns_ON - avg_turns_OFF| < 10%`
   - If compression changes agent behavior (drops critical context → agent retries), turn counts diverge
   - Small divergence OK; large divergence = compression confusing the agent
4. **Byte savings consistency**: ≥40 instances (80%) show >0 byte savings
   - If compression only helps <half, it's too brittle

---

## §4. Secondary endpoints (diagnostic; not gating)

### §4.1 Per-layer instrumentation (Gate 2.1 primary; Gate 2.2+ ablation)

**Extract from `compression_events` JSON in `observer-on-full.db` (per turn):**
- **Layer 1 (per-type) breakdown:**
  - Bytes saved by JSON compressor
  - Bytes saved by code compressor
  - Bytes saved by logs compressor
  - Bytes saved by tools compressor
  - Compression events fired (count per type)
- **Layer 2 (budget-drop) breakdown:**
  - Messages dropped (count)
  - Bytes saved by dropping messages
  - Drop reasons (budget threshold vs importance ranking)
- **Layer 3 (stash) breakdown:**
  - Stash writes (count)
  - Bytes saved by stashing (total bytes offloaded to disk)
  - Stash read attempts (should be 0 — stash is write-only)

**Per-layer cumulative savings:**
```python
# From observer.db compression_events
total_savings = {
  'L1_json': sum(bytes_saved where mechanism='json_compressor'),
  'L1_code': sum(bytes_saved where mechanism='code_compressor'),
  'L1_logs': sum(bytes_saved where mechanism='logs_compressor'),
  'L1_tools': sum(bytes_saved where mechanism='tools_compressor'),
  'L2_drop': sum(bytes_saved where mechanism='message_drop'),
  'L3_stash': sum(bytes_saved where mechanism='stash'),
}
```

**Purpose:** 
- Identify which layer(s) provide the bulk of savings
- Gate 2.2+ ablation will test each layer independently to isolate contribution
- Final deliverable: **Compression-vs-Accuracy curve** (x=layer combination, y=accuracy delta, size=cost savings)

### §4.2 Resolution rate (gold standard)

Run SWE-bench harness on both arms' patches:
- `resolved_OFF / 50`
- `resolved_ON / 50`
- Paired discordance: `D_resolved = (B_only_resolved) - (A_only_resolved)`

**Purpose:** Completion rate is the accuracy gate (§3), but resolution rate is what operators care about. Reported separately.

### §4.3 Per-phase turn counts

From Agentless phase logs:
- Avg turns in `file_level` phase (OFF vs ON)
- Avg turns in `related_level` (OFF vs ON)
- Avg turns in `edit_location` (OFF vs ON)
- Avg turns in `repair` (OFF vs ON)

**Purpose:** If compression degrades one phase disproportionately, refine that phase's compression heuristics.

### §4.4 Token savings trajectory

Plot input token count per turn (cumulative) for both arms:
- Does savings increase linearly with turns (good — compression scales)?
- Or plateau early (bad — only helps short sessions)?

**Purpose:** Validates H4 (savings concentrate in later turns).

---

## §5. Verdict computation

**Computor**: `compression-testing/gate2/gate2_verdict.py`

**Inputs:**
- `~/swe-bench-3slot-work/observer-off.db` (Arm OFF API logs)
- `~/swe-bench-3slot-work/observer-on.db` (Arm ON API logs)
- `~/swe-bench-3slot-work/gate2_off/` (Arm OFF results)
- `~/swe-bench-3slot-work/gate2_on/` (Arm ON results)

**Outputs:**
- `gate2_verdict.json` (verdict bucket + all metrics)
- Stdout summary (readable report)

**Verdict logic:**
```python
if completion_delta < -0.03 or cost_per_completed_ON > cost_per_completed_OFF:
    verdict = "FAIL"
elif token_savings < 0.10 or byte_ratio > 0.80:
    verdict = "PARTIAL"
else:
    verdict = "PASS"
```

**Artifacts to commit:**
- `gate2_verdict.json`
- `gate2_diagnostic_memo.md` (if FAIL or PARTIAL, explain failure modes)

---

## §6. Integrity contract (leak prevention)

**Gate 2 tests middleware** (compression happens AFTER the agent generates its request, BEFORE it reaches the LLM). The leak risk here is **compression changing the semantic content** of the request, not leaking gold data into prompts.

### §6.1 Compression integrity checks

1. **No gold-patch injection**: Observer never sees gold patches (they're in SWE-bench harness, not API requests)
2. **No test-output injection**: Observer compresses model outputs (tool_results), not gold test outputs
3. **No hints from dropped messages**: Dropped messages are replaced with `[Message dropped for compression]` markers, not summaries
4. **Stash integrity**: Stashed bodies are write-only (never read back into compressed request)

### §6.2 Audit gates (run after each arm)

From `observer.db`:
1. `SELECT COUNT(*) FROM api_turns WHERE compression_errors > 0` → must be 0
2. `SELECT COUNT(*) FROM api_turns WHERE compression_original_bytes < compression_compressed_bytes` → must be 0 (no size increase)
3. Manual spot-check: inspect 5 random compressed requests, verify no semantic corruption

**Fail-closed:** If any audit fails, STOP and fix compression before continuing.

---

## §7. Operational details

### §7.1 Infrastructure

- **Observer proxy**: Built from `https://github.com/srinivasdrona/superbased-observer`
  - Version: commit SHA at freeze (TBD in §10)
  - Two instances: port 8831 (OFF), port 8832 (ON)
- **Working directory**: `~/swe-bench-3slot-work` (WSL)
- **Agentless**: Installed at `~/swe-bench-3slot-work/agentless/Agentless`
- **Docker testbeds**: Prebuilt from Phase 1/2 (reused)
- **Precompute cache**: `~/swe-bench-3slot-work/project_file_loc` (kills clone overhead)

### §7.2 Run orchestration

**Master script**: `compression-testing/gate2/run_gate2_full.sh`

**Sequence:**
1. Build observer (if not built)
2. Start both proxies (8831 OFF, 8832 ON)
3. Fire Arm OFF: `run_gate2_off.sh` (50 instances through port 8831)
4. Fire Arm ON: `run_gate2_on.sh` (50 instances through port 8832)
5. Stop proxies, move observer DBs to work dir
6. Compute verdict: `gate2_verdict.py`

**Expected wall time**: ~10-12 hours (50 instances × 2 arms × ~6-8 min/instance)

**Cost gating**: None (n=50 is small enough to fire all at once). If needed, can add rungs at 10→25→50.

### §7.3 Resumability

Both `run_gate2_off.sh` and `run_gate2_on.sh` skip instances that already have `repair_sample_1/output_0_processed.jsonl` (idempotent).

**To resume after crash:**
```bash
cd /mnt/e/superbased-observer/compression-testing/gate2
bash run_gate2_full.sh  # skips completed instances
```

---

## §8. Amendment protocol

**Before freeze:** Edits to this document are unrestricted.

**After freeze** (commit + first instance scored):
- §0–§6 (hypotheses, design, metrics, thresholds, arms, contract) are **locked**
- Amendments (e.g., gate fixes, config corrections) go in this section as dated entries:

### Amendment A1 — Harness changed from Agentless to SWE-agent
**Date:** 2026-06-20  
**Type:** infrastructure change  
**Rationale:** Agentless produces single-shot API calls with no multi-turn context accumulation — the exact scenario compression is NOT designed for. Multi-turn tool-intensive agents are required to stress the compression pipeline.  
**Change:** Harness replaced with SWE-agent 1.1.0. All other parameters (model, arms, proxy ports, observer DBs, compression config) unchanged.  
**Impact:** Minor — changes which agent generates the trajectory, not what compression sees. Results still directly test the Observer proxy on real coding tasks.

---

### Amendment A2 — Gate 2.1 cohort was exploratory (not pre-registered)
**Date:** 2026-06-22  
**Type:** scope clarification  
**Rationale:** The pre-registered cohort (`pilot_subset_verified_multifile_n50.txt`) was not used for the initial SWE-agent run. The run started with convenience instances (21 astropy + 4 django) for infrastructure validation, then was completed as n=25 before discovering the pre-registered file existed in `E:\swe-bench-3slot\artifacts\`.  
**Change:** The n=25 astropy+django run is designated **Gate 2.1 (exploratory)**. Its results are in `GATE2_1_VERDICT.md` — verdict PASS, but cohort is not the pre-registered design.  
**Impact:** Requires re-run. Gate 2.2 below is the pre-registered study with the correct balanced cohort.

---

### Amendment A3 — Pre-registered cohort replaced with balanced selection; Gate 2.2 defined
**Date:** 2026-06-22  
**Type:** cohort redesign  
**Rationale:** `pilot_subset_verified_multifile_n50.txt` (the original §0.1 file) was too django-heavy (21/50 = 42%) and contained only 2 repos used in actual runs. A balanced resample was performed.  
**Change:** New cohort: `gate2_2_subset_balanced_n50.txt` — 50 instances, 10 repos, django capped at 11, 98% multi-file patches, seed=20260622. This is the **Gate 2.2** study.  
**Impact:** Re-run required with new cohort. Old cohort file `pilot_subset_verified_multifile_n50.txt` is archived; new file is `E:\swe-bench-3slot\artifacts\gate2_2_subset_balanced_n50.txt`.

---

### Amendment A4 — Ablation study (formerly §12/§13) deferred to Gate 2.3+
**Date:** 2026-06-22  
**Type:** scope deferral  
**Rationale:** Ablation study (individual layer testing) is conditional on Gate 2.2 PASS. Renumbered to Gate 2.3+ to keep Gate 2.2 as the primary pre-registered study.  
**Change:** §12 (Gate 2.2 Ablation) → Gate 2.3+. Gate 2.2 is now the balanced-cohort n=50 run. See §14 (new) for Gate 2.2 design.  
**Impact:** Sequencing only — no change to experimental design.

---

## §9. Pre-flight build tasks (must be GREEN before freeze)

| Task | Status | Notes |
|------|--------|-------|
| 9.1 Observer built | ✅ | `go build -o observer.exe ./cmd/observer` |
| 9.2 Run config frozen | ✅ | `gate2_run_config.yaml` (all run parameters) |
| 9.3 Configs created | ✅ | `ab-off.toml`, `ab-on.toml` |
| 9.4 Azure endpoint set | ⏳ | Replace PLACEHOLDER in configs |
| 9.5 Run scripts created | ✅ | `run_gate2_off.sh`, `run_gate2_on.sh`, `run_gate2_full.sh` |
| 9.6 Verdict computor created | ✅ | `gate2_verdict.py` |
| 9.7 Instance list frozen | ✅ | `instances_n50.txt` (50 lines) |
| 9.8 Pre-flight smoke test | ⏳ | 1 curl through each proxy, verify DB capture |
| 9.9 Docker networking verified | ⏳ | Container can reach localhost:8831 on host |
| 9.10 Agentless validated | ⏳ | Reuse existing install |
| 9.11 Freeze commit recorded | ⏳ | Commit SHA + date in this section |

**Freeze protocol:** Once all tasks GREEN, commit this document, record SHA below, fire run.

**Freeze commit:** TBD (must commit gate2_run_config.yaml + GATE2_PRE_REGISTRATION.md + all scripts)  
**Freeze date:** TBD

---

## §10. Post-run deliverables

1. **Verdict artifact**: `gate2_verdict.json` (committed to repo)
2. **Diagnostic memo** (if FAIL or PARTIAL): `gate2_diagnostic_memo.md` (committed)
3. **Raw observer DBs**: `observer-off.db`, `observer-on.db` (archived, not committed)
4. **Raw results dirs**: `gate2_off/`, `gate2_on/` (archived, not committed)
5. **Full log**: `gate2_full.log` (archived)
6. **Update to compression-testing/PLAN.md**: Add Gate 2 results section

---

## §11. Success criteria summary (operator reference)

**PASS requires ALL of:**
- ✅ Completion rate delta ≥ -3pp
- ✅ Input token savings ≥ 10%
- ✅ Cost per completed: ON ≤ OFF
- ✅ Byte compression ratio ≤ 0.80 (≥20% saved)
- ✅ Cache-read tokens preserved (≥90%)
- ✅ No compression errors
- ✅ Turn count delta <10%
- ✅ ≥80% instances show byte savings

**PARTIAL acceptable if:**
- Completion delta ≥ -3pp AND (token savings 5-10% OR cost neutral OR byte savings 10-20%)

**FAIL triggers:**
- Completion rate drops >3pp (>1.5 instances)
- Cost per completed increases
- Compression errors > 0
- Cache-read tokens drop >10%

---

**Document status:** DRAFT (awaiting freeze)  
**Next step:** Complete §10 build tasks, operator review, freeze commit

---

## §12. Gate 2.2+ Ablation Study (conditional on Gate 2.1 PASS)

**Trigger:** Gate 2.1 (Arm OFF vs Arm ON-FULL) achieves PASS verdict.

**Goal:** Isolate which compression layer(s) provide cost savings without accuracy harm. Publish compression-vs-accuracy curves for external validation.

### §13.1 Additional arms (3 single-layer + 3 two-layer combinations)

| Arm | Layers Enabled | Config | Purpose |
|-----|----------------|--------|---------|
| **ON-L1** | Per-type only | `compress_types=[...], no drop, no stash` | Test per-type compressors alone |
| **ON-L2** | Budget-drop only | `drop enabled, compress_types=[], no stash` | Test message dropping alone |
| **ON-L3** | Stash only | `stash enabled, compress_types=[], no drop` | Test stash alone |
| **ON-L1L2** | Per-type + drop | `compress_types=[...], drop enabled, no stash` | Test if drop adds to per-type |
| **ON-L1L3** | Per-type + stash | `compress_types=[...], stash enabled, no drop` | Test if stash adds to per-type |
| **ON-L2L3** | Drop + stash | `drop + stash, no per-type` | Test if stash adds to drop |

**Sample size:** Same 50 instances per arm (reuse OFF baseline from Gate 2.1)

**Expected wall time:** ~10 hours × 6 arms = 60 hours total (can parallelize if needed)

### §13.2 Compression-vs-Accuracy curve

**X-axis:** Compression configuration (OFF, L1, L2, L3, L1L2, L1L3, L2L3, FULL)

**Y-axis:** Completion rate delta (vs OFF baseline)

**Bubble size:** Token savings %

**Deliverable:** SVG plot + data table showing:
- Each arm's completion rate (± error bars)
- Each arm's token savings %
- Each arm's cost-per-completed
- Statistical significance (McNemar test vs OFF)

**Publication-ready format:** Similar to Gate 1's GSM8K curve (8-level sweep), but accuracy instead of exact-match.

### §13.3 Decision criteria for ablation

**Per arm:**
- **Acceptable:** Completion delta ≥ -3pp AND token savings ≥ 5%
- **Recommended:** Completion delta ≥ -2pp AND token savings ≥ 10%
- **Best:** Highest token savings among all Acceptable arms

**Final recommendation:** 
- If L1 alone is Acceptable → deploy L1 only (simplest)
- If FULL is Acceptable but no single layer is → deploy FULL (synergy effect)
- If L1L2 is Best → deploy L1L2 (drop L3 to reduce complexity)

---

## §13. Post-ablation deliverables (Gate 2.2+ only)

1. **Ablation results table**: `gate2_ablation_results.csv` (8 rows × 10 columns)
2. **Compression-accuracy curve**: `gate2_compression_accuracy_curve.svg` (publication-ready)
3. **Layer contribution analysis**: `gate2_layer_analysis.md` (which layers essential?)
4. **Production recommendation**: One-page memo recommending optimal config
5. **Update to PLAN.md**: Gate 2 complete section with both baseline + ablation results

---

## §14. Gate 2.2 — Pre-registered Balanced Cohort Run (THIS IS THE PRIMARY STUDY)

> **Status:** Pending — to run after Gate 2.1 infrastructure validated ✅

### §14.1 Cohort

**File:** `E:\swe-bench-3slot\artifacts\gate2_2_subset_balanced_n50.txt`  
**Manifest:** `E:\swe-bench-3slot\artifacts\gate2_2_subset_balanced_n50.json`  
**Selection seed:** 20260622  
**Strategy:** Multi-file patches preferred (≥2 files), cap=10/repo, repos with zero multi-file instances excluded.

**Distribution:**

| Repo | n | Multi-file | Files avg |
|------|---|-----------|-----------|
| django | 11 | 10 | 2.3 |
| sympy | 8 | 8 | 5.1 |
| sphinx-doc | 8 | 8 | 2.4 |
| pylint-dev | 6 | 6 | 2.8 |
| pydata (xarray) | 5 | 5 | 2.0 |
| matplotlib | 4 | 4 | 2.5 |
| astropy | 3 | 3 | 2.7 |
| pytest-dev | 2 | 2 | 2.0 |
| scikit-learn | 2 | 2 | 2.0 |
| mwaskom (seaborn) | 1 | 1 | 2.0 |
| **Total** | **50** | **49 (98%)** | **avg 2.8** |

### §14.2 Design

All parameters identical to Gate 2.1 except cohort:
- **Agent**: SWE-agent 1.1.0
- **Model**: gpt-5.3-codex via Azure (32k output tokens)
- **Arms**: OFF (port 8831) / ON-FULL (port 8832), both proxied through Observer
- **Compression config**: ab-on.toml (all layers: L1 per-type + L2 budget-drop + L3 stash)
- **Harness**: SWE-bench docker harness (reuses docker images from swe-bench-3slot)
- **Pass/fail criteria**: Same as §3.3 and §3.4

### §14.3 New repos requiring setup

Compared to Gate 2.1 (astropy + django), Gate 2.2 requires cloning:
- `sympy/sympy`
- `sphinx-doc/sphinx`
- `pylint-dev/pylint`
- `pydata/xarray`
- `matplotlib/matplotlib`
- `pytest-dev/pytest`
- `scikit-learn/scikit-learn`
- `mwaskom/seaborn`

Each repo needs:
1. Cloned to `E:\superbased-observer\compression-testing\gate2\repos\<slug>\`
2. CRLF fixed (Git config: `core.autocrlf=false`)
3. Base commits pre-fetched for each instance
4. Added to `REPO_LINUX_PATHS` in `run_gate2_swe_agent.py`

### §14.4 Pre-flight checklist

- [ ] All 8 new repos cloned and CRLF-fixed
- [ ] Base commits fetched for all 50 instances
- [ ] `PILOT_INSTANCES` in runner updated to all 50 instance IDs
- [ ] `BASE_COMMITS` dict populated for all 50 instances
- [ ] `REPO_LINUX_PATHS` updated for all 10 repos
- [ ] Observer proxies running (8831 OFF, 8832 ON)
- [ ] DB baselines noted before run start
- [ ] Smoke test: 1 instance from a new repo (e.g., sympy) through each arm

### §14.5 Expected deliverables

1. `GATE2_2_VERDICT.md` — verdict + all metrics + layer breakdown
2. `gate2_2_predictions_off.jsonl` / `gate2_2_predictions_on.jsonl` — 50 patches per arm
3. Harness resolve rates for both arms
4. Layer-level compression stats (L1/L2/L3 breakdown from observer-on.db)

