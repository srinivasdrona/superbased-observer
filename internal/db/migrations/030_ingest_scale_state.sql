-- 030_ingest_scale_state.sql
--
-- First slice of the ingest-scale redesign:
--   1. lightweight maintenance lease so multiple historical backfill
--      jobs do not contend on the same SQLite DB
--   2. manifest-style per-file ingest state
--   3. per-file, per-repair version tracking so routine maintenance can
--      select candidate files without rewalking every history tree

CREATE TABLE IF NOT EXISTS maintenance_leases (
    name         TEXT PRIMARY KEY,
    owner_token  TEXT NOT NULL,
    acquired_at  TEXT NOT NULL,
    heartbeat_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS file_ingest_state (
    tool                TEXT NOT NULL,
    path                TEXT NOT NULL,
    size_bytes          INTEGER NOT NULL,
    mtime_unix_ns       INTEGER NOT NULL,
    last_scan_at        TEXT NOT NULL,
    parser_version      INTEGER NOT NULL DEFAULT 1,
    last_error          TEXT,
    last_full_replay_at TEXT,
    PRIMARY KEY (tool, path)
);
CREATE INDEX IF NOT EXISTS idx_file_ingest_state_tool_scan
    ON file_ingest_state(tool, last_scan_at);

CREATE TABLE IF NOT EXISTS file_repair_versions (
    tool       TEXT NOT NULL,
    path       TEXT NOT NULL,
    repair_key TEXT NOT NULL,
    version    INTEGER NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (tool, path, repair_key)
);
CREATE INDEX IF NOT EXISTS idx_file_repair_versions_lookup
    ON file_repair_versions(tool, repair_key, version);
