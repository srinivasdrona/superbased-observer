package cursor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/marmutapp/superbased-observer/internal/adapter"
	"github.com/marmutapp/superbased-observer/internal/models"
	"github.com/marmutapp/superbased-observer/internal/platform/crossmount"
	"github.com/marmutapp/superbased-observer/internal/scrub"
)

// Adapter is the file-watcher implementation of the cursor adapter. It
// complements the hook-driven path (BuildEvent / BuildStopTokenEvent /
// BuildStopTranscriptEvents): when the live hook is not registered (or
// the host runs Cursor on a different OS than the observer — e.g.
// Cursor on Windows + observer in WSL — where the wsl.exe-launched hook
// either isn't installed or hasn't been wired yet), the watcher scans
// Cursor's on-disk transcripts under
// `<home>/.cursor/projects/<slug>/agent-transcripts/<conv>/<conv>.jsonl`
// and emits action rows derived from the assistant tool_use blocks +
// the opening user prompt of every turn.
//
// Coverage compared to the hook path:
//   - Activity (user prompts, every tool_use): covered.
//   - Token usage / model: NOT covered — the transcript file has no
//     `model` or `usage` fields. Token rows still require the live
//     hook (BuildStopTokenEvent on the `stop` event).
//   - Real-time: lags by one assistant turn — Cursor flushes the JSONL
//     after the turn completes.
//
// Hook + watcher running in tandem produce two rows per turn (one from
// each path) sharing SessionID = conversation_id but with distinct
// MessageIDs (real generation_id vs the synthetic
// `transcript:<convID>:turn<N>` produced here). SourceFile distinguishes
// the rows: hook rows carry "cursor:hook"; watcher rows carry the real
// transcript file path.
// SessionHookChecker reports whether sessionID already has rows in
// the DB whose source_file is the cursor live-hook handler. The
// watcher consults this before emitting transcript-derived events
// for a session: if the live hook has already captured the session
// (which becomes true after the first beforeSubmitPrompt fires for
// the session), watcher emission would be pure duplication and is
// skipped. Returns (false, nil) on a missing/empty result.
type SessionHookChecker func(ctx context.Context, sessionID string) (bool, error)

type Adapter struct {
	scrubber  *scrub.Scrubber
	roots     []string
	hookCheck SessionHookChecker
}

// New returns an Adapter with platform-default cross-mount roots.
// Mirrors antigravity.New(): on WSL2, every detected Windows-side
// $HOME-equivalent contributes a .cursor/projects root.
func New() *Adapter {
	return &Adapter{
		scrubber: scrub.New(),
		roots:    defaultRoots(),
	}
}

// NewWithOptions customises scrubber and roots for tests. Pass nil
// scrubber for the default; pass nil/empty roots for default
// platform discovery.
func NewWithOptions(s *scrub.Scrubber, roots ...string) *Adapter {
	if s == nil {
		s = scrub.New()
	}
	if len(roots) == 0 {
		roots = defaultRoots()
	}
	return &Adapter{scrubber: s, roots: roots}
}

// WithSessionHookChecker injects the predicate the watcher uses to
// detect "this session already has live-hook rows; skip the
// transcript replay." A nil checker (the default) means always emit
// — appropriate for cold-start ingestion or environments without
// the live hook layer wired up. Returns the adapter for chaining.
func (a *Adapter) WithSessionHookChecker(check SessionHookChecker) *Adapter {
	a.hookCheck = check
	return a
}

// Name implements adapter.Adapter.
func (*Adapter) Name() string { return models.ToolCursor }

// WatchPaths implements adapter.Adapter.
func (a *Adapter) WatchPaths() []string { return a.roots }

// IsSessionFile implements adapter.Adapter. Matches Cursor agent
// transcripts: `.cursor/projects/<slug>/agent-transcripts/<conv>/<conv>.jsonl`.
// The double-uuid pattern (dir name == file basename) is what lets the
// matcher reject other JSONLs Cursor may grow under projects/ in
// future without enumerating an allowlist. Path separators are
// normalised to `/` so the matcher works against backslash-shaped
// strings even on Linux (where filepath.Base wouldn't split on `\`).
func (a *Adapter) IsSessionFile(path string) bool {
	if !matchesSessionShape(path) {
		return false
	}
	return adapter.UnderAnyWatchRoot(path, a.WatchPaths())
}

