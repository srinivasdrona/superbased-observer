package antigravity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marmutapp/superbased-observer/internal/adapter"
	"github.com/marmutapp/superbased-observer/internal/models"
	"github.com/marmutapp/superbased-observer/internal/platform/crossmount"
	"github.com/marmutapp/superbased-observer/internal/platform/oscrypt"
	"github.com/marmutapp/superbased-observer/internal/scrub"
)

// Adapter parses Antigravity conversation files.
type Adapter struct {
	scrubber *scrub.Scrubber
	roots    []string

	// NetworkRecovery selects the fallback when local decryption
	// fails. "" / "off" = pure-local; "local" = call running
	// language_server gRPC API. See AntigravityConfig docs.
	networkRecovery string

	// dumpShapeMismatchesDir, when non-empty, enables the Issue #5
	// follow-up debug dump: every GetCascadeTrajectory response that
	// is non-trivial (≥ 10 KiB) but extracts to zero tokens AND
	// empty model writes its raw bytes to <dir>/<conversation_id>.bin
	// so the operator can capture the wire shape for proto-path
	// investigation. Set via WithShapeMismatchDumpDir from the
	// [observer.antigravity] dump_shape_mismatches_dir config.
	dumpShapeMismatchesDir string

	// Process-wide secret cache. Decryption is expensive; the
	// underlying OSCrypt key doesn't change at runtime. Populated
	// lazily on first ParseSessionFile and zeroed on observer
	// shutdown via sync.Once-style behavior in Close (future).
	secretMu  sync.Mutex
	secret    oscrypt.Secret
	secretErr error

	// Index reader cache: keyed by absolute state.vscdb path.
	indexMu    sync.Mutex
	indexCache map[string]*indexCacheEntry

	// Cached language-server discovery — refreshed at most once
	// per scan via lsCacheValidUntil.
	lsMu              sync.Mutex
	lsCache           []LanguageServer
	lsCacheValidUntil int64 // unix nanos

	// Cached workspace_id → path resolution. The reverse-encoding walk
	// is depth-4 BFS over crossmount.AllHomes() — cheap once but not
	// per-conversation. Cache hits AND misses (an empty-string entry
	// is a recorded miss).
	wsResolveMu    sync.Mutex
	wsResolveCache map[string]string

	// Cached working endpoint per language_server. The Endpoints()
	// iteration tries up to 6 URLs (PreferredEndpoint + 2-3 ports * 2
	// protocols, deduped); the heuristic-derived first candidate is
	// often wrong (for one verified host, the heuristic-HTTP port was
	// actually TLS, costing a full ~5s timeout per conversation
	// before fall-through). Cache key is PID:CSRFToken — distinct
	// CSRF means a different language_server generation (process
	// restart), so stale cache entries naturally expire when a new
	// server replaces the old. Value is the URL that succeeded most
	// recently.
	endpointMu    sync.Mutex
	endpointCache map[string]string

	// Persistent unrecoverable-file tracker. Injected by the caller
	// (cmd/observer/main.go wires a shim around store.Store's
	// LookupUnrecoverable / MarkUnrecoverable / ClearUnrecoverable
	// methods that scope all calls to adapter="antigravity"). nil =
	// no persistence; the adapter behaves as if the cache is always
	// a miss. Per CLAUDE.md, the adapter package doesn't depend on
	// store directly — the interface lets the dependency point the
	// right way.
	//
	// Lets the `observer backfill --all` CLI (operator's primary
	// case from the 2026-05-19 handover) short-circuit files that
	// already failed every recovery path on a prior run, instead
	// of re-attempting the ~30s-per-file decrypt+gRPC dance every
	// invocation. (Issue #4 follow-up; supersedes the v1.6.11 first-
	// pass in-memory cache, which only helped the long-running
	// daemon.)
	unrecoverableTracker UnrecoverableTracker
}

// UnrecoverableTracker is the persistent record of files for which
// BOTH local decrypt AND gRPC fallback have failed. The antigravity
// adapter consults it before doing the expensive recovery dance, and
// records failures (or clears them on success) afterwards.
//
// Defined here (not in internal/store) so the adapter package stays
// store-free per the dependency direction in CLAUDE.md's architecture
// quick reference. The concrete implementation in cmd/observer wraps
// *store.Store with adapter="antigravity" scoping.
type UnrecoverableTracker interface {
	// Lookup returns the prior failure reason when (sourceFile,
	// size, mtimeUnix) all match a persisted record. found=false
	// covers both "no record" and "record exists but file has
	// drifted" — caller should retry the full path in either case.
	Lookup(ctx context.Context, sourceFile string, size, mtimeUnix int64) (reason string, found bool, err error)
	// Mark upserts a failure record. Repeated marks on the same
	// path replace prior values (latest size/mtime/reason wins).
	Mark(ctx context.Context, sourceFile string, size, mtimeUnix int64, reason string) error
	// Clear removes the failure record. Called on successful parse
	// so the next file change doesn't get short-circuited by stale
	// state. Idempotent.
	Clear(ctx context.Context, sourceFile string) error
}

// indexCacheEntry holds a parsed trajectorySummaries map for a
// given state.vscdb path, plus the file's mtime at read time so we
// can detect staleness.
type indexCacheEntry struct {
	mtime   int64
	entries map[string]indexEntry
}

// New returns an adapter with platform-default cross-mount roots.
func New() *Adapter {
	return &Adapter{
		scrubber:   scrub.New(),
		roots:      defaultRoots(),
		indexCache: map[string]*indexCacheEntry{},
	}
}

// NewWithOptions customizes scrubber and roots for tests. Pass
// nil scrubber for the default; pass nil/empty roots for default
// platform discovery.
func NewWithOptions(s *scrub.Scrubber, roots ...string) *Adapter {
	if s == nil {
		s = scrub.New()
	}
	if len(roots) == 0 {
		roots = defaultRoots()
	}
	return &Adapter{
		scrubber:   s,
		roots:      roots,
		indexCache: map[string]*indexCacheEntry{},
	}
}

