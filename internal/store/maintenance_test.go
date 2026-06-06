package store

import (
	"context"
	"testing"
	"time"

	"github.com/marmutapp/superbased-observer/internal/models"
)

func TestAcquireMaintenanceLease_RejectsLiveHolderAndStealsStale(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 6, 9, 0, 0, 0, time.UTC)

	acquired, lease, err := s.AcquireMaintenanceLease(ctx, "bf", "owner-a", now, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired || lease == nil || lease.OwnerToken != "owner-a" {
		t.Fatalf("first acquire = %v lease=%+v", acquired, lease)
	}

	acquired, lease, err = s.AcquireMaintenanceLease(ctx, "bf", "owner-b", now.Add(2*time.Minute), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if acquired {
		t.Fatal("second acquire should fail while lease is live")
	}
	if lease == nil || lease.OwnerToken != "owner-a" {
		t.Fatalf("live holder = %+v, want owner-a", lease)
	}

	acquired, lease, err = s.AcquireMaintenanceLease(ctx, "bf", "owner-b", now.Add(11*time.Minute), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired || lease == nil || lease.OwnerToken != "owner-b" {
		t.Fatalf("stale steal = %v lease=%+v, want owner-b", acquired, lease)
	}
}

func TestListRepairCandidateFiles_UsesPerRepairVersion(t *testing.T) {
	t.Parallel()
	s, _ := newTestStore(t)
	ctx := context.Background()
	pid, _ := s.UpsertProject(ctx, "/tmp/p-repair", "")
	if err := s.UpsertSession(ctx, models.Session{
		ID: "sess-repair", ProjectID: pid, Tool: models.ToolCodex, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertActions(ctx, []models.Action{
		{
			SessionID: "sess-repair", ProjectID: pid, Timestamp: time.Now().UTC(),
			ActionType: models.ActionRunCommand, Target: "go test", Success: true,
			Tool: models.ToolCodex, SourceFile: "a.jsonl", SourceEventID: "a-1",
		},
		{
			SessionID: "sess-repair", ProjectID: pid, Timestamp: time.Now().UTC(),
			ActionType: models.ActionRunCommand, Target: "go vet", Success: true,
			Tool: models.ToolCodex, SourceFile: "b.jsonl", SourceEventID: "b-1",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRepairAppliedBatch(ctx, models.ToolCodex, "codex-rescan", 1, []string{"a.jsonl"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListRepairCandidateFiles(ctx, models.ToolCodex, "codex-rescan", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "b.jsonl" {
		t.Fatalf("candidate files = %v, want [b.jsonl]", got)
	}
}
