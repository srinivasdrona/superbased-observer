# Gate 2.4 — Live Run State (operational handoff)

## ✅ RUN COMPLETE — 2026-06-25 13:23 IST (exit 0)

- 300/300 agent runs, **0 empty patches**, 3 multi-attempt. 30/30 harness.
- **Resolution: OFF 31/150 = 20.7%  vs  ON 31/150 = 20.7% → resolution-NEUTRAL.**
- Tokens (whole-DB sums; incl ~handful of warmup/preflight turns, negligible):
  - input non-cached: OFF 1,508,057 / ON 1,381,143 → **−8.4%**
  - cache_read:       OFF 10,064,512 / ON 9,180,672 → **−8.8%**
  - total input (incl cache): **−8.7%**
  - output:           OFF 221,477 / ON 247,978 → **+12.0%** (ON higher, as in 2.3)
  - all tokens:       **−8.3%**
  - ON-arm body compression: 71.46MB → 46.47MB = **35.0%** byte reduction
- Cache capture WORKS: ~93%+ of turns log cache_read (bug gone).
- Consistency vs frozen 2.3 (Input −9.1%, net −8.7%, turns +6.8%): matches.
- PENDING: cache-aware cost (need gpt-5.3-codex cached-input price); write
  Gate 2.4 VERDICT; fix 2.3 VERDICT Limitation #4.

### Cache-aware COST (gpt-5.3-codex Standard: in $1.75 / cached $0.175 / out $14.00 per 1M)
- OFF total $7.501  vs  ON total $7.495  → **−0.1% (cost-NEUTRAL)**.
- Component: input OFF $2.639/ON $2.417; cache OFF $1.761/ON $1.607;
output OFF $3.101/ON $3.472 (output = 41% of cost, ON +12% output offsets input saving).
- 2.3-style cache-FREE billing of same tokens → −6.0% (reproduces the old −6.8%
artifact). The cost edge was cache-blind accounting; corrected = neutral.
- Verdict: 1st-order resolution neutral; 2nd-order tokens −8.7% input/−8.3% all;
3rd-order cost −0.1% neutral (cached tokens 10× cheaper + higher output negate $ saving).

---


> Operational state for the in-flight Gate 2.4 run. NOT the strategic plan
> (that is `compression-testing/PLAN.md`). This file exists so the run can be
> monitored / resumed / recovered after a context compaction or a crash.

## What Gate 2.4 is

Exact A/B replay of **frozen Gate 2.3**, on the committed `439ff1b` binary,
with the **cache-read-token capture fix** applied. Purpose: a publication-grade
baseline that proves compression is resolution-neutral AND quantifies token/cost
savings *including* the large cache-token bucket that Gate 2.3 silently lost to a
proxy parser bug.

- Gate 2.3 is FROZEN (results + VERDICT). Do not alter its numbers/DBs.
- Gate 2.4 runs on the **latest repo** (clean `439ff1b`); 2.3 ran on dirty
  `3c78587` (2026-06-17). Different repo states are accepted (user decision).
- Cohort, base commits, retries, reps, cost limits = **identical to 2.3**
  (verified AST-equal). Only differences: ports (8841/8842), output dirs/DBs,
  and the cache fix.

## Live process (launched 2026-06-25 ~01:55)

- **Runner**: `gate2.4/run_gate2_4.py`, launched via `gate2.4/_launch.sh`
  as **root in WSL**, detached. Copilot async shellId: `gate24run`.
- **Log**: path is in `gate2.4/logs/CURRENT_LOG`
  (currently `gate2.4/logs/run_full_20260625_015515.log`).
- **Output root**: `gate2.4/runs/gate2_4_swe_20260625_015519/`
  - `state.json` — checkpoint after every (instance,rep,arm) + harness step.
  - `grand_summary.json` — written at completion.
  - `harness/batch_NN/` — predictions + swebench reports.
- **Pre-flight cache guard: PASSED** (cached=7808 both arms before batches).

## Proxies (Windows host, binary `bin/observer-2.4.exe` = clean 439ff1b)

