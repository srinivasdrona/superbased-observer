package watcher

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marmutapp/superbased-observer/internal/adapter"
	"github.com/marmutapp/superbased-observer/internal/adapter/claudecode"
	"github.com/marmutapp/superbased-observer/internal/adapter/codex"
	"github.com/marmutapp/superbased-observer/internal/db"
	"github.com/marmutapp/superbased-observer/internal/store"
)

func setup(t *testing.T) (*Watcher, *store.Store, string) {
	t.Helper()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "w.db")
	database, err := db.Open(ctx, db.Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := store.New(database)

	watchRoot := t.TempDir()
	reg := adapter.NewRegistry()
	reg.Register(claudecode.NewWithOptions(nil, watchRoot))

	w := New(s, reg, Options{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		NativePredicate: map[string]func(string) bool{
			"claude-code": claudecode.IsNativeTool,
		},
		Debounce: 50 * time.Millisecond,
	})
	return w, s, watchRoot
}

// writeJSONL copies one of the fixture JSONL files into dst under the watch
// root so the adapter can find it.
func writeJSONL(t *testing.T, watchRoot, name string, body []byte) string {
	t.Helper()
	dst := filepath.Join(watchRoot, name)
	if err := os.WriteFile(dst, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestScanIngestsFixtureFile(t *testing.T) {
	t.Parallel()
	w, s, root := setup(t)
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claudecode", "simple-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writeJSONL(t, root, "session.jsonl", body)

	ctx := context.Background()
	res, err := w.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesProcessed != 1 {
		t.Errorf("files processed: %d", res.FilesProcessed)
	}
	n, _ := s.CountActions(ctx)
	// 4 actions: user_prompt (line 1 user text) + claudecode.assistant_text
	// (line 2 leading "I'll read main.go" text block) + Read (line 2
	// tool_use) + Bash (line 4 tool_use). Lines 3 and 5 are tool_result-
	// only user messages and don't emit user_prompt.
	if n != 4 {
		t.Errorf("actions after scan: %d want 4", n)
	}

	// Re-scan: cursor should prevent duplicates.
	res2, err := w.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n2, _ := s.CountActions(ctx)
	if n2 != 4 {
		t.Errorf("actions after re-scan: %d want 4", n2)
	}
	if res2.FilesProcessed != 1 {
		t.Errorf("re-scan files processed: %d", res2.FilesProcessed)
	}
}

func TestWatchPicksUpAppendedLines(t *testing.T) {
	t.Parallel()
	w, s, root := setup(t)

	// Seed with the first two lines of the fixture, so we have a valid
	// tool_use followed by a tool_result pair.
	body, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claudecode", "simple-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// Split into lines.
	var l1End int
	for i, b := range body {
		if b == '\n' {
			if l1End == 0 {
				l1End = i + 1
				continue
			}
			// Second newline: keep lines 1+2 initially.
			path := writeJSONL(t, root, "grow.jsonl", body[:i+1])
			_ = path
			break
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- w.Watch(ctx) }()

	// Wait for the initial scan to finish.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := s.CountActions(ctx)
		if n >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n, _ := s.CountActions(ctx); n < 1 {
		t.Fatalf("initial scan did not ingest: %d", n)
	}

	// Append the rest.
	p := filepath.Join(root, "grow.jsonl")
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		n, _ := s.CountActions(ctx)
		if n >= 4 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	// After the full fixture lands: user_prompt (line 1) +
	// claudecode.assistant_text (line 2 text block) + Read (line 2
	// tool_use) + Bash (line 4 tool_use) = 4 actions.
	if n, _ := s.CountActions(ctx); n != 4 {
		t.Errorf("watch did not pick up new lines: actions=%d want 4", n)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit")
	}
}

// TestPollCursorsCatchesUpDroppedWrites simulates the WSL2/NTFS
// fsnotify-drop scenario: a Write that the OS never delivers to
// fsnotify leaves parse_cursors behind on-disk file size. pollCursors
// is the safety net — it stat()s every known cursor and re-runs
// processFile when bytes are pending.
func TestPollCursorsCatchesUpDroppedWrites(t *testing.T) {
	t.Parallel()
	w, s, root := setup(t)
	full, err := os.ReadFile(filepath.Join("..", "..", "testdata", "claudecode", "simple-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// Split on newline so the partial file is still valid JSONL. Take
	// the first two lines (user_prompt + assistant_text + Read), leaving
	// the Bash on line 4 to be picked up by the poll pass. Line 2
	// produces TWO actions: the leading text block (assistant_text) and
	// the trailing tool_use (Read).
	lines := bytes.SplitAfter(full, []byte("\n"))
	if len(lines) < 4 {
		t.Fatalf("fixture has only %d lines; expected >=4", len(lines))
	}
	prefix := bytes.Join(lines[:2], nil)
	path := writeJSONL(t, root, "session.jsonl", prefix)

	ctx := context.Background()
	if _, err := w.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	n0, _ := s.CountActions(ctx)
	if n0 != 3 {
		t.Fatalf("after partial scan: got %d actions, want 3", n0)
	}

	// Append the remainder out-of-band (no fsnotify event delivered).
	rest := bytes.Join(lines[2:], nil)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(rest); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// pollCursors should detect file_size > byte_offset and re-process.
	if err := w.pollCursors(ctx); err != nil {
		t.Fatalf("pollCursors: %v", err)
	}
	n1, _ := s.CountActions(ctx)
	if n1 != 4 {
		t.Errorf("after pollCursors: got %d actions, want 4 (poll didn't catch up)", n1)
	}

	// Second poll on a now-current cursor must be a no-op.
	if err := w.pollCursors(ctx); err != nil {
		t.Fatalf("pollCursors (idempotent): %v", err)
	}
	n2, _ := s.CountActions(ctx)
	if n2 != n1 {
		t.Errorf("idempotent poll inserted rows: %d → %d", n1, n2)
	}
}

func TestPollCursorsSkipsOrphanPaths(t *testing.T) {
	t.Parallel()
	w, s, _ := setup(t)
	ctx := context.Background()

	// Seed a cursor for a path no adapter recognises (mimics older
	// adapter versions whose IsSessionFile has since been tightened).
	// pollCursors must skip it — same exclusion the dashboard health
	// endpoint applies — so the watcher doesn't churn on rows the
	// recovery flow can't process anyway.
	orphan := filepath.Join(t.TempDir(), "unknown-tool.log")
	if err := os.WriteFile(orphan, []byte("garbage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCursor(ctx, orphan, 0); err != nil {
		t.Fatal(err)
	}
	if err := w.pollCursors(ctx); err != nil {
		t.Fatalf("pollCursors: %v", err)
	}
	// Cursor must remain at 0 — we never invoked an adapter.
	off, _ := s.GetCursor(ctx, orphan)
	if off != 0 {
		t.Errorf("orphan cursor advanced: %d", off)
	}
}

func TestNewClampsNegativePollInterval(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "w.db")
	database, _ := db.Open(context.Background(), db.Options{Path: dbPath})
	defer database.Close()
	w := New(store.New(database), adapter.NewRegistry(), Options{
		PollInterval: -5 * time.Second,
	})
	if w.pollInterval != 0 {
		t.Errorf("negative PollInterval not clamped: %v", w.pollInterval)
	}
}

// TestCodexShortSessionMultiPassIngest reproduces the live failure mode
// reported 2026-05-06: a short ChatGPT-auth Codex rollout grows in three
// chunks (leading prompts, then function_call + token_count, then final
// task_complete + tokens). With pass-A's cursor advanced past the
// function_call line, the resumed pass-B never re-emits the run_command
// — yet pass-A also never persists it because pass-A finished BEFORE
// the function_call landed on disk. End-to-end behavior must produce the
// run_command row regardless of where the pass boundaries fall.
func TestCodexShortSessionMultiPassIngest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "w.db")
	database, err := db.Open(ctx, db.Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := store.New(database)

	// Place the rollout under the codex watch root so the adapter picks
	// it up. The codex adapter expects a "rollout-*.jsonl" filename.
	codexHome := t.TempDir()
	sessionsRoot := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(sessionsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(sessionsRoot, "rollout-2026-05-06T10-58-21-019dfbc2-344c-7a41-9766-9e6d97c28825.jsonl")

	reg := adapter.NewRegistry()
	reg.Register(codex.NewWithOptions(nil, sessionsRoot))

	w := New(s, reg, Options{
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Debounce: 50 * time.Millisecond,
	})

	leading := []string{
		`{"timestamp":"2026-05-06T05:28:28.414Z","type":"session_meta","payload":{"id":"019dfbc2-344c-7a41-9766-9e6d97c28825","cwd":"D:\\programsx\\partner-names","model":"gpt-5.5"}}`,
		`{"timestamp":"2026-05-06T05:28:28.416Z","type":"event_msg","payload":{"type":"task_started","turn_id":"019dfbc2-3c66-72b1-9bcf-97654ee68beb"}}`,
		`{"timestamp":"2026-05-06T05:28:28.416Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions instructions>\nshort dev prompt body\n</permissions instructions>"}]}}`,
		`{"timestamp":"2026-05-06T05:28:28.417Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>D:\\programsx\\partner-names</cwd>\n</environment_context>"}]}}`,
		`{"timestamp":"2026-05-06T05:28:28.417Z","type":"turn_context","payload":{"turn_id":"019dfbc2-3c66-72b1-9bcf-97654ee68beb","cwd":"D:\\programsx\\partner-names","model":"gpt-5.5"}}`,
		`{"timestamp":"2026-05-06T05:28:28.418Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Do not edit files. Run a PowerShell command, then reply only with ok."}]}}`,
		`{"timestamp":"2026-05-06T05:28:28.418Z","type":"event_msg","payload":{"type":"user_message","message":"Do not edit files. Run a PowerShell command, then reply only with ok."}}`,
	}
	middle := []string{
		`{"timestamp":"2026-05-06T05:28:31.181Z","type":"event_msg","payload":{"type":"token_count","info":null}}`,
		`{"timestamp":"2026-05-06T05:28:31.185Z","type":"response_item","payload":{"type":"function_call","name":"shell_command","arguments":"{\"command\":\"Write-Output 'observer-live-ingest-test'\"}","call_id":"call_test_id"}}`,
		`{"timestamp":"2026-05-06T05:28:31.186Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":13445,"cached_input_tokens":7552,"output_tokens":45,"reasoning_output_tokens":0,"total_tokens":13490},"total_token_usage":{"input_tokens":13445,"cached_input_tokens":7552,"output_tokens":45,"reasoning_output_tokens":0,"total_tokens":13490}}}}`,
		`{"timestamp":"2026-05-06T05:28:31.828Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_test_id","output":"observer-live-ingest-test\r\n"}}`,
	}
	tail := []string{
		`{"timestamp":"2026-05-06T05:28:33.647Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":13445,"cached_input_tokens":7552,"output_tokens":45,"reasoning_output_tokens":0,"total_tokens":13490},"total_token_usage":{"input_tokens":13445,"cached_input_tokens":7552,"output_tokens":45,"reasoning_output_tokens":0,"total_tokens":13490}}}}`,
		`{"timestamp":"2026-05-06T05:28:33.649Z","type":"event_msg","payload":{"type":"agent_message","message":"ok","phase":"final_answer"}}`,
		`{"timestamp":"2026-05-06T05:28:33.649Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}],"phase":"final_answer"}}`,
		`{"timestamp":"2026-05-06T05:28:33.650Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":13524,"cached_input_tokens":13184,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":13529},"total_token_usage":{"input_tokens":26969,"cached_input_tokens":20736,"output_tokens":50,"reasoning_output_tokens":0,"total_tokens":27019}}}}`,
		`{"timestamp":"2026-05-06T05:28:33.651Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"019dfbc2-3c66-72b1-9bcf-97654ee68beb","last_agent_message":"ok","completed_at":1778045313,"duration_ms":9733}}`,
	}

	writeStage := func(stage []string, includePrev []string) {
		all := append([]string{}, includePrev...)
		all = append(all, stage...)
		body := strings.Join(all, "\n") + "\n"
		if err := os.WriteFile(rollout, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Pass A: only leading lines on disk. Scan picks them up.
	writeStage(leading, nil)
	if _, err := w.Scan(ctx); err != nil {
		t.Fatal(err)
	}

	// Pass B: middle lines (function_call + function_call_output) appended.
	// pollCursors covers the no-fsnotify-event case, which mirrors the
	// real Windows-NTFS dropped-event scenario.
	writeStage(middle, leading)
	if err := w.pollCursors(ctx); err != nil {
		t.Fatal(err)
	}

	// Pass C: tail (task_complete + final tokens) appended.
	writeStage(tail, append(append([]string{}, leading...), middle...))
	if err := w.pollCursors(ctx); err != nil {
		t.Fatal(err)
	}

	// Verify run_command landed under the canonical UUID session.
	wantSession := "019dfbc2-344c-7a41-9766-9e6d97c28825"
	rows, err := database.QueryContext(ctx,
		`SELECT action_type, COUNT(*) FROM actions WHERE session_id = ? GROUP BY action_type`,
		wantSession)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var at string
		var n int
		if err := rows.Scan(&at, &n); err != nil {
			t.Fatal(err)
		}
		counts[at] = n
	}
	if counts["run_command"] == 0 {
		t.Errorf("run_command missing from DB after multi-pass ingest. counts=%v", counts)
	}
	if counts["task_complete"] == 0 {
		t.Errorf("task_complete missing from DB after multi-pass ingest. counts=%v", counts)
	}
}

// TestCodexConcurrentProcessFileRace fires two processFile calls
// concurrently against the same file, simulating the watcher's
// debounce+settle pair both firing for the same path. INSERT OR IGNORE
// + MAX cursor semantics should keep the result deterministic.
func TestCodexConcurrentProcessFileRace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	dbPath := filepath.Join(t.TempDir(), "w.db")
	database, err := db.Open(ctx, db.Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	s := store.New(database)

	codexHome := t.TempDir()
	sessionsRoot := filepath.Join(codexHome, "sessions")
	if err := os.MkdirAll(sessionsRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	a := codex.NewWithOptions(nil, sessionsRoot)
	rollout := filepath.Join(sessionsRoot, "rollout-2026-05-06T10-58-21-019dfbc2-344c-7a41-9766-9e6d97c28825.jsonl")

	reg := adapter.NewRegistry()
	reg.Register(a)

	w := New(s, reg, Options{
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})

	full := []string{
		`{"timestamp":"2026-05-06T05:28:28.414Z","type":"session_meta","payload":{"id":"019dfbc2-344c-7a41-9766-9e6d97c28825","cwd":"D:\\programsx\\partner-names","model":"gpt-5.5"}}`,
		`{"timestamp":"2026-05-06T05:28:28.416Z","type":"event_msg","payload":{"type":"task_started","turn_id":"019dfbc2-3c66-72b1-9bcf-97654ee68beb"}}`,
		`{"timestamp":"2026-05-06T05:28:28.418Z","type":"event_msg","payload":{"type":"user_message","message":"hi"}}`,
		`{"timestamp":"2026-05-06T05:28:31.185Z","type":"response_item","payload":{"type":"function_call","name":"shell_command","arguments":"{\"command\":\"x\"}","call_id":"call_x"}}`,
		`{"timestamp":"2026-05-06T05:28:31.828Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call_x","output":"ok"}}`,
		`{"timestamp":"2026-05-06T05:28:33.651Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"019dfbc2-3c66-72b1-9bcf-97654ee68beb","last_agent_message":"ok","completed_at":1778045313,"duration_ms":9}}`,
	}
	body := strings.Join(full, "\n") + "\n"
	if err := os.WriteFile(rollout, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	// Fire two parses concurrently for the same file.
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { done <- w.processFile(ctx, a, rollout, false) }()
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent processFile: %v", err)
		}
	}

	wantSession := "019dfbc2-344c-7a41-9766-9e6d97c28825"
	rows, err := database.QueryContext(ctx,
		`SELECT action_type, COUNT(*) FROM actions WHERE session_id = ? GROUP BY action_type`,
		wantSession)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var at string
		var n int
		if err := rows.Scan(&at, &n); err != nil {
			t.Fatal(err)
		}
		counts[at] = n
	}
	if counts["run_command"] != 1 {
		t.Errorf("run_command count = %d want 1. counts=%v", counts["run_command"], counts)
	}
	if counts["task_complete"] != 1 {
		t.Errorf("task_complete count = %d want 1. counts=%v", counts["task_complete"], counts)
	}
}

func TestScanWithNoAdaptersIsNoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	database, err := db.Open(ctx, db.Options{Path: dbPath})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s := store.New(database)
	reg := adapter.NewRegistry()
	w := New(s, reg, Options{Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})

	res, err := w.Scan(ctx)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if res.FilesProcessed != 0 {
		t.Errorf("expected zero files, got %d", res.FilesProcessed)
	}
}