// WithUnrecoverableTracker enables persistent skipping of files that
// already failed every recovery path. nil clears any previously-set
// tracker (i.e. the adapter behaves as cache-miss-always). Returns
// the adapter for chaining; mirrors the pattern used by
// WithNetworkRecovery + cursor.WithSessionHookChecker.
func (a *Adapter) WithUnrecoverableTracker(t UnrecoverableTracker) *Adapter {
	a.unrecoverableTracker = t
	return a
}

// WithShapeMismatchDumpDir enables the Issue #5 debug dump. Pass an
// absolute directory; "" disables. The directory is created on first
// dump. Returns the adapter for chaining.
func (a *Adapter) WithShapeMismatchDumpDir(dir string) *Adapter {
	a.dumpShapeMismatchesDir = dir
	return a
}

// WithNetworkRecovery enables the local-language-server gRPC
// fallback. mode = "off" (default) or "local". Returns the adapter
// for chaining.
func (a *Adapter) WithNetworkRecovery(mode string) *Adapter {
	a.networkRecovery = mode
	return a
}

// Name implements adapter.Adapter.
func (*Adapter) Name() string { return models.ToolAntigravity }

// WatchPaths implements adapter.Adapter.
func (a *Adapter) WatchPaths() []string { return a.roots }

// IsSessionFile implements adapter.Adapter. Matches
// `.gemini/antigravity/conversations/<uuid>.pb` regardless of host
// OS path separators. Other Antigravity .pb subdirs (implicit/,
// user_settings.pb) are out of v1 scope. The under-WatchPaths
// constraint enforces the v1.4.51 dispatch contract — predicates
// self-limit to paths the adapter could actually own.
func (a *Adapter) IsSessionFile(path string) bool {
	if !matchesSessionShape(path) {
		return false
	}
	return adapter.UnderAnyWatchRoot(path, a.WatchPaths())
}