| Arm | Port | PID | Compression |
|---|---|---|---|
| 2.4 OFF | 8841 | 22868 | disabled |
| 2.4 ON  | 8842 | 18648 | full (2.3 knobs) |

**Do NOT touch the 2.3 live proxies** (still up for provenance):
2.3 OFF 8831 (PID 49772), 2.3 ON 8832 (PID 41636).

## Networking traps

- Runner is in WSL; proxies are on Windows. WSL reaches them via the NAT
  gateway = `/etc/resolv.conf` nameserver = **currently `192.168.80.1`**.
  `localhost` from WSL does NOT reach Windows proxies.
- This IP can change on WSL restart. Re-capture:
  `grep nameserver /etc/resolv.conf | cut -d' ' -f2`, then relaunch with
  `PROXY_HOST=<ip>`.
- Reachability check returns HTTP 404 when a proxy is up (that's "alive").

## Scale / expected duration

50 instances × 3 reps × 2 arms = 300 agent runs (≤10 retry attempts each),
5 batches of 10. After each batch: harness per rep per arm (30 harness
evaluations total, Docker). Multi-hour (tens of hours) — same as 2.3.

## Monitor

```powershell
$log = (wsl -e bash -lc "cat /mnt/e/superbased-observer/compression-testing/gate2.4/logs/CURRENT_LOG").Trim()
wsl -e bash -lc "tail -50 /mnt/e/superbased-observer/compression-testing/gate2.4/$log"
# progress: count completed runs in state.json
wsl -e bash -lc "python3 -c \"import json;s=json.load(open('/mnt/e/superbased-observer/compression-testing/gate2.4/runs/gate2_4_swe_20260625_015519/state.json'));print('runs',len(s['runs']),'harness',len(s['harness']))\""
```

## Resume after a crash

```bash
cd /mnt/e/superbased-observer/compression-testing/gate2.4
export PROXY_HOST=192.168.80.1
export AZURE_OPENAI_API_KEY="$(cat ../gate2/.gate2_key)"; export OPENAI_API_KEY="$AZURE_OPENAI_API_KEY"
export GATE24_RESUME_DIR=/mnt/e/superbased-observer/compression-testing/gate2.4/runs/gate2_4_swe_20260625_015519
<swebench-wsl-python> -u run_gate2_4.py --resume
```
(run as root; `--resume` skips keys already in state.json.)

## ✅ Post-run tasks — ALL COMPLETE

1. ✅ Cache-aware cost computed (`$7.501 OFF vs $7.495 ON = −0.1% neutral`); pricing sourced from OpenAI.
2. ✅ Gate 2.4 VERDICT.md written (three-order framing). Committed `325db85`.
3. ✅ Gate 2.3 VERDICT Limitation #4 corrected (proxy parser gap, not Azure; frozen numbers untouched). Committed `995b546`.
4. ✅ SPLIT_ANALYSIS.md published (resolved-vs-unresolved compression split, validated epoch-bucketing). Committed `325db85`.
5. ✅ Temp cleanup: `_dep_check.py`, `_netcheck.sh`, `_verify_constants.py`, `cache-probe/`, all `g24_*.py` scratch scripts removed.

**Gate 2.4: CLOSED.**

## Key facts

- Cache fix (committed `439ff1b`): `internal/proxy/provider.go`
  `parseOpenAIResponse` reads `usage.input_tokens_details.cached_tokens` as
  fallback (non-streaming Azure Responses API `resp_` IDs put cache there).
  Validated end-to-end: warm calls show `cache_read_tokens=7808` in both 2.4 DBs.
- Gate 2.3 frozen numbers (do not alter): Resolve ON 22.0% (31/141) vs OFF
  22.5% (32/142); Input −9.1%; net input+output −8.7%; proxy cost −6.8%;
  turns +6.8%; cache 0/0 (the bug). 2.3 cost edge unreliable (both arms billed
  cache-free) — 2.4 produces the corrected cache-aware cost.
- swebench-wsl python:
  `/home/sdrona/swe-bench-3slot-work/python-env/swebench-wsl/bin/python3`