// matchesSessionShape returns true when path looks like a Cursor agent
// transcript: `.cursor/projects/<slug>/agent-transcripts/<conv>/<conv>.jsonl`.
// Path-component string match, normalises backslashes so fixtures with
// foreign-OS separators still work on Linux CI.
func matchesSessionShape(path string) bool {
	norm := strings.ReplaceAll(path, `\`, "/")
	lower := strings.ToLower(norm)
	if !strings.HasSuffix(lower, ".jsonl") {
		return false
	}
	if !strings.Contains(lower, "/.cursor/projects/") {
		return false
	}
	if !strings.Contains(lower, "/agent-transcripts/") {
		return false
	}
	idx := strings.LastIndex(norm, "/")
	if idx < 0 {
		return false
	}
	base := norm[idx+1:]
	rest := norm[:idx]
	parentIdx := strings.LastIndex(rest, "/")
	if parentIdx < 0 {
		return false
	}
	parent := rest[parentIdx+1:]
	return base == parent+".jsonl"
}

// ParseSessionFile implements adapter.Adapter.
//
// Cursor transcript JSONL is small (one file per conversation; tens
// to low hundreds of lines in practice), so parseTranscriptTurns reads
// the whole file and the watcher relies on the (source_file,
// source_event_id) UNIQUE index for idempotency on re-scan rather
// than offset-based incremental parsing. NewOffset = file size at scan
// time, so the polling fallback only re-parses on file growth.
func (a *Adapter) ParseSessionFile(ctx context.Context, path string, fromOffset int64) (adapter.ParseResult, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return adapter.ParseResult{}, fmt.Errorf("cursor.ParseSessionFile: stat: %w", err)
	}
	res := adapter.ParseResult{NewOffset: fi.Size()}
	if fi.Size() == 0 {
		return res, nil
	}
	if fromOffset == fi.Size() {
		return res, nil
	}

	turns, err := parseTranscriptTurns(path)
	if err != nil {
		return adapter.ParseResult{}, fmt.Errorf("cursor.ParseSessionFile: parse: %w", err)
	}
	if len(turns) == 0 {
		return res, nil
	}

	convID := convIDFromPath(path)
	if convID == "" {
		res.Warnings = append(res.Warnings, fmt.Sprintf("cursor: cannot derive conversation_id from %s — skipping", path))
		return res, nil
	}
	// Defer to the live hook when it's already captured this session.
	// The checker is set by the watcher constructor; nil means
	// "no hook layer running, emit unconditionally" (the cold-start
	// ingestion path).
	if a.hookCheck != nil {
		hooked, err := a.hookCheck(ctx, convID)
		if err != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("cursor: hook-check for %s failed (%v); falling back to watcher emission", convID, err))
		} else if hooked {
			return res, nil
		}
	}
	slug := projectSlugFromPath(path)
	projectRoot := DecodeProjectSlug(slug)
	if projectRoot == "" {
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("cursor: cannot decode project slug %q for %s — emitting events with empty project_root", slug, path))
	}

	// Use file mtime as the per-turn timestamp. The transcript's
	// `<timestamp>` envelope on user lines is human-formatted and
	// fragile to parse; mtime is monotonic and good enough for
	// dashboard ordering.
	ts := fi.ModTime().UTC()

	for i, turn := range turns {
		if ctx.Err() != nil {
			return adapter.ParseResult{}, ctx.Err()
		}
		// Synthetic generation_id, distinct from any real cursor
		// `generation_id` (which is a UUID). Stable across re-scans
		// because turn ordering in the JSONL is stable.
		genID := fmt.Sprintf("transcript:%s:turn%d", convID, i+1)
		// Watcher does NOT emit user_prompt rows: the live
		// beforeSubmitPrompt hook captures every user prompt with the
		// real generation_id, so a transcript-derived user_prompt
		// would be a pure duplicate (different MessageID, same
		// content, same SessionID). The hook fires synchronously when
		// the user submits, so coverage is reliable as long as the
		// hook is registered (which auto-register on `observer start`
		// guarantees). Cost: pre-install historical transcripts lose
		// user_prompt rows; the assistant tool_use rows that follow
		// still convey what the model did.
		toolEvs := buildTranscriptToolEvents(turn, convID, projectRoot, genID, path, ts, a.scrubber)
		res.ToolEvents = append(res.ToolEvents, toolEvs...)
	}
	return res, nil
}

// DecodeProjectSlug reverses Cursor's projects/<slug>/ encoding into
// a workspace path string. Cursor encodes a Windows-style path like
// `C:\programsx\marmutmain` as `c-programsx-marmutmain`: drive letter
// (lowercase) + `-` + each path component joined by `-`. Linux/macOS
// paths get encoded without a leading drive letter, e.g.
// `/home/user/repo` → `home-user-repo`.
//
// The encoding is LOSSY when a path component contains a literal `-`
// (Cursor presumably has a separate escape; observed corpora to date
// don't exercise this case, so the decoder treats `-` as a separator
// universally). Returns "" for an empty slug.
//
// Heuristic for Windows-vs-POSIX:
//   - First segment exactly 1 char → treat as Windows drive letter,
//     emit `<DRIVE>:\` joined by `\`.
//   - First segment 2+ chars → treat as POSIX, emit `/` joined by `/`.
func DecodeProjectSlug(slug string) string {
	if slug == "" {
		return ""
	}
	parts := strings.Split(slug, "-")
	if len(parts[0]) == 1 {
		drive := strings.ToUpper(parts[0])
		if len(parts) == 1 {
			return drive + `:\`
		}
		return drive + `:\` + strings.Join(parts[1:], `\`)
	}
	return "/" + strings.Join(parts, "/")
}

// convIDFromPath extracts the conversation UUID from a transcript
// path of shape `.../agent-transcripts/<conv>/<conv>.jsonl`. Returns
// "" when the shape doesn't match.
func convIDFromPath(path string) string {
	base := filepath.Base(path)
	if !strings.HasSuffix(base, ".jsonl") {
		return ""
	}
	conv := strings.TrimSuffix(base, ".jsonl")
	// Cross-check with the parent dir name; if they don't match, the
	// path isn't shaped the way IsSessionFile asserted.
	if filepath.Base(filepath.Dir(path)) != conv {
		return ""
	}
	return conv
}

// projectSlugFromPath extracts the projects/<slug>/ component from a
// transcript path. Returns "" when not found.
func projectSlugFromPath(path string) string {
	norm := strings.ReplaceAll(path, `\`, "/")
	idx := strings.Index(norm, "/projects/")
	if idx < 0 {
		return ""
	}
	rest := norm[idx+len("/projects/"):]
	end := strings.Index(rest, "/")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// defaultRoots returns `<home>/.cursor/projects` under every cross-mount
// resolved $HOME. On WSL2 with Cursor running on Windows, this lights
// up `/mnt/c/Users/<u>/.cursor/projects` automatically.
func defaultRoots() []string {
	var roots []string
	for _, h := range crossmount.AllHomes() {
		roots = append(roots, filepath.Join(h.Path, ".cursor", "projects"))
	}
	return roots
}