// matchesSessionShape is the path-component string match for
// Antigravity conversation .pb files. Foreign-OS separators are
// normalised so tests and WSL2 /mnt/c paths still match without
// host-OS dependence.
func matchesSessionShape(path string) bool {
	lower := strings.ReplaceAll(strings.ToLower(path), `\`, "/")
	if !strings.HasSuffix(lower, ".pb") {
		return false
	}
	return strings.Contains(lower, "/.gemini/antigravity/conversations/")
}

// ParseSessionFile implements adapter.Adapter.
//
// Cursor semantics: the byte_offset stored in parse_cursors is the
// file size at last successful parse. A file size that hasn't grown
// past fromOffset is treated as "no work to do." Decryption +
// classification run on every size advance.
//
// Errors fall into three buckets:
//   - decryption failure (no validating plaintext): warning + advance
//     cursor so we don't retry forever on a permanently-bad file.
//   - secret retrieval failure: warning + DON'T advance cursor — the
//     OSCrypt key may become available later (e.g. user unlocks
//     keychain), in which case we want to retry.
//   - file system error: hard error returned to caller.
func (a *Adapter) ParseSessionFile(ctx context.Context, path string, fromOffset int64) (adapter.ParseResult, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return adapter.ParseResult{}, fmt.Errorf("antigravity.ParseSessionFile: stat: %w", err)
	}
	res := adapter.ParseResult{NewOffset: fi.Size()}
	if fi.Size() == 0 {
		return res, nil
	}
	if fromOffset == fi.Size() {
		// Cursor already at current size; the watcher woke us with a
		// non-content event. No work.
		return res, nil
	}

	// Unrecoverable-file short-circuit. If this path's last full
	// attempt (decrypt + gRPC fallback) failed AND the file's
	// size+mtime are unchanged since, skip the entire ~30s dance.
	// Mirrors the documented Antigravity-on-WSL2 unrecoverable case:
	// Windows .pb files whose Linux-native language_server doesn't
	// host the conversation AND oscrypt can't decrypt (cipher mode
	// unknown). Cache is process-local (in-memory) — daemon
	// `observer start` benefits across triggered Rescans; one-shot
	// CLI invocations don't (process exits before re-visiting).
	// (Issue #4)
	if reason := a.lookupUnrecoverable(ctx, path, fi); reason != "" {
		return adapter.ParseResult{
			NewOffset: fi.Size(),
			Warnings: []string{
				fmt.Sprintf("antigravity: %s previously unrecoverable (%s); skipping retry until file changes",
					filepath.Base(path), reason),
			},
		}, nil
	}

	secret, err := a.lookupSecret()
	if err != nil {
		return adapter.ParseResult{
			NewOffset: fromOffset, // hold cursor — secret may come back
			Warnings: []string{
				fmt.Sprintf("antigravity: OSCrypt secret retrieval failed (%v); will retry on next event", err),
			},
		}, nil
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return adapter.ParseResult{}, fmt.Errorf("antigravity.ParseSessionFile: read: %w", err)
	}

	plaintext, strategy, err := decryptOne(body, secret)
	if err != nil {
		// Local decrypt failed. If the user opted into
		// network_recovery="local", try the running
		// language_server's gRPC ConvertTrajectoryToMarkdown.
		if a.networkRecovery == "local" {
			if recRes, recWarn, recErr := a.recoverViaLocalGRPC(ctx, path, fi); recErr == nil {
				recRes.NewOffset = fi.Size()
				if recWarn != "" {
					recRes.Warnings = append(recRes.Warnings, recWarn)
				}
				// On success, clear any prior unrecoverable mark so
				// the file gets re-processed normally next time.
				a.clearUnrecoverable(ctx, path)
				return recRes, nil
			} else {
				// Recovery failed too — mark unrecoverable so the
				// next ParseSessionFile call (e.g. another
				// `observer backfill --all` invocation, or a
				// dashboard-triggered Rescan) short-circuits
				// without re-attempting the ~30s decrypt+gRPC
				// dance until the file actually changes. (Issue #4)
				a.markUnrecoverable(ctx, path, fi, fmt.Sprintf("decrypt+grpc: %v / %v", err, recErr))
				return adapter.ParseResult{
					NewOffset: fi.Size(),
					Warnings: []string{
						fmt.Sprintf("antigravity: decrypt failed for %s (%v); language-server gRPC fallback also failed (%v). Skipping.",
							filepath.Base(path), err, recErr),
					},
				}, nil
			}
		}
		// Pure-local mode (default): advance cursor so we don't
		// retry forever. Warning surfaces the gap. Also mark
		// unrecoverable so subsequent Rescans short-circuit.
		a.markUnrecoverable(ctx, path, fi, fmt.Sprintf("decrypt: %v (no network_recovery)", err))
		return adapter.ParseResult{
			NewOffset: fi.Size(),
			Warnings: []string{
				fmt.Sprintf("antigravity: decrypt failed for %s (%v); skipping. Set [observer.antigravity] network_recovery = \"local\" in config to fall back via the running language_server's gRPC API.", filepath.Base(path), err),
			},
		}, nil
	}
	// Successful decrypt — drop any prior unrecoverable mark since
	// the file is now readable (oscrypt key may have become
	// available, e.g. user unlocked keychain).
	a.clearUnrecoverable(ctx, path)

	conversationID := uuidFromFilename(path)
	indexEntry := a.lookupIndexEntry(path, conversationID)

	emitted, classifyWarnings, err := a.classify(path, conversationID, plaintext, indexEntry)
	if err != nil {
		return adapter.ParseResult{
			NewOffset: fi.Size(),
			Warnings: append(classifyWarnings,
				fmt.Sprintf("antigravity: classify failed for %s (%v) [strategy=%s]", filepath.Base(path), err, strategy)),
		}, nil
	}
	res.ToolEvents = emitted.ToolEvents
	res.TokenEvents = emitted.TokenEvents
	res.Warnings = append(res.Warnings, classifyWarnings...)
	if len(emitted.ToolEvents) == 0 && len(emitted.TokenEvents) == 0 {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("antigravity: %s decrypted (strategy=%s, %d bytes plaintext) but no events extracted; classifier may need tuning",
				filepath.Base(path), strategy, len(plaintext)))
	}
	return res, nil
}

// recoverViaLocalGRPC tries the language_server's
// ConvertTrajectoryToMarkdown gRPC endpoint and parses the returned
// Markdown into events.
//
// On WSL2, the language_server is bound to Windows-side 127.0.0.1
// which isn't reachable from Linux. We delegate to the
// antigravity-bridge.exe helper (which runs Windows-side via
// powershell.exe) — it does the discovery + gRPC call and returns
// the Markdown via stdout. On native macOS / native Windows /
// non-WSL Linux, we make the call directly via Go's HTTP/2 client.
func (a *Adapter) recoverViaLocalGRPC(ctx context.Context, path string, fi os.FileInfo) (adapter.ParseResult, string, error) {
	conversationID := uuidFromFilename(path)
	idxEntry := a.lookupIndexEntry(path, conversationID)
	projectRoot := "[antigravity]"
	if idxEntry != nil && idxEntry.workspaceURI != "" {
		projectRoot = decodeFileURIToRoot(idxEntry.workspaceURI)
	}
	ts := time.Now().UTC()
	if idxEntry != nil && !idxEntry.created.IsZero() {
		ts = idxEntry.created
	}

	wsl := isWSL()
	tracef("recover %s wsl=%v", conversationID, wsl)

	// On WSL2, language_server discovery yields Windows-side processes
	// listening on Windows loopback. Linux loopback is a different
	// network namespace, so dialing those endpoints from Linux always
	// fails with `connection refused`. Skip the dial loop entirely
	// and route through the Windows-side bridge, which runs its own
	// discovery and makes the gRPC call from where the ports are
	// actually reachable.
	//
	// Issue A fix: SOME conversations live on the Linux side
	// (~/.gemini/antigravity/conversations/<uuid>.pb) and are only
	// hosted by a Linux-native language_server — the Windows bridge
	// can't reach them. Try Linux-native discovery first; if a
	// Linux-native server has the conversation, use it. Fall through
	// to the bridge for everything else (Windows-side conversations,
	// or hosts where no Linux server is running).
	//
	// Path-based gating (added 2026-05-05 after a Run All trace
	// showed every conversation hitting Linux-native pid=2259 with
	// `broken pipe` / `unexpected EOF` and falling through to the
	// bridge anyway): only attempt Linux-native when the .pb file is
	// itself under /home/ — Windows-side .pb files (under /mnt/c/)
	// are guaranteed not to be hosted by a Linux-native server, so
	// the attempt is wasted overhead. This trims roughly one failed
	// 5s-timeout HTTP call per Windows-side conversation from the
	// backfill (~80% of conversations on a typical WSL2 host).
	if wsl && strings.HasPrefix(path, "/home/") {
		if linuxServers, _ := discoverNativeLinux(); len(linuxServers) > 0 {
			// Workspace matching: each language_server only hosts
			// conversations from ITS workspace. Talking to a non-
			// matching server returns an empty stub that produces 0
			// events — the symptom that prompted this fix
			// (2026-05-10). Sort matching servers first, fall through
			// to non-matchers as last resort with a warning.
			wantWS := ""
			if idxEntry != nil {
				wantWS = workspaceIDFromURI(idxEntry.workspaceURI)
			}
			ordered := sortServersByWorkspaceMatch(linuxServers, wantWS)
			for _, ls := range ordered {
				wsMatch := wantWS != "" && ls.WorkspaceID == wantWS
				md, useEndpoint, ok := a.tryConvertTrajectoryAcrossEndpoints(ctx, ls, conversationID, 5*time.Second)
				if !ok {
					continue
				}
				tracef("linux-native pid=%d %s OK (%d bytes, ws=%q match=%v)",
					ls.PID, useEndpoint, len(md), ls.WorkspaceID, wsMatch)
				enrichment := a.fetchStructuredEnrichmentNativeAt(ctx, ls, useEndpoint, conversationID, projectRoot, path)
				if !enrichment.StartedAt.IsZero() {
					ts = enrichment.StartedAt
				}
				skipInline := len(enrichment.ToolEvents) > 0
				skipUserInputs := hasStructuredUserPrompts(enrichment.ToolEvents)
				skipPlanner := structuredAssistantTextCoverage(enrichment) >= AssistantTextCoverageThreshold
				if isWrongWorkspaceStub(enrichment) {
					// Wrong-workspace stub guard. Checked BEFORE
					// parseMarkdownConversation runs because the stub
					// markdown still parses as a fake user_prompt
					// event — the pre-2026-05-19 guard checked the
					// merged result and missed this case (see
					// isWrongWorkspaceStub doc comment). Try the
					// next server.
					tracef("linux-native pid=%d returned wrong-workspace stub (no structured payload); trying next server", ls.PID)
					continue
				}
				res := parseMarkdownConversation(path, conversationID, projectRoot, ts, a.scrubber, md, skipInline, skipUserInputs, skipPlanner)
				applyStructuredEnrichment(&res, enrichment)
				// Project-root recovery: when idxEntry was nil
				// (live in-progress conversation not yet in
				// trajectorySummaries) but we have a workspace_id from
				// the language_server, invert the encoding to find a
				// real path and overwrite the "[antigravity]"
				// placeholder.
				if projectRoot == "[antigravity]" && ls.WorkspaceID != "" {
					if resolved := a.resolveWorkspaceIDToPathCached(ls.WorkspaceID); resolved != "" {
						tracef("linux-native pid=%d resolved workspace_id %q to path %q (idx miss; live conversation)",
							ls.PID, ls.WorkspaceID, resolved)
						applyResolvedProjectRoot(&res, resolved)
					}
				}
				tracef("linux-native pid=%d extracted %d tool(s), %d token row(s), model=%q",
					ls.PID, len(res.ToolEvents), len(res.TokenEvents), enrichment.Model)
				return res, "", nil
			}
		}
	}
	if wsl {
		// Linux-side conversations (~/.gemini/antigravity/conversations/
		// <uuid>.pb) are unreachable from the Windows-side bridge — the
		// bridge can't read files behind WSL's filesystem boundary.
		// Linux-native discovery above was the only viable path; if we
		// got here, a Linux-native language_server either isn't running
		// or doesn't host this conversation. Return a single-line hint
		// instead of burning ~3s on a guaranteed-to-fail bridge call
		// (the structured-recovery path tries the same dead bridge for
		// another ~3s, so each unrecoverable conversation costs ~6s on
		// a Run All — adds up fast at 24 of them).
		if strings.HasPrefix(path, "/home/") {
			tracef("skip wsl bridge for linux-side path %s", path)
			return adapter.ParseResult{}, "", fmt.Errorf(
				"no Linux-native language_server hosts this conversation; reopen the originating workspace in Antigravity to recover")
		}
		bridgeBinary, locErr := locateBridgeBinary()
		if locErr != nil {
			tracef("bridge_locate_err=%v", locErr)
			return adapter.ParseResult{}, "", locErr
		}
		tracef("bridge=%s", bridgeBinary)
		md, err := invokeBridgeFromWSL(ctx, conversationID, 30*time.Second)
		if err != nil {
			tracef("bridge err: %v", err)
			return adapter.ParseResult{}, "", fmt.Errorf("bridge: %w", err)
		}
		tracef("bridge OK (%d bytes)", len(md))
		// Best-effort structured-trajectory enrichment. Per-conversation
		// fetch via the same bridge, ~250ms. On failure we keep the
		// markdown result unchanged — observer still records actions,
		// just without model attribution or token rows.
		enrichment := a.fetchStructuredEnrichmentWSL(ctx, conversationID, projectRoot, path)
		if !enrichment.StartedAt.IsZero() {
			tracef("structured ts override: %s -> %s", ts.Format(time.RFC3339), enrichment.StartedAt.Format(time.RFC3339))
			ts = enrichment.StartedAt
		}
		// Tier 1 dedup: when structured emits ToolEvents (artifact
		// edits, file views), suppress markdown's `*Edited file…*` /
		// `*Viewed…*` inline-tool extraction so the same edit doesn't
		// land twice. Tier 2 dedup: when structured emits
		// user_prompt rows, also suppress markdown's `### User Input`
		// turns. Tier 3 dedup: suppress markdown's `### Planner
		// Response` turns ONLY when structured assistant_text covers
		// >= AssistantTextCoverageThreshold of LLM calls — gemini
		// sessions emit far fewer 1.2.20 PLANNER_RESPONSE steps and
		// markdown carries the missing narrative. Below threshold,
		// keep markdown so the dashboard's per-token-row expand has
		// content to show.
		skipInline := len(enrichment.ToolEvents) > 0
		skipUserInputs := hasStructuredUserPrompts(enrichment.ToolEvents)
		skipPlanner := structuredAssistantTextCoverage(enrichment) >= AssistantTextCoverageThreshold
		res := parseMarkdownConversation(path, conversationID, projectRoot, ts, a.scrubber, md, skipInline, skipUserInputs, skipPlanner)
		applyStructuredEnrichment(&res, enrichment)
		return res, "", nil
	}

	// Native macOS / native Windows / non-WSL Linux: discover the
	// local language_servers and call them directly via Go's HTTP/2
	// client. The discovered endpoints are on the same loopback as
	// this process.
	servers, discoveryErr := a.discoverLanguageServersCached()
	if discoveryErr != nil {
		tracef("discovery_err=%v", discoveryErr)
		return adapter.ParseResult{}, "", fmt.Errorf("language-server discovery failed: %w", discoveryErr)
	}
	tracef("discovered %d server(s)", len(servers))
	for i, ls := range servers {
		tracef("  [%d] pid=%d http=%d https=%d workspace=%q endpoint=%q",
			i, ls.PID, ls.HTTPPort, ls.HTTPSPort, ls.WorkspaceID, ls.PreferredEndpoint())
	}
	wantWS := ""
	if idxEntry != nil {
		wantWS = workspaceIDFromURI(idxEntry.workspaceURI)
	}
	ordered := sortServersByWorkspaceMatch(servers, wantWS)
	var lastErr error
	for _, ls := range ordered {
		wsMatch := wantWS != "" && ls.WorkspaceID == wantWS
		md, useEndpoint, ok := a.tryConvertTrajectoryAcrossEndpoints(ctx, ls, conversationID, 15*time.Second)
		if !ok {
			lastErr = fmt.Errorf("all endpoints for pid=%d failed", ls.PID)
			continue
		}
		tracef("grpc pid=%d %s OK (%d bytes, ws=%q match=%v)", ls.PID, useEndpoint, len(md), ls.WorkspaceID, wsMatch)
		enrichment := a.fetchStructuredEnrichmentNativeAt(ctx, ls, useEndpoint, conversationID, projectRoot, path)
		if !enrichment.StartedAt.IsZero() {
			ts = enrichment.StartedAt
		}
		if isWrongWorkspaceStub(enrichment) {
			// Wrong-workspace stub guard (see isWrongWorkspaceStub
			// doc comment). Same fix as the linux-native loop above.
			tracef("grpc pid=%d returned wrong-workspace stub (no structured payload); trying next server", ls.PID)
			continue
		}
		skipInline := len(enrichment.ToolEvents) > 0
		skipUserInputs := hasStructuredUserPrompts(enrichment.ToolEvents)
		skipPlanner := structuredAssistantTextCoverage(enrichment) >= AssistantTextCoverageThreshold
		res := parseMarkdownConversation(path, conversationID, projectRoot, ts, a.scrubber, md, skipInline, skipUserInputs, skipPlanner)
		applyStructuredEnrichment(&res, enrichment)
		if projectRoot == "[antigravity]" && ls.WorkspaceID != "" {
			if resolved := a.resolveWorkspaceIDToPathCached(ls.WorkspaceID); resolved != "" {
				tracef("grpc pid=%d resolved workspace_id %q to path %q (idx miss; live conversation)",
					ls.PID, ls.WorkspaceID, resolved)
				applyResolvedProjectRoot(&res, resolved)
			}
		}
		tracef("grpc pid=%d extracted %d tool(s), %d token row(s), model=%q",
			ls.PID, len(res.ToolEvents), len(res.TokenEvents), enrichment.Model)
		return res, "", nil
	}
	if lastErr != nil {
		return adapter.ParseResult{}, "", fmt.Errorf("all %d language servers failed: %w", len(servers), lastErr)
	}
	return adapter.ParseResult{}, "", fmt.Errorf("no usable language_server endpoint")
}

