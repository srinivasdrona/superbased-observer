# Changelog

All notable changes to SuperBased Observer are documented here.

## [1.6.22] — 2026-05-21

### feat(claudecode,hook): per-turn `effort.level` capture for Claude Code on both Linux CLI and Windows Desktop

Closes a gap operator-reported 2026-05-21: Claude Code's effort
dropdown (Max / Extra High / High / Medium / Low) was changing real
behavior turn-to-turn, but the dashboard's per-action **Effort** column
(landed in v1.6.18) stayed empty for every claude-code row. Three
distinct fragilities lined up underneath that one symptom; each is
fixed in this ship.

**Why the JSONL alone can't carry effort.** Exhaustive JSON-key scan
across hundreds of `.jsonl` files on both Windows and Linux corpora
returns zero hits for `effort` / `reasoning_effort` / `budget_tokens` /
`thinking_budget`. Effort is a request-side
`thinking: {budget_tokens: N}` parameter that the Anthropic API never
echoes back into the response stream. Per
`code.claude.com/docs/en/hooks`, `effort.level` IS exposed to the
PreToolUse / PostToolUse / Stop / SubagentStop hook payloads — and
that's the only per-turn source. Observer registers all four events
already, but `HandleApprove` was discarding the payload.

**What this ship adds:**

- **Migration 026** — new `claudecode_effort` sidecar table keyed
  `(session_id, tool_use_id)` storing `effort_level`, `event_name`,
  `received_at`. The Anthropic `toolu_xxx` block ID is already the
  `source_event_id` for tool_use rows in `actions`, so joins are
  natural without a schema change to `actions`.
- **`store.UpsertClaudecodeEffort`** — single in-tx upsert that
  populates the sidecar AND runs an `UPDATE` on any matching
  already-inserted action row's `metadata.effort_level`. Race-safe in
  either ordering: hook fires before JSONL ingest (sidecar lookup
  catches it on parse) or after (UPDATE stamps the row immediately).
- **`recordClaudecodeEffort`** in `cmd/observer/hook.go` — extracts
  `(session_id, tool_use_id, effort.level)` from PreToolUse + new
  PostToolUse dispatch; no-ops cleanly when `effort.level` is absent
  (which the docs say happens on models that don't support effort).
- **`claudecode.Adapter.WithEffortLookup`** — JSONL parse-time
  enrichment via per-session cached map; fail-safe on lookup errors
  (parse still returns events).
- **`claude-code-windows` registration target** — mirrors
  `cursor-windows`. Writes hooks into a Windows-side
  `.claude/settings.json` with `wsl.exe -d <distro> -- <linux-bin>` so
  Claude Desktop on Windows can fire hooks into the WSL-side observer
  binary. Auto-surfaced via `Registry.Installed()` when crossmount
  detects a Windows-side `.claude/`.

### fix(hook): three E2E fragilities that broke the chain on first ship

Discovered during empirical Desktop-side validation:

1. **Git Bash MSYS path translation** — Claude Code on Windows runs
   hook commands through Git Bash (per the upstream docs). Git Bash's
   MSYS layer auto-rewrites POSIX-shaped `/home/...` arguments into
   `C:/Program Files/Git/home/...` before they reach the program being
   spawned, so `wsl.exe -d Ubuntu-20.04 -- /home/.../bin/observer`
   became `wsl.exe -d Ubuntu-20.04 -- C:/Program Files/Git/home/.../
   bin/observer`, which wsl.exe could not find inside the Linux distro,
   exit-127 every fire. JSONL attachment records captured the symptom
   verbatim:
   ```
   {"type":"hook_non_blocking_error", "exitCode":127,
    "stderr":"/bin/bash: C:/Program Files/Git/home/.../observer: No
    such file or directory"}
   ```
   Fix: prefix every registered Windows-side wsl.exe command with
   `MSYS_NO_PATHCONV=1 ` so Git Bash skips POSIX→Win32 conversion. The
   env-var assignment is bash-only — silently ignored by macOS/Linux
   `sh -c` and by cmd.exe — so it's safe to set unconditionally on the
   Windows registrars without OS branching. Same fix mirrored to
   `cursor-windows` (Cursor on Windows uses the same Git Bash hook
   execution path). `isObserverWindowsClaudeEntry` /
   `isObserverWindowsCursorEntry` updated to recognise both the old
   prefix-free and new MSYS-prefixed shapes, so refresh-on-drift
   silently upgrades existing user registrations.

2. **`selectTools` whitelist missing `-windows` variants** —
   `cmd/observer/init.go::selectTools` had a local `supported` map
   hard-coded to `{claude-code, cursor, codex}` that silently dropped
   `claude-code-windows` and `cursor-windows` from `observer init
   --all` even though both registrars existed and `Installed()`
   surfaced them. Auto-register-on-start was unaffected (uses
   `hookSupported` directly), but the explicit `init` path needed the
   fix. Cursor-windows had been quietly broken for users running
   `observer init` explicitly; bonus fix included.

3. **`db.Open` deadlined by HookTimeout** —
   `recordClaudecodeEffort` initially fenced both `db.Open` and the
   upsert under `cfg.Observer.Hooks.HookTimeout()` (~250-500ms write-
   side budget). While the long-running daemon holds the WAL hot, a
   per-process hook invocation's `db.Open` reliably busts that budget
   on the `quick_check` integrity probe and silently drops the effort
   write. Split the deadlines (mirrors the cursor/codex hook
   handlers): unbounded `context.Background()` for `db.Open`,
   `HookTimeout` only on the actual write. Sidecar writes now land in
   <50ms in steady state.

### Migration

- `db.Open` applies migration 026 (new `claudecode_effort` table)
  automatically on next daemon start. Zero risk: pure additive
  CREATE — no existing data touched.
- Existing `claude-code` + `cursor` hook registrations on the Linux
  home are untouched. Windows-side `settings.json` / `hooks.json`
  entries previously written by Phase 2 / the cursor-windows v1.4.45
  ship get refreshed in place with the new `MSYS_NO_PATHCONV=1`
  prefix on the next `observer start` or `observer init --all
  --force` (refresh-on-drift recognises both shapes as ours).

### What this doesn't fix

- The target session `bd834194-1b9c-480f-bd14-f126a5f35a55` that
  prompted this investigation **cannot be retroactively backfilled** —
  no hooks fired during its lifetime because the Windows-side
  `settings.json` hadn't been written yet. Only sessions started AFTER
  the registration write AND after Claude Desktop is restarted to
  re-read settings.json will capture per-turn effort.
- Models that don't support the `effort` parameter (per Anthropic's
  docs) will still leave the column blank. The hooks fire and exit 0;
  `recordClaudecodeEffort` no-ops cleanly when `effort.level` is empty
  in the payload. Opus 4.7 and other extended-thinking-capable models
  do emit it.

### Verified

- Empirical end-to-end on Claude Desktop v2.1.138 (2026-05-20, fresh
  install): hooks fire exit 0; `claudecode_effort` sidecar populates;
  dashboard Effort column shows the per-turn value across Max → High
  → Low ladder changes.
- `go test -race ./...` — 51 packages OK (one inherent concurrent-
  migration test flake under 51-package parallel load; passes 5/5 in
  isolation).
- `go vet ./...` — clean.

### Docs

- New `docs/claudecode-hook-capture.md` — verified Claude Code hook
  envelope, dual-registration model, MSYS gotcha, sidecar contract.
- `docs/provider-mapping.md` — `claude-code-windows` cell added,
  MSYS_NO_PATHCONV note appended to both Windows registrars' rows.

## [1.6.21] — 2026-05-20

### fix(release): gate npm-release.yml to the private repo so the public-repo orphan push doesn't double-fire

Hotfix for a second failure-mode surfaced by v1.6.19/v1.6.20: the
`.github/workflows/` directory rides along in the orphan tree
force-pushed to the public repo by `scripts/release.sh public`
(it's not in `PRIVATE_ONLY_PATHS` — intentional, for transparency).
The v* tag push to the public repo then also fires `npm-release.yml`
there. The public repo has no `NPM_TOKEN_OBSERVER` secret (and
shouldn't — npm publish belongs to the maintainer side), so the
`publish to npm` job fails with `ENEEDAUTH`. The `build`,
`public_release`, etc. jobs also wastefully run there, only
"working" because `gh release edit + upload --clobber` is
idempotent against the same release the private run just created.

**What this ship changes:**

- `frontend` job gains `if: github.repository == 'marmutapp/superbased-observer-private'`.
  Every other job in the workflow chains on `needs: frontend`
  transitively (build → publish → release; build → public_release),
  so skipping `frontend` on the public repo short-circuits the whole
  pipeline cleanly — all jobs report as "Skipped" instead of running
  and failing.
- No change to behavior on the private repo: every existing condition
  still holds because `github.repository == 'marmutapp/superbased-observer-private'`
  is true there.

**What didn't change:**

- `ci.yml` (the pre-merge gate) is intentionally left ungated. It
  fires on `push: main` on the public repo when the orphan commit
  lands, runs the same typecheck + build + test pass against the
  public-tree source, and succeeds. That's a useful free
  cross-check (does the scrubbed public tree still build + pass
  tests?), not a duplicate publish.
- The orphan tree still includes `.github/workflows/` so the
  workflow source remains visible on the public repo. Browsers
  see the same YAML the maintainer ships; CI just doesn't fire.

## [1.6.20] — 2026-05-20

### fix(release): public_release job — fail-fast on missing PAT + unauth'd tag-wait

Hotfix for a confusing failure mode hit on the first v1.6.19 release.
Before `PUBLIC_REPO_TOKEN` was set, the `public_release` job's
`Wait for v* tag on public repo` step ran `gh api --silent 2>/dev/null`
against the public repo's ref endpoint. With `GH_TOKEN` empty (unset
secret evaluates to ""), gh CLI sends an empty bearer token and the
API returns 401 — but the `--silent 2>/dev/null` swallowed it and the
loop kept retrying. After 12 iterations (120s) the job errored with
"Public tag not present" even though the tag was right there on the
public repo, accessible to anonymous curl. The operator chased a
non-issue ("did release.sh public actually push the tag?") before
realizing the PAT secret was missing.

**What this ship changes:**

- `Wait for v* tag on public repo` step now uses **unauthenticated
  curl** against `api.github.com/repos/marmutapp/superbased-observer/git/ref/tags/<tag>`.
  The public repo's ref endpoint is readable anonymously, so the wait
  doesn't need a PAT. The step explicitly switches on HTTP status:
  200 → success, 404 → keep polling (expected during the wait
  window), 403 → rate-limit backoff, anything else → log + retry.
- New explicit `Sanity-check PUBLIC_REPO_TOKEN is set` step right
  after the wait. Fails fast with a one-line "see runbook" pointer
  if the secret isn't configured, instead of letting the downstream
  `gh release create` step fail with the cryptic
  `gh: ... HTTP 404` (GitHub returns 404 for unauthorized cross-repo
  writes to avoid leaking repo existence — harder to debug than "PAT
  not set").

**Net effect:** the first-time setup failure mode now produces an
actionable error in the workflow log instead of a 2-minute
silent-401 wait. No behavior change for anyone whose PAT is already
set; the wait step's curl is functionally equivalent to the prior
gh-api call when both are reaching the public ref endpoint.

No code changes outside `.github/workflows/npm-release.yml`. No
schema migration. No daemon restart. Patch lands as v1.6.20 rather
than v1.6.19.1 because npm's strict SemVer 2.0.0 rejects 4-part
versions — the npm-publish step would error on a 1.6.19.1 tag.

## [1.6.19] — 2026-05-20

### chore(release): public-repo GitHub Release with per-platform binary archives

Adds direct-download binaries on the public repo's Releases tab so
users who don't want to install via npm can `curl` the right archive
for their architecture. Builds on the existing cross-compile pipeline
that already produces the 5 platform binaries for npm — same
artifacts, now also surfaced as tarball/zip downloads on
`github.com/marmutapp/superbased-observer/releases`.

**What ships per release:**

| Asset | Platform | Contents |
|---|---|---|
| `observer-vX.Y.Z-linux-x64.tar.gz` | Linux x86_64 | `observer` + `antigravity-bridge.exe` |
| `observer-vX.Y.Z-linux-arm64.tar.gz` | Linux arm64 | `observer` + `antigravity-bridge.exe` |
| `observer-vX.Y.Z-darwin-x64.tar.gz` | macOS Intel | `observer` |
| `observer-vX.Y.Z-darwin-arm64.tar.gz` | macOS Apple Silicon | `observer` |
| `observer-vX.Y.Z-win32-x64.zip` | Windows x86_64 | `observer.exe` |
| `SHA256SUMS` | — | sha256 of all 5 archives, verifiable via `shasum -a 256 -c` |

**Pipeline changes:**

- `scripts/release.sh public` now accepts an optional `<version>` arg.
  When set, it force-pushes the same `v*` tag to the public remote
  pointing at the orphan commit's sha. `scripts/release.sh full
  <version>` passes the version through automatically; the legacy
  `scripts/release.sh public` (no version) form still works for
  tree-only refreshes that don't bump the release version.
- `.github/workflows/npm-release.yml` gains a `public_release` job
  that runs in parallel with `publish` (only needs `build`), waits
  for the v* tag to exist on the public repo (polls up to 2 min),
  bundles each platform's `bin/` folder into a tarball or zip,
  computes `SHA256SUMS`, and calls `gh release create --repo
  marmutapp/superbased-observer`. Idempotent on retry — uses
  `gh release edit` + `gh release upload --clobber` if the release
  already exists.
- New repo secret on the private repo: `PUBLIC_REPO_TOKEN`. Fine-
  grained PAT with `contents: write` on the public repo only. **Must
  be set before the v1.6.19 tag lands** or the new job will fail
  with a `gh: HTTP 404` from the release-create step. Setup steps
  in `docs/release-runbook.md` under "Required CI secret".

**Backwards compatibility:** existing npm distribution flow is
unchanged. The `public_release` job is additive; if it fails (e.g.
missing PAT, public-tag push lagged), the npm publish + private
GH Release jobs are unaffected. Re-running the failed job after
fixing the underlying cause picks up where it left off.

**Body composition:** the public Release notes are the matching
CHANGELOG.md section plus a `### Downloads` addendum that lists the
five archives, verification command, and a pointer to the npm
package for users who prefer that install path. Same extraction
logic as the private release job — `awk` from `## [<version>]` to
the next `## [` header.

### fix(store): copilot-cli rescan FK 787 — stale LastInsertId on UPSERT-UPDATE

User on a Windows-native daemon reported repeated rescan failures:

```
watcher.Scan: process failed adapter=copilot-cli
  path=C:\Users\sdrona\.copilot\session-state\<uuid>\events.jsonl
  err="freshness.UpsertFileState: constraint failed: FOREIGN KEY constraint failed (787)"
```

8 of 79 copilot-cli session files erroring per rescan cycle. Root
cause: the single-row `insertSingleAction` path (`internal/store/store.go:1124`)
takes `LastInsertId()` unconditionally after `INSERT ... ON CONFLICT
DO UPDATE`. Per SQLite docs, UPSERTs that take the UPDATE branch
**do not change last_insert_rowid** — so on a re-scan that hits the
UPDATE branch, `LastInsertId()` returns the connection's previous
true-INSERT rowid. If retention has pruned that earlier row,
`UpsertFileState` then writes the stale `last_action_id` and trips
the actions(id) foreign-key constraint (SQLITE_CONSTRAINT_FOREIGNKEY,
extended code 787).

The batch `InsertActions` path (lines 290–428) already documented
and handled this exact issue with an existence pre-check (see its
inline comment at line 314). The single-row path didn't. This ship
brings the single-row path in line:

- Add the same `SELECT id FROM actions WHERE source_file = ? AND source_event_id = ?`
  pre-check before the UPSERT.
- On the UPDATE branch (row already existed): bind `a.ID` to the
  pre-check's `existingID`, so downstream `UpsertFileState` writes
  a valid `last_action_id`.
- On the INSERT branch (no pre-existing row): keep using
  `LastInsertId()`, which is reliable for genuine inserts.

Regression test pins the bug with the user's exact error message:
`TestIngestFileAction_RescanAfterRetentionDoesNotFKFail` in
`internal/store/store_test.go` reproduces the failure pre-fix (FK
787 surfaces in `err.Error()`) and passes post-fix. The test pins
`db.SetMaxOpenConns(1)` so the connection-level `LastInsertId`
staleness carries deterministically across operations — same regime
as a long-running daemon doing serial work on a single
high-contention connection.

No DB migration. No daemon restart required for users who upgrade
across this version — but the existing erroring sessions on disk
will keep producing the same warnings until the rescan runs against
the v1.6.19 binary.

### chore(antigravity,cost): adapter audit + Gemini 3.x Pro/Flash pricing pins

Bundles the deliverables from the end-to-end Antigravity adapter
audit at `docs/antigravity-audit-2026-05-19.md`. The audit followed
`docs/adapter-audit-playbook.md` against the live antigravity corpus
(8,808 rows / 4 source-mounts / 7 distinct model SKUs) and the four
live Gemini sessions served by the running Linux-native
`language_server` processes.

**Headline finding resolved as a non-bug.** The kickoff doc's flagged
signal — "`reasoning_tokens = 0` across all 8,808 antigravity rows
despite adapter wiring at `structured.go:394`" — turned out to be a
**stale-corpus artifact**. Every row in the live DB predates commit
`6d37da7` (v1.4.53, 2026-05-15) which replaced `case 3: r.output =
f.Varint` with `case 9: r.reasoning + case 10: r.output`. Live probes
(`TestTokenRowDump` + `TestStructuredReasoningTokenCapture`) confirm
the production parser correctly emits non-zero ReasoningTokens for
fresh Gemini sessions (e.g. `162c4ab9` turn 0: `.9 = 598`, `.10 =
28`). **Aggregate cost is unchanged** because `.3 == .9 + .10` and
reasoning bills at the output rate (memory
`[[feedback-reasoning-tokens-billed]]`) — pre-v1.4.53 `.3 ×
output_rate` ≡ post-v1.4.53 `(.9 + .10) × output_rate`.

**Five verified-correct surfaces (G-ok-1 through G-ok-5):**
- Field-map complete for `1.3.1.17.2.*` usage; `1.3.1.17.3` =
  error/status strings (correctly skipped); `1.3.1.17.4` = 16-byte
  trace IDs (non-token); `1.3.1.4.*` mirrors `1.3.1.17.2.*` per turn
  (parallel duplicate emit; capturing only one is correct).
- Cross-tier emit is mutually exclusive by construction (Path A
  `classify()` vs Path B `applyStructuredEnrichment()`). Live data is
  **100% structured tier** (all 8,808 eventIDs start with
  `antigravity-struct-token:`); zero classify-tier rows.
- Re-parse idempotency intact: zero duplicate `(source_file,
  source_event_id)` tuples across all 8,808 rows.
- Store-layer dedup correctly excludes antigravity — all four
  `DELETE FROM token_usage` sweeps in `internal/store/store.go` are
  scoped to other tools (copilot-cli ×2, claude-code+codex
  tuple-dedup, claude-code snapshot-drift).

**Actionable findings shipped:**

0. **Gemini 3.5 Flash added** — `gemini-3.5-flash` exact entry pinned
   at Standard-tier rates per Google's Developer API pricing card
   (2026-05-19): Input $1.50 / Output $9.00 / CacheRead $0.15 per
   1M tokens. The entry also serves as a family prefix via
   `familyKeys()`, so `gemini-3.5-flash-experimental` (or any future
   flash-suffix SKU) resolves to Flash rates rather than falling to
   the `gemini-3` Pro-family entry. Google has no `gemini-3.5-pro`
   on its official pricing page as of this date — hypothetical 3.5
   Pro SKUs deliberately fall through `gemini-3.5-flash` (prefix
   doesn't match) to the `gemini-3` family (Pro rates), the
   conservative default. Batch / Flex / Priority tiers and grounding
   per-query fees ($14/1000 = $0.014/call) are NOT modelled —
   per-tier pricing and `GroundingPerRequest` field structural work
   remains in Workstream B (kickoff doc post-v1.6.18 §2). Per-hour
   cache storage ($1/1M tokens/hr) also deferred — shape doesn't
   fit the current per-call Pricing struct.

1. **B1 — Gemini 3 Flash family-prefix resilience**
   `gemini-3-flash-<unknown>` SKUs (any hypothetical Flash variant
   not pinned in the explicit `-preview/-agent/-high/-medium/-low`
   set) fall through the family-prefix matcher to the `gemini-3`
   family and bill at **Pro rates** ($2/$12 + LC ladder) instead of
   Flash ($0.50/$3) — ~4× over-bill. Gemini 2.5 is robust because
   `gemini-2.5-flash` and `gemini-2.5-flash-lite` are both pinned as
   family entries. Adds the missing `gemini-3-flash` family-prefix
   entry at Flash rates. Zero current live rows affected; pure
   resilience against future Antigravity SKUs.
2. **B2 — Gemini 3 Pro effort SKUs pinned explicitly**
   `gemini-3-pro-{low,medium,high}` were not pinned. 3,831 live
   `gemini-3-pro-high` rows resolved correctly via the `gemini-3`
   family-prefix fallback (Pro rates by coincidence), but the
   resolution is load-bearing on the matcher. Three new explicit
   entries paralleling the existing `gemini-3.1-pro-*` set. Zero
   behavioral change for any current row; pure resilience.
3. **C1 — `probe_e371fdb1_test.go` removed** (164 lines).
   Added in v1.6.11 to investigate session `e371fdb1` being "not at
   all being picked up". That session now extracts 17 rows / 17,595
   input tokens in the live DB — the underlying wrong-workspace-stub
   bug was fixed. Probe has served its purpose.

**Test coverage added:**
- `TestTable_AntigravityInternalSKUs` extended with three subtests
  pinning `gemini-3-pro-{high,medium,low}` to `PricingSourceExact`.
- New `TestTable_Gemini3FlashFamilyResilience` locks in the
  Flash-family fallback (asserts `gemini-3-flash-experimental` /
  `gemini-3-flash-foo-bar` resolve to Flash rates, not Pro rates).

**Carry-forward (operator decisions, documented in audit doc §X1/X2):**
- 8,808 historical rows could be back-filled with reasoning
  breakdown by invalidating their parse cursors. Aggregate cost is
  already correct; this updates the per-row column only. Most rows
  source from Windows-side `.pb` files (non-decryptable locally
  per `[[project-antigravity-windows-cipher]]`).
- 310 blank-model rows across 3 `.pb` files (160 file-missing, 150
  in-disk). Self-correctable for the 150 via parse-cursor reset; the
  current parser may resolve a model on re-attempt. Outcome
  unverified pre-action.
- Explicit `gemini-3.1-flash` and `gemini-3.1-flash-lite` family
  pins deferred pending an authoritative pricing card distinguishing
  3.1 Flash full from 3.1 Flash-Lite. Workstream B (pricing audit)
  follow-up.

**What didn't change:**
- The antigravity adapter source (`internal/adapter/antigravity/*.go`)
  is unchanged except for the probe deletion. The wiring is correct
  as of v1.4.53; the audit confirmed it.
- No schema migration. Schema stays at version 25.
- No daemon restart required — cost is computed at read time, so
  pricing.go edits take effect on the next dashboard query.

## [1.6.18] — 2026-05-19

### feat(cursor): capture finalized assistant thinking + response via afterAgent* hooks

Closes a long-standing visibility gap reported on session
`32c83fe8-3763-4f29-b127-a0968203db01`: the assistant's thinking text
was visible in Cursor's UI but completely missing from the observer
dashboard. The cursor adapter's prior coverage relied on the `stop`
event's transcript walker (`BuildStopTranscriptEvents`) to back-fill
assistant prose by reading the `agent-transcripts/<conv>.jsonl` file
the stop payload's `transcript_path` references — but on modern
Cursor (3.4.x), **that file no longer materializes**. Cursor moved
conversation storage to a SQLite store at
`User/globalStorage/state.vscdb` (key namespaces `composerData:<conv>`
+ `bubbleId:<conv>:<bubble>`, both of which carry the finalized
thinking text in `thinking.text`). The walker is dead-code on
current builds.

The v1.4.45 docstring at `internal/adapter/cursor/adapter.go` had
also rationalized away `afterAgentThought` and `afterAgentResponse`
as "fires per text/thought delta — high overhead." That claim was
verified false against captured live payloads in
`/tmp/cursor-hook-capture/` (operator's 2026-05-19 tee-shim
experiment): both events fire **once per finalized block**, with the
full text and a single `duration_ms` per thought (no per-token
streaming). The original concern doesn't apply to the current
Cursor build.

**What this ship adds:**
- New constants `EventAfterAgentThought` + `EventAfterAgentResponse`
  in the cursor adapter, with a fresh package-level docstring
  documenting the pivot from the v1.4.45 design.
- New `Text` field on `rawHookPayload` (json `text`) used by both
  events.
- New `BuildEvent` switch cases:
  - `afterAgentThought` → `models.ToolEvent` with
    `RawToolName: "cursor.thinking"`,
    `ActionType: ActionTaskComplete`,
    `PrecedingReasoning` = preview of the thinking text,
    `ToolOutput` = full body (truncated to 4000 chars),
    `DurationMs` = Cursor's rendered "Thought for Ns" timer.
    Empty-text events (rare metadata-only fires observed in
    capture dumps) are dropped.
  - `afterAgentResponse` → `models.ToolEvent` with
    `RawToolName: "cursor.assistant_response"` and the same
    preview/body shape used by the now-dormant transcript
    walker's `cursor.assistant_text` row. Empty-text events
    likewise dropped.
- `afterAgentThought` + `afterAgentResponse` appended to the
  registered Cursor hook event list in `internal/hook/register.go`
  (`cursorEvents`).

**Deliberately NOT changed:**
- Token counts. `afterAgentResponse` carries the same
  `input_tokens`/`output_tokens`/`cache_read_tokens`/
  `cache_write_tokens` fields as `stop`. We keep `stop` as the
  single source of per-turn token truth — emitting a `TokenEvent`
  from both would double-count via two distinct `(source_file,
  source_event_id)` pairs that map to the same generation. If a
  future Cursor build makes `stop` unreliable, this is the place
  to revisit.
- The dead JSONL walker. Left in place for two reasons: (1) older
  Cursor versions may still write the file (graceful fallback);
  (2) a state.vscdb reader is the natural future replacement and
  belongs in its own ship.

**New tests:**
- `TestBuildEvent_AfterAgentThought` — fixture mirrors the real
  payload shape captured for session 32c83fe8 in /tmp.
- `TestBuildEvent_AfterAgentThought_EmptyTextDropped` — guards the
  empty-text skip.
- `TestBuildEvent_AfterAgentResponse` — pins the response row shape
  + asserts no token/duration fields leak from the response payload
  into the row (those belong to the thought row + stop event).

**Live impact**: All cursor sessions ingested after the hooks are
re-registered (operator runs `observer init cursor` or the next
`observer start` auto-registration cycle) will start capturing
thinking + response prose. Historical sessions are NOT backfilled —
the data is in state.vscdb but the adapter has no reader for it
yet. Backfill is a follow-up patch (the operator-confirmed
Workstream B carry-forward of a state.vscdb reader).

Memory entry added: `feedback_cursor_after_agent_events.md` —
documents the v1.4.45 → v1.6.18 design pivot + the
single-source-of-truth principle for token counts.

### feat(dashboard): Effort column on Session Detail Messages table

The per-turn reasoning effort is already stored in
`actions.metadata.$.effort_level` (sourced from
`codex.collaboration_mode.settings.reasoning_effort` per
[[project_codex_v0_130_schema]], antigravity SKU-encoded
low/medium/high per [[project_antigravity_skus]], and any other
adapter that captures a reasoning-effort knob). It was already
surfaced on individual `toolCallRow` entries in
`/api/session/<id>/messages` but never bubbled up to the parent
`messageRow`, so the Session Detail panel's Messages table had no
way to show it. This patch closes that gap.

**Backend** (`internal/intelligence/dashboard/dashboard.go`):
- New `EffortLevel string` field on `messageRow` (omitempty).
- Aggregation in the action-scan loop: first non-empty
  `effort_level` from any contained action wins (all actions in a
  single turn share the same effort_level — codex picks it
  per-turn, antigravity bakes it into the SKU).

**Type surface** (`web/src/lib/types.ts`):
- `MessageRow.effort_level?: string` mirroring the existing
  `permission_mode` shape.

**UI** (`web/src/components/SessionDetailPanel.tsx`):
- New "Effort" column between Model and In. Renders the value
  uppercase in a tight mono font (matches the existing Msg ID
  column treatment); shows the standard `—` placeholder when
  empty (Anthropic models, copilot, etc. — adapters that don't
  expose an effort knob).
- Header `title` attribute documents the column's data sources so
  someone hovering at 2am understands why some rows are blank.
- Table `min-w-[1180px]` → `min-w-[1260px]` to accommodate the
  new column.
- SlideOver `width={1400}` → `width={1480}` per the operator's
  explicit request. The doc-comment block tracks the panel's
  width history (880 → 1200 → 1400 → 1480, each bump unlocking
  another column).

No new tests in this dashboard increment — the messages endpoint's
existing `TestHandleSessionMessages_*` golden-output tests cover
the new field via JSON marshalling (omitempty drops it when
unused, so no fixture regressions), and the column is a thin
presentational add.

## [1.6.17] — 2026-05-19

### fix(cost): long-context output rate is 1.5× standard, not 2× (OpenAI + Gemini Pro)

Acts on the pricing audit at
[`docs/pricing-audit-2026-05-19.md`](docs/pricing-audit-2026-05-19.md)
against `internal/intelligence/cost/pricing.go`. The audit
cross-checked every observed `(tool, model)` pair in the live
`token_usage` table against authoritative provider price cards
(Anthropic, OpenAI, Google Gemini, xAI). All standard rates, cache
tiers, and `WebSearchPerRequest` fees verified clean — but the
**long-context output rate** was systematically wrong across two
provider families: pricing.go used 2× the standard output rate above
the LC threshold, but both OpenAI and Google publish the rule as **2×
input / 1.5× output**. Every LC turn was over-billed by 33% on the
output side.

**Live-corpus impact**: 177 of 447 `gpt-5.4` rows (40% of gpt-5.4
traffic) cross the >272K threshold today — those rows' cost numbers
in the dashboard were 33% inflated on the output portion. Gemini Pro
+ gpt-5.5 LC traffic is zero in the current corpus so the structural
fix has no historical-cost delta for those models, but the same bug
class would have manifested as future volume hit the thresholds.

**Numeric changes** (`pricing.go`):
- `gpt-5.4.LongContextOutput`: `30 → 22.50`
- `gpt-5.5.LongContextOutput`: `60 → 45`
- 7 Gemini Pro entries (`gemini-3.1-pro-preview`,
  `gemini-3.1-pro-{low,medium,high}`, `gemini-pro-agent`, `gemini-3.1`
  family, `gemini-3` family): `LongContextOutput: 24 → 18`
- 2 Gemini 2.5 Pro entries (`gemini-2.5-pro`, `gemini-2.5` family):
  `LongContextOutput: 20 → 15`

**Test changes** (`engine_test.go`):
- `TestTable_LongContextDefaults` golden values updated to pin the
  corrected rates.
- New regression test
  `TestCompute_LongContextOutputIs1p5xNot2x` runs end-to-end (table
  lookup → `Compute`) across 6 representative SKUs, asserting the LC
  *output* rate is exactly 1.5× standard. Guards against silent
  reintroduction.

`TestCompute_ReasoningTokensBilledAtOutputRate` intentionally
unchanged — its inline `Pricing{LongContextOutput: 60}` is a synthetic
LC-dispatch fixture, not a claim about a real published rate.

### fix(cost): refresh xAI rates for the 2026-05 retirement schedule

`docs/x.ai/docs/models` lists `grok-code-fast-1` as retired
2026-05-15, with requests now billed at `grok-4.3` pricing
($1.25 / $2.50 per 1M). The current grok-4.x line (`grok-4.3`,
`grok-4.20-0309-*` reasoning / non-reasoning / multi-agent) all share
the same $1.25 / $2.50 card. Pre-2026-05-19 this table carried the
September-2025 launch rates ($0.20 / $1.50 for `grok-code-fast-1`,
$2 / $6 for `grok-4-20`), which under-bills `grok-code-fast-1`
post-retirement (6× input / 1.67× output) and over-bills `grok-4-20`
rows by 60% / 140%. Live-corpus impact: 1 row across both entries —
trivial historical dollars, but the entries were structurally wrong
going forward.

Numeric changes:
- New explicit entries: `grok-4.3`, `grok-4.20` (covers the 0309-*
  family).
- `grok-4-20` (historical alias), `grok-code-fast-1`, `grok-code`
  family, `grok` family prefix — all updated to $1.25 / $2.50.
  `CacheRead` falls back to 10% × Input ($0.125) via `fillDefaults`,
  matching how other xAI entries handle the absent published
  cache rate.

`TestTable_OtherProviderPricing` table updated with the new values
plus an explicit row asserting `grok-code-fast-1` resolves to the
grok-4.3 rate (the post-retirement-redirect invariant).

### Audit deferrals (not in this patch)

The pricing audit surfaced two additional gaps deferred for operator
review:

- **`copilot/auto` framing** (carry-forward from v1.6.16 F1) —
  recommend passthrough (accept $0), pending memory entry.
- **`GroundingPerRequest` field** — Gemini grounding-with-search
  ($0.014–$0.035/call) not modelled in the `Pricing` struct.
  Adapter side has no grounding count yet, so the engine field is
  not load-bearing today.
- **`WebSearchPerRequest` backfill** on gpt-4.1 / gpt-4o / o-series —
  zero live impact today; revisit when live web_search counts appear
  on these models.

See the audit doc §10 for the recommended sequence.

## [1.6.16] — 2026-05-19

### fix(copilot): capi-* model fallback + thinking-text preceding-reasoning capture

Acts on the VS Code Copilot Chat adapter audit at
[`docs/copilot-vscode-audit-2026-05-19.md`](docs/copilot-vscode-audit-2026-05-19.md)
against the maintainer's live 8-file modern corpus (8.2 MB combined,
4 substantive requests across 3 substantive sessions). The audit's
token-math reconciliation was clean — all 4 token buckets sum
zero-delta against `result.metadata.{promptTokens, completionTokens}`
ground truth — so no cost-engine work in this patch. The two fixes
below close the model-attribution coherence gap (F1) and the
reasoning-prose observability gap (F2) the audit surfaced.

**F1 — `capi-*` resolvedModel IDs silently bill $0.** Copilot
sometimes returns internal routing identifiers in
`result.metadata.resolvedModel` like
`capi-cus-ptuc-h100-oswe-vscode-prime` (CUS/NOE prefixes, H100/H200
GPU codes, OSWE/VSCODE channel tags) when its backend routes to a
custom-tier model without exposing a public name. **2 of 4** live
token rows had `capi-*` resolvedModel values; neither is in
`internal/intelligence/cost/pricing.go`, so the cost engine returned
$0 for them. Fix: when `resolvedModel` starts with `capi-`,
`emitModernTurn` now falls through to `modelId` (typically
`copilot/auto`) — collapsing the unpriced one-off into the canonical
Copilot routing bucket. New regression test:
`TestParseModern_CapiResolvedModelFallsBackToModelId` pins both the
public-resolvedModel preserved path (`grok-code-fast-1` survives)
and the capi-* fallback path. Pre-existing test
`TestParseModern_Kind2_AppendsNewTurn` updated to reflect the new
expected behavior. The deeper pricing question — what
`copilot/auto` should bill at, given the user pays a per-seat
subscription rather than per-token — is deferred to a later cost-
engine pass; this patch only fixes the attribution coherence.

**F2 — assistant thinking-text reasoning prose now surfaced.**
Modern Copilot interleaves `response[*].kind="thinking"` entries
between user prompt and tool calls. The adapter was already
capturing the *token count* via
`result.metadata.toolCallRounds[*].thinking.tokens` (always 0 in
this corpus — the field exists for forward-compat with
thinking-capable models), but the *prose* (16 entries across 4
requests) was dropped on the floor: `assistantResponseText` walked
the response[] array but only collected entries with a `value`
field and no `kind`, so thinking entries weren't picked up. Two
follow-on changes:
- New `collectThinkingTexts(req)` helper extracts the ordered
  thinking-prose list from `response[*].kind="thinking"`.
  `emitModernTurn` pairs the first thinking entry with the first
  round of tool calls, the second with the second round, etc.
  (sticking at the last entry if rounds outnumber thinking blocks).
  Each tool-call ToolEvent now carries the preceding round's
  reasoning in `PrecedingReasoning` (truncated to 1000 chars,
  matching the cowork sliding-window pattern).
- `assistantResponseText` tightened: any envelope with a `kind`
  field is now skipped (not just those with a `toolId`). Previously
  thinking text could leak into the task_complete row's
  `ToolOutput`; now it lands only in `PrecedingReasoning` on tool
  rows. The visible assistant prose (kind-less entries with a
  `value` string) is the only thing in `ToolOutput`.

New regression tests: `TestParseModern_ThinkingTextStampedOnToolRows`
(asserts thinking text on each tool row + asserts NO thinking text
leaks into task_complete output) and
`TestParseModern_NoThinkingTextWhenAbsent` (graceful empty case).

### Carry-over from the audit (deferred)

The following findings from
`docs/copilot-vscode-audit-2026-05-19.md` are intentionally not in
this patch:

- **F3** — other `response[*].kind` envelopes (`inlineReference`,
  `mcpServersStarting`, `progressTaskSerialized`) ignored.
  Observability-only; no token/cost impact.
- **Population audit** — the live corpus has 4 requests across 3
  substantive sessions, too small for population-level
  reconciliation. A larger-corpus re-audit when a heavy-Copilot
  maintainer corpus exists should follow.
- **Sticky-state model fallback risk** — `state.Model` is sticky
  across turns within a request; in degraded snapshots where
  modelId and resolvedModel are both empty after a populated turn,
  the next turn inherits the prior model. No live impact (every
  observed request has at least `modelId="copilot/auto"`) but
  worth a forward-look fixture if Copilot's schema ever degrades.
- **Copilot's internal cache** — Copilot uses its own caching
  layer between IDE and upstream model; the published shape exposes
  no cache fields. Adapter writes 0 for cache columns. This means
  cost engine cannot reflect cache savings the user is actually
  getting — a structural Copilot gap, not an adapter fix.

### Verification gates (all green)

- `go test -race ./... -count=1` 51/51 packages
- `go vet ./...` clean
- New regression tests: 3 in `internal/adapter/copilot/audit_2026_05_19_test.go`
- One pre-existing test updated (`TestParseModern_Kind2_AppendsNewTurn`)
  to reflect the new capi-* fallback behavior
- No migrations (parser-level changes only)

## [1.6.15] — 2026-05-19

### fix(cowork): action-taxonomy + system-event + image-message audit follow-through

Acts on the deep cowork audit at
[`docs/cowork-audit-2026-05-19.md`](docs/cowork-audit-2026-05-19.md)
against the 13-instance live Windows-side corpus
(2,569 events across 9 substantive sessions, 4 stubs). The audit's
token-math reconciliation was clean — all 18 (session × model)
buckets sum zero-delta against `result.modelUsage` ground truth, so
no cost-engine work is in this patch. The fixes below close the
action-taxonomy and system-event coverage gaps that account for
~11% of meaningful events the platform emits.

**B1 — 9 unmapped tool names land as `ActionUnknown`.** Live DB
showed **115 of 1091 cowork action rows (10.5%) tagged `unknown`**.
The biggest hit was `mcp__workspace__bash` at **83 rows** (the
MCP-routed shell tool — semantically identical to the built-in
`Bash`, ~half the bash-shaped activity in the corpus). Other
unmapped names: `mcp__workspace__web_fetch`, `Skill`, `ToolSearch`,
`mcp__cowork__{request_cowork_directory, present_files,
allow_cowork_file_delete}`, `mcp__visualize__{show_widget, read_me}`.
Fix: extend `actionMap` so MCP-routed shell/web tools land on
`ActionRunCommand` / `ActionWebFetch`, and the remaining MCP /
Skill / ToolSearch entries land on `ActionMCPCall`. `extractTarget`
gains `mcp__workspace__bash` → `command` and
`mcp__workspace__web_fetch` → `url`. New regression test:
`TestActionMap_CoworkNewToolNamesMappedCorrectly` covers all 9
names against the live-roster vocabulary.

**B2 — image-only user messages silently dropped.** `userPromptText()`
only emits when at least one `b.Type=="text"` block carries
non-empty text. A user message with only `b.Type=="image"` content
falls through — no row in `actions`. **5 image-only messages
missing** from the live DB for this corpus. Fix: when no text is
extractable but image blocks are present, emit a marker user_prompt
row with `Target = "[user sent N image attachment(s)]"`. No cost
impact (image input tokens land on the next `result.modelUsage`
bucket); observability-only. New regression test:
`TestParseSessionFile_ImageOnlyUserMessageEmitsMarkerRow` covers
single-image, multi-image, and the plain-text control case.

**G1 — system permission events captured.** The `case "system":`
branch in `ParseSessionFile` was a no-op. The corpus carries **38**
permission-flow events that fall in three subtypes:
- `permission_request` (18) → emitted as `ActionPermissionRequest`
  with `Target = tool_name` and `RawToolInput =` scrubbed
  `tool_input`. A `pendingPermissions` map keyed on `rec.UUID`
  records the row index.
- `permission_response` (18) → does NOT emit a new row; instead
  patches the queued request (response shares its UUID with the
  request per the audit). Sets `Success = granted` and stamps
  `Metadata.PermissionApprovalKind = decision`. When the request
  landed in a prior batch (resumed parse), the response drops
  silently — the adapter doesn't retro-update DB rows.
- `permission_denied` (2) → emitted as `ActionPermissionDenied`
  with `Target = tool_name`, `ErrorMessage =` scrubbed `message`,
  `RawToolName = decision_reason_type` (e.g. `"mode"`). The
  top-level `message` string on this subtype is decoded from
  `rawRecord.Message` opportunistically — for user/assistant
  records that field is a JSON object, not a string, so the
  type-mismatched decode just returns "" and falls through.

Mirrors the Copilot CLI v1.6.13 audit (B2) shape — reuses the
existing `ActionMetadata.PermissionApprovalKind` field, no new
metadata columns. New regression test:
`TestParseSessionFile_PermissionEventsCaptured` covers all three
subtypes including the request-then-response patch path.

**G2 — `system.compact_boundary` captured.** 3 compaction
boundaries in the corpus were ignored. Each carries
`compact_metadata.{trigger, pre_tokens, post_tokens, duration_ms}` —
exactly the shape the claudecode adapter already handles via its
`CompactMetadata` struct. Fix: emit `ActionContextCompacted`
mirroring the claudecode shape — `Target =
"<trigger>: ~<preTokens> tokens reclaimed"`, `RawToolInput =`
full metadata JSON, `RawToolName = "compact_boundary"`,
`DurationMs = compact_metadata.duration_ms`,
`SourceEventID = "compact:" + rec.UUID`. New regression test:
`TestParseSessionFile_CompactBoundaryEmitsContextCompacted`.

### Carry-over from the audit (deferred)

The following findings from `docs/cowork-audit-2026-05-19.md` are
intentionally not in this patch — they're either future-proofing
gaps with zero current impact or richer features that need their
own scope discussion:

- **G3** — `system.init.model` + `mcp_servers[]` + `tools[]` roster
  not lifted. Would unblock model attribution for the 4 stub
  sessions (≤4 lines each, no `result` event); the Models Used
  panel currently shows "no model attribution captured" for them.
- **G4** — `result.usage.server_tool_use.web_fetch_requests`
  decoded but not billed. Zero current cost impact (cost engine
  has no `WebFetchPerRequest`); forward-looking gap.
- **G5** — 4 `rate_limit_info` fields (`isUsingOverage`,
  `overageResetsAt`, `surpassedThreshold`, `utilization`)
  un-captured. Governance signal, not cost.
- **G6** — `modelUsage.<model>.{contextWindow, maxOutputTokens}`
  decoded but never propagated to TokenEvent metadata.

### Verification gates (all green)

- `go test -race ./... -count=1` 51/51 packages
- `go vet ./...` clean
- New regression tests: 4 in `internal/adapter/cowork/audit_2026_05_19_test.go`
  (1 actionMap mapping case × 9 names, 1 image-only-user-message
  case, 1 permission-events case, 1 compact-boundary case)
- No migrations (all changes are parser-level)

## [1.6.14] — 2026-05-19

### fix(codex,opencode,openclaw): follow through audit findings

Three adapter audits ran on a Windows-host live corpus during the
v1.6.13 ship; their findings are landed in this patch. Audit
documents:
[`docs/codex-audit-2026-05-18.md`](docs/codex-audit-2026-05-18.md),
[`docs/opencode-audit-2026-05-19.md`](docs/opencode-audit-2026-05-19.md),
and [`docs/openclaw-audit-2026-05-19.md`](docs/openclaw-audit-2026-05-19.md).
A separate implementation tracker
([`docs/codex-audit-followthrough-plan-2026-05-19.md`](docs/codex-audit-followthrough-plan-2026-05-19.md))
captures which findings are closed here versus left as historical
DB / dashboard repair work.

#### fix(codex): forked-rollout session ownership + Observer MCP taxonomy

- **C1 — session-meta ownership bug in forked rollouts.** When a
  Codex rollout carried a second `session_meta` envelope replaying
  the parent session id (real specimen:
  `rollout-2026-05-06T02-38-04-019df9f8-…`), `applyContext()`
  blindly overwrote the file's owning `SessionID`. The static
  two-file reproducer landed **1,783 of 1,784** child-file actions
  + all **460** child-file token rows onto the parent session id;
  the parent file then dropped to **0** token rows by source-file
  because tuple-dedup collapsed them against the replayed copy.
  Fix: session ownership is now file-local — the first real
  `SessionID` seen in a file wins, and later replayed
  `session_meta` records can still refresh prompt / cwd / model
  / branch context but no longer move the owning id. The watcher
  resume path (`prefetchSessionContext` → `mergeSessionContext`)
  mirrors the same rule, so a resumed parse can't reintroduce the
  split. New regression test:
  `TestParseForkedRolloutSessionMetaOwnership`.
- **C3 residual — Observer MCP helper taxonomy.** Eight remaining
  Observer-MCP function-call names were still landing as
  `ActionUnknown`: `search_past_outputs`, `get_session_summary`,
  `get_project_patterns`, `get_last_test_result`,
  `get_session_recovery_context`, `get_cost_summary`,
  `check_command_freshness`, `get_failure_context`, plus
  `load_workspace_dependencies`. All now map to `ActionMCPCall`.
  New regression test: `TestActionMap_ObserverMCPHelpers`.

Codex findings C2 (live DB / dashboard residue from historical
parser repairs) and C4 (FTS5 corruption in the live maintainer DB)
remain operational repair work — not part of this adapter patch.

#### fix(opencode): apply_patch underscore variant

- **O1.** The live OpenCode corpus emits `tool="apply_patch"` with
  an underscore, but `mapTool()` only handled the no-underscore
  spelling `applypatch`. **5/5** real edit-file actions in the live
  corpus were landing as `ActionUnknown`. Added `apply_patch` to
  the `ActionEditFile` branch. `internal/adapter/opencode/doc.go`
  expanded so the field-map decisions the audit had to
  reverse-engineer (authoritative token source =
  `message.data.tokens`; `step-finish` intentionally ignored as
  duplicate) are documented at the package boundary.

OpenCode findings O2 (six historical assistant-text rows missing
from the live DB), O3 (twelve obsolete `prompt-history` /
`turn-complete` rows still in live DB from the retired
desktop-global ingest path), and O4 (`sessions.started_at` stuck
22m late on one session because store policy treats it as
immutable on conflict) are live-data drift that a normal `scan
--force --adapter opencode` only partially repairs; they remain
historical-cleanup follow-up.

#### fix(openclaw): session identity + cross-mount project root + tool taxonomy

- **B2 — session identity.** `parseSessionsIndex()` was using
  `sess.SessionID` (the raw provider id) while the JSONL and
  `task_runs` paths used the alias / session-key form via
  `lookupSessionAlias()` / `sessionID(tr)`. Live consequence on
  the maintainer corpus: **5 OpenClaw sessions** in the live DB
  for only **3 logical** OpenClaw sessions, with the prompt + API
  error rows split away from the completion row on
  `observer-ollama-smoke`. New `canonicalSessionID()` helper
  prefers `systemPromptReport.sessionKey`, then the index key,
  then the raw provider id — matching the JSONL / `task_runs`
  ordering. New regression test:
  `TestParseSessionFile_SessionsIndexUsesCanonicalSessionKey`.
- **B3 — cross-mount project-root translation missing.**
  `resolveProjectRoot()` was calling `git.Resolve(cwd)` directly,
  with no `crossmount.TranslateForeignPath()` translation and no
  stat gate first. Live consequence: **all 5** OpenClaw sessions
  attributed to the placeholder project root `[openclaw]` even
  though their source metadata pointed at a real Windows-side git
  repo `C:\Users\marmu\.openclaw\workspace`. Fix mirrors the
  Claude Code / Codex / Cowork cross-mount pattern: translate
  first, stat-gate the translated path, fall through to
  `git.Resolve()` only when the path is locally reachable. New
  regression test: `TestResolveProjectRoot_PreservesUnreachableForeignPath`.
- **B5 — tool-name coverage gap.** The live Gemma4 session's
  `systemPromptReport.tools.entries` roster declared 23 tool
  names; `mapToolName()` covered only 13. The remaining 10
  (`canvas`, `cron`, `memory_get`, `message`, `nodes`, `process`,
  `session_status`, `sessions_yield`, `subagents`, `tts`) were
  guaranteed to land as `ActionUnknown` whenever they hit the
  JSONL. `process` is a shell-execution tool — mapped to
  `ActionRunCommand`. The other nine map to `ActionMCPCall`.

**Two new historical-repair backfill flags** for openclaw rows
already in the live DB:

- `--openclaw-session-id` — collapses split sessions where
  `sessions.json` historically emitted the raw provider id. Walks
  the user's `<home>/.openclaw/agents/**/sessions.json` files,
  computes the canonical id for each entry, and merges rows on
  `actions`, `token_usage`, and `sessions` onto the alias form.
  Returns per-pass counts: alias files scanned, session rows
  merged, action rows updated, token-usage rows updated, orphan
  session rows deleted.
- `--openclaw-project-root` — re-attributes historical action /
  session rows whose project previously collapsed to `[openclaw]`
  or to a foreign-OS literal. Uses the same translate-first /
  stat-gate / git.Resolve path as the live parser, so a re-run is
  idempotent.

`--openclaw-action-types` extended to also cover the new mappings
(`process → run_command`; `canvas` / `cron` / `memory_get` /
`message` / `nodes` / `session_status` / `sessions_yield` /
`subagents` / `tts` → `mcp_call`).

Both new flags participate in `--all`. Test coverage added in
`cmd/observer/backfill_test.go` for the two new passes and the
extended action-types SQL.

OpenClaw findings B1 (mutable-snapshot `sessions.json` / `task_runs`
ghost rows whose source counterpart no longer exists), B4
(aggregate token fields on `sessions.json` not used as a recovery
fallback when JSONL is missing), and X1 (historical
`openclaw.assistant_text` rows that need a rescan to land) remain
deferred — B1 needs a reconcile design, not a one-line parser fix.

### Verification gates (all green)

- `go test -race ./... -count=1` 51/51 packages
- `go vet ./...` clean
- New regression tests: `TestParseForkedRolloutSessionMetaOwnership`,
  `TestActionMap_ObserverMCPHelpers`,
  `TestResumePreservesSessionContext`,
  `TestParseSessionFile_SessionsIndexUsesCanonicalSessionKey`,
  `TestResolveProjectRoot_PreservesUnreachableForeignPath`,
  expanded `TestMapToolName_*` coverage, plus the two new backfill
  test functions for `--openclaw-session-id` and
  `--openclaw-project-root`
- No migrations (all changes are parser-level or SQL-only repair
  in the backfill command)

## [1.6.13] — 2026-05-19

### fix(copilotcli) + feat(dashboard): events.jsonl schema-audit follow-through + session-detail Models Used panel

Two coherent things land together in this patch. The Copilot CLI
parser bugs and the new dashboard panel are independent — the panel
reads a field the API has been emitting since v1.5.0 — but they
share a release because both are short and ship-ready.

#### feat(dashboard): Models Used panel on session detail

The session detail slide-over already gets `per_model` on every
`/api/session/<id>` response (one bucket per model with token
breakdown + 3-way cost split + turn count), but the React rewrite
never rendered it — so a session that used both haiku and opus
showed per-message model attribution in the Messages table but no
session-level rollup. Operator-flagged.

Fix: new `ModelsUsedPanel` component sits as the third tile in the
`[Action breakdown] [Token buckets]` band. Style:

- One horizontal stacked bar per model with **bar LENGTH encoding
  magnitude** (not normalized to 100%) — the most-expensive model
  takes the full track; everyone else is proportional. A 99%-vs-1%
  cost split is immediately visible.
- **Segments colored by token bucket** (input / cache read / cache
  write / output) using the exact same colors as the adjacent
  `TokenBucketsPanel` — same hue means the same thing across the
  slide-over.
- **`$ / tokens` `SegmentedControl` toggle** flips the encoded
  metric. Cost mode shows per-bucket dollars (dominated by output);
  tokens mode shows raw counts (typically dominated by cache reads).
  Same colors, same rows, re-proportioned bars.
- Per-row sub-line: turn count + the inverse metric (so cost mode
  shows token totals below, tokens mode shows the dollar total) +
  optional tool-cost.
- Shared 4-bucket legend below the panel.
- Top 6 models visible, "+N more" footer when truncated. Empty
  state names the missing dependency: "no model attribution
  captured for this session yet."

Layout: grid widened from `xl:grid-cols-2` → `lg:grid-cols-2
xl:grid-cols-3` so on wide viewports all three panels sit side by
side; below xl, the third tile wraps under the first two.

**Cost-engine extension to feed the panel:**
`cost.CostBreakdown` now carries four per-bucket components —
`InputCost`, `OutputCost`, `CacheReadCost`, `CacheCreationCost` —
populated by `ComputeBreakdown`. Their sum equals `AICost` on every
reachable shape (pinned by
`TestComputeBreakdown_PerBucketComponentsSumToAICost` across 5
bundle variants). Reasoning tokens (billed at output rate) fold
into `OutputCost` so the four buckets always cover the full AI
bill. `/api/session/<id>`'s `per_model` rows expose them as
`input_cost_usd`, `output_cost_usd`, `cache_read_cost_usd`,
`cache_creation_cost_usd`. Adapters that emit recorded costs
(OpenCode/Pi) leave the buckets zero — the `$`-mode bar collapses
to nothing in that case, which is accurate to the data we have.

#### fix(copilotcli): events.jsonl schema-audit follow-through

Acts on the operator-authored deep schema review at
[`docs/copilot-cli-events-schema-audit-2026-05-19.md`](docs/copilot-cli-events-schema-audit-2026-05-19.md)
against a 25,320-line / 28-event-type Copilot CLI specimen. The
v1.6.6–v1.6.8 fixes already covered the load-bearing token
payloads; this audit surfaced two real parser bugs plus three
high-value schema gaps under the reliability / permission-
auditability / project-attribution dimensions. All five land in
this patch.

**B1 — Failed-tool error message extraction.** Failed
`tool.execution_complete` events carry the actual error text at
top-level `data.error.message`, NOT at `result.content`. Empirically
**158/158** failures in the specimen follow the new shape. The pre-fix
parser read `result.content` only, so every failed tool row emitted an
empty `error_message` column — weakening both dashboard review and
observer-side failure search. Fix: add `Error` field to
`toolExecutionCompleteData`; on failure prefer `data.error.message`,
fall back to `result.detailedContent` then `result.content`
(defensive against future shape drift).

**B2 — Permission events are shell-command shaped.** All **3**
permission requests in the specimen carry `kind="shell"` with
`fullCommandText`, `commands[]`, `possiblePaths[]`,
`hasWriteFileRedirection`, `canOfferSessionApproval` — none carry
the legacy `fileName`/`diff` fields. Pre-fix parser sourced
`RawToolInput` from `FileName` only, so the actual command being
approved was lost. Also: `permission.completed.result.kind` has a
new `"approved-for-location"` value that the pre-fix `Success ==
"approved"` check marked as a denial. Fix: extend `permissionRequest`
with the full shell-shape field set (file-shaped fields stay for
backward compat); `RawToolInput` priority is now FullCommandText >
FileName > Intention; `Success` is now `strings.HasPrefix(kind,
"approved")`; granularity (`approved-for-location` / -session) plus
the `locationKey` scope land in new `ActionMetadata` fields
(`PermissionApprovalKind`, `PermissionLocationKey`).

**G1 — `session.resume` state is now consumed.** All **9** resume
events in the specimen carry `selectedModel` + `reasoningEffort` +
`context.{cwd, gitRoot?, branch?, repository?}`. Pre-fix
`dispatchState` ignored them entirely — a long-lived session where
the user re-opened on a different model (without a later
`session.model_change`) silently mis-attributed every subsequent
`assistant.message`. Fix: new `sessionResumeData` struct + case in
`dispatchState`, mirroring `session.start`'s field-by-field updates
(non-empty wins, empty preserves prior context).

**G2 — Workspace.yaml-wins-if-present for project-root resolution.**
The specimen's `session.start.context.cwd` is `"E:\\"` (drive root)
while actual file edits target `"E:\\opencell\\..."`. Action rows
derived from `events.jsonl` would normalize to the drive root.
Fix: before falling back to `resolveProjectRoot(st)` (which reads
the event stream), `parseEventsJSONL` now consults the sibling
`workspace.yaml` at `<copilot-root>/session-state/<sessionID>/
workspace.yaml` — when present and carrying a usable `git_root` or
`cwd`, it wins over the event stream. Extracted the existing
`resolveProjectFromSibling` helper into a reusable
`resolveProjectFromWorkspaceYAML(yamlPath)` so both `parseProcessLog`
and `parseEventsJSONL` share one yaml-reading path.

**MCP enrichment — explicit server attribution.** **84/4,443**
`tool.execution_start` events in the specimen carry `mcpServerName`
+ `mcpToolName`. Some MCP servers don't follow the
`github-mcp-server-*` toolName convention (e.g. `ide-get_selection`
via `mcpServerName="ide"`) and would classify as `ActionUnknown`.
Even when the bare toolName already matches a built-in classifier
(e.g. `web_search` via `github-mcp-server`), dashboards benefit from
explicit server-name preservation. Fix: add `MCPServerName` +
`MCPToolName` to `toolExecutionStartData`; new `classifyToolName`
helper promotes `ActionUnknown` to `ActionMCPCall` when an MCP server
is named; new `composeRawToolName` helper emits `<server>:<tool>`
(stripping the legacy `<server>-` prefix when present, so we never
double-tag).

**ActionMetadata schema additions** (Invariant #50 covered by the
existing reflection test):

- `permission_approval_kind` — captures non-default approval
  granularity (`approved-for-location`, `approved-for-session`).
  Empty for plain `"approved"` so the column doesn't churn.
- `permission_location_key` — the filesystem scope bound to an
  `approved-for-location` grant (e.g. `"D:\\OneDrive - Microsoft"`).

**Files changed:**

| File | Lines | What |
|---|---|---|
| `internal/adapter/copilotcli/events.go` | +224 / −20 | B1 + B2 + G1 + MCP struct/handler/helpers |
| `internal/adapter/copilotcli/log.go` | +18 / −2 | Extract `resolveProjectFromWorkspaceYAML` from `resolveProjectFromSibling` |
| `internal/adapter/copilotcli/adapter_test.go` | +471 / 0 | 13 new tests across B1+B2+G1+G2+MCP |
| `internal/models/models.go` | +15 / −1 | Two `ActionMetadata` fields + `IsZero` extension |
| `internal/intelligence/cost/engine.go` | +30 / −10 | Per-bucket components on `CostBreakdown` (input/output/cache_read/cache_creation); `ComputeBreakdown` restructured to populate them while preserving the AICost / ToolCost / Total invariants |
| `internal/intelligence/cost/engine_test.go` | +90 / 0 | Two new tests pinning the sum-to-AICost invariant across 5 bundle shapes + reasoning-folds-into-output-cost |
| `internal/intelligence/dashboard/dashboard.go` | +21 / 0 | Four `*_cost_usd` fields on `modelBucket` + per-row accumulation from `ComputeBreakdown`'s new components |
| `web/src/components/SessionDetailPanel.tsx` | +242 / −2 | New `ModelsUsedPanel` (stacked horizontal bars per model, bar length = magnitude, segments colored by token bucket, `$ / tokens` SegmentedControl) + grid extension (`xl:grid-cols-2` → `lg:grid-cols-2 xl:grid-cols-3`) |
| `web/src/lib/types.ts` | +9 / 0 | Four `*_cost_usd` fields on `SessionModelBucket` matching the new backend payload |
| `internal/intelligence/dashboard/webapp/dist/**` | regenerated | `make web-build` output |

**Verification:** `go test -race ./... -count=1` 51/51 packages green.
13 new tests added to `internal/adapter/copilotcli/adapter_test.go`,
all passing. `gofmt -l` clean. The `ActionMetadata` reflection
invariant test (`TestActionMetadata_IsZeroCoversEveryField`)
automatically covers both new fields.

**No migrations.** All changes are parser-level + struct-level —
the existing `actions.metadata` JSON column already serializes any
`ActionMetadata` shape.

**Deferred to a follow-up release** (audit §3 G3/G4/G5):
session-level product telemetry (`totalPremiumRequests`,
`totalApiDurationMs`, `codeChanges.*`, per-model `requests.*`),
compaction summary content / checkpoint metadata, and new event
lanes (`session.task_complete`, `session.error`). These need a
larger schema/UX decision and don't gate the bug fixes here.

## [1.6.12] — 2026-05-19

### fix(test): CI regressions surfaced by v1.6.11 release pipeline

Two test failures hit the v1.6.11 GitHub Actions release workflow
(`go test -race ./...`). Patch release fixes both. No production
code changes — only test fixes — so the v1.6.11 binaries +
migrations are unaffected.

**T1 — Data race in `TestBackfillJobsList_ReturnsRunningAndCompleted`**
(introduced in v1.6.11, Issue #1). The test mutated `server.execBackfill`
between two `handleBackfillRun` calls so the second job would finish
fast while the first was still sleeping. But the first job's
goroutine (spawned by `handleBackfillRun` → `runBackfillJob`) reads
`server.execBackfill` concurrently — `-race` flagged the write-vs-read
unsynchronized field access. The race only matters in CI where the
race detector is enabled; local `go test` (no -race) passed.

Fix: restructure to ONE mode-aware fake installed at construction.
The fake inspects its `args` for `--all` and sleeps 200ms in that
branch; everything else returns immediately. Both jobs use the
same fake — no field mutation, no race.

**T2 — `TestAnalysisCacheSavingsTrend_DailySavingsAttribution` straddles
midnight UTC** (pre-existing latent test bug, fired on 2026-05-19
because CI happened to run between 23:00 and 00:00 UTC). The test
anchors `dayA := time.Now().UTC().Add(-3 * 24 * time.Hour)`, then
inserts a second Sonnet turn at `dayA.Add(1 * time.Hour)`. When
the wall clock is within an hour of UTC midnight, `dayA.Add(1h)`
lands on the next calendar day → one of the two day-A turns gets
attributed to day B's bucket. Symptom: day A savings = $0.27
(half of expected $0.54), day B savings = $0.495 (= $0.27 + $0.225
instead of $0.225), day B cache_read_tokens = 150K (= 100K + 50K
instead of 50K).

The bug is in the test, not the cost engine — totals reconcile
($0.27 + $0.495 = $0.54 + $0.225 = $0.765). The day-attribution
shift is the symptom of timestamp drift across the midnight
boundary.

Fix: anchor `dayA` / `dayB` at noon UTC via
`.Truncate(24 * time.Hour).Add(12 * time.Hour)`. Adding ±1–2 hours
to a noon timestamp can never cross a calendar-day boundary, so
the test is now time-of-day-independent. Verified at 23:14 UTC
(the failing time) where the unpatched test fails reliably and
the patched test passes.

Other `time.Now().UTC().Add(-N * 24 * time.Hour)` sites in
`analysis_test.go` were audited (lines 484, 532, 628, 809, 904,
961, 978, 1018, 1662): none combine a same-day anchor with an
intra-day `.Add(hours)` follow-up that could straddle midnight.
Line 628 uses hour-bucket aggregation (not day-bucket) so its
`.Truncate(time.Hour)` is sufficient. No further fixes needed.

**Files changed:**

| File | Lines | What |
|---|---|---|
| `internal/intelligence/dashboard/backfill_run_test.go` | +24 / −13 | T1 — mode-aware fake, no field swap |
| `internal/intelligence/dashboard/analysis_test.go` | +10 / −2 | T2 — noon-UTC anchor, comment explaining why |

**Build state:** `go test -race ./... -count=1` 51/51 packages green
(verified locally at 23:14 UTC, the same wall-clock condition that
broke CI). No production code changes — v1.6.11's binaries,
migrations, and live-DB state are all unchanged.

## [1.6.11] — 2026-05-19

### fix(observer): handover §1 punch list + deferred follow-throughs + e371fdb1 root cause

Operator-flagged 6 issues from a `observer backfill --all` run on
2026-05-19 (handover doc:
[`docs/next-session-handover-2026-05-19.md`](docs/next-session-handover-2026-05-19.md)).
Tackled all 6 in handover §4 order, then closed out 5 explicitly-
deferred follow-throughs from those passes, then root-caused the
"e371fdb1 antigravity session not at all being picked up"
regression that surfaced during operator-side post-deploy
verification. 13 distinct fixes total; 3 new migrations; ~35 new
tests across 8 packages.

**#2 — SQLITE_BUSY contention between watcher + backfill** *(operator-
blocking).* `observer backfill --all` against the live watcher
process produced `store.InsertActions: upsert dup: database is
locked (5) (SQLITE_BUSY)` on claudecode-user-prompts and other
multi-thousand-row passes. Two layers of fix in
`internal/db/db.go`: (1) default `BusyTimeout` raised 5s → 30s
(matches the migration runner's own dedicated busy_timeout); (2)
DSN now includes `_txlock=immediate` so every BeginTx acquires
the SQLite write lock upfront. The previous BEGIN DEFERRED took a
read lock at BeginTx then upgraded on first write — when two
writers raced that upgrade, one got SQLITE_BUSY immediately
(busy_timeout doesn't kick in on upgrade-deadlocks). IMMEDIATE
serializes writers through the file lock so busy_timeout's
exponential backoff handles contention properly. All four BeginTx
callers in the codebase (`store.InsertActions`,
`store.InsertTokenEvents`, `retention.deleteActionsOlder`,
`indexing.EmbedBatch`) are write-only so the IMMEDIATE upgrade is
always correct. New `TestConcurrentWritersSurviveContention`
(4 writers × 25 txs × 20 rows hammered against the same on-disk
DB) pins it.

**#1 — Dashboard backfill running-status disappears on navigation.**
Settings > Backfill's running indicator was destroyed on component
unmount because the jobs state lived only in local React state.
New `GET /api/backfill/jobs` endpoint returns every in-flight + recent
job from the in-memory registry, newest first. `BackfillSection`'s
new mount-time `useEffect` re-fetches the list and reduces it to
one-job-per-mode (matching the UI's single-job-per-mode model).
The 3-second polling loop picks up running entries automatically.
3 backend tests (`TestBackfillJobsList_Empty`, `_ReturnsRunningAndCompleted`,
`_RejectsNonGet`).

**#6 — PowerShell + codex `exec_command` normalized.** Bash-family
shell commands across all tools got `action_type='run_command'`
via the `Bash` tool name mapping, but PowerShell / pwsh / cmd.exe
(Windows-side claude-code, copilot-cli powershell tool) fell through
to ActionUnknown — silently dropping out of the dashboard's
`run_command`-filtered views. **Codex's modern `exec_command` tool
name (260 historical maintainer-corpus rows!) was also never
mapped.** Cross-adapter sweep: added shell-interpreter variants
(`PowerShell`, `powershell`, `pwsh`, `cmd`, `cmd.exe`, `sh`) to
the `actionMap` / `mapToolName` of all 9 adapters that handle
shell tools (claudecode, codex, copilotcli, cowork, cline, cursor,
gemini, antigravity, openclaw, copilot, pi, opencode). Migration
`023_powershell_action_type_backfill.sql` re-derives historical
unknown rows (262 maintainer rows: 260 codex exec_command, 1
claudecode PowerShell, 1 copilot-cli powershell). 3 tests pin
the new mappings + the cross-adapter scope guard.

**#4 — Antigravity backfill perf (persistent unrecoverable cache).**
`observer backfill --antigravity-rescan` re-attempted the ~30s
decrypt+gRPC dance on the same handful of structurally-unrecoverable
.pb files on every invocation, since the parse cursor at fi.Size()
makes the watcher skip them but Rescan() ignores cursors. Two-stage
implementation: v1.6.11 first pass added an in-memory cache on the
Adapter struct (helps the `observer start` daemon's dashboard-
triggered Rescans but not one-shot CLI). v1.6.11 final pass replaces
that with a persistent table (migration `025_adapter_unrecoverable_files.sql`):
composite PK `(adapter, source_file)` + size + mtime + reason +
last_attempted_at. New `store.Lookup/Mark/ClearUnrecoverable`
methods. Antigravity adapter now consults the tracker via the
`UnrecoverableTracker` interface (keeps the adapter store-free per
CLAUDE.md), wired in `cmd/observer/main.go` via
`antigravityTrackerShim`. **Now helps `observer backfill --all`
CLI** (the operator's primary case). 7 tests across adapter
(fakeTracker-backed) and store (size+mtime drift, adapter-scoping).

**#5 — Antigravity session `e371fdb1` missing tokens/model** *(this
is the bug the operator hit after deploying v1.6.11-first-pass).*
The probe (one-off test guarded by `PROBE_E371=1`) captured the
gRPC wire bytes from both broken (pid=1339) and correct (pid=1340)
language_servers. **The bug was NOT in `ParseStructuredTrajectory`**
(verified: parser correctly extracts model=`claude-sonnet-4-6` +
17 token rows + 25 tools from the real wire bytes). The bug was
in `recoverViaLocalGRPC`'s empty-stub guard: `numEvents(merged_result) == 0`
checked the MERGED (markdown + structured) event count. A wrong-
workspace language_server returns a 244-byte stub markdown that
parses as 1 fake `user_prompt` event → numEvents = 1 → guard
never fired → wrong server accepted before the correct server
(pid=1340) was tried. **New `isWrongWorkspaceStub(enrichment)`
predicate** checks the structured payload directly
(`Model == "" && len(TokenEvents) == 0 && len(ToolEvents) == 0`) —
a server that hosts the conversation always populates at least
the model name (verified across 122 working sessions in the
maintainer corpus). Applied in both iteration loops (linux-native
+ native non-WSL) so the fix is systemic, not specific to
e371fdb1. Live verify on the maintainer DB: e371fdb1 went from
**1 action / 0 tokens / model="" / wrong project** to **31 actions
/ 17 token rows (17,595 input + 5,074 output + 541,170 cache_read)
/ model="claude-sonnet-4-6" / project "/home/marmutapp/superbased-observer"**.
Also added a debug `dump_shape_mismatches_dir` config option in
`[observer.antigravity]` that writes raw gRPC bytes to disk on
`structured_shape_mismatch` (≥10 KiB payload + 0 tokens + empty
model) — operator-runnable wire-byte capture for future proto-
path investigations. 3 tests (`TestIsWrongWorkspaceStub` 6-case
truth table, `TestParseStructuredTrajectory_E371fdb1WireBytes`
fixture-backed regression, `TestDumpShapeMismatchPayload_WritesWhenConfigured`).

**Session.project_id stickiness** *(surfaced via e371fdb1 fix
verification).* After the routing fix correctly re-ingested
e371fdb1 with project 307 (`/home/marmutapp/superbased-observer`),
the session row's `project_id` stayed pinned at 109
(`/home/marmutapp/superbased` — from the earlier buggy ingest).
Root cause: `store.UpsertSession`'s `ON CONFLICT DO UPDATE SET`
clause omitted `project_id`, so once a session was created with a
wrong project_id (from ANY adapter, ANY buggy ingest) it stayed
stuck. Fix: always overwrite project_id on conflict. Cross-project
session resumes aren't supported; adapters derive project_root
from per-file metadata so the latest value is always the most
accurate. New `TestUpsertSession_ProjectIDChangesOnReingest` pins
the contract including COALESCE preservation for other fields.

**Claude Code historical-row backfill** *(Issue #6 extension).* Live
DB had thousands of `action_type='unknown'` rows for Claude Code
tools that are MAPPED in the adapter's actionMap today but were
ingested before the mapping landed. Added `TodoWrite` +
`EnterPlanMode` + `ExitPlanMode` to the actionMap, then migration
`024_claudecode_action_type_backfill.sql` re-derives historical
unknowns scoped to `tool='claude-code'`. Live verify: **5,909 rows
re-tagged** (TaskUpdate: 3499, TaskCreate: 1844, Agent: 276,
AskUserQuestion: 128, TaskOutput: 84, ExitPlanMode: 22, TaskList:
20, TodoWrite: 16, TaskStop: 11, EnterPlanMode: 9). 16-case
migration test including the cross-tool scope guard.

**#3 — Backfill flag surface audit + `--dry-run`.** Full audit at
[`docs/backfill-flag-audit-2026-05-19.md`](docs/backfill-flag-audit-2026-05-19.md):
27 flags categorized into 4 buckets (schema-migration, cross-mount
reattribution, per-adapter rescans, historical adapter-parity).
Recommendation: no flag removals in this release — annotate help
text instead. Help-text rewrite in `cmd/observer/backfill.go` groups
flags under 4 visual headers per the audit. **`--dry-run` flag**
implemented via the simpler DB-copy approach from the audit:
snapshots the live DB via SQLite's atomic `VACUUM INTO`, overrides
`OBSERVER_OBSERVER_DB_PATH` env so downstream `config.Load` calls
route to the snapshot, runs backfills normally, cleans up snapshot
+ WAL/SHM siblings on exit. Live DB untouched; the row counts in
the final summary reflect what WOULD have updated. 2 tests
(snapshot+redirect+cleanup contract; overwrite-guard).

**Migrations:**

- `023_powershell_action_type_backfill.sql` — shell-variant tool
  names re-tagged across adapters. Codex exec_command rule scoped
  to `tool='codex'` only (defensive against future adapters
  reusing the name).
- `024_claudecode_action_type_backfill.sql` — historical Claude
  Code unknowns re-derived per current actionMap. Tool-scoped to
  `claude-code`.
- `025_adapter_unrecoverable_files.sql` — new table backing the
  persistent unrecoverable-file tracker. Composite PK
  `(adapter, source_file)`; size+mtime pin file identity at failure.

**Files changed:**

| File | Lines | What |
|---|---|---|
| `internal/db/db.go` | +28 / −3 | SQLITE_BUSY fix (busy_timeout 30s + _txlock=immediate) |
| `internal/db/db_test.go` | +316 / 0 | Migration 023+024 tests + concurrent-writer regression |
| `internal/intelligence/dashboard/backfill_run.go` | +35 / +1 | New /api/backfill/jobs endpoint |
| `internal/intelligence/dashboard/dashboard.go` | +1 | Route registration |
| `internal/intelligence/dashboard/backfill_run_test.go` | +120 / 0 | 3 list-endpoint tests |
| `web/src/lib/types.ts` | +4 | `BackfillJobsListResponse` type |
| `web/src/pages/Settings.tsx` | +30 | Mount-time refetch + state restore |
| `internal/adapter/{12 adapters}` | varied | PowerShell + exec_command + TodoWrite + EnterPlanMode + ExitPlanMode added to mappings |
| `internal/adapter/antigravity/adapter.go` | +245 / −20 | UnrecoverableTracker + isWrongWorkspaceStub + dumpShapeMismatchPayload |
| `internal/adapter/antigravity/adapter_test.go` | +360 / 0 | fakeTracker + wire-bytes fixture + stub-classifier truth table |
| `internal/adapter/antigravity/probe_e371fdb1_test.go` | NEW | Diagnostic probe (PROBE_E371-gated) |
| `internal/store/store.go` | +94 / −2 | UpsertSession project_id update + Unrecoverable methods |
| `internal/store/store_test.go` | +213 / 0 | UpsertSession project_id test + Unrecoverable tests |
| `internal/config/config.go` | +20 | DumpShapeMismatchesDir field |
| `cmd/observer/main.go` | +41 | antigravityTrackerShim + WithUnrecoverableTracker/WithShapeMismatchDumpDir wiring |
| `cmd/observer/backfill.go` | +246 / −1 | --dry-run flag + setupBackfillDryRun + snapshotSQLiteDB + restructured help |
| `cmd/observer/backfill_test.go` | +157 / 0 | dry-run tests |
| `internal/db/migrations/023_powershell_action_type_backfill.sql` | NEW | Codex exec_command + PowerShell backfill |
| `internal/db/migrations/024_claudecode_action_type_backfill.sql` | NEW | Claude Code unknowns backfill |
| `internal/db/migrations/025_adapter_unrecoverable_files.sql` | NEW | Unrecoverable-file tracker table |
| `docs/backfill-flag-audit-2026-05-19.md` | NEW | 27-flag categorization + dry-run spec |

**Build state:** `go vet ./...` clean; `go test ./...` 51/51 packages
green; `make web-build` clean (embedded dist regenerated). Live-DB
verification gates after restart on the maintainer DB: 5,909+
historical Claude Code unknowns re-tagged via 024; 260 codex
exec_command rows re-tagged via 023; e371fdb1 reattributed
correctly (31 actions / 17 token rows / claude-sonnet-4-6 model /
superbased-observer project). The wrong-workspace-stub fix is
systemic — any future conversation hosted by a non-first
language_server is routed correctly automatically.

## [1.6.10] — 2026-05-19

### fix(claudecode)+fix(store): five-bug claude-code audit follow-through

Operator-requested deep audit of the claude-code adapter
(`internal/adapter/claudecode/`) against the live maintainer corpus
(42,179 token_usage rows / 305 sessions, 2026-02-08 → 2026-05-18,
spanning 215 WSL-native and 90 Windows-side captures). Five bugs
surfaced + a stale doc-comment. All six fixed in this release. Audit
report: [`docs/claude-code-audit-2026-05-18.md`](docs/claude-code-audit-2026-05-18.md);
empirical scope: [`docs/claude-code-audit-2026-05-18-scope.md`](docs/claude-code-audit-2026-05-18-scope.md);
methodology follows
[`docs/adapter-audit-playbook.md`](docs/adapter-audit-playbook.md)
(v1.6.8).

**The billing-accuracy regroup gate ([CC4]) passed 36/36 sessions
bit-for-bit** across `input_tokens` / `output_tokens` /
`cache_read_tokens` / `cache_creation_tokens` / `cache_creation_1h_tokens`
on every parent-absent acompact-only session. No regroup needed —
the v1.6.5 tuple dedup is doing the right thing on the canonical
billing case.

**B1 — Windows-capture `project_root` misattribution.**
`resolveProjectRoot` at `adapter.go:686-700` passed raw `cwd` to
`git.Resolve` without first translating Windows-style paths
(`C:\programsx\superbased`) via `crossmount.TranslateForeignPath`.
`git.Resolve` calls `filepath.Abs`, which on Linux prepends the
observer's own CWD and walks parents for `.git` — landing on the
observer's own repo. Result: **100% of Windows claude-code captures
(90 sessions / 3,536 token_usage rows / 7,219 actions across 4
distinct Windows projects) misfiled** under `/home/marmutapp/superbased-observer`
in the dashboard's project view. Session_id and per-row token counts
were correct; only the project grouping was wrong. Fix is a one-line
port from the codex adapter (`internal/adapter/codex/adapter.go:2494`,
mirrors `#54` / memory `[[feedback-foreign-path-git-resolve]]`):
call `cwd = crossmount.TranslateForeignPath(cwd)` BEFORE `git.Resolve`.
Backfill via new `observer backfill --claudecode-project-root` flag
(see V7a-bf below) reattributed **84 of the 90** Windows sessions
(7,219 actions updated). 15 sessions are unrecoverable — their
source JSONLs have rotated off disk, so no `cwd` is available to
re-resolve.

**B2 — Cross-source-file `message_id` double-count (snapshot drift).**
When the same `message_id` appeared in both a parent JSONL and an
`agent-acompact-*.jsonl` snapshot with **different** cumulative
token counts, both rows survived the store-layer v1.6.5 tuple dedup
(which only collapses byte-identical tuples). Pattern: the acompact
subagent file captures an in-flight API turn at a LATER cumulative
state than the parent file's earliest matching row — output_tokens
differs, byte-identical dedup misses, cost engine sums both. Pattern
verified: 100% of duplicate-msgid rows were exactly one parent-file
row + one acompact-subagent row (no three-way splits). **Corpus
impact: 96 msgids across 2 sessions (`853e0bc0`, `c242775f`), +2,401
output tokens / +122 input tokens** (~$0.06 at Opus rates). Small
in dollars, but per operator's absolute-accuracy bar this fails the
gate. Fix: new post-batch sweep in `store.InsertTokenEvents` at
`store.go:679-705` that deletes per-(tool, session_id, message_id,
source) all but the row with the highest output_tokens (ties broken
by higher id for determinism). **Scoped to `tool='claude-code'`
only** — codex emits LEGITIMATE per-turn delta `token_count` rows
that must NOT be collapsed (pinned by
`TestInsertTokenEvents_TupleDedupPreservesDistinctRows`), and
source-scoping preserves the legitimate proxy+jsonl complementary
coverage (pinned by `TestInsertTokenEvents_ClaudecodeCrossSourceProxyJsonlSurvive`).
Backfill via migration 021 cleaned the 96 historical rows; idempotent
on re-run.

**B3 — `server_tool_use.web_search_requests` never decoded
(forward-looking).** The adapter's `rawUsage` struct had no
`ServerToolUse` field. The cost engine's `TokenEvent.WebSearchRequests`
was populated correctly by other adapters (cowork at
`adapter.go:235,847`) but remained zero for claude-code's JSONL
path. **Current corpus impact: zero** — disk-wide scan across all
305 sessions found zero `web_search_requests > 0`. **Forward-looking
impact:** every future claude-code WebSearch call would silently
drop the $0.01/call flat fee (memory
`[[feedback-web-search-rate-flat-0p01]]`). Fix: add the
`ServerToolUse` struct to `rawUsage`; route `WebSearchRequests` +
`WebFetchRequests` into the TokenEvent at construction.

**B4 — Four metadata line types silently dropped.** Live captures
emit at least 10 top-level `type` values (per scope doc §2a); the
adapter's early-`continue` at `adapter.go:388-389` on
`len(line.Message) == 0` was dropping four that carry actionable
metadata. Operator confirmed 2026-05-18 these are oversights, not
deliberate skips:

| Type | What it carries | Why it matters |
|---|---|---|
| `agent-name` | `agentName` (subagent persona) | Closest analog to Copilot CLI's `agentId` attribution — surfaces "which subagent did what" for Task tool calls. |
| `system.compact_boundary` | `compactMetadata.preTokens`, `preCompactDiscoveredTools` | Enables compaction-count + reclaimed-tokens timeline; dashboard was blind to compactions. |
| `system.turn_duration` | `durationMs`, `messageCount` | Per-turn wall-clock authority. **Unblocks the B5 fix.** |
| `permission-mode` | `acceptEdits` / `plan` mode entry/exit | Plan-mode timeline analog to codex's `collaboration_mode.mode` (memory `[[project-codex-v0-130-schema]]`). |

Fix: four new handlers in `ParseSessionFile`, each emitting a
`ToolEvent` deduped by name/mode (new `ActionPermissionMode` action
type added to `internal/models/models.go`). No billing impact — none
of these are token-bearing — but unblocks subagent attribution,
compaction surfacing, accurate per-turn wall-clock, and the
plan-mode timeline.

**B5 — Bash duration inflation (consumes B4's `turn_duration`).**
Documented in the prior audit `docs/claudecode-bash-duration-audit-2026-05-15.md`:
62 historical rows with `duration_ms > 3,600,000` claimed **246.4
false hours** of Bash wallclock; top session (`d7657cad`) alone had
73.3 false hours. Root cause: the adapter infers per-tool wallclock
from the gap between `tool_use` assistant-message and `tool_result`
user-message timestamps; auto-compact stitching + session-idle resume
measure the IDLE GAP, not the tool's actual exec time. Fix: hard cap
of **30 minutes** (Claude Code's documented Bash hard ceiling, per
audit doc §B5) on the inferred duration at `adapter.go:744-756`. The
cap chose 30min over the prior audit's 1-hour option per the
documented ceiling. Backfill via migration 022 zeroed **135 inflated
bash rows** (308.8 false hours wiped — more than the 246 the prior
audit projected because the 30-minute cap is stricter than the
audit's 1-hour reference threshold). Idempotent on re-run.

**DH1 — Stale subagent doc-comment.** `adapter.go:114-117` claimed
subagent activity is logged "inline in the same JSONL session with
`isSidechain: true` markers per line — NOT as a separate session_id".
Empirically false on every Claude Code 2.1.x session captured:
subagents live in separate
`<session-uuid>/subagents/agent-(acompact-)?XXX.jsonl` files,
sharing the parent's `sessionId` but flagged `isSidechain: true` per
line. Comment rewritten to describe the actual file layout.

**`observer backfill --claudecode-project-root` flag (V7a-bf).** New
backfill mode that walks `sessions` joined with one
`token_usage.source_file` per session, re-resolves the project root
through `crossmount.TranslateForeignPath + git.Resolve` from the
JSONL's `cwd` field, and updates `sessions.project_id` (creating
new `projects` rows as needed). Live verify on maintainer DB: 84/90
Windows sessions reattributed, 7,219 actions updated, 15 unrecoverable
(JSONLs rotated). Idempotent — already-correct sessions are no-ops.

**Migrations:**

- `021_claudecode_snapshot_drift_dedup.sql` — DELETE pass scoped to
  `tool='claude-code'` that keeps only the highest-output_tokens row
  per `(session_id, message_id, source)`. Mirrors the runtime pass
  at `store.go:679-705` exactly. Idempotent. Live verify on
  maintainer DB: **96 dup-msgid rows deleted, leftover = 0**.
- `022_claudecode_bash_duration_cap.sql` — UPDATE zeroing
  `duration_ms` on any `tool='claude-code'` actions row whose
  computed duration exceeds 30 minutes (1,800,000 ms). UPDATE not
  DELETE because the action row itself is legitimate (the tool call
  did happen); only the DurationMs attribution was wrong. Idempotent.
  Live verify: **135 inflated bash rows zeroed, leftover above 30min
  cap = 0**.

**Files changed (+969 / −6 lines + 2 new migrations):**

| File | Lines | What |
|---|---|---|
| `internal/adapter/claudecode/adapter.go` | +256 / −2 | B1 crossmount call; B3 rawUsage.ServerToolUse + emit; B4 four metadata handlers (agent-name / compact_boundary / turn_duration / permission-mode); B5 30-min bash duration cap; DH1 doc-comment rewrite |
| `internal/adapter/claudecode/adapter_test.go` | +329 / −0 | 9 new regression tests pinning all five bug fixes |
| `internal/store/store.go` | +73 / −0 | B2 snapshot-drift dedup post-batch sweep (claude-code-scoped, source-scoped) |
| `internal/store/store_test.go` | +127 / −0 | 2 new regression tests (B2 + cross-source-survival) |
| `internal/models/models.go` | +10 / −0 | New `ActionPermissionMode` action-type constant |
| `cmd/observer/backfill.go` | +178 / −0 | `--claudecode-project-root` flag + helpers |
| `internal/db/migrations/021_claudecode_snapshot_drift_dedup.sql` | NEW | B2 backfill |
| `internal/db/migrations/022_claudecode_bash_duration_cap.sql` | NEW | B5 backfill |
| `docs/adapters.md` | +2 / −2 | Claude Code row rewritten to match post-audit reality |

**Tests added (11 new):**

- `TestWindowsCwdResolvedViaCrossmount` — B1: Windows `C:\` cwd
  resolves through crossmount to mounted-WSL path, not observer's CWD.
- `TestServerToolUseWebSearchRequestsCaptured` — B3: non-zero
  `server_tool_use.web_search_requests` populates `WebSearchRequests`.
- `TestServerToolUseAbsentImpliesZero` — B3: missing field defaults
  to 0, doesn't crash on decode.
- `TestCompactBoundaryCaptured` — B4: `system.compact_boundary`
  emits the compaction event with `preTokens`.
- `TestTurnDurationCaptured` — B4: `system.turn_duration` emits the
  per-turn wallclock event.
- `TestAgentNameCapturedDedupedByName` — B4: `agent-name` lines
  emit subagent persona events deduped by name.
- `TestPermissionModeCapturedDedupedByMode` — B4: `permission-mode`
  entry/exit emits `ActionPermissionMode` deduped by mode.
- `TestBashDurationCappedAt30Min` — B5: inferred duration above
  1,800,000 ms is clamped to 0.
- `TestBashDurationUnderCapPreserved` — B5: inferred duration ≤
  cap passes through unchanged.
- `TestInsertTokenEvents_ClaudecodeSnapshotDriftDedup` — B2:
  parent-row (early snapshot) + acompact-row (later snapshot) same
  message_id → only the higher-output_tokens row survives.
- `TestInsertTokenEvents_ClaudecodeCrossSourceProxyJsonlSurvive` —
  B2 scope guard: source-scoping preserves legitimate
  proxy+jsonl complementary coverage.

**Build state:** `go vet ./...` clean; `go build` clean; full repo
test sweep **51/51 packages, 0 failures**. Live-DB verification gates
all green: 90 Windows sessions reattributed (84 successful + 15
unrecoverable due to rotated JSONLs), 96 dup-msgids deleted (leftover
= 0), 135 bash rows zeroed (leftover above 30min cap = 0).

## [1.6.9] — 2026-05-18

### chore(release): include docs/assets/ in public orphan so README images render

Operator-flagged: every image link in the public-repo README and on
the npm package page (`@superbased/observer` on npmjs.com) showed a
broken-image placeholder. Latent bug since the two-repo private/
public split.

**Root cause.** `scripts/release.sh:78-85` strips the entire `docs/`
directory from the public orphan tree as part of the PRIVATE_ONLY_PATHS
sweep. Both READMEs reference images under `docs/assets/`:

- `README.md` (lines 14, 52, 145, 157, 169, 180, 193, 208, 219, 231,
  244, 259) — 12 relative-path image refs (`docs/assets/...`).
- `npm/observer/README.md` (lines 14, 168, 250, 268, 290, 308, 322,
  352, 364, 382, 405) — 11 absolute-URL refs
  (`https://github.com/marmutapp/superbased-observer/raw/main/docs/assets/...`).

Both 404 on the public repo because `docs/` doesn't exist there.
Empirically confirmed:

```
$ curl -s -o /dev/null -w "%{http_code}\n" \
  https://github.com/marmutapp/superbased-observer/raw/main/docs/assets/screenshots/01-overview.png
404
```

**Fix.** Surgical re-inclusion of `docs/assets/` in the public orphan
tree, keeping the rest of `docs/` private:

1. After `git rm --cached -r docs` in `cmd_public()`, re-add the
   assets subtree with `git add --force docs/assets`. Files are
   still on disk after the rm-cached, so the re-add is a no-op
   in working-tree terms — just brings them back into the staging
   index.
2. Change the public-side `.gitignore` append from `/docs/` to the
   negation pattern `/docs/*` + `!/docs/assets/`. Gitignore's
   re-include syntax requires `/docs/*` (not `/docs/`) because a
   directory-level exclusion can't be undone for children otherwise.
3. Update the leak sanity-check in `cmd_public()` to split the regex
   across `docs/` vs `docs/assets/` so a future stray `docs/...`
   path still errors but `docs/assets/...` passes.

No README edits required — the references in both README.md and
npm/observer/README.md are unchanged. Future-proof: any new image
added under `docs/assets/` automatically ships in the public orphan.

**Limitations** (npm has no same-version re-upload):

- The public-repo README on GitHub renders correctly **immediately**
  after this release force-pushes the corrected orphan.
- The npm package pages for **v1.6.5 through v1.6.8 stay broken** —
  npm doesn't allow republishing the same version with a corrected
  README. The v1.6.9 npm page (this release) and all future versions
  render correctly.

**Risk:** the screenshots show real session UUIDs, project paths
(`/home/marmutapp/...`), and cost figures from the maintainer's
machine. These were always intended for the public-facing README
(they're the dashboard's marketing assets) and have been referenced
from the README since v1.5.0 — they just never rendered. Publishing
them was the intent all along; this release makes it actually work.

**Files changed:**

| File | What |
|---|---|
| `scripts/release.sh` | Surgical re-inclusion + gitignore negation + leak-check split |

No source-code, test, or runtime changes. Pure release-mechanics fix.

**Build state:** test sweep unchanged (no code touched). `bash -n
scripts/release.sh` syntax-check clean. The fix takes effect at this
release's `scripts/release.sh public` step.

## [1.6.8] — 2026-05-18

### fix(copilot-cli)+fix(store): four-bug Copilot CLI audit follow-through

Operator-requested deep audit of the Copilot CLI integration against
the operator-supplied sample log
`tmp/session-files-examples/events.jsonl` (~25k events, 4-week sticky
session, 9 shutdowns, 39 compactions, 17 model-resolved subagents +
20 model-unresolved subagents). Four bugs found + a fifth attribution
gap that surfaced during fix verification. All five fixed in this
release. Audit report:
[`docs/copilot-cli-audit-2026-05-18.md`](docs/copilot-cli-audit-2026-05-18.md);
reusable methodology:
[`docs/adapter-audit-playbook.md`](docs/adapter-audit-playbook.md).

**B1 — tuple-dedup false-drops legitimate per-block Tier-3 rows on
copilot-cli.** The v1.6.5 tuple-dedup was authored tool-agnostically.
Copilot CLI emits one `assistant.message` event per content block,
each carrying its own per-block `outputTokens` delta (NOT a cumulative
snapshot like Claude Code's emission model). Multiple blocks share
one `MessageID = requestId`. Two distinct blocks under one request
with byte-identical small output counts (e.g. two empty tool_use
blocks both 119 tokens) were wrongly collapsed by the dedup.
Quantified on the sample: 3,303 → 3,297 Tier-3 rows; 1,440 output
tokens lost (~$0.04 at opus-4.7 list). Fix scopes the dedup via SQL
to `tool IN ('claude-code', 'codex')` allowlist — both verified to
have "same MessageID implies same logical content" emission. Future
adapters must NOT join this allowlist until their emit contract is
audited (see playbook §3 Check 5).

**B2 — Tier 1 ≺ Tier 0 session-summary dedup was session-wide.** When
ANY `source='otel'` row existed for a copilot-cli session, EVERY
`source='session_summary'` row for that session was deleted — losing
modelMetrics-derived input/cache for any shutdown window NOT covered
by Tier 1. Real-world trigger: user enables `--log-level debug`
mid-session; pre-debug shutdowns have populated modelMetrics with no
otel coverage; the sweep wipes them anyway. Fix scopes the drop to
per-shutdown timestamp window — only delete the session_summary row
if otel exists in the `(prior_session_summary_ts, this_ts]` interval
for the same session. Empty lower-bound (first shutdown) defaults to
`''` lexicographically less than any RFC3339 timestamp.

**B3 — Tier-1 Request-ID regex rejected 92% of production
requestIds.** The v1.6.6 regex `[0-9a-f-]+` only matched lowercase
hex + hyphen. Sample analysis revealed Copilot CLI emits TWO
distinct Request-ID formats interleaved in production:

- `00000-<uuid>` (lowercase-hex-uuid) — 8.1% of asst.message rids
- `<HEX>:<HEX>:<HEX>:<HEX>:<HEX>` (uppercase hex with colons) — 91.9%

The smoke-test capture used only the UUID format, so the regex
silently dropped Tier-1 coverage for 92% of debug-mode requests on
real sessions. Fix: `[^\s)]+` — permissive up to whitespace or close
paren.

**B4 + V6k — Subagent-context model attribution.** Copilot CLI's
`st.model` tracks only the parent session's selected model
(`selectedModel` / `session.model_change.newModel`). When the
parent spawns a subagent via the `task` tool, asst.message events
under the subagent's `agentId` were attributed to `st.model`
(always opus on the sample) instead of the subagent's actual model.
Net misattribution: **251K output tokens** routed to opus instead of
haiku/gpt-5.4/gpt-5.2; per-model dashboard slice showed
output=0 for the subagent models despite real activity; ~$3 over-
charge in dollars.

Two-step fix in `internal/adapter/copilotcli/events.go`:

- **B4 (primary):** record `subagentModels[agentId] = data.model`
  from `subagent.completed` events. Defer asst.message Model
  resolution until end-of-parse via `pendingSubagentTokenEmits` —
  subagent.completed fires AFTER all its asst.messages in the file,
  so the post-loop patch is mandatory. Resolves 17 of 37 subagents.

- **V6k (fallback):** when `subagent.completed.data.model` is empty
  (20 of 37 events on the sample — cancelled / short-lived
  subagents that never resolved), fall back to
  `tool.execution_complete.data.model` for events sharing the
  subagent's agentId. First-write-wins (don't churn across the
  subagent's many tool calls); subagent.completed still
  authoritatively overwrites if it lands later. Resolves an
  additional 15 of the remaining 20 subagents.

Net post-fix: **32 of 37 subagents (86%)** correctly attributed;
235K of the 251K mis-attributed tokens recovered. Residual 15K
tokens still on parent st.model — the 5 pure-text subagents that
emit zero tool calls and zero model-bearing events anywhere in
their context. Documented in audit doc §B4 + §V6k.

Empirical post-fix on the operator sample (re-ran via the §6
reproducer):

| source | model | rows | output |
|---|---|---|---|
| jsonl | claude-opus-4.7 | 2,540 | 2,651,458 |
| jsonl | claude-opus-4.6 | 492 | 373,526 |
| jsonl | **gpt-5.4** | **262** | **186,778** *(was 0 pre-fix)* |
| jsonl | **claude-haiku-4.5** | **47** | **45,256** *(was 0)* |
| jsonl | **gpt-5.2** | **1** | **3,507** *(was 0)* |
| session_summary | (per-model Tier 0 untouched) | 9 | 0 |

Total Tier-3 output reconciles exactly: 3,260,525 =
3,004,941 (asst.message Tier 3) + 255,584 (compaction Tier 3).

**Audit doc (`docs/copilot-cli-audit-2026-05-18.md`, 30 KB):** full
methodology, severity-ordered bugs, sum-reconciliation tables,
ground-truth recompute correcting the v1.6.7 handover's "$1,806 ≈
$1,800 API-list ceiling" claim (right number, wrong reasoning —
coincidence driven by opus-4.5+ pricing at $5/$25 ≈ 1 Copilot
premium credit per dollar; not a structural equivalence). Reproducer
in §6 is operator-runnable.

**Audit playbook (`docs/adapter-audit-playbook.md`, 31 KB):**
reusable methodology distilled from the audit. Codifies the six
checks every adapter must pass (field-map completeness, handler
coverage, MessageID stability, cross-tier sum reconciliation,
dedup safety, model attribution), four robustness sweeps (regex
variance, state-machine ordering, idempotent re-parse, cross-OS
paths), a red-flag table of 11 anti-patterns to grep for, a 10-gate
PR checklist for new adapters, and an investigative workflow for
"cost looks off" reports. Applies to every adapter we ship; meant
to surface the bug classes that copilot-cli's audit uncovered before
they recur on cowork / claudecode / codex / antigravity / etc.

**Tests added (10 new):**

- `TestInsertTokenEvents_TupleDedupSkipsCopilotCLIMultiBlock` —
  6-block multi-row fixture mimicking the sample's collision pattern
- `TestInsertTokenEvents_TupleDedupAllowlistRejectsUnknownTool` —
  cline-as-future-tool: byte-identical rows survive (not on allowlist)
- `TestInsertTokenEvents_SessionSummaryMidDebugToggle` — pre-debug
  session_summary kept; post-debug dropped
- `TestInsertTokenEvents_SessionSummaryMultiShutdownAllDebug` —
  always-debug path preserved unchanged from v1.6.6
- `TestParseEventsJSONL_SubagentModelAttribution` — 2 distinct
  subagent models + parent-context control
- `TestParseEventsJSONL_SubagentNeverCompleted` — orphan subagent
  falls back to st.model
- `TestParseEventsJSONL_SubagentCompletedWithoutModelField` —
  empty data.model doesn't clobber Model to ""
- `TestParseEventsJSONL_SubagentResolvedFromToolExecution` — V6k
  fallback via tool.execution_complete
- `TestParseEventsJSONL_SubagentCompletedModelWinsOverToolExecution`
  — precedence: subagent.completed authoritative
- `TestParseEventsJSONL_SubagentExplicitModelWins` — data.model on
  asst.message beats both lookups
- `TestParseProcessLog_HexOpaqueRequestID` — Tier-1 regex captures
  both UUID and hex:colon formats

**Build state:** `go vet ./...` clean; `go build` clean; full repo
test sweep **51/51 packages, 0 failures**. Empirical reproducer
verified against the operator sample.

**Files changed (+985 / −15 lines + 2 new docs):**

| File | Lines | What |
|---|---|---|
| `internal/store/store.go` | +70 / −15 | B1 dedup allowlist; B2 per-shutdown window scope |
| `internal/store/store_test.go` | +312 / −0 | B1 + B2 regression tests |
| `internal/adapter/copilotcli/events.go` | +164 / −15 | B4 + V6k subagent attribution; eventEnvelope.AgentID; subagentCompletedData type; post-loop patch |
| `internal/adapter/copilotcli/log.go` | +14 / −0 | B3 regex broadening |
| `internal/adapter/copilotcli/adapter_test.go` | +300 / −0 | 6 subagent tests + 1 hex:colon regex test |
| `docs/copilot-cli-audit-2026-05-18.md` | +662 NEW | audit report + V6k addendum |
| `docs/adapter-audit-playbook.md` | +770 NEW | reusable methodology |

## [1.6.7] — 2026-05-18

### feat(copilot-cli): capture session.compaction_complete.compactionTokensUsed

Follow-on to v1.6.6's Tier 0 fix. While reconciling the operator's
"$1,806 is too low" intuition against the sample events.jsonl, found
that `session.shutdown.data.modelMetrics` does NOT include compaction
tokens — the proof is the modelMetrics outputs sum (2,999,106) matching
Tier 3 assistant.message outputs (3,004,941) within 0.2% (streaming
noise), while `compactionTokensUsed.output` across 39 compactions
contributes another 255,584 outputs that are **separately billable**.
Compaction is its own API call (the assistant summarises prior
context); without capture, every compaction's input / output /
cache_read silently disappears.

For the sampled file: ~$8 (if Copilot uses Haiku for compaction) to
~$38 (if Opus) of dollar cost was being dropped per session. For
summarization-heavy long sessions this could be a meaningful share of
total spend; on this file specifically: 5.99M input + 256K output +
5.7M cached input across 39 compactions.

**Adapter:** new `case "session.compaction_complete":` handler in
`internal/adapter/copilotcli/events.go`. Emits one TokenEvent per
compaction call with the payload's tokens. `source='jsonl'`,
reliability='approximate'.

**Schema variants handled.** Two observed shapes in the wild:

- Older (38/39 events in the sampled log):
  `{input, output, cachedInput, duration}` — no `Tokens` suffix, no
  model field.
- Newer (1/39): `{inputTokens, outputTokens, cacheReadTokens,
  cacheWriteTokens, model}`.

`compactionTokensUsed.resolveTokens()` collapses both by
field-fallback: `InputTokens || Input`, `OutputTokens || Output`,
`CacheReadTokens || CachedInput`. Model from payload when present
(newer schema), else `st.model` fallback (older schema —
empirically Copilot compacts with whatever model is active when the
threshold trips, so `st.model` is the right approximation).

**MessageID.** When `data.requestId` is present (every observed
event), use it — joins to Tier 1 (otel) rows from process.log for the
same compaction call via the v1.6.3 T1 dedup. When absent, fall back
to `"compaction:<env.ID>"` so the row is uniquely keyable.

**Coexistence.** Compaction rows share `source='jsonl'` with Tier 3
per-message rows, but their MessageIDs come from different request
IDs so no collision under the v1.6.5 tuple dedup. When Tier 1 (otel)
captures the same compaction call (debug logging on), v1.6.3 T1 drops
the jsonl compaction row in favour of the more granular otel row —
correct precedence.

**Four new tests in `internal/adapter/copilotcli/adapter_test.go`:**

- `TestParseEventsJSONL_CompactionTokensOldSchema` — `{input, output,
  cachedInput}` → resolves correctly, falls back to `st.model`
- `TestParseEventsJSONL_CompactionTokensNewSchema` — `{inputTokens,
  outputTokens, cacheReadTokens, cacheWriteTokens, model}` → model
  from payload overrides `st.model`
- `TestParseEventsJSONL_CompactionTokensEmptyOrZero` — absent or
  all-zero compactionTokensUsed → 0 TokenEvents
- `TestParseEventsJSONL_CompactionTokensMissingRequestID` → MessageID
  falls back to `compaction:<env.ID>`

**Live verify (no change observable on the maintainer DB —
intentionally).** The 5 active copilot-cli sessions on the
maintainer's machine are short-test sessions, none long enough to
trigger Copilot's compaction threshold (0 compaction_complete events
in any of them). So a rescan emits 0 new rows. The fix is pinned by
the unit tests, which cover both schema variants + edge cases. For
a real long-conversation user (like the operator's sample file with
39 compactions) the impact would be the $8–$38 quantified above.

**Build state:** typecheck + go vet + go build all clean. Full repo
test sweep: 51/51 packages, 0 failures.

## [1.6.6] — 2026-05-18

### feat(copilot-cli): Tier 0 capture from session.shutdown.modelMetrics — closes the input/cache/reasoning gap for non-debug users

Operator-flagged: copilot-cli cost figures on the dashboard were
"very misleading" — only outputTokens were captured for users who
didn't run `copilot --log-level debug`. Investigation of a sample
events.jsonl (~25k events, 9 shutdowns) found `data.modelMetrics`
inside `session.shutdown` carries per-model cumulative usage delta
(inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens,
reasoningTokens, plus a Copilot-premium-credit `cost` field) and the
adapter was discarding it. For that one file alone: **320M input
tokens, 295M cache_read, 115K reasoning, and ~$1,806 of model
activity** were silently dropped — only Tier 3 (per-message
outputTokens, 3M total) survived.

**Empirical findings driving the design.**

- Each `session.shutdown.modelMetrics` is a *delta block* covering
  the work span between the most recent `session.resume` and this
  shutdown, NOT a cumulative all-time snapshot. Verified: summing
  all 9 shutdowns on the sample file gives 2,999,106 output tokens,
  matching the Tier 3 sum (3,004,941) within streaming-chunk noise.
  The latest shutdown alone has all-zero usage (idle pause) — so
  "take last" would have given $0.
- `modelMetrics.requests.cost` is a "Copilot premium request credit"
  count, not dollar cost. gpt-5.4 reports `cost=$0` despite 200+
  requests and 14M input tokens. We compute cost via our standard
  pricing engine on the captured token columns instead.
- `session.compaction_complete.data.compactionTokensUsed` *is already
  aggregated into* modelMetrics — verified by the matching sum check.
  Separately capturing it would double-count. Skipped.

**Tier 0 capture (new `source='session_summary'`).** The
`session.shutdown` handler at
`internal/adapter/copilotcli/events.go:553` now parses
`data.modelMetrics` and emits one `TokenEvent` per model with
non-zero usage:

- `Source = TokenSourceSessionSummary` (new constant)
- `Reliability = ReliabilityApproximate`
- `SourceEventID = env.ID + ":" + model` (per-shutdown-per-model
  unique; idempotent re-parse via `UNIQUE(source_file, source_event_id)`)
- `MessageID = "session-shutdown:" + env.ID` (per-shutdown grouping)
- `InputTokens / CacheReadTokens / CacheCreationTokens
  (= cacheWriteTokens) / ReasoningTokens` from `modelMetrics[model].usage`
- `OutputTokens = 0` — Tier 3 per-message rows already cover output;
  including here would double-count by ~2x
- Models with all-zero usage (idle-pause shutdowns) are skipped so
  the table doesn't accumulate noise rows

**Store-layer Tier 0 ≺ Tier 1 dedup.** When `--log-level debug` IS
on, Tier 1 (`source='otel'`) captures the same model-call data with
per-request granularity. The session-level Tier 0 aggregate would
duplicate Tier 1's input/cache/reasoning columns. New dedup pass in
`store.InsertTokenEvents` (after the v1.6.3 T1 block, gated on
`hasCopilotCLI`): when any `source='otel'` row exists for a
`(tool='copilot-cli', session_id)`, drop every `source='session_summary'`
row for that session. Scoped per-session not per-message because
Tier 0's MessageID groups per-shutdown, not per-request. Arrival-order
independent; idempotent across re-parses.

**Tier coexistence after this change** (cost engine SUMs all rows):

| User mode | Tiers active | Capture quality |
|---|---|---|
| No debug | Tier 0 + Tier 3 | input/cache/reasoning from session-aggregate, output per-message |
| Debug | Tier 1 (drops 0 + 3) | full per-request breakdown |
| Pre-v1.6.6 no-debug | Tier 3 only | **output_tokens only — input/cache/reasoning = 0** |

**Live verify on the maintainer DB** (`observer scan --force --adapter copilot-cli`):
- copilot-cli total cost: **$0.0622 → $0.1810** (+191%, ~3×) for the 7 small sessions on disk
- 4 new `source='session_summary'` rows landed (1 claude-haiku-4.5, 3 gpt-5-mini across distinct shutdowns)
- claude-haiku-4.5 input went 0 → 79,940; cache_read 0 → 70,445
- gpt-5-mini input went 171,579 → 282,908 (+65%); cache_read 20,992 → 84,224 (+301%)
- 0 sessions have both `otel` and `session_summary` rows → Tier 0 ≺ Tier 1 dedup confirmed clean
- For the operator's sample file ($1,806 of model activity in a single session), the projected capture jump on rescan is **~30,000× larger** than the 3× we see on the maintainer's micro-sessions

**Pricing engine coverage** — verified the dot-variant model names that
Copilot CLI emits (`claude-opus-4.6`, `claude-opus-4.7`,
`claude-haiku-4.5`, `gpt-5.2`, `gpt-5.4`) all resolve to non-zero
rates. `claude-haiku-4.5` already had an exact entry; opus dot-variants
fall back to `claude-opus-4` family rates (identical to the hyphen
variants). Dashboard will surface a `~` badge on the opus rows
indicating non-exact lookup — operator can add explicit dot-variant
entries to the pricing table later if desired.

**Four new adapter tests in `internal/adapter/copilotcli/adapter_test.go`:**
- `TestParseEventsJSONL_SessionShutdownModelMetrics` — 3-model shutdown
  emits 3 TokenEvents with correct token mappings, OutputTokens=0,
  session-end marker preserved, stable SourceEventID + MessageID
- `TestParseEventsJSONL_SessionShutdownEmptyModelMetrics` — absent and
  explicit-empty modelMetrics both emit 0 TokenEvents; marker still
  fires
- `TestParseEventsJSONL_SessionShutdownSkipsZeroUsage` — model with all
  zero usage columns (idle-pause) is suppressed; non-zero peers
  survive
- `TestParseEventsJSONL_SessionShutdownPartialFields` — missing
  reasoningTokens / cacheWriteTokens in older Copilot CLI versions
  zero-fill cleanly

**Five new store tests in `internal/store/store_test.go`:**
- `TestInsertTokenEvents_SessionSummaryDroppedWhenOtelPresent` — Tier
  1 wins; Tier 0 is dropped
- `TestInsertTokenEvents_SessionSummaryPreservedAloneNoOtel` — Tier 0
  survives when Tier 1 is absent (the common non-debug case)
- `TestInsertTokenEvents_SessionSummaryAndTier3Coexist` — Tier 0 +
  Tier 3 sum to complete capture
- `TestInsertTokenEvents_SessionSummaryIdempotentReparse` — 3
  re-parses of same shutdown event → 1 row
- `TestInsertTokenEvents_SessionSummaryMultiModelsAllSurvive` — multi-
  model rows in one shutdown share MessageID but distinct token
  tuples; v1.6.5 tuple dedup must not collapse them

**Build state:** typecheck + go vet + make web-build + go build all
clean. **Full repo test sweep: 51/51 packages, 0 failures.**

## [1.6.5] — 2026-05-18

### fix(store): generalized tuple-level token_usage dedup + migration 020

Operator-flagged: cost numbers on the maintainer dashboard looked
roughly right per-session but absolute totals were higher than
plausible. Probe across the live DB (74,227 token_usage rows) found
**41,019 byte-identical re-emission rows in 18,316 (session_id,
message_id) groups for claude-code alone** — pre-cb16006 (2026-04-25)
residue that escaped the `UNIQUE(source_file, source_event_id)`
constraint and inflated per-model cost by ~30% on every dashboard
surface.

**Root cause.** Claude Code's JSONL emits N content-block lines per
assistant message (one per text/tool_use/thinking block), and every
line carries the **same cumulative `message.usage` snapshot** of the
parent API call. The cb16006 fix (2026-04-25) keyed
`SourceEventID = msg.ID` so re-parses correctly hit ON CONFLICT —
forward data is clean. But the pre-fix code used per-line UUID as
`SourceEventID`, leaving N rows per logical message and ~22.6k of
those rows lingering on any DB that ran observer before 2026-04-25.
Verified on a Windows-mounted JSONL too: same N-lines-per-msg-id
emit pattern, but zero DB residue (those rows were ingested post-fix).

A smaller residue (~22 rows, 111 groups) exists for codex from
runtime re-emissions of identical `total_token_usage`. copilot-cli
has 3 groups outside the v1.6.3 T1 otel/jsonl mixed-source scope.

**Tool-agnostic tuple-level fix.** `internal/store/store.go::InsertTokenEvents`
now appends a post-batch DELETE inside the same transaction: per
`(tool, session_id, message_id)`, drop rows whose
`(input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens,
cache_creation_1h_tokens, reasoning_tokens)` tuple is byte-identical
to a higher-id sibling. Gated on the batch containing at least one
msgid-bearing event so empty-msgid-only inserts skip the EXISTS scan.
Rows with distinct token values (real progressing snapshots, real
per-emission deltas) are preserved — only byte-identical re-emissions
collapse. Tool-agnostic so future adapters inherit the dedup without
bespoke per-tool scoping. The existing `idx_token_usage_session_message`
index keeps the EXISTS cheap.

`internal/db/migrations/020_token_usage_tuple_dedup.sql` runs the
same DELETE once over historical data. Idempotent on clean DBs;
schema version 19 → 20.

**Live verify on the maintainer DB:**
- pre-migration token_usage rows: 74,227
- post-migration: **51,671** (`−22,556 rows deleted`)
- claude-code all-history total: **$10,090.55 → $6,768.41** (`−$3,322 / −33%`)
- codex all-history total: $300.40 → $298.21 (essentially unchanged,
  confirms codex per-turn deltas are real cost, not duplicates)
- copilot-cli / antigravity / cowork totals: unchanged (no false
  positives — non-byte-identical dups preserved)
- 30-day sessions vs models reconciliation Δ: $14.14 → **$7.07**
  (tighter — cleaner data reconciles better)
- residual dup groups (all non-byte-identical, preserved by design):
  96 claude-code + 111 codex + 3 copilot-cli

**Why MAX-collapse would have been wrong for codex** — investigation
found codex `token_count` events carry `last_token_usage` (per-turn
delta) not `total_token_usage` (cumulative), so multiple rows
sharing TurnID represent real separate billable cost. Naive
`MAX(output_tokens)` collapse per `(session_id, message_id)` would
have under-counted codex by 80%+. Tuple-equality is the only
semantically safe key.

Six new tests in `internal/store/store_test.go`:
- `TestInsertTokenEvents_TupleDedupIdenticalRows` — claudecode-style
  5-line group with identical tuples collapses to 1 row.
- `TestInsertTokenEvents_TupleDedupPreservesDistinctRows` — codex-style
  3 distinct delta emissions all survive.
- `TestInsertTokenEvents_TupleDedupPreservesEmptyMessageID` —
  msg-less rows never collapse even if tuples match.
- `TestInsertTokenEvents_TupleDedupAcrossTools` — cross-tool rows
  with identical (session_id, message_id, tuple) survive
  independently.
- `TestInsertTokenEvents_TupleDedupArrivalOrder` — final state
  identical across single-batch / sequential / reversed insert
  orders.
- `TestInsertTokenEvents_TupleDedupCoexistsWithCopilotCLI` — single
  batch with copilot-cli tier1+tier3 AND claude-code identical-tuple
  group resolves correctly: T1 mixed-source dedup drops tier3, tuple
  dedup collapses claude-code dups.

`dashboard_test.go::TestAPISessionMessages_LongContextPerTurn` test
fixture updated to use distinct token tuples (149,999 + 150,001
instead of 150,000 + 150,000) — the original seed double-counted by
shape and the v1.6.5 dedup correctly collapses byte-identical
inputs. Production-realistic.

**Build state:** typecheck + go vet + make web-build + go build all
clean. **Full repo test sweep: 51/51 packages, 0 failures.**

## [1.6.4] — 2026-05-18

### fix(dashboard): cost reconciliation + tool-filter coverage on three orphaned endpoints

Operator-flagged: per-session cost on /api/sessions didn't reconcile
with /api/models' total on the Cost page. Investigation surfaced
three related gaps left from the v1.6.3 ship.

**1. /api/sessions cost rollup window aligned to `days` param.**
handleSessions previously hardcoded `cost.Options.Days=365` even when
the caller passed `days=30` — over-counted sessions whose token_usage
spanned beyond the visible window. After the fix, the cost engine
respects the same window as the rest of the page.

Live verify on the maintainer DB (days=30):
- pre-fix: sum-of-sessions=$6,204.21 vs /api/models=$6,168.90
  (**Δ=$35.31 / 0.57%**)
- post-fix: sum-of-sessions=$6,159.97 vs /api/models=$6,174.11
  (**Δ=$14.14 / 0.23%**)

Residual ~$14 is a different smaller phenomenon — sessions started
>30d ago that have token_usage rows in the window. /api/sessions
filters by `started_at`; /api/models filters by `tu.timestamp`. That's
a sessions-list semantic question (whether to include long-resumed
sessions in the windowed list), not a cost engine bug. Deferred.

`days=0` callers (CLI without window) keep the full-history rollup —
`costDays` falls back to 36500 in that case.

New test `TestAPISessions_CostReconcilesWithModels` seeds a session
with a recent + a 60-day-old api_turn, asserts sum-of-/api/sessions
≈ /api/models?days=7 total within 0.01%.

**2. /api/cost honors `tool` query param.** handleCost only read
`project` and `source` — the v1.6.2 ship's "Cost engine + handlers"
sweep wired Tool through `cost.Options` but missed this handler.
Dashboard-orphaned (Cost.tsx calls /api/models, not /api/cost) but
`observer cost --tool=X` CLI consumers expected it. Two-line fix.
New test `TestAPICost_HonorsToolFilter` covers unfiltered vs
per-tool vs nonexistent-tool, asserting tool=A + tool=B sums to
unfiltered.

**3. /api/compression/rolling-cost honors tool + project filters.**
Backend: handleCompressionRollingCost joins `summary_calls` through
sessions → projects and `compression_events` through api_turns →
sessions → projects when filters are set. Frontend Compression.tsx
passes tool + project to the rolling-cost useApi call with proper
deps array. The handler still returns identical shape; only the
underlying scan narrows.

**Build state:** typecheck + go vet + make web-build + go build all
clean. All 51 packages green; two new dashboard tests pin both
behaviors.

## [1.6.3] — 2026-05-18

### feat: copilot-cli cost-accuracy completes — Tier 1+3 dedup + Tier 2 utilization fallback + migration

The v1.6.2 ship landed copilot-cli's three-tier capture and flagged
two known gaps for v1.6.3: (a) Tier 1 + Tier 3 double-count output
tokens when debug logging is enabled, and (b) Tier 2 was documented
but not implemented, leaving the "no debug log = no input tokens"
case fully un-billed. v1.6.3 closes both.

**Tier 1 + Tier 3 dedup (store-layer).** When `--log-level debug` is
on, the same Request-ID produces a Tier-1 row (`source='otel'`, full
usage breakdown from `[DEBUG] response` block) AND a Tier-3 row
(`source='jsonl'`, OutputTokens-only from `assistant.message.outputTokens`).
They land under different `(source_file, source_event_id)` keys, so
the existing ON CONFLICT clause can't dedup them — `output_tokens`
was double-counted in every rollup for those sessions.

`internal/store/store.go::InsertTokenEvents` now appends a post-batch
sweep DELETE inside the same transaction: when a copilot-cli batch
arrives, drop any `source='jsonl'` row where a `source='otel'` row
exists for the same `(session_id, message_id)`. Scoped to
`tool='copilot-cli'` only — other adapters with similar overlap
patterns (Anthropic proxy + claudecode JSONL) are left alone for
future work. Arrival-order independent, idempotent across re-parses;
index `idx_token_usage_session_message` keeps the EXISTS subquery
cheap. 9 new dedup tests (`TestInsertTokenEvents_CopilotCLITierDedup`
covers 7 arrival-order scenarios — tier3-then-tier1, tier1-then-tier3,
both-in-same-batch, tier1-only, tier3-only, double-tier1 re-parse,
double-tier3 re-parse — plus `TestInsertTokenEvents_DedupScopedToCopilotCLI`
pins the cross-adapter non-interference contract).

**Migration `019_copilotcli_dedup.sql`** backfills the same DELETE
once at next observer startup — runtime dedup only catches new
inserts, so without the migration the over-counted rows from the
pre-v1.6.3 era would persist forever. Migration is conservative
(Tier-3-only sessions keep their rows, no OutputTokens loss);
idempotent against clean DBs.

**Tier 2 (utilization fallback).** When `--log-level` is NOT debug
(INFO is the Copilot CLI default), `[DEBUG] response (Request-ID …)`
lines never fire — so Tier 1 has nothing to parse, and input tokens
are completely invisible. Tier 2 fills the gap: the `[INFO]
CompactionProcessor: Utilization X% (CTX/128000 tokens)` line fires
once per outgoing request (verified empirically — every Utilization
sample is followed directly by `--- Start of group: Sending request
to the AI model ---`). Each sample's raw CTX value IS the gross
prompt size for the upcoming request, NOT a delta vs the prior
sample — the v1.6.2 handover's "subtract successive Utilization
values" framing was incorrect; using deltas would under-count input
by ~80%.

`internal/adapter/copilotcli/log.go` now buffers a TokenEvent per
Utilization snapshot during the log-parse pass, then flushes the
buffer at end-of-file ONLY IF the new
`logParserState.seenDebugResponseHeader` flag is false (i.e. no
`[DEBUG] response` line fired anywhere in the file → Tier 1 isn't
covering anything → buffered Tier 2 emits land). When debug logging
IS on, Tier 1 covers every request and the buffered Tier 2 rows are
dropped — avoids double-counting input. Mutual exclusion is
per-file because `--log-level` is a process-level flag (so any one
log file is either all-debug or all-INFO; mid-session toggling is
impossible). New `models.TokenSourceLogDelta = "log_delta"`
distinguishes Tier 2 rows from Tier 1 (`otel`) and Tier 3 (`jsonl`).
Tier 2 has no MessageID (Request-IDs aren't surfaced at INFO level);
the matching Tier 3 row carries OutputTokens with its own MessageID
and the two compose at the rollup layer rather than at the
TokenEvent level. Reliability = `approximate`. 2 new tests pin the
mutual-exclusion contract (`TestParseProcessLog_Tier2_InfoOnlyLogging`
+ `TestParseProcessLog_Tier2_SuppressedByDebugLogging`).

**Live verification on the maintainer DB** (run after
`observer scan --force --adapter copilot-cli`):

- Session `72bbb346` (debug-on): had 6 dupe rows pre-v1.6.3 (3 otel +
  3 jsonl); migration cleaned to 3 otel-only; output_tokens dropped
  from over-counted 2,758 → correct 1,379.
- Session `9da4aa10` (debug-off): had 4 jsonl rows pre-v1.6.3
  (output_tokens=3,807, input_tokens=0 — fully invisible input);
  scan emitted 5 new `log_delta` rows totaling InputTokens=123,348.
  Now has the full per-request gross-prompt billing.
- `/api/analysis/headline?tool=copilot-cli` period_cost shifted
  $0.031 → $0.062 — Tier 2 effectively doubles the captured cost on
  this maintainer's corpus because INFO-only sessions outweigh
  debug-on ones.

**Known limitation:** Tier 2's per-turn count overstates the actual
turn count because Tier 2 (log_delta, no MessageID) + Tier 3 (jsonl,
MessageID set) rows for the same request don't share a join key —
they appear as two distinct "turns" in `per_turn.count`. The
aggregated `period_cost_usd` is correct; only the per-turn
denominator is inflated. Future work: join via timestamp-window or
session-boundary heuristic so per_turn count reflects API turns
rather than token-row count.

### feat(dashboard): filter-wiring follow-up — patterns + compression-retrieval + compaction-events

The v1.6.2 ship hardened most surfaces but explicitly deferred
several smaller ones to v1.6.3. Operator audit (Lane R) of the live
v1.6.2 dashboard with `tool=copilot-cli` + project filters surfaced
three concrete gaps + one hygiene issue.

**`/api/patterns` + `/api/patterns/timeseries` honor the Tool
filter.** `project_patterns` rows are project-scoped at the DB
level, but the semantic "patterns mined from sessions that used
tool=X within project=Y" is the user's expectation when the global
Tool dropdown is set. Backend: both handlers now read `tool` query
param and restrict via `IN (SELECT DISTINCT project_id FROM actions
WHERE tool = ?)`. The IN-subquery with a single scan + hash-join
avoids the EXISTS-per-pattern quadratic risk the v1.6.2 ship hit on
`crossToolFiles` (handover §4d). Frontend: `Patterns.tsx` pulls
`tool` from `useFilters()` and passes it to both `useApi` calls. New
table-driven test `TestAPIPatterns_ToolFilter` covers 7 filter
combinations (no-filter, two tool variants, missing tool,
project-only, project+tool agreeing, project+tool disagreeing).

**`/api/compression/retrieval` honors tool + project.** The K43
retrieval-rate report (`learn.PatternMiner.Report`) was windowed by
days only. New `learn.ReportOptions{Days, Tool, Project}` struct
extends the signature; queries on `retrieval_signals` join through
`action_id → actions → projects` for tool/project filtering (live
DB sanity-check confirmed `action_id` is populated on 179/180
signals — `session_id` is structurally NULL in the historic
corpus, so the action-based path is load-bearing). `compression_events`
queries join through `api_turn_id → api_turns → sessions →
projects`. Two helper functions `signalsFilter` /
`compressionEventsFilter` build the optional JOIN + WHERE clauses
so the empty-filter case stays an unjoined scan. Existing legacy
caller (`signals_test.go` × 5 sites) updated to the new struct.

**`/api/compaction/events` honors tool + project.** Direct WHERE
clauses on `tool = ?` + `project_id = (SELECT id FROM projects
WHERE root_path = ?)` — `compaction_events` already has both columns
indexed via the `001_initial.sql` schema, so no JOIN is needed.

**Frontend `Compression.tsx`**: both `retrieval` and `compaction`
`useApi` calls now pull `tool` + `project` from `useFilters()` and
include them in the deps array.

Verified live: `/api/patterns?tool=copilot-cli` → 0 (no patterns
mined yet for the new adapter — sample threshold not met) vs
`tool=claude-code` → 108. `/api/compression/retrieval?tool=claude-code`
→ 165 search_hits (was 179 unfiltered). `/api/compaction/events?tool=codex`
→ 0 events (no codex compaction events in the corpus). All filter
narrowing is monotone-decreasing vs the unfiltered baseline.

### fix(discover): `cross_tool_files` nil-slice JSON hygiene

Lane R audit caught `/api/discover` returning
`"cross_tool_files": null` (instead of `[]`) when no tool filter
was set — a stale Go nil slice from
`internal/intelligence/discover/discover.go::crossToolFiles()`
that escaped the v1.6.2 ship's source-level slice-init sweep.
Frontend guards with `?.length` so the v1.6.2 routing-suggestions
crash class doesn't repeat, but the type contract (`CrossToolFile[]`
on the wire) is now honest. One-line fix: `var out []CrossToolFile`
→ `out := []CrossToolFile{}`.

### fix(test): newTestServer relative-to-now timestamp (closes time-bomb test failures)

`TestAPITimeseriesActions` + `TestAPITools` were carried over from
v1.6.0 as known-failing per the v1.6.1 + v1.6.2 handovers. Root
cause was a hardcoded `time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)`
in `newTestServer` (the dashboard test seed helper) — once today's
date crossed 2026-05-16 (April 16 + 30 days), every test that
queried with `days=30` saw zero rows from the seed and asserted
empty-result-is-failure. Fix at the source:
`base := time.Now().UTC().Add(-time.Hour)`, with a comment
explaining the trap. Other tests that build on the seeded `sA`
session reference it by ID (not by timestamp) so they're unaffected.
Both tests now pass; the full dashboard suite + every package across
the repo (51 packages) is green.

**Build state at tag:** typecheck + go vet + make web-build + make
build + the full test sweep all clean. No pre-existing failures
remain on `main`.

## [1.6.2] — 2026-05-17

### feat(adapter): GitHub Copilot CLI — new `copilot-cli` adapter with three-tier token capture

v1.6.2 adds Observer support for the GitHub Copilot CLI agent
(`@github/copilot` npm package, binary `copilot`) as a NEW adapter
distinct from the existing `copilot` adapter (which covers the VS
Code Copilot Chat extension). Both products share branding but have
wholly different on-disk layouts and event schemas; conflating them
would corrupt per-tool dashboard slicing.

The adapter watches two file shapes under one home-rooted set of
roots (`<home>/.copilot/`), cross-mount-aware for WSL2:

- `<home>/.copilot/session-state/<uuid>/events.jsonl` — per-session
  event stream (13 event types; envelope `{type, data, id, timestamp,
  parentId}`; always written even at INFO log level)
- `<home>/.copilot/logs/process-<ts>-<pid>.log` — global per-process
  log file

**Token capture — three-tier graceful degradation.** GitHub does NOT
expose input / cache / reasoning tokens in `events.jsonl` (only
`outputTokens` on each `assistant.message`). Investigation of the
binary surfaced the full upstream-API `usage` object in the
per-process log at `--log-level debug`. Adopted strategy:

- **Tier 1 (accurate, reliability=approximate)** — log file at
  `--log-level debug` captures `[DEBUG] response (Request-ID …):`
  blocks with full `usage` JSON: `prompt_tokens`,
  `completion_tokens`, `prompt_tokens_details.cached_tokens`,
  `completion_tokens_details.reasoning_tokens`. Joined to
  events.jsonl `assistant.message.requestId` via Request-ID; one
  Request-ID may cover N response blocks within a turn (WebSocket
  serves multiple `response.create` calls), so the parser increments
  a per-Request-ID sequence counter to produce N distinct
  `SourceEventID = "log:<requestId>:<seq>"` rows.
- **Tier 2 (approximate, ~5%)** — even at default INFO level the log
  captures `CompactionProcessor: Utilization X% (CTX/128000 tokens)`
  snapshots before every API call; successive-call deltas give an
  approximate input-token count per turn. Not yet implemented — Tier 1
  + Tier 3 ship first; Tier 2 lands in a follow-up.
- **Tier 3 (output-only, reliability=unreliable)** — events.jsonl
  `assistant.message.outputTokens` is captured unconditionally.
  Input / cache / reasoning land as 0 when neither log tier is
  available.

Strategies rejected during investigation (documented in
`docs/copilot-cli-adapter-plan.md` §11.3): WebSocket MITM proxy
(Copilot uses `wss://api.individual.githubcopilot.com/responses` —
TLS-encrypted WS, ~1 week of work for a problem already solved by the
debug log), local `o200k_base` tokenizer (no good pure-Go port; lossy
on cache splits), env-var endpoint hijack (no `COPILOT_DEBUG_GITHUB_API_URL`
override exists for the model endpoint).

**Verified live on both WSL native and Windows-via-`/mnt/c` capture**
(2026-05-17 smoke). Math reconciles to the Copilot CLI's terminal
footer to the token: a 3-call session displayed
`↑ 48.2k • ↓ 1.4k • 21.0k (cached) • 1.1k (reasoning)`; observer DB
sum: input 48,231 / output 1,379 / cache 20,992 / reasoning 1,088 — all four match.

**Tool-name mapping** (Copilot's built-in tool surface — initial set,
expand as needed): `view`/`read` → read_file; `create` → write_file;
`edit`/`str_replace_editor` → edit_file; `glob` → search_files; `grep`
→ search_text; `bash` → run_command; `web_fetch` → web_fetch;
`web_search` → web_search; `task` → spawn_subagent; `report_intent`
→ unknown (Copilot's mid-turn intent announcement);
`github-mcp-server-*` and `*-mcp-server-*` → mcp_call.

**New artifacts:**

- `internal/models/models.go` — new `ToolCopilotCLI = "copilot-cli"`
  constant
- `internal/adapter/copilotcli/{adapter,events,log,paths,doc,adapter_test}.go`
  — adapter package + 7-test table-driven test suite
- `internal/adapter/defaults/defaults.go` + `_test.go` —
  registration + IsSessionFile fixture
- `internal/config/config.go` — `"copilot-cli"` added to
  `Default().EnabledAdapters` (13 → 14 adapters)
- `internal/intelligence/cost/pricing.go` — `claude-haiku-4.5` alias
  (with a dot, the exact string Copilot CLI emits — pricing already
  had `claude-haiku-4-5` with dashes; gpt-5-mini was already covered)
- `cmd/observer/backfill.go` — new `--copilot-cli-rescan` flag,
  also fires from `--all`. Re-walks both `session-state/` and `logs/`
  trees so a debug-log enabling mid-flight retrofits Tier-1 tokens.
- `internal/intelligence/dashboard/{backfill_run,settings}.go` —
  `copilot-cli` → `--copilot-cli-rescan` allowlist entry + status
  description
- `web/src/lib/tools.ts` + `web/src/styles/tokens.css` — dashboard
  tool registry entry (label "Copilot CLI", provider "github",
  color `--tool-copilot-cli` = `#0969da` light / `#0550ae` dark for
  GitHub-blue distinction from Copilot's slate-gray)

**Docs:**

- `docs/copilot-cli-adapter-plan.md` (new, ~600 lines) — full
  implementation plan with the three-tier strategy deep dive in §11
- `docs/copilot-cli-smoke-test.md` (new) — operator-facing
  enable-debug-logging recipe + cross-mount story + backfill workflow
- `docs/cross-adapter-schema-mapping.md` — new §3.7d row for
  `copilot-cli` (disambiguation from `copilot`; field mapping;
  three-tier token table; joiner cardinality note)
- `docs/adapters.md` — new row in implemented-adapters table

**Known issue (v1.6.2 ship — fix planned for v1.6.3):** when both
Tier 1 and Tier 3 fire for the same session, `output_tokens` is
duplicated (both tiers carry the output side). Cost rollups
over-count output by ~2× for sessions where debug-log capture is
enabled. Workaround for accurate cost analysis: filter
`source_event_id LIKE 'log:%'` to see Tier-1-only totals (which match
the Copilot CLI's terminal footer exactly). Fix: store-layer dedup by
`(session_id, message_id, source)`.

Build state at tag: typecheck + go vet + make web-build + make build
all clean; new adapter tests (7) + defaults invariants pass; the 2
pre-existing dashboard test failures (`TestAPITimeseriesActions`,
`TestAPITools`) carried over from v1.6.0/v1.6.1 still fail on
baseline — not introduced here.

### fix(dashboard): null-slice JSON crash + comprehensive global-filter wiring

Operator-flagged crash on `/analysis`:
`TypeError: Cannot read properties of null (reading 'length') at Analysis.tsx:263`.
Root cause was the dashboard backend marshaling empty `[]T` slices as
JSON `null` (Go's zero-value for an uninitialized slice header) which
then crashed `routing.data.suggestions.length` since the `?.` operator
only guards the parent. Fixed at the source — initialized to non-nil
empty slices in `internal/intelligence/dashboard/analysis.go`
(`allMovers`, `newEntrants`, `increases`/`decreases`, routing `out`,
top-sessions `badges`) and `internal/intelligence/dashboard/health.go`
(`out []fileHealth`). Belt-and-suspenders frontend `?.length` chains
added across Analysis, Cost, Tools, Settings.

Then a comprehensive sweep of every page's filter wiring: the global
Tool dropdown was effectively decorative on most pages (page didn't
read it, or backend didn't honor it), and Project worked piecemeal.
Audit produced an explicit matrix; fix applied in two passes:

- **Cost engine + handlers**: added `Tool string` to `cost.Options`,
  applied via `sessions.tool = ?` in both `loadProxyRows` and
  `loadJSONLRows`. A new analysis-only helper
  `analysisScopeClause(table, alias, tool, project)` builds the
  qualifying JOINs + WHERE for the 6 custom-SQL analysis handlers
  that scan `api_turns ∪ token_usage` via the `proxy_turn_ids` /
  `combined` CTE pattern (headline, top-sessions, routing-suggestions,
  cost-by-hour, cost-by-dow-hour, cache-savings-trend). The remaining
  cost-engine handlers (movers, trend, models, tokens-by-model,
  timeseries-cost, timeseries-actions, tools, tools-breakdown,
  patterns, patterns-timeseries, compression-events,
  compression-by-model, compression-timeseries) now also accept
  `tool` + `project` query params.

- **Frontend pages**: Cost, Analysis, Overview, Compression, Tools,
  Patterns, Discovery now destructure all relevant filters from
  `useFilters()`, pass them to every `useApi` call, and include them
  in deps arrays so changes refetch.

Verified live: `/api/models?tool=claude-code` → 6 models / $10,001 (vs
23 / $10,633 unfiltered); `/api/timeseries/cost?project=…` → $3,731
vs $10,634; `/api/analysis/routing-suggestions` → `suggestions: []`
(not `null`); all 15 probed routes return 200.

### fix(dashboard): scoped KPI counts via `/api/status/scoped`

Operator-flagged inconsistency: Overview's Sessions / API Turns /
Token Rows tiles and Analysis's Total Sessions tile showed identical
all-time numbers regardless of window/tool/project filters, despite a
"window 30d" chip suggesting filter dependence. Root cause — those
tiles sourced from `diag.Snapshot`'s lifetime `SELECT COUNT(*)` block.

- **New endpoint**: `/api/status/scoped?days=&tool=&project=` returns
  `{days, sessions, api_turns, token_usage, actions}` scoped to the
  filter set. Walks the right joins per table (`sessions.tool` +
  `sessions.project_id` direct; `api_turns.project_id` direct + tool
  via session; `token_usage.session_id` → sessions for both project
  and tool; `actions.tool` + `actions.project_id` direct). Original
  `/api/status` stays as the global snapshot — still drives the CLI
  + Settings audit.

- **Frontend re-source**: Overview's three broken tiles and Analysis's
  Total Sessions tile now consume the scoped endpoint. Analysis label
  changed to `Sessions ({win})` to keep the chip honest.

- **discover engine**: `discover.Options` gained `Tool string`,
  threaded into `totalActions`, `staleReads`, `repeatedCommands`,
  `nativeVsBash`. `crossToolFiles` short-circuits to `[]` when a tool
  filter is active (single-tool can't be "cross-tool"; the alternative
  EXISTS approach timed out on the live action table). The Analysis
  Waste tile (`/api/analysis/headline`) now passes `Tool` +
  `ProjectRoot` to `discover.WastedTokens`; `/api/discover` honors a
  new `tool` query param; Discovery + Overview frontends pass it.

Verified live: `/api/status/scoped?days=30` → 304 sessions; `&days=7`
→ 47; `&tool=copilot-cli` → 5; `&project=…/superbased-observer`
→ 199. Analysis Waste: claude-code = $5.27 / 1.27M tokens;
copilot-cli = $0 / 0.

**Known gaps deferred to v1.6.3**: Compression Retrieval +
Compaction sub-panels still honor `days` only (not tool/project);
`cross_tool_files` returns `[]` when a tool filter is set rather
than redefining the panel's semantics; the v1.6.2-candidate Tier 1
+ Tier 3 output-tokens duplication noted above also still applies.

## [1.6.1] — 2026-05-17

### Dashboard polish + backfill 3-modes restored + Settings audit + full CI/CD pipeline overhaul

v1.6.1 follows v1.6.0 (shipped same day) with five workstreams: a
help-icon scroll-into-view fix, three previously-failing Backfill
modes brought back online with a new tracker dialog, a Settings
audit fixing six bugs (one critical — compression save was zeroing
config), Vite preload-error auto-recovery, and a full build/release
pipeline overhaul that adds triple-layer gating (local preflight →
ci.yml on push → npm-release.yml on tag). Build state at tag:
`npm run typecheck` + `make web-build` + `go vet ./...` + `make
build` all clean; all 10 dashboard routes return 200; the three
previously-failing backfill modes return `status: running`;
Compression + Proxy round-trip via `/api/config` preserving full
bodies.

**Dashboard polish:**

- **Help-icon scroll-into-view** (`web/src/components/HelpDrawer.tsx`).
  Clicking any `?` indicator now opens the drawer AND navigates to
  the matching entry via `itemRefs` + a two-frame
  `requestAnimationFrame` then
  `el.scrollIntoView({ block: "center", behavior: "smooth" })`. Prior
  behavior opened the drawer but left the entry off-screen.
- **Vite preload-error auto-recovery** (`web/src/main.tsx`). New
  `window.addEventListener("vite:preloadError", …)` handler reloads
  once per session on stale-chunk fetch failure, with a
  `sessionStorage` guard to break out if the chunk is genuinely
  missing. Catches the "tab cached old `index.html`; new `dist` has
  different hashes" class hit after every `make web-build`.

**Backfill — 3 previously-failing modes restored + tracker dialog:**

- **`antigravity` / `antigravity-project-root` / `gemini-cli`** brought
  back online (`cmd/observer/backfill.go`,
  `internal/intelligence/dashboard/backfill_run.go`,
  `internal/intelligence/dashboard/settings.go`). Added
  `--antigravity-rescan`, `--antigravity-project-root`, and
  `--gemini-cli-rescan` rescan paths mirroring the existing
  `--codex-rescan` / `--cowork-rescan` shape; `allowlistedBackfillModes`
  gains the three entries; `--all` now also fires the new rescans;
  `handleBackfillStatus` now advertises the real CLI flag names
  (previously surfaced descriptive labels like
  `--all (rescan + recover)` that weren't real args).
- **`BackfillTrackerDialog`** (`web/src/pages/Settings.tsx`). 720-px
  SlideOver with live-streaming output and auto-scroll-to-bottom
  while running. Closed-dialog banner above the list shows live-job
  count when jobs are in flight.

**Settings audit — 6 bugs (A–F):**

- **B (critical)**: compression spec used `path: []`, so the draft
  was the entire root config and the PUT body decoded as
  `CompressionConfig` — every save zeroed `cfg.Compression`. Fixed by
  setting `path: ["Compression"]` and making the group paths
  relative (`["Shell"]`, `["Conversation", "Stash"]`, etc.).
- **A**: proxy upstreams (`AnthropicUpstream` / `OpenAIUpstream` /
  `ChatGPTUpstream`) were not editable — a fake `Upstreams: list`
  field shadowed the three real string fields. Replaced with three
  explicit text fields matching `ProxyConfig`.
- **C**: removed the dead `code_graph` compression group
  (`cfg.Compression.CodeGraph` has zero Go consumers, verified via
  grep); the section description now points users to Settings →
  Intelligence → Code graph.
- **D**: header sub-copy claimed "restart-on-save plumbing arrives
  next PR", but every section was already editable. Rewritten to
  state pricing hot-reloads while every other section saves the file
  and surfaces the restart banner.
- **E**: Indexing.Embeddings carried upbeat copy despite no Go
  consumer. Now labeled "Embeddings (experimental — not yet wired)"
  with honest help text per `[[feedback-honest-disable-copy]]`.
- **F**: pricing `about.behavior` had a duplicated stutter
  ("restart-on-save plumbing arrives next PR; restart-on-save
  plumbing arrives next PR") + stale next-PR claim. Trimmed both.

PUT round-trip smoke verified: Compression + Proxy bodies survive
the save unchanged.

**CI/CD pipeline overhaul (triple-layer gate):**

- **`.github/workflows/npm-release.yml`** refactored into 4 jobs:
  `frontend` (build once, upload `web/dist` artifact) → `build`
  matrix × 5 (download artifact, sync into embed dir, cross-compile
  observer; linux variants ALSO cross-compile
  `antigravity-bridge.exe`; upload whole `bin/` folder) → `publish`
  (download `bin/` artifacts, `cp -R` into npm dirs) → `release`
  (create GitHub Release on private repo, body extracted from
  `CHANGELOG.md` via `awk`). Workflow now requires
  `permissions: contents: write` for the Release step.
- **`.github/workflows/ci.yml`** NEW. Triggers on `pull_request` +
  push to `main`. Two parallel jobs: `frontend` (`npm ci` +
  `typecheck` + `build` + dist-consistency check — fails if the
  committed `webapp/dist` drifted from a fresh build) and `go`
  (`vet` + `test -race` + build observer + cross-compile bridge).
- **`scripts/release.sh`** new `preflight_checks()` runs inside
  `cmd_tag` before tagging: `npm run typecheck` + `npm run build` +
  dist-consistency check + `go vet` + `go build` smoke +
  `GOOS=windows` bridge cross-compile smoke + `CHANGELOG.md
  ## [<version>] —` section assertion. Any failure exits BEFORE the
  tag is created, so the npm-release workflow never fires on a known-
  broken state.

Same dist-consistency check at all three layers
(release.sh preflight → ci.yml on push → npm-release.yml on tag).

**WSL2 / Antigravity adapter:** `@superbased/observer-linux-x64`
and `@superbased/observer-linux-arm64` npm packages now bundle
`antigravity-bridge.exe` alongside the observer binary —
`locateBridgeBinary` finds it via `filepath.Dir(exe)` lookup. No
clone, no `make build` needed for the Antigravity adapter on WSL2.

**Docs:**

- **`docs/release-runbook.md`** new "Build pipeline at a glance"
  section with full ASCII diagram of the 4-stage flow. "Releasing a
  new version" updated to walk through the preflight gate. Three
  new troubleshooting entries for the new failure modes (drifted
  dist / typecheck fail / missing CHANGELOG section).
- **`docs/developer.md`** NEW (248 lines). Developer guide covering
  the two-tier stack, prerequisites, Go-only vs React-contributor
  dev loops, every `make` target, running tests, coding standards,
  project layout, both CI workflows, releasing TL;DR, memory entries
  that influence dev workflow.
- **`README.md`** "Build from source" rewritten for the React/TS/
  Vite dual-stack (mentions `web/`, `make web-build`, Node 22 LTS);
  new "CI gates" subsection summarizing both workflows.
  Self-contained — no internal `docs/*` links that would 404 on the
  public repo.

**Surviving from v1.6.0:** two pre-existing dashboard test failures
(`TestAPITimeseriesActions`, `TestAPITools`) — fail on baseline too,
not introduced by this work. Worth investigating eventually.

## [1.6.0] — 2026-05-17

### Dashboard — Lane A + 6 rounds of Lane B operator feedback + DB-API perf sweep + Sessions Calendar fix

v1.6.0 builds on the v1.5.0 React dashboard cutover with four feature
items (Lane A), six rounds of operator feedback (Lane B / Rounds L–Q),
four DB-API performance fixes covering the longest-running endpoints,
and one Sessions Calendar day-click bug fix. Build state at tag:
`npm run typecheck` + `make web-build` + `make build` + `go vet ./...`
all clean; all 10 dashboard routes return 200; new endpoints smoke-
tested green.

**Lane A — finish prior-handover §2a + §2c (4 items):**

- **Sessions Filters drawer** (`web/src/pages/sessions/FiltersDrawer.tsx`).
  Slide-over with model multi-select + cost-range + actions-range +
  duration buckets + sidechain toggle + reliability filter. Versioned
  localStorage persistence (`sb:sessions:filters:v1`). Active-chip
  strip above the table + count badge on the toolbar button.
- **Antigravity bridge download link** — `make build` now ships
  `bin/antigravity-bridge.exe` (8.5 MB). The `AntigravityHelperCard`
  HEAD-probes `/api/admin/antigravity-bridge.exe` on mount and renders
  a green "Download .exe" CTA when present, warn copy ("rebuild
  observer with `make build`") when missing.
- **FilterBar `ComboChip` popover** (`web/src/components/primitives/ComboChip.tsx`).
  Filter-chip + portal-positioned combobox replaces native `<select>`
  for Tool + Project filters. Type-to-filter input, ↑↓+Enter+Esc
  keyboard navigation, click-outside dismiss.
- **⌘K command palette** (`web/src/components/CommandPalette.tsx`).
  Portal modal with 3 sections (Jump-to / Recent sessions / Recent
  actions), shared keyboard cursor, ⌘K-toggles-from-anywhere
  (handler at the App level).

**Lane B — 6 rounds of operator feedback on top of v1.5.0:**

- **Round L (initial fresh-feedback sweep)** — 11 items: Sessions
  Calendar honors the global Window (sourced from
  `/api/sessions/calendar`); Models cell uses provider-tinted
  `ModelDot` swatch + bare " + N" mono text (replacing the bordered
  pill); per-tool distinctive `ToolGlyph` glyphs (nested arcs for
  claude-code, `>_` for codex, 4-point sparkle for gemini, two linked
  circles for cowork, arrow for cursor, etc., ported from
  `design/provider-icons.jsx`) wrapped in a `ToolGlyphFrame` (18% bg /
  35% border); `DataTable` accepts a `loading` prop that drives an
  indeterminate top stripe + "Loading…" empty-state swap; Actions
  Target / Content column widths tightened to eliminate horizontal
  scroll; new `HeroStat` primitive (wide hero KPI tile, 1.4fr in xl
  grid, accent / danger / warn variants) swap-in on Compression
  ("Total compression savings", accent) + Discovery ("Estimated
  waste — last 30 days", danger); new `DensityBar` primitive (multi-
  segment bar with capacity rail) in Discovery's Reads Density column;
  Settings parity — icon + EDIT/READ-ONLY group polish on the section
  nav.
- **Round M (Calendar 7-day cap + Timeline single-dot fixes)** — new
  backend endpoints `/api/sessions/calendar` and `/api/actions/day-counts`
  so per-day rollups span the full configured Window (not the
  paginated page-50 slice). `/api/actions` accepts `from_date` /
  `to_date` (YYYY-MM-DD prefix against `substr(a.timestamp, 1, 10)`)
  so the Timeline day strip can scope per-day fetches. `HEAD` method
  added to `/api/admin/antigravity-bridge.exe` for the helper card's
  liveness probe.
- **Round N (Timeline rebuild attempt 1)** — horizontal day strip on
  top, entry cards below. Superseded in Round O.
- **Round O (Timeline final — vertical rail)** — Actions Timeline view
  redesigned around the design's Event-Log shape: `TimelineDayAxis`
  (horizontal day-chip strip, density-bar height per day, "All" reset
  chip) over `VerticalTimeline` rendered as a 3-column flex per row:
  timestamp (HH:MM:SS / Mon DD) | continuous 16-px rail with per-row
  colored dot marker (action-type color, danger ring on failure;
  first/last row line capped at the dot) | `EventLogCard`
  (ToolBadge + raw_tool_name + ActionTypeBadge + #id, target on the
  right, body FTS5 excerpt OR target fallback, footer status pill +
  effort + permission + source filename + session deep-link).
- **Round P (pagination + loading-on-day-switch)** — `<Pagination />`
  rendered below the vertical Event Log; busy days like Apr 23
  (2,275 actions) and May 15 (4,173 actions) walk through
  `TIMELINE_LIMIT=500` chunks. Stale-data fix:
  `rows={loading ? [] : data?.rows ?? []}` swaps the body to the
  loading skeleton during the in-flight refetch when `pickedDay`
  changes. Page resets to 1 on day pick.
- **Round Q (default-to-today + live-tail scoped)** — Timeline picks
  today on first entry, guarded by a `useRef` so a follow-up "All"
  click isn't clobbered by the same effect re-firing. "Loading
  forever" root-cause fix: live-tail interval ticks `tailNonce` every
  5 s, which sits in the actions useApi dep array; each tick aborted
  the in-flight Timeline fetch faster than the network round-trip
  could resolve. Live-tail now suppressed when `view === "timeline"`.

**DB-API performance sweep (operator complaint: messages table slow):**

- **`/api/session/<id>/messages` 136.2 s → 0.13 s (1048× on the largest
  1772-action session)**. Root cause: `LEFT JOIN action_excerpts ae ON
  ae.action_id = a.id` against the FTS5 virtual table, whose
  `action_id` column is declared `UNINDEXED`. SQLite has no b-tree on
  it, so each outer row triggered a full virtual-table SCAN. Fix: drop
  the LEFT JOIN; add a single batch `SELECT … FROM action_excerpts
  WHERE action_id IN (?, ?, …)` after the main row query via new
  `loadActionExcerpts(ctx, db, ids, maxBytes)` helper. One ~50 ms scan
  per request regardless of |ids|.
- **`/api/actions?limit=500` 38.1 s → 0.09 s (423×)**. Same FTS5
  UNINDEXED root cause via the correlated subquery inside the SELECT
  list. Same batch-load fix using the shared helper.
- **`/api/discover` 15.1 s → 0.52 s (30×)**. `discover.repeatedCommands`
  was an N+1+M anti-pattern: for each of up to 500 outer rows it ran a
  query to fetch run timestamps then **another** `SELECT COUNT(*) FROM
  actions WHERE action_type IN ('edit_file','write_file') AND …` per
  consecutive run pair. Replaced with one
  `LAG() OVER (PARTITION BY session_id, target, project_id ORDER BY
  timestamp) + NOT EXISTS` CTE in new `noChangeRerunCounts`. The dead
  `countNoChangeReruns` is removed.
- **`/api/sessions?limit=20` 0.63 s → 0.02 s (30× default page; 14× at
  limit=50; 3× at limit=200)**. `handleSessions` called
  `CostEngine.Summary` with `Days: 365`, no session filter, and
  `Limit: 100_000` — loading every row from `api_turns` + `token_usage`
  (~71 k rows here) into Go and rolling them up just to attach token
  totals to the ≤500 sessions visible on the current page. New
  `cost.Options.SessionIDs` field is plumbed through all three loaders
  (`loadProxyRows` / `loadJSONLRows` / `loadSummaryCallRows`) as
  `AND session_id IN (?, ?, …)`. `handleSessions` passes the page's
  session IDs so the engine only scans the relevant subset.

### Fixed

- **Sessions Calendar day-click filters server-side**. Click on
  2026-04-16 in the Calendar previously did `setLocalQuery("2026-04-16")`
  and switched to Table view; the local substring filter then ran
  against the most-recent page-50 of sessions (none of which were
  from April 16) and produced a misleading "No sessions match" empty
  state for any picked day older than the loaded slice. `handleSessions`
  now accepts `from_date` / `to_date` (mirroring `/api/actions`'s
  Round-M shape). `Sessions.tsx` swaps the calendar click from a
  local-query mutation to a `pickedDay` state that the
  `/api/sessions` fetch URL consumes; the active-chip strip shows a
  clearable `day: <date>` chip; empty-state copy distinguishes
  picked-day-with-no-results from search-query-with-no-results.

### Build, release, docs

- Build state at tag: `npm run typecheck` + `make web-build` +
  `make build` + `go vet ./...` all clean. All 10 dashboard routes
  return 200 on the embedded webapp. New endpoints (`/api/sessions/calendar`,
  `/api/actions/day-counts`, `HEAD /api/admin/antigravity-bridge.exe`,
  `/api/actions?from_date=&to_date=`, `/api/sessions?from_date=&to_date=`)
  return green data on a 81 k-action / 499-session real DB.

## [1.5.0] — 2026-05-16

### Dashboard — React/TS/Vite rewrite + design-parity + operator-feedback sweep

The dashboard is now a React + TypeScript + Vite + Tailwind app served
at `/` (the legacy vanilla SPA was retired in Phase 8). v1.5.0 marks
the major version bump for the cutover, alongside two rounds of
design-parity work and three rounds of operator-feedback systemic
fixes. Build state at tag: `npm run typecheck` + `make web-build` +
`make build` + `go vet ./...` all clean; all 10 dashboard routes
return 200.

**Backend additions (Go) — new endpoints + per-row enrichments:**

- `GET /api/analysis/cost-by-dow-hour` — 168-cell (7×24) aggregator
  for the "When you spend" 2D heatmap on the Analysis tab.
- `GET /api/patterns/timeseries` — per-day pattern reinforcements,
  segmented by `pattern_type`. Drives the Patterns "discovery over
  time" stacked bars.
- `GET /api/compression/by-model` — per-model × mechanism rollup of
  compression savings ($, bytes, events, est-tokens). Drives the
  Compression "Per-model breakdown" table.
- `POST /api/suggest` (preview) + `POST /api/suggest/write` — wrap the
  existing `internal/intelligence/suggest` package so the Patterns
  tab can render + persist `CLAUDE.md` / `AGENTS.md` / `.cursorrules`
  from the dashboard.
- `GET /api/sessions` — adds a `models` field per row (api_turns ∪
  token_usage GROUP BY session_id, model, ordered by turn count desc)
  so the Sessions Models col can render a primary chip + `+N`.
- `GET /api/actions` — adds `source_file`, `source_event_id`, and a
  280-char excerpt from the FTS5 `action_excerpts` table per row.
- `PUT /api/config/section/<id>` already existed; now wired
  end-to-end with a fully editable form on the frontend.

**Dashboard — design-parity passes (A–G) + operator-feedback (H–K):**

- New primitives: `PageHeader`, `IdLink`, `Toggle`, `CopyOnClick` (portal
  toast); `StatCard` extended with `icon`, `loading`, `cornerPill`,
  `linkTo`, `deltaPrior`, default + accent + warn gradient overlays,
  sparkline absolutely-positioned bottom-right. Sparkline now centers
  flat data instead of pinning the line to the bottom edge.
- Charts: gradient fills on CostArea / ActionsArea / CacheSavings /
  TokensByDay (Recharts linearGradient defs). New `DowHourHeatmap`
  component with log-scaled accent saturation + scale legend +
  fixed-height hover readout (no height jump on hover).
- Sessions: complete rewrite with cols matching design (no API$/Tool$
  cols; split surfaces in the Total$ tooltip), Models col live,
  Calendar view restyled with gradient cells + header summary + scale
  legend, Calendar day-click filters Table view to that day.
- SessionDetailPanel: 1400px wide (96vw cap), 4-tile KPI band with
  CostStat splitting API + Tool below the headline total, vibrant
  donut for action breakdown, Token Buckets with per-bucket help +
  share %, 14-col messages table with message IDs / per-message
  cache reads/writes/output/tools/API$/Tool$/Total$, expanded tool
  calls fully copyable (no truncation), pagination on the messages
  table (25/page).
- Settings: structured form per section (legacy `SECTION_FIELDS`
  parity restored), Compression rendered as 7 sub-groups (CodeGraph /
  Shell / Indexing / Conversation / Stash / Rolling / Compaction).
  Save POSTs the section, surfaces success / error / restart-required
  banner. Antigravity Windows-bridge helper card preserved.
- Patterns: real-data per-day distribution chart, `edit_test_pair`
  flow card layout, Generate / Preview / Write live against the
  `/api/suggest` family (respects the global Project filter).
- Analysis: 12 KPI tiles with icons + sparklines (Spend → daily cost,
  MTD → cumulative, Cache Savings → savings_usd from
  cache-savings-trend). "When you spend" is now the 2D dow×hour
  heatmap from the design.
- Actions: Effort col, live-tail indicator + pause button, active
  filter chip strip, Source + Content cols.
- Tools: % / count mix-mode toggle on ActionMixPanel; shared legend.
- Compression: SetupBanner with expandable Claude + Codex detail
  toggles + "Configure now" link; CompressionByModelTable mount.
- Discovery: single-tone density + frequency bars (was 2-tone
  overlay); CopyOnClick on file paths + commands.
- Global CSS: themed scrollbars (both axes) via
  `::-webkit-scrollbar*` + Firefox `scrollbar-color`; themed `<select>`
  + `<option>` so dropdown panels inherit dashboard tokens
  (eliminates white-on-white in dark mode).
- StatCard `loading` state propagated to every KPI tile across
  Overview / Cost / Analysis / Compression / Discovery / Tools so
  refetches visibly pulse the affected cards.

**v1.4.51 → v1.4.53 backend batch (included in this bump):**

- v1.4.51: per-session token + cost split (Input / Cache R / Cache W
  / Output + 3-way API$/Tool$/Total$ cost) on `/api/sessions`.
- v1.4.52: codex audit batch (web_search billing, reasoning tokens,
  rate limits, reasoning rows, `--codex-rescan` umbrella).
- v1.4.53: Claude Cowork adapter (capture, parser, normalization,
  cost-reconciliation endpoint), claudecode JSONL CRLF + empty-line
  cursor parity (#52/#53), foreign-OS path translation through
  `git.Resolve` (#54), `enabled_adapters` drift warning (#51),
  reasoning-tokens billing fix (output rate, LC-tier-aware).

**Compatibility:**

- The `/v2/` bookmark path is preserved via SPA index fallback —
  legacy bookmarks resolve to the same React route.
- All public API shapes either extended (new optional fields) or
  unchanged. The `/api/analysis/cost-by-hour` legacy endpoint is
  still served alongside the new `/api/analysis/cost-by-dow-hour`
  in case external consumers depend on it.

## [1.4.53] — 2026-05-15

### Added — Claude Cowork adapter (M0–M3)

User-driven workstream: capture Claude Cowork session data into
Observer's normalized schema. Cowork is Anthropic's "knowledge-work"
desktop product layered on top of Claude Code's CLI session model; on
Windows MSIX installs it stores per-local-instance audit logs at
`%LOCALAPPDATA%\Packages\Claude_*\LocalCache\Roaming\Claude\local-agent-mode-sessions\<cowork-uuid>\<device-uuid>\local_<instance>\audit.jsonl`
(macOS: `~/Library/Application Support/Claude/...`). Each local-
instance is one Observer session; `audit.jsonl` carries the canonical
user / assistant / system / tool_use_summary / rate_limit_event /
result records, plus the inner Claude Code session's rich usage
envelope (5m/1h cache-creation split, service_tier, inference_geo).

Full schema reference, design rationale, and per-task checklist in
`docs/cowork-adapter-plan.md`.

**M0 — Capture-only adapter:**

- New tool constant `models.ToolCowork = "cowork"`.
- 8 new `omitempty` fields on `ActionMetadata`:
  `CoworkProcessName`, `CoworkTitle`, `HostLoopMode` (sidecar-derived),
  `ServiceTier`, `InferenceGeo`, `CacheCreate5mTok`, `CacheCreate1hTok`
  (per-usage), `TotalCostUSD` (per-result, Cowork-authoritative).
- New `internal/adapter/cowork/` package: streaming audit.jsonl parser
  with `pending` map for tool_use ↔ tool_result pairing and
  msg.id-based dedup for streaming usage.
- Sidecar (`<device-uuid>/local_<id>.json`) loaded once per parse;
  fields lifted onto every event's `Metadata`. Project root resolves
  from `userSelectedFolders[0]` → `cwd` → "".
- 19-name `actionMap` covering Cowork's Claude-Code-derived tool
  surface. `Skill` + `mcp__*` fall through to `ActionUnknown` with raw
  name preserved.
- Identity: one Observer session per `local_<instance-uuid>/` dir.
  Pinned by `TestParseSessionFile_SessionIDIsLocalInstance` — the
  audit-internal `session_id` is observed varying within one file
  (sub-agent dispatch resets it); local-instance UUID is the stable
  anchor.
- Registered in `internal/adapter/defaults/defaults.go` + added to
  `EnabledAdapters` default in `internal/config/config.go::Default()`.
  v1.4.51 invariant tests
  (`TestAllAdapters_IsSessionFile_RequiresUnderWatchRoots` +
  `TestRegistryRootsNonOverlapping`) extend automatically; both pass.
- Reflection invariant `TestActionMetadata_IsZeroCoversEveryField`
  added (closes Invariant #50 follow-up — guarantees every new
  `ActionMetadata` field is covered by `IsZero` or the column
  marshals to non-NULL `{}` on sparse rows).

**M1 — Cross-platform paths + auto-detect:**

- `WatchPaths` uses `crossmount.AllHomes()` for WSL2 ↔ Windows
  bridging (same model as the codex adapter). Per-OS expansion in
  `candidateRoots`:
  - Windows MSIX: glob `<home>/AppData/Local/Packages/Claude_*/LocalCache/Roaming/Claude/local-agent-mode-sessions`
    (handles MSIX-hash rotation).
  - Windows non-MSIX: `<home>/AppData/Roaming/Claude/local-agent-mode-sessions`
  - macOS: `<home>/Library/Application Support/Claude/local-agent-mode-sessions`
  - Linux: skip — no known Cowork install path.
- `cowork.IsNativeTool` wired into the watcher's `NativePredicate`
  map in `buildWatcherWithOverride`, so `actions.is_native_tool` is
  set on cowork rows.
- Auto-registration is inherited from inclusion in the default set
  + `EnabledAdapters` — no manual `observer init` step needed.

**M2 — Rich metadata + sub-agent IsSidechain:**

- `tool_use_summary` records join to their matching `tool_use` action
  via `preceding_tool_use_ids[0]`; summary text lands on
  `Metadata.CoworkToolSummary`. Live data: 297 attachments.
- `rate_limit_event` records emit `ActionRateLimit` rows (new action
  constant) with 4 new typed metadata fields:
  `RateLimitStatus`, `RateLimitType`, `RateLimitResetsAt`,
  `RateLimitOverageStatus`. Generic naming so the codex 0.130+
  rate_limits workstream can reuse the schema. Live data: 53 rows
  including out-of-credits cases.
- Sub-agent `IsSidechain` cross-reference: `collectSidechainUUIDs`
  walks `<instance>/.claude/projects/**/subagents/agent-*.jsonl`
  once per parse, builds a set of assistant uuids, and the
  audit.jsonl handlers flag `IsSidechain=true` when `rec.UUID`
  matches. Resolves the §6 verification open question (audit.jsonl
  covers sub-agent rows but lacks the `isSidechain` flag — we
  back-fill from the inner transcripts). Live data: 64 of 898
  actions flagged sidechain.

**M3 — Cost reconciliation:**

- New `GET /api/cowork/reconcile` dashboard endpoint surfaces
  per-session Cowork-authoritative cost (`result.total_cost_usd`)
  vs Observer-derived cost (from `token_usage` × pricing-table)
  with absolute and percentage drift. Rows over 5% threshold are
  flagged. Unknown models surface as `unknown_model=true`.
- Live smoke against maintainer's install: 8 cowork sessions
  reconciled, all 8 over 5% threshold. Observer under-counts
  Cowork's claimed cost by ~21% systemically ($44.17 Cowork vs
  $34.70 derived). Real pricing-drift signal — the tile does its
  job; whether the gap is pricing-table stale vs Cowork-includes-
  overhead is for the operator to investigate.

**Tests added (32 new, all pass):**

- `internal/models/models_test.go`: 1 (reflection invariant; 22 sub-cases)
- `internal/adapter/cowork/adapter_test.go`: 10
- `internal/adapter/cowork/m2_test.go`: 4
- `internal/adapter/cowork/paths_test.go`: 5
- `internal/intelligence/dashboard/cowork_reconcile_test.go`: 5
- `internal/adapter/defaults/defaults_test.go`: cowork sub-cases pin
  the v1.4.51 dispatch-contract invariants automatically.

**Live smoke** (`/tmp/cowork-smoke-*/`) against maintainer's real
Cowork install (12 audit.jsonl files): 845–898 actions + 12 sessions
+ 3 models (opus-4-6 heavy, sonnet-4-6, haiku-4-5) ingested cleanly.
0 parse errors.

**Files touched (v1.4.53):**

- `internal/models/models.go` — `ToolCowork`, `ActionRateLimit`, 13 new `ActionMetadata` fields, `IsZero` extended
- `internal/models/models_test.go` (new) — `IsZeroCoversEveryField`
- `internal/adapter/cowork/{doc,adapter,adapter_test,m2_test,paths_test}.go` (new package)
- `testdata/cowork/cowork-aaaa/dev-bbbb/...` (new fixtures incl. subagent)
- `internal/adapter/defaults/defaults.go` + `defaults_test.go` — register cowork + fixture entry
- `internal/config/config.go` — add `"cowork"` to `EnabledAdapters`
- `cmd/observer/main.go` — import cowork + `NativePredicate` entry
- `internal/intelligence/dashboard/cowork_reconcile.go` (new) — endpoint + math
- `internal/intelligence/dashboard/cowork_reconcile_test.go` (new)
- `internal/intelligence/dashboard/dashboard.go` — route registration
- `docs/cowork-adapter-plan.md` (new) — comprehensive plan + checklists
- `docs/cowork-adapter.md` (new) — operator-facing reference
- `CHANGELOG.md` + `PROGRESS.md`

### Fixed — Post-ship bug classes surfaced against live data (collectively v1.4.54)

The M0–M3 implementation landed cleanly but the operator's iteration
against the live install surfaced four bug classes plus a cost-drift
investigation that produced a fifth fix. All folded into the v1.4.53
release.

**Cowork reconciliation tile not wired into the dashboard.** M3 added
the `/api/cowork/reconcile` endpoint but didn't surface it in the UI.
Added a "Cowork — cost reconciliation" panel inside the Cost tab in
`internal/intelligence/dashboard/static/index.html`. Renders only when
`sessions_total > 0`; hides on fresh installs.

**`enabled_adapters` existing-config drift (Invariant #51).** Adding
`"cowork"` to `config.Default()` is a no-op for users with an explicit
`enabled_adapters` list set by an earlier release — Default() only
applies when the key is absent. Documented one-liner in
`docs/cowork-adapter.md`'s Recovery section. Long-term fix candidate
(startup warning on missing-from-allow-list defaults) deferred to a
future release.

**CRLF byte accounting + empty-line cursor stall (Invariants #52, #53).**
Cowork's Windows-side audit.jsonl writer uses CRLF line endings.
`bufio.Scanner` strips `\r\n` from the returned token; pairing it with
`lineBytes = len(raw) + 1` undercounted CRLF lines by 1 byte each.
Plus: an empty trailing line (`\n` byte read when resuming from
`cursor = file_size - 1`) hit `if len(raw) == 0 { continue }` BEFORE
updating `res.NewOffset`. Combined effect: cursor stalled at
`file_size - 1`, watcher poll fired forever on every cowork audit
file. Fix: switched from `bufio.Scanner` to `bufio.Reader.ReadString('\n')`
(preserves the full terminator) AND moved `res.NewOffset = bytesRead`
to fire on every complete line (only partial trailing lines hold back).
Three regression tests in `internal/adapter/cowork/crlf_test.go`.

**Foreign-path → `git.Resolve` CWD-leak (Invariant #54, mirrors codex
v1.4.28 fix exactly).** Sidecars carrying `userSelectedFolders =
["C:\\..."]` passed Windows paths directly to `git.Resolve`. On Linux,
`filepath.Abs` treats Windows paths as relative, prepends observer's
CWD, and `findGitRoot` walks up to find observer's own `.git` — 6 of
12 cowork sessions misattributed to `/home/marmutapp/superbased-observer`.
Fix: delegate to `crossmount.TranslateForeignPath` (translates
`C:\foo` → `/mnt/c/foo` on WSL2) AND `os.Stat`-gate the result before
calling `git.Resolve`. New `cowork.ProjectAttribution` exported helper.
Project-less sessions whose `cwd` points inside Cowork's own storage
tree now synthesize `/sessions/<processName>` matching Cowork's
non-host-loop convention (Invariant #55). Two new regression tests.

**Backfill ergonomics.** Two new flags so the operator can fix
existing rows without a full `--all` rescan:

- `--cowork-rescan` — fast cowork-only rescan (12 audit.jsonl files,
  seconds). Equivalent to `observer scan --force --adapter cowork`
  but discoverable via the dashboard Backfill UI.
- `--cowork-project-root` — re-attribute existing rows by re-running
  `cowork.ProjectAttribution` on each local_*/audit.jsonl and
  UPDATEing sessions + actions on mismatch. Modeled on
  `backfillCodexProjectRoot`. Bundled into `--all`.

### Fixed — Token ingest from result.modelUsage (closes 21% cost drift)

**Cost-drift investigation findings (drops live reconciliation drift
from 21.4% → 2.5%).** The maintainer's live install showed Cowork
reporting $44.17 across 8 sessions vs Observer-derived $34.70 (21.4%
drift). Read-only investigation
(`docs/cowork-cost-drift-investigation-2026-05-15.md`) decomposed the
drift into three causes:

1. **Streaming-snapshot stale `output_tokens`** in audit.jsonl
   assistant rows — most records carry `stop_reason: null` and
   `output_tokens=0`. For f70e7c7a (sonnet-only): Observer's
   audit-derived sum = 4,339 output tokens; Cowork's
   `result.modelUsage` says 102,371 (23.6× under-count). Inner
   Claude-Code transcript agrees with audit — Cowork bills from the
   SDK's ApiTracker which sees `message_stop`, not the audit log.
2. **Haiku shadow cost** — 6 of 8 sessions have
   `claude-haiku-4-5-20251001` token consumption in
   `result.modelUsage` with **zero corresponding assistant rows** in
   audit.jsonl. Cowork dispatches background haiku work outside the
   transcript stream (7b03e00c: 517,928 input tokens of haiku
   invisible to assistant-row ingest).
3. **Sonnet-4-6 pricing-rate delta** — Cowork's per-token cost for
   `claude-sonnet-4-6` is a consistent 1.67× Observer's pricing-table
   rates. Opus-4-6 matches exactly (1.00×). Hypothesis: Cowork
   mis-routes sonnet-4-6 to the opus-4-6 rate card (since 5/3 =
   1.667). Cannot be verified without Anthropic billing dashboard
   ground truth — left as Phase 2.

**Fix (Phase 1):** Switched cowork adapter token ingest from
per-assistant-row `message.usage` to per-result `result.modelUsage`.
Closes causes #1 and #2 in one move:

- `internal/adapter/cowork/adapter.go::handleAssistant` no longer
  emits TokenEvents (kept content parsing for assistant_text /
  tool_use / sidechain attribution).
- `internal/adapter/cowork/adapter.go::handleResult` now emits one
  `TokenEvent` per `(result.uuid, model)` from `rec.ModelUsage`.
  `SourceEventID = result.uuid + ":" + model` for stable resume;
  `MessageID = "result:" + result.uuid + ":" + model`. Reliability
  bumped from `ReliabilityUnreliable` to `ReliabilityAccurate` since
  modelUsage is Cowork's authoritative source.
- 5m/1h cache-creation tier split derived per-result from the
  top-level `result.usage.cache_creation` ratio, applied uniformly to
  each model's `cacheCreationInputTokens`. Approximate (the tier
  split is per-final-turn, not per-batch) but matches Cowork's own
  accounting for opus-4-6 within rounding.
- New struct `rawResultModelUsage` decodes the per-model breakdown.
  `rawRecord` extended with `TopUsage *rawUsage` and
  `ModelUsage map[string]rawResultModelUsage` (populated only on
  result records).

**Live verification:** After re-backfill via
`bin/observer backfill --cowork-rescan`, the reconciliation tile drift
drops from 21.4% to 2.5% across the maintainer's 8 cost-claiming
sessions. Action count unchanged (898 — UNIQUE index kept idempotent);
token_usage rows replaced (508 stale per-assistant rows → 56
modelUsage-derived rows across 8 sessions × 3 models, with
previously-invisible haiku now properly accounted).

**Tests:** Two new tests in `internal/adapter/cowork/adapter_test.go`:
- `TestParseSessionFile_ModelUsageEmitsTokenEventPerModel` — pins
  one TokenEvent per (result.uuid, model), including a haiku entry
  with zero corresponding assistant rows (the shadow-cost case).
- `TestParseSessionFile_AssistantRowsDoNotEmitTokenEvents` — pins
  that an assistant-only audit.jsonl (no result record) emits zero
  TokenEvents but ToolEvents are unaffected.

`TestParseSessionFile_CacheCreationSplit` updated to assert against
the opus TokenEvent (looked up by Model name, since sort-by-name
puts haiku first now). Fixture `audit.jsonl` extended: result record
now carries `usage` (with cache_creation tier split) and `modelUsage`
(opus + haiku entries summing to the existing total_cost_usd=0.0125).

### Fixed — Phase 2: web_search billing in cost engine (closes haiku 1.4-1.5× residual)

**Phase 2 outcome** (cross-checked against Anthropic's authoritative
2026-05-15 pricing supplied by the operator): Observer's pricing
table is verified correct per Anthropic. The residual 2.5% drift
after Phase 1 decomposes into THREE Cowork-side billing
inaccuracies reproducible to penny precision:

1. Cowork bills `claude-sonnet-4-6` token traffic at `claude-opus-4-6`
   rates (5/3 = 1.667× the correct sonnet rate). Verified penny-perfect
   on every sonnet result record: f70e7c7a result#1 $0.527 = opus-rates
   $0.5274; result#4 $4.7001 = opus-rates $4.7001 exact.
2. Cowork bills opus `cache_creation` at the $6.25/M (5m) rate
   regardless of stored ephemeral tier — even for the 100%-1h-tier
   sessions where Anthropic's invoice should apply $10/M. 7b03e00c
   has 628,137 1h-tier opus cw tokens; Cowork's $14.68 = `cw × 6.25`,
   real Anthropic bill = $17.03.
3. Server-side web_search ($10/1000 calls) was not modeled in
   Observer's cost engine at all — captured as Phase 2 work.

**Phase 2 fix scope (foundational cost-engine update, benefits all
adapters):**

- Migration `018_web_search_requests.sql` — new INTEGER column
  `web_search_requests` on `token_usage` and `api_turns`.
- `models.TokenEvent.WebSearchRequests` and `models.APITurn.WebSearchRequests`
  fields added.
- `cost.Pricing.WebSearchPerRequest float64` — flat per-request fee
  ($/call, NOT $/1M-tokens) separate from the existing per-token
  rate fields. No LC tier (LC repricing applies only to token
  rates).
- `cost.TokenBundle.WebSearchRequests int64` + accumulator support.
- `cost.Compute()` adds `b.WebSearchRequests × p.WebSearchPerRequest`
  to the total.
- Default pricing entries for Anthropic 4.x models
  (`claude-opus-4-{5,6,7}`, `claude-sonnet-4-{5,6}`, `claude-haiku-4-{4,5}`)
  populated with `WebSearchPerRequest: 0.01`.
- `store.InsertTokenEvents` + `store.InsertAPITurn` write the new
  column.
- Cowork adapter `handleResult` emits `WebSearchRequests` from
  `rec.ModelUsage[model].WebSearchRequests`.
- `cowork_reconcile.go` updated to SUM `web_search_requests` into
  the TokenBundle so the engine's per-request fee applies in the
  reconciliation tile's derived cost.

**Tests:**

- New `TestCompute_WebSearchPerRequest` in `internal/intelligence/cost/`
  pins the flat per-request math (no /1M scaling), the combined
  tokens+web-search calculation, and the zero-rate fallback for
  non-Anthropic entries.
- New `TestParseSessionFile_ModelUsageEmitsWebSearchRequests` in
  `internal/adapter/cowork/` pins haiku's WebSearchRequests=3
  emission from modelUsage (with the opus entry's
  WebSearchRequests=0 confirming no cross-model bleed).
- `TestParseSessionFile_ResultCarriesTotalCost` updated for the
  fixture's new total_cost_usd=0.0425 (was 0.0125; bumped by +$0.03
  for 3 web searches × $0.01 to keep the fixture internally
  consistent).

**Live verification:** After Phase 2 backfill, 42 web_search_requests
captured across 2 sessions (5afdac1e: 11; 7b03e00c: 31) — exactly
matching what Cowork's `modelUsage.webSearchRequests` reports. 3bb5ea14
(28 WebSearch action rows) and ee3ab099 (16 action rows) report
`modelUsage.webSearchRequests=0` despite the tool calls — these are
SDK-internal / cached web searches that didn't hit Anthropic's
billed endpoint, so they correctly don't appear in cost.

**Final reconciliation tile state:** 21.4% (pre-fix) → 2.53% (after
Phase 1, token ingest from modelUsage) → 3.48% (after Phase 2,
web_search added). The slight Phase-2 drift INCREASE is intentional:
Observer now correctly applies Anthropic's $10/1000 web_search fee,
which Cowork's modelUsage already includes — the net change is the
proper attribution of that fee in Observer's derived cost. The
remaining +3.48% drift is now cleanly partitioned into two
diagnostic signals (Cowork's two billing bugs), per-session
direction varying based on opus-vs-sonnet token mix.

**What Phase 2 explicitly does NOT do:**

- No patch to `claude-sonnet-4-6` or `claude-opus-4-6` base rates
  in `pricing.go`. Observer's table is verified correct against
  Anthropic's authoritative pricing. The residual drift IS the
  Cowork bug, surfaced exactly as the reconciliation tile is
  designed to do.
- No `inference_geo` 1.1× data-residency multiplier (worth adding
  for codex/antigravity traffic that DOES set `inference_geo: "us"`,
  but irrelevant for current cowork dataset which has
  `inference_geo: ""`).
- No fast-mode 6× detection (Opus 4.6/4.7 beta), no Managed Agents
  $0.08/hour metering — deferred until use cases land.

See `docs/cowork-cost-drift-investigation-2026-05-15.md` for the
full verified-against-truth analysis and the per-result penny-perfect
reverse-engineering.

### Fixed — claudecode adapter CRLF + empty-line cursor stall (Invariants #52/#53 preempt)

Mirrors the cowork v1.4.54 fix shape proactively onto the claudecode
adapter before it bites on a Windows-side / cross-mount claudecode
transcript. Same bug class as the cowork case:

- `bufio.NewScanner(f)` + `lineBytes := int64(len(raw) + 1)`
  undercounted CRLF lines by 1 byte each, stranding the cursor
  short of EOF and causing the watcher to repoll the same range
  forever.
- `if len(raw) == 0 { continue }` skipped the
  `res.NewOffset = bytesRead` commit on empty trailing lines, same
  stall mode.

Fix: switched the parsing loop in
`internal/adapter/claudecode/adapter.go::ParseSessionFile` from
`bufio.NewScanner` to `bufio.Reader.ReadString('\n')` (preserves the
exact terminator length) and moved the `res.NewOffset = bytesRead`
commit so it fires on every complete line, including empty,
malformed, and unknown-type lines. Partial trailing lines (no `\n`,
EOF) are deferred to the next poll so the watcher doesn't read a
half-written record.

The 16 MiB `maxLine` cap is gone — `bufio.Reader.ReadString` grows
dynamically across buffer boundaries, so the previous cap is no
longer needed; trades a fixed buffer for transient allocation on
the rare giant tool-output line.

Removes the now-unused `errors` import. Existing claudecode tests
that built JSONL fixtures via `strings.Join(records, "\n")` (no
trailing newline) were updated to append `"\n"` — matches real
Claude Code transcripts on disk and pins the deferred-partial-line
contract.

Three new regression tests in
`internal/adapter/claudecode/crlf_test.go`:

- `TestParseSessionFile_CRLF_CursorReachesEOF` — CRLF body, asserts
  NewOffset == file size.
- `TestParseSessionFile_TrailingPartialLine_DoesNotAdvance` —
  pins the partial-line deferral.
- `TestParseSessionFile_TrailingEmptyLine_AdvancesPastIt` — pins
  the empty-trailing-line cursor advance.

Also updated `cmd/observer/backfill_test.go::TestBackfillClaudeCodeAPIErrors`
fixture to add the trailing newline so the api_error record on the
last line is processed by the v1.4.20 recovery pass under the new
parser contract.

### Fixed — Codex web_search billing + rate_limits + reasoning capture (provider-normalization parity with cowork)

Operator-driven audit of live session
`019e2bb0-6042-7571-962b-f03b9303653a` (gpt-5.5, 1 user turn, 9
web_search_end events, 10 reasoning items, 2 token_count events
carrying rate_limits) surfaced three coverage gaps in the codex
adapter that the cowork Phase 2 work had already addressed for
Anthropic. All three are closed in this batch — codex now sits at
the same provider-normalization level as cowork for web_search
billing, rate-limit snapshots, and reasoning visibility.

**1. Web search billing wired through the cost engine.**
`internal/adapter/codex/adapter.go` gains a session-scoped
`runningWebSearchCount int64` that increments on every
`event_msg/web_search_end` and flushes onto the next non-dedup
`event_msg/token_count`'s `TokenEvent.WebSearchRequests`, where the
cost engine then applies `Pricing.WebSearchPerRequest` as a flat
per-call fee (Invariant #57 mechanics, mirroring cowork). Counter
resets after each flush so adjacent turns attribute their searches
independently. Live verification on the maintainer's install: 9
web searches on the target session now flow through to
`token_usage.web_search_requests=9`, lifting derived cost from
$0.445 → $0.670 (Δ = 9 × $0.025 = $0.225 of previously-invisible
search billing). 13 web_search billable calls captured across 2
sessions globally.

`internal/intelligence/cost/pricing.go` adds
`WebSearchPerRequest: 0.01` to every gpt-5 family entry,
matching Anthropic's flat rate. Operator-confirmed against
OpenAI's published 2026-05-15 rate card: **$10 per 1k calls**
($0.01/call). "Search content tokens are free" per the same
card — no separate token billing layer; the flat per-call fee
is the entire web-search charge. Initial commit used $0.025
(speculation against a tiered-by-search_context_size pricing
model that turned out not to apply); corrected based on the
authoritative rate.

**2. token_count.rate_limits emitted as ActionRateLimit rows.**
Codex 0.130+ embeds a `rate_limits.{primary,secondary,plan_type,rate_limit_reached_type}`
envelope on every `event_msg/token_count`, INCLUDING the startup
one where `info: null` (so the snapshot fires before any usage
exists). New `buildCodexRateLimitEvent` maps the envelope into the
generic schema cowork introduced (`RateLimitStatus` from
`rate_limit_reached_type` or "ok"; `RateLimitType` from
`limit_id`; `RateLimitResetsAt` from `primary.resets_at`;
`RateLimitOverageStatus` from `plan_type`) and stores the full
envelope verbatim in `RawToolInput` so the dashboard can render
both windows without losing the secondary one. Stable
`source_event_id` (`ratelimit:<file>:L<linenum>`) keeps re-parses
idempotent. Live: 843 codex rate_limit rows materialized across
the live install (2 on the target session).

**3. response_item.reasoning rows visible in the action stream.**
Codex Desktop emits one `response_item.reasoning` per turn
component carrying `encrypted_content` (opaque) plus a `summary[]`
array that's empty in every 2026-05 build inspected. Pre-fix the
adapter folded these into the per-turn `agentMessages` cache for
`PrecedingReasoning` propagation but never surfaced them in
actions — operators saw "10 reasoning items happened" only via
the rollout file, not the dashboard timeline. v1.4.53 keeps the
agentMessages threading AND emits one `codex.reasoning` ToolEvent
per reasoning block (ActionTaskComplete + `RawToolName=codex.reasoning`).
Summary text lands in `Target`/`ToolOutput` when present;
empty-summary items show `"(encrypted reasoning, N bytes)"` so
the reasoning's existence is visible even when content is
unrecoverable. Live: 746 codex.reasoning rows materialized
globally (10 on the target session).

**4. `observer backfill --codex-rescan` umbrella flag.** Models on
`--cowork-rescan`: fast codex-only `Rescan` that re-walks every
codex rollout from offset 0, picking up all three additions on
historical rows. Bundled into `--all`. Surfaces in the dashboard
Backfill settings table.

**5. `token_usage` ON CONFLICT extended to backfill
`web_search_requests`.** The existing `ON CONFLICT(source_file,
source_event_id) DO UPDATE` clause only refreshed `model`. A
rescan that re-emitted the same `tk:<file>:L<linenum>`
source_event_id with a newly-populated `WebSearchRequests`
therefore got dropped silently. Extended the clause to set
`web_search_requests` from the excluded row when (a) the excluded
value is non-zero AND (b) the existing value is zero/NULL. Other
token columns stay untouched (they don't change across rescans
under stable source_event_ids). Without this fix `--codex-rescan`
would correctly insert new rate_limit / reasoning rows but leave
the existing token_usage row's web_search_requests at 0 — caught
by live-verification on the target session.

**6. Cost engine SQL pulls the new column on both paths.**
`internal/intelligence/cost/summary.go::loadJSONLRows` and
`loadProxyRows` now SELECT and Scan `web_search_requests` into
`TokenBundle.WebSearchRequests` so per-session/per-model/per-day
cost rollups see the billing. Without this `bin/observer cost` /
the dashboard Cost tab would render the same $0.445 as before
the fix even with the DB column populated. `isNoiseRow` also
updated so a token row carrying only web_search_requests (no
input/output/cache tokens) is no longer dropped as zero-token
noise.

**Tests added:**

- `TestParseRolloutWebSearchCountAttribution` — pins 3 web searches
  in one turn flow through to `TokenEvent.WebSearchRequests=3`.
- `TestParseRolloutWebSearchCountResetsAcrossTokenCounts` — pins
  counter reset between adjacent turn flushes.
- `TestParseRolloutRateLimitsCapturedFromTokenCount` — pins
  startup (`info: null`) + end-of-turn rate_limits both emit
  ActionRateLimit rows, with the full envelope in
  `RawToolInput`.
- `TestParseRolloutReasoningEmptySummaryEmitsPlaceholderRow` —
  pins the encrypted-only reasoning case (the 100% shape today)
  emits a row with the byte-count proxy in `Target`.
- `TestParseRolloutResponseItemReasoning` updated for the new
  dual-row shape (codex.reasoning + the downstream
  exec_command_end that inherits PrecedingReasoning).

**Files (this batch):**

- `internal/adapter/codex/adapter.go` — runningWebSearchCount +
  TokenEvent flush on both token_count paths;
  `rate_limits` struct + `parseModernRateLimits` +
  `buildCodexRateLimitEvent`; response_item.reasoning emits row
  via new `buildCodexReasoningEvent`; new `webSearchRawInput`
  helper serializes `action.queries[]` fan-out as JSON into
  RawToolInput (3-4 sub-queries per top-level call, previously
  collapsed to just the top-level Query string).
- `internal/intelligence/cost/pricing.go` — WebSearchPerRequest
  $0.025 on all gpt-5 family entries.
- `internal/intelligence/cost/summary.go` — SELECT + Scan
  web_search_requests on JSONL + proxy paths; isNoiseRow update.
- `internal/intelligence/dashboard/dashboard.go` +
  `analysis.go` — 5 CTE/SELECT/Scan sites updated to pull
  `web_search_requests` into `TokenBundle.WebSearchRequests`
  (`handleSessionDetail`, `handleCostHC`, `handleCacheSavings`,
  `handleAnalysisSessions`, `handleAnalysisCacheSavingsTrend`).
  Without this, per-session/per-day cost rollups would still
  render the pre-fix $0.4452 even with the DB column populated.
- `internal/store/store.go` — token_usage ON CONFLICT backfills
  `web_search_requests`; actions ON CONFLICT refreshes
  `raw_tool_input` when (a) existing is empty or (b) excluded
  payload is strictly longer (length heuristic; rescan-richer
  payloads win, accidental-truncation regressions don't).
- `cmd/observer/backfill.go` — `--codex-rescan` flag, bundled into
  `--all`.
- `internal/intelligence/dashboard/backfill_run.go` +
  `settings.go` — dashboard wiring for the new mode.

### Fixed — Reasoning tokens are now billed (closes a systemic under-charge across every reasoning-capable model)

Operator-driven follow-up audit surfaced a foundational cost-engine
gap that the v1.4.53 web_search work didn't touch: **`TokenBundle`
had no `Reasoning` field** and **`Compute()` never multiplied
reasoning_tokens by the output rate**. Both OpenAI (o1/o3/o4/gpt-5
`reasoning_output_tokens`) and Anthropic (extended-thinking output
portion) bill reasoning at the standard output rate, but Observer
was systematically dropping that line from every per-session,
per-model, and per-day cost rollup. For the target session
`019e2bb0` that's $0.045 missing (1496 tokens × $30/1M); across the
maintainer's 24 reasoning-bearing codex sessions it's **$6.53 of
previously-uncharged spend (217,618 reasoning tokens)**.

**Cost engine:**
- `cost.TokenBundle.Reasoning int64` field added (with `Add()` accumulator).
- `Compute()` now adds `b.Reasoning × rates.Output / 1e6` to the
  total, AFTER LC-tier dispatch — reasoning is part of the model's
  output stream, so the LC-tier output rate applies when prompt
  size crosses the threshold.
- New `TestCompute_ReasoningTokensBilledAtOutputRate` pins the
  reasoning-only, combined-with-tokens, and LC-tier-dispatch
  shapes. Default rate is "billed at p.Output" — no per-model
  pricing-table change needed (reasoning ≡ output across providers).

**SQL paths plumbed end-to-end:**
- `cost/summary.go::loadJSONLRows` SELECTs `reasoning_tokens` from
  `token_usage` into `TokenBundle.Reasoning`; `isNoiseRow` updated
  so a reasoning-only row isn't dropped as zero-token noise.
- `cost/summary.go::loadProxyRows`: no change — `api_turns` has no
  `reasoning_tokens` column (the proxy already folds reasoning into
  `output_tokens` at capture, so the existing math is correct).
- `dashboard/dashboard.go::handleSessionDetail` +
  `handleSessionMessages` CTEs add `0 AS reasoning_tokens` for the
  api_turns arm and `reasoning_tokens` for the token_usage arm of
  the UNION ALL, with matching Scan slots.
- `dashboard/analysis.go` (5 CTE sites: headline, cost-hc,
  cache-savings, analysis-sessions×2, cache-savings-trend) all
  updated symmetrically.
- `dashboard/cowork_reconcile.go::SUM(reasoning_tokens)` so the
  reconciliation tile's derived-cost includes reasoning where
  cowork modelUsage reports it.

**Dashboard rendering:**
- New `Reasoning` column on the Cost-tab model breakdown table —
  always visible, blank cell for non-reasoning models.
- Session-detail token-bucket table conditionally appends a
  `Reasoning` row when the session total is non-zero (keeps
  non-reasoning sessions uncluttered).
- Session-detail per-model breakdown gains a `Reasoning` column
  (em-dash for models that didn't emit any).
- Per-message timeline row table gains a `Reasoning` column —
  this is the row the operator's screenshot showed missing the
  data. Tooltip explains OpenAI vs Anthropic billing semantics.

**Backend JSON:**
- `modelBucket` struct: `Reasoning int64 \`json:"reasoning,omitempty"\``.
- Session-detail `tokens` map: `"reasoning": totalReasoning`.
- `messageRow` struct (`/api/session/<id>/messages`):
  `Reasoning int64 \`json:"reasoning,omitempty"\``.

**Live verification on session 019e2bb0:**

| | input | output | cache_read | reasoning | web_search | cost |
|---|---|---|---|---|---|---|
| Pre-v1.4.53 | 71135 | 2738 | 14720 | 1496 (ignored) | 9 (ignored) | $0.4452 |
| Post web_search + reasoning fix | 71135 | 2738 | 14720 | 1496 ($0.0449) | 9 ($0.09 at $0.01/call) | **$0.5801** |

Per-row codex.reasoning rows continue to render as
`(encrypted reasoning, N bytes)` — per-row token attribution
would require buffering reasoning items until each token_count
flush and proportionally splitting by encrypted_content size, an
approximation whose accuracy isn't worth the complexity. Session
total is the authoritative number; visible in both the
per-message timeline row and the Reasoning column.

### Fixed — observer start warns when registered defaults are missing from `enabled_adapters` (Invariant #51 long-term fix)

Closes the v1.4.53 carryover for Invariant #51: `config.Default()`
only seeds `enabled_adapters` when the key is absent, so adding a
new adapter to the registered defaults is silently a no-op for any
user whose `config.toml` pins an explicit allow-list from a prior
release. (The cowork rollout surfaced this — the new adapter
registered, watched paths existed, but the allow-list filter
dropped every event.)

`buildWatcherWithOverride` in `cmd/observer/main.go` now compares
the registered defaults against the user's explicit
`enabled_adapters` list and emits a single WARN per startup naming
each missing tool, plus the exact append-this-string remediation
the user can paste into their `config.toml`'s
`[observer.watch] enabled_adapters`. Silent when the allow-list is
empty (fresh installs let `Default()` populate the full set) or
when every registered default is already present. Skipped on the
`--adapter <name>` override path (where filtering is the user's
intent).

New helper `warnMissingDefaultsFromAllowList` is independently
tested via `cmd/observer/missing_adapter_warning_test.go` with 5
cases pinning the empty-allow-list silence, full-match silence,
single-missing emission, multi-missing emission, and extra-unknown-
tool silence shapes.

### Fixed — Antigravity reasoning_output_tokens captured (1.3.1.17.2.9, Gemini-only)

Operator-driven path-inventory dig surfaced a coverage gap in the
antigravity adapter parallel to the codex/cost-engine reasoning
fix above: `ParseStructuredTrajectory` was mapping
**`1.3.1.17.2.3 → OutputTokens`** but that field is the **total
output (response + reasoning) on Gemini sessions**, not response-
only. Reasoning was being absorbed into OutputTokens with no
separate attribution, so the new Reasoning column on the dashboard
read `0` for every antigravity row (8,787 rows total) even on
Gemini Pro / Flash sessions that emit thinking tokens.

**Empirical evidence** (`TestTokenRowDump` against 5 live sessions
covering Gemini Pro low/high, Gemini Flash, and Claude Sonnet 4.6
via the language_server gRPC bridge, 33 turns total):

| Path | Wire | Semantics | Verified |
|---|---|---|---|
| `1.3.1.17.2.1` | varint | input_tokens | already mapped |
| `1.3.1.17.2.2` | varint | cache_creation_input_tokens | already mapped |
| `1.3.1.17.2.3` | varint | **total** output = .9 + .10 | re-interpreted |
| `1.3.1.17.2.5` | varint | cache_read_input_tokens | already mapped |
| `1.3.1.17.2.6` | varint | constant per session (24 Gemini, 26 Claude) — header overhead, NOT a count | n/a |
| `1.3.1.17.2.9` | varint | **reasoning_output_tokens** (Gemini-only; field absent on Claude) | **NEW MAPPING** |
| `1.3.1.17.2.10` | varint | response_output_tokens (text-only); mirror of .3 on Claude | **NEW MAPPING** |

Universal invariant `.3 == .9 + .10` holds on every observed turn
across all 5 sessions / 33 turns. On Claude `.9` is absent → .10
== .3.

**Adapter change** (`internal/adapter/antigravity/structured.go`):

- `turnTokens` struct gains `reasoning uint64`.
- Walker's path-switch now reads `.10 → r.output` (was `.3`) and
  `.9 → r.reasoning`. `.3` is no longer mapped (it's the sum we
  reconstruct from .9 + .10).
- Empty-turn guard updated to consider `reasoning == 0` alongside
  the existing fields.
- TokenEvent emission gains `ReasoningTokens: int64(r.reasoning)`.
- Path-mapping comment block at the top of `ParseStructuredTrajectory`
  rewritten to document the empirical relationship.

**Cost preservation:** under the codex/cost-engine convention
(Invariant #58 — reasoning + output additive, both at output_rate),
cost = `OutputTokens × rate + ReasoningTokens × rate` =
`(.10 + .9) × rate` = `.3 × rate`. Total identical to the pre-fix
math; the breakdown is now visible.

**Tests:** existing synthetic test (`TestParseStructuredTrajectory_Synthetic`)
extended with reasoning assertions (turn 0 carries 12 reasoning,
turn 1 mirrors Claude with `.9` absent). `buildTurn` helper takes
a new `reasoning` arg; all 6 call sites updated. Live-bridge
acceptance test `TestStructuredReasoningTokenCapture` (gated on
`OBSERVER_AG_REASONING_TOKEN_CHECK=1`) asserts Gemini sessions
emit non-zero reasoning and Claude sessions zero.

**Live verification** (gRPC bridge, 5 sessions, all PASS):

| sid | model | turns | output | reasoning |
|---|---|---|---|---|
| 162c4ab9 | gemini-3.1-pro-low | 5 | 1728 | 2111 |
| ae703782 | gemini-3-flash-agent | 9 | 1632 | 2396 |
| 462469ee | claude-sonnet-4-6 | 4 | 1394 | 0 ✓ |
| da973a42 | gemini-pro-agent | 6 | 2063 | 3698 |
| fc8d44e5 | claude-sonnet-4-6 | 9 | 5431 | 0 ✓ |

**Backfill:** existing 8,787 antigravity rows have OutputTokens=.3
(includes reasoning) + ReasoningTokens=0. Total cost is unchanged
(`.3 × rate` either way), so no operator action is required for
billing correctness. To re-decompose for visibility:
`observer scan --force --adapter antigravity`. Idempotent via
`(source_file, source_event_id)` UNIQUE; refreshes both fields
under stable IDs.

**Invariant #63 (Antigravity output_tokens decomposition).**
`1.3.1.17.2.3` is the TOTAL output emission (response + reasoning);
`1.3.1.17.2.9` is the reasoning portion (Gemini-only); `1.3.1.17.2.10`
is the response-only portion. Adapter maps `.10 → OutputTokens`
and `.9 → ReasoningTokens` so cost = (output + reasoning) × output_rate
matches the pre-decomposition `.3 × output_rate` — codex convention
of reasoning+output additive, never subset.

**Cross-adapter reasoning-coverage sweep** (alongside the antigravity
fix): audited all 11 default adapters for reasoning_tokens capture.
Verdict — coverage is now complete:

| Adapter | Provider class | Reasoning capture |
|---|---|---|
| codex | OpenAI | ✓ (this batch — `reasoning_output_tokens`) |
| copilot | OpenAI | ✓ (pre-existing — `metadata.toolCallRounds[].thinking.tokens`) |
| opencode | provider-agnostic | ✓ (pre-existing — `msg.Tokens.Reasoning`) |
| gemini-cli | Gemini | ✓ (pre-existing — `thoughtsTokenCount`) |
| antigravity | Gemini + Claude | ✓ (this session — `1.3.1.17.2.9`) |
| claudecode | Anthropic | implicit — API folds thinking into `output_tokens` |
| cowork | Anthropic | implicit — `result.modelUsage.outputTokens` folded |
| cline | Anthropic | implicit — `usage.output_tokens` folded |
| openclaw | Anthropic | implicit — `tokenUsage.output` folded |
| pi | Anthropic | implicit — `tokenUsage.output` folded |
| cursor | hook-only | n/a — no token-row emission from adapter |

The five Anthropic-direct adapters have no `reasoning_tokens` field
in their on-disk usage envelopes because Anthropic's `/v1/messages`
API folds extended-thinking output into `output_tokens` (per
docs.anthropic.com as of 2026-05-15). Cost is correct without
separate capture: `output_tokens × output_rate` already prices
thinking. If Anthropic later splits the field (operator-confirmed
via API release notes), each adapter's `rawUsage` struct gains the
new field and the existing TokenEvent emission path lights up.


## [1.4.52] — 2026-05-14

### Added — Codex 0.130+ turn_context schema capture

User-driven audit of live session `019e22b1-84a6-7ea3-ad2c-3672aaf9268f`
(Codex Desktop 0.130.0-alpha.5 running on Windows, observer in WSL2,
post-v1.4.51 dispatch fix) verified ingestion was clean but surfaced
several new `turn_context` payload fields that observer was not
capturing. v1.4.52 wires four sticky-metadata fields plus a latency
metric onto `actions.metadata`:

| Field source | New `ActionMetadata` field | Type | Semantics |
|---|---|---|---|
| `turn_context.payload.collaboration_mode.mode` | `CollaborationMode` | string | `"default"` vs `"plan"` — Plan mode is read-only-thinking, materially different signal from edit-capable sessions |
| `turn_context.payload.personality` | `Personality` | string | Codex Desktop persona (e.g. `"friendly"`) — informs the base-instructions tone |
| `turn_context.payload.realtime_active` | `RealtimeActive` | bool | True while real-time/voice surface is active (rare today, future signal) |
| `turn_context.payload.truncation_policy.{mode,limit}` | `TruncationMode` + `TruncationLimit` | string + int | Per-turn truncation strategy + token budget — forensics for "why was this turn cut short?" |
| `event_msg.task_complete.time_to_first_token_ms` | `TimeToFirstTokenMS` | int | Gap between `task_started` and first streamed assistant token — separates model warmup + queue latency from total `duration_ms` |

Capture pattern matches the existing `EffortLevel` mechanism: per-turn
sticky values lifted onto `ctxState` from each `turn_context` line,
then stamped onto every action emitted in that turn via the
`withEffort` helper (now stamps the full metadata struct, not just
effort). All fields are `omitempty` — `ActionMetadata.IsZero` returns
true when every field is zero, so the store layer writes NULL instead
of `{}`, keeping the column dense.

**Pinned by:**

- `TestParseSessionFile_Codex0_130TurnContextMetadata` — full set
  appears on actions when present in turn_context.
- `TestParseSessionFile_Codex0_130StickyMetadataAcrossTurns` — turn_context
  with most fields omitted does NOT wipe sticky values from the prior
  turn (matches the `EffortLevel` sticky-then-overwrite contract).
- `TestParseSessionFile_Codex0_130TimeToFirstToken` — `task_complete`
  carries the new latency on `Metadata.TimeToFirstTokenMS`; existing
  `DurationMs` column unchanged.

### Deferred — `speed` toggle NOT in the rollout (Codex-side gap)

User asked whether Codex Desktop's new `speed` toggle (`standard` |
`fast`) could be captured. Empirically verified by flipping the
toggle mid-session on `019e22b1-…` and re-grepping the post-flip
turn (rollout grew from 44 → 53 lines): **no `speed`,
`priority`, `tier`, `latency`, or equivalent field appears anywhere
in the resulting JSONL** — not in `turn_context`, `session_meta`,
`task_started`, `task_complete`, `token_count.rate_limits`,
`agent_message`, or `event_msg`. The post-flip `turn_context` payload
has the exact same key set as pre-flip (only `turn_id` differs).
Conclusion: Codex 0.130.0-alpha.5 does not persist the speed setting
into the on-disk rollout. The toggle is presumably UI-side, resolving
to a server-side routing/queue-priority knob that never gets baked
into the client artifact. Tracked as deferred until Codex starts
emitting it; revisit when newer CLIs ship.

### Further-deferred surface (visible in 0.130+ but not yet captured)

Other new fields observed in `019e22b1-…` that are good capture
candidates for follow-on work but not in this release:

- `event_msg.token_count.rate_limits.{primary,secondary,credits,plan_type}`
  — primary/secondary rate-limit windows with `used_percent` +
  `resets_at`, plus credit balance and plan type. High-signal for
  quota-tracking; would naturally live on `token_usage.metadata` but
  needs schema work + dashboard surface.
- `event_msg.task_started.{started_at,model_context_window,collaboration_mode_kind}`
  — duplicates info already on other rows; `collaboration_mode_kind`
  cross-confirms our `turn_context.collaboration_mode.mode` capture.
- `event_msg.user_message.{images,local_images,text_elements}` —
  vision-input support landing in 0.130+. Currently the adapter
  captures only the text `message` field.
- `event_msg.agent_message.memory_citation` — memory references the
  model produced. Schema unverified; needs a session that actually
  uses memory.
- `response_item.reasoning` — reasoning blocks now appear as their
  own response_item type. Already partially captured for cost via
  `reasoning_output_tokens` on token_count; the body itself isn't
  ingested as an `assistant_text`-shaped action.

**Backfill note for existing codex rows:** the actions UPSERT
(`store.go:225-229`, same invariant the v1.4.50 antigravity tool_time
fix hit) only writes `metadata` when the prior value was NULL.
Sessions ingested before v1.4.52 froze with `{"effort_level":"…"}`
metadata and won't pick up the new fields just by re-running
`observer scan --force`. Verified on the maintainer's
`019e22b1-…` session: 18 pre-v1.4.52-rescan rows stayed at
`effort_only`, 3 fresh rows from the post-speed-flip turn got the
full new metadata.

To backfill existing codex rows, NULL the metadata first then rescan:

```bash
sqlite3 ~/.observer/observer.db \
  "UPDATE actions SET metadata = NULL WHERE tool = 'codex';"
observer scan --force --adapter codex
```

Sessions ingested after v1.4.52 install pick up rich metadata
automatically on first scan — no backfill needed.

**Files touched (v1.4.52):**

- `internal/models/models.go::ActionMetadata` — 5 new omitempty fields
- `internal/adapter/codex/adapter.go` — `sessionContext` + `turnContextPayload` + `taskComplete` struct extensions, `applyContext` sticky merge for new fields, `withEffort` extended to stamp full metadata, `buildTaskCompleteEvent` carries `TimeToFirstTokenMS`
- `internal/adapter/codex/adapter_test.go` — 3 new tests
- `CHANGELOG.md` + `PROGRESS.md`

## [1.4.51] — 2026-05-13

### Fixed — Watcher adapter misrouting silently strands Codex token rows under WSL2

User-reported and externally diagnosed: live Codex session
`019e222a-097b-7642-83a9-fb24c23419b3` ingested its leading
`session_meta` + early hook rows but the assistant turn's
`token_count` / `response_item` rows never landed in `token_usage` or
`api_turns`. The watcher log surfaced the smoking gun:

```
watcher.poll: caught up dropped writes adapter=claude-code
  path=C:\Users\marmu\.codex\sessions\...\rollout-...jsonl
```

— a Codex rollout file dispatched to **claude-code** by the poll
fallback.

**Root cause (architectural):** the watcher's poll fallback at
`internal/watcher/watcher.go::adapterFor` iterated
`registry.Detected(allow)` (alphabetical by `Adapter.Name()`) and
returned the first adapter whose `IsSessionFile` accepted the path.
Combined with three "broad" predicates that matched any `.jsonl`
regardless of location (claude-code, openclaw, pi), Codex's
`rollout-*.jsonl` matched **claude-code first** by sort order. The
claude-code parser walked every JSON line cleanly (Codex JSONL
unmarshals as valid JSON) so the cursor advanced to EOF, but none of
the Codex schema fields (`response_item`, `event_msg/token_count`,
`turn_context`) matched claude-code's record types, so **zero events
were emitted**. Subsequent polls saw `file_size == byte_offset` and
did nothing. Token + turn rows stranded until `observer scan --force`
re-walked from offset 0.

**Failure footprint:** any cursor row whose path matched a broad
predicate could misroute under the poll fallback. JSONL-only matches:
claude-code, openclaw, pi — all three would steal each other's
.jsonl files and any other adapter's .jsonl-shaped files. Codex was
the most exposed because its sessions live under WSL2/NTFS where
fsnotify drops are routine, so the poller fires constantly. Other
adapters share the same architectural risk on lossy filesystems.

**Fix — two layers, both load-bearing:**

1. **Dispatch layer (`internal/watcher/watcher.go::adapterFor`):**
   rewritten to use longest-watched-root prefix — the same
   `adapterForPath` rule fsnotify event dispatch has always used.
   Registry is still queried per call so dynamically-added adapters
   are picked up next tick. A defensive `IsSessionFile` sanity check
   was added to `pollCursors`: after root-prefix dispatch picks an
   adapter, the adapter's own predicate must also accept the file
   before processFile fires — catches the future case where a stray
   file is placed inside a watch root that the adapter shouldn't
   ingest.
2. **Predicate layer (every adapter's `IsSessionFile`):** all ten
   adapters (antigravity, claude-code, cline, codex, copilot, cursor,
   gemini-cli, opencode, openclaw, pi) now AND their shape filter
   with `adapter.UnderAnyWatchRoot(a.WatchPaths())`. Predicates
   self-limit to paths the adapter could actually own. Two new
   invariant tests in `internal/adapter/defaults/` enforce this:
   `TestAllAdapters_IsSessionFile_RequiresUnderWatchRoots` (rejects
   shape-correct paths outside watch roots) and
   `TestRegistryRootsNonOverlapping` (asserts no two adapters' watch
   roots prefix each other).

Either layer alone would fix the bug; both together make the
bug-class regression-proof. The regression test
`internal/watcher/multiadapter_test.go::TestPollerDispatchesByRoot_NotByShape`
registers both claude-code + codex adapters into one registry, drops
a Codex rollout file under the Codex root, and asserts the poller
dispatches to Codex. Fails on every pre-fix version.

**Operator-facing additions:**

- New CLI flag `observer scan --force --adapter <name>` scopes a
  recovery scan to one adapter without editing config. Recommended
  one-liner for users who installed any pre-v1.4.51 build:
  `observer scan --force --adapter codex` re-walks Codex sessions
  from offset 0, recovering any token rows the pre-fix poller routed
  to the wrong adapter.
- `/api/health/watcher` now surfaces a `suspected_misrouted` signal
  per `parse_cursors` row: cursor at EOF on a non-trivial file
  (>1 KB) AND zero rows in `actions` for that source_file. That's the
  pre-fix fingerprint claude-code produced when it silently "parsed"
  Codex JSONL — surface so operators can run the recovery one-liner
  before the recovery flow is forgotten.

**Future-proofing deferred (Option B in the audit):** persisting an
`adapter` column on `parse_cursors` so dispatch never depends on
WatchPaths at lookup time was considered. Deferred because today no
two adapters share an overlapping watch root — the
`TestRegistryRootsNonOverlapping` invariant guards future regressions.
If that invariant ever fails (e.g. a future adapter intentionally
shares a tree with another), the upgrade path is Option B: add the
column, write it from `processFile`, dispatch via
`registry.Get(c.Adapter)`. See `docs/dispatch-contract.md`.

**Affected paths:**

- `internal/adapter/match.go` (new) — `HasPathPrefix` + `UnderAnyWatchRoot` helpers
- `internal/adapter/defaults/defaults.go` (new) — production adapter set factored out of `cmd/observer/main.go`
- `internal/adapter/defaults/defaults_test.go` (new) — invariant tests
- `internal/adapter/{antigravity,claudecode,cline,codex,copilot,cursor,gemini,opencode,openclaw,pi}/adapter.go` (or `scan.go`) — IsSessionFile predicates tightened
- `internal/watcher/watcher.go::adapterFor` + `pollCursors` — root-prefix dispatch + defensive predicate check
- `internal/watcher/multiadapter_test.go` (new) — regression test
- `cmd/observer/main.go::newScanCmd` + `buildWatcherWithOverride` — `--adapter` flag
- `internal/intelligence/dashboard/health.go` — `suspected_misrouted` heuristic
- `docs/dispatch-contract.md` (new) — architectural rule and Option B upgrade path

## [1.4.50] — 2026-05-13

### Fixed — Antigravity tool_time inflation (32h on a single turn)

User-reported: Messages view on session `162c4ab9-…` showed a single
turn with `Tool time = 32h 18m` despite the session being only ~50s
long. Root cause: Antigravity's protobuf records snapshots of all
currently-active terminals on each AI turn. When one of those terminals
is a long-running background process (e.g. `codex` or `claude --resume`
left running across days), the adapter naively computed
`duration_ms = endSec − startSec` from the terminal's process lifetime
and attributed it as the AI's tool-call duration. For the user's
session, a `codex` terminal alive 32h before the session started turned
into `115,776,000 ms` of fake tool time.

Same shape applied to the step-to-next-step duration computation at
`structured.go:401-404`: when there's an idle gap between turns
(or when the parser sees stale pre-session steps), the gap inflated
every step's reported duration.

DB audit found **3,746h total** of bogus tool time across 157
antigravity rows >1h, plus an additional **9.8h** in pre-session rows
under the 1h threshold (157 + 1,917 = **2,074 rows** affected
overall).

Two-part fix:

- **Adapter** (`internal/adapter/antigravity/structured.go`): both
  duration computation sites now cap at 1h. Additionally, terminal
  durations are only emitted when `startSec >= session_start` (the
  terminal was spawned during the session, not before it).
- **One-shot SQL backfill** zeroes `duration_ms` on every antigravity
  action row that is either (a) `> 1h` or (b) timestamped before its
  parent session's `started_at`. Required because the action UPSERT
  path (`store.go:225-229`) only overrides `duration_ms` when the
  prior value was zero — re-scans alone wouldn't clean up the
  historical pollution.

Live verify: user's session 162c4ab9 now shows `tool_time = 11.0s` on
the previously-32h turn; all 7 messages sum to ~50s total, matching
the actual session wall-clock.

### Fixed — Antigravity: reasoning capture + final-summary capture restored across all models

User-reported gap: Antigravity IDE chat UI renders per-step reasoning
blocks ("**Prioritizing Tool Specificity**\n\nI'm focusing now on…")
between tool calls, but observer's DB captured none of them. The
`structured.plan_step` (1.2.93.x) and `structured.final_summary`
(1.2.94.x) emission cases in `internal/adapter/antigravity/structured.go`
were derived against a single Claude-Sonnet fixture (`FB48`) and fire
**zero hits on every current Antigravity session** — verified
empirically against four user sessions on 2026-05-12:

| Session | Model | reasoning rows (was) | reasoning rows (now) |
|---|---|---|---|
| `da973a42…` | gemini-pro-agent (Pro, high effort) | 0 | **5** |
| `fc8d44e5…` | claude-sonnet-4-6 | 0 | **1** |
| `162c4ab9…` | gemini-3.1-pro-low | 0 | **5** |
| `ae703782…` | gemini-3-flash-agent | 0 | **4** |

All four now also produce 1 `structured.final_summary` row each.

**Root cause:** Antigravity's protobuf schema moved between the
fixture and now. Reasoning bodies now live at **`1.2.20.3`** (sibling
of `1.2.20.1` assistant text on the same `PLANNER_RESPONSE` step) and
the final-summary envelope moved to **`1.2.30.{4,5,15}`** (title /
body / URI). The old paths return zero across 2.5MB of plaintext
across all four sessions.

**Discovery method:** new `path_inventory_test.go` walks every
protobuf wire path the language-server's `GetCascadeTrajectory`
endpoint returns and produces a `(path, wire_type, count, sample)`
catalog. Diffing across the four sessions identified the new path
layout. The probe is permanently in-tree but gated on
`OBSERVER_AG_PATH_INVENTORY=1` so it doesn't fire in CI.

**Code change:**

- `internal/adapter/antigravity/structured.go`: added `case pathEq(f.Path, 1, 2, 20, 3)`,
  `1, 2, 30, {4,5,15}` clauses; new `reasoningText` and
  `finalSummaryTitle` fields on `stepData`; new emission for
  `structured.reasoning` ToolEvent + revised `structured.final_summary`
  to prefer the explicit `1.2.30.4` title when present.
- Legacy `1.2.93.x` / `1.2.94.x` cases retained as fallback for any
  pre-rewrite session files that might still exist; commented as
  DEPRECATED.

**Not addressed in this change** (deferred):

- `reasoning_tokens` is still 0 across all sessions. The
  `digestTokenRow` (`classify.go:373`) read at `1.3[].17.2 sorted[3]`
  doesn't fire; likely the field renumbered or the gRPC trajectory
  strips it. Separate investigation.

### Fixed — Antigravity-internal model SKU pricing (3 bugs)

Antigravity encodes the model + effort selector into a single
identifier string. Two naming conventions co-exist:

- `<family>-agent` for default/high effort
- `<version>-<family>-<effort>` for explicit effort selection

Verified empirically across four sessions on 2026-05-12:

| Antigravity selection | Stored model string |
|---|---|
| Gemini 3.1 Pro — high | `gemini-pro-agent` |
| Gemini 3.1 Pro — low | `gemini-3.1-pro-low` |
| Gemini 3 Flash | `gemini-3-flash-agent` |
| Claude Sonnet 4 | `claude-sonnet-4-6` (clean) |

Pre-2026-05-13 pricing-table coverage:

| Model in DB | Lookup result | Effective behavior |
|---|---|---|
| `gemini-pro-agent` | `PricingSourceMiss` → $0 | ❌ silent zero |
| `gemini-3.1-pro-low` | `gemini-3.1` family → Pro rates | ✅ correct by accident |
| `gemini-3-flash-agent` | `gemini-3` family → **Pro rates** | ❌ ~4× overcharge (Pro $2/$12 vs Flash $0.50/$3) |
| `claude-sonnet-4-6` | `PricingSourceExact` | ✅ correct |

Added 8 explicit entries pinning each variant to `PricingSourceExact`:
`gemini-pro-agent`, `gemini-3.1-pro-{low,medium,high}` → Gemini 3.1 Pro
rates incl. 200K LC tier; `gemini-3-flash-{agent,low,medium,high}` →
Gemini 3 Flash rates (no LC tier). Pinned by
`TestTable_AntigravityInternalSKUs` so future table churn can't
silently re-break the alignment.

### Changed — Sessions table: per-bucket tokens + elapsed time

The Sessions tab's single "Tokens" column is replaced with the four
billable buckets shown separately — **Input · Cache R · Cache W ·
Output** — plus a new **Elapsed** column. The single-number column
collapsed the cache-reuse story; the split lets users see at a glance
which sessions are cache-anchored (Cache R ≫ Input) vs cache-burning
(Cache W ≫ Cache R) without opening the detail modal.

**Backend (`/api/sessions`):**

- New fields per row: `input_tokens`, `output_tokens`,
  `cache_read_tokens`, `cache_creation_tokens` (sourced from the cost
  engine's per-session rollup — same dedup as `/api/cost`).
- New fields: `last_seen_at` = `COALESCE(s.ended_at, MAX(actions.timestamp))`
  + `duration_seconds` = `last_seen_at − started_at`. Open sessions
  (no `ended_at`) still show in-flight elapsed time using the latest
  action.
- `total_tokens` is kept in the JSON for backwards compatibility with
  CLI / external callers; the Sessions table no longer renders it.

**Frontend:**

- Columns reordered: `ID · Tool · Project · Started · Elapsed ·
  Actions · Sub-agent · Input · Cache R · Cache W · Output · Cost`
  (+ Quality / Errors / Redundancy when `observer score` has run).
- New `fmtDuration(secs)` helper renders the two largest non-zero
  units ("19h 7m", "1d 4h", "35m 12s"). Zero / unknown renders as the
  muted em-dash to stay visually quiet.
- Each per-bucket cell falls back to the muted em-dash when that
  bucket is zero (e.g. Codex sessions emit no cache_creation tokens).

**Tests:** `TestAPISessions_AttachesTokensAndCost` extended to assert
the per-bucket fields and the zero-fallback for unused buckets. New
`TestAPISessions_DurationSecondsFromActions` pins the open-session
elapsed math (no `ended_at`, falls back to MAX(actions.timestamp)).

**Help-text:** 5 new column entries in `help.js` (Elapsed + the 4
token buckets); the old `col.sessions.tokens` entry marked deprecated
but retained for any callers still wiring `data-help="col.sessions.tokens"`.

### Changed — Analysis tab metric overhaul (audit-driven)

User-reported audit found that the v1.4.49-and-earlier "Effective rate"
tile was structurally misleading: dividing period cost by
(output + cache_write) treated paid-for setup overhead (cache writes)
as if they were value tokens, deflating the displayed rate when cache
churn was high. On the maintainer's live install the displayed rate was
$36.75/M against $211.55/M output-only — same data, very different
signal. A second finding: the "LC tier surcharge" tile silently
reported $0 for users on Anthropic Opus (which has no LC pricing tier),
hiding 18k+ heavy-context turns. A third: LC surcharge attribution
silently dropped to zero for any row with `recorded > 0` (proxy-priced
cost), with no user-facing disclosure.

This release retires the misleading tiles and adds cost-sensitive
replacements.

**Tiles retired:**

- `effective.rate_per_million` (output + cache_write denominator)
- `long_context.turns / surcharge_usd` (no high-context signal when
  models lack LC tier)

**Tiles added (all in `/api/analysis/headline`):**

- **`$/M output`** (`output_rate.rate_per_million`) — clean per-token-
  of-value rate. Period cost ÷ output tokens × 1M. Unambiguous; matches
  industry convention.
- **`Cache savings $`** (`cache_savings.usd`) — counterfactual: per-
  turn `Compute(p, {Input += CacheRead, CacheRead = 0}) − Compute(p, b)`,
  summed. The dollar value of cache_read traffic vs uncached input
  pricing. Pairs with the existing efficacy % tile.
- **`High-context turns`** (`high_context.turns_over_100k / 200k /
  cost_over_*_usd / lc_eligible_turns / lc_surcharge_usd`) — model-
  agnostic turn counts at 100K and 200K prompt-window thresholds + the
  $ attributable to those turns. LC surcharge becomes a sub-line that
  fires only when LC-eligible models (Sonnet 4 / 4.5 / Gemini Pro /
  GPT-5.4/5.5) actually crossed their threshold.
- **`$ per turn (p95)`** (`per_turn.count / mean_usd / p95_usd`) —
  variance signal across all costed turns. p95 is the headline; mean
  is the sub-line.
- **`Burn rate $/hr`** (`burn_rate.active_hours / cost_per_hour_usd`)
  — period cost ÷ distinct (date, hour) buckets with traffic. The
  user's hourly AI bill while engaged.
- **`Top model`** (`top_model.key / cost_usd / concentration_pct`) —
  single model with the most $ + concentration as a % of period total.
  One-glance attribution.

**Tile changed:**

- **`Month-to-date`** gains `prior_month_same_day_usd` +
  `vs_prior_month_pct` as the primary sub-line. Linear projection
  drops to a secondary reference. Prior-month-same-day is a more honest
  comparison than extrapolation: on day 12 of May, it compares MTD
  against April 1–12, not against an assumption that today's daily
  rate continues.
- **`Period`** gains `recorded_cost_share_pct` disclosure (B3 fix) —
  the share of period cost that came from upstream-recorded values.
  When >5%, the dashboard renders a small note below the tile grid
  explaining that LC surcharge / cache savings may under-report on
  those rows. Quiet when share is low.

**New endpoints:**

- **`/api/analysis/cost-by-hour?days=N`** — 24 fixed buckets (0..23
  UTC) with cost + turn count per hour-of-day. Powers the "When you
  spend" bar chart in the new Analysis-tab panel.
- **`/api/analysis/cache-savings-trend?days=N`** — daily cache savings
  $ across the window. Same counterfactual as the headline tile but
  per-day. Recorded-cost rows are excluded since they can't be
  decomposed. Powers the "Cache savings trend" line chart.

**Frontend:**

- Analysis tab card grid expanded from 6 → 10 tiles.
- Two new chart panels added under the daily-trend chart: cost-by-hour
  (bar) + cache-savings-trend (line).
- Full set of `help.js` registry entries added for every new + revised
  tile and chart — pre-v1.4.50 the tile help-keys were `data-help`-
  attributed but had no registry entries, so hover popovers were empty.

**Performance:**

- Headline endpoint no longer calls `discover.New().Run()` (which
  walked stale-reads + repeated-commands + cross-tool-files + native-
  vs-bash). New `discover.WastedTokens(ctx, opts)` helper runs only
  the stale-reads pass — the only one the headline needed. Other passes
  weren't expensive but were wasted work on every tab load.

**Audit summary** (full detail in
`docs/handover-v1.4.50-analysis-metrics-2026-05-12.md`):

| Finding | Resolution |
|---|---|
| B1 — "Effective rate" denominator includes cache_write overhead | Tile retired; replaced with $/M output + Cache savings $ |
| B2 — LC surcharge always $0 for Opus-only users despite 18k+ heavy turns | Tile retired; replaced with model-agnostic High-context turns + LC sub-line for LC-eligible models |
| B3 — LC surcharge silently $0 when recorded cost present | Disclosed via `period.recorded_cost_share_pct` |
| B4 — `tile.analysis.*` help-keys had no registry entries | All entries added to help.js |
| B5 — Discovery walked actions table on every headline hit | Lighter `discover.WastedTokens` helper added |

**Tests:** 11 new tests in `analysis_test.go` covering output_rate,
cache_savings, high_context turns + LC surcharge, opus model-agnostic
high-context, per_turn distribution, burn_rate hour buckets, top_model
concentration, prior_month_same_day, recorded_cost_share, plus 4 tests
for the two new endpoints. Pre-existing `TestAnalysisHeadline_LCSurchargeAttribution`
rewritten as `TestAnalysisHeadline_HighContextAndLCSurcharge` —
asserts both the new high_context counts AND that LC surcharge still
fires for LC-eligible models (regression coverage for the half that
survived the refactor).

## [1.4.49] — 2026-05-12

### Fixed — Dashboard backfill subprocess timeout raised to 2h

The dashboard's "Run all" backfill kicker (`/api/backfill/run` with
`mode=all`) wraps the spawned `observer backfill --all` subprocess in
a `context.WithTimeout` that hard-kills the child at the cap. The
original 30-min cap got blown on the maintainer's 67k-action /
51k-action_excerpts DB during v1.4.49 dogfood: `--all` does a full
Rescan from offset 0 + 15 surgical backfills in sequence, which
exceeded 30 min before completing the rescan phase.

Raised to **2h** and extracted into the named constant
`backfillJobTimeout` in `internal/intelligence/dashboard/backfill_run.go`.
Surgical individual modes (`message-id`, `cursor-subagents`, etc.)
finish in single-digit minutes, so the new cap only bites on `--all`
against heavy installs.

The new v1.4.49 assistant-text emission adds ~25s of indexer work for
~4k new rows — negligible by itself, but it nudged the prior just-
under-30m run past the boundary, surfacing the inadequate cap.

Diagnosis-by-elimination: pre-checking
`store.go::InsertActions::InsertActions` line 304-328 confirmed that
the FTS5 indexer only fires on NEWLY-inserted rows (UPSERT no-op rows
leave `a.ID == 0`, and `indexOutputs` skips ID==0 rows). So the per-
row indexer cost is bounded by NEW rows per backfill, not by re-
processed rows. The real wall-clock dominator on `--all` is the
rescan's per-file UPSERT loop across years of session JSONL.

### Added — Cross-adapter assistant-text capture batch (8 adapters)

The "what did the AI say to the user?" question now has a first-class
answer across every adapter that has the data. Before this batch only
Antigravity (the precedent) and Pi emitted standalone assistant-text
rows; the rest captured assistant text only as PrecedingReasoning on
sibling tool events. This batch wires per-emission assistant-text rows
in seven additional places + adds a dashboard filter to surface them.

**Convention decisions (binding for future adapters):**

- **`ActionType = ActionTaskComplete`** for all new assistant-text rows.
  Mirrors the Antigravity precedent and Pi's existing pattern. A single
  turn can legitimately produce multiple ActionTaskComplete rows (one
  per assistant message); the RawToolName discriminates intent.
- **`RawToolName = "<source>.assistant_text"`** for new wirings.
  Legacy precedents (Pi's `message.assistant.<stopReason>`, Copilot's
  `agent_response`, Antigravity's `structured.assistant_text`,
  OpenClaw's `message.assistant.stop`) are **kept as-is** to avoid
  SourceEventID dedup churn on existing rows. The dashboard surface
  uses a multi-pattern OR-chain.
- **No token/cost fields on assistant-text ToolEvents.** Token usage
  flows through the dedicated `TokenEvent` path. Tests pin this
  invariant for every wiring (the `ToolEvent` struct doesn't expose
  token fields, so the type system enforces it).
- **Per-emission granularity.** Codex emits one row per
  `event_msg.agent_message` line. Cline/Roo emit one per text content
  block on a role=assistant message. Claudecode JSONL same. Cursor
  same (from the transcript-walking path). Gemini same. OpenCode emits
  one per text part in the `part` table where parent is
  role=assistant. OpenClaw emits one per `type:"text"` content
  alongside the existing `message.assistant.stop` marker.
- **MessageID strategy:** prefer the native upstream id (msg_xxx,
  turn_id, generation_id, message-row-id) where available; fall back
  to a content hash so re-parses dedupe via the (source_file,
  source_event_id) UPSERT path.

**Adapter wiring (with file:line):**

- **Codex JSONL** — `internal/adapter/codex/adapter.go::buildAgentMessageEvent`
  + new emission inside `case "agent_message"` (adapter.go:823-840).
  Wrapped in `withEffort` so per-turn `effort_level` rides the row.
  4 new tests covering field shape, no-TokenEvent, L-num stability,
  multi-message turn IDs.
- **Cline / Roo Code** — `internal/adapter/cline/adapter.go::assistantTextEvent`
  + new `case "text"` in the content-block switch. `RawToolName`
  uses `toolID + ".assistant_text"` so cline → `cline.assistant_text`
  and roo-code → `roo-code.assistant_text`. 2 new tests + existing
  fixture-driven test bumped 3→4.
- **Claude Code JSONL** — `internal/adapter/claudecode/adapter.go::assistantTextEvent`
  + emission in the existing `case "text"` for role=assistant. Synthetic-
  model (`<synthetic>`) compaction placeholder rows correctly continue
  to be filtered out by the existing pre-emit gate at adapter.go:367.
  1 new test + 3 existing fixture tests rebalanced to account for
  the new emissions.
- **Claude Code Stop hook** — pre-v1.4.49 the dispatch fell through
  to the generic approve-reply path so the assistant's final-message
  text was lost. Added `case "stop"` to `cmd/observer/hook.go::run`
  + new `buildClaudeStopEvent` (cmd/observer/hook.go) mirroring
  SubagentStop's pattern. Empty-message suppression returns
  (zero, false). 3 new tests + existing `TestBuildClaudeAllRejectMissingSessionID`
  extended.
- **OpenCode** — new `loadAssistantTextEvents` + `assistantTextEvent`
  in `internal/adapter/opencode/adapter.go` query the `part` table
  for type=text parts whose parent `message` is role=assistant.
  Complements the existing `assistant.stop` lifecycle marker.
  1 new test seeding an opencode SQLite DB with multiple assistant
  text parts.
- **Gemini CLI** — new emission inside `internal/adapter/gemini/parser.go::emitMessage`
  + `assistantTextEvent` builder. Both the legacy JSON format and the
  JSONL spec proposed in issue #15292 benefit since both share the
  `emitMessage` orchestrator. 1 new test.
- **Cursor (transcript walker)** — new emission inside
  `internal/adapter/cursor/adapter.go::buildTranscriptToolEvents`
  covering both the live Stop hook's transcript replay AND the
  historical `backfillCursorSubagents` path (sidechain rows inherit
  `IsSidechain=true` via the existing wrapping at
  cmd/observer/backfill.go:3592). 5 existing tests rebalanced for
  the new emissions and content-block ordering.
- **OpenClaw** — per-text-part emission inside
  `internal/adapter/openclaw/adapter.go::parseMessageLine`'s
  `case "assistant":` block, not gated on `stopReason`. The existing
  `message.assistant.stop` row stays as the lifecycle marker. 2
  existing tests rebalanced; new emission covers mid-turn text parts
  on tool_use-stopping turns that previously produced no observability
  row.

### Added — Dashboard "AI messages only" filter

`/api/actions` accepts `assistant_text=1` to filter on a multi-pattern
RawToolName OR-chain covering the 8 new `<source>.assistant_text`
conventions + the 4 legacy precedents (`structured.assistant_text`,
`message.assistant.*`, `agent_response`). The Actions tab toolbar
gains an "AI messages only" checkbox wired to this filter. Backend +
frontend test coverage in `dashboard_test.go::TestAPIActions_AssistantTextFilter`
(8 seeded rows × 4 filter combos).

### Audit-subagent verdicts revised

Three Tier 3 tasks the initial audit flagged as "blocked on upstream
schema" or "scope unclear" turned out to be unblocked under live code
inspection. Each shipped code, not doc-only:

- **Gemini** — subagent claimed adapter was "skeletal, no message
  parsing." Verified false: `emitMessage` handles user / gemini /
  model / assistant / tool roles end-to-end. Wired per-text-part
  emission.
- **Cursor** — subagent claimed "no hook event carries the model's
  final response." Verified false: `BuildStopTranscriptEvents`
  already walks the transcript JSONL on the `stop` hook (crossmount-
  translated WSL paths). Wired per-text-part emission in the
  transcript walker.
- **OpenClaw** — subagent claimed "schema doesn't store assistant
  responses." Verified false: openclaw's JSONL has assistant-role
  messages with text/thinking/toolCall content. The existing
  `message.assistant.stop` emission only fired on `stopReason=stop`;
  added per-text-part emission for the other stop reasons.

Lesson reaffirmed (invariant 36-ish): "Trust empirical capture >
local docs > inherited claims." Subagent code reads excerpts; full
file reads catch what the excerpts missed.

### Fixed — Codex JSONL SourceEventID stable across re-parses (L-num bug)

Surfaced by the 2026-05-11 backfill: `observer scan --force`
created 17 duplicate codex rows in the maintainer's DB instead of
upserting via the (source_file, source_event_id) UNIQUE key.
Affected `user_prompt`, `task_complete`, `system_prompt`, and any
event using the `:L<lineNum>:` SourceEventID format (15 builders
in the codex JSONL adapter).

**Root cause:** `ParseSessionFile` initialized `lineNum := 0` at the
start of every invocation. On incremental watcher resume
(`fromOffset > 0`), the scanner read from the seek position and
counted from 1 starting at the RESUME line — making lineNum
*chunk-relative* on resume but *absolute* on rescan. Same file →
different lineNum → different SourceEventID → ON CONFLICT didn't
match → INSERT created a duplicate.

Concrete observation: a "what files are in /tmp?" prompt at
file-line 7 was tagged `L2` by the watcher (chunk-relative, the
prompt was 2nd line of the resumed chunk) and `L7` by the rescan
(absolute). Two rows for one prompt.

**Fix:** `prefetchSessionContext` already scanned every line from
offset 0 to fromOffset to recover SessionID/Cwd/Model/EffortLevel.
It now ALSO counts those lines and returns the count. `ParseSessionFile`
seeds `lineNum` from that count, so resumed parses continue from
the same absolute line number a full-file rescan would compute.

The signature of `prefetchSessionContext` changed from
`(sessionContext, bool)` to `(sessionContext, int, bool)` (added
lineNum count). Single caller updated.

Pinned by new `TestParseSessionFile_SourceEventIDStableAcrossResume`
which writes a header section + a user_prompt at file-line 6, parses
once from offset 0 and once from offset-past-header, and asserts
the SourceEventID matches.

**Affected event types** (15 SourceEventID builders that embed
`:L<lineNum>:`): `user_prompt`, `task_complete`, `system_prompt`,
`compacted`, `mcp`, `error`, `web`, `view_image`, `patch`, plus
the call_id-fallback paths in exec_command_end, function_call_end,
function_call_output, etc. All now produce stable IDs across
re-parses.

### Fixed — Codex JSONL effort_level survives watcher cycles

The v1.4.47 sticky-effort invariant (#33) held within a single
`ParseSessionFile` invocation but silently broke whenever the
watcher's incremental ingest split a session across multiple parse
cycles. Surfaced on 2026-05-11 when 3 codex sessions on the
maintainer's host showed `reasoning_effort: "medium"` in the
JSONL but observer captured `effort_level=NULL` on every emitted
ToolEvent.

**Two-part root cause:**
1. `mergeSessionContext` copied SessionID/TurnID/Model/Cwd/GitBranch
   but NOT EffortLevel. Even when `prefetchSessionContext` correctly
   read the leading `turn_context` line on resume, EffortLevel was
   silently dropped during merge.
2. `prefetchSessionContext` unmarshaled `turnContextPayload` and
   passed `meta.sessionContext` to mergeSessionContext directly —
   but EffortLevel is `json:"-"` so it's NEVER populated by
   unmarshal. The live-parse path lifts effort via
   `meta.EffortFromPayload()` before applyContext; the prefetch
   path didn't mirror this.

Either bug in isolation produced the same symptom; both needed
fixing.

**Fix:** `mergeSessionContext` now copies EffortLevel following the
same sticky rule as applyContext (non-empty overwrite).
`prefetchSessionContext` now calls `EffortFromPayload()` to lift
effort from the nested envelope before merge. Pinned by new
`TestParseSessionFile_EffortLevelSurvivesWatcherCycle` which
exercises fromOffset > 0 past a `reasoning_effort: "medium"`
turn_context AND verifies the live-parse path as a control.

**Field audit:** every other context field maintained by
`applyContext` (SessionID/TurnID/Model/Cwd/GitBranch) was already
correctly preserved. EffortLevel was the only gap. Other per-parse
state (pending tool_use map, turnModels, seenSystemPrompts,
seenModernTotal) doesn't survive cycles either, but those losses
are self-healing at the store layer (system prompts dedup via
content-hash source_event_id) or theoretical / never reported.

**Revises v1.4.47 invariant 33:** sticky effort propagation now
survives watcher cycles via prefetch, not just a single
ParseSessionFile call.

**Backfill:** `observer scan --force` populates effort_level on
the historical empty-effort rows via the v1.4.47 ON CONFLICT
UPDATE metadata-backfill rule (invariant 30 — only updates when
existing is NULL).

### Refactored — Codex hook envelope cleanup

Completes the v1.4.48 task #2 capture-first procedure with the
2026-05-11 maintainer dogfood findings.

- **`Effort.Level` and `CollaborationMode` struct fields removed**
  from `rawHookPayload`. Both were speculative defensive paths
  introduced in v1.4.47; live capture of codex 0.129.0
  SessionStart + UserPromptSubmit fires confirmed neither field
  is ever in codex hook payloads. Effort capture for codex
  remains JSONL-only via the adapter's `withEffort` wrapper.
- **`hookEnvelopeMetadata` simplified** to just `PermissionMode`
  (the one envelope field codex DOES carry — undocumented, but
  empirically present).
- **`ensureCodexHooksFeatureFlag` doc-comment corrected.** The
  feature-flag name `[features].hooks = true` IS canonical (codex
  0.129.0 prints a deprecation warning on the longer
  `codex_hooks = true`). The local docs file at
  `tmp/codex-hooks.md` is stale on this point.
- **`docs/codex-hook-capture.md` rewritten** as a verified-facts
  reference (was a speculative procedure). Documents the real
  envelope shape, flag-name history, and the codex `unified_exec`
  coverage gap (tool-context hooks only intercept simple Bash,
  apply_patch edits, and MCP tools — not modern streaming shell
  calls).
- **`hook_test.go::TestBuildHookEvent_PopulatesMetadata`**
  rewritten (6 → 5 cases): dropped the both-shapes and
  collaboration_mode-shape cases (testing removed code paths),
  added a stray_effort_level_ignored case pinning that observer
  no longer extracts effort from the hook even when payload
  contains it.

### Added — Claude Code Worktree hooks (carried-over menu #4)

Wires the Claude Code `WorktreeCreate` and `WorktreeRemove` hooks
that fire on Agent spawns with `isolation: "worktree"`. The v1.4.47
handover labeled this "Cursor Tier 4" but the actual hook surface
is Claude Code's (Cursor doesn't have a WorktreeCreate event). The
two hooks have asymmetric risk profiles, so the wiring is asymmetric:

| Hook | Blocking? | Default-registered? | Why |
|---|---|---|---|
| `WorktreeRemove` | No (logging-only per docs matrix) | Yes | Zero behavioral risk — incorrect handler can't break spawns |
| `WorktreeCreate` | Yes (any non-zero exit fails spawn) | **No** | Path-echo contract unverified on live bytes; opt-in only |

- **New ActionType constants** in `internal/models/models.go`:
  `ActionWorktreeCreate = "worktree_create"`,
  `ActionWorktreeRemove = "worktree_remove"`.
- **WorktreeRemove**: added to `claudeCodeEvents` (now 22 events).
  Standard `buildClaudeWorktreeRemoveEvent` follows the existing
  Tier 1 pattern. Target carries `worktree_path`. SourceEventID:
  `{session_id}:worktree_remove:{worktree_path}`.
- **WorktreeCreate**: NOT in `claudeCodeEvents`. The dispatch case
  in `handleClaudeCodeHook` and the handler
  `handleClaudeCodeWorktreeCreate` exist, but the registration
  helper won't write the entry to `~/.claude/settings.json` on
  `observer init --claude`. Users opt in per
  `docs/claude-worktree-hook.md`. The handler:
  - Reads stdin, parses payload
  - Computes worktreePath: `$OBSERVER_CLAUDE_WORKTREE_ROOT/<name>`
    if set, else `~/.claude/worktrees/<name>` (docs default), else
    `/tmp/.claude/worktrees/<name>` (last-ditch when `os.UserHomeDir`
    fails)
  - Synthesizes a `worktree-<base36-nanos>` name when payload.name
    is empty
  - Writes `{"hookSpecificOutput":{"worktreePath":"<path>"}}` on
    stdout FIRST (so Claude Code never blocks on the DB write)
  - Inserts the action row best-effort (logs to stderr on failure)
- **Hook event arg mappings** in `internal/hook/register.go::hookEventArg`:
  `WorktreeCreate → worktree-create`, `WorktreeRemove → worktree-remove`.

**Tests:** 6 new tests in `cmd/observer/hook_test.go`:
- `TestBuildClaudeWorktreeRemoveEvent` — standard builder coverage.
- `TestBuildClaudeWorktreeCreateReply_DocumentedPayload` — path
  resolution with HOME-stubbed env.
- `TestBuildClaudeWorktreeCreateReply_EmptyNameSynthesizes` — empty
  payload synthesizes a `worktree-<base36>` name.
- `TestBuildClaudeWorktreeCreateReply_EnvRootOverride` —
  `$OBSERVER_CLAUDE_WORKTREE_ROOT` takes priority.
- `TestBuildClaudeWorktreeCreateReply_NoSessionIDStillEchosPath` —
  missing session_id skips DB write but STILL echoes the path
  (preventing spawn failures on malformed payloads).
- `TestHandleClaudeCodeWorktreeCreate_AlwaysReplies` — end-to-end:
  invalid config path → handler still writes a valid reply.

Plus extended `TestBuildClaudeAllRejectMissingSessionID` to cover
`WorktreeRemove`.

**Behavior delta:**
- `observer init --claude` now writes a `WorktreeRemove` entry to
  the user's `~/.claude/settings.json`. Future fires emit
  `action_type='worktree_remove'` rows in the actions table.
- `WorktreeCreate` is undisturbed by default — no regression for
  existing Agent spawns. Users who want to capture it follow the
  opt-in walkthrough at `docs/claude-worktree-hook.md`.

**Schema:** no migrations.

**Honest framing:**
- WorktreeCreate's reply shape (`hookSpecificOutput.worktreePath`)
  is documented but unverified on live bytes. The capture-first
  procedure in `docs/claude-worktree-hook.md` walks through dry-run
  validation before relying on it for real Agent spawns.
- The default path `~/.claude/worktrees/<name>` is a docs best-guess.
  Users on non-standard layouts (shared `/workspace/` mounts,
  alternate filesystem roots) should set
  `$OBSERVER_CLAUDE_WORKTREE_ROOT` before opting in.

### Added — Dashboard metadata filters (carried-over menu #3)

Lands the actionable-analysis surface for the v1.4.47 metadata
badges. The Actions tab already showed effort_level / permission_mode
/ is_interrupt as pills next to each action_type; this PR adds three
filters so users can drill into "show me only high-effort runs" or
"only plan-mode actions" without manual SQL.

| Filter | Query param | Frontend control |
|---|---|---|
| effort_level | `?effort_level=minimal\|low\|medium\|high\|xhigh\|max` | `<select id="effort-filter">` dropdown |
| permission_mode | `?permission_mode=default\|plan\|acceptEdits\|auto\|dontAsk\|bypassPermissions` | `<select id="permission-filter">` dropdown |
| is_interrupt | `?is_interrupt=1` (only `1` triggers the filter) | checkbox `<input id="interrupt-filter">` |

- **Backend** (`internal/intelligence/dashboard/dashboard.go::handleActions`):
  parses the three new query params and adds them to the WHERE
  clause via `json_extract(a.metadata, '$.<field>') = ?` (no
  JSON1 dependency added — `modernc.org/sqlite` ships it). Empty
  / missing params skip the filter entirely so legacy callers'
  results are unchanged. `is_interrupt=1` matches the JSON
  boolean's numeric representation (SQLite returns 1/0 for true/
  false on `json_extract`); rows with NULL metadata correctly
  fail the equality check rather than matching as 0.
- **Frontend** (`internal/intelligence/dashboard/static/index.html`):
  three new controls in the Actions tab toolbar adjacent to the
  existing `action_type` dropdown. Action-type dropdown also
  expanded with the v1.4.48 Tier 2/3 types
  (`user_prompt_expansion`, `tool_failure`, `permission_request`,
  `permission_denied`, `post_tool_batch`, `setup`,
  `instructions_loaded`, `config_change`, `subagent_start`,
  `subagent_stop`, `notification`, `cwd_change`, `api_error`,
  `session_start`, `session_end`) so the filter options match the
  rows the table actually surfaces.
- **State** (3 new fields on the `state` object): `actionsEffortFilter`,
  `actionsPermissionFilter`, `actionsInterruptFilter`. All reset
  the page to 1 on change.

**Tests:** new `TestAPIActions_MetadataFilters` in
`dashboard_test.go` seeds 5 rows covering the filter matrix (plan/
high/interrupt, plan/medium/no-interrupt, default/high, acceptEdits/
xhigh/interrupt, no-metadata) and exercises 11 filter combinations
including combined effort+permission+interrupt and the empty-param
fall-through. NULL metadata correctly fails the equality check.

**Schema:** no migrations. Filters operate on the existing
`actions.metadata` JSON column from migration 017.

**Behavior delta:** before this PR, the Actions tab badges from
v1.4.47 were view-only — users could see that a row had
`effort=xhigh` or `permission=plan` but not select for those rows
without manual SQL. After this PR, three new controls turn the
badges into actionable filters. Mixed-and chains supported (e.g.
`?action_type=run_command&effort_level=high&permission_mode=plan&is_interrupt=1`
finds high-effort plan-mode run-command rows that were interrupted).

### Added — Codex hook capture infrastructure (carried-over menu #2)

Ships the capture-first procedure for settling the
`effort.level` vs `collaboration_mode.settings.reasoning_effort`
envelope-shape question on real Codex hook fires (the open item from
v1.4.47 handover pending #2).

- `scripts/codex-hook-tee.sh` — versioned tee-shim. Captures the
  raw JSON stdin of each codex hook fire to
  `$OBSERVER_CODEX_CAPTURE_DIR/<Event>-<timestamp>.json` (default
  `/tmp/codex-hook-captures/`) then pipes the unchanged bytes
  through to `$OBSERVER_BIN`. Matches the precedent of
  `/tmp/cursor-tee-shim.sh` from the v1.4.45 cursor work.
- `docs/codex-hook-capture.md` — walkthrough doc. Covers the 7-step
  procedure: back up hooks.json → swap commands to the shim →
  re-trust in codex /hooks → run codex for ~30s → inspect captures
  → decide which of the two `effort` shapes to drop → restore the
  original hooks.json. Documents the three possible outcomes and
  the exact `internal/adapter/codex/hook.go` lines to edit for each.
- `internal/adapter/codex/hook.go:39-58` (and counterpart on
  `CollaborationMode`): doc-comment now points at the capture doc
  as the cleanup path, so future readers know HOW to verify, not
  just THAT verification is pending.

**No behavior change.** The defensive first-non-empty parsing
introduced in v1.4.47 stays. Once the user captures real fires and
the doc's Step 6 decision is made, a follow-up PR removes the dead
path and tightens the doc-comments to "verified".

### Added — Tier 2/3 Claude Code hook events (carried-over menu #1)

Wires 7 of the 8 Tier 2/3 events from the v1.4.45 menu / v1.4.47
handover's highest-impact carryover slot. Each event now emits its
own row in the `actions` table via a per-event builder following the
established Tier 1 pattern (`handleClaudeCodeActionEvent` +
`baseToolEvent` + `envelopeMetadata`). FileChanged is deferred — its
matcher takes literal filenames split on `|` (NOT regex / `*`) so it
needs a per-project config surface before registration.

**Verified shapes.** Every field extraction was matched against the
canonical Claude Code hooks reference fetched from
`code.claude.com/docs/en/hooks` (full table at
`tmp/claude-code-hooks-payload-reference.md`). No speculative fields
— each builder's struct unmarshals exactly the documented keys and
nothing else. Unknown payload keys fall through into the universal
envelope's catch-all behavior (no panic, no row drop).

| Event | ActionType | Target | RawToolName | Extracted fields | Success |
|---|---|---|---|---|---|
| `Setup` | `setup` | `trigger` | — | `trigger` | true |
| `UserPromptExpansion` | `user_prompt_expansion` | `command_name` | `expansion_type` | `prompt`, `command_args`, `command_source` (in PrecedingReasoning JSON) | true |
| `PostToolBatch` | `post_tool_batch` | `N tool call(s)` | first call's `tool_name` | `tool_calls[]` JSON in RawToolInput | true |
| `PermissionRequest` | `permission_request` | `tool_name` | `tool_name` | `tool_input` in RawToolInput, `permission_suggestions` in PrecedingReasoning | true |
| `PermissionDenied` | `permission_denied` | `tool_name` | `tool_name` | `tool_input`, `reason` → ErrorMessage; SourceEventID from `tool_use_id` | **false** |
| `InstructionsLoaded` | `instructions_loaded` | `file_path` | `memory_type` | `load_reason` + optional `globs` / `trigger_file_path` / `parent_file_path` (JSON in RawToolInput) | true |
| `ConfigChange` | `config_change` | `file_path` or `source` | `source` | — | true |

**SourceEventID uniqueness.** Three events fire multiple times per
session without a payload-carried unique key (UserPromptExpansion,
PostToolBatch, PermissionRequest). For those, SourceEventID is
`{session_id}:{event}:{fnv32a(body)}` so distinct fires don't collide
under `ON CONFLICT DO UPDATE`. PermissionDenied uses
`{tool_use_id}:permission_denied` (mirrors PostToolUseFailure).
InstructionsLoaded uses `{session_id}:instructions_loaded:{file_path}`
(natural per-session discriminator). ConfigChange uses
`{session_id}:config_change:{source}:{file_path}`. Setup uses
`{session_id}:setup:{trigger}`.

**Coverage.**
- `internal/models/models.go`: 7 new ActionType constants with
  full doc comments explaining field choices.
- `internal/hook/register.go`: 7 new entries in `claudeCodeEvents`
  (now 21 events) + 7 new cases in `hookEventArg` mapping.
- `cmd/observer/hook.go`: 7 new dispatch cases in
  `handleClaudeCodeHook` + 7 new `buildClaude*Event` builders + new
  `claudeContentHash` helper (`hash/fnv` import added).
- `cmd/observer/hook_test.go`: 11 new tests (one per builder + 3
  extra: PostToolBatch empty-batch suppression, InstructionsLoaded
  optional-fields, ConfigChange skills-source fallback) + new
  builders added to the missing-session-ID rejection sweep.
- `internal/hook/unregister_test.go`: switched the
  user-hook-preserved seed event from `Setup` (now managed) to
  `TeammateIdle` (still unmanaged).

**Schema:** no migrations. All event rows land in the existing
`actions` table with the `metadata` column inherited from migration
017 (PermissionMode + EffortLevel auto-populated from the envelope
via `baseToolEvent`).

**Behavior delta:** before this PR, 7 Claude Code lifecycle / flow
events fired into `hook.HandleApprove` (acknowledge-only, no DB
write). After this PR, each emits one row. Dashboard's Actions tab
now shows `permission_request` / `permission_denied` / `setup` /
`user_prompt_expansion` / `post_tool_batch` / `instructions_loaded`
/ `config_change` types alongside existing Tier 1 rows. New
`config_change` source values (`user_settings` / `project_settings`
/ `local_settings` / `policy_settings` / `skills`) appear as
RawToolName values — filterable via the existing Actions tab.

**Honest framing:**
- All 7 builders' field shapes are docs-verified — see the
  reference doc at `tmp/claude-code-hooks-payload-reference.md` for
  the source-of-truth grid. Live captures of real fires haven't
  landed yet; if Claude Code emits unexpected keys, they fall
  through silently (the docs-verified extraction is conservative
  about what it reads).
- PermissionRequest's `permission_suggestions` array shape is rich
  (addRules / setMode / addDirectories variants). We stash the
  array verbatim in PrecedingReasoning rather than parsing per-
  variant — dashboards can `json_extract` if they want to
  visualize specific suggestion types.
- FileChanged deferred. Wiring it cleanly needs either (a) Claude
  Code-default matcher behavior verified, or (b) a project-config
  surface that lets users opt files / globs in. Either deserves
  its own design discussion.

## [1.4.47] — 2026-05-10

### Added — Action metadata schema (migration 017)

New `actions.metadata` TEXT column carries per-event JSON for fields
both Claude Code and Codex hook payloads emit on every fire that
previously had no place to land: `permission_mode`, `effort_level`,
`is_interrupt`. App-layer JSON marshal/unmarshal — no SQLite JSON1
dependency added (matches the existing `sessions.metadata`
treat-as-string precedent). New `models.ActionMetadata` struct with
`omitempty` tags + `IsZero()` predicate so a zero-valued struct
persists as NULL and the column stays dense rather than filling with
empty `{}` placeholders.

`Store.InsertActions` extended:
- `insertActionSQL` 25 → 26 columns; new `marshalActionMetadata`
  helper.
- `ON CONFLICT DO UPDATE` extended with a metadata backfill rule
  (parallel to the duration_ms rule from v1.4.28): backfill from
  NULL only, never clobber a populated value.
- `ToolEvent → Action` conversion in `Store.Ingest` threads
  `e.Metadata → act.Metadata`.

Hook adapters populate metadata:
- **Claude Code** (`cmd/observer/hook.go`): `claudeBaseEnvelope`
  extended with `permission_mode` + `effort.level` parsing; new
  `envelopeMetadata` helper; `baseToolEvent` populates Metadata for
  every Tier 1 builder automatically.
- **Codex** (`internal/adapter/codex/hook.go`): `rawHookPayload`
  extended with the same fields plus `collaboration_mode.settings.
  reasoning_effort` (the JSONL-verified shape) as a defensive
  fallback. `BuildHookEvent` sets `base.Metadata` on every event
  branch; first-non-empty-wins between the two effort shapes.
- The pre-fix lossy `[interrupt] ` prefix on
  `buildClaudePostToolFailureEvent`'s ErrorMessage is replaced
  with a typed `Metadata.IsInterrupt = true` flag. ErrorMessage
  now carries only the actual error text.

Verified live: 6 Claude Code rows on the maintainer's DB landed with
`{"permission_mode":"default","effort_level":"xhigh"}` after restart.

### Added — Codex JSONL reasoning_effort capture

Extends the migration 017 metadata column to capture from the Codex
JSONL adapter (every tool call, every session) — not just hook events
(lifecycle markers only).

**Verified field path** on a real Codex 0.129.0 rollout:
`payload.collaboration_mode.settings.reasoning_effort` on
`type: "turn_context"` lines. Values per Codex CLI:
`minimal | low | medium | high` (or `null` = use model default).
The maintainer's local rollouts all had null, so non-null parsing
is implemented but not fixtured against real bytes.

- `internal/adapter/codex/adapter.go::sessionContext` extended with
  `EffortLevel string` (hand-populated from the nested envelope, not
  directly JSON-parsed since it doesn't ride on the flat envelope).
- `turnContextPayload` extended with the `CollaborationMode.Settings.
  ReasoningEffort *string` envelope; `*string` distinguishes
  field-absent from explicit-null (both collapse to empty downstream).
- `applyContext` extended with sticky propagation: a later
  `turn_context` that omits `collaboration_mode` (or sends explicit
  null) does NOT wipe a previously-set effort. Same precedence
  rule as Cwd/Model.
- New `withEffort(ev) ToolEvent` helper inside `ParseSessionFile`
  stamps `ctxState.EffortLevel` onto every emitted ToolEvent's
  `Metadata.EffortLevel` (when non-empty AND the event doesn't
  already carry one). Wraps all 17 `res.ToolEvents = append(...)`
  call sites — single mechanical seam.

Tests pin the propagation, sticky semantics across null /
omitted-collaboration_mode turns, and overwrite on explicit
change.

### Added — Cursor after-event update-in-place via Store.UpdateActionOutcome

Lands the symmetric "after-event enriches the matching before-event
row" path the v1.4.45 Tier 3 work registered as no-row pending. Pre-
fix `afterShellExecution`, `afterMCPExecution`, and `postToolUse` all
returned `(zero, false, nil)` from `cursor.BuildEvent`, deferring
the richer behavior to a follow-up. Post-fix the dispatcher routes
them through a new code path that updates the matching before-event
row's outcome fields in place.

- New `Store.UpdateActionOutcome(ctx, sourceFile, sourceEventID,
  success, errorMessage, durationMs) (int64, error)`. Conservative
  backfill: success always overwritten (after-event is
  authoritative); error_message only overwritten when newValue is
  non-empty (don't wipe a structured error from a sibling
  postToolUseFailure row); duration_ms only when newValue is
  non-zero AND existing is zero (mirrors InsertActions rule —
  never lower a populated duration).
- New `cursor.OutcomeUpdate` struct + `cursor.BuildAfterOutcome`
  function. SourceEventID is computed using the BEFORE-event's
  slug + identical hash inputs to `cursorEventID` so the update
  lands on the matching before-row.
- New `beforeEventNameFor(after, raw)` mapper handles routing:
  `afterShellExecution → beforeShellExecution`, `afterMCPExecution
  → beforeMCPExecution`, `postToolUse` → routed by tool_name (per-
  tool slug for covered tools; `preToolUse` for long-tail; no-
  pair for FileEdit/Subagent).
- New `deriveAfterSuccess(raw)` collapses the three-way success
  signal: explicit `success` bool > `exit_code == 0` >
  error-string emptiness fallback.
- `rawHookPayload` extended with `Success *bool`, `ExitCode *int`,
  `Output string` for after-event outcome parsing.
- `internal/hook/cursor.go::HandleCursorEvent` now calls
  `cursor.BuildAfterOutcome` BEFORE `cursor.BuildEvent` for
  after-events. CursorSink interface extended with
  `UpdateActionOutcome` method.

**Behavior delta:** before this PR a Cursor `afterShellExecution`
event was a no-op in the actions table (the before-row stayed at
the placeholder `Success=true` forever). After this PR the matching
beforeShellExecution row is updated with the real outcome. The
structured `postToolUseFailure` row is unchanged — both surfaces
coexist (failure row carries the typed failure_type; before-row
carries the typed-payload outcome).

### Decided — Cursor pre/postToolUse dedup model (kept current)

After review, kept the per-tool + universal-long-tail-fill model
rather than going universal-only. Per-tool hooks deliver typed
payloads (`command`, `server_name`+`tool_name`, `file_path`) that
beat re-extracting the same fields from `tool_input json.RawMessage`,
the dedup logic (`coveredByPerToolHook`) is small (5 cases), and
the after-event update-in-place wired in this same release makes
the before/after pair enrich a single row rather than emit
duplicates. Decision pin in `internal/adapter/cursor/adapter.go`
comment block.

### Fixed — Claude Code SubagentStop emits empty rows on most fires

Pre-fix every Claude Code `SubagentStop` hook fire wrote a row to
the actions table — but on the maintainer's live DB, 11 of 12
historical SubagentStop rows had empty `agent_type`, empty
`last_assistant_message`, and rendered as blank rows in the
dashboard. Claude Code apparently fires SubagentStop with only
the universal envelope + `agent_id` for many lifecycle cases
(not just real Agent-tool subagents).

Two coordinated fixes in `cmd/observer/hook.go::buildClaudeSubagentStopEvent`:

1. **Empty-shell suppression.** When `agent_type`,
   `last_assistant_message`, AND `agent_transcript_path` are ALL
   empty, return `(zero, false, nil)` so the row is never inserted.
   The loss is just a content-less lifecycle marker; the JSONL
   watcher's sidechain capture already covers real subagent
   activity via `agent-<id>.jsonl` transcript files.
2. **`last_assistant_message` lands where the dashboard renders.**
   Pre-fix the message went to `ToolOutput` (which feeds the
   FTS5 index but is NOT rendered in the actions list — the
   dashboard reads `target` and `raw_tool_input`). Post-fix the
   message lands in BOTH `RawToolInput` (rendered) and
   `ToolOutput` (indexed). `Target` falls back through three
   tiers: `agent_type` first (e.g. `Explore`), then a 120-char
   preview of `last_assistant_message`, then the
   `agent_transcript_path` basename — so the row never renders
   blank when there's any useful payload.

Tests pin all three paths plus the suppression case.

**Migration note:** existing empty rows in the DB from prior
fires remain (no in-place delete). A user can run
`DELETE FROM actions WHERE action_type='subagent_stop' AND
COALESCE(target,'')='' AND COALESCE(raw_tool_input,'')='';` to
clear them; future fires no longer create them.

### Added — Dashboard surface for action metadata + session-modal loading state

Three UI affordances to make the new metadata column visible and
the session detail modal feel responsive:

1. **Actions tab** — `permission_mode` / `effort_level` /
   `is_interrupt` extracted via `json_extract(metadata, '$....')`
   in the `/api/actions` SQL and surfaced as inline pills next
   to the action_type pill in the table:
   - `⚙ <level>` — reasoning effort (xhigh / high / medium / low / minimal)
   - `🔒 <mode>` — permission mode (only shown when non-default)
   - `⏸ interrupt` — user pressed esc on a tool failure

2. **Session detail modal** — same three badges added to the
   per-message tool_calls expand-row in the messages timeline.
   `handleSessionMessages` extends `toolCallRow` with the three
   metadata fields; SQL extracts via `json_extract`.

3. **Session modal loading + stale-data guard** — clicking a
   session now shows a centered spinner with "Loading session…"
   while `/api/session/<id>` and `/api/session/<id>/messages`
   resolve in parallel; the body stays hidden until both
   complete. New `state.sessionDetail.fetchToken` counter
   guards against switching sessions mid-fetch: if the user
   opens session B before A's fetch resolves, A's response is
   dropped and only B paints. Eliminates the prior "wrong
   session's data renders briefly" race.

### Schema

Migration `017_action_metadata.sql`: `ALTER TABLE actions ADD
COLUMN metadata TEXT`. Pre-migration rows leave the column NULL
and surface in queries as "we never had this metadata" — the
right interpretation since the adapters didn't capture it before
this release.

### Honest framing

- Codex JSONL effort path: field name verified from a real local
  rollout; non-null *values* not fixtured (all local sessions had
  `reasoning_effort: null` = use model default).
- Codex hook envelope: defensively accepts both the speculative
  `effort.level` shape (from the v1.4.45 handover docs) AND the
  JSONL-verified `collaboration_mode.settings.reasoning_effort`
  shape; first non-empty wins. Real hook captures will eventually
  settle which (or both) Codex actually emits.
- Claude Code envelope: live DB confirms `permission_mode` +
  `effort.level` ARE present in real hook payloads — 6 rows
  post-restart landed with `{"permission_mode":"default",
  "effort_level":"xhigh"}` (Claude uses `xhigh`, not the
  Codex/OpenAI `minimal/low/medium/high` ladder).
- Cursor after-event payload: field shapes assumed from the
  cursor-telemetry-observation-reference.md docs; real captures
  will validate. The dispatcher logs `outcome update touched 0
  rows` to stderr when pairing fails so dogfood can spot any
  mismatch.

## [1.4.46] — 2026-05-10

### Fixed — Antigravity endpoint cache + INSERT-vs-UPDATE distinction (FK 787 root cause)

Two follow-on improvements after the project-folder fix landed:

**1. Cache the working endpoint per language_server.** `Endpoints()`
iteration tries up to 6 URLs per call (PreferredEndpoint first, then
http for every owned port, then https). The heuristic-derived
PreferredEndpoint is often wrong (verified case: pid 694's
ports[1]=37933 was actually TLS, not HTTP as the heuristic guessed),
costing a full ~5s timeout on the first failed attempt for every
single conversation. New `endpointCache` on the Adapter keyed by
`PID:CSRFToken` (CSRF rotates each language_server start, so stale
cache entries naturally expire when a server is restarted) stores
the URL that succeeded most recently. Cache-hit path tries the
cached URL first; on success, returns immediately. On cache-hit
failure (server restarted, port changed), invalidates and falls
through to full Endpoints() iteration. Saves ~5s per conversation
on hosts where the heuristic gets the protocol wrong.

**2. FK 787 root cause fix.** The recurring
`store.recordCommandOutcome: insert: constraint failed: FOREIGN KEY
constraint failed (787)` errors traced to a real bug, not the
modernc.org/sqlite quirk we'd guessed:

- `insertActionSQL` is `INSERT ... ON CONFLICT DO UPDATE` (an
  UPSERT), but observer treated it like `INSERT OR IGNORE`. When the
  DO UPDATE branch fires for a duplicate (source_file,
  source_event_id), SQLite reports `RowsAffected() = 1` (a row was
  modified) and `LastInsertId()` returns the connection's PREVIOUS
  successful true-INSERT rowid — not the row we just upserted.
- Combined with retention pruning old rows, that stale rowid often
  points at a long-gone action. Observer set `a.ID = stale_id`,
  which `recordCommandOutcome` then used as the `action_id` FK in
  `failure_context.INSERT` → row doesn't exist → FK violation.
- Verified directly: the action_ids reported in the user's logs
  (1266813, 52321, 52322, 52323, 1266832) did NOT exist in the
  actions table; current MAX(id) was 1832013 with only 62814 rows
  (most older rows pruned by retention).

Fix: pre-check existence with a SELECT against the
`(source_file, source_event_id) UNIQUE` index BEFORE the upsert.
On hit, run the upsert (so duration_ms backfill still fires for
legitimately-improved values) but leave `a.ID = 0` so the caller's
`if a.ID == 0` skip for failure_context / file_state side effects
keeps working. On miss, run the upsert as before and trust
LastInsertId — it's reliable for the true-INSERT path.

Tests in `internal/store/store_test.go::TestInsertActions_DupLeaves
IDZero` pin the duplicate-leaves-ID-zero invariant directly. The
existing TestInsertActionsIdempotent + TestInsertActions_Duration
RefreshOnConflict tests still pass — duration_ms backfill behavior
preserved.

Tests in `internal/adapter/antigravity/process_test.go` add
`TestEndpointCacheKey` (PID+CSRF identity) and
`TestAdapterEndpointCacheRoundtrip` (hit/miss/invalidate flow).
All green; vet/fmt clean.

### Fixed — Antigravity project folder resolution + non-fatal failure_context insert

Two follow-on fixes for Antigravity-on-WSL after the language_server
selection fix landed: project folder kept showing as `[antigravity]`
even when capture was working, and a `FOREIGN KEY constraint failed`
error in `failure_context` was tearing down the watcher between
otherwise-successful ingests.

**Project folder fix.** Two compounding gaps:

1. `stateDBPathFor` only checked Linux-side candidates for the
   state.vscdb. With Antigravity-server on WSL + IDE running on
   Windows, conversations sit Linux-side under `/home/<u>/.gemini/`
   but the IDE's `state.vscdb` lives Windows-side at
   `/mnt/c/Users/<u>/AppData/Roaming/Antigravity/User/globalStorage/
   state.vscdb`. None of the same-home candidates existed, so
   `idxEntry == nil`, so `wantWS == ""`, so the workspace-match check
   always said `false`, so `projectRoot` stayed at the placeholder.
2. Even after fixing path resolution, live in-progress conversations
   weren't yet in `trajectorySummaries` — Antigravity flushes the
   index on session save / checkpoint, not real-time. Verified
   directly: a 657 KB blob held 101 indexed conversation UUIDs but
   not the active live one.

Two coordinated fixes:

- **`stateDBPathFor` cross-mount candidates.** New layer iterates
  `crossmount.AllHomes()` and adds OS-appropriate state.vscdb
  candidates for each detected mount: `<windows_home>/AppData/
  Roaming/Antigravity/...` for Windows homes, `<mac_home>/Library/
  Application Support/Antigravity/...` for macOS, `<linux_home>/
  .config/Antigravity/...` for non-native Linux. Same-home
  candidates still tried first; cross-mount candidates fall through
  cleanly when not present.
- **Workspace_id reverse-resolution for live conversations.** New
  `resolveWorkspaceIDToPath(wsID, roots, maxDepth)` helper inverts
  the lossy `file_` + replace-non-alphanumerics-with-underscore
  encoding by BFS-walking each home root up to depth 4, encoding
  every visited directory's path back to the workspace_id form, and
  matching. Caches hits AND misses on `Adapter.wsResolveCache`
  (depth-4 walk is cheap once but not per-conversation; both
  outcomes saved). When the gRPC recovery succeeds and the
  language_server has a `--workspace_id` but `idxEntry` is still
  nil, the resolver runs and `applyResolvedProjectRoot` overwrites
  `[antigravity]` placeholders on every emitted ToolEvent /
  TokenEvent. Pruning skips dotfiles and known-noisy roots
  (`node_modules`, `vendor`, `target`) so the walk stays bounded on
  real filesystems.

**Non-fatal `recordCommandOutcome`.** The `failure_context` table is
a supplementary index for the dashboard's "this command kept
failing" view. A per-row insert error there shouldn't fail the
whole batch and rip down the watcher. Two changes in
`internal/store/store.go`:

- The Ingest loop now logs the error to stderr and continues to the
  next action instead of returning it. Actions and tokens that did
  land stay landed.
- `recordCommandOutcome` adds a defensive guard: if `action_id`,
  `session_id`, or `project_id` are zero/empty, return early
  (silent skip) rather than provoking the FK violation.

The underlying root cause of the FK violation is still under
investigation (likely a modernc.org/sqlite quirk with `INSERT OR
IGNORE` + `LastInsertId` returning a stale id, or a session that's
elided by an upsert race) — but treating this index as best-effort
rather than load-bearing is the right framing regardless.

Tests in `internal/adapter/antigravity/process_test.go` add
`TestResolveWorkspaceIDToPath` (5 cases incl. exact match for
hyphenated and non-hyphenated paths, no-match on wrong wsID, depth
exhaustion, missing `file_` prefix) and
`TestResolveWorkspaceIDToPath_SkipsDotfilesAndNoise` (BFS skips
hidden dirs and `node_modules` subtrees). All green; `vet`/`fmt`
clean; full suite shows only the two pre-existing flakies.

### Fixed — Antigravity WSL recovery picks the right language_server

Fixes a silent zero-events-captured bug for Antigravity-on-WSL: tool
calls, token usage, and project-folder attribution were missing from
the dashboard despite observer logging "OK" responses for each
language_server gRPC call. Two compounding root causes:

1. **Port-protocol heuristic was wrong.** `discoverNativeLinux`
   assumed `ports[0]=HTTPS, ports[1]=HTTP` (sorted ascending), but on
   the host this fix was diagnosed against (`pid 694 --workspace_id
   file_home_marmutapp_superbased_observer`) the actual mapping was
   `ports[0]=HTTP (35989), ports[1]=TLS (37933)` — flipped from the
   heuristic. Observer sent HTTP/2 plaintext (h2c) to a TLS port,
   which the language_server killed mid-stream → `write: broken pipe`.
2. **Wrong-server fall-through.** When the heuristic mismatch caused
   the correct workspace's server to fail, observer iterated to the
   *next* discovered language_server — but each language_server only
   hosts conversations from ITS `--workspace_id`. Talking to a non-
   matching server returned a stub response (190-byte markdown wrapper
   + a 12K stale structured payload from a different workspace). The
   parser extracted ~0 events, which observer accepted as success.

Three coordinated fixes:

- **`workspaceIDFromURI(uri)` + workspace-aware server ordering.** New
  helper in `internal/adapter/antigravity/process.go` encodes a
  `file://` URI to the language_server's `--workspace_id` format
  (`file_` + path with non-alphanumerics replaced by `_`, leading
  slashes stripped). `recoverViaLocalGRPC` decodes the conversation's
  index `workspaceURI`, derives the wanted workspace_id, and sorts
  discovered servers so matchers come first via new
  `sortServersByWorkspaceMatch`. Non-matching servers still get tried
  as last resort (no info loss when the index is missing) but no
  longer outrank the right one.
- **`LanguageServer.Endpoints()` + try-both-protocols.** New method
  returns every plausible URL for a server (`PreferredEndpoint()`
  first, then `http://port` for every owned port, then `https://port`
  for every owned port — deduped). New `tryConvertTrajectoryAcross
  Endpoints` helper iterates the list until one returns a non-error
  response, then threads the working endpoint through to the
  structured-trajectory call via the new `fetchStructuredEnrichment
  NativeAt` method (so the second gRPC doesn't re-iterate failing
  protocols). The old `HTTPPort`/`HTTPSPort` fields stay populated by
  the heuristic for backwards-compat (they feed `PreferredEndpoint()`,
  the first candidate in the list) — but the heuristic is no longer
  load-bearing.
- **Empty-stub guard.** New `numEvents(res)` helper. After parse, if
  `len(ToolEvents) + len(TokenEvents) == 0`, observer treats the
  response as a wrong-server stub and continues to the next server
  instead of returning success. Closes the failure mode that masked
  the bug — the next-server iteration that previously short-circuited
  on the first responsive but-empty server now correctly walks all
  candidates until one extracts real events.

Diagnostic logging extended so the same symptom is easier to debug
next time: `tracef` after each server call now reports the workspace
match flag, and after parse reports extracted tool/token counts +
model name. Lines like `linux-native pid=846 ... OK (190 bytes,
ws="file_home_marmutapp_superbased" match=false)` and `pid=846
returned empty stub (0 events extracted); trying next server` make
the filtering decisions visible.

Tests in `internal/adapter/antigravity/process_test.go` pin
`workspaceIDFromURI` (8 cases incl. URL-encoded, vscode-remote URIs,
trailing slash), `LanguageServer.Endpoints()` (iteration order,
dedup, fallback to HTTPPort/HTTPSPort when Ports is empty),
`sortServersByWorkspaceMatch` (matches first, empty wantWS preserves
order), `numEvents` (count helper for the empty-stub guard), and the
existing `parseTCPListeners` inode-filtering invariant.

## [1.4.45] — 2026-05-10

### Added — Cursor hook expansion (Tier 3)

Builds on the Tier 2 expansion below by adding the universal preToolUse
event and three paired-after observers, taking Cursor live-hook
coverage from 12 to 16 of the 18 documented agent-loop events. The
remaining two (afterAgentResponse, afterAgentThought) fire on every
streaming text/thought delta — registering them would mean a process
spawn per fragment and is deferred until streaming-aware ingest lands.
Tab events (beforeTabFileRead, afterTabFileEdit) remain out of scope:
tab uses a different session model from agent chat with no shared
correlation keys.

- **`preToolUse` — long-tail fill.** Cursor's universal pre-tool hook
  fires for every tool call, but the actions table already gets rich
  per-tool rows from `beforeShellExecution` / `beforeMCPExecution` /
  `afterFileEdit` / `beforeReadFile` / `subagentStart`. To avoid
  double-counting, the `preToolUse` builder calls a new
  `coveredByPerToolHook(tool_name)` predicate (matches `Shell`/`Bash`/
  `command`, `call_mcp_tool`/`MCP`, `ApplyPatch`/`EditFile`/
  `StrReplace`/`Edit`, `Read`/`ReadFile`/`cat`/`ReadLints`,
  `Subagent`/`Agent`) and returns `(zero, false)` for those — the
  per-tool hook stays canonical. For the long-tail tools the per-tool
  hooks miss (`Glob`/`FindFiles` → `ActionSearchFiles`,
  `Grep`/`Search`/`SearchFiles`/`semanticsearch` → `ActionSearchText`,
  `WriteFile`/`CreateFile` → `ActionWriteFile`, plus any future tools)
  the builder emits a row using `cursorTranscriptActionType` +
  `cursorTranscriptTarget` (the same helpers the transcript walker
  uses, so the mapping stays in one place). Truly-unknown tools
  surface as `ActionUnknown` rows preserving `RawToolName`. Trade-off
  consciously accepted: pre-edit visibility for `EditFile`/`StrReplace`
  is dropped (afterFileEdit fires only on success); revisit if
  pre-edit gating becomes a real use case.
- **`postToolUse` / `afterShellExecution` / `afterMCPExecution` —
  registered no-row.** Parity with codex's `PreToolUse`/`PostToolUse`
  precedent. The richer thing to do — update the `before*` row's
  `Success` / `ErrorMessage` / `DurationMs` from the after-event —
  needs a new `Store.UpdateActionOutcome(sourceFile, sourceEventID,
  ...)` method, deferred to a separate batch. Failures are still
  captured live via `postToolUseFailure` (Tier 2) so the most
  load-bearing signal isn't blocked by the deferral.
- **`afterAgentResponse` / `afterAgentThought` — explicitly NOT
  registered.** Per the docs reference these fire on every text
  delta during streaming (`Final assistant text in a single payload
  (only deltas via afterAgentResponse)`), so registering means a
  process spawn per fragment. High overhead, low marginal value —
  the JSONL transcript walker delivers the same content on stop with
  no per-fragment cost. Defer until streaming-aware ingest lands.
- **`internal/adapter/cursor/adapter.go`** adds 4 new constants
  (`EventPreToolUse`, `EventPostToolUse`, `EventAfterShellExecution`,
  `EventAfterMCPExecution`), one new `rawHookPayload` field
  (`ToolInput json.RawMessage` for `tool_input` — distinct from the
  existing `Input` field which beforeMCPExecution uses for `input`),
  the `coveredByPerToolHook` predicate, and the new switch cases.
  `cursorEventID` extended for `EventPreToolUse` to disambiguate
  multiple preToolUse fires within one turn (uses `tool_use_id` when
  present, falls back to `(tool_name, tool_input)` hash).
- **`internal/hook/register.go::cursorEvents`** grows from 12 to 16
  entries.
- **Tests.** `internal/adapter/cursor/adapter_test.go` adds 4 new
  tests: `TestBuildEvent_PreToolUse_CoveredToolsSuppressed` (table-
  driven across all 11 covered names), `TestBuildEvent_PreToolUse_
  LongTailToolsRecorded` (table across Glob/Grep/semanticsearch/
  WriteFile/unknown-future-tool), `TestBuildEvent_PreToolUse_
  DistinctIDsForDistinctTools` (within-turn dedup), and
  `TestBuildEvent_PostToolUseAndAfterEventsNoRow` (the three paired-
  after events all return `(zero, false, nil)`).

### Added — Cursor hook expansion (Tier 2)

Broadens Cursor live-hook coverage from the original 5 events to 12 by
adding `beforeReadFile`, `postToolUseFailure`, `sessionStart`,
`sessionEnd`, `subagentStart`, `subagentStop`, and `preCompact`.
Implemented from the Cursor Hooks docs (`https://cursor.com/docs/hooks`)
against the field-level reference at
`tmp/cursor-telemetry-observation-reference.md` — no live capture
pre-implementation (capture is blocked on Cursor Pro, free tier disables
chat model selection even with BYOK). Honest framing: payload shapes
follow documented + sibling-tool conventions; tolerant decoders accept
plausible field-name variants (`agent_id` / `subagent_id` for sub-agent
identity; `error` field on tool failure). Discrepancies discovered post-
hoc when a Pro user drives the integration will surface as `unknown`-
typed rows rather than silent drops, and tests will pin the corrected
shape.

- **`beforeReadFile` → `ActionReadFile`.** Closes audit C2: Cursor's
  pre-v1.4.45 hook surface had no live file-read signal, so freshness/
  redundancy detection systematically undercounted Cursor activity vs
  claudecode. Live capture now happens at the same point in the agent
  loop as `beforeShellExecution` — before the tool runs — and shares
  `generation_id` with sibling rows for correlation.
- **`postToolUseFailure` → `ActionToolFailure`.** Pairs with the
  Claude Code Tier 1 expansion below: same ActionType, same field
  shape (`tool_name`, `failure_type` → `RawToolName`, `error` →
  `ErrorMessage`, `duration_ms`, scrubbed `input`). `Success=false`
  always. `tool_use_id` discriminates duplicates within a turn when
  present; falls back to `(tool_name, error)` hash otherwise.
- **`sessionStart` / `sessionEnd` → `ActionSessionStart` /
  `ActionSessionEnd`.** Lifecycle markers — composer-session boundary
  events that fire before any generation, so `generation_id` may be
  empty. Event ID falls back to `conversation_id` to keep the insert
  idempotent. `Target` carries `source` (start: `"startup"|"resume"|
  "clear"`) or `reason` (end: `"clear"|"resume"|"logout"|...`).
- **`subagentStart` / `subagentStop` → `ActionSubagentStart` /
  `ActionSubagentStop`.** Brackets a Cursor sub-agent's runtime,
  parallel to the Claude Code SubagentStart/Stop pair. `IsSidechain=
  true` so existing sidechain-aware queries pick them up. `MessageID`
  is the sub-agent identity (accepts both `agent_id` and the docs-
  shape alternate `subagent_id`); `Target` is `agent_type` for fan-out
  attribution.
- **`preCompact` → `ActionContextCompacted`.** Records context-window
  compaction trigger (`auto`/`manual`) so the dashboard can surface
  compaction frequency without polluting the file-edit timeline.
  Parallel to codex's top-level `compacted` event.

- **Out of scope for this batch (deferred):** `preToolUse` and
  `postToolUse` are universal events that overlap with the per-tool
  `before*` hooks already registered — emitting both would duplicate
  every shell/MCP/file row. Deferred until we make a dedup decision
  (probably: register universal events as no-row, drop the per-tool
  ones — but that's a behavioral change worth doing in isolation).
  `afterShellExecution` and `afterMCPExecution` are paired with their
  `before*` siblings; cleanest semantics is to update the existing
  before-row's `Success` / `ErrorMessage` / `DurationMs` rather than
  emit a new row, but update-in-place is more design than a 1-batch
  job. `afterAgentResponse` and `afterAgentThought` would duplicate
  assistant text + thinking already in the JSONL transcript walker.
  Tab events (`beforeTabFileRead`, `afterTabFileEdit`) use a different
  session model from agent chat — separate work.

- **`internal/adapter/cursor/adapter.go::BuildEvent`** extended with
  7 new switch cases. New constants `EventBeforeReadFile`,
  `EventPostToolUseFailure`, `EventSessionStart`, `EventSessionEnd`,
  `EventSubagentStart`, `EventSubagentStop`, `EventPreCompact`.
  `rawHookPayload` extended with the Tier 2 union of fields
  (`tool_use_id`, `error`, `failure_type`, `duration_ms`, `source`,
  `reason`, `agent_id`, `agent_type`, `subagent_id`, `trigger`).
  `cursorEventID` updated to derive a stable ID from `conversation_id`
  when `generation_id` is empty (session-lifecycle events) and to
  hash event-specific discriminators (file_path / tool_use_id /
  agent_id) so within-turn duplicates of the same event class never
  collide.
- **`internal/hook/register.go::cursorEvents`** grows from 5 to 12
  entries. Both `registerCursor` (writes `~/.cursor/hooks.json` for
  native installs) and `registerCursorWindows` (writes to
  `/mnt/c/Users/<u>/.cursor/hooks.json` with `wsl.exe -d <distro> --`
  prefix for Cursor-on-Windows + WSL-observer setups) iterate the
  same slice, so the expansion lights up both surfaces. Existing
  conflict-guard / staleness-detect logic carries through unchanged
  (cross-binary-path refreshes; foreign-entry rejection without
  `--force`).
- **Tests.** `internal/adapter/cursor/adapter_test.go` adds one new
  test per Tier 2 event (`TestBuildEvent_BeforeReadFile`,
  `TestBuildEvent_PostToolUseFailure`,
  `TestBuildEvent_SessionStart`, `TestBuildEvent_SessionEnd`,
  `TestBuildEvent_SubagentStartStop`, `TestBuildEvent_PreCompact`)
  pinning the ActionType mapping, target extraction, error-field
  capture, idempotency-key derivation, and the
  `agent_id`/`subagent_id` fallback. The existing
  `TestBuildEvent_DeterministicEventID` continues to cover the
  shared-payload-stable-ID invariant for the original 5 events.

### Added — Codex hook coverage (full) + Claude Code hook expansion (Tier 1)

Captures the previously-zero Codex hook surface and broadens Claude Code
from 6 of ~23 events to 14, ingesting tool failures, sub-agent
lifecycle, host notifications, working-dir changes, and clean
session-end markers as actions table rows. Schemas were captured live
from real Codex (codex 0.129.0) and Claude Code sessions and
implemented against the resulting payloads — both surfaces had
doc-vs-reality gaps the captures surfaced (codex `hooks.json` requires
`{matcher, hooks: [{type, command}]}` not the flat shape the
developer docs example implied; Claude's `WorktreeCreate` payload
sends `{name}` not `{base_path}`; Claude's `CwdChanged` sends
`old_cwd`/`new_cwd` not `previous_cwd`).

- **Codex hooks — new package.** `internal/adapter/codex/hook.go` adds
  `BuildHookEvent` covering all six documented events
  (`SessionStart`, `UserPromptSubmit`, `PreToolUse`,
  `PermissionRequest`, `PostToolUse`, `Stop`). `SessionStart` and
  `UserPromptSubmit` emit action rows (`session_start`, `user_prompt`);
  `PermissionRequest` emits an `unknown`-typed row carrying the tool
  name + scrubbed input so dashboards can correlate slow turns to
  permission prompts. `PreToolUse` / `PostToolUse` / `Stop` are
  registered for parity but emit no rows — codex's session JSONL and
  the proxy stream already capture that data with richer detail.
  Unknown future events surface as `ActionUnknown` rather than
  silently dropping.
- **Codex hook handler.** New `internal/hook/codex.go::HandleCodexEvent`
  mirrors the existing Cursor handler shape (read stdin → reply
  immediately with `{}` so codex doesn't wait → ingest into the
  configured DB with a 250ms deadline). `cmd/observer/hook.go` adds
  `case "codex":` dispatching to it.
- **Codex registration.** `internal/hook/register.go::registerCodex`
  writes `~/.codex/hooks.json` with the `{hooks: {<event>: [{matcher,
  hooks: [{type, command}]}]}}` schema codex 0.129.0 requires. Also
  patches `[features].hooks = true` into `~/.codex/config.toml` via
  `ensureCodexHooksFeatureFlag` — codex gates its hook dispatcher
  behind that flag and silently ignores hooks.json without it. The
  legacy `[features].codex_hooks` flag was deprecated in a recent
  codex release; we set the new flag and leave any legacy key alone
  (codex prints its own deprecation warning, the right signal for
  the user). Idempotent re-registration; conflict-guard refuses to
  clobber user-authored hook entries unless `--force`.
  `Registry.Installed()` already detected `~/.codex` so
  auto-register-on-start picks codex up automatically.
  `cmd/observer/init.go::hookSupported` extended to accept `codex`;
  the `--codex` flag description updated to reflect hooks-now-supported.
- **Codex trust-flow guidance — surfaced on three independent paths.**
  Codex requires per-hook user trust approval the first time each
  entry is seen (security feature; trust state lives in
  `~/.codex/config.toml [hooks.state]` keyed by an opaque sha256 hash
  that no `codex` subcommand exposes). Because observer can't
  pre-trust on the user's behalf, the gap is surfaced wherever the
  user is most likely to look:
  1. **CLI hint after `observer init --codex`** — one-time guidance
     block walking the user through `codex` → `/hooks` → mark all 6
     trusted.
  2. **`observer doctor` check** — new `codex.hook_trust` check reads
     `~/.codex/hooks.json` for observer-owned entries and
     cross-references `[hooks.state]` in `config.toml`. Reports
     `StatusWarn` listing the specific event names that need trust;
     flips to `StatusOK` once the user completes the `/hooks` walk.
  3. **Dashboard banner** — Compression tab renders a
     `codex-hook-trust-card` warning whenever there's actionable
     friction (`needs_trust` / `config_missing` / `feature_disabled`
     statuses). The card lists exact event names, includes the
     specific `/hooks` instruction, and offers a *Re-check after I've
     trusted* button so users can verify their walk worked without
     reloading the page. Auto-hides on `all_trusted` / `no_hooks` /
     `no_codex`. Backed by the new `GET /api/setup/codex-hooks`
     endpoint.

  We don't reverse-engineer the hash because codex can change the
  algorithm in any release; capture-driven probes against codex
  0.129.0 confirmed 7 plausible inputs (command-only, JSON-canonical
  with/without matcher, etc.) all miss the actual hash. If OpenAI
  ever ships `codex hooks trust <event>` (or equivalent), observer
  adopts it transparently — the trust check just looks at config.toml
  state. Full rationale + walkthrough in `docs/codex-hook-trust.md`.
- **Claude Code Tier 1 events.** `claudeCodeEvents` now registers 14
  events (was 6): added `SessionEnd`, `UserPromptSubmit`,
  `PostToolUseFailure`, `StopFailure`, `SubagentStart`, `SubagentStop`,
  `Notification`, `CwdChanged`. Each maps to an actions row via a
  per-event builder in `cmd/observer/hook.go`:
  - `PostToolUseFailure` → new `ActionToolFailure`. Carries the failing
    tool name, scrubbed input, host-side error message, and duration.
    `is_interrupt: true` payloads get `[interrupt]` prepended to
    `ErrorMessage` so dashboards can distinguish user-cancelled tools
    from genuine failures without a new column.
  - `StopFailure` → existing `ActionAPIError`. Captures typed error
    classes (`rate_limit | authentication_failed | oauth_org_not_allowed |
    billing_error | invalid_request | server_error | max_output_tokens
    | unknown`) the inline JSONL `system / api_error` records don't
    carry as cleanly.
  - `SubagentStart` / `SubagentStop` → new `ActionSubagentStart` /
    `ActionSubagentStop`. Distinct from the parent's
    `ActionSpawnSubagent` (which fires when the parent invokes
    `Agent`) — the new pair brackets the sub-agent's own runtime,
    carrying `agent_id` + `agent_type` for fan-out attribution and
    `IsSidechain=true` so the existing sidechain-aware queries
    pick them up.
  - `Notification` → new `ActionNotification`. Surfaces typed dispatches
    (`permission_prompt | idle_prompt | auth_success | elicitation_*`)
    that aren't in the JSONL transcript at all.
  - `SessionEnd` → new `ActionSessionEnd`. Clean lifecycle close marker.
  - `CwdChanged` → new `ActionCwdChange`. `Target` carries the new cwd,
    `PrecedingReasoning` the previous cwd. Builder accepts both
    `old_cwd` (observed) and `previous_cwd` (docs) for forward-compat.
  - `UserPromptSubmit` → existing `ActionUserPrompt`. Live capture
    parallel to the JSONL watcher's eventual ingest (one-turn lag
    closed for hook-enabled installs).
- **Tests.** `internal/adapter/codex/hook_test.go` covers every event
  shape including the `Pre/PostToolUse no-row` decision and the
  `unknown future event` capture path. `internal/hook/register_test.go`
  adds 5 codex registration cases (fresh install, idempotency,
  config.toml preservation, conflict guard, unregister).
  `cmd/observer/hook_test.go` adds 11 builder-level tests covering
  each Claude Tier 1 event, including the `is_interrupt` marker prefix
  and the `old_cwd` vs `previous_cwd` field-shape forward-compat.

**Honest framing — what this does NOT cover:**
- **Codex hook trust automation.** Per-hook trust still requires a
  one-time manual `/hooks` walk inside codex. We surface the hint
  but cannot bypass codex's security model without reverse-engineering
  the (undocumented, change-prone) trust hash algorithm.
- **`permission_mode` and `effort.level` capture on Claude Code tool
  events.** Both surface in the live payload but ingesting them
  requires a schema migration on the actions table (no free-form
  metadata column today). Deferred to a follow-up — the row writes
  succeed today; the new fields are dropped on the floor.
- **Tier 2/3/4 Claude Code events.** `Setup`, `UserPromptExpansion`,
  `PostToolBatch`, `PermissionRequest`, `PermissionDenied`,
  `InstructionsLoaded`, `ConfigChange`, `FileChanged`,
  `WorktreeCreate` / `WorktreeRemove`, `TaskCreated` / `TaskCompleted`,
  `TeammateIdle`, `Elicitation` / `ElicitationResult` — captured as
  payloads during the dev probe; not yet wired through registration
  or handlers. Some (e.g. `WorktreeCreate`) require non-trivial reply
  shapes: `WorktreeCreate` is blocking and demands the worktree path
  on stdout — implemented naively it would fail every Claude Code
  subagent spawn with `isolation: "worktree"`.
- **Cursor hook expansion.** Tracked separately; capture shim is in
  place at `/mnt/c/Users/<u>/.cursor/hooks.json` awaiting a Cursor
  drive — implementation pending.

### Added — Cursor on Windows + observer on WSL bridge

- **Cursor transcript watcher.** New `internal/adapter/cursor/scan.go`
  implements the `adapter.Adapter` interface against Cursor's on-disk
  agent transcripts under
  `<home>/.cursor/projects/<slug>/agent-transcripts/<conv>/<conv>.jsonl`.
  Until now Cursor was hook-only; the watcher path lights up cross-mount
  capture (Cursor on Windows + observer in WSL via `crossmount.AllHomes()`)
  and provides a fallback when hooks aren't registered. Reuses the
  existing `parseTranscriptTurns` + `BuildTranscriptToolEvents` +
  `BuildTranscriptUserPromptEvent` machinery; emits synthetic
  `MessageID = transcript:<convID>:turn<N>` so watcher rows coexist with
  hook rows (different MessageIDs, same SessionID = conversation_id;
  SourceFile distinguishes them — `cursor:hook` vs the real transcript
  path). `cmd/observer/main.go::defaultAdapters` now registers
  `cursor.New()` alongside the existing JSONL adapters.
- **Project slug decoder.** `DecodeProjectSlug("c-programsx-marmutmain")`
  → `C:\programsx\marmutmain`. Reverse-engineered from real on-disk data:
  Cursor encodes the workspace path's drive letter (lowercase) + each
  component joined by `-`. Heuristic: 1-char first segment = Windows
  drive letter; 2+ char first segment = POSIX-style path with a leading
  `/`. Lossy only if a path component itself contains `-` (no observed
  cases). Captures the workspace_root field for transcript-derived rows.
- **Tool-name aliases for v1.4-era transcripts.** Cursor's current
  transcripts use `Read` and `SemanticSearch` as tool names; the action-
  type switch only knew `readfile` / `cat` / `readlints` (and didn't
  handle SemanticSearch at all). `cursorTranscriptActionType` and
  `cursorTranscriptTarget` extended.
- **Windows-host hook registration (`cursor-windows` target).** New
  `internal/hook/register.go::registerCursorWindows` writes hooks at
  `/mnt/c/Users/<u>/.cursor/hooks.json` with each command wrapped in
  `wsl.exe -d <distro> -- <linux-bin> hook cursor <event> [--config ...]`,
  so a Windows-Cursor process can invoke the WSL-side observer binary
  for stop-event token telemetry the watcher path can't recover. Distro
  read from `Options.WSLDistro` or `$WSL_DISTRO_NAME`; auto-detection
  via `crossmount.AllHomes()` + `OS=windows` filter. New options:
  `WSLDistro`, `WindowsCursorHome` (test seam). Registry.Installed()
  surfaces `cursor-windows` when applicable. `observer init` picks it
  up automatically via the existing union-of-installed dispatch.
- **Stop-hook transcript replay now works on WSL+Windows-Cursor.**
  `BuildStopTranscriptEvents` was silently failing to ingest tool_use
  rows when Cursor on Windows sent `transcript_path` as a Windows-style
  path (`C:\Users\<u>\.cursor\projects\…`) — `os.Open` from the WSL
  observer can't resolve that. Now wrapped via
  `crossmount.TranslateForeignPath` so the path becomes `/mnt/c/…`
  before the file read. SourceFile on the emitted events also uses the
  translated path, which makes hook-replay rows and watcher rows agree
  on file identity (the SourceEventID still differs intentionally; see
  the dual-source design note).
- **Watcher no longer emits user_prompt rows.** The live
  `beforeSubmitPrompt` hook captures every user prompt with the real
  `generation_id` synchronously when the user submits; the watcher's
  transcript-derived user_prompt was a pure duplicate (different
  MessageID, same content, same SessionID). On a normal install
  (auto-register on `observer start` guarantees hooks are wired) the
  user-prompt row appears once. Trade-off: pre-install historical
  transcripts lose user_prompt rows, but the assistant tool_use rows
  that follow still convey what the model did. Watcher continues to
  emit tool_use rows because Cursor's hook layer has no
  `beforeFileRead` / `beforeGrep` event (audit C2 coverage gap), so
  reads and searches would otherwise be invisible.
- **Watcher defers to live hook when session is already covered.**
  `cursor.Adapter.WithSessionHookChecker` lets callers inject a
  predicate that reports whether a session_id has any
  `source_file='cursor:hook'` rows in the DB. When the predicate
  returns true, `ParseSessionFile` skips emission entirely — the
  live hook layer (beforeSubmitPrompt + stop's BuildStopTranscriptEvents
  with the path-translation fix above) covers everything for that
  session. Wired in `cmd/observer/main.go` against
  `Store.SessionHasSourceFileRows`. Net effect: live sessions
  produce one row per event class (no watcher/hook duplication);
  pre-install historical sessions still get full watcher coverage
  because they have zero hook rows. New tests:
  `TestParseSessionFile_DefersWhenHookActive` (skip path) and
  `TestParseSessionFile_EmitsWhenNoHookRows` (cold-start fallback).
- **Auto-register hooks on `observer start`.** The daemon now runs an
  idempotent registration pass on every launch, installing hooks for
  every tool `Registry.Installed()` detects. Already-registered
  entries are silent no-ops; freshly installed entries print
  `auto-register <tool>: installed N hook(s) at <path>` to stdout;
  conflicts with non-observer entries log a warning and skip
  (`Force=false` — never silently overwrites user-authored
  configuration). Closes the discoverability gap where a user with
  Cursor on Windows had to manually run `observer init` AFTER the
  daemon was already up, which most users wouldn't think to do.
  Gated by new `[observer.hooks] auto_register` config key
  (defaults to `true`); set to `false` to opt out and manage hooks
  via `observer init` exclusively.
- **Tests.** `internal/adapter/cursor/scan_test.go` covers slug decode
  (Windows + POSIX shapes, edge cases), `IsSessionFile` matcher
  (back-slash + forward-slash inputs, dir/file-basename mismatch
  rejection), path-component extractors, end-to-end `ParseSessionFile`
  against a synthesised real-shape transcript, and the offset-based
  no-op shortcut. `internal/hook/register_test.go` adds three new
  cases (fresh install, missing-distro error, idempotent re-run).
- **Docs.** `docs/provider-mapping.md` Cursor column file-telemetry rows
  filled in (rollout location, format, tool-call shape, adapter-package
  description) and the hook-registration row gained a `cursor-windows`
  paragraph. CHANGELOG entry follows the c20a116 honest-framing
  pattern.

**Honest framing — what's NOT covered by the watcher path:**
- **Token usage / model.** Cursor's transcript JSONL has neither field;
  cost columns for transcript-only sessions stay zero. Token rows still
  require the live `stop` hook (or the new `cursor-windows` wsl.exe
  variant on Windows installs).
- **Real-time.** Cursor flushes the transcript file after each
  assistant turn completes; the watcher reacts on file growth, so
  there's a one-turn lag vs in-process hooks.
- **No `beforeFileRead` event.** Audit C2's coverage gap on the live
  hook layer doesn't apply to the watcher path — `tool_use{name:"Read"}`
  blocks are present in the transcript so reads ARE captured. (Hook
  + watcher together over-count; the new MessageID prefix makes the
  rows distinct so the dashboard's tools tab can render both without
  collision.)
- **Latency on cold WSL.** The `wsl.exe`-launched cursor-windows hook
  inherits WSL's cold-start latency (~100-300ms after distro idle).
  Within the 500ms hook budget but not generous; if cursor users
  observe drops, the next iteration is a native `cursor-hook-bridge.exe`
  that POSTs to a localhost endpoint on the WSL daemon (option 3 from
  the design discussion). Not measured against a real workload yet.

**Smoke-tested live** against `/mnt/c/Users/auzy_/.cursor/projects/
c-programsx-marmutmain/agent-transcripts/93eb822a-…/93eb822a-….jsonl`
on a real WSL host: 16 actions ingested cleanly, project_root
decoded as `C:\programsx\marmutmain`, action types split as 7
`read_file` + 1 `user_prompt` + 6 `search_files` + 2 `run_command`.

## [1.4.44] — 2026-05-08

### Added — codex CLI compression parity (this session)

- **Codex CLI now gets the same compression treatment as Claude Code.**
  Verified against codex 0.129.0 via live capture. Phase summary:

  - **Tier 0/1 wire-side parity for the OpenAI Responses API.** New
    functions in `internal/compression/conversation/openai.go`:
    `resolveOpenAIResponsesToolCalls` (back-fills toolName + filename
    onto `function_call_output` items via `call_id` lookup),
    `collectOpenAIMCPToolNames` (walks `tools[]` for `namespace`-typed
    entries with `mcp__` prefix), `isOpenAIMCPCall` (union predicate:
    set membership OR HasPrefix `mcp__` fallback),
    `compressOpenAIResponsesToolResults` (per-block per-type
    compression mirroring the Anthropic `compressToolResults`),
    `stashOpenAIResponsesLargeBodies` (G31 stash for codex outputs),
    `compressOpenAIResponsesToolDefinitions` with namespace recursion
    via `compressOpenAIResponsesToolEntry` (description-tail trim +
    `examples` strip from `parameters`; forbidden list — `parameters` /
    `properties` / `required` / `enum` / numeric bounds — universal),
    `extractFilePathFromArgs` + `parseReadCmdForPath` (heuristic
    file-path extraction from `function_call.arguments` shapes; explicit
    `file_path` / `path` / `filename` keys take priority; falls back to
    parsing `cmd` / `command` for `cat` / `head` / `tail` / `less` /
    `more` / `nl` verbs with `bash -lc` wrapper unwrap),
    `substituteOpenAIRedundantReads` (C16 read-cache substitution per
    session; (filename, content-hash) tuple deduplication; mirrors the
    Anthropic-side path with the same MCP-skip invariant). `runOpenAI`
    in `pipeline.go` extended to invoke the full pre-pass chain
    (read-cache → per-type → tool-defs trim → stash) and adds the
    fast-path early-return for `prompt_cache_key`-based prefix-cache
    invariance (without it, even a no-op compression run breaks
    OpenAI's prefix cache on every turn).

  - **Tier 2 session-aware features for codex.**
    `extractOpenAISessionID` (`internal/proxy/provider.go`) reads the
    Responses API body's `prompt_cache_key` field (verified against the
    codex 0.129.0 capture: equals the `session_id` HTTP header in
    observed traffic). Header fallback in `proxy.go` reads the
    `session_id` request header for the rare case where the body lacks
    `prompt_cache_key`. The proxy's session-aware compressor gate +
    auth-cache write are both extended to fire on
    `provider == ProviderOpenAI` (previously Anthropic-only).
    `internal/messagesummary/summarizer_openai.go` ships
    `OpenAISummarizer` (Responses API summary calls, Bearer auth,
    `gpt-5-nano` default — free per OpenAI's 2026-04-29 pricing
    catalog) and `OpenAIFactory` (per-session credential lookup; JWT
    detection — `Bearer eyJ...` ChatGPT-Plus tokens return nil so D20
    no-ops gracefully on those sessions; API-key sessions get a real
    summariser). New `Pipeline.WithSummarizerFactoryFor(provider, …)`
    method + `summarizerFor(provider)` lookup so the pipeline picks
    the right summariser per request.

  - **Codex CLI launcher (`observer codex`).** Mirrors `observer claude`
    in shape: spawns codex with `-c openai_base_url='"<proxy>/v1"'`
    injected into argv so the Responses API request lands at the
    observer proxy. Both auth shapes (sk- API key + ChatGPT-Plus JWT)
    flow through the same override — the proxy's existing
    `isChatGPTAuthRequest` + `translateChatGPTPath` machinery routes
    the JWT form to `chatgpt.com/backend-api/codex/responses`
    automatically. `prepareCodexArgs` respects user-supplied
    `-c openai_base_url=…` / `-c model_provider=…` overrides without
    double-injecting (intent wins). Pinned by `cmd/observer/codex_test.go`.

  - **A/B harness scripts for codex.** New
    `scripts/ab-codex-{setup,start,stop,report}.sh` — mirror the
    Claude-side scripts on ports 8832 (ON) / 8833 (OFF), with
    pre-flight against `api.openai.com` (expects 401 on a fake bearer)
    and per-mechanism breakdown filtered to `provider = 'openai'`.

  - **Provider-mapping reference doc.** New `docs/provider-mapping.md`
    captures the Claude Code ↔ Codex CLI surface mapping (wire endpoint,
    auth shapes, body envelope, tool defs, tool results, session ID
    extraction, cache mechanism, hooks, MCP, file telemetry, feature
    portability matrix) as a reusable template for future provider work.
    **Cursor column now filled** (this session, follow-up): hook-side
    rows from the existing `internal/adapter/cursor/` source-of-truth
    (5 hook events, conversation_id/generation_id session model,
    `~/.cursor/{hooks.json,mcp.json}` config, beforeFileRead coverage
    gap per audit C2); wire-shape rows marked `Opaque (proprietary)`
    since Cursor's backend is hardcoded into the binary with no
    user-controllable upstream URL — capture-driven verification of
    BYOK-mode routing tracked as a follow-up to confirm whether
    `ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` are honored when the user
    supplies their own keys. Gemini CLI / Cline / Copilot / Antigravity
    columns still TBD.

  - **Bonus production-bug fix in the scrubber.** `sensitiveKeyRE` and
    the JSON env-var-style key pattern were redacting any field with a
    `*_key` suffix — including codex's `prompt_cache_key` (the OpenAI
    prefix-cache lookup field). That would have broken OpenAI's prompt
    cache on every codex turn in production, regardless of the rest of
    the compression work landing. Tightened both regexes to require a
    recognized secret-prefix word (`API_KEY`, `ACCESS_KEY`,
    `PRIVATE_KEY`, `MASTER_KEY`, `ENCRYPTION_KEY`, `SIGNING_KEY`,
    `HMAC_KEY`) before treating `*_KEY` as sensitive. Bare suffixes
    like `prompt_cache_key`, `idempotency_key`, `partition_key`,
    `cache_key` now pass through. Pinned by
    `TestScrubPreservesNonSecretCacheLookupKeys` in
    `internal/scrub/scrub_test.go`. Existing
    `TestScrubString.secret_key_in_json` still covers the
    `SECRET_KEY` redaction path (matches via the SECRET prefix
    alternative).

  - **Config:** `RollingConfig.OpenAISummaryModel` (defaults to
    `gpt-5-nano`). Anthropic and OpenAI summary models are independent.

  - **Chat Completions (`/v1/chat/completions`) parity (this session,
    follow-up).** The Responses-API pre-pass chain now has a Chat
    Completions twin so non-codex OpenAI clients (Cursor / Cline / any
    Chat Completions–wired host) get the same compression treatment.
    New helpers in `internal/compression/conversation/openai.go`:
    `resolveOpenAIChatToolCalls` (back-fills toolName + filename onto
    role:"tool" messages by walking earlier assistant.tool_calls
    entries — the producing function's name + arguments live one
    message back, not on the tool message itself),
    `substituteOpenAIChatRedundantReads` (C16 read-cache via
    (filename, content-hash) tuple dedup; sessionID-gated, MCP-skip
    via HasPrefix `mcp__`), `compressOpenAIChatToolResults` (per-block
    per-type compression with the same MCP-skip and `len(out) >=
    len(body)` short-circuit), `stashOpenAIChatLargeBodies` (G31 with
    the same marker shape), `compressOpenAIChatToolDefinitions` +
    `compressOpenAIChatToolEntry` (description-tail trim + recursive
    `examples` strip — descends through the `function` wrapper since
    Chat Completions nests one level deeper than Responses API:
    `tools[].function.{name, description, parameters}` vs
    `tools[].name/.description/.parameters`). Non-function tool types
    (`web_search`, `mcp`, etc.) pass through unchanged so future tool
    types don't get accidentally mutated. `runOpenAI` rewired so the
    Chat Completions branch invokes the full pre-pass chain (same
    order: read-cache → per-type → tool-defs → stash → score+enforce)
    and the fast-path early-return fires when zero events fire,
    preserving OpenAI's `prompt_cache_key` cache hit on Chat
    Completions traffic the same way it does on Responses API. D20
    rolling summarisation rides through `summarizeIfThreshold` —
    same provider="openai" key as the Responses API path. Pinned by
    11 new tests in `openai_test.go` covering the resolver,
    per-type-fires + MCP-skip, stash + MCP-skip, tool-defs trim +
    non-function-skip, read-cache + sessionID-gate + MCP-skip, and
    the trivial-body fast-path byte-identity invariant.

- **Measured codex compression A/B — 30% cost reduction on a 4-prompt
  expressjs/express exploration workload.** Drove the harness against
  ChatGPT-Plus JWT codex against expressjs/express, single session per
  side, four prompts in sequence. Per `docs/codex-compression-ab-results.md`:

  | Metric | Compression ON | OFF | Delta |
  |---|---:|---:|---:|
  | API turns | 30 | 26 | +4 (+15%) |
  | Input tokens | 584,735 | 853,292 | **−268,557 (−31.5%)** |
  | Output tokens | 9,352 | 9,083 | within variance |
  | Cost USD | **$3.41** | **$4.88** | **−$1.46 (−30.0%)** |
  | Bytes saved (compression telemetry) | 22.9% | 0% | — |
  | Median per-turn latency | 6,691 ms | 7,792 ms | −14% (ON faster) |
  | p95 per-turn latency | 13,796 ms | 12,662 ms | +9% (within variance) |

  Per-mechanism distribution: text=84% / tools=14% / logs=8% / stash=4%
  / drop=0.3% / code=0.0%. All five mechanisms fired on real chatgpt-
  auth codex traffic; stash fired twice (small sample, not enough to
  validate retrieve-rate yet). C16 read-cache substitution did NOT fire
  on this workload — codex's `exec_command` `cat`-style file reads
  apparently didn't duplicate within a single request body. Worth a
  follow-up dig.

  **Honest framing:**
  - **NOT measured this run:** D20 (rolling-summ no-ops on JWT auth —
    routing to chatgpt.com/backend-api for summary calls is deferred);
    long-session retrieve-rate (2 stashes is not enough); cross-run
    variance (n=1 paired run, need 3-5 for noise bounds); MCP tool path
    (no server registered).
  - **Caveat — turn-count regression:** ON used 4 more API turns than
    OFF (+15%). Small in absolute terms; cost still net-positive
    despite the extra turns. Watch across more runs to see if the +15%
    delta is stable.

  Two production bugs surfaced + fixed during this dogfood (next
  bullet).

- **Two production-bug fixes surfaced during the codex A/B dogfood
  (this session, follow-up).**

  - **chatgpt.com Responses API SSE arrives with empty Content-Type
    header.** `isStream` detection in `internal/proxy/proxy.go`
    required a `text/event-stream` Content-Type prefix → SSE bodies
    from chatgpt.com (path-translated from `/v1/responses` to
    `/backend-api/codex/responses`) routed to the JSON-only
    non-streaming branch → `parseOpenAIResponse` failed to
    JSON-unmarshal the SSE body → the proxy returned a turn with
    `Model=""` and silently bailed. Net effect: every chatgpt-auth
    codex turn was silently dropped from cost/token telemetry — the
    compression-events panel showed activity but the cost columns
    stayed zero on the codex side, masking the 30% cost-saving
    measurement entirely. Fix: new `looksLikeSSE` content-sniff helper
    + post-read fallback in `proxy.go::serve` that reroutes to
    `buildStreamTurn` when the body looks like SSE despite a missing
    Content-Type. Pinned by `TestLooksLikeSSE` (10 cases),
    `TestParseSSEStream_ChatGPTFixture` (full-fixture parse), and
    `TestProxy_ChatGPTSSEWithEmptyContentTypeRoutedToStreamParser`
    (end-to-end via fake chatgpt.com upstream returning the captured
    fixture). The fixture lives at
    `internal/proxy/testdata/chatgpt_codex_responses_sse.bin` —
    captured from a real codex 0.129.0 + chatgpt.com session
    2026-05-08, 19 SSE events terminated by `response.completed`
    carrying `usage.{input_tokens, output_tokens,
    input_tokens_details.cached_tokens}`.

  - **Scrubber regex false-positive on Fernet-encrypted reasoning
    blobs.** OpenAI's Responses API ZDR-mode emits `encrypted_content`
    fields containing Fernet tokens (gAAA...== prefix, hundreds of
    base64 chars). The scrubber's existing global patterns —
    `gh[pousr]_[A-Za-z0-9]{20,}` (GitHub PAT),
    `(?i)(?:sk|pk|ak)[_-][A-Za-z0-9_]{16,}` (OpenAI/Stripe-style key),
    `AKIA[0-9A-Z]{16}` (AWS access key) — match SUBSTRINGS inside long
    base64 strings roughly every fourth Fernet token. When matched,
    the redacted substring corrupts the Fernet HMAC and the upstream
    returns `invalid_encrypted_content` on the second-and-later turn
    of any reasoning chain. Net effect: codex sessions errored out
    after ~15-25 turns on the ON side once enough reasoning context
    accumulated; OFF side unaffected because the scrubber only runs
    inside `Pipeline.finalize` which is gated on
    `cfg.Compression.Conversation.Enabled`. Fix:
    `shieldFernetEncryptedContent` in `internal/scrub/scrub.go`
    extracts every `encrypted_content` value to a regex-inert
    placeholder (`OBSERVERSHIELD<padded-N>ENDSHIELD`) before pattern
    application, restores after. Surrounding text (other JSON fields,
    other secrets that happen to appear outside encrypted_content) is
    still redacted normally — the shield is field-name targeted, not
    a global bypass. Pinned by `internal/scrub/encrypted_content_test.go`
    (3 tests: `TestScrubPreservesEncryptedContentValues` covering
    realistic + synthetic shapes, `TestScrubStillRedactsSecretsOutsideShield`
    proving the shield doesn't mask legit secrets, and
    `TestScrubHandlesMultipleEncryptedContentFields` for multi-blob
    bodies — codex sessions accumulate one `encrypted_content` per
    agent loop turn).

- **Codex A/B harness now enables `proxy.force_chatgpt_http` by
  default.** The flag (already shipped as a config knob; pinned by
  `TestProxy_ForceChatGPTHTTPRejectsWebSocketUpgrade`) makes the proxy
  return `426 Upgrade Required` for ChatGPT-backed WebSocket upgrade
  requests. Codex's CLI tries WebSocket first on ChatGPT-Plus JWT
  sessions and only falls back to HTTPS after 7 retries × ~15s ≈ 2
  minutes per session start; this flag short-circuits the cycle so
  codex falls straight to HTTPS (the path observer compresses) on the
  first try. No effect on Anthropic or sk-OpenAI traffic. Wired into
  `scripts/ab-codex-setup.sh` so `[proxy] force_chatgpt_http = true`
  ships in every per-side `observer-config.toml` the harness writes.
  Users running `observer start` outside the harness can opt in
  explicitly in their `~/.observer/config.toml`.

- **Codex parity NOT shipping (intentionally):**
  - **D23 post-compact injection:** codex has no compact event (no
    hook system), so there's no signal to inject around. Documented
    in `docs/provider-mapping.md` as "n/a — not portable".
  - **Cache-aware mode:** codex's Responses API has no block-level
    `cache_control` markers — `prompt_cache_key` is a single-shot
    session-keyed cache. The fast-path early-return preserves the cache
    hit; nothing else applies.

### Added

- **Tier 3.5 / dashboard observability bundle — Compression tab gains
  three new panels surfacing the v1.4.42/43 mechanisms that were
  previously invisible to the user (Tier 3.5; opt-in features remain
  opt-in, the cards just render zero state until enabled).** Pre-fix:
  K43's retrieval signals lived in their own table never queried by
  the UI; D23's post-compact injections mutated request bodies without
  emitting `compression_events` rows; D20 charged Haiku tokens against
  api.anthropic.com directly, bypassing the proxy, so `api_turns`
  couldn't see the spend. New users opening the Compression tab saw
  the same KPI tiles they saw in v1.4.39 even when half a dozen new
  mechanisms were firing. Post-fix: every shipped Tier 2/3 feature has
  a visible surface.

  **Three new panels on the Compression tab:**
  - **Reversibility — CCR retrieve rate** (G31 / K43): 4 KPI tiles
    (stashes, retrieves, retrieve rate, search hits) + side-by-side
    top-shas / top-actions tables. `retrieve_rate = retrieves ÷
    stashes`; > 100% reads naturally as "model returned to the same
    sha multiple times". Backed by `GET /api/compression/retrieval`.
  - **Compaction events** (D23): 4 KPI tiles (compactions, sessions
    affected, injections fired, inject rate) + per-event table with
    ghost-files / file-snapshot / injected pill. Migration 015 adds
    `injected_at` to `compaction_events`; the injector writes it
    idempotently the first time it builds non-empty content. Backed
    by `GET /api/compaction/events`.
  - **Rolling-summarisation net cost** (D20): 4 KPI tiles (summary
    calls, Haiku spend, cache-creation savings, net delta with
    ok/warn colour). Migration 016 adds `summary_calls`; the new
    `messagesummary.CallRecorder` interface + `DBRecorder` production
    impl writes one row per successful Haiku call, parsing Anthropic's
    response usage block for token counts and pricing them at insert
    time via the cost engine. Savings are priced at `cache_creation`
    (not `input`) since rolling-summ replaces bytes that would
    otherwise be re-cached on the next turn. Backed by `GET /api/compression/rolling-cost`.

  **Help drawer extended** with 5 new mechanism entries
  (`mechanism.read_cache`, `mechanism.stash`, `mechanism.tools`,
  `mechanism.rolling_summary`, `mechanism.compaction`) and 3 new
  metric entries (`term.retrieve_rate`, `term.compaction_events`,
  `term.rolling_cost_net`). Existing `mechanism.code` corrected — was
  describing the v1.4.39-and-earlier signature-only-skeleton
  behaviour, replaced with the four content-preserving v1.4.40
  transforms. `mechanism.logs` extended with the eight-pass pipeline
  + G32 level enrichment + E27 anomaly preservation.

  **Doc-drift fixes** — `CLAUDE.md`, `README.md`, `kickstart-prompt.md`
  references to "12 tools" updated to "13 always-on (+ retrieve_stashed
  conditional with stash)". Historical PROGRESS.md mentions left
  intact (accurate as-of-when-written).

  **Verification caveat — zero-state honest framing.** Every new card
  surfaces a metric whose load-bearing value is "is the feature
  actually helping?" Until dogfood data lands, the cards mostly show
  zeros — D20/D23 default-off; K43 always-on but stash itself is
  opt-in. The shipping bar is "the surface exists and renders
  correctly when data flows through", not "the metrics are meaningful
  on a fresh install". Once dogfood populates real values, follow-up
  CHANGELOG entries will report measured retrieve rates / inject
  rates / rolling-summ net deltas.

  **Implementation:**
  - `internal/db/migrations/015_compaction_injected_at.sql` (new),
    `016_summary_calls.sql` (new).
  - `internal/intelligence/compaction/postcompact.go` — `Injector.Get`
    now writes `injected_at` via `UPDATE ... WHERE injected_at IS NULL`
    (idempotent).
  - `internal/messagesummary/recorder.go` (new) — `DBRecorder` +
    `PricingLookup` + `Pricing` shape. `recorder_test.go` for the
    round-trip + nil-safe + unknown-model-zero-cost tests.
  - `internal/messagesummary/summarizer.go` — added `CallRecorder`
    interface, `SummaryCall` shape, `SummarizerOptions.{Recorder,
    SessionID}`, `extractSummaryAndUsage` (parses Anthropic's usage
    block alongside the assistant text), `Factory.For` threads
    sessionID into per-call options.
  - `internal/intelligence/dashboard/dashboard.go` — three new
    handlers (`handleCompressionRetrieval`, `handleCompactionEvents`,
    `handleCompressionRollingCost`).
  - `internal/intelligence/dashboard/static/index.html` — three new
    panels + JS renderers (`renderRetrievalCards`,
    `renderCompactionCards`, `renderRollingCostCards`).
  - `internal/intelligence/dashboard/static/help.js` — 8 new entries
    + 2 corrections.
  - `cmd/observer/proxy.go` — production wire-up:
    `messagesummary.NewDBRecorder` + `costEnginePricingAdapter`
    threaded through `messagesummary.NewFactory`.

  **Tests (all green):** 9 new tests
  (`TestAPICompressionRetrieval_HappyPath` + `..._EmptyWindowNoZeroDiv`;
  `TestAPICompactionEvents_HappyPath` + `..._EmptyWindow`;
  `TestAPICompressionRollingCost_HappyPath`;
  `TestDBRecorder_RecordsRow` + `..._NilSafe` + `..._UnknownModelZeroCost`;
  `TestInjector_SetsInjectedAtOnFirstFire` — the idempotency pin).
  Existing 39+ tests still pass — no regressions.

- **D20 rolling summarisation — live API wire-up (Tier 2;
  opt-in via `compression.conversation.rolling.enabled`).** v1.4.42
  shipped the framework; v1.4.43+ closes the loop with the
  production summariser, the proxy-side auth capture, the cross-
  turn-invariance design, and end-to-end wiring through
  `runAnthropic`. Long sessions that crossed Anthropic's context
  window had no answer pre-D20 — `cache_aware` extends *prefix life*
  but doesn't extend *conversation life*. Rolling summarisation
  replaces older messages with a `[<N> earlier messages summarized:
  <text>]` marker so the conversation can keep going long after the
  raw byte-count would have hit the cap.

  **Cross-turn invariance — sticky boundary (the cache-hit
  predicate).** Naïve summarisation re-summarises a slightly-longer
  prefix every turn → different summary text → different marker bytes
  → Anthropic prefix-cache miss → `cache_creation` charged on every
  turn (the v1.4.38 regression class re-introduced via the rolling
  layer). The fix is a per-session sticky boundary held in
  `Pipeline.rollingState`: once turn N summarises at boundary K, turns
  N+1 onward reuse the same summary + boundary unless the
  unsummarised tail grows past `2 × PreserveLastN`. Turn N+1's prefix
  bytes are then byte-identical to turn N's, hitting the cache. A
  stale-input safety check (hash of `msgs[:K]`) catches the rare path
  where a downstream rewrite changes the summarised content out from
  under us.

  **Production summariser — `internal/messagesummary/`:**
  - `AuthCache` — process-wide session_id → credentials map. The
    proxy's `serve()` writes one entry per Anthropic request before
    invoking the compressor; the rolling-summ summariser reads it
    when threshold fires. Soft cap at `auth_cache_size` (default
    1024) with drop-everything reset.
  - `AnthropicSummarizer` — calls `https://api.anthropic.com/v1/messages`
    with the captured Authorization (Pro/Max OAuth bearer) or
    `x-api-key`. Honours `anthropic-version`. 60s default timeout —
    long enough for a Haiku summary, short enough that a stuck
    request can't hang the parent proxy request indefinitely.
  - `Factory` — implements `conversation.SummarizerFactory` by
    combining the cache + summariser. Returns `nil` when no
    credentials are cached for the session, which makes the pipeline
    no-op the rolling pass for that session.
  - System prompt biases the model toward dense factual recall (file
    paths, decisions, errors, current goal) over narrative — the
    marker sits inline in a long conversation and dense bytes are
    more useful per token.

  **Proxy-side auth capture (`internal/proxy/proxy.go::serve`):** when
  an Anthropic request comes through with a session_id, the proxy
  captures `Authorization` and `x-api-key` headers into the
  `AuthCache` *before* invoking the compressor. Token rotation is
  picked up on the next regular request.

  **Pipeline wire-up:** new `RunInSessionContext(ctx, provider, body,
  sessionID)` threads the proxy request's context all the way down
  into the summariser HTTP call, so a cancelled parent request kills
  the in-flight summary too. Legacy `RunInSession(...)` delegates with
  `context.Background()` for backward compat.

  **Net-positive guard (the `len(out) >= len(body)` analogue):** when
  the marker is longer than the original messages it would replace,
  the summarisation result is discarded and the pipeline returns the
  original messages — so a verbose summariser response can never grow
  the body.

  **Disabled by default.** Honest framing on the cost side: the
  summary call costs Haiku tokens; the savings come from the avoided
  context blow-up + `cache_creation` premium on the regular request.
  Whether the net is positive depends on workload (long-session
  multi-tool exploration favours ON; short Q&A sessions favour OFF).
  Once dogfood lands measured numbers, the default may flip.

  **Implementation:**
  - `internal/compression/conversation/rolling.go` — `rollingState`
    type + sticky-boundary logic in `summarizeIfThreshold`.
  - `internal/compression/conversation/pipeline.go` — `Pipeline.
    rollingState` field, `RunInSessionContext`, runAnthropic
    integration.
  - `internal/messagesummary/{doc.go,auth_cache.go,summarizer.go}` —
    new package (3 files).
  - `internal/proxy/proxy.go` — `Options.AuthCache`, `AuthCache`
    interface, `AuthCredentials` shape, capture in `serve()`.
  - `internal/config/config.go` — `RollingConfig{Enabled,
    ThresholdTokens, SummaryModel, AuthCacheSize}` (defaults:
    enabled=false, threshold=80000, model=`claude-haiku-4-5`,
    auth_cache_size=1024).
  - `cmd/observer/proxy.go` — production wire-up:
    `messagesummary.NewAuthCache` →
    `messagesummary.NewFactory` → `pipeline.WithSummarizerFactory`,
    plus `authCacheAdapter` bridging the two `AuthCredentials` types.

  **Tests (all green):** 1 sticky-boundary cross-turn test
  (`TestSummarizeIfThreshold_StickyBoundaryAcrossTurns` — turn N
  fires, N+1 reuses, N+2 rebuilds when tail outgrows trigger);
  5 messagesummary auth-cache tests
  (round-trip / empty no-op / drop-at-cap / empty-session-Get /
  AuthCredentials.Empty); 6 messagesummary summariser tests
  (httptest.Server happy path with header + body assertions; api-key
  auth path; no-creds error; upstream 429 surfaces body; Factory
  returns nil for missing session, returns working summariser for
  present session, nil-cache no-op); 2 pipeline-level integration
  tests (`TestPipeline_RollingSummary_FiresOnAnthropic_E2E` — 25-msg
  body fires + sticky on second run; `..._NoOpUnderThreshold`).
  Existing 9 framework tests still pass (the sticky-boundary refactor
  is additive — single-turn behaviour is unchanged).

  **Pending follow-ups (not blocking):**
  - Cost-engine surfaces Haiku summary calls separately so the user
    can see "spent $X on summary calls vs $Y avoided on cache_creation".
  - Dogfood ≥1 long session (≥30 turns) measuring tokens-on-the-wire
    stays under context window.
  - Threshold tuning from real-workload data — the 80000-token default
    is a conservative starting point.

- **K43 self-learning feedback loop — `retrieval_signals` table +
  instrumentation on `retrieve_stashed` and `search_past_outputs`
  (Tier 3; always-on once a `SignalRecorder` is wired in `serve()`).**
  Every successful `retrieve_stashed({sha})` MCP call writes a
  `signal_type = 'retrieve_stashed'` row carrying the sha; every non-
  empty `search_past_outputs({query})` writes one `signal_type =
  'search_hit'` row per FTS5 hit carrying the originating query and
  the hit's `action_id`. Both writes are best-effort — losing a single
  signal row never fails an MCP tool call.

  **Why this is the closing move on the moat (per
  `docs/list-of-features-competition.md` cross-cutting observation 3):**
  the K43 aggregate is the input that lets the compression layer
  *learn* which content classes are load-bearing. CCR (G31) lets the
  compressor stash aggressively without losing data; K43 measures
  whether the stashed content is later retrieved — turning compression
  from a static heuristic into a feedback loop. Patterns that surface
  through the report ("tool_result bodies > 8KB containing `panic:` are
  retrieved 87% of the time → consider lowering the stash threshold for
  panic-shaped bodies") feed back into Tier 1 / Tier 2 tuning decisions.

  **`PatternMiner.Report({days})` aggregates over the lookback window:**
  - Per-type counts (`stash_retrievals`, `search_hits`).
  - `top_retrieved_shas` — top 10 shas by retrieve count (good
    candidates for a stash-bypass override: model keeps asking for
    them anyway).
  - `top_searched_actions` — top 10 action_ids by search-hit count
    (load-bearing tool outputs the model returns to repeatedly across
    sessions).

  **Migration `014_retrieval_signals.sql`** — new table, `action_id`
  nullable (stash retrievals don't have one), three indexes
  (`action_id`, `signal_type`, `signal_at`). No `ON DELETE CASCADE` —
  orphan rows after action deletion are harmless and we'd rather
  preserve the long tail for the pattern miner.

  **Implementation:**
  - `internal/intelligence/learn/signals.go` — `SignalRecorder` (write
    side) + `PatternMiner` (read side) + `RetrievalRateReport` /
    `ShaCount` / `ActionCount` shapes.
  - `internal/mcp/server.go` — new `Options.SignalRecorder` field +
    `SignalRecorder` interface (import-cycle-free).
  - `internal/mcp/tools.go::searchPastOutputsTool.Invoke` — logs one
    `search_hit` per returned hit.
  - `internal/mcp/tools_extra.go::retrieveStashedTool.Invoke` — logs
    one `retrieve_stashed` per successful retrieve (sha tracked in
    payload).
  - `cmd/observer/serve.go` — production wire-up:
    `learn.NewSignalRecorder(database)`.

  **Tests (all green):** 5 learn tests
  (`TestSignalRecorder_RecordRetrieveStashed` × 3 rows;
  `..._RecordSearchHit` — action_id round-trip via FK reference;
  `..._NilSafe` — nil receiver no-ops both Record methods;
  `TestPatternMiner_ReportAggregates` — 7 stashes / 4 hits / top sha +
  action ordering on a seeded set;
  `..._OutsideWindowExcluded` — 30-day-old row drops out of a 7-day
  window) plus 1 MCP integration test
  (`TestServer_RetrieveStashed_LogsK43Signal` — successful
  retrieve_stashed lands one signal row with the sha as payload).

- **D23 compaction-survival — proxy-side post-compact context injection
  (Tier 3; opt-in via `compression.conversation.compaction.inject_post_compact`).**
  Claude Code's `/compact` summarises the conversation and starts
  fresh — but the summary throws away tool-state (which files were
  read, which edits were made, which commands failed and why). The
  model effectively has to re-discover the project on the first turn
  after `/compact`. D23 closes that gap: when the proxy sees an
  Anthropic request for a session that has a recent `compaction_event`
  row, it prepends a synthetic system block carrying recovery context:

  ```
  <observer-compaction-recovery>
  Session sess-X underwent context compaction. Below is the most recent
  activity from before the compaction so you can re-orient without
  re-reading every file.

  Recently read files:
    - internal/proxy/proxy.go
    - internal/compression/conversation/pipeline.go
    ...

  Recently edited files:
    - internal/proxy/proxy.go
    ...

  Recent failures (in this session):
    - go test ./... — FAIL TestPipelineCacheAware: ...

  Project-specific learned rules (from prior sessions):
    - cargo build [build_failure] — failed 4 times, recovered 3 times
  </observer-compaction-recovery>
  ```

  **Cross-turn invariance — the cache-hit predicate.** The synthetic
  content is a pure function of (compaction_event timestamp, DB rows
  AT-OR-BEFORE that timestamp). All the underlying queries scope to
  `timestamp <= compactionAt` so post-compaction edits don't churn the
  block on subsequent turns. The proxy-side `Injector` caches per
  compaction event, so every turn of the post-compact conversation
  sees byte-identical injection — Anthropic's prefix cache hits hold.
  A new compaction event invalidates the cache and rebuilds.

  **Forbidden — same shape as the other compressors:** never mutates
  the `messages` array (only `system`), never strips existing system
  content (string `system` is lifted into a two-element array, array
  `system` is prepended-to), and the body is unchanged when content is
  empty (no compaction event, or no recovery data to surface).

  **Sections (limits chosen to fit a few hundred tokens):** last 10
  distinct read targets, last 5 distinct edit targets, last 3 failures
  from `failure_context`, top 5 `learn` rules for the session's
  project (`Days = 30`).

  **Disabled by default.** Once dogfood shows the model uses the
  injected context (vs. ignoring it) and cross-turn invariance holds
  on real long-session workloads, the default may flip.

  **Implementation:**
  - `internal/intelligence/compaction/postcompact.go` — new
    `BuildPostCompactContext(ctx, db, sessionID)` builder + `Injector`
    with per-compaction-event cache.
  - `internal/proxy/postcompact_inject.go` — new
    `injectAnthropicSystemBlock(body, content)` envelope mutator
    handling all three `system` field shapes (missing, string, array)
    + a deterministic-key `marshalEnvelope` helper.
  - `internal/proxy/proxy.go::serve` — runs injection after zstd
    decode but before compression, so the compressor sees the
    injected body. When compression skips (zero events fire) but
    injection happened, forwards the injected bytes upstream rather
    than the pre-injection original (with zstd re-encode where
    needed).
  - `cmd/observer/proxy.go` — wires `compaction.NewInjector(db)` into
    `proxy.Options.PostCompactInjector` when the config flag is set.

  **Tests (all green):** 4 builder tests
  (`TestBuildPostCompactContext_HappyPath` — all four sections
  populated and wrapped in the recovery envelope; `..._NoCompactionEvent`
  — empty for fresh sessions; `..._Determinism` × 5; `TestInjector_*` —
  cache reuse + invalidation on new compaction event + empty-session
  no-op) plus 7 envelope-mutation tests
  (`TestInjectAnthropicSystemBlock_NoSystemField` /
  `_StringSystem` / `_ArraySystem` — three input shapes;
  `_PreservesMessages` — messages array passes through bytewise;
  `_EmptyContentNoOp` — empty content returns body unchanged;
  `_Determinism` × 25; `_BadJSON` — non-JSON surfaces error).

- **G33 three-layer progressive disclosure — new `list_actions_around`
  MCP tool (Tier 3; always-on).** Inserts a chronological middle layer
  between `search_past_outputs` (FTS5 keyword hit on tool output) and
  `get_action_details` (full row + excerpt body). The model's previous
  two-call workflow forced an all-or-nothing choice at the FTS5 hit:
  either pay for the full body of every adjacent action via repeated
  `get_action_details` calls, or guess at session timeline shape from a
  single hit. The new tool returns ±N actions adjacent to a pivot
  `action_id` within the same session, with summary fields only (id,
  timestamp, tool, action_type, target, success, freshness, position).

  **Tool spec:** `list_actions_around({action_id, before?: int,
  after?: int})`. `before`/`after` default to 5, capped at 20 each.
  Output rows carry `position: "before" | "target" | "after"` and are
  ordered chronologically (the target row sits in the middle of the
  array). Same-session-only — neighbour windows do not surface
  unrelated actions from other sessions that happened to overlap.

  **Tie-break by id at identical timestamps** so two actions with
  microsecond-equal timestamps return in deterministic order.

  **Found=false on missing action_id** — no error, just an empty
  `actions` array. The model can distinguish "wrong id" from "first
  action in session" via the empty-but-found-true vs empty-but-
  found-false response.

  Four new tests
  (`TestServer_ListActionsAround_HappyPath` — chronological order
  and position labels on a 11-action seed, target at index 5;
  `..._NotFound` — bogus action_id returns found=false;
  `..._AtSessionBoundary` — first-in-session has empty before window;
  `..._DefaultsAndCaps` — 5/5 default, 20/20 cap on a 50-action seed)
  plus the existing tool-count smoke test bumped from 12 → 13.

- **E27 anomaly preservation — FATAL/ERROR/PANIC lines lift out of the
  elided middle through `LogsCompressor` head+tail truncation (Tier 3;
  always-on once `LogsCompressor` runs).** Pre-fix the head+tail
  truncation pass at the end of `LogsCompressor` could elide a load-
  bearing failure signal — a `FATAL: out of memory` line in the middle
  of a long log would vanish into the `… [N lines elided]` marker even
  though it was the single most useful line in the body. Post-fix
  `headTailWithLevelStats` lifts anomaly-shaped lines out of the
  elided range and inserts them right after the marker, so the model
  sees the failure inline:

  ```
  starting up
  ready
  … [10 lines elided: 5 ERRORs, 3 WARNs, 2 INFO; preserved 3 anomalies]
  FATAL: out of memory at allocator.go:42
  panic: runtime error: index out of range [7] with length 5
  Caused by: storage backend offline
  final-1
  final-2
  ```

  **Anomaly pattern (deterministic regex):** severities `FATAL`, `ERROR`,
  `PANIC` (matched at line start or after `[`/space, mirroring
  `logLevelPattern`); plus line-start anchors `^Caused by:` and
  `^panic:`; plus `exit (?:status|code) [1-9]` for non-zero process
  exits. Stack-frame heuristics (`^\s+at\s+`) deliberately deferred —
  the lift-out is conservative on purpose and pairs with the
  load-bearing severities, not the surrounding context.

  **Cap at 20 preserved anomalies per truncation** — pathological
  middles (e.g. retry-storm `ERROR` floods) can't blow up the post-
  truncation body. The cap is the same shape as the head/tail line
  budgets: bounded lift-out, never larger than the input.

  Marker form (deterministic, depends only on body bytes):
  - `[N lines elided]` — bare form, no levels and no anomalies.
  - `[N lines elided: 3 ERRORs, 12 WARNs]` — G32 level-count form.
  - `[N lines elided: 3 ERRORs; preserved 5 anomalies]` — E27 form when
    anomalies are lifted; the lifted lines themselves follow inline.

  Three new tests pin the behaviour
  (`TestLogsCompressor_AnomalyPreservation_LiftsFatalOutOfElidedMiddle`,
  `..._NoAnomaliesIsBareMarker`,
  `..._CapsAt20`) plus the existing
  `TestLogsCompressor_ElisionMarkerCountsLogLevels` and
  `TestLogsCompressorClampsLargeInput` continue to pass — the marker
  shape change is additive (suffix-only).

- **C16 read-cache auto-substitution — per-call dedup of redundant
  Read tool_results (Tier 2; session-gated, always-on when session_id
  is present).** When the model re-Reads a file with bytewise-
  identical content, the second and subsequent occurrences of the
  Read tool_result are replaced inline with a deterministic marker:

  ```
  [file /repo/main.go unchanged since earlier in this turn; same content already returned]
  ```

  **Per-call dedup design:** Anthropic's request body includes the
  entire conversation history every turn, so when the model re-Reads
  a file in turn N+1, both turn N's and turn N+1's Read tool_results
  appear in the same `Pipeline.Run` call. `substituteRedundantReads`
  hashes (filename, line-strip-normalised body) per Read block and
  collapses second-and-subsequent occurrences. No SQLite, no in-
  memory cross-call cache, no session-state plumbing — the per-call
  scope is exactly the right semantic.

  **Safety properties (pinned by 4 new tests):**
  - **Read-only:** Bash / Grep / Glob with identical command output
    (which is content-unstable across re-runs) does NOT collapse.
    `tool_use.name == "Read"` is the gate.
  - **Hash-aware:** when the same path appears with different content
    (file edited between reads), C16 does NOT collapse — the model
    needs to see both states.
  - **Session-gated:** disabled when `sessionID == ""` so legacy
    `Pipeline.Run()` callers and unit tests preserve the previous
    behaviour.
  - **Cache_control-respecting:** honours `preserveMsg`/`preserveBlock`
    so the SDK's cache marker block stays bit-identical.
  - **Never grows:** net-shrink short-circuit at the per-block scope.

  Wired via the new `Pipeline.RunInSession(provider, body, sessionID)`
  entry point + `proxy.SessionAwareCompressor` interface extension.
  Production `proxy.serve` extracts the Anthropic SDK's `session_id`
  from `metadata.user_id` and threads it through; the legacy
  `Pipeline.Run()` and `proxy.Compressor` interface are kept for
  backward compat.

- **D20 rolling summarisation — framework + cache + tests (Tier 2;
  live API integration deferred to a follow-up).** New `Summarizer`
  interface, `SummarizerFactory` per-session lookup,
  `Pipeline.WithSummarizerFactory(factory, thresholdTokens)` setter,
  `EstimateTokens` heuristic (chars / 4), `summaryCache` (sha256-keyed
  in-memory dedup with drop-everything-at-cap eviction),
  `Pipeline.summarizeIfThreshold` helper that gates on every safety
  condition (no factory / no session / under threshold / no
  summariser for this session / Summarize error / marker doesn't
  shrink the prefix).

  Marker form (deterministic): `[<N> earlier messages summarized: <text>]`.

  9 new tests cover the estimate, cache round-trip + cap eviction +
  determinism, mock-summariser happy path, every no-op gate, error-
  fallback, cache-hit avoid-upstream.

  **Live wire-up pending follow-up commit** — substantial scope:
  - Operate on `extracted` (parsed JSON messages) so the serializer
    can replace K original messages with 1 marker.
  - Cross-turn invariance design: naïve summarisation re-summarises a
    slightly-longer prefix every turn, busting Anthropic's prefix
    cache. Need session-scoped sticky boundary (turn N+1 reuses turn
    N's summary unless tail grew past `2 × PreserveLastN`).
  - Production Anthropic API client wired to the per-session auth
    cache (the proxy captures Authorization headers per session_id;
    the Summarizer factory pulls them out for the actual API call).
  - Cost tracking — Haiku summary calls billed separately; honest
    framing on cost engine net-delta.
  - Config struct.

- **Format-aware shell-output parsing (Tier 1 / B12; cargo_test_json
  shipped, pytest + npm deferred to a follow-up landing).** New rule
  type `format_parse` in `internal/compression/shell/`. The first
  parser handles libtest's NDJSON output from
  `cargo test -- --format json`: parses each JSON line as a libtest
  event, accumulates pass/fail/ignored/filtered counts, and on Flush
  emits a compact summary:

  ```
  cargo test summary: 247 passed, 3 failed, 1 ignored
  
  failures (3):
    - foo::a::handles_empty_input
        thread 'foo::a::handles_empty_input' panicked at src/foo.rs:42:5
        assertion failed: result.is_some()
    - foo::b::respects_timeout
        ...
    - foo::c::reads_metadata
        ...
  ```

  Fall-through contract — **never load-bearing**: when the buffered
  content doesn't parse as the declared format (non-JSON output, the
  `--format json` flag wasn't passed, or the stream was truncated
  mid-run), the rule emits the buffered lines verbatim. The model
  never silently loses content; format-aware parsing is a yield
  optimisation that can degrade safely to no-op.

  `failure_only` flag piped through `RuleSpec` and `FormatParse`
  constructor (B13 piggy-back) — currently a signature placeholder
  for cargo since per-test detail isn't emitted for passes; richer
  formats (pytest, npm) will use it.

  Cargo TOML default split into two specs: the existing generic
  `cargo` filter (drops `Fresh` lines, squashes whitespace) and a new
  subcommand-targeted `cargo test` filter that runs the
  `cargo_test_json` parser. Pinned by 6 unit tests
  (`TestParseCargoTestJSON_*` + `TestFormatParse_*`) covering the
  happy path, non-JSON fall-through, truncated-stream fall-through,
  determinism × 50 with alphabetical failure ordering, fall-back on
  unparseable buffered content, and unknown/missing format errors at
  construction.

  **Deferred:** pytest_json (libtest's `pytest-json-report` plugin
  shape; needs real-fixture sourcing for the assertion-shape
  extraction) and npm_test_json (jest/mocha dialects). Both follow
  the same framework with format-specific summarisers; ~1 hour each,
  not blocking the rest of Tier 1. Tracked in
  `docs/compression-roadmap.md`.

- **`LogsCompressor` truncation marker enriched with log-level counts
  (Tier 1 / G32 — LogsCompressor side).** When the head+tail
  truncation pass elides a range that contains recognisable log
  levels, the marker shape switches from the bare
  `… [N lines elided]` to the enriched form:

  ```
  … [347 lines elided: 12 ERRORs, 47 WARNs, 288 INFO]
  ```

  Lets the model decide whether to retrieve the elided range based
  on what's in it (e.g. an elided range with 12 ERRORs is much more
  likely to be load-bearing than one with 288 INFO lines). New
  helpers `countLogLevels`, `formatLogElisionMarker`, and
  `headTailWithLevelStats` in `internal/compression/conversation/logs.go`
  recognise ERROR/FATAL → ERRORs, WARN/WARNING → WARNs, INFO,
  DEBUG/TRACE → DEBUG. Falls back to the bare form when no levels
  appear — the enrichment is opportunistic, not load-bearing. Two
  new tests (`TestLogsCompressor_ElisionMarkerCountsLogLevels`,
  `TestLogsCompressor_ElisionMarkerBareWhenNoLevels`).

  CCR-side enrichment (stash markers carrying format-shape signal —
  `[output 47KB stashed: cargo test run, 247 passed 3 failed at retrieve_stashed("<sha>")]`)
  is deferred to a follow-up alongside the pytest/npm parsers; needs
  the same per-format detection infrastructure.

- **CCR (Compressed Content Retrieval) — disk-stash + retrieve_stashed
  MCP tool — the strategic moat (Tier 1 / G31; opt-in via
  `compression.conversation.stash.enabled`).** Tool_result bodies whose
  size exceeds the inline-threshold (default 8 KB; configurable) after
  per-type compression are written to a content-addressed on-disk stash
  and replaced inline with a deterministic marker:

  ```
  [output 47KB stashed at observer://stash/<sha>; use retrieve_stashed]
  ```

  The model retrieves originals via the new MCP tool
  `retrieve_stashed({sha, max_bytes?})`, which validates the on-disk
  bytes re-hash to the requested sha (catches partial writes / manual
  tampering) and supports an optional truncation cap so the model can
  pull just the head of a multi-MB blob.

  **Why this is the moat (per
  `docs/list-of-features-competition.md` cross-cutting observation 2):**
  pure-filter tools (RTK, snip) hit a quality ceiling because lossy
  compression eventually drops content the model needs. CCR is
  reversible — nothing is actually thrown away; the marker is a
  compress-then-retrieve indirection. This lets us compress
  aggressively (large bodies always stash, regardless of dedup-friend-
  liness) without ever forcing the model to re-run a tool because we
  truncated mid-content.

  **Implementation:**
  - New package `internal/stash/` — content-addressed flat-layout
    storage at `~/.observer/stash/<sha>`. Atomic write (temp file +
    rename), idempotent on re-write, sha-validated on read,
    LRU-mtime-eviction GC capped at `max_total_mb` (default 256 MB).
    9 unit tests (round-trip, idempotence, missing/invalid/corrupt
    sha, GC eviction, GC no-op when under cap, dir-on-first-write).
    Path-traversal guard via 64-char-lowercase-hex sha validation.
  - `Pipeline.WithStash(s, threshold)` — opt-in attachment that runs
    `stashLargeBodies` after `compressToolResults` and
    `compressToolDefinitions`. Honours the existing
    `preserveMsg`/`preserveBlock` cache_control protection. Emits
    `compression_events.mechanism = "stash"` per stashed block.
  - `mcp.Options.Stash` — opt-in registration of the
    `retrieve_stashed` tool. Without it, tools/list excludes the
    tool (pinned by `TestServer_RetrieveStashed_NotRegisteredWithoutStash`).
  - Production wire-up in `cmd/observer/proxy.go` (proxy daemon) and
    `cmd/observer/serve.go` (MCP server) reads
    `compression.conversation.stash.{enabled,dir,threshold_bytes,max_total_mb}`
    and constructs the stash conditionally. `~/`-expansion is applied
    in `config.Load`.

  **Cross-turn invariance — pinned by
  `TestPipeline_StashCrossTurnInvariance`:** the marker depends only on
  body bytes (sha + size-in-KB), not on call ordering or wall-clock,
  so the same body in turn N and turn N+1 produces byte-identical
  markers. Anthropic's prefix cache hits stay intact across turns
  even when the body is stashed.

  **Verification caveat — yield + retrieve-rate claims.** Five new
  pipeline tests
  (`TestPipeline_StashFires`,
  `TestPipeline_StashSkipsBelowThreshold`,
  `TestPipeline_StashCrossTurnInvariance`,
  `TestPipeline_StashDisabledByDefault`)
  + 5 MCP-side tests pin the structural behaviour. The dogfood-data-
  driven retrieve-rate (`% of stashed bytes ever retrieved`) and the
  threshold-tuning numbers will be reported in a follow-up CHANGELOG
  entry once we have a week of usage. Default-on flip is gated on
  retrieve-rate landing in a healthy range.

- **Tool/function schema compression at the request envelope level
  (sub-feature 2 of the v1.4.40 honest-defaults bundle; cold-cache
  effect only — see verification caveat).** The Anthropic Messages API
  request body carries a `tools` array on every turn (Claude Code SDK
  ships ~45 tools, ~94 KB); on cache-cold turns or after the prompt-
  cache TTL elapses the entire array is paid for in full. New
  `compressToolDefinitions` helper in
  `internal/compression/conversation/anthropic.go` mutates
  `envelope["tools"]` in place with two strict-content-preserving
  transforms applied per tool:
  1. **Top-level description trim** — when `description` has ≥ 3
     paragraphs (split on a blank line), keep only the first 2. The
     first 2 paragraphs reliably carry the "what the tool does" +
     "primary parameters" content; later paragraphs typically repeat
     usage hints already encoded in the input schema.
  2. **Deep `examples` strip from `input_schema`** — `examples` is a
     JSON-Schema informational keyword. Removing it does not change
     validation behaviour, parameter names, types, required-ness,
     enums, or numeric bounds. The model loses sample values but
     retains every other signal the parameter schema carries.

  **Forbidden — pinned by `TestCompressToolDefinitions_NeverTouchesParameterSchemaContract`:**
  tool `name`, `input_schema.type`, `input_schema.properties` keys,
  nested `type`/`description`/`required`/`enum`/`minimum`/`maximum`,
  any field outside `description` and `input_schema`. Touching
  parameter schemas changes the model's tool-use behaviour and re-
  introduces a regression equivalent to v1.4.38's turn-count loss
  for tool calls.

  Wired into `pipeline.go::runAnthropic` alongside
  `compressToolResults`; events join `br.Stats.Events` so the fast-
  path early-return correctly skips when tools-only compression fires.
  Net-shrink short-circuit at the envelope-tools scope mirrors the
  per-body `len(out) >= len(body)` rule: if compressed tools aren't
  smaller than original, the envelope is restored unchanged.

  **Verification caveat — yield claims.** Five new tests
  (`TestCompressToolDefinitions_StripsExamplesAndTrimsDescription`,
  `..._Idempotent`, `..._NoOp`, `..._NeverTouchesParameterSchemaContract`,
  `TestPipeline_ToolSchemaCompressionFires`) pin the structural
  behaviour. The tools array is part of Anthropic's cached prefix once
  warm, so this compression's value is realised only on cache-cold
  turns (first turn of a session, or after the cache TTL elapses).
  Real-workload yield numbers from the v1.4.40 A/B re-run will be
  reported in a follow-up CHANGELOG entry — until then the cost-savings
  claim is structural (fewer bytes go up cold) rather than empirical.

- **`LogsCompressor` extended with five new content-preserving filters
  (sub-feature 1 of the v1.4.40 honest-defaults bundle; live A/B
  re-run pending).** The pre-existing dedup-only behaviour was leaving
  ~70 % of real Bash / npm / cargo / pytest tool_result bytes on the
  table because ANSI escapes, progress-bar updates, padding-only blank
  runs, and tabular-output column gutters all survived intact. The new
  pipeline runs (in order):
  1. **ANSI CSI escape strip** — `\x1b\[[0-9;?]*[a-zA-Z]` covers SGR
     colour codes, cursor moves, and line-erase sequences. Fast-path
     skips the regex entirely when the body contains no `0x1b` byte.
  2. **Carriage-return overwrite collapse** — per line, `prefix\rmid\rfinal`
     → `final`. Progress bars and spinners shrink to their final
     state.
  3. **Trailing-whitespace trim per line.**
  4. **PowerShell wide-padding squash** — runs of ≥ 4 spaces collapse
     to a single space, but only on lines with ≥ 3 such runs. The
     ≥ 3-run threshold is the conservative sweet spot: it catches PS
     `Get-*` cmdlet table output (which has 3 + column gutters per
     data row) without corrupting `ls -l` style output (which has 1
     wide gutter per line). Pinned negatively by
     `TestLogsCompressor_LeavesLsAlone`.
  5. **Progress-bar line drop** — drops lines whose visible content is
     only padding / progress glyphs (`[=\->%#*.[\]<>█▓▒░]`) AND length
     ≥ 6, so Markdown-style short dividers (`---`, `===`) survive.
  6. **Run-length dedup of consecutive identical non-blank lines** —
     refactored from the existing `dedupConsecutive` so blank lines
     now act as run boundaries (avoiding the ugly ` [×N]` marker on
     pure-whitespace runs) and `capBlankRunsAt(2)` runs as a separate
     pass.

  **Forbidden / deferred — same regression-class guards as
  CodeCompressor:** no truncation, no head-tail elision (the existing
  truncate pass at the end is unchanged and still bounded by
  `MaxLines`), no fuzzy line collapse. The originally-planned
  near-identical run-length collapse (`fetched a@1.0.0` / `fetched
  b@1.0.0` → `fetched <pkg>@<ver> ×N (a, b, ..., z)`) is **deferred**
  to a follow-up because the deterministic-marker requirement (every
  cross-turn output must list the same set of differing tokens
  byte-identically) makes the heuristic materially riskier than the
  other five — it will land alongside Tier 1's B12 format-aware test-
  runner parsers (`docs/compression-roadmap.md`).

  **Verification caveat — yield claims.** Eleven new unit tests
  (positive + negative per filter, plus determinism × 50, idempotence,
  no-ANSI fast-path no-op) pin the structural behaviour; the live A/B
  re-run on the `/tmp/ab-claude/{on,off}/` 4-prompt workload has not
  yet been executed. Per-filter yield on synthetic fixtures: 10–30 %
  for ANSI strip on coloured `pytest`/`cargo` output, 20–40 % on PS
  Get-* tables, 5–15 % on install/build progress lines. Real-workload
  `Bytes saved %` on Claude Code Bash tool_results targeting `npm`,
  `cargo`, `pytest`, `kubectl` will be reported in a follow-up
  CHANGELOG entry once the A/B is re-run with the v1.4.40 bundle.

- **Content-preserving `CodeCompressor` rewrite + `code` enters default
  `compress_types` allow-list (yield is structural, post-rewrite live
  A/B not yet run — see verification caveat).** Replaces the previous
  signature-keep / body-elision strategy in
  `internal/compression/conversation/code.go` (which was the regression
  class v1.4.38 + v1.4.39 spent two releases ring-fencing) with four
  strict-content-preserving transforms, applied in order:
  1. Trim trailing whitespace per line.
  2. Elide a leading license/banner block — only at file top, only when
     the contiguous run of comment-only (or blank) lines is ≥ 5 lines
     AND contains ≥ 3 actual comment lines. Trailing blanks are trimmed
     off the elided range so the visual break before the first code
     line is preserved. Replaced by a single
     `// [<N>-line license header elided]` marker. Python `"""`
     docstrings are deliberately NOT recognised — they are string
     literals with module-level semantics (`__doc__`), not comments.
  3. Run-length dedup of consecutive identical non-blank lines into
     `<line> [×N]`, mirroring `LogsCompressor.dedupConsecutive` so the
     marker form is consistent across compressors. Yield is near-zero
     on hand-written source, 30–60 % on generated/templated files.
  4. Cap consecutive blank-line runs at 2.

  **Forbidden — these are the regression class:** body elision, mid-
  file comment stripping, signature-only skeleton. Every non-comment,
  non-blank line of the input now appears bytewise in the output. The
  test `TestCodeCompressor_PreservesEveryNonCommentNonBlankLine` is the
  unit-level guard that would have caught the v1.4.38 turn-count
  regression.

  Defaults flip: `internal/config/config.go::Default()` now ships
  `compress_types = ["json", "logs", "code"]`, up from
  `["json", "logs"]`. The `code` allow-list entry is justified by the
  rewrite's content preservation; reverting it is a product decision
  and is regression-guarded by `TestDefaultCompressTypesIncludesCode`.

  **Verification caveat — yield claims.** The rewrite is structurally
  correct (29 unit tests across determinism, idempotence, license-
  header positive/negative, mid-file negative, generated-code dedup,
  trailing-whitespace trim, blank-run cap, never-grows-body, plus
  pipeline-level integration `TestPipeline_CodeCompressionFiresOnReadToolResult`),
  but the live A/B re-run on the `/tmp/ab-claude/{on,off}/` 4-prompt
  workload has not yet been executed for this change. Per-file yield
  on synthetic fixtures: 5–15 % on header-bearing files, ~1–3 % on
  bare source, 30–60 % on generated code. Real-workload `Bytes saved %`
  on Claude Code Read tool_results targeting `.go` / `.py` / `.js`
  files will be reported in a follow-up CHANGELOG entry once the A/B
  is re-run with the v1.4.40 bundle.

- **Per-type compression detection improved for Claude Code tool
  envelope shapes (effect on real workloads is currently zero — see
  caveat below).** Pre-fix `types.Detect` returned `Unknown` on every
  Claude Code tool envelope because (a) Read tool output is line-
  numbered (cat -n style) so structural sniffing fails, (b) every
  caller passed an empty filename so extension-based routing never
  fired, and (c) Bash output shapes weren't recognised by the log-line
  classifier. Three detection paths now light up correctly in unit
  tests and on synthetic fixtures:
  1. **Line-numbered Read tool output is unwrapped before
     classification.** New exported `IsLineNumbered` + `StripLineNumbers`
     helpers strip the `<digits>\t` prefix from each line; `types.Detect`
     classifies the underlying content. `compressToolResults` also
     strips line numbers before invoking the compressor. Trade-off
     accepted: compressed Read output loses line numbers (model can
     re-Read for line numbers if `Edit` needs them later).
  2. **Producing-tool filename hints flow through to `types.Detect`.**
     `resolveToolUseInputs` pre-walks the request body, builds a
     `tool_use_id → {Name, FilePath}` index from every `tool_use`
     block's `input.file_path`, and back-fills
     `extractedMessage.resultFilenames` + `resultToolNames` parallel
     to `resultBlockIdxs`. The per-block compression loop passes the
     matching filename to `types.Detect`.
  3. **Bash output shapes (ls -la, grep -n, find, wc -l) now classify
     as `Logs`** via a new `shellOutputPattern` regex in
     `types/detect.go`.

  **Real-workload caveat (the honest part).** With the conservative
  default `compress_types = ["json", "logs"]`, the new detection
  paths produce **zero observable byte savings on Read/Bash-heavy
  code-exploration workloads** because:
  * Read tool_results that classify as `Code` (the common case for
    `.go`/`.js` files) are filtered by the allow-list — `code` is
    excluded by default for the same regression-class reason `text`
    is excluded (the existing `CodeCompressor` elides function bodies,
    which would re-introduce the v1.4.38 turn-count regression on
    prompts that need to re-reference function bodies).
  * Bash tool_results that classify as `Logs` are typically too short
    (5-line `ls -la`, 3-line `grep`) for `LogsCompressor`'s dedupe +
    head/tail strategy to shrink them — the `len(out) >= len(body)`
    short-circuit fires.
  * Mixed Bash output (`cat package.json && ls`) doesn't sniff as
    JSON because the leading bytes aren't `{`/`[`.

  Net live A/B on Express repo, post-fix: zero compression events
  recorded, `Bytes saved % = 0%`. The detector improvements are
  wired correctly and have unit-test coverage; they remain latent
  until either (a) a content-preserving `CodeCompressor` ships and
  `code` enters the default allow-list, or (b) workloads shift toward
  longer Bash outputs / structured-data-file Reads.

  Synthetic-fixture verification
  (`TestPipelineCacheAware_RealClaudeCodeShapes`,
  `TestDetect_LineNumberedReadOutput`,
  `TestDetect_BashOutputShapes`,
  `TestDetect_DiffShapes`,
  `TestResolveToolUseInputs_FilenamePlumbing`,
  `TestStripLineNumbers`): JSON compression fires on a synthetic
  line-numbered package.json (163 → 132 bytes, 19% saved on the
  touched block), pipeline remains deterministic across two runs,
  cross-turn invariance still holds on real Claude Code shapes.
  Diff detection covers line-numbered `.diff`/`.patch` Read output
  and bare `git diff` Bash output transparently via the strip-and-
  route at the top of `Detect`.

- **`compression.conversation.mode = "cache_aware"`: new strategy
  for Anthropic Pro/Max sessions where the SDK already places
  `cache_control` markers (cost-savings claim is structural, not
  empirical — see verification caveat below).** Skips the drop pass
  entirely (drop ranking is budget-relative and shifts as the
  conversation grows, which invalidates Anthropic's prefix cache
  turn-over-turn), narrows per-type compression eligibility to
  `Role == RoleTool` (RoleAssistant flips Preserved at the
  PreserveLastN boundary; preserving it bit-stable keeps cache hits
  intact), and skips `cache_control` injection (the SDK already sets
  markers; injecting a 4th is wasteful or rejected). No-ops
  gracefully (effectively ModeToken without drops) when no SDK
  marker is present in the request body. New `findLastCacheBreakpoint`
  helper in `internal/compression/conversation/anthropic.go` walks
  messages-level `cache_control` annotations (system-level markers
  ignored). New `BudgetOptions.CacheAware` + `OriginalBodyBytes`
  fields in `budget.go` propagate the mode into `Enforce`. Determinism
  and cross-turn invariance unit tests
  (`TestPipelineCacheAware_Determinism`,
  `TestPipelineCacheAware_CrossTurnInvariance`) pin the load-bearing
  properties: same input → byte-identical output, and turn N+1's
  output shares a byte-identical prefix with turn N's output (the
  cache-hit predicate). Default `Mode` flipped from `"token"` to
  `"cache_aware"`.

  **Verification caveat — cost claims.** Four live A/B runs on the
  Express repo (identical 4-prompt workload, symmetric on both
  sides) showed cost deltas spanning **-14.7% to +14.8%** ON-vs-OFF.
  Three of the four post-HTML-escape-fix runs favoured ON (+14.8%,
  +6.1%, +6.1% directionally), but the variance is wider than any
  reproducible signal. Critically, **zero compression events fire on
  these workloads** (per the caveat in the detection entry above) and
  the fast-path makes ON forward byte-identical bytes to OFF when
  nothing compresses — which means `cache_aware`'s structural
  behaviour (no drops, no injection) has no observable bytes-on-the-
  wire difference from `compression.enabled = false`. The cost
  variance we measure is dominated by cascade non-determinism (same
  prompts, temperature > 0, different tool decisions, different turn
  counts) rather than anything compression did. **No measurable
  cost-savings claim is supported by current evidence**; the value
  of `cache_aware` mode is *latent* — it makes future compressor
  work safe to ship without re-introducing the v1.4.38 turn-count
  regression class, but until compressors actually fire (see the
  detection-entry caveat for blockers), the mode is functionally
  equivalent to compression-disabled on common code-exploration
  workloads.

- **Per-session `api_turns.session_id` for Claude Code proxy traffic.**
  Pre-fix every proxy row from Pro/Max OAuth Claude Code carried
  `session_id=NULL` because no `SessionStart` hook fires for the
  launcher path. The Claude Code SDK 2.1+ already embeds a stable
  per-session UUID inside `metadata.user_id` (a JSON-encoded blob of
  the form `{"device_id":...,"account_uuid":...,"session_id":"<uuid>"}`)
  on every `/v1/messages` POST. New `extractAnthropicSessionID` helper
  in `internal/proxy/provider.go` parses the blob defensively
  (returning `""` on any malformed JSON or missing field).
  `proxy.go::serve` consults it first for `provider == "anthropic"`,
  falling through to the existing `X-Session-Id` header and
  `SessionResolver` paths so non-Claude-Code Anthropic clients aren't
  affected. 7-case unit test on the helper plus 2 end-to-end proxy
  tests pin the body→DB column flow and the header-fallback path.
  Live verification: a fresh `observer claude --print` turn lands
  `api_turns.session_id` populated with the SDK's own UUID, matching
  across every turn of the same invocation. Replaces the launcher-
  injection plan from the v1.4.38 handover — the value was already on
  the wire.

- **`observer claude` launcher: closes the Pro/Max OAuth bypass.**
  Pro/Max-OAuth Claude Code (2.1+) reads
  `~/.claude/.credentials.json::claudeAiOauth.accessToken` and sends
  Bearer tokens straight to `api.anthropic.com`, bypassing
  `ANTHROPIC_BASE_URL` for the `/v1/messages` chat call — i.e. the
  observer proxy never sees the request, compression doesn't run, and
  no `api_turns` rows land for the OAuth majority. The bypass is
  conditional, not absolute: when the OAuth bearer is exposed via
  `ANTHROPIC_AUTH_TOKEN`, Claude Code falls into its API-key code
  path, which DOES respect `ANTHROPIC_BASE_URL`. Same Bearer header
  on the wire, same Pro/Max billing — observer just gets to see (and
  compress) the body. The launcher (`cmd/observer/claude.go`)
  resolves the proxy URL from `cfg.Proxy.Port` (or `--proxy`), reads
  the credentials file (honoring `CLAUDE_CONFIG_DIR` /
  `ANTHROPIC_CONFIG_DIR`), exports both env vars without overriding
  anything the user already set, and execs `claude` with the original
  argv. Smoke test: `observer claude -- --print "hi"` lands a real
  `claude-opus-4-7` row with compression metadata populated, on a
  Pro/Max account with `hasApiKey: false`. API-key users get
  `ANTHROPIC_BASE_URL` only — the same path that worked pre-2.1.
  7 unit tests in `cmd/observer/claude_test.go` pin the env-prep
  rules, including the "user-already-set" precedence and the
  malformed-credentials surfacing path. Mirrors the v1.4.36 codex
  ChatGPT-auth fix in spirit (close a silent-bypass for the
  subscription-login majority) but not in shape — codex needed
  upstream-host + path translation; Claude Code OAuth needs only an
  env-var injection because the on-wire upstream is unchanged
  (api.anthropic.com either way).

- **Configure Claude Code dashboard card.** New
  `/api/setup/claude` GET endpoint (purely informational — no config
  to write, no POST) backs a Compression-tab card alongside the codex
  one. Probes `~/.claude/.credentials.json` (honoring
  `CLAUDE_CONFIG_DIR` / `ANTHROPIC_CONFIG_DIR`) and `claude` on PATH,
  classifies the install as `oauth_ready` / `api_key_ready` /
  `claude_not_installed`, and surfaces the launcher snippet
  pre-filled with this observer's proxy port. Status pills show the
  detected `claude` binary path. The card explains the OAuth-bypass
  background only on the `oauth_ready` path so API-key users don't
  see noise that doesn't apply to them. 6 new tests in
  `setup_test.go` cover the four status branches plus
  method-not-allowed and the `CLAUDE_CONFIG_DIR` override path.

### Fixed

- **MCP tool outputs no longer pollute the FTS5 search index
  (recursive-search bug, surfaced 2026-05-08 dogfood).** Pre-fix,
  every `search_past_outputs` (or other MCP tool) call's output got
  written to the `action_excerpts` FTS5 table on ingest. The next
  search for the same keyword surfaced the prior search results as
  hits — because the search response's JSON contains the query
  keyword multiple times. Compounded across sessions: every search
  inflated the index with self-referential hits, degrading recall
  quality session-over-session. The model itself flagged the
  behaviour after the JSON-compression fix landed: *"Many of the
  hits are recursive — prior search_past_outputs calls for the
  same/similar query that themselves got indexed."*

  **Fix in `internal/store/store.go::Ingest`** — skip the
  `idx.Index(...)` call for any tool event whose `RawToolName`
  starts with `mcp__`. Same semantic as the JSON-compression skip:
  MCP tool outputs are derived query data, not source content; the
  search index should reflect what the AI agent *did* (Bash, Read,
  Edit), not what it *queried* about prior actions.

  Pinned by `TestIngest_SkipsMCPToolOutputsFromFTSIndex` — seeds two
  events with the same keyword (`app.set`), one from `Bash` and one
  from `mcp__observer__search_past_outputs`; post-ingest, FTS5
  search returns exactly 1 hit (the Bash row), confirming the MCP
  row was excluded.

  **Note on existing indexed rows.** The fix prevents *new* MCP
  outputs from being indexed but doesn't retroactively clear prior
  ones. The pollution accumulated over the 2026-05-07 stress-test
  sessions remains in `/tmp/ab-claude/on/observer.db`. To reset
  cleanly: `rm /tmp/ab-claude/on/observer.db` and re-run the
  harness; the new binary will rebuild the schema without the
  recursive entries. Production main DB users can either accept
  the slow drift-out (hits age out as new content lands) or run a
  one-time clean with: `DELETE FROM action_excerpts WHERE
  rowid IN (SELECT id FROM actions WHERE raw_tool_name LIKE 'mcp__%')`.

- **CRITICAL — MCP tool_results were silently being JSON-compressed,
  corrupting query data (surfaced 2026-05-08 dogfood; broke
  `search_past_outputs` user-visible).** When the user asked the model
  to call `mcp__observer__search_past_outputs` with `query="app.set"`,
  the tool returned 10 hits — but the model reported "values weren't
  surfaced in this response (placeholders only)". Root cause: the
  proxy's per-type JSONCompressor saw the search response as JSON
  content, replaced every scalar value with a type sentinel
  (`"action_id": "<number>"`, `"excerpt": "<string>"`, etc.), and the
  model received the corrupted shape.

  **Severity.** Every MCP observer tool's response is structured
  query data where the **values are the answer**:
  `search_past_outputs` (action_ids, excerpts, ranks),
  `get_action_details` (full row data), `get_cost_summary` (dollars),
  `check_file_freshness` (hashes, mtimes), `retrieve_stashed`
  (catastrophic — would replace the stashed bytes with `<string>`).
  Two places fired:
  1. `compressToolResults` — JSON/code/logs compressors per
     tool_result block.
  2. `Enforce`'s budget-pass `compressMessage` — second compression
     pass on the joined text.

  Suspected this also explains the 0-of-114 retrieve_stashed rate
  observed in the prior two stress tests: the model may have been
  receiving corrupted retrieve_stashed responses on earlier sessions
  and learned not to trust the tool. Or the marker phrasing alone is
  the issue. Subsequent runs will distinguish.

  **Fix.** New `isMCPToolName` helper checks for the standard
  `mcp__<server>__<tool>` prefix. Three guards added:
  - `compressToolResults` skips per-tool_result compression when the
    producing `tool_use.name` starts with `mcp__`.
  - `stashLargeBodies` skips stashing when the producing tool is MCP.
  - New `Message.NoCompress` field on the conversation Message
    abstraction. `toConversationMessages` sets it whenever any of
    the message's tool_results came from an MCP tool. Budget
    enforcer's per-type compression pass respects the flag, so the
    second-pass `compressMessage` can't corrupt the response either.

  **Tests:** `TestCompressToolResults_SkipsMCPToolResults` pins the
  end-to-end fix — a 10-hit `search_past_outputs` response with 60+
  scalar values goes through the full pipeline with `["json", "logs",
  "code"]` allow-list + stash enabled at 1 KB threshold; output must
  contain the actual `action_id` values (1000..1009) and must NOT
  contain `<number>` / `<string>` sentinels; no `json`/`code`/`logs`/
  `stash` events allowed for the MCP block.

- **`search_past_outputs` FTS5 syntax error on dotted queries
  (surfaced in 2026-05-08 dogfood).** Pre-fix, calling
  `mcp__observer__search_past_outputs` with a query like `app.set` —
  the natural shape for "find anywhere we discussed app.set in
  prior sessions" — failed with
  `search: indexing.Search: query: SQL logic error: fts5: syntax
  error near "."`. SQLite FTS5 treats `.` as a syntactic separator
  (column-qualified search) and rejected the unquoted token. The
  model self-corrected by re-issuing the query as `"app.set"`, but
  the wasted tool round-trip was a real cost.

  **Fix in `internal/compression/indexing/indexer.go::Search`** —
  new `sanitizeFTSQuery` helper auto-wraps single-token queries that
  contain FTS5-special chars (`.`, `:`, `(`, `)`, `*`, `-`) in
  double quotes (FTS5's literal-phrase form). Multi-token queries
  pass through untouched (those callers are presumed to be using
  FTS5 boolean / phrase syntax deliberately). Already-quoted queries
  pass through too. Embedded `"` characters are escaped via FTS5's
  standard `""` doubling.

  Pinned by `TestSanitizeFTSQuery_DottedTokensWrapped` × 9 cases
  (dotted / colon / parens / hyphen / plain word / already-quoted /
  multi-token boolean / empty / whitespace) +
  `TestSearch_DottedQueryNoLongerErrors` integration test that
  re-runs the exact `app.set` query against a real seeded FTS5
  index.

- **Stash marker rephrased to be directive (surfaced in 2026-05-08
  dogfood — model never called `retrieve_stashed`).** Pre-fix marker:
  `[output 47KB stashed at observer://stash/<sha>; use retrieve_stashed]`.
  In a stress-test session with 114 stashed bodies, the model called
  `retrieve_stashed` **zero times** — even when an explicit prompt
  asked for the full output of a previously-stashed grep. The phrase
  `use retrieve_stashed` after the semicolon read as a status note
  rather than an actionable instruction; the model's reflex was to
  re-run the original Bash command instead.

  **Fix in `internal/compression/conversation/anthropic.go::formatStashMarker`** —
  new directive form:
  `[output 47KB stashed at observer://stash/<sha> — to view full content, call mcp__observer__retrieve_stashed with sha="<sha>"]`.
  Three changes vs. prior:
  - Explicit MCP tool name (`mcp__observer__retrieve_stashed`) so
    the model recognises it as a tool-call template, not free-form
    advice.
  - Explicit `sha="<sha>"` argument shape so the model can copy-
    paste the call.
  - "to view full content, call ..." phrased as instruction, not as
    afterthought.

  The URL form `observer://stash/<sha>` is preserved as a secondary
  reference so existing greps / tests still locate it. Cross-turn
  invariance preserved: the marker is still a pure function of
  (kb, sha) → byte-identical bytes turn-over-turn.
  `TestPipeline_StashFires` updated to (a) assert the new directive
  substrings (`mcp__observer__retrieve_stashed`, `sha=\"`) are
  present, and (b) parse the URL-form sha with whitespace as
  terminator (was `;`).

  **Verification caveat.** This fix is structural — the new
  phrasing is more directive on its face, but whether the model
  actually changes its behaviour and starts calling
  `retrieve_stashed` instead of re-running producing commands needs
  another dogfood pass to confirm. Follow-up CHANGELOG entry will
  report the 0-of-114 → N-of-M shift once we re-run the stress
  test on the new binary.

- **D20 rolling summarisation tool-pair invariant violation — naive
  boundary could drop a `tool_use` block whose `tool_use_id` was
  still referenced by a `tool_result` block in the preserved tail
  (Tier 2 / D20 follow-up; surfaced in dogfood on the Express-repo
  stress test).** Pre-fix, when D20 fired on a long session
  containing tool calls, `summarizeIfThreshold` computed `boundary =
  len(msgs) - PreserveLastN` and dropped `msgs[:boundary]` wholesale.
  If any of those dropped messages was an assistant message
  containing `tool_use` blocks whose IDs were still referenced by
  `tool_result` blocks in the preserved tail, the outgoing request
  body was invalid: Anthropic's API validator rejected with HTTP
  400 (`messages.0.content.1: unexpected tool_use_id found in
  tool_result blocks: each tool_result block must have a
  corresponding tool_use block in the previous message`).

  **Root cause.** D20 was wired with attention to cross-turn
  invariance (sticky boundary, hash-checked prefix) but no
  preservation contract for tool pairs. The existing budget enforcer
  (`Score`/`Enforce`) has the concept of `Preserved` for tool-pair-
  live messages — D20 sat in a separate code path and didn't consult
  it. The original D20 mock-summariser tests fed in `Message` slices
  with `Raw == nil`, so the regression mode was latent.

  **Fix.** New helpers in `internal/compression/conversation/rolling.go`:
  - `extractToolUseIDs(raw)` parses an Anthropic message envelope's
    `content` blocks and returns the `tool_use_id`s it produces (from
    `tool_use` blocks) and consumes (from `tool_result` blocks).
    Best-effort: nil on parse failure / string-form content / empty
    Raw, falling back to "include this message" (the safe default).
  - `summarizableBoundary(msgs, initialK)` walks backward from
    `initialK`, expanding K to keep any message that produces a
    referenced `tool_use_id`. Chained preservation: when a newly-
    included message itself consumes IDs, those propagate further
    back. Returns 0 when the entire conversation is one tool-pair
    chain — caller then no-ops summarisation (degraded gracefully:
    request goes upstream uncompressed rather than malformed).

  `summarizeIfThreshold` calls `summarizableBoundary` after
  computing the initial boundary on a rebuild. Sticky reuse is
  unchanged — once a boundary was tool-pair-safe, byte-prefix
  invariance keeps it safe forever (subsequent turns can only ADD
  new pairs at the tail, never reach back into the dropped range).

  **Tests (5 new, all green):**
  - `TestExtractToolUseIDs` × 6 cases — parser shape (string
    content / tool_use / tool_result / parallel calls / empty raw /
    malformed JSON).
  - `TestSummarizableBoundary_PreservesToolPair` — tool_use producer
    at index 4, tool_result consumer at index 14, naive boundary 14
    expands to 4.
  - `TestSummarizableBoundary_NoToolPairsIsNoOp` — boundary stays at
    initialK on no-tool conversations.
  - `TestSummarizableBoundary_PairFullyInDroppedRangeIsNoOp` —
    complete pair (both sides) in dropped range: no expansion needed.
  - `TestSummarizeIfThreshold_PreservesToolPairsAcrossBoundary` —
    end-to-end integration: output preserves `msg[4]` (the producer)
    despite naive boundary 14.

- **Compression pipeline silently rewrote HTML escapes in upstream
  request bytes, causing a model-behaviour regression.**
  `serializeAnthropic`'s `json.Marshal([]json.RawMessage)` call
  applied Go's default `HtmlEscape=true` to the raw bytes coming
  through `RawMessage.MarshalJSON`, rewriting `<system-reminder>` →
  `<system-reminder>` on the wire even when zero compression
  events fired. The SDK sends those tags literally to signal tool-use
  guidance to the model; subtle on-wire differences in those tags
  measurably shifted the model's tool-use sampling, taking ~2× turns
  on multi-file exploration prompts (live A/B repro: ON 9-10 turns on
  "summarize architecture" vs OFF 5 turns, reproduced across two
  runs). Same bug affected `rewriteToolResult` and
  `marshalToolResultBlock` via `json.Marshal` on structs/maps. Fix in
  `internal/compression/conversation/pipeline.go::runAnthropic`: when
  no compression events fire, no drops happen, and no `cache_control`
  injection is needed (the typical case for Read/Bash-heavy code-
  exploration workloads where JSON/logs `Detect` doesn't classify
  Read-tool envelopes), forward the original body unchanged after
  scrubbing. Diagnostic test confirmed two real captured Anthropic
  bodies pass through `delta=+0` (byte-identical) with the fast
  path. Live A/B post-fix: regression closed (14 vs 21 turns, ON 33%
  fewer; cost -14.8%).

- **Parallel `tool_result` messages no longer have their compression
  silently discarded by the serializer.** Pre-fix,
  `extractedMessage`'s `resultBlockIdx` field was only set when a
  message had EXACTLY one `tool_result` block; multi-`tool_result`
  messages (Claude Code's parallel-tool-call pattern: `Read + Bash +
  Read` in one assistant turn produces a single user message with N
  `tool_results`) had `resultBlockIdx = -1`, and `serializeAnthropic`
  short-circuited back to the original raw bytes — Enforce's flat-
  text compression was thrown away. Refactor in `anthropic.go`
  replaces three singular fields (`resultBlockIdx`, `resultText`,
  `resultIsStructured`) with parallel slices (`resultBlockIdxs`,
  `resultTexts`, `resultIsStructureds`) and adds `compressedTexts`.
  New `compressToolResults` runs per-type compression on every block
  independently before scoring (so Score/Enforce see post-compression
  byte counts and drop decisions don't trigger spuriously on already-
  shrunk content). `BudgetOptions.OriginalBodyBytes` carries the true
  pre-compression body size into `Enforce` so the budget reference
  stays anchored against the original. Per-block compression also
  respects the cache_aware marker block (preserves it bit-identical).
  New `TestPipelineCompressesParallelToolResults` pins three parallel
  JSON `tool_results`: blocks 0+1 compressed, block 2 (the small
  tail) preserved, all three structurally retained in the output.

- **Proxy: `api_turns.cost_usd` now populated at insert time.** The
  column existed since the table was created but no writer set it —
  the dashboard's `/api/cost` and `/api/timeseries/cost` endpoints
  computed cost on the fly via `cost.Engine.Compute(model, bundle)`,
  but every reader that summed the column directly (`observer cost`
  CLI, `scripts/ab-claude-report.sh`'s headline panel, ad-hoc SQL
  queries) saw NULL/zero and reported `$0.00 saved` even on sessions
  with real multi-thousand-token spend. The proxy now accepts an
  optional `CostComputer` (mirrored on `Compressor`'s pattern, so the
  proxy stays free of an `intelligence/cost` import cycle); when
  set, every insert site (stream success, stream error, non-stream
  success, non-stream error) calls a small `applyCost` helper that
  populates `APITurn.CostUSD` before handing the row to the sink.
  `cmd/observer/proxy.go` wires a `costEngineAdapter` over
  `cost.NewEngine(cfg.Intelligence)` so production gets the same
  pricing table the dashboard uses. Computers are nil-safe and
  unknown-model-safe (cost stays NULL when pricing isn't found, so
  downstream on-the-fly computation still wins). Two new tests in
  `proxy_test.go`: `TestProxy_PopulatesCostUSDOnInsert` pins a known
  rate × known tokens product through the full proxy → sink path;
  `TestProxy_CostUSDUnsetWhenComputerAbsent` confirms the no-op when
  no computer is wired (so older Options callers don't regress).
  Manual verification on the live OAuth A/B: a single `--print`
  turn with 6 input + 11 output + 46,577 cache-creation tokens
  records `cost_usd = 0.291411`, matching the spreadsheet math
  against `claude-opus-4-7`'s rates ($5/$25/$6.25 per 1M).

- **Proxy: usage tokens lost when upstream gzips the response.** Modern
  HTTP clients (claude included) negotiate `Accept-Encoding: gzip, br`
  by default and `api.anthropic.com` honors it — so the proxy's SSE
  parser saw gzip-encoded bytes, found zero `data:` lines or `usage`
  keys, and `api_turns` rows landed with input/output tokens both
  zero. The bug was invisible for API-key clients run via curl/scripts
  (which don't negotiate compression) and only surfaced once the
  `observer claude` launcher started routing live OAuth Pro/Max
  traffic through the proxy: 24 of 24 successful chat rows from the
  first live A/B had `input_tokens=output_tokens=0` despite real
  multi-thousand-token prompts. Fix:
  `internal/proxy/proxy.go::serve` now sets
  `Accept-Encoding: identity` on the upstream request after copying
  client headers. Identity-forced responses are plaintext and the
  parser extracts usage as designed. The proxy runs on loopback so
  the bandwidth cost is effectively free; the gain is accurate
  per-turn input/output/cache token counts (and therefore real
  `cost_usd`, real `bytes saved %`, real Live verification numbers
  in the headline panel). New regression test
  `TestProxy_AnthropicStreamingForcesIdentityEncoding` in
  `proxy_test.go` mocks an upstream that records the negotiated
  encoding; the test fails if the override regresses.

- **`enabled_adapters = []` in config.toml is now respected as
  "no adapters" (was silently treated as "all"). ** `internal/adapter/
  registry.go::Detected` distinguished `len(allow)==0` (empty allow
  list → no filter) from `allow==nil` (no allow list → no filter)
  prior to this fix; both paths yielded the same result. The user-
  facing footgun was the A/B compression test setup: each A/B daemon
  config wrote `enabled_adapters = []` to disable its watcher, but
  the watcher still came up with the full default adapter set and
  ingested 41,698 actions / 218 sessions / 31,327 token rows from
  the user's main `~/.claude/` directory into each A/B observer.db
  (113 MB per DB). Now `nil` (zero-value, callers using
  `watcher.Options{}` without setting `Allow`) means "no filter,
  return all detected"; non-nil empty slice (the explicit-empty TOML
  case) means "filter to zero adapters". BurntSushi/toml correctly
  preserves the nil-vs-empty distinction. Two new tests pin the
  semantics:
  `TestDetectedExplicitEmptyAllowDisablesAllAdapters` in
  `internal/adapter/registry_test.go` and
  `TestEmptyEnabledAdaptersDisablesWatch` in
  `internal/config/config_test.go`. The A/B harness re-tested clean
  after the fix: 0 actions/sessions/token rows in both `/tmp/ab-
  claude/{on,off}/observer.db`, DB sizes dropped from 113 MB to 4 KB.

- **Antigravity Run All wastes ~70s on Linux-side conversations.**
  `internal/adapter/antigravity/adapter.go::recoverViaLocalGRPC` and
  `internal/adapter/antigravity/index_resolve.go::FetchStructuredTrajectory`
  now short-circuit the Windows-side bridge fallback for paths under
  `/home/` (where Linux-side `~/.gemini/antigravity/conversations/`
  `.pb` files live). The bridge can't read files behind WSL's
  filesystem boundary, so the prior fall-through cost ~3s per
  conversation × 24 conversations on a typical Run All — and emitted
  multi-line WARN dumps for each. Now returns a one-line
  "no Linux-native language_server hosts this conversation; reopen
  the originating workspace in Antigravity to recover" hint and moves
  on. Linux-native discovery still runs first (so a running native
  server still recovers any conversation it hosts); only the
  guaranteed-to-fail bridge step is skipped.

- **Proxy test stability sweep.** Three SSE-shaped upstream test
  fixtures were missing the `Cache-Control: no-cache` header and an
  explicit `flusher.Flush()` after writing the body — same pattern
  that bit `TestProxy_OpenAIResponsesStreamingCompletedUsage` and
  `TestProxy_StreamUpstreamError`. Patched proactively to keep
  release runs deterministic:
  `TestProxy_AnthropicStreamingZeroUsageDropped`,
  `TestProxy_OpenAIStreamingNoUsageDropped`,
  `TestProxy_OpenAIStreamingNoUsageKeptWhenCompressed`. Plus
  `TestProxy_StreamUpstreamError` itself: the test client closed
  `resp.Body` without draining, which can race the proxy's
  `teeStream → InsertAPITurn` sequence; now drains via
  `io.Copy(io.Discard, resp.Body)` first. 10× stress-runs all pass.

### Changed

- **Default `compression.conversation.compress_types` is now
  `["json", "logs"]` (was `["json", "logs", "text"]`).** The `text`
  compressor's head-tail truncation strategy keeps the first ~1KB and
  last ~1KB of a body and replaces the middle with a `[truncated]`
  marker — destructive to mid-file content the agent may re-reference.
  Live A/B on a "trace request flow" prompt showed the model re-
  reading `lib/application.js` 8 times in a row trying to recover
  elided content; OFF answered the same prompt in 2 turns. Removing
  `text` from the default closes that regression class. Users who want
  it can opt in explicitly. JSON schema replacement and logs dedup
  remain content-preserving and stay enabled.

- **`crossmount: detected extra home` log demoted INFO → DEBUG.**
  Useful when the cross-mount detector was new (users could see
  exactly which Windows-side homes WSL2 picked up); in steady state
  it re-prints 8+ lines of stable data on every scan. Diagnostics
  that actually change state stay at INFO. (`cmd/observer/main.go`.)

- **CI: `actions/setup-node` Node version bumped 20 → 22.** Node 20
  is in maintenance LTS and the GitHub Actions runtime emits a
  deprecation warning on every workflow run. The publish job only
  invokes `npm publish`, so it's Node-version-insensitive. The
  `actions/checkout`/`setup-go`/`upload-artifact`/`download-artifact`
  versions were already current.

### Added

- **A/B harness pre-flight check.** `scripts/ab-claude-start.sh`
  now hits `/v1/messages` on each daemon's port with a fake
  `x-api-key` after startup; expected response is Anthropic's
  `invalid x-api-key` 401, which proves the proxy *forwards* (not
  just *binds*). If the user has typoed the env-prefix or Claude
  Code ignores `ANTHROPIC_BASE_URL`, the harness now flags it before
  the user burns 4 prompts on a bypassed-proxy run. Pre-flight 401s
  are filtered out of `ab-claude-report.sh`'s aggregates via
  `error_class IS NULL`. Doc tightened with one-liner invocation
  guidance: `cd …` first, then `ANTHROPIC_BASE_URL=… claude` —
  inline env-prefix only attaches to the immediately following
  command, and `… && claude` chains drop the prefix on the second
  step.

- **Claude Code compression A/B harness.** Four shell scripts plus a
  recipe doc give end users a one-command way to measure conversation-
  compression savings on their own Claude Code sessions against a
  real public repo. `scripts/ab-claude-setup.sh` clones a repo
  (default `expressjs/express`) into `/tmp/ab-claude/{on,off}/repo/`
  and writes per-side observer configs (port 8830 vs 8831, separate
  SQLite DBs, `[compression.conversation].enabled` true vs false,
  watch-adapters disabled so the test daemon doesn't ingest the
  user's main `~/.claude/` sessions). `scripts/ab-claude-start.sh`
  brings up both daemons in the background with `--no-dashboard`;
  startup probe uses a connect-only `/dev/tcp` check so it doesn't
  land as a 404 turn in either DB. `scripts/ab-claude-stop.sh`
  tears down without touching the user's main daemon.
  `scripts/ab-claude-report.sh` reads both DBs read-only and prints
  a markdown table covering turns / input / output / cache-read
  tokens / cost USD / compression bytes saved + % / per-mechanism
  breakdown / median + p95 latency. Recipe lives in
  `docs/claude-code-compression-ab.md`. The user drives the actual
  Claude Code sessions (one terminal per port); the scripts handle
  setup, lifecycle, and result extraction.

- **Codex CLI proxy routing from the dashboard.** New `/api/setup/codex`
  GET + POST endpoints back a "Configure Codex CLI" card on the
  Compression tab. The card surfaces only when `~/.codex/config.toml` is
  not already routed through this Observer's proxy and offers a
  one-click write — no terminal, no `observer init --codex`. Reuses
  the `proxyroute.Registrar` code path the CLI uses, so the conflict
  matrix is identical (reserved `[model_providers.openai]` block,
  non-loopback `base_url`, foreign top-level `model_provider` all
  refuse without `force=true`). 409 conflicts surface a "Force
  overwrite" button on the card so users on v1.4.34-broken installs
  recover in one click. ChatGPT-auth installs see an inline note about
  the JSONL-only token capture path. `dashboard.Options.ProxyPort` is
  plumbed from `cfg.Proxy.Port` in `cmd/observer/start.go` so the
  card's desired URL matches the running proxy. 9 new tests in
  `setup_test.go` cover GET (no_config / routed_to_observer /
  reserved_block_present / non_loopback / not_configured) and POST
  (fresh-file / reserved-block-without-force / reserved-block-with-
  force / dry_run) plus method-not-allowed.

- **Compression dashboard: $ savings on charts.** The Compression tab's
  per-day chart and per-mechanism stacked-bar chart previously surfaced
  bytes and tokens only — KPI tile and per-model table already showed
  $, but the time-series visuals didn't. Now:
  - **Per-day chart** replaces the right-hand "Bytes saved" series with
    "$ saved (est.)" — bytes are still totalled in the KPI tile above.
    Y-axis B switches from byte units to USD with a `fmtUSD` tick
    formatter, tooltip routes the $ axis through `fmtUSD` while the
    tokens axis stays on `fmtN`.
  - **Per-mechanism chart** gets a third "$" button on the unit toggle
    (alongside tokens / bytes). Y-axis title and tooltip switch to "$
    saved (tokens × model input rate)" when active.
  - **Footer notes** under both charts spell out the bytes → tokens
    (`÷ 4`) and tokens → $ (× model input rate) math so the derivation
    is on-page rather than buried in the tab blurb.
  - **"Recent compression events" table** gains a `$ saved` column
    between `Saved (tok)` and `Saved %`. Per-row $ uses the joined
    `api_turns.model` priced through `cost.Engine.Lookup`; rows whose
    model has no pricing entry render `—` with a tooltip explaining
    why (bytes/tokens still counted).

  Backend: `handleCompressionTimeseries` now joins `compression_events
  → api_turns` to get per-event model context, looks up each model's
  input rate via `cost.Engine.Lookup`, and emits `saved_usd_est` per
  `(bucket, mechanism)` plus a `total_saved_usd_est` per bucket.
  `handleCompressionEvents` adds `saved_usd_est` to each row in the
  paginated response. Models without a pricing entry contribute 0 to
  $ but still count toward bytes/tokens — matches
  `cost.Engine.Summary`'s `CostSavedUSDEst` semantics. `dashboard_test`
  extended in both endpoints.



Brings the cross-handoff proxy + compression + dashboard + watcher
work together into one shippable bundle. Proves out the A/B
compression test against codex 0.128.0 with ChatGPT-plan auth: short
prompt records 1,402 tokens with compression ON vs 10,106 OFF
(~86% reduction); long prompts record api_turns on both sides with
populated `compressed_bytes` on the ON side.

### Added

- **Proxy: ChatGPT-auth detection + path translation.** Codex
  0.128.0+ rejects overriding the reserved built-in `openai`
  provider, so the recommended workaround is a custom provider name
  — which means codex always sends canonical `/v1/responses` paths
  even on ChatGPT-plan auth, where older codex used native
  `/backend-api/codex/responses`. Path-based detection alone
  therefore mis-routed every request to api.openai.com, where
  ChatGPT JWTs returned 401 ("Missing scopes:
  api.responses.write"). Detection now reads the `Authorization`
  header — `Bearer eyJ...` is a ChatGPT JWT, `Bearer sk-...` is a
  platform API key. When ChatGPT auth is detected on an OpenAI-side
  path, the proxy:
  - routes the upstream request to `chatgpt_upstream` (default
    `https://chatgpt.com`) instead of `openai_upstream`;
  - rewrites `/v1/{responses,models,...}` → `/backend-api/codex/...`;
  - short-circuits `GET /v1/models` with a synthetic 200 carrying
    `{"models":[]}`, since chatgpt.com doesn't expose that endpoint
    and codex's model-list refresher would otherwise spam 401s every
    turn. Codex 0.128.0+ expects the chatgpt-shaped `models` field —
    OpenAI's `{"object":"list","data":[]}` shape trips the decoder
    with "missing field `models`";
  - rejects ChatGPT websocket upgrades when `force_chatgpt_http` is
    set so codex falls back to HTTP POST (Observer can only inspect
    HTTP bodies);
  - decodes/re-encodes zstd request bodies — chatgpt.com sends them
    compressed and the compression layer needs the plain JSON.

  Also: `/v1/models` was historically routed to `ProviderAnthropic`
  by default (no prefix match) — added it to the OpenAI prefix list
  so the short-circuit can match.

- **Compression: Responses-API (`input` array) support.** The
  conversation compressor previously only handled the Anthropic
  `messages` shape. Codex's Responses API uses `input` instead. Adds
  `internal/compression/conversation/openai.go` with input-array
  extraction and structural-aware token compression that preserves
  the user prompt and tool-call structure verbatim.
  budget/scoring/pipeline updated for the new shape; `pipeline_test`
  gets ~360 lines of coverage.

- **Dashboard: ChatGPT-mode awareness + unlinked-turn count.** New
  `internal/intelligence/dashboard/codex_support.go` adds detection
  for the four codex modes (`jsonl_only`, `proxy_unlinked+jsonl`,
  `proxy_linked+jsonl`, `proxy_only`) and three compression modes
  (`none`, `live`, `live_unlinked`). The Codex section's banner now
  shows unlinked proxy turns explicitly so users on the ChatGPT-auth
  path see what's being captured even when the JSONL session can't
  yet be linked. Header, table, and tooltip in `static/index.html`
  updated to surface the new fields. `dashboard_test` gets ~180
  lines of regression coverage for the support-snapshot endpoint.

- **Watcher: settle pass for short ChatGPT-auth Codex sessions.**
  `internal/watcher/watcher.go` runs a delayed second parse after
  each debounced batch so files that grow shortly after the first
  parse get re-ingested even when fsnotify drops the follow-up
  Write event (common on Windows-NTFS for short codex turns). Pairs
  with the v1.4.33 prefetch fix to make live ingestion reliable for
  the ChatGPT-auth A/B path.

### Fixed

- **Proxy: keep ChatGPT-auth turns even when usage is empty.**
  `isEmptyUsage(turn) && !hasCompressionEvidence` previously
  dropped zero-token turns to keep averages clean. That filter
  dropped every non-compressed ChatGPT-auth turn during live
  verification — the chatgpt.com SSE shape can omit the `usage`
  block in places the parser expects, so the OFF-side A/B baseline
  was silently lost on the dashboard. Added a third exemption:
  `&& !chatgptAuth`. Real ChatGPT turns now land regardless of how
  the upstream framed usage.

- **`TestProxy_OpenAIResponsesStreamingCompletedUsage` flake.** The
  new test for `response.completed` usage parsing failed ~80% of
  runs because the upstream test handler wrote the SSE bytes
  without an explicit `flusher.Flush()` call; under contention the
  close-flush race against the proxy's `teeStream` read loop made
  the capture intermittently see zero bytes. Mirrored the
  earlier-passing `TestProxy_OpenAIStreamingWithUsage` pattern
  (`Flush()` + `Cache-Control: no-cache`); 8/8 stable after the
  change. Test-only — the proxy capture path is fully synchronous.

### Documentation

- **npm/observer/README.md.** Per-AI-client setup table now
  separates Codex's two practical capture modes: API-key auth runs
  full proxy + JSONL; ChatGPT-plan login currently behaves as JSONL
  only (live proxy compression depends on the proxy → JSONL session
  link, which isn't yet reliable on the ChatGPT-auth path).

### Added — post-bundle follow-ups

- **Cost engine surfaces D20 summary_calls as a first-class source.**
  New `cost.SourceSummaryCalls` reads the migration-016 ledger
  (`internal/intelligence/cost/summary.go::loadSummaryCallRows`).
  Rows are tagged `source = "summary_calls"` and
  `tool = "observer-rolling-summary"` so per-tool grouping shows the
  observer-initiated Haiku spend separately from natural turns. Wired
  into `SourceAuto` so `observer cost` CLI, `mcp__observer__get_cost_summary`,
  and dashboard cost-by-model views all pick it up automatically without
  per-consumer changes. Defensive against missing migration 016
  (no-such-table → empty rows, never a fatal error). 3 new tests pin
  the shape: `TestSummary_SourceAuto_IncludesSummaryCalls`,
  `TestSummary_SourceAuto_TaggedAsObserverTool`,
  `TestSummary_SourceSummaryCallsOnly`. Pre-fix the dashboard's
  rolling-cost panel was the only place that knew about Haiku spend;
  totals on the cost CLI / MCP tool understated the user's bill by the
  rolling-summ portion. Honest framing: production users won't see
  these rows until they enable D20 (off by default), so totals only
  drift on configs where rolling-summ is actually firing.

- **K43 PatternMiner emits threshold-tuning hints.**
  `learn.PatternMiner.Report` now also reads `compression_events` to
  compute `TotalStashes` and `RetrieveRate`, then runs
  `deriveThresholdHints` to produce banded recommendations:
  * 0 retrieves on ≥ 10 stashes → info: "current threshold catches
    bodies the model never returns to; raising
    `compression.conversation.stash.threshold_bytes` (default 8192)
    would shrink the stash dir without hurting safety."
  * retrieve rate ≥ 5% → consider: "model returns to stashed content
    often; lowering threshold captures smaller bodies the model is
    asking about."
  * retrieve rate in (0, 5%) → info: "within expected baseline;
    no threshold change recommended" (matches the CCR-moat reframe:
    retrieve rate measures safety-net utilisation, not the primary
    cost saving).
  Surfaced in the dashboard's Reversibility panel via a new
  `retrieval-hints` block (rendered only when hints fire — fresh
  installs stay clean). `GET /api/compression/retrieval` now
  delegates to `PatternMiner.Report` instead of inlining duplicate
  SQL — DRY win + Hints surface naturally.
  Tests: `TestPatternMiner_RetrieveRateAndHints` covers all three
  bands + the no-stashes no-op case.

### Changed — post-bundle follow-ups

- **D20 tool-pair safety reads from `Message.ToolUseIDs` /
  `Message.ReferencedIDs` instead of re-parsing Raw bytes.**
  `summarizableBoundary` previously called `extractToolUseIDs(msg.Raw)`
  twice per message in the tool-pair walk — once to collect referenced
  IDs in the tail, once per backward-walk message to check producers.
  The same parsing already happens at extract time
  (`anthropic.go::fillFromBlocks`) and populates `Message.ToolUseIDs` /
  `ReferencedIDs`, so the walk now reads those fields directly. No
  behaviour change — pinned by existing
  `TestSummarizeIfThreshold_PreservesToolPairsAcrossBoundary` +
  `TestSummarizableBoundary_PreservesToolPair`. Removed dead
  `extractToolUseIDs` + `bytesTrimSpace` helpers and the
  `TestExtractToolUseIDs` parser-shape test (the actual parser is
  `fillFromBlocks`, covered by integration tests).

- **A/B harness threshold reverts.**
  `/tmp/ab-claude/on/observer-config.toml`:
  `compression.conversation.stash.threshold_bytes` 1024 → 8192 (production
  default), `compression.conversation.rolling.threshold_tokens` 10000 →
  80000 (production default). Inline comments updated.
  `~/.observer/config.toml`: stash dir repointed from
  `/tmp/ab-claude/on/stash` → `~/.observer/stash`, threshold 1024 →
  8192. The earlier dir mirroring was a workaround for the MCP/proxy
  config split; the new `--config` propagation (see Fixed below)
  removes the need for it. Numbers from the v1.4.43+ stress runs are
  unsafe for headline claims now that thresholds are aggressive — a
  fresh A/B re-run with these production thresholds is the next
  measurement to take.

### Fixed — post-bundle follow-ups

- **`observer init --config` now propagates to MCP + hook
  registrations.** Surfaced 2026-05-08 dogfood. Without this, the
  registered MCP launch (`observer serve`) and hook commands
  (`observer hook claude-code <event>`) always read
  `~/.observer/config.toml`, regardless of which proxy daemon they
  paired with. Three concrete breakages: (1) MCP server's
  `retrieve_stashed` registration could not align with whichever
  proxy was actually stashing (had to mirror stash config in the
  main file); (2) `/compact` rows landed on the main DB even when
  the harness proxy fired the hook → D23's `Injector` queried the
  proxy's DB and found nothing; (3) K43 retrieve_stashed signals
  landed on the main DB rather than the harness DB.
  Fix: `RegisterOptions.ConfigPath` on both `internal/mcp/register.go`
  and `internal/hook/register.go`. When set, the registered argv
  becomes `serve --config <path>` (MCP, JSON args array) or
  `hook claude-code <event> --config '<shell-quoted path>'` (hook,
  bash -c command string with POSIX single-quote escaping for paths
  with spaces / quotes). `cmd/observer/init.go` gains a `--config`
  flag that validates + absolutises before threading into both
  registrars. `cmd/observer/hook.go` gains a `--config` flag that
  threads through every `config.Load()` call. Same-binary args
  drift silently refreshes the registration; non-observer entries
  still require `--force`. New tests:
  `TestRegistrar_ConfigPathThreadedIntoArgs`,
  `TestRegistrar_ConfigPathChangeIsNotAlreadySet`,
  `TestRegisterClaudeCodeWithConfigPath`,
  `TestRegisterClaudeCodeRefreshesOnConfigPathChange`,
  `TestShellQuoteEscapesSingleQuotes`.

## [1.4.35] — 2026-05-06

Hot-fix on top of v1.4.34. Live verification surfaced that codex
0.128.0+ rejects any attempt to override the built-in `openai`
provider in `config.toml` with a hard config-load error
("model_providers contains reserved built-in provider IDs:
`openai`. Built-in providers cannot be overridden."). The
`[model_providers.openai]` block v1.4.34's `observer init --codex`
wrote is therefore unloadable on current codex — any user who ran
v1.4.34's init now has a config that prevents codex from starting at
all. Recovery requires re-running init with the new release.

### Fixed

- **Codex proxy auto-registration uses a sibling provider, not an
  override of the reserved `openai` ID.** `observer init --codex` now
  writes a custom provider under the non-reserved name
  `openai-observer` and switches `model_provider` to it, instead of
  trying to override the built-in `openai` provider:

  ```toml
  model_provider = "openai-observer"

  [model_providers.openai-observer]
  name = "OpenAI (via Observer)"
  base_url = "http://127.0.0.1:8820/v1"
  wire_api = "responses"
  requires_openai_auth = true
  ```

  ChatGPT-auth survives via `requires_openai_auth = true` on the
  custom provider — that field exists on `ModelProviderInfo`
  specifically to let custom providers reuse the OpenAI auth flow.

  **Migration path for v1.4.34-broken installs.** When init runs
  against a config that already contains the old reserved
  `[model_providers.openai]` block, it errors with a clear explanation
  and points at `--force`. With `--force`, the reserved block is
  deleted and replaced with the custom-provider shape. A single
  `observer init --codex --force` recovers a v1.4.34-broken install.

  Other refused conflicts (existing v1.4.34 conflict matrix, now
  applied to the new key): a non-loopback `base_url` on the custom
  provider (third-party routing — only `--force` overwrites), and a
  top-level `model_provider` that points at a different provider
  (e.g. `anthropic` — again only `--force` flips it). A `127.0.0.1`
  base_url on a different port is treated as the user's own observer
  install and reports `AlreadySet` without changes.

  `--skip-proxy-route` hint output is also updated so users following
  the manual path get the new shape (with an explanatory note about
  the reserved-ID constraint), not the broken v1.4.34 shape.

  Coverage: `internal/proxyroute/codex_test.go` restructured to 11
  cases — fresh-file, preserves-unrelated, reserved-block-rejected,
  reserved-block-force, idempotent-same-port, loopback-collision,
  non-loopback-refused-with-force, third-party-model-provider-refused,
  half-state (provider written but model_provider not flipped),
  dry-run, `IsObserverBaseURL`, and `CodexHint` key contents.

## [1.4.34] — 2026-05-06

Two follow-ups from v1.4.33's live verification: codex now wires its
proxy automatically (it was silently bypassing the proxy because the
`OPENAI_BASE_URL` env var path codex 0.128.0 ignores was the only one
documented), and `observer doctor` now surfaces the dual-daemon failure
mode that turned the v1.4.32 codex-prefetch bug into a recurring
"phantom partial session" symptom.

### Added

- **Codex proxy auto-registration via `internal/proxyroute`.**
  `observer init --codex` now writes the proxy routing config into
  `~/.codex/config.toml` (e.g. `OPENAI_BASE_URL =
  "http://127.0.0.1:8820/v1"`) so codex actually transits the observer
  proxy. Pre-fix init only touched MCP and printed a hint; users had
  to hand-edit TOML or remember the `-c` flag, and codex 0.128.0
  silently ignores the `OPENAI_BASE_URL` env var that the docs
  examples leaned on. The new package owns per-tool routing as a
  concern parallel to `internal/mcp`. `RegisterCodex` round-trips
  through TOML's generic `map[string]any` so unrelated sections —
  including the `[mcp_servers.observer]` table written by the MCP
  registrar — survive intact. Same idempotent + dry-run + force
  pattern as MCP/hook registration: re-running with the same port
  reports `AlreadySet`, a different loopback URL is treated as the
  user's own observer (warn but don't clobber), a non-loopback URL
  refuses without `--force`. New CLI flags on `observer init`:
  `--proxy-port` (default 8820) and `--skip-proxy-route` (print the
  manual hint instead of writing the file). The existing claude-code
  routing hint is now parameterized on the same `--proxy-port` value
  so it stops hardcoding `8820` in its example output. Coverage: 8
  unit tests in `internal/proxyroute/codex_test.go` for the
  fresh-file / preserves-unrelated / idempotent / loopback-collision
  / non-loopback-refused-with-force / dry-run / `IsObserverBaseURL` /
  hint paths.
- **Doctor check: multiple observer daemons sharing a DB.** Two
  `observer.exe` processes watching `~/.codex/sessions/` against the
  same `~/.observer/observer.db` race on the `parse_cursors` table —
  one advances past resumed-context lines and the other never sees
  them, silently corrupting backfill state. This was the root cause
  of the v1.4.32-era "phantom partial session" symptoms during live
  verification of the codex prefetch fix; killing the stale
  npm-installed observer was the only way to recover. `observer
  start` now writes a per-PID lockfile under the DB directory
  (`observer-<pid>.lock` — JSON with `pid` / `started_at` / `db_path`
  / `binary_path`) and removes it on clean shutdown. Stale lockfiles
  from crashed processes are swept on the next `observer start`. The
  new doctor check `daemon.unique` enumerates live lockfiles and
  warns when more than one is alive on the same DB, with each PID +
  binary path printed for forensics. 0 daemons (just running doctor)
  is OK; 1 daemon is OK; 2+ is a warn nudge to kill the strays.
  Cross-platform liveness: `os.FindProcess` on Windows verifies the
  handle; `Signal(0)` on Unix. False negatives bounded — at worst a
  stale lockfile sticks around until the next `observer start`
  sweep. Coverage: 8 unit tests in `internal/diag/lockfile_test.go`
  for `WriteLock` / `RemoveLock` round-trip, stale-PID filtering,
  sort order, empty dir, garbage files, plus the three
  `checkConcurrentDaemons` cases (none / one / multiple).

## [1.4.33] — 2026-05-06

### Fixed

- **Codex incremental parse now preserves session context across
  resume.** When the watcher's first parse advanced the cursor past
  `session_meta` and the file then grew with `token_count` /
  `function_call` / `task_complete` events, the resumed parse started
  with empty `Cwd` (no `session_meta` in the new chunk) and the
  date-prefixed filename as `SessionID`. Every emitted event therefore
  had `ProjectRoot=""`, and `store.Ingest` silently dropped the lot
  because empty `ProjectRoot` is a hard skip — short ChatGPT-auth
  Codex sessions surfaced on the dashboard with only the leading 4
  prompt rows and 0 tokens unless the user ran `observer scan
  --force`. The parser now prefetches the file's leading
  context-bearing lines (`session_meta`, `session_configured`,
  `turn_context`) on every resume so the resumed events inherit the
  canonical UUID `SessionID` and the recorded `cwd`. Regression
  tests: `TestResumePreservesSessionContext` (codex unit) replays the
  failure-mode rollout shape and asserts every resumed event carries
  a non-empty `ProjectRoot` and the UUID `SessionID`;
  `TestCodexShortSessionMultiPassIngest` (watcher integration) drives
  the same shape end-to-end through a real SQLite store and asserts
  the `run_command` row lands; `TestCodexConcurrentProcessFileRace`
  fires two `processFile` calls concurrently for one path to confirm
  INSERT OR IGNORE + MAX cursor semantics keep the result deterministic
  even when the watcher's debounce + settle timers race. Already-broken
  sessions can be backfilled once with `observer scan --force`.

## [1.4.32] — 2026-05-05

Surface the full underlying text on the per-message timeline expand
view. Three coordinated additions:

### Fixed

- **Per-tool-call `full_text` on `/api/session/<id>/messages`.** The
  adapter truncates `target` to 200 chars for the table-row preview;
  prompt-style actions (`user_prompt`, `system_prompt`, `ask_user`,
  `run_command`) now also surface their pre-truncation `raw_tool_input`
  via a new `full_text` field. Prompts that ran past the 200-char
  cap are no longer cut off when the row is expanded.
- **`run_command` argv decode for display.** Codex / Claude shells
  serialize commands as JSON arrays (e.g.
  `["powershell.exe","-Command","Get-ChildItem ..."]`). The new
  `decodeCommandInput()` helper detects a leading `[`, JSON-unmarshals
  into `[]string`, and joins with spaces for human-readable display.
  Defensively tolerant — falls back to the raw string on any decode
  error or empty input.
- **`excerpt` field surfaced inline.** `action_excerpts` rows
  (FTS5-indexed tool-output excerpts) are now LEFT JOIN'd into the
  `/api/session/<id>/messages` response and rendered as a
  `**Output:** …` sub-line under each tool call on the expand view.
  Previously only visible via the search tab.

## [1.4.31] — 2026-05-05

Fix the session modal's per-message cost rollup for long-context models.
The dashboard was summing all token rows under one `message_id` and then
pricing that aggregate as a single synthetic turn, which falsely tripped
LC pricing for GPT-5.4/5.5 and could do the same for Claude Sonnet 4/4.5
and Gemini Pro families whenever multiple sub-threshold turns shared one
message row.

### Fixed

- **Per-message timeline cost is now priced per underlying deduped turn,
  then summed back into the message row.** This keeps LC threshold checks
  on the real turn boundary instead of the UI aggregation boundary, so the
  message modal no longer doubles displayed cost for sessions like the
  user's local Codex audit.
- **Regression coverage for multi-turn single-message rows under
  `gpt-5.5`.** New dashboard test proves two 150K-input turns grouped
  under one message still bill at standard tier (`$1.50` total), not the
  false aggregate LC tier (`$3.00`).

## [1.4.30] — 2026-05-05

Docs-only follow-up to v1.4.29. Both READMEs (top-level + npm
package) had drifted behind the adapter set: Google Antigravity and
Gemini CLI shipped in v1.4.29 but neither was listed in the supported
clients prose, the per-client setup table, the JSONL-adapters
architecture section, the per-AI-client breakdown lists on the
Sessions / Tools tabs, the reliability-tagging paragraph, the
Discovery glossary "Tool" definition, or the architecture diagram's
adapter count.

### Documentation

- **`README.md`**: opening sentence + Tools-tab description list +
  architecture diagram's adapter count (9 → 10) + the
  `observer backfill` granular-flags list (added
  `--codex-project-root` and `--antigravity-project-root`).
- **`npm/observer/README.md`**: opening "across X, Y, Z" sentence +
  per-AI-client setup table (two new rows describing Antigravity's
  encrypted-protobuf + oscrypt-key-fetcher + gRPC-bridge fallback
  pipeline and Gemini CLI's dual-format JSON/JSONL dispatch with the
  three-step project-root fallback) + reliability-tagging paragraph
  + Sessions-tab tool list + Discovery glossary "Tool" definition +
  architecture-section "JSONL adapters (passive ingest)" prose
  (added the Antigravity-specific decryption + gRPC-fallback
  paragraph).

## [1.4.29] — 2026-05-05

Antigravity coverage round 2 — close the orphan-token-row gap on the
dashboard for agentic sessions, plus a JSONL recovery pass for Claude
Code's own log corruption and two WSL2-side cleanups around the
language-server bridge.

### Added

- **Tier 4 — `structured.run_command` extraction** from
  `1.2.19.4.8.x` terminal-session snapshots embedded in every
  `1.2.19` user-message envelope. Each `1.2.19` carries a snapshot of
  every live IDE terminal; we walk the snapshot, dedup commands by
  `(terminal_uuid, start_unix_sec)` (the same command repeats in
  every later snapshot until the session is closed), and emit one
  `structured.run_command` ToolEvent per unique command with
  scrubbed `cwd`, stdout/stderr, real start timestamp, duration, and
  exit-code-derived success flag. **184 commands extracted DB-wide**
  in the live smoke.

- **Tier 5 — `structured.plan_step` extraction** from `1.2.93.x`
  (step-type enum `1.2.1 = 81`). The agent emits one of these
  between actions carrying step description (`.2`), long-form
  analysis (`.3`), and a status varint (`.5` — observed values 1, 2,
  3). Surfaces agent reasoning that the markdown trajectory and Tier
  3 PLANNER_RESPONSE often miss. **2,438 plan steps DB-wide**.

- **Tier 6 — `structured.final_summary` extraction** from
  `1.2.94.x` (step-type enum `1.2.1 = 82`). The agent emits these as
  polished markdown sign-offs at major milestones; `.1` lists
  referenced file URIs and `.2` is the formatted summary body. **427
  final summaries DB-wide**.

- **Dashboard orphan-token-row stub.** For agentic sessions where
  the upstream API stores no extractable content for most LLM calls
  (gemini-style tool-call-only turns), `/api/session/<id>/messages`
  now synthesizes a `synthetic.api_call` toolCallRow per orphan
  carrying the per-turn token totals so the expand-row view shows
  *something* instead of the previous "—". Gated on orphan ratio
  > 0.5 so claude sessions (where every turn already has narrative
  or a tool call) don't grow noise stubs that obscure real content.

- **Claude Code JSONL prefix-recovery for concatenated records.** A
  rare CLI atomic-write failure can produce a single JSONL line
  containing two records back-to-back (the writer was interrupted
  before the first record's trailing newline could flush, the next
  record's payload was then appended directly). Pre-fix the
  adapter's `json.Unmarshal` returned an error and the entire line
  was dropped. The new `recoverConcatenatedJSONLines` helper scans
  the malformed line for `{"parentUuid":` and `{"type":"` anchors,
  streaming-decodes each anchor-rooted suffix, and returns every
  parseable sub-record. The leading truncated record is still
  unrecoverable (its tail was overwritten); trailing records parse
  cleanly. Verified on the user's host: **6 sub-records salvaged**
  across 4 corrupted lines that would previously have been lost.

- **`structured.plan_step` + `structured.final_summary` + `structured.run_command`
  enum mapping table** added to the smoke recipe doc, including the
  three remaining unmapped step-type enums (17, 21, 23, 65, 81, 82,
  83) probed during this work and the rationale for which were
  shipped vs. deferred (errors, redundant terminal output, memory
  recall, full-file snapshots).

### Fixed

- **Bug 1 — token MessageID rewire (join correctness).** The
  initial Path B commit (`5af3771`) emitted token rows with
  MessageID `antigravity-struct:<uuid>:turn:N`. The subsequent
  bug-fix session moved structured ToolEvents to the shared
  `antigravity:<uuid>:turn:N` scheme so they'd join under the right
  per-turn cluster on the dashboard — but token rows already in the
  DB kept the legacy prefix and were orphaned from every joined
  ToolEvent. The antigravity-project-root backfill now runs an
  `UPDATE token_usage SET message_id = ?` keyed by `(source_file,
  source_event_id)`, refreshing the legacy prefix in place.
  Idempotent — re-runs find no rows to update.

- **Bug 2 — coverage-conditional `markdown.planner_response` dedup
  with bridge re-fetch recovery.** The initial Tier 3 work always
  suppressed `markdown.planner_response` rows whenever any
  `structured.assistant_text` existed. For gemini sessions where
  structured covers <10% of LLM calls (e.g. 803add5e: 3/130), this
  wiped 90%+ of the assistant narrative. Both the live recovery
  (`adapter.go::recoverViaLocalGRPC`) and the backfill
  (`backfillAntigravityProjectRoot`) now gate dedup on
  `antigravity.AssistantTextCoverageThreshold = 0.5`. Below that
  ratio, structured is too sparse to be authoritative; markdown
  stays. The backfill ALSO re-fetches the markdown trajectory via
  the bridge for sessions below the threshold, parses
  planner-response-only events, and idempotently INSERTs them to
  recover any rows a prior over-aggressive dedup pass deleted.
  **1,278 markdown.planner_response rows recovered** across the
  sparse-structured sessions on the user's host.

- **Option 1 — time-based MessageID for `structured.run_command`.**
  Tier 4's terminal-snapshot extraction always pinned each command's
  MessageID to the FIRST `1.2.19` step where the snapshot appeared,
  but the command may have actually run hours later — the dashboard
  showed all FB48 run_commands clustered on turn 0 even though the
  python invocation ran ~8 minutes after the conversation started.
  `nearestTokenMessageID(ts, en.TokenEvents, conversationID)` now
  picks the MessageID of the TokenEvent whose Timestamp is closest
  to the command's `t.startSec`. Token timestamps are spread evenly
  across `[StartedAt, EndedAt]`, so nearest-by-time still places
  commands in the correct turn cluster. Live smoke on 803add5e: 8
  commands previously at turn:0 (4) + turn:117 (4) now spread across
  turn:0 (4) + turn:15 (3) + turn:128 (1).

  A new `updateActionMessageID` UPDATE was added to the antigravity
  backfill (mirroring `updateTokenMessageID`) so existing rows pick
  up the new turn assignment — `InsertActions`' ON CONFLICT only
  refreshes `duration_ms`, so without this UPDATE existing rows kept
  stale ids and stayed orphaned from the dashboard's per-turn
  grouping. Live smoke: **69 action message_ids rewired** on the
  first run; idempotent on re-runs.

- **Option 4A — WSL2 routing for Linux-native conversations.**
  `recoverViaLocalGRPC` on WSL2 used to route everything to the
  Windows-side bridge, leaving the ~18 Linux-native `.pb` files
  under `~/.gemini/antigravity/conversations/` permanently
  unrecoverable. The WSL2 branch now attempts Linux-native
  language-server discovery first (via the existing
  `discoverNativeLinux` helper) and falls through to the bridge on
  failure. The attempt is **path-gated to `/home/...` paths** —
  Windows-side `.pb` files (under `/mnt/c/`) are guaranteed not to
  be hosted by a Linux-native server, so the attempt would be
  wasted overhead. Live smoke: ~80% of conversations (the
  Windows-side ones) now skip the Linux-native attempt entirely,
  saving roughly one failed 5s-timeout HTTP call per conversation
  off Run All.

- **Option 4B — bridge cache-dest race.**
  `bridge.go::windowsCacheDestination` previously re-probed the
  `/mnt/c/...` write target on every invocation, racing concurrent
  bridge calls on a shared `.observer-write-probe` marker (one call
  removed the marker before the other read it; the loser surfaced
  `no writable /mnt/c destination`). The resolved destination is now
  cached process-wide via `sync.Once`, and the marker filename is
  PID-suffixed so cross-process first-calls don't race either.
  Live smoke: backfill no longer surfaces the intermittent
  `wsarecv: An existing connection was forcibly closed by the
  remote host` cascade that previously ran the wall time up to
  ~5min on a typical Run All.

### Notes

- **Conversations under `~/.gemini/antigravity/conversations/` that
  weren't loaded by any running language_server** still fail both
  recovery paths and skip with a warning. The fix-by-reopen path
  (open the originating workspace in Antigravity so a fresh
  `language_server_linux_x64` is spawned with the matching
  `--workspace_id`) is operator-side, not observer-side. Flagged in
  the smoke doc.

## [1.4.28] — 2026-05-03

User-flagged correctness batch: project attribution for Windows-side
Codex data, missing time-elapsed metric on the per-message timeline,
broken days filter on the Sessions tab, and same-timestamp ordering.

### Fixed

- **Codex Windows-cwd → cross-mount translation.** Codex on Windows
  records `cwd` as a Windows path like `c:\programsx\regulation`. On
  WSL2, `filepath.Abs` doesn't recognise the drive prefix and treats
  the string as a relative path, prepending the observer's CWD. Then
  `findGitRoot` walks UP that bogus path looking for `.git` — and in
  the worst case lands on observer's own repo. The user's live DB had
  all 1,350 Codex actions misattributed to
  `/home/marmutapp/superbased-observer`. The codex adapter now
  translates Windows-style cwds via the new
  `crossmount.TranslateForeignPath` helper before resolving (`c:\…` →
  `/mnt/c/…` on Linux). Post-fix smoke: all 1,350 actions correctly
  attributed to `/mnt/c/programsx/regulation`.

- **Backfill: `--codex-project-root`.** New backfill (also wired into
  `--all`) re-reads each codex rollout's first `session_meta` line,
  applies the cwd translation, resolves the new project, and UPDATEs
  every cascaded session/action row whose current `project_id`
  differs. Walks crossmount-resolved homes so Windows-side rollouts
  reachable via `/mnt/c/Users/*/.codex` are picked up. Idempotent.

- **Sessions tab now honours the global days filter.** `/api/sessions`
  accepted only `tool` and `project` — the dropdown's `days=N` was
  silently dropped, so a 30-day window still rendered every session
  in history. Endpoint now accepts `days` (alongside the existing
  filters); `loadSessions()` forwards the global window. Total /
  scored counts share the same WHERE clause so pagination stays
  coherent. `days=0` (or omitted) preserves the prior "no time
  filter" behaviour for older callers. Smoke: 30-day window on the
  user's DB returned 157 of 210 total sessions; 7-day returned 23.

- **Per-message timeline: tie-break ordering on equal timestamp.**
  When a synthesized user_prompt and its assistant turn shared a
  wall-clock (the proxy / adapter often stamps both with the same
  millisecond), `sort.SliceStable` preserved insertion order, which
  was non-deterministic w.r.t. role. Comparator now explicitly
  prefers `role=user` on ties so the timeline reads "user said X →
  assistant did Y" consistently.

### Added

- **Per-message wall-clock elapsed metric.** New `elapsed_ms` field
  on `/api/session/<id>/messages` rows: gap from this message's
  timestamp to the next message's, computed across the full sorted
  timeline (so rows near a page boundary still get a correct
  successor). Surfaced as a new "Elapsed" column on the per-message
  timeline. For user rows it approximates "time the assistant took to
  respond"; for assistant rows it approximates "time the user took
  before the next prompt". `null` on the final message in a session.

- **Per-message tool-execution time + per-tool-call duration.**
  Companion field `tool_duration_ms` sums the contained tool_calls'
  `actions.duration_ms`, surfaced as a "Tool time" column. Per-tool
  rows in the expand panel now show their individual duration in
  square brackets. Differs from `elapsed_ms` (wall-clock between
  messages) by excluding model-think time and user typing time.

- **Adapter coverage for `actions.duration_ms`.** Pre-fix only
  copilot-modern (via `elapsedMs`) and a subset of codex paths
  (legacy `duration: {secs, nanos}` struct) populated `duration_ms`.
  Live coverage: claude-code 0/42,338; codex 0/1,350. Two
  fixes:

  - **claude-code:** when matching a `tool_use` block to its later
    `tool_result`, compute the wall-clock gap and write it to
    `DurationMs`. Anthropic's JSONL has no structured per-tool
    elapsed field so the timestamp delta is the only signal.
  - **codex `response_item`:** when matching `function_call` (or
    `custom_tool_call`) to its `function_call_output`, fall back to
    the timestamp gap when no structured duration is on the row
    yet. Newer codex builds bury the duration in flat-text output
    (`Wall time: 32.3 seconds`) or in nested JSON metadata
    (`{"output":"…","metadata":{"duration_seconds":1.1}}`); the gap
    works for both without parsing variant-specific fields.

  Post-fix coverage on the user's live DB: claude-code 91.1%
  (34,965/38,383), codex 86.3% (1,165/1,350).

- **`actions.duration_ms` refreshes on conflict.** Adapter
  improvements above only populate `duration_ms` on INSERT; the
  pre-existing `INSERT OR IGNORE` semantics meant historical
  rows would stay at zero forever even after a `backfill --all`
  rescan. The insert now uses `ON CONFLICT(source_file,
  source_event_id) DO UPDATE SET duration_ms = CASE WHEN
  excluded > 0 AND existing IS NULL/0 THEN excluded ELSE
  existing END` — picks up new values from a re-parse while
  protecting any value that's already populated. Smoke
  simulation: zeroed durations on a session went from 0/210 →
  286/294 (97.3%) after `backfill --all`. Pattern mirrors
  v1.4.27's `token_usage.model` upsert. Side-effect: the
  `InsertActions` return value (and the `ActionsInserted`
  metric on `IngestResult`) now reports "rows touched" rather
  than "actually new rows" because SQLite's `RowsAffected`
  doesn't distinguish insert from update on conflict; the
  contract remained "no row duplication", which is now
  asserted via `SELECT COUNT(*)` in tests.

- **"All time" option on the global window dropdown.** The
  dashboard toolbar previously offered only 7 / 30 / 90 / 365
  days. The new option sends `days=36500`. Every relevant
  endpoint's `intArg(r, "days", 30, 1, 365)` clamp had its
  upper bound bumped to `36500` (~100 years) so the value
  passes through unmodified. `/api/sessions` also accepts
  `days=0` as an explicit "no time filter" sentinel for older
  CLI / API consumers.

### Schema

No migrations.

## [1.4.27] — 2026-05-02

Modern VS Code Copilot Chat support. v1.4.26 noted that the legacy
debug-log auto-flip is irrelevant to users on Copilot Chat ≥0.45,
which writes to entirely different files in a snapshot+patches JSONL
format. This release ingests that format directly.

### Added

- **Modern Copilot Chat parser.** Single Copilot adapter now dispatches
  on path shape between the existing legacy `debug-logs/.../main.jsonl`
  scanner and a new snapshot+patches parser for:
  - `<workspaceStorage>/<ws>/chatSessions/<sessionId>.jsonl` —
    workspace-bound chats.
  - `<globalStorage>/emptyWindowChatSessions/<sessionId>.jsonl` —
    chats opened with no folder attached.

  Each line is one JSON object with a `kind` discriminator. Three line
  types are handled:

  - `kind=0` — full session snapshot. Multiple snapshots can appear in
    one file (VS Code rewrites the snapshot when a session reopens);
    the parser keeps only the LAST one and discards earlier ones'
    accumulated patches.
  - `kind=1` — replace the value at JSON-pointer-style path `k` with
    `v`.
  - `kind=2` — splice insert: insert the elements of `v[*]` into the
    array at path `k` starting at index `i`, or append when `i` is
    omitted. This is how VS Code adds NEW turns to `requests[]` after
    the snapshot was written; without handling it, every turn after
    the first would be silently lost.

  All patch lines are filtered on `k[0]=="requests"` so
  `inputState/attachments` patches drop without ever materializing
  multi-MB inline image payloads (the user's data has 7.8 MB and 14.8
  MB attachment patches that would otherwise dominate memory).

  Idempotence keys on `requestId` (stable across snapshot rewrites)
  via the existing `(source_file, source_event_id)` UNIQUE constraint;
  `fromOffset` is intentionally ignored because snapshot replay has no
  meaningful resumption point.

  **Model resolution.** `Model` now prefers
  `requests[].result.metadata.resolvedModel` — the actual upstream
  model that processed the turn (e.g. `claude-haiku-4-5-20251001`,
  `grok-code-fast-1`) — over `requests[].modelId`, which usually
  carries the user-facing routing choice (`copilot/auto`) when the
  user hasn't pinned a specific model. `modelId` is the fallback when
  `resolvedModel` is absent (e.g. snapshot written before the result
  patch landed).

  **Per-turn token mapping.**
  - `result.metadata.promptTokens` → `InputTokens`
  - `completionTokens` (fallback `result.metadata.outputTokens`) →
    `OutputTokens`
  - Sum of `result.metadata.toolCallRounds[*].thinking.tokens` →
    `ReasoningTokens`
  - Cache token counts are NOT exposed by this format (VS Code only
    emits `cacheType:"ephemeral"` markers on rendered context blocks,
    no count) — same gap as legacy.

### Changed

- **Token usage rows refresh `model` on conflict.** Previously
  `INSERT OR IGNORE INTO token_usage` silently dropped re-inserts,
  so a row first written with a placeholder model
  (`copilot/auto`) kept that label even after a re-scan with an
  improved adapter resolved the actual upstream model
  (`claude-haiku-4-5-20251001`). The insert now uses
  `ON CONFLICT(source_file, source_event_id) DO UPDATE SET
  model = COALESCE(NULLIF(excluded.model, ''), token_usage.model)`,
  which propagates new model values without ever clobbering an
  existing one with an empty string. Counts, cost, source, and
  reliability are still preserved on conflict — they are
  quality-sensitive and a re-parse must not overwrite a
  proxy-accurate value.

- **Dashboard messages: user_prompt rows show per-turn model.**
  When the dashboard's `/api/session/<id>/messages` endpoint
  synthesizes a row for a `user_prompt` action (which has no
  `token_usage` row to join against), it now looks up the peer
  `assistant:<requestId>` row and inherits its model. Falls back
  to `sessions.model` only when the peer's model is empty. Pre-fix
  every user prompt in a multi-turn session displayed the FIRST
  turn's model (`sessions.model` is set once on session creation),
  which surfaced as a real bug whenever Copilot Auto routed
  different turns to different upstream models.

- **Tool-name normalization for camelCase.** `mapToolName` now
  collapses camelCase (`runInTerminal`, `replaceStringInFile`,
  `editFiles`, `viewImage`, `fileSearch`, ...) and snake_case
  (`run_in_terminal`, `replace_string_in_file`, ...) variants into
  a single key by lowercasing and stripping underscores, so the
  legacy and modern formats share one mapping table without parallel
  entries. New entry: `viewImage` → `read_file`.

- **`globalStorage/emptyWindowChatSessions` watch root.** Per-OS
  variant added to `defaultRoots` alongside the existing
  `workspaceStorage` root.

### Smoke

Live test against the user's WSL2 host on 2026-05-02 against
`/mnt/c/Users/auzy_/AppData/Roaming/Code/User/...`:

- 5 modern session files cursored: 3 workspace `chatSessions`, 2
  `emptyWindowChatSessions` — including a 7.8 MB and a 14.8 MB
  attachment-heavy file, both processed without crash.
- 13 Copilot actions captured (2 user prompts + 2 task_complete + 7
  read_file + 2 search_files), 2 token rows.
- 0 Copilot parse warnings.

Recipe in `docs/copilot-modern-smoke-test.md`.

### Schema

No migrations.

## [1.4.26] — 2026-05-02

WSL2/Windows quality bundle: cross-platform tool discovery, watcher
self-recovery, and one-shot enabling of VS Code's legacy Copilot
debug-log writer.

Note on Copilot: the auto-flip targets the legacy `debug-logs/...
main.jsonl` writer. Modern VS Code Copilot Chat (≥0.45) writes to
`workspaceStorage/<ws>/chatSessions/*.jsonl` and
`globalStorage/emptyWindowChatSessions/*.jsonl` in a snapshot+patches
format observer doesn't yet parse — this is queued for v1.4.27. See
`docs/copilot-modern-format.md`.

### Added

- **Cross-mount adapter discovery for WSL2 ↔ Windows installs.** Pre-fix
  observer running in WSL2 only inspected `/home/<u>/...`; tools
  running on the Windows side (Codex, Claude Code, Copilot Chat,
  Cline, Roo Code, OpenClaw, Pi) were silently invisible because
  their data lives at `/mnt/c/Users/<u>/...`. Symmetric problem on
  the reverse: observer in Windows native couldn't see WSL distro
  homes. With this fix:

  - On WSL2 (any Linux host where `/mnt/c/Users` is statable), every
    directory under `/mnt/c/Users` becomes a candidate Windows home.
  - On Windows (any host where `\\wsl.localhost\` is enumerable),
    every `<distro>/home/<user>` becomes a candidate Linux home.
  - On macOS / pure Linux / pure Windows hosts, no extras are
    detected — behavior is identical to pre-fix.

  Each adapter's `WatchPaths` / `defaultRoots` now expands to the
  appropriate subpath under every detected home, with per-OS
  branching where the subpath differs (Copilot/Cline VS Code
  globalStorage, OpenCode desktop variants). Adapters with uniform
  subpaths (Codex, Claude Code, OpenClaw, Pi) just iterate.

  Auto-detection by default; no config knob needed for the common
  case. Detected extras are logged at INFO at startup so the user
  can confirm what was picked up:

      crossmount: detected extra home path=/mnt/c/Users/<u> os=windows origin=wsl-mnt:<u>

  Verified end-to-end on a WSL2/Windows install: 173 of 525
  ingested files came from cross-mount paths post-fix; codex went
  from 0 → 1,350 actions captured, claude-code grew by ~6,500
  Windows-side actions.

  New package `internal/platform/crossmount` with full table-driven
  tests for both bridge directions (using a fakeFS seam — no host
  dependencies).

  **Copilot debug flag is now auto-enabled.** VS Code only writes
  substantive Copilot Chat debug records when
  `github.copilot.chat.advanced.debug` is `true` — pre-fix this was
  off by default on most installs and observer captured nothing
  even after cross-mount discovery found the file. Observer now
  flips the flag automatically across every cross-mount-resolved
  VS Code install at startup, with a loud INFO log identifying the
  exact file and prior value:

      copilot.setup: enabled github.copilot.chat.advanced.debug path=/mnt/c/Users/<u>/.../settings.json prior_value=missing

  Idempotent: subsequent starts log nothing because the flag is
  already true. JSONC-aware (preserves comments and trailing commas
  via `github.com/tailscale/hujson`); preserves the user's existing
  indent style (4-space, 2-space, or tab — auto-detected from the
  file). Tail formatting and member ordering unchanged. Atomic
  write via temp file + rename. See `docs/copilot-setup.md`.

### Fixed

- **Watcher polling fallback recovers from fsnotify event drops.**
  fsnotify is documented to drop Write/Create events on busy or
  virtualized filesystems — WSL2 reading a Windows NTFS mount,
  network FUSE, certain editor write patterns. When that happened,
  the watcher silently sat behind a growing JSONL until the user
  clicked Run All. The dashboard's `⚠ Watcher is behind on N
  file(s)` banner surfaced the state but didn't fix it.

  Fix: a polling goroutine runs alongside the fsnotify event loop
  inside `Watcher.Watch`. Every `[observer.watch].poll_interval_seconds`
  tick (default `2`) it stat()s every known `parse_cursors` row and
  re-runs `processFile` whenever the file has grown past the saved
  offset. Every 15th tick (~30s by default) it does a full root
  walk to also catch never-seen-before files (the same bug class
  for fsnotify Create drops). Disabled by setting
  `poll_interval_seconds = 0`.

  The poll path reuses the existing idempotent ingest:
  `(source_file, source_event_id)` UNIQUE keeps duplicate inserts
  no-ops; `SetCursor` uses MAX() so a poll and an fsnotify-debounced
  fire racing on the same file can never regress the cursor. Logs
  at INFO only when a poll actually advances a cursor — steady-
  state polling is silent. Closes the kickoff's "live-watcher
  reliability on Windows/WSL2" item.

  New: `store.ListCursors` / `store.CursorEntry` + new
  `watcher.Options.PollInterval`. Threaded from
  `cfg.Observer.Watch.PollIntervalSeconds`, which already existed
  in config but wasn't wired anywhere.

## [1.4.25] — 2026-05-01

Three real-data correctness fixes user-flagged after testing v1.4.24
on live codex sessions and the dashboard.

### Fixed

- **Codex `event_msg/token_count` dedup by `total_token_usage`.**
  User-reported inflation: Observer's per-session JSONL token sums
  exceeded Codex's own final cumulative `total_token_usage` figure
  by ~12% on a real rollout (input +122,680, cache_read +88,704,
  output +731, reasoning +279). Root cause: Codex's runtime
  re-emits identical `event_msg/token_count` records (same
  `last_token_usage` AND `total_token_usage`, 2-3s apart). Pre-fix
  the adapter summed both; the second event double-counts. Total is
  monotonic, so any non-advancing total is a re-emission. The
  modern dispatch now tracks the last total fingerprint per
  `SessionID` and skips duplicates. Verified on the user's
  rollout-2026-04-23T00-29-51-019db690 file: post-fix sums match
  Codex's own final cumulative for all four buckets exactly.

- **Watcher-behind banner no longer shows orphan parse_cursors.**
  Pre-fix the `⚠ Watcher is behind on N file(s)` banner included
  paths whose adapter version had been tightened (e.g. older
  copilot adapter matched any `.log` under `GitHub.copilot-chat/`,
  current adapter narrowed to `/debug-logs/main.jsonl`). The
  parse_cursors rows from the broader-match era stayed in the DB;
  the health endpoint reported them as "behind"; Run All couldn't
  recover them because Rescan only walks paths a CURRENT adapter
  recognises. Banner sat forever.

  Fix: `dashboard.Options.RecognizesSessionFile` is a new predicate
  built from the unified `defaultAdapters()` list (extracted from
  `buildWatcher`). The health endpoint tags rows the predicate
  rejects with `orphan_unmatched: true`, lists them in the response
  but EXCLUDES them from `behind_count`. The JS banner filters them
  out. Banner now only fires on genuinely recoverable issues.

- **Sessions-tab + Analysis-Top-sessions ID columns gained the
  v1.4.24 click-to-copy affordance.** v1.4.24 only updated four of
  six ID renders; the main `#sessions-table` ID column rendered
  `<code>aabbcc…</code>` with no `title` and no idCopy, and the
  Analysis tab's `#analysis-top-sessions-table` had a hand-built
  title without idCopy. Both now use `idCopy()` for consistent
  hover + click-to-copy behaviour.

- **Modal overflow belt-and-braces.** Added `overflow-x: hidden` on
  `.modal` and explicit `width: 100%` (not just `max-width`) on
  `.session-messages-scroller`. The previous fix only added
  `max-width: 100%` which has no effect when the parent container
  has no definite width — the scroller followed the table's
  intrinsic width, defeating the scroll constraint.

### Added

- **Codex `response_item.message.role=user` envelope capture.** User
  pointed out a payload with body
  `<environment_context>\n  <cwd>...</cwd>\n  ...\n</environment_context>`
  was silently dropped. v1.4.23 only handled `role=developer` because
  `role=user` overlapped with `event_msg/user_message` (real user
  prompts). Corpus analysis: 118 user-role response_items split
  ~80% plain text (real prompts already covered) / ~17%
  XML-envelope synthetic context injections that look like user
  messages but originate from the runtime. New discriminator:
  trimmed body must start with `<` to qualify as system-prompt-shaped;
  otherwise stays with `event_msg/user_message`. Rows tag with
  `role=user-envelope` so analysts can split synthetic context
  from real user input.

## [1.4.24] — 2026-05-01

UX + reliability: dashboard affordances for truncated content,
session-detail pagination, npm install clarification, and a
proxy-side fix for the "connection reset by peer" upstream errors.

### Fixed

- **Proxy retries once on transient transport errors.** The user
  reported `write tcp ...: connection reset by peer` from the proxy
  forwarding to api.anthropic.com. Root cause: stale keep-alive
  entry in the http.Transport pool — WSL2 / corporate-firewall /
  mobile-hotspot NATs close idle TCP streams faster than our
  pre-fix 90s `IdleConnTimeout`. Two-part fix: (a) tighten
  `IdleConnTimeout` 90s → 30s + cap `MaxIdleConnsPerHost=16`;
  (b) new `doWithRetry` helper retries exactly once on
  {connection reset by peer, broken pipe, use of closed network
  connection, EOF} after closing pooled idle conns. Non-transient
  errors (TLS handshake, dial timeout) bypass retry.

- **Session-detail messages table no longer pushes the modal past
  its bounds.** Wrapped the table in a `.session-messages-scroller`
  with `overflow-x: auto`; expand row (the "N ▾" drop-down's
  content) gets `white-space: normal; word-break: break-word` so
  long target / error text wraps inside the cell instead of
  inheriting the global `td { white-space: nowrap }` and forcing
  horizontal overflow.

### Added

- **Session-detail Messages pagination.** Pre-v1.4.24 the panel
  rendered every message in one go — for large sessions this was
  crashing the browser tab. `handleSessionMessages` now accepts
  `?limit=N&offset=M` (default limit=100, limit=0 for unlimited);
  response shape gains `{total, limit, offset}`. Frontend gets
  prev/next buttons + a 50/100/200 page-size selector. Filter
  ("Tool messages only" vs "All messages") stays client-side and
  applies after server pagination.

- **Click-to-copy on truncated IDs.** Every truncated session_id /
  message_id render across the dashboard (Sessions tab, Actions
  tab, Compression-events tab, Session-detail Messages panel) gets
  a hover-discoverable affordance (dotted underline, cursor:copy)
  and a click handler that writes the full value to the clipboard.
  Single delegated handler keeps the per-row JS work negligible
  even on tables with hundreds of rows.

- **Click-to-expand on truncated text.** Long Target / Error /
  Command / File-path values in the Actions, Discovery, and
  Session-detail tabs render with a `.expandable` wrapper. Click
  toggles between truncated and full text in-place; full value
  lives in `data-full` so no extra fetch is needed.

### Documentation

- **npm README clarifies global-vs-local install.** Pre-fix the
  Install section showed `npm install -g` followed by `observer
  --version` without explaining what happens for users who install
  locally (`npm install` without `-g`) — in that case the binary
  lives at `./node_modules/.bin/observer` and isn't on `$PATH`,
  so the `observer init` / `observer start` examples fail with
  "command not found". README now lists both forms (global +
  `npx observer ...` for local) and cross-links the Troubleshooting
  → EACCES fix for shared / CI machines.

## [1.4.23] — 2026-05-01

Cross-adapter message normalization, Tier 3: system-prompt and
bootstrap-context capture across codex and openclaw. One new
ActionType (`ActionSystemPrompt`); content-hash dedup keeps the DB
size bounded despite codex's 9-18KB prompts being repeated across
nearly every session_meta and turn_context record.

### Added

- **New ActionSystemPrompt constant** in `internal/models/models.go`,
  symmetric to ActionUserPrompt. Adapters emit one row per unique
  prompt body per session; MessageID is "system:<content_hash>" so
  cross-row queries can group occurrences of the same prompt.
  Adapters MUST hash-dedup or the DB would gain hundreds of
  identical rows per session.

- **Codex system prompt capture (3 sources, hash-dedup'd within parse).**
  - `session_meta.base_instructions.text` — the base Codex system
    prompt (~18KB), repeated verbatim in every session_meta record.
    Emit once per unique body, role="base".
  - `turn_context.developer_instructions` — per-turn permissions /
    sandbox / context envelope (~9KB), nearly identical across all
    turns in a session. Emit once per unique body, role="developer".
  - `response_item.payload.type=message` + `payload.role="developer"`
    — mid-turn system instruction injections. Same dedup behaviour;
    assistant + user roles still skipped here because event_msg/
    agent_message + event_msg/user_message already cover those.

- **OpenClaw bootstrap-context custom event.** Pre-v1.4.23 the
  adapter had no `case "custom"` at all — both customType variants
  in real corpora were silently dropped. This commit handles
  customType="openclaw:bootstrap-context:full" by emitting an
  ActionSystemPrompt row carrying the marker payload; "model-snapshot"
  stays no-op'd because model_change already lifts that info.

### Smoke results vs real samples

The 1614-event codex session captures **+11 unique system_prompt
rows** despite the underlying source repeating identical prompts
across hundreds of records. Smaller sessions: 2-3 system_prompt rows
each. Zero parse warnings across all 9 sample rollouts.

## [1.4.22] — 2026-05-01

Cross-adapter message normalization, Tier 2: feature-parity work
that captures meaningful action/event types previously dropped on
the floor. No schema changes; two new ActionType constants
(`ActionTurnAborted`, `ActionContextCompacted`).

### Added

- **Codex `event_msg/mcp_tool_call_end` capture.** Executor side-
  channel for MCP tool calls (server, tool, structured arguments,
  duration, result.Ok|Err). Pre-v1.4.22 the adapter's event_msg
  switch had no case for it: paired calls (Tier 1
  response_item.function_call(list_mcp_resources*)) kept the call's
  terse data; unpaired calls were dropped entirely. Now merges into
  the pending row with Target="server:tool", ToolOutput from
  Ok.content[*].text, Success from Ok.isError=false vs Err.message,
  DurationMs from secs+nanos. Standalone path emits a fresh row.

- **Codex `event_msg/turn_aborted` → ActionTurnAborted.** New
  ActionType for turns interrupted before the model finishes
  generating (user pressed esc / cancelled). Distinct from
  task_complete with success=false: aborted turns never finished, so
  output is partial — the discriminator matters for cost analysis
  (aborts still consumed input/output tokens up to the abort point).

- **Codex `event_msg/view_image_tool_call` capture (merge +
  standalone).** Pre-v1.4.22 the paired form had stale RawToolName
  and the standalone form was dropped. Also fixes a Tier 1
  oversight: `view_image` was in actionMap but missing from
  extractTarget, so Tier 1 view_image rows had empty Target.

- **Codex `event_msg/dynamic_tool_call_request` + response
  pairing.** Runtime-loaded tools (e.g. load_workspace_dependencies).
  Field-name quirk: request uses camelCase (callId, turnId), response
  uses snake_case (call_id, turn_id) — both spellings tolerated.
  Same pattern as exec_command_end / patch_apply_end / mcp_tool_call_end:
  request creates a row, response merges in success / error /
  duration / content_items text body.

- **Codex `compacted` events → ActionContextCompacted (non-
  searchable).** Top-level type="compacted" event Codex emits when
  the model decides to summarize earlier turns. New ActionType,
  distinct from the searchable file-edit set so dashboard filters
  can suppress these rows from action-type browsers while keeping
  them queryable for cost / compaction-frequency analytics. Row
  Target is "<N> msgs, ~<T> tokens"; RawToolInput is JSON
  {messages, bytes_estimate, tokens_estimate} for analytics. The
  paired event_msg/context_compacted is no-op'd to avoid double-
  emission. Per user direction (2026-05-01): "doesn't need to be
  searchable like file edits" — but DO capture token/event info.

- **Codex `response_item.reasoning` forward-compat capture.**
  Current Codex Desktop builds always emit summary:[] (838 reasoning
  items, 0% non-empty in the corpus). The adapter now extracts text
  from summary[*].text when present and threads it into the turn's
  agentMessages cache so future builds (or summary-populating
  variants) inherit it as PrecedingReasoning without further
  changes.

- **OpenClaw `stopReason='error'` → ActionAPIError.** OpenClaw
  assistants emit empty-content messages with stopReason="error"
  + an errorMessage carrying the upstream provider's verbatim
  response (e.g. `400 {"error":"...does not support tools"}`).
  Pre-v1.4.22 the adapter's stop-reason gate only fired on "stop"
  so these were silently dropped. Now emits an ActionAPIError row
  with status-code-prefix discriminator (`http_400` etc.).

### Smoke results vs real samples

The largest inspected codex session (1586 events) now captures
**+17 previously-dropped rows**: 9 context_compacted, 2 turn_aborted,
6 api_error. Zero parse warnings across all 9 codex sample rollouts.

## [1.4.21] — 2026-05-01

Cross-adapter message normalization, Tier 1: closes data-loss bugs
in the codex and cursor adapters that were silently dropping
significant fractions of real-session activity. No schema changes;
new actions enrich what was already a partial view.

### Added

- **Codex `response_item` envelope dispatch.** Codex Desktop wraps
  every assistant tool intent in a `response_item` envelope
  (`payload.type` discriminates `function_call`, `function_call_output`,
  `reasoning`, `message`, `custom_tool_call`, `custom_tool_call_output`,
  `web_search_call`). The adapter previously had no `case "response_item"`
  at all, so on real Desktop sessions the entire `function_call` /
  `function_call_output` stream (~1613/1612 events per inspected
  corpus) was silently dropped — only the side-channel
  `event_msg/exec_command_end` caught ~⅔ of shell calls, leaving
  every `update_plan`, `view_image`, and `list_mcp_resources` call
  missing entirely.

  New `case "response_item"` routes function_call through the same
  `pending[call_id]` machinery as the legacy top-level dispatch.
  Dedup logic in `event_msg/exec_command_end` and
  `event_msg/web_search_end`: when a response_item.function_call
  already landed for the same call_id, the side-channel merges its
  richer fields (command, exit_code, duration, stdout, query) into
  the existing row instead of emitting a duplicate. If only the call
  landed (mid-session truncation, user interrupt) the row stands
  alone; if only the side-channel landed (resume mid-file) the
  legacy emit path handles it. **No double-counting in either
  direction.**

- **Codex `apply_patch` capture (`custom_tool_call` +
  `patch_apply_end`).** Codex Desktop's apply_patch flow writes
  through three separate JSONL events sharing a `call_id`:
  response_item/custom_tool_call (assistant intent + raw patch text),
  event_msg/patch_apply_end (executor result with structured
  `changes` map), and response_item/custom_tool_call_output (string-
  wrapped {output, metadata}). In real corpora `patch_apply_end` lands
  BEFORE `custom_tool_call_output` so the previous step's pattern of
  deleting the pending entry on the *_output event was wrong here —
  patch_apply_end is now treated as the terminal event for apply_patch
  and merges in its structured fields. ~166 patch_apply_end events per
  representative corpus now land as ActionEditFile rows.

- **Codex `event_msg/error` → ActionAPIError.** Codex emits a
  structured event when an upstream API call fails before a turn can
  complete (`usage_limit_exceeded`, rate limit, content-policy block,
  etc.). Pre-v1.4.21 these were silently dropped — same gap claudecode
  had pre-v1.4.20. Mirrors claudecode's ActionAPIError shape.

- **Cursor user_prompt emission from JSONL transcripts.** Cursor's
  agent runtime wraps user prompts in `<user_query>...</user_query>`
  XML before passing them to the model; this wrapper landed verbatim in
  rows on the live-hook path (`beforeSubmitPrompt`). New
  `stripUserQueryWrapper` helper applied in both the live-hook path
  and a new `BuildTranscriptUserPromptEvent` exported function. Strip
  ONLY when both opening and closing tags are present so partial-
  wrapper text (a user typing `<user_query>`) is not damaged.

- **Cursor sub-agent transcript ingestion.** Cursor writes a separate
  JSONL when the parent agent spawns a sub-agent
  (`agent-transcripts/<parent>/subagents/<sub>.jsonl`). Pre-v1.4.21
  these were explicitly skipped — the parent transcript only recorded
  a `Subagent` tool_use; the sub-agent's actual fan-out work
  (WebFetch, ReadFile, sub-prompts) was lost. Now ingested as
  IsSidechain=true rows under the parent session_id, mirroring
  claudecode's sidechain semantics.

- **Cursor tool-name normalizer extensions.** Real cursor transcripts
  contain tool names previously routed to ActionUnknown:
  `ReadLints` → ActionReadFile, `StrReplace` → ActionEditFile,
  `Subagent` (capitalized) → ActionSpawnSubagent (case-insensitive),
  `call_mcp_tool` → ActionMCPCall (parity with the live-hook MCP path),
  `Await` → ActionUnknown (kept Unknown intentionally — control-flow
  primitive with no file/command target).

### Changed

- **CLI flattened**: `observer scan`, `observer watch`, `observer init`,
  `observer uninstall`, `observer serve`, `observer doctor`,
  `observer status`, `observer tail`, `observer prune`, `observer cost`,
  `observer score`, `observer discover`, `observer patterns`,
  `observer learn`, `observer suggest`, `observer dashboard`,
  `observer metrics`, `observer summarize`, and `observer export` are
  now top-level subcommands. The legacy `observer observer <sub>`
  nesting is preserved as a hidden alias group so installed hooks and
  MCP entries from earlier versions keep working without re-init.
- **MCP registration writes `["serve"]` instead of `["observer","serve"]`.**
  Existing entries continue to work via the alias; to migrate to the
  flat form, run `observer uninstall && observer init` (or
  `observer init --force`).

### Fixed

- `internal/adapter/copilot/adapter.go`: `IsSessionFile` now matches
  Windows-formatted fixture paths on Linux hosts (the `filepath.ToSlash`
  no-op on `\` separators caused `TestAdapter_IsSessionFile` to fail
  under `make test`).
- `gofmt -w` on five files dirty since `bb815b5`.

## [1.4.20] — 2026-04-30

Long-context (LC) pricing modeling, full Analysis dashboard tab, and
fully-editable Settings tab with backfill controls. Largest single
release since v1.0 — see PROGRESS.md for the per-section detail.

### Added

- **Long-context pricing tier in the cost engine.** `Pricing` struct
  gains `LongContextThreshold` + per-dimension LC rates (Input,
  Output, CacheRead, CacheCreation, CacheCreation1h). `Compute()`
  now reprices an entire turn at the LC tier when its prompt window
  (`Input + CacheRead + CacheCreation`) exceeds the threshold —
  closes the under-billing gap for Anthropic Sonnet 4 / 4.5 (>200K),
  OpenAI gpt-5.4 / 5.5 (>272K), and Gemini 2.5 Pro / 3.1 Pro Preview
  (>200K). Defaults baked in for every affected SKU; user overrides
  via TOML or the new Settings → Pricing form. Each LC field falls
  back to its standard counterpart when zero so an entry can pin
  only the dimensions that actually change at the LC tier.

- **Analysis dashboard tab.** New tab between Cost and Sessions with
  four bands keyed off a single time-window picker:
  - **Headline KPIs** (6 tiles) — period vs prior period (with Δ%
    colour-coded), month-to-date + linear projection (+ optional
    budget % when `intelligence.monthly_budget_usd` is set),
    effective rate ($/1M output + cache-write tokens), cache
    efficacy (`cache_read / (cache_read + cache_creation)`), LC tier
    surcharge attribution (turns + extra $ from LC repricing), and
    waste $ (Discovery stale-read tokens × blended input rate).
  - **Trend / cross-session deep-dive** — daily stacked-bar with a
    Model / Project / Tool dimension toggle. New cost-engine
    groupings `GroupByDayProject` + `GroupByDayTool` mirror the
    existing `GroupByDayModel`.
  - **What changed / movers** — top 5 cost increases, top 5
    decreases, and new entrants for the chosen dimension.
    `cost.Options.Until` adds a closed-window upper bound so the
    prior-period query is a clean `[Since, Until)` window.
  - **Top expensive sessions + routing efficiency** — sessions
    sorted by cost with `opus` / `lc_tier` / `many_turns` /
    `large_prompt` badges, and a soft "you might have used Sonnet"
    table for trivial Opus sessions (small prompt, low output, no
    LC turns) flagged by a conservative work-profile heuristic.
    Click-through to existing session detail modal.

  Six new endpoints under `/api/analysis/*`: `headline`, `trend`,
  `movers`, `top-sessions`, `routing-suggestions`. All do per-turn
  pricing (LC dispatch is per-request — aggregating tokens at SQL
  before pricing would false-trip the LC tier).

- **Settings dashboard tab.** Last in nav, two-column shell with a
  rail and 10 sections:
  - **Pricing** — table-form editor (rows = active overrides, cols =
    Input/Output/CacheRead/CacheCreation 5m/CacheCreation 1h, with a
    chevron toggling 6 long-context fields per row). Filter input,
    Add Override prompt (auto-fills from baked-in defaults when the
    model id matches), per-row Reset + Delete buttons. Defaults
    reference list at the bottom (collapsed `<details>`, all 95
    baked-in models with an "Override" shortcut). Saves hot-reload
    via `cost.Engine.Reload` — no restart needed.
  - **Backfill** — table of all 14 documented `observer backfill`
    flags with candidate counts (SQL-checkable) or "scan needed"
    (file-walking). Per-row Run button + Run All. Output panel
    surfaces captured stdout incrementally as the subprocess runs;
    multiple modes can run concurrently.
  - **Observer / Watcher / Freshness / Retention / Hooks / Proxy /
    Compression / Intelligence** — per-field forms driven by
    `SECTION_FIELDS` field specs (kind / path / label / help /
    placeholder / select options). Compression keeps a 4-card layout
    matching its sub-struct shape; the others are flat. Save → file
    is rewritten via the same `.bak`-fallback + atomic-rename path
    used for pricing; a "Restart daemon" banner appears at the top
    of the tab with a confirmation dialog.

  Endpoints: `GET /api/config`, `PUT /api/config/pricing`,
  `GET /api/config/pricing/defaults`, `PUT /api/config/section/<name>`,
  `POST /api/admin/restart`, `GET /api/backfill/status`,
  `POST /api/backfill/run`, `GET /api/backfill/jobs/<id>`.

- **API error capture — both JSONL and proxy paths.** Pre-v1.4.20
  upstream API failures (content-policy blocks, rate limits,
  overloaded responses, invalid-request errors) were silently
  dropped on both surfaces: the proxy returned early on non-2xx
  responses, the claudecode adapter skipped `type: "system"` records
  before any system-record handling. v1.4.20 closes both gaps.

  *JSONL adapter:* new `ActionAPIError` action type. The
  claudecode adapter now decodes `type: "system"` +
  `subtype: "api_error"` records. Captured rows carry the upstream
  request_id (joinable to `api_turns.request_id` when both proxy +
  JSONL saw the same call), the specific error class
  (`overloaded_error` / `rate_limit_error` /
  `invalid_request_error`), and the human message. Recursive
  envelope walker handles the 1- and 2-level-nested shapes that
  appear in real logs. Companion `--claudecode-api-errors` backfill
  flag (umbrella'd by `--all`) recovers historical errors.
  Smoke-tested against 344 live JSONL files: 54 errors recovered,
  52 with the specific class attributed correctly.

  *Proxy:* new `api_turns.{http_status, error_class, error_message}`
  columns (migration 013). The proxy now records a zero-token
  api_turn row when the upstream returns 4xx/5xx — both
  non-streaming and streaming paths. `parseErrorBody` handles both
  Anthropic (`{type: "error", error: {…}}`) and OpenAI
  (`{error: {type, message, code}}`) envelope shapes;
  `extractStreamErrorBody` pulls error JSON out of an SSE
  `event: error` data line when the upstream errored mid-stream;
  `extractRequestID` reads `x-request-id` from the response with
  `cf-ray` fallback. `store.InsertAPITurn` validation relaxed for
  error turns (Model may be empty when HTTPStatus != 0 — some
  upstreams reject malformed requests before any model field is
  parsed).

  Sister Settings → Backfill UI gets a Run button for the new
  `--claudecode-api-errors` mode.

- **Long-context Pricing struct + ModelPricing config fields**: 6
  new TOML keys per model (`long_context_threshold`,
  `long_context_input`, `long_context_output`,
  `long_context_cache_read`, `long_context_cache_creation`,
  `long_context_cache_creation_1h`). Threading from
  `IntelligenceConfig.MonthlyBudgetUSD` via `cost.Engine.Reload`.

- **`config.ResolveGlobalPath(override)`**: mirrors the path
  resolution used by `config.Load` so callers can locate the file
  for save-back operations without reimplementing the rule. Threaded
  into `dashboard.Options.ConfigPath` from
  `cmd/observer/{start.go,dashboard.go}`.

- **`cost.BakedInDefaults()`**: returns a fresh copy of
  `defaultPricing` for the dashboard's pricing-defaults reference
  list. Mutating the returned map has no effect on engine state.

- **Watcher recovery path.** `observer scan --force` (new flag) and
  `Watcher.Rescan()` re-walk every JSONL the registered adapters
  claim from offset 0, ignoring `parse_cursors`. The
  `(source_file, source_event_id)` UNIQUE index keeps the pass
  idempotent — rows already in the DB are no-ops, anything the live
  watcher dropped silently gets ingested. `backfill --all` runs the
  rescan first (before the surgical column-update backfills) so a
  single click of "Run all" on the Settings → Backfill tab recovers
  missing data and patches new columns in one shot. Diagnostic for
  the well-known watcher-falls-behind failure mode (fsnotify event
  drops on busy sessions, daemon restart with stale cursors).

- **Watcher-health endpoint + sticky banner.**
  `GET /api/health/watcher` lists every JSONL the watcher knows
  about with its saved `byte_offset` vs the live `file_size` on
  disk, plus how stale the cursor is. The dashboard polls this on
  load and every 60 s; when any file is behind by more than 10 KB,
  a top-of-page banner appears (`Watcher is behind on N file(s)…
  click to recover via Settings → Backfill → Run all`) so the
  silent-data-loss case the v1.4.20 recovery path was built for
  can't sneak past the user again.

- **Toast feedback for Backfill Run buttons.** Generic
  `showToast(id, status, title, detail, autoDismissMs)` helper.
  Click Run → sticky toast top-right with spinner + label.
  The poll loop tails captured stdout and updates the toast detail
  line live (so Run All shows phase transitions:
  `rescan complete: files_processed=346` →
  `is_sidechain backfill complete…` → `✓ recovered N rows`).
  Done auto-dismisses after 8 s; failed stays sticky with the error.

- **Compression events table — tokens / message_id / linked
  session.** `/api/compression/events` rows now carry
  `original_tokens_est` / `compressed_tokens_est` /
  `saved_tokens_est` (computed server-side as `bytes ÷ 4`, matching
  the cost engine's `CompressionStats` heuristic), plus
  `message_id` (sourced from the joined `api_turns.request_id`).
  The dashboard table replaces the verbose
  Original/Compressed/Saved-bytes triplet with a compact
  `Saved (B)` cell (with original→compressed in the tooltip), adds
  a `Saved (tok)` column, and surfaces `Session` (clickable link
  opening the existing detail modal) and `Msg ID` columns.

- **Actions table — message_id column + linked session.** The
  `/api/actions` response now exposes `message_id` (the upstream
  Anthropic `msg_xxx` populated by the claudecode adapter + the
  `--message-id` backfill). The Actions tab adds a `Msg ID` column
  (truncated, full id in tooltip) and converts the `Session` cell
  from a static `<code>` to an accent-coloured link that opens the
  session detail modal — same affordance the Compression events
  table got.

- **`docs/compression-audit.md`** — verifies which Compression
  toggles are wired to a live consumer (Shell ✓, Indexing
  excerpts ✓, Conversation pipeline ✓) versus stubs the v1.4.20
  audit found (`compression.code_graph.*` was duplicate dead
  config — Intelligence's CodeGraph is the real toggle;
  `compression.indexing.embeddings` is an experimental hook with
  no runtime consumer yet). The Compression form removes the
  CodeGraph card and labels Embeddings explicitly as "experimental,
  not yet wired."

- **Per-section purpose blurbs in Settings.** `SECTION_BLURBS` map
  covers all 10 Settings sections — each renders a one-paragraph
  explainer above the form fields describing what the section
  controls and when you'd touch it. Prevents the
  "what is this for?" foot-gun the audit-screenshots flagged on
  Intelligence + others.

### Changed

- **Cost engine made hot-reload-safe.** `Engine.Table *Table` →
  `Engine.table atomic.Pointer[Table]` (private). New
  `Engine.Lookup` / `LookupWithSource` / `Reload(cfg)` wrappers; the
  Settings → Pricing save calls `Reload`, swapping the active table
  via `atomic.Pointer.Store`. In-flight Lookup callers see either
  the old or new table — never a torn state. External `engine.Table`
  access migrated through the wrapper methods (only call sites were
  in `analysis.go` and tests).

- **Dashboard session-detail per-model breakdown.** Previously
  aggregated tokens at SQL level then called `Compute` once on the
  sum — that would false-trip the LC tier whenever a session's
  summed prompt cleared the threshold even if no individual turn
  did. Rewrote to pull individual rows and bucket per-model in Go
  after `Compute`. Same pattern applied to the headline and
  top-sessions endpoints from the start.

- **Backfill subprocess output streams incrementally.**
  `realExecBackfill` switched from `cmd.CombinedOutput()` (all-at-
  once buffer) to `StdoutPipe`/`StderrPipe` with concurrent drain
  goroutines firing an `onChunk` callback per 4 KiB read. The
  registry appends chunks under a mutex; the existing 2 s poll
  surfaces partial output as it accumulates. Output capped at 1 MiB
  with truncation marker.

- **Pricing reference doc.** `docs/pricing-reference.md` Anthropic /
  OpenAI / Gemini sections now describe the LC modeling instead of
  flagging it as a future enhancement; only Gemini Flash long-context
  remains in the out-of-scope list.

- **Backfill table responsive layout.** Scoped `.backfill-table` CSS
  with `table-layout: fixed`, wrapped descriptions, sticky action
  column, drops the Flag column at <900px viewports. Closes the
  horizontal-scroll-on-narrow-screens issue surfaced in the v1.4.20
  audit screenshots.

- **Loading-spinner drift fixed (CSS).** The chart-panel loading
  overlay's spinner used `transform: translate(-50%, -50%)` for
  centering AND `animation: obs-spin` rotating the same property —
  every animation frame replaced the centering translate with a pure
  rotation, snapping the spinner to top-left then back, producing the
  visible drift on the Analysis tab's Daily-spend chart. Centering
  switched to `top/left: calc(50% - 14px)` so the only `transform`
  on the element is the rotation.

- **Help drawer suppressed on tab clicks.** The body-level click
  handler that opened the help drawer for any `data-help`-tagged
  element previously fired on tab-nav clicks too — every tab switch
  popped the drawer open. Now skips clicks where
  `el.closest('nav')` matches AND the element is a `<button>` with
  a `data-tab` attribute. Tab navigation is silent; hover tooltips
  on tabs still show a one-line definition; the drawer is still
  reachable via the `?` button or any non-tab `data-help` click.

- **Link colour for Session-id affordances.** New `.session-link`
  CSS rule pins the new Session link in Compression events + Actions
  tables (and the existing `.row-clickable` cells across Sessions /
  Top expensive sessions / etc.) to `var(--accent)` instead of the
  default browser blue, keeping the dark theme readable.

### Tests

- 30+ new tests across `cost/`, `dashboard/`, and adjacent packages.
  Coverage highlights:
  - LC dispatch (below/above threshold, zero-fallback, threshold-
    disable, prompt-includes-cache-read, defaults round-trip,
    config-override round-trip, Reload swap-while-reading,
    concurrent-safe under `-race`).
  - Analysis endpoints (period vs prior, LC surcharge attribution,
    cache efficacy, budget echo, trend dimensions, movers diff
    math, top-sessions ranking + badges, routing-suggestions
    heuristic correctness).
  - Settings endpoints (GET full struct, no-file-yet defaults,
    pricing save+reload cycle, no-config-path 409, retention
    section save, intelligence save preserves pricing,
    unknown-section 400, backfill 14-mode coverage, pricing
    defaults shape).
  - Backfill run (allowlist rejection, happy path, non-zero exit,
    config-path arg propagation, partial output streamed before
    exit, 1 MiB output cap with truncation marker, unknown job 404).

  Full suite passes under `-race`.

### Migration notes

- New optional config fields:
  - `intelligence.monthly_budget_usd` (float, USD): hides the
    Analysis budget tile when zero/unset.
  - `intelligence.pricing.models.<id>.long_context_threshold`
    (int) + 5 paired LC rate fields. Ignored when zero.

- No new database migrations.

- `cost.Engine.Table` is now a method (`Table()`) rather than an
  exported field. External Go callers using `engine.Table` directly
  must switch to `engine.Lookup` (recommended) or `engine.Table()`.
  Internal callers in this repo were already migrated.

- The dashboard's Settings → Pricing form preserves the `.bak`
  fallback for the prior `config.toml` on every save. Comments are
  lost when the engine re-marshals the file (Option A from the
  planning doc) — the `.bak` is the recovery path for users who
  hand-comment their configs.

## [1.2.1] — 2026-04-23

### Fixed

- **Pidbridge attribution for recent Claude Code versions.** The
  `SessionStart` hook now registers the whole non-shell ancestor chain
  (hook parent → grandparent → ... up to the shell) instead of just
  `os.Getppid()`. Recent Claude Code routes hooks through a short-lived
  Node worker; registering only the worker's PID caused every
  `api_turns` row to land with `session_id = NULL` once the worker
  exited, because the proxy's `/proc/<pid>/status:PPid` walk could not
  cross the dead worker to reach the still-live main process. Walking
  multi-step at registration time guarantees at least one still-live
  PID is in the bridge by the time the proxy looks up traffic.

## [1.2.0] — 2026-04-23

### Added

- **`observer uninstall`** — clean reversal of `observer init`. Removes
  hook entries from `~/.claude/settings.json` and `~/.cursor/hooks.json`
  and the `observer` MCP server entry from `~/.claude.json`,
  `~/.cursor/mcp.json`, and `~/.codex/config.toml`. User-authored hooks
  and other MCP servers are preserved.
- Checksum-based drift detection: uninstall refuses to touch a config
  file that has been modified since install unless `--force` is passed.
- `--purge` flag to additionally delete `~/.observer/` (observer.db,
  config.toml, hook_checksums.json).

### Changed

- Config env-var prefix finished the superbased→observer rename:
  `SUPERBASED_*` overrides now read as `OBSERVER_*` in
  `internal/config/config.go`. Doc comments and test fixtures updated.

## [1.0.0] — 2026-04-17

First stable release. Captures, normalizes, and analyzes tool call activity
from AI coding assistants (Claude Code, Codex, Cursor, Cline, Copilot).

### Core

- **Multi-adapter ingestion** — Claude Code (JSONL), Codex (rollout),
  Cline/Roo Code (JSON), Cursor (hook-based), Copilot (experimental)
- **SQLite storage** — WAL mode, migrations 001–006, pure-Go via
  `modernc.org/sqlite` (no CGO)
- **Freshness engine** — content hashing, stat fast-path, 5-state
  classification (fresh/stale/new/unchanged/unknown)
- **FTS5 excerpt indexing** — searchable tool output excerpts (2KB cap)
- **Secrets scrubbing** — regex-based pipeline (Bearer, AWS, API keys,
  connection strings, env vars)

### Proxy

- **API reverse proxy** — Anthropic + OpenAI, streaming + non-streaming,
  per-turn token/cost logging in `api_turns`
- **Session attribution** — `X-Session-Id` header + Linux `/proc` pid
  bridge via SessionStart hook (migration 004)
- **Conversation compression** — content-type pipeline: per-type
  compressors, importance scoring, budget enforcer, Anthropic cache
  alignment with `cache_control` breakpoint injection
- **OpenAI compression** — Chat Completions MVP (Responses API deferred)

### MCP Server

- **12 tools** — `check_file_freshness`, `get_file_history`,
  `get_session_summary`, `search_past_outputs`, `check_command_freshness`,
  `get_session_recovery_context`, `get_project_patterns`,
  `get_last_test_result`, `get_failure_context`, `get_action_details`,
  `get_cost_summary`, `get_redundancy_report`
- **Codegraph enrichment** — `check_file_freshness` and `get_file_history`
  include `structure: {functions, callers, imports}` when graph DB is available
- **Registration** — auto-configures Claude Code, Cursor, Codex MCP configs

### Intelligence

- **Pattern derivation** — hot files, co-changes, common commands,
  edit-test pairs, onboarding sequences, cross-tool files, knowledge
  snippets (from preceding reasoning)
- **Session quality scoring** — error rate, redundancy ratio, onboarding
  cost, retry cost
- **Cost estimation** — per-model pricing table, proxy + JSONL source
  merging, compression savings tracking
- **Failure correlation** — error categorization, retry detection,
  `observer learn` correction rules
- **`observer suggest`** — generates CLAUDE.md / AGENTS.md / .cursorrules
- **AI session summaries** — Claude Haiku generates 2–4 sentence summaries,
  scrubbed before storage (migration 005)
- **Dashboard** — embedded HTML + `/api/*` JSON endpoints

### Compression

- **Shell output** — TOML-driven filter engine with 6 embedded defaults
  (git, go test, docker, kubectl, cargo, pytest), `observer run <command>`
- **Conversation layer** — message importance scoring, budget enforcement,
  prefix-stable cache alignment, savings metrics in `observer cost`

### Observability

- **Prometheus `/metrics`** — 29 gauge families from `diag.Snapshot`,
  cost engine, and pid bridge; `observer metrics` serves text format 0.0.4
- **`observer doctor`** — DB integrity, hook checksums, MCP registration
  drift, pid bridge health
- **`observer status`** / `observer tail`** — live activity monitoring

### Semantic Search

- **Feature-hashed TF-IDF vectors** — 256-dim, FNV-1a hash trick, stored
  in `action_embeddings` (migration 006)
- **Cosine similarity search** — brute-force scan, gated behind
  `compression.indexing.embeddings = true`

### Codegraph

- **Auto-install** — downloads latest release from GitHub
  (DeusData/codebase-memory-mcp), verifies SHA-256 against `checksums.txt`,
  extracts platform binary
- **Graph queries** — `FunctionsInFile`, `ImportsInFile`, `CallersOf`
  against confirmed `nodes`/`edges` schema

### CLI

`observer scan|watch|init|doctor|status|tail|prune|cost|score|
discover|patterns|learn|suggest|dashboard|metrics|summarize|export`
plus `observer proxy start`, `observer start`, `observer run <cmd>`,
`observer hook`.

## [0.6.0-alpha] — 2026-04-17

Phase 6 Strand A items 1–4: pid bridge, dashboard savings, cache_control
injection, OpenAI conversation compression.

## [0.5.0-alpha] — 2026-04-17

Phase 5: full compression layer (shell + conversation).

## [0.4.0-alpha] — 2026-04-16

Phase 4: intelligence layer (patterns, scoring, cost, dashboard, suggest).

## [0.3.0-alpha] — 2026-04-16

Phase 3: API proxy, MCP server (12 tools), codegraph skeleton, Cursor +
Copilot adapters, doctor/status/tail/prune commands.

## [0.2.0-alpha] — 2026-04-16

Phase 2: freshness engine, failure context, FTS5 indexing, Codex + Cline
adapters, init command with hook registration.

## [0.1.0-alpha] — 2026-04-16

Phase 1: foundation — config, SQLite, migrations, Claude Code adapter,
storage layer, scan + watch commands.
