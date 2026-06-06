package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// MaintenanceLease is the persisted lease record guarding historical
// maintenance work (backfill / targeted replay). The lease is advisory:
// live daemon ingest is intentionally NOT blocked by it.
type MaintenanceLease struct {
	Name        string
	OwnerToken  string
	AcquiredAt  time.Time
	HeartbeatAt time.Time
}

// AcquireMaintenanceLease acquires or steals the named lease. A live
// lease is one whose heartbeat is within ttl of now. Missing/stale
// leases are overwritten atomically. Returns the active lease record
// when another owner still holds it.
func (s *Store) AcquireMaintenanceLease(ctx context.Context, name, ownerToken string, now time.Time, ttl time.Duration) (bool, *MaintenanceLease, error) {
	if name == "" || ownerToken == "" {
		return false, nil, errors.New("store.AcquireMaintenanceLease: name and ownerToken required")
	}
	if ttl <= 0 {
		return false, nil, errors.New("store.AcquireMaintenanceLease: ttl must be > 0")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, fmt.Errorf("store.AcquireMaintenanceLease: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		curOwner      string
		acquiredText  string
		heartbeatText string
	)
	err = tx.QueryRowContext(
		ctx,
		`SELECT owner_token, acquired_at, heartbeat_at
		   FROM maintenance_leases
		  WHERE name = ?`,
		name,
	).Scan(&curOwner, &acquiredText, &heartbeatText)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		ts := timestamp(now.UTC())
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO maintenance_leases(name, owner_token, acquired_at, heartbeat_at)
			 VALUES(?, ?, ?, ?)`,
			name, ownerToken, ts, ts,
		); err != nil {
			return false, nil, fmt.Errorf("store.AcquireMaintenanceLease: insert: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, nil, fmt.Errorf("store.AcquireMaintenanceLease: commit insert: %w", err)
		}
		return true, &MaintenanceLease{
			Name:        name,
			OwnerToken:  ownerToken,
			AcquiredAt:  now.UTC(),
			HeartbeatAt: now.UTC(),
		}, nil
	case err != nil:
		return false, nil, fmt.Errorf("store.AcquireMaintenanceLease: read: %w", err)
	}

	acquiredAt, err := time.Parse(time.RFC3339Nano, acquiredText)
	if err != nil {
		return false, nil, fmt.Errorf("store.AcquireMaintenanceLease: parse acquired_at: %w", err)
	}
	heartbeatAt, err := time.Parse(time.RFC3339Nano, heartbeatText)
	if err != nil {
		return false, nil, fmt.Errorf("store.AcquireMaintenanceLease: parse heartbeat_at: %w", err)
	}
	live := now.UTC().Sub(heartbeatAt) < ttl
	if curOwner == ownerToken || !live {
		ts := timestamp(now.UTC())
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE maintenance_leases
			    SET owner_token = ?, acquired_at = ?, heartbeat_at = ?
			  WHERE name = ?`,
			ownerToken, ts, ts, name,
		); err != nil {
			return false, nil, fmt.Errorf("store.AcquireMaintenanceLease: update: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, nil, fmt.Errorf("store.AcquireMaintenanceLease: commit update: %w", err)
		}
		return true, &MaintenanceLease{
			Name:        name,
			OwnerToken:  ownerToken,
			AcquiredAt:  now.UTC(),
			HeartbeatAt: now.UTC(),
		}, nil
	}
	return false, &MaintenanceLease{
		Name:        name,
		OwnerToken:  curOwner,
		AcquiredAt:  acquiredAt.UTC(),
		HeartbeatAt: heartbeatAt.UTC(),
	}, nil
}