// sortServersByWorkspaceMatch returns a copy of servers ordered with
// the wantWS-matching ones first, then the others. Used by
// recoverViaLocalGRPC to prefer the language_server that actually
// hosts the conversation. wantWS == "" returns the input unchanged
// (no matching info available, treat all servers equally).
func sortServersByWorkspaceMatch(servers []LanguageServer, wantWS string) []LanguageServer {
	if wantWS == "" || len(servers) == 0 {
		return servers
	}
	matches := make([]LanguageServer, 0, len(servers))
	others := make([]LanguageServer, 0, len(servers))
	for _, s := range servers {
		if s.WorkspaceID == wantWS {
			matches = append(matches, s)
		} else {
			others = append(others, s)
		}
	}
	return append(matches, others...)
}

// tryConvertTrajectoryAcrossEndpoints iterates ls.Endpoints() until
// one returns a non-error markdown response. Returns the markdown,
// the endpoint that worked, and true on success — or ("", "", false)
// if every endpoint errored. Each endpoint failure is logged via
// tracef so users can diagnose port-protocol mismatches.
//
// This replaces the previous "trust ls.PreferredEndpoint()" behavior:
// the heuristic that picks HTTP-vs-HTTPS for a given port is
// unreliable across language_server versions, so we iterate.
//
// Deprecated: prefer Adapter.tryConvertTrajectoryAcrossEndpoints
// which consults the per-server endpoint cache to skip the
// heuristic-bad first attempt on subsequent calls.
func tryConvertTrajectoryAcrossEndpoints(ctx context.Context, ls LanguageServer, conversationID string, timeout time.Duration) (string, string, bool) {
	endpoints := ls.Endpoints()
	if len(endpoints) == 0 {
		tracef("pid=%d has no endpoints to try", ls.PID)
		return "", "", false
	}
	for _, endpoint := range endpoints {
		md, err := callConvertTrajectory(ctx, endpoint, ls.CSRFToken, conversationID, timeout)
		if err != nil {
			tracef("pid=%d %s err: %v", ls.PID, endpoint, err)
			continue
		}
		return md, endpoint, true
	}
	return "", "", false
}

