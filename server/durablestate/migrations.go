package durablestate

// migration is one versioned, embedded schema step. Down is included where
// a safe reversal exists (plain DROP/ALTER-back); Go's database/sql +
// SQLite make some reversals (e.g. dropping a column pre-3.35) awkward, so
// a handful of Down statements recreate the table instead of altering it in
// place — still a real, tested reversal, not a no-op.
type migration struct {
	version int
	name    string
	up      []string
	down    []string
}

// migrations is applied in ascending version order by Migrate, and in
// descending order (down) by MigrateDown. Keep every historical migration
// here permanently once released — never edit an already-applied one in
// place — the same append-only discipline this package enforces on its own
// data applies to its own schema.
var migrations = []migration{
	{
		version: 1,
		name:    "initial_durable_state_schema",
		up: []string{
			`CREATE TABLE schema_migrations (
				version    INTEGER PRIMARY KEY,
				name       TEXT NOT NULL,
				applied_at TEXT NOT NULL
			)`,
			// §6 single-writer fencing: one row per workspace holding the
			// highest fencing token ever accepted. Only AcquireLease
			// advances this value; mutation/compaction/restore calls only
			// ever compare against it.
			`CREATE TABLE workspace_fencing (
				workspace_id  TEXT PRIMARY KEY,
				fencing_token INTEGER NOT NULL DEFAULT 0,
				leader_id     TEXT,
				updated_at    TEXT NOT NULL
			)`,
			// §1.1's materialized current-state surface: exactly what an
			// agent needs to act right now, one row per live record.
			`CREATE TABLE current_state (
				workspace_id TEXT NOT NULL,
				record_id    TEXT NOT NULL,
				record_kind  TEXT NOT NULL,
				body         TEXT NOT NULL,
				sha256       TEXT NOT NULL,
				updated_at   TEXT NOT NULL,
				PRIMARY KEY (workspace_id, record_id)
			)`,
			`CREATE INDEX idx_current_state_workspace_kind
				ON current_state (workspace_id, record_kind)`,
			// §1.3 append-only mutation log. mutation_id is monotonic
			// per-workspace (not a global autoincrement), enforced by
			// nextMutationID under BEGIN IMMEDIATE.
			`CREATE TABLE mutation_log (
				workspace_id     TEXT NOT NULL,
				mutation_id      INTEGER NOT NULL,
				timestamp        TEXT NOT NULL,
				op               TEXT NOT NULL,
				record_id        TEXT NOT NULL,
				record_kind      TEXT NOT NULL,
				before_storage   TEXT,
				before_sha256    TEXT,
				before_body      TEXT,
				before_ref       TEXT,
				after_storage    TEXT,
				after_sha256     TEXT,
				after_body       TEXT,
				after_ref        TEXT,
				compaction_id    TEXT,
				restore_id       TEXT,
				fencing_token    INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, mutation_id)
			)`,
			`CREATE INDEX idx_mutation_log_record
				ON mutation_log (workspace_id, record_id)`,
			`CREATE INDEX idx_mutation_log_compaction
				ON mutation_log (workspace_id, compaction_id)`,
			// §1.3 content-addressed payload store backing content_ref
			// bodies not already covered by the archive table.
			`CREATE TABLE content_store (
				workspace_id TEXT NOT NULL,
				sha256       TEXT NOT NULL,
				body         TEXT NOT NULL,
				created_at   TEXT NOT NULL,
				PRIMARY KEY (workspace_id, sha256)
			)`,
			// §1.1 full snapshots. content is the canonical JSON of the
			// full kind -> record_id -> body map.
			`CREATE TABLE snapshots (
				snapshot_id            TEXT NOT NULL,
				workspace_id           TEXT NOT NULL,
				timestamp              TEXT NOT NULL,
				trigger                TEXT NOT NULL,
				content                TEXT NOT NULL,
				byte_count             INTEGER NOT NULL,
				sha256                 TEXT NOT NULL,
				record_count           INTEGER NOT NULL,
				record_id_set_sha256   TEXT NOT NULL,
				mutation_log_watermark INTEGER,
				fencing_token          INTEGER NOT NULL,
				PRIMARY KEY (workspace_id, snapshot_id)
			)`,
			`CREATE INDEX idx_snapshots_workspace_ts
				ON snapshots (workspace_id, timestamp)`,
			// Append-only archive: full bodies of rolled/resolved records,
			// never overwritten or deleted (short of the receipted
			// snapshot_prune/retention procedures in §1.2/§1.3, which never
			// touch this table's rows directly).
			`CREATE TABLE archive (
				id            INTEGER PRIMARY KEY AUTOINCREMENT,
				workspace_id  TEXT NOT NULL,
				compaction_id TEXT NOT NULL,
				record_id     TEXT NOT NULL,
				record_kind   TEXT NOT NULL,
				resolved_at   TEXT NOT NULL,
				body          TEXT NOT NULL,
				sha256        TEXT NOT NULL,
				appended_at   TEXT NOT NULL
			)`,
			`CREATE INDEX idx_archive_workspace_compaction
				ON archive (workspace_id, compaction_id)`,
			`CREATE UNIQUE INDEX idx_archive_workspace_compaction_record
				ON archive (workspace_id, compaction_id, record_id)`,
			// §3/§7.5/§1.2 receipts: rollup, restore_reactivation, and
			// snapshot_prune all share this hash-anchored shape.
			`CREATE TABLE compaction_receipts (
				compaction_id                       TEXT NOT NULL,
				workspace_id                        TEXT NOT NULL,
				timestamp                           TEXT NOT NULL,
				kind                                 TEXT NOT NULL,
				restore_id                           TEXT,
				pre_snapshot_id                      TEXT,
				pre_byte_count                       INTEGER,
				pre_sha256                           TEXT,
				pre_record_count                     INTEGER,
				pre_record_id_set_sha256             TEXT,
				rolled_records                       TEXT NOT NULL,
				post_snapshot_id                     TEXT,
				post_byte_count                      INTEGER,
				post_sha256                          TEXT,
				post_record_count                    INTEGER,
				post_record_id_set_sha256            TEXT,
				archive_path                         TEXT,
				archive_byte_count_appended           INTEGER,
				archive_sha256_of_appended_bytes      TEXT,
				archive_rolled_record_id_set_sha256   TEXT,
				fencing_token                        INTEGER NOT NULL,
				phase                                 TEXT NOT NULL,
				PRIMARY KEY (workspace_id, compaction_id)
			)`,
			`CREATE INDEX idx_compaction_receipts_phase
				ON compaction_receipts (workspace_id, phase)`,
			// §5 replay crash/retry checkpointing: "successfully applied up
			// to mutation_id = k" as a single comparable value per named
			// replay run, so a crash mid-replay resumes from k+1 instead of
			// restarting from the base snapshot.
			`CREATE TABLE replay_checkpoints (
				workspace_id          TEXT NOT NULL,
				checkpoint_key        TEXT NOT NULL,
				last_applied_mutation INTEGER NOT NULL,
				updated_at            TEXT NOT NULL,
				PRIMARY KEY (workspace_id, checkpoint_key)
			)`,
			// Narrow health counters (compaction verification failures,
			// receipt failures, lease/recovery counters) surfaced via
			// health.go's Health() query.
			`CREATE TABLE health_counters (
				workspace_id  TEXT NOT NULL,
				counter_name  TEXT NOT NULL,
				value         INTEGER NOT NULL DEFAULT 0,
				updated_at    TEXT NOT NULL,
				PRIMARY KEY (workspace_id, counter_name)
			)`,
		},
		down: []string{
			`DROP TABLE IF EXISTS health_counters`,
			`DROP TABLE IF EXISTS replay_checkpoints`,
			`DROP TABLE IF EXISTS compaction_receipts`,
			`DROP TABLE IF EXISTS archive`,
			`DROP TABLE IF EXISTS snapshots`,
			`DROP TABLE IF EXISTS content_store`,
			`DROP TABLE IF EXISTS mutation_log`,
			`DROP TABLE IF EXISTS current_state`,
			`DROP TABLE IF EXISTS workspace_fencing`,
			`DROP TABLE IF EXISTS schema_migrations`,
		},
	},
}
