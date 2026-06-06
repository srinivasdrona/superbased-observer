package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/marmutapp/superbased-observer/internal/models"
)

// Targeted replay is only useful if re-ingest can lift a missing
// message_id in place on existing rows. These tests pin that rule for
// both actions and token_usage.

func TestInsertActions_BackfillsMessageIDOnConflict(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()
	pid, _ := s.UpsertProject(ctx, "/tmp/msgid_bf", "")
	if err := s.UpsertSession(ctx, models.Session{
		ID: "s-msgid-bf", ProjectID: pid, Tool: models.ToolCodex,
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	pre := []models.Action{{
		SessionID: "s-msgid-bf", ProjectID: pid, Timestamp: time.Now().UTC(),
		ActionType: models.ActionRunCommand, Target: "go test", Success: true,
		Tool: models.ToolCodex, SourceFile: "rollout.jsonl", SourceEventID: "evt-1",
	}}
	if _, err := s.InsertActions(ctx, pre); err != nil {
		t.Fatal(err)
	}
	post := []models.Action{{
		SessionID: "s-msgid-bf", ProjectID: pid, Timestamp: time.Now().UTC(),
		ActionType: models.ActionRunCommand, Target: "go test", Success: true,
		Tool: models.ToolCodex, SourceFile: "rollout.jsonl", SourceEventID: "evt-1",
		MessageID: "turn-123",
	}}
	if _, err := s.InsertActions(ctx, post); err != nil {
		t.Fatal(err)
	}

	var got sql.NullString
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT message_id FROM actions WHERE source_file = 'rollout.jsonl' AND source_event_id = 'evt-1'`,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String != "turn-123" {
		t.Fatalf("message_id = %q valid=%v, want turn-123", got.String, got.Valid)
	}
}

func TestInsertTokenEvents_MessageIDRefreshOnConflict(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 5, 2, 11, 8, 10, 0, time.UTC)

	if _, err := s.UpsertProject(ctx, "/tmp/proj-msgid", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSession(ctx, models.Session{
		ID: "sess-msgid", ProjectID: 1, Tool: models.ToolCopilotCLI,
		StartedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	first := []models.TokenEvent{{
		SourceFile: "events.jsonl", SourceEventID: "request-1",
		SessionID: "sess-msgid", Timestamp: now,
		Tool:   models.ToolCopilotCLI,
		Source: models.TokenSourceJSONL, Reliability: models.ReliabilityApproximate,
	}}
	if _, err := s.InsertTokenEvents(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := []models.TokenEvent{{
		SourceFile: "events.jsonl", SourceEventID: "request-1",
		SessionID: "sess-msgid", Timestamp: now,
		Tool: models.ToolCopilotCLI, MessageID: "msg-1",
		Source: models.TokenSourceJSONL, Reliability: models.ReliabilityApproximate,
	}}
	if _, err := s.InsertTokenEvents(ctx, second); err != nil {
		t.Fatal(err)
	}

	var got sql.NullString
	if err := s.db.QueryRowContext(
		ctx,
		`SELECT message_id FROM token_usage WHERE source_file = 'events.jsonl' AND source_event_id = 'request-1'`,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Valid || got.String != "msg-1" {
		t.Fatalf("message_id = %q valid=%v, want msg-1", got.String, got.Valid)
	}
}