// tryConvertTrajectoryAcrossEndpoints is the cache-aware variant.
// Looks up the cached working endpoint for ls (key: PID:CSRFToken);
// if hit, tries the cached endpoint FIRST. On cache-hit success,
// returns immediately — saves the ~5s timeout we'd otherwise pay on
// the heuristic-bad first candidate when PreferredEndpoint() is
// wrong (verified case: pid 694 had heuristic-HTTP=37933 but the
// actual TLS port; cache pin to 35989 saves the 5s burn per
// conversation).
//
// On cache-hit failure (e.g. server restarted, port changed), falls
// through to the full ls.Endpoints() iteration and updates the cache
// with the new working endpoint.
func (a *Adapter) tryConvertTrajectoryAcrossEndpoints(ctx context.Context, ls LanguageServer, conversationID string, timeout time.Duration) (string, string, bool) {
	cacheKey := endpointCacheKey(ls)
	if cached := a.cachedEndpoint(cacheKey); cached != "" {
		md, err := callConvertTrajectory(ctx, cached, ls.CSRFToken, conversationID, timeout)
		if err == nil {
			return md, cached, true
		}
		tracef("pid=%d cached endpoint %s err: %v (will re-iterate)", ls.PID, cached, err)
		a.invalidateEndpoint(cacheKey)
	}
	endpoints := ls.Endpoints()
	if len(endpoints) == 0 {
		tracef("pid=%d has no endpoints to try", ls.PID)
		return "", "", false
	}
	for _, endpoint := range endpoints {
		md, err := callConvertTrajectory(ctx, endpoint, ls.CSRFToken, conversationID, timeout)
		if err != nil {
			tracef("pid=%d %s err: %v", ls.PID, endpoint, err)
			continue
		}
		a.cacheEndpoint(cacheKey, endpoint)
		return md, endpoint, true
	}
	return "", "", false
}

