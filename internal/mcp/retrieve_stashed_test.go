package mcp

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marmutapp/superbased-observer/internal/db"
	"github.com/marmutapp/superbased-observer/internal/stash"
)

// TestServer_RetrieveStashed_RoundTrip pins the v1.4.41 / Tier 1 / G31
// MCP-side contract: a sha that was Written into the stash retrieves
// the original bytes via tools/call, and the response shape matches
// the documented `{sha, size_bytes, content}` form.
func TestServer_RetrieveStashed_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "obs.db")
	database, err := db.Open(context.Background(), db.Options{Path: path})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	st, err := stash.New(stash.Options{Dir: filepath.Join(dir, "stash")})
	if err != nil {
		t.Fatalf("stash.New: %v", err)
	}
	body := []byte("the original tool_result body that was stashed")
	sha, err := st.Write(body)
	if err != nil {
		t.Fatalf("stash.Write: %v", err)
	}

	s, err := New(Options{DB: database, ServerName: "test", ServerVersion: "0", Stash: st})
	if err != nil {
		t.Fatalf("mcp.New: %v", err)
	}

	parsed := callTool(t, s, "retrieve_stashed", map[string]any{"sha": sha})
	if parsed["sha"] != sha {
		t.Errorf("sha mismatch: got %v, want %s", parsed["sha"], sha)
	}
	if int(parsed["size_bytes"].(float64)) != len(body) {
		t.Errorf("size_bytes: got %v, want %d", parsed["size_bytes"], len(body))
	}
	if parsed["content"] != string(body) {
		t.Errorf("content mismatch:\n got %v\n want %s", parsed["content"], body)
	}
	if _, ok := parsed["truncated"]; ok {
		t.Errorf("unexpected truncated flag set: %v", parsed)
	}
}

// TestServer_RetrieveStashed_NotFound pins the missing-sha error path:
// the model gets a clear error rather than empty content.
func TestServer_RetrieveStashed_NotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "obs.db")
	database, _ := db.Open(context.Background(), db.Options{Path: path})
	t.Cleanup(func() { database.Close() })

	st, _ := stash.New(stash.Options{Dir: filepath.Join(dir, "stash")})
	s, _ := New(Options{DB: database, ServerName: "test", ServerVersion: "0", Stash: st})

	resp := rpcCall(t, s, "tools/call", 1, map[string]any{
		"name":      "retrieve_stashed",
		"arguments": map[string]any{"sha": "0000000000000000000000000000000000000000000000000000000000000000"},
	})
	result, _ := resp["result"].(map[string]any)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("expected isError=true on missing sha, got result=%v", result)
	}
}

// TestServer_RetrieveStashed_MaxBytes pins the optional truncation
// path: a max_bytes argument shorter than the body returns truncated
// content with truncated=true and the full size_bytes still reports
// the original length so the model can decide whether to follow up
// with a wider window.
func TestServer_RetrieveStashed_MaxBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "obs.db")
	database, _ := db.Open(context.Background(), db.Options{Path: path})
	t.Cleanup(func() { database.Close() })

	st, _ := stash.New(stash.Options{Dir: filepath.Join(dir, "stash")})
	body := []byte(strings.Repeat("xyz", 200)) // 600 bytes
	sha, _ := st.Write(body)
	s, _ := New(Options{DB: database, ServerName: "test", ServerVersion: "0", Stash: st})

	parsed := callTool(t, s, "retrieve_stashed", map[string]any{"sha": sha, "max_bytes": 30})
	truncated, _ := parsed["truncated"].(bool)
	if !truncated {
		t.Errorf("expected truncated=true, got %v", parsed)
	}
	content, _ := parsed["content"].(string)
	if len(content) != 30 {
		t.Errorf("content length: got %d, want 30", len(content))
	}
	if int(parsed["size_bytes"].(float64)) != len(body) {
		t.Errorf("size_bytes should report full size %d, got %v", len(body), parsed["size_bytes"])
	}
}

// TestServer_RetrieveStashed_NotRegisteredWithoutStash pins that the
// tool is opt-in via Options.Stash — without it, tools/list does NOT
// include retrieve_stashed.
func TestServer_RetrieveStashed_NotRegisteredWithoutStash(t *testing.T) {
	s, _, _ := testServer(t)
	resp := rpcCall(t, s, "tools/list", 1, nil)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	for _, raw := range tools {
		tm := raw.(map[string]any)
		if tm["name"] == "retrieve_stashed" {
			t.Errorf("retrieve_stashed registered without Options.Stash")
		}
	}
}

// TestServer_RetrieveStashed_LogsK43Signal pins the v1.4.43+ / Tier 3 /
// K43 wiring: a successful retrieve_stashed call writes one row to
// retrieval_signals so the learn pattern miner can later surface high-
// retrieval-rate shas.
func TestServer_RetrieveStashed_LogsK43Signal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "obs.db")
	database, err := db.Open(context.Background(), db.Options{Path: path})
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	st, _ := stash.New(stash.Options{Dir: filepath.Join(dir, "stash")})
	body := []byte("k43 signal payload")
	sha, _ := st.Write(body)

	s, _ := New(Options{
		DB: database, ServerName: "test", ServerVersion: "0",
		Stash:          st,
		SignalRecorder: stubSignalRecorder{database: database},
	})

	_ = callTool(t, s, "retrieve_stashed", map[string]any{"sha": sha})

	var n int
	if err := database.QueryRow(
		`SELECT COUNT(*) FROM retrieval_signals WHERE signal_type = 'retrieve_stashed' AND payload = ?`,
		sha,
	).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 retrieve_stashed signal row for sha %s, got %d", sha, n)
	}
}

// stubSignalRecorder is a minimal SignalRecorder that writes through
// to the test DB. We can't import learn here without an import cycle.
type stubSignalRecorder struct{ database *sql.DB }

func (r stubSignalRecorder) RecordRetrieveStashed(ctx context.Context, sha, sessionID string) error {
	_, err := r.database.ExecContext(ctx,
		`INSERT INTO retrieval_signals (action_id, signal_type, signal_at, session_id, payload)
		 VALUES (NULL, 'retrieve_stashed', ?, ?, ?)`,
		"2026-05-07T12:00:00Z", nullableString(sessionID), sha)
	return err
}
func (r stubSignalRecorder) RecordSearchHit(ctx context.Context, actionID int64, query, sessionID string) error {
	_, err := r.database.ExecContext(ctx,
		`INSERT INTO retrieval_signals (action_id, signal_type, signal_at, session_id, payload)
		 VALUES (?, 'search_hit', ?, ?, ?)`,
		actionID, "2026-05-07T12:00:00Z", nullableString(sessionID), query)
	return err
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestServer_RetrieveStashed_RegisteredWhenStashSet pins the inverse:
// passing Options.Stash adds retrieve_stashed to the tool list.
func TestServer_RetrieveStashed_RegisteredWhenStashSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "obs.db")
	database, _ := db.Open(context.Background(), db.Options{Path: path})
	t.Cleanup(func() { database.Close() })

	st, _ := stash.New(stash.Options{Dir: filepath.Join(dir, "stash")})
	s, _ := New(Options{DB: database, ServerName: "test", ServerVersion: "0", Stash: st})

	resp := rpcCall(t, s, "tools/list", 1, nil)
	tools := resp["result"].(map[string]any)["tools"].([]any)
	found := false
	for _, raw := range tools {
		tm := raw.(map[string]any)
		if tm["name"] == "retrieve_stashed" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("retrieve_stashed missing from tools/list when Options.Stash is set")
	}
}
