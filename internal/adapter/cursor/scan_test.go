package cursor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDecodeProjectSlug pins the slug↔workspace-path encoding observed
// in real Cursor IDE installs (sample: c-programsx-marmutmain ↔
// C:\programsx\marmutmain). The encoding is lossy when a path
// component contains a literal `-`; the tests below exercise only the
// shapes Cursor is observed to emit.
func TestDecodeProjectSlug(t *testing.T) {
	tests := map[string]struct {
		slug string
		want string
	}{
		"windows simple":         {"c-programsx-marmutmain", `C:\programsx\marmutmain`},
		"windows three segments": {"d-projects-foo-bar", `D:\projects\foo\bar`},
		"windows root only":      {"c", `C:\`},
		"windows preserves underscores": {
			"c-users-auzy_-projects-myrepo",
			`C:\users\auzy_\projects\myrepo`,
		},
		"posix home":  {"home-user-myrepo", "/home/user/myrepo"},
		"posix root":  {"home", "/home"},
		"posix usr":   {"usr-local-bin", "/usr/local/bin"},
		"empty":       {"", ""},
		"single char": {"x", `X:\`},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := DecodeProjectSlug(tc.slug)
			if got != tc.want {
				t.Fatalf("DecodeProjectSlug(%q) = %q, want %q", tc.slug, got, tc.want)
			}
		})
	}
}

// TestMatchesSessionShape pins the path-component string match
// against the real `.cursor/projects/<slug>/agent-transcripts/
// <conv>/<conv>.jsonl` shape and rejects everything else under
// projects/. Foreign-OS separators are normalised so fixtures with
// Windows backslashes still work on Linux CI. The integrated
// IsSessionFile (shape AND under-WatchPaths) is covered by
// TestIsSessionFile below.
func TestMatchesSessionShape(t *testing.T) {
	tests := map[string]struct {
		path string
		want bool
	}{
		"linux match": {
			"/home/u/.cursor/projects/c-programsx-foo/agent-transcripts/abc/abc.jsonl",
			true,
		},
		"wsl /mnt/c match": {
			"/mnt/c/Users/auzy_/.cursor/projects/c-programsx-marmutmain/agent-transcripts/93eb822a-1b3b-4775-b2c6-ec234fa61b1a/93eb822a-1b3b-4775-b2c6-ec234fa61b1a.jsonl",
			true,
		},
		"windows native sep": {
			`C:\Users\u\.cursor\projects\c-programsx-foo\agent-transcripts\abc\abc.jsonl`,
			true,
		},
		"reject not in projects": {
			"/home/u/.cursor/agent-transcripts/abc/abc.jsonl",
			false,
		},
		"reject not in agent-transcripts": {
			"/home/u/.cursor/projects/c-programsx-foo/canvases/abc.jsonl",
			false,
		},
		"reject mismatched dir/file basename": {
			"/home/u/.cursor/projects/c-programsx-foo/agent-transcripts/abc/other.jsonl",
			false,
		},
		"reject non-jsonl": {
			"/home/u/.cursor/projects/c-programsx-foo/agent-transcripts/abc/abc.json",
			false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := matchesSessionShape(tc.path)
			if got != tc.want {
				t.Fatalf("matchesSessionShape(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// TestIsSessionFile pins the integrated public API: shape filter AND
// adapter.UnderAnyWatchRoot. Uses host-native paths so filepath.Abs
// behaves correctly.
func TestIsSessionFile(t *testing.T) {
	root := t.TempDir()
	a := NewWithOptions(nil, root)
	conv := "93eb822a-1b3b-4775-b2c6-ec234fa61b1a"
	good := filepath.Join(root, ".cursor", "projects", "slug", "agent-transcripts", conv, conv+".jsonl")
	if !a.IsSessionFile(good) {
		t.Errorf("matching path under watch root should match: %s", good)
	}
	// Shape match but outside the watch root (v1.4.51 invariant).
	outside := filepath.Join("/tmp/foreign/.cursor/projects/slug/agent-transcripts", conv, conv+".jsonl")
	if a.IsSessionFile(outside) {
		t.Errorf("shape-match outside watch root must NOT match: %s", outside)
	}
}

// TestConvIDFromPath / TestProjectSlugFromPath pin the path-component
// extractors used by ParseSessionFile to derive the conversation
// session_id and the workspace slug from the transcript path.
func TestConvIDFromPath(t *testing.T) {
	tests := map[string]struct {
		path string
		want string
	}{
		"matched": {
			"/home/u/.cursor/projects/c-foo/agent-transcripts/abc/abc.jsonl",
			"abc",
		},
		"mismatched dir/file": {
			"/home/u/.cursor/projects/c-foo/agent-transcripts/abc/other.jsonl",
			"",
		},
		"non-jsonl": {
			"/home/u/.cursor/projects/c-foo/agent-transcripts/abc/abc.json",
			"",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := convIDFromPath(tc.path); got != tc.want {
				t.Fatalf("convIDFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestProjectSlugFromPath(t *testing.T) {
	tests := map[string]struct {
		path string
		want string
	}{
		"linux": {
			"/home/u/.cursor/projects/c-programsx-foo/agent-transcripts/abc/abc.jsonl",
			"c-programsx-foo",
		},
		"windows backslashes": {
			`C:\Users\u\.cursor\projects\c-programsx-foo\agent-transcripts\abc\abc.jsonl`,
			"c-programsx-foo",
		},
		"missing": {"/home/u/something/else.jsonl", ""},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := projectSlugFromPath(tc.path); got != tc.want {
				t.Fatalf("projectSlugFromPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

// TestParseSessionFile exercises the full scan path against a fixture
// transcript whose shape mirrors what Cursor emits in production
// (verified against a captured /mnt/c-side transcript): user line
// wrapped in <timestamp>+<user_query> envelopes, assistant lines
// carrying text + tool_use parts, with the v1.4-era tool names
// (Glob, Read, Shell). The test asserts the synthetic generation_id
// shape, the action types lifted from cursorTranscriptActionType, and
// SourceFile pointing at the real transcript path. The watcher does
// NOT emit user_prompt rows (the live beforeSubmitPrompt hook covers
// them) — the test pins that behavior so a regression that
// re-introduces watcher user_prompts would fail loudly.
func TestParseSessionFile(t *testing.T) {
	dir := t.TempDir()
	convID := "93eb822a-1b3b-4775-b2c6-ec234fa61b1a"
	transcriptDir := filepath.Join(dir, "projects", "c-programsx-marmutmain", "agent-transcripts", convID)
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(transcriptDir, convID+".jsonl")
	body := strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Friday, May 8, 2026, 4:16 PM (UTC+5:30)</timestamp>\n<user_query>\nCan you give me a quick summary of this project\n</user_query>"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"I'll scan the repo."},{"type":"tool_use","name":"Glob","input":{"target_directory":"C:\\programsx\\marmutmain","glob_pattern":"README*"}},{"type":"tool_use","name":"Read","input":{"path":"C:\\programsx\\marmutmain\\main.go"}}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"Listing dirs."},{"type":"tool_use","name":"Shell","input":{"command":"ls handlers"}}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &Adapter{} // no scrubber: keep the round-trip simple
	res, err := a.ParseSessionFile(context.Background(), transcript, 0)
	if err != nil {
		t.Fatalf("ParseSessionFile: %v", err)
	}
	if res.NewOffset == 0 {
		t.Fatalf("NewOffset should equal file size, got 0")
	}
	// Expected events: 2 cursor.assistant_text (one per text block on each
	// assistant line) + 3 tool_use (Glob, Read, Shell). NO user_prompt —
	// the watcher delegates user-prompt capture to the live hook.
	if len(res.ToolEvents) != 5 {
		t.Fatalf("event count = %d want 5 (2 assistant_text + 3 tool_use); events=%+v", len(res.ToolEvents), res.ToolEvents)
	}
	for _, ev := range res.ToolEvents {
		if ev.ActionType == "user_prompt" {
			t.Fatalf("watcher must not emit user_prompt rows, got %+v", ev)
		}
		if ev.SessionID != convID {
			t.Errorf("event SessionID = %q want %q", ev.SessionID, convID)
		}
		wantMsgID := "transcript:" + convID + ":turn1"
		if ev.MessageID != wantMsgID {
			t.Errorf("event MessageID = %q want %q", ev.MessageID, wantMsgID)
		}
		if ev.SourceFile != transcript {
			t.Errorf("event SourceFile = %q want %q", ev.SourceFile, transcript)
		}
		if ev.ProjectRoot != `C:\programsx\marmutmain` {
			t.Errorf("event ProjectRoot = %q want C:\\programsx\\marmutmain", ev.ProjectRoot)
		}
	}
	// Emission order within a turn: text part first, then tool_use parts
	// (block-iteration order). Sequence here:
	//   [0] cursor.assistant_text — "I'll scan the repo." (line 2, text)
	//   [1] Glob                  — search_files
	//   [2] Read                  — read_file
	//   [3] cursor.assistant_text — "Listing dirs." (line 3, text)
	//   [4] Shell                 — run_command
	wantSeq := []struct {
		rawTool    string
		actionType string
	}{
		{"cursor.assistant_text", "task_complete"},
		{"Glob", "search_files"},
		{"Read", "read_file"},
		{"cursor.assistant_text", "task_complete"},
		{"Shell", "run_command"},
	}
	for i, w := range wantSeq {
		ev := res.ToolEvents[i]
		if ev.RawToolName != w.rawTool {
			t.Errorf("event[%d] RawToolName = %q want %q", i, ev.RawToolName, w.rawTool)
		}
		if ev.ActionType != w.actionType {
			t.Errorf("event[%d] ActionType = %q want %q", i, ev.ActionType, w.actionType)
		}
	}
}

// TestParseSessionFile_DefersWhenHookActive pins the live-hook
// deferral: when WithSessionHookChecker is wired and the checker
// reports the session is already covered (any cursor:hook row in the
// DB), ParseSessionFile must return zero events. This is what
// prevents the dashboard from showing duplicate tool_use rows during
// a live session — the live stop hook produces canonical rows; the
// watcher steps aside.
func TestParseSessionFile_DefersWhenHookActive(t *testing.T) {
	dir := t.TempDir()
	convID := "abc123"
	transcriptDir := filepath.Join(dir, "projects", "home-user-repo", "agent-transcripts", convID)
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(transcriptDir, convID+".jsonl")
	body := strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"hello"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"path":"/x"}}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	a := (&Adapter{}).WithSessionHookChecker(func(_ context.Context, sessionID string) (bool, error) {
		if sessionID != convID {
			t.Errorf("checker called with sessionID %q want %q", sessionID, convID)
		}
		return true, nil
	})
	res, err := a.ParseSessionFile(context.Background(), transcript, 0)
	if err != nil {
		t.Fatalf("ParseSessionFile: %v", err)
	}
	if len(res.ToolEvents) != 0 {
		t.Fatalf("expected 0 events when hook covers the session; got %d: %+v", len(res.ToolEvents), res.ToolEvents)
	}
	if res.NewOffset == 0 {
		t.Fatalf("NewOffset should advance even on deferral, got 0")
	}
}

// TestParseSessionFile_EmitsWhenNoHookRows pins the cold-start /
// no-hook-installed fallback: when the checker reports the session
// is NOT covered by hooks, the watcher emits transcript-derived
// rows as usual. This is the only thing that captures historical
// (pre-install) transcripts.
func TestParseSessionFile_EmitsWhenNoHookRows(t *testing.T) {
	dir := t.TempDir()
	convID := "abc456"
	transcriptDir := filepath.Join(dir, "projects", "home-user-repo", "agent-transcripts", convID)
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(transcriptDir, convID+".jsonl")
	body := strings.Join([]string{
		`{"role":"user","message":{"content":[{"type":"text","text":"hello"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"path":"/x"}}]}}`,
		"",
	}, "\n")
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	a := (&Adapter{}).WithSessionHookChecker(func(_ context.Context, _ string) (bool, error) {
		return false, nil
	})
	res, err := a.ParseSessionFile(context.Background(), transcript, 0)
	if err != nil {
		t.Fatalf("ParseSessionFile: %v", err)
	}
	if len(res.ToolEvents) != 1 {
		t.Fatalf("expected 1 tool_use when hook absent; got %d: %+v", len(res.ToolEvents), res.ToolEvents)
	}
}

// TestParseSessionFile_Idempotent asserts that NewOffset acts as a
// "no work to do" gate when the file size hasn't grown since the
// last scan — the watcher's polling fallback relies on this.
func TestParseSessionFile_Idempotent(t *testing.T) {
	dir := t.TempDir()
	convID := "abc123"
	transcriptDir := filepath.Join(dir, "projects", "home-user-repo", "agent-transcripts", convID)
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(transcriptDir, convID+".jsonl")
	body := `{"role":"user","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(transcript, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{}

	first, err := a.ParseSessionFile(context.Background(), transcript, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.NewOffset != int64(len(body)) {
		t.Fatalf("first NewOffset = %d want %d", first.NewOffset, len(body))
	}

	// Second call with fromOffset == file size should be a no-op.
	second, err := a.ParseSessionFile(context.Background(), transcript, first.NewOffset)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ToolEvents) != 0 {
		t.Fatalf("second call emitted %d events; expected 0 (file unchanged)", len(second.ToolEvents))
	}
}