// endpointCacheKey returns the per-server identity used to key the
// endpoint cache. PID alone isn't enough (PIDs are reused after
// restart); CSRFToken is generated fresh each language_server start
// and is the stable identity within a process generation.
func endpointCacheKey(ls LanguageServer) string {
	return fmt.Sprintf("%d:%s", ls.PID, ls.CSRFToken)
}

// lookupUnrecoverable consults the persistent tracker for path. When
// no tracker is wired (test paths, smoke runs) this is a no-op
// returning "". The tracker itself handles the size+mtime drift
// check internally — if the file changed, it returns found=false so
// the caller retries the full recovery path. (Issue #4)
func (a *Adapter) lookupUnrecoverable(ctx context.Context, path string, fi os.FileInfo) string {
	if a.unrecoverableTracker == nil {
		return ""
	}
	reason, found, err := a.unrecoverableTracker.Lookup(ctx, path, fi.Size(), fi.ModTime().Unix())
	if err != nil {
		// Soft-fail: a tracker error must not block parsing. Log via
		// tracef so it surfaces in debug logs without poisoning the
		// adapter's success path.
		tracef("unrecoverable_lookup_err path=%s err=%v", path, err)
		return ""
	}
	if !found {
		return ""
	}
	return reason
}

// markUnrecoverable persists a failure record. The tracker upserts
// the (path, size, mtime, reason) tuple so a re-failed-then-re-marked
// file pins to the latest content. No-op when no tracker is wired.
// (Issue #4)
func (a *Adapter) markUnrecoverable(ctx context.Context, path string, fi os.FileInfo, reason string) {
	if a.unrecoverableTracker == nil {
		return
	}
	if err := a.unrecoverableTracker.Mark(ctx, path, fi.Size(), fi.ModTime().Unix(), reason); err != nil {
		tracef("unrecoverable_mark_err path=%s err=%v", path, err)
	}
}

// clearUnrecoverable removes any persisted failure record for path.
// Idempotent. No-op when no tracker is wired. (Issue #4)
func (a *Adapter) clearUnrecoverable(ctx context.Context, path string) {
	if a.unrecoverableTracker == nil {
		return
	}
	if err := a.unrecoverableTracker.Clear(ctx, path); err != nil {
		tracef("unrecoverable_clear_err path=%s err=%v", path, err)
	}
}

func (a *Adapter) cachedEndpoint(key string) string {
	a.endpointMu.Lock()
	defer a.endpointMu.Unlock()
	return a.endpointCache[key]
}

func (a *Adapter) cacheEndpoint(key, endpoint string) {
	a.endpointMu.Lock()
	defer a.endpointMu.Unlock()
	if a.endpointCache == nil {
		a.endpointCache = make(map[string]string)
	}
	a.endpointCache[key] = endpoint
}

func (a *Adapter) invalidateEndpoint(key string) {
	a.endpointMu.Lock()
	defer a.endpointMu.Unlock()
	delete(a.endpointCache, key)
}

// numEvents returns the total ToolEvent + TokenEvent count for a
// ParseResult. Retained for backwards compatibility; the per-loop
// stub guard now uses isWrongWorkspaceStub on the structured
// enrichment directly (see below for why).
func numEvents(res adapter.ParseResult) int {
	return len(res.ToolEvents) + len(res.TokenEvents)
}

// isWrongWorkspaceStub returns true when a structured-trajectory
// response carries no semantic payload — no model name, no per-turn
// token rows, no tool events. This is the wire signature of an
// Antigravity language_server that doesn't host the requested
// conversation: it returns a syntactically-valid but content-empty
// response (e.g. 430 KiB of envelope with zero `1.3` per-turn
// entries).
//
// Why this matters: the previous guard at the iteration sites
// checked `numEvents(res) == 0` on the MERGED result (markdown
// extraction + structured enrichment). But ConvertTrajectoryToMarkdown
// against a wrong-workspace server returns a tiny stub markdown
// (e.g. 244 bytes) that still parses as one user_prompt event, so
// numEvents = 1 and the guard never fired — the wrong server's
// stub was accepted as real and the correct server (with the actual
// token data) was never tried. The maintainer's e371fdb1 session
// surfaced this 2026-05-19: pid=1339 had wrong workspace and returned
// the stub; pid=1340 had the real 17-turn / 2,700+ token data.
//
// Treating the structured payload as the authoritative "is this a
// real response" signal eliminates the misclassification — a server
// that hosts the conversation always populates at least the model
// name (verified across the live corpus's 122 working sessions).
func isWrongWorkspaceStub(en StructuredEnrichment) bool {
	return en.Model == "" && len(en.TokenEvents) == 0 && len(en.ToolEvents) == 0
}

// resolveWorkspaceIDToPathCached looks up wsID in the adapter's cache;
// on miss, walks crossmount.AllHomes() roots via resolveWorkspaceIDToPath
// and stores the result (including empty-string misses, so subsequent
// lookups don't re-walk the FS for the same workspace_id).
//
// Used by recoverViaLocalGRPC to recover a project root for live
// in-progress conversations that aren't yet indexed in
// trajectorySummaries: the language_server has the workspace_id in
// its --workspace_id flag, and inverting that encoding by walking
// candidate home roots gives us a usable project path until
// Antigravity flushes the next checkpoint.
func (a *Adapter) resolveWorkspaceIDToPathCached(wsID string) string {
	if wsID == "" {
		return ""
	}
	a.wsResolveMu.Lock()
	defer a.wsResolveMu.Unlock()
	if a.wsResolveCache == nil {
		a.wsResolveCache = make(map[string]string)
	}
	if p, ok := a.wsResolveCache[wsID]; ok {
		return p
	}
	roots := make([]string, 0)
	for _, h := range crossmount.AllHomes() {
		if h.Path != "" {
			roots = append(roots, h.Path)
		}
	}
	resolved := resolveWorkspaceIDToPath(wsID, roots, 4)
	a.wsResolveCache[wsID] = resolved
	return resolved
}