// RefreshMaintenanceLease updates the heartbeat for the named lease when
// the caller still owns it. Returns false when the lease is absent or is
// owned by another process.
func (s *Store) RefreshMaintenanceLease(ctx context.Context, name, ownerToken string, now time.Time) (bool, error) {
	res, err := s.db.ExecContext(
		ctx,
		`UPDATE maintenance_leases
		    SET heartbeat_at = ?
		  WHERE name = ? AND owner_token = ?`,
		timestamp(now.UTC()), name, ownerToken,
	)
	if err != nil {
		return false, fmt.Errorf("store.RefreshMaintenanceLease: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ReleaseMaintenanceLease deletes the lease when the caller owns it.
func (s *Store) ReleaseMaintenanceLease(ctx context.Context, name, ownerToken string) error {
	if _, err := s.db.ExecContext(
		ctx,
		`DELETE FROM maintenance_leases WHERE name = ? AND owner_token = ?`,
		name, ownerToken,
	); err != nil {
		return fmt.Errorf("store.ReleaseMaintenanceLease: %w", err)
	}
	return nil
}

// BreakMaintenanceLease forcibly removes the named lease.
func (s *Store) BreakMaintenanceLease(ctx context.Context, name string) error {
	if _, err := s.db.ExecContext(
		ctx,
		`DELETE FROM maintenance_leases WHERE name = ?`,
		name,
	); err != nil {
		return fmt.Errorf("store.BreakMaintenanceLease: %w", err)
	}
	return nil
}

// FileIngestState is the per-file manifest row maintained by successful
// ingest/replay passes.
type FileIngestState struct {
	Tool             string
	Path             string
	SizeBytes        int64
	MtimeUnixNS      int64
	LastScanAt       time.Time
	ParserVersion    int
	LastError        string
	LastFullReplayAt *time.Time
}

// UpsertFileIngestState records the latest successful ingest metadata for
// a file. last_full_replay_at is advanced only when fullReplay is true.
func (s *Store) UpsertFileIngestState(ctx context.Context, st FileIngestState, fullReplay bool) error {
	if st.Tool == "" || st.Path == "" {
		return errors.New("store.UpsertFileIngestState: tool and path required")
	}
	if st.LastScanAt.IsZero() {
		st.LastScanAt = time.Now().UTC()
	}
	if st.ParserVersion <= 0 {
		st.ParserVersion = 1
	}
	var replayAt any
	if fullReplay {
		t := st.LastScanAt.UTC()
		replayAt = timestamp(t)
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO file_ingest_state
		   (tool, path, size_bytes, mtime_unix_ns, last_scan_at, parser_version, last_error, last_full_replay_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(tool, path) DO UPDATE SET
		   size_bytes = excluded.size_bytes,
		   mtime_unix_ns = excluded.mtime_unix_ns,
		   last_scan_at = excluded.last_scan_at,
		   parser_version = excluded.parser_version,
		   last_error = NULLIF(excluded.last_error, ''),
		   last_full_replay_at = COALESCE(excluded.last_full_replay_at, file_ingest_state.last_full_replay_at)`,
		st.Tool,
		st.Path,
		st.SizeBytes,
		st.MtimeUnixNS,
		timestamp(st.LastScanAt.UTC()),
		st.ParserVersion,
		nullableString(st.LastError),
		replayAt,
	)
	if err != nil {
		return fmt.Errorf("store.UpsertFileIngestState: %w", err)
	}
	return nil
}

// ListRepairCandidateFiles returns distinct source files for the given tool
// whose repair_key is absent or below targetVersion. Candidate enumeration is
// DB-driven; no filesystem walk is performed here.
func (s *Store) ListRepairCandidateFiles(ctx context.Context, tool, repairKey string, targetVersion int) ([]string, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`WITH source_files(path) AS (
		     SELECT path
		       FROM file_ingest_state
		      WHERE tool = ?
		     UNION
		     SELECT source_file
		       FROM actions
		      WHERE tool = ? AND source_file IS NOT NULL AND source_file != ''
		     UNION
		     SELECT source_file
		       FROM token_usage
		      WHERE tool = ? AND source_file IS NOT NULL AND source_file != ''
		   )
		   SELECT sf.path
		     FROM source_files sf
		     LEFT JOIN file_repair_versions rv
		       ON rv.tool = ? AND rv.path = sf.path AND rv.repair_key = ?
		    WHERE sf.path IS NOT NULL
		      AND sf.path != ''
		      AND (rv.version IS NULL OR rv.version < ?)
		    ORDER BY sf.path`,
		tool, tool, tool, tool, repairKey, targetVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("store.ListRepairCandidateFiles: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("store.ListRepairCandidateFiles: %w", err)
		}
		if path == "" {
			continue
		}
		out = append(out, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store.ListRepairCandidateFiles: %w", err)
	}
	return out, nil
}

// MarkRepairAppliedBatch records repair_key=version for each successfully
// processed file.
func (s *Store) MarkRepairAppliedBatch(ctx context.Context, tool, repairKey string, version int, paths []string) error {
	if tool == "" || repairKey == "" {
		return errors.New("store.MarkRepairAppliedBatch: tool and repairKey required")
	}
	if len(paths) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store.MarkRepairAppliedBatch: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmt, err := tx.PrepareContext(
		ctx,
		`INSERT INTO file_repair_versions(tool, path, repair_key, version, updated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(tool, path, repair_key) DO UPDATE SET
		   version = excluded.version,
		   updated_at = excluded.updated_at`,
	)
	if err != nil {
		return fmt.Errorf("store.MarkRepairAppliedBatch: prepare: %w", err)
	}
	defer stmt.Close()
	ts := timestamp(time.Now().UTC())
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, tool, path, repairKey, version, ts); err != nil {
			return fmt.Errorf("store.MarkRepairAppliedBatch: exec %s: %w", path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store.MarkRepairAppliedBatch: commit: %w", err)
	}
	return nil
}

// ProcessExists returns whether the pid currently exists on this host.
// Best-effort helper for human-facing diagnostics only.
func ProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}