// applyResolvedProjectRoot rewrites every ToolEvent/TokenEvent's
// ProjectRoot from the placeholder "[antigravity]" (or empty) to the
// resolved real path. Called from recoverViaLocalGRPC when idxEntry
// was nil but the language_server's workspace_id resolved to a real
// directory.
func applyResolvedProjectRoot(res *adapter.ParseResult, resolved string) {
	if resolved == "" {
		return
	}
	for i := range res.ToolEvents {
		if res.ToolEvents[i].ProjectRoot == "" || res.ToolEvents[i].ProjectRoot == "[antigravity]" {
			res.ToolEvents[i].ProjectRoot = resolved
		}
	}
	for i := range res.TokenEvents {
		if res.TokenEvents[i].ProjectRoot == "" || res.TokenEvents[i].ProjectRoot == "[antigravity]" {
			res.TokenEvents[i].ProjectRoot = resolved
		}
	}
}

// fetchStructuredEnrichmentWSL pulls the GetCascadeTrajectory payload
// via the Windows-side bridge and parses it. Errors are swallowed
// (logged as a trace line); callers must treat the returned
// enrichment as opportunistic.
func (a *Adapter) fetchStructuredEnrichmentWSL(ctx context.Context, conversationID, projectRoot, path string) StructuredEnrichment {
	raw, err := invokeBridgeStructuredFromWSL(ctx, conversationID, 30*time.Second)
	if err != nil {
		tracef("structured bridge err: %v", err)
		return StructuredEnrichment{}
	}
	tracef("structured bridge OK (%d bytes)", len(raw))
	return ParseStructuredTrajectory(raw, conversationID, projectRoot, path, a.scrubber)
}

// fetchStructuredEnrichmentNative pulls the GetCascadeTrajectory
// payload via the already-discovered language_server endpoint
// (non-WSL hosts only). Same best-effort contract as the WSL helper.
//
// Deprecated: prefer fetchStructuredEnrichmentNativeAt which accepts
// an explicit endpoint — the heuristic-derived PreferredEndpoint() is
// unreliable, and callers that already iterated Endpoints() to find a
// working URL should pass it through rather than re-iterate.
func (a *Adapter) fetchStructuredEnrichmentNative(ctx context.Context, ls LanguageServer, conversationID, projectRoot, path string) StructuredEnrichment {
	return a.fetchStructuredEnrichmentNativeAt(ctx, ls, ls.PreferredEndpoint(), conversationID, projectRoot, path)
}

// fetchStructuredEnrichmentNativeAt is the endpoint-explicit variant
// of fetchStructuredEnrichmentNative. Callers that already iterated
// ls.Endpoints() to find a working URL pass it through here so the
// structured-trajectory call lands on the same endpoint that just
// succeeded for ConvertTrajectoryToMarkdown — avoids re-iterating
// failing protocols.
func (a *Adapter) fetchStructuredEnrichmentNativeAt(ctx context.Context, ls LanguageServer, endpoint, conversationID, projectRoot, path string) StructuredEnrichment {
	if endpoint == "" {
		return StructuredEnrichment{}
	}
	raw, err := callGetCascadeTrajectory(ctx, endpoint, ls.CSRFToken, conversationID, 30*time.Second)
	if err != nil {
		tracef("structured grpc pid=%d %s err: %v", ls.PID, endpoint, err)
		return StructuredEnrichment{}
	}
	en := ParseStructuredTrajectory(raw, conversationID, projectRoot, path, a.scrubber)
	tracef("structured grpc pid=%d %s OK (%d bytes, %d tools, %d tokens, model=%q)",
		ls.PID, endpoint, len(raw), len(en.ToolEvents), len(en.TokenEvents), en.Model)
	// Wire-shape mismatch warning (Issue #5). When the gRPC response
	// is non-trivial (≥ 10 KiB) but ParseStructuredTrajectory yields
	// zero token rows AND no model, the proto path mapping
	// (structured.go:122 et al.) likely doesn't match this response's
	// shape — newer Antigravity versions may have migrated fields,
	// or claude-vs-gemini variants may diverge on rare turns.
	// Verified case: maintainer session e371fdb1-… returned 430,202
	// bytes but extracted 0 tokens / model="". Surface as a tracef
	// so it shows up in observer logs without flooding successful
	// sessions; an operator can grep for "structured_shape_mismatch"
	// to find candidates for proto-path investigation.
	if len(raw) >= 10*1024 && len(en.TokenEvents) == 0 && en.Model == "" {
		tracef("structured_shape_mismatch pid=%d %s conversation=%s payload=%d_bytes — proto path mapping may need updating",
			ls.PID, endpoint, conversationID, len(raw))
		// Optional debug dump (Issue #5 follow-up). Operator sets
		// [observer.antigravity] dump_shape_mismatches_dir in
		// config.toml; on mismatch we write the raw bytes for
		// offline proto-path analysis. Soft-fail — a dump error
		// must not poison the extraction path (which has already
		// succeeded as a no-op).
		a.dumpShapeMismatchPayload(conversationID, raw)
	}
	return en
}

// dumpShapeMismatchPayload writes the raw GetCascadeTrajectory bytes
// to <dumpDir>/<conversationID>.bin so the operator can diff the wire
// shape between a known-working and known-broken session. No-op when
// the dump dir isn't configured. Issue #5 follow-up.
func (a *Adapter) dumpShapeMismatchPayload(conversationID string, raw []byte) {
	if a.dumpShapeMismatchesDir == "" || conversationID == "" || len(raw) == 0 {
		return
	}
	if err := os.MkdirAll(a.dumpShapeMismatchesDir, 0o755); err != nil {
		tracef("dump_shape_mismatch_mkdir_err dir=%s err=%v", a.dumpShapeMismatchesDir, err)
		return
	}
	out := filepath.Join(a.dumpShapeMismatchesDir, conversationID+".bin")
	if err := os.WriteFile(out, raw, 0o644); err != nil {
		tracef("dump_shape_mismatch_write_err path=%s err=%v", out, err)
		return
	}
	tracef("dump_shape_mismatch_written path=%s bytes=%d", out, len(raw))
}

// hasStructuredUserPrompts returns true when the structured
// enrichment surfaced at least one user_prompt ToolEvent (Tier 2
// extraction from 1.2.19.2). Caller uses this to decide whether to
// suppress markdown's `### User Input` rows so the same prompt
// doesn't land twice.
func hasStructuredUserPrompts(events []models.ToolEvent) bool {
	for _, e := range events {
		if e.RawToolName == "structured.user_prompt" {
			return true
		}
	}
	return false
}

// structuredAssistantTextCoverage returns the fraction of
// per-LLM-call token rows that have a corresponding
// `structured.assistant_text` ToolEvent. Used to decide whether
// markdown's `### Planner Response` rows can be safely suppressed:
// claude sessions hit ~58% coverage (15/26 on FB48) so dedup is
// safe; gemini sessions can drop to 2% (3/130 on 803add5e) where
// suppressing markdown loses 90%+ of the assistant narrative. The
// 0.5 cutoff in callers separates the two regimes.
func structuredAssistantTextCoverage(en StructuredEnrichment) float64 {
	if len(en.TokenEvents) == 0 {
		return 0
	}
	count := 0
	for _, e := range en.ToolEvents {
		if e.RawToolName == "structured.assistant_text" {
			count++
		}
	}
	return float64(count) / float64(len(en.TokenEvents))
}

// AssistantTextCoverageThreshold gates markdown.planner_response
// dedup. Below this fraction, structured assistant_text is sparse
// enough that markdown carries strictly more content and must be
// preserved. At/above, structured is authoritative and markdown is
// safely suppressed. Centralised so the live adapter and the
// backfill stay in lockstep.
const AssistantTextCoverageThreshold = 0.5

// applyStructuredEnrichment overlays model + per-turn token rows +
// per-step file-view tool rows on top of the markdown-derived
// ParseResult. Mutates res in place.
//
// Model attribution: we set Model on every emitted ToolEvent so the
// store layer's UpsertSession (called for the FIRST event of a new
// session) writes it into sessions.model. For sessions whose row
// already exists with model="" (re-ingest case), this still has no
// effect — that case is handled by the backfill mode which UPDATEs
// sessions.model directly.
//
// Structured tool events use a different SourceEventID prefix
// (`antigravity-struct-tool:`) than markdown inline-tool events
// (`antigravity-md-tool:` via stableMarkdownID), so the (source_file,
// source_event_id) UNIQUE constraint lets both coexist. The
// structured events carry real per-step timestamps + duration_ms +
// MessageIDs that join to the parent token row, so they're more
// useful for the dashboard's chronological view; markdown inline
// tools remain as a fallback for sessions where structured fetch
// fails.
func applyStructuredEnrichment(res *adapter.ParseResult, en StructuredEnrichment) {
	if en.Model != "" {
		for i := range res.ToolEvents {
			if res.ToolEvents[i].Model == "" {
				res.ToolEvents[i].Model = en.Model
			}
		}
	}
	if len(en.TokenEvents) > 0 {
		res.TokenEvents = append(res.TokenEvents, en.TokenEvents...)
	}
	if len(en.ToolEvents) > 0 {
		res.ToolEvents = append(res.ToolEvents, en.ToolEvents...)
	}
}

// tracef writes a verbose diagnostic line to stderr (the terminal).
// Kept separate from ParseResult.Warnings so the dashboard log panel
// stays light — heavy per-file traces stream to the terminal only.
// Format: `antigravity: <message>` so it's grep-able alongside slog
// output.
func tracef(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "antigravity: "+format+"\n", args...)
}

// discoverLanguageServersCached refreshes a in-memory cache of
// running language_server processes at most once every 30s. The
// underlying discovery shells out (PowerShell on WSL2; ps/lsof on
// macOS) and is too expensive to run per file.
func (a *Adapter) discoverLanguageServersCached() ([]LanguageServer, error) {
	a.lsMu.Lock()
	defer a.lsMu.Unlock()
	now := time.Now().UnixNano()
	if now < a.lsCacheValidUntil {
		return a.lsCache, nil
	}
	servers, err := discoverLanguageServers()
	if err != nil {
		return nil, err
	}
	a.lsCache = servers
	a.lsCacheValidUntil = now + int64(30*time.Second)
	return servers, nil
}

// lookupSecret returns the cached OSCrypt secret, fetching it on
// first call. Errors are also cached for the process lifetime —
// retrying a failed secret lookup on every file is expensive and
// usually pointless (keychain is unlocked once per session).
func (a *Adapter) lookupSecret() (oscrypt.Secret, error) {
	a.secretMu.Lock()
	defer a.secretMu.Unlock()
	if a.secret != nil {
		return a.secret, nil
	}
	if a.secretErr != nil {
		return nil, a.secretErr
	}
	cfg := oscrypt.AppKeyConfig{
		AppName:                "Antigravity",
		KeychainAccounts:       []string{"Antigravity Key", "Antigravity"},
		LinuxFallbackToPeanuts: true,
	}
	secret, err := oscrypt.RetrieveSecret(cfg)
	if err != nil {
		a.secretErr = err
		return nil, err
	}
	a.secret = secret
	return secret, nil
}

// uuidFromFilename returns the UUID portion of a conversations/<uuid>.pb
// path. Used as a stable identifier across sessions for the same
// conversation and as the join key against the state.vscdb index.
//
// Handles both native and foreign-OS path separators so the matcher
// works regardless of host OS (a Windows path like
// `C:\Users\u\.gemini\antigravity\conversations\X.pb` parsed on
// Linux still yields "X").
func uuidFromFilename(path string) string {
	normalized := strings.ReplaceAll(path, `\`, "/")
	base := filepath.Base(normalized)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// contentHash returns a stable per-event id derived from the
// session file path and an offset/marker. Used when we synthesize
// SourceEventIDs from heuristic-extracted text without a natural
// ID anchor in the proto.
func contentHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// defaultRoots returns ~/.gemini/antigravity/conversations under
// every cross-mount-resolved $HOME so observer in WSL2 picks up
// Antigravity data on /mnt/c/Users/<u>/.gemini (and vice versa).
func defaultRoots() []string {
	var roots []string
	for _, h := range crossmount.AllHomes() {
		roots = append(roots, filepath.Join(h.Path, ".gemini", "antigravity", "conversations"))
	}
	return roots
}

// _ keeps errors importable when the package is built standalone.
var _ = errors.New
