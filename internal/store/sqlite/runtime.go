package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Migration struct {
	ID string
	Up string
}

type RuntimeDB struct {
	db   *sql.DB
	path string
}

func Open(path string, extraMigrations []Migration) (*RuntimeDB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	if err := applyPragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureSchema(db, append(baseMigrations(), extraMigrations...)); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &RuntimeDB{db: db, path: path}, nil
}

func (r *RuntimeDB) DB() *sql.DB {
	return r.db
}

func (r *RuntimeDB) Path() string {
	return r.path
}

func (r *RuntimeDB) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func (r *RuntimeDB) FileSizeBytes() (int64, error) {
	info, err := os.Stat(r.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

func applyPragmas(db *sql.DB) error {
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA synchronous = NORMAL;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA temp_store = MEMORY;",
		"PRAGMA busy_timeout = 3000;",
	}
	for _, stmt := range pragmas {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("apply sqlite pragma %q: %w", stmt, err)
		}
	}
	return nil
}

func ensureSchema(db *sql.DB, migrations []Migration) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			id TEXT PRIMARY KEY,
			applied_at_unix_ms INTEGER NOT NULL
		);
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin sqlite migration tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	for _, migration := range migrations {
		var exists string
		err = tx.QueryRow(`SELECT id FROM schema_migrations WHERE id = ?`, migration.ID).Scan(&exists)
		switch {
		case err == nil:
			continue
		case err != sql.ErrNoRows:
			return fmt.Errorf("check migration %s: %w", migration.ID, err)
		}

		if _, err = tx.Exec(migration.Up); err != nil {
			return fmt.Errorf("apply migration %s: %w", migration.ID, err)
		}
		if _, err = tx.Exec(
			`INSERT INTO schema_migrations (id, applied_at_unix_ms) VALUES (?, unixepoch('subsec') * 1000)`,
			migration.ID,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", migration.ID, err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite migrations: %w", err)
	}
	return nil
}

func baseMigrations() []Migration {
	return []Migration{
		{
			ID: "0001_runtime_kv",
			Up: `
				CREATE TABLE IF NOT EXISTS runtime_kv (
					namespace TEXT NOT NULL,
					key TEXT NOT NULL,
					value_json TEXT NOT NULL,
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000),
					PRIMARY KEY (namespace, key)
				);
			`,
		},
		{
			ID: "0002_system_event",
			Up: `
				CREATE TABLE IF NOT EXISTS system_event (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					component TEXT NOT NULL,
					level TEXT NOT NULL,
					message TEXT NOT NULL,
					details_json TEXT NOT NULL DEFAULT '{}',
					created_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000)
				);
				CREATE INDEX IF NOT EXISTS idx_system_event_component_time
				ON system_event(component, created_at_unix_ms DESC);
			`,
		},
		{
			ID: "0003_requery_job",
			Up: `
				CREATE TABLE IF NOT EXISTS requery_job (
					job_id TEXT PRIMARY KEY,
					config_key TEXT NOT NULL,
					mode TEXT NOT NULL,
					trigger_source TEXT NOT NULL,
					enabled INTEGER NOT NULL DEFAULT 1,
					definition_json TEXT NOT NULL DEFAULT '{}',
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000)
				);
				CREATE INDEX IF NOT EXISTS idx_requery_job_config_key
				ON requery_job(config_key, updated_at_unix_ms DESC);
			`,
		},
		{
			ID: "0004_requery_run",
			Up: `
				CREATE TABLE IF NOT EXISTS requery_run (
					run_id TEXT PRIMARY KEY,
					config_key TEXT NOT NULL,
					job_id TEXT NOT NULL DEFAULT '',
					mode TEXT NOT NULL,
					trigger_source TEXT NOT NULL,
					state TEXT NOT NULL,
					stage TEXT NOT NULL DEFAULT '',
					stage_label TEXT NOT NULL DEFAULT '',
					total INTEGER NOT NULL DEFAULT 0,
					completed INTEGER NOT NULL DEFAULT 0,
					error_text TEXT NOT NULL DEFAULT '',
					metadata_json TEXT NOT NULL DEFAULT '{}',
					started_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					ended_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000)
				);
				CREATE INDEX IF NOT EXISTS idx_requery_run_config_key
				ON requery_run(config_key, updated_at_unix_ms DESC);
				CREATE INDEX IF NOT EXISTS idx_requery_run_mode_state
				ON requery_run(mode, state, updated_at_unix_ms DESC);
			`,
		},
		{
			ID: "0005_requery_checkpoint",
			Up: `
				CREATE TABLE IF NOT EXISTS requery_checkpoint (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					config_key TEXT NOT NULL,
					run_id TEXT NOT NULL,
					stage TEXT NOT NULL,
					completed INTEGER NOT NULL DEFAULT 0,
					total INTEGER NOT NULL DEFAULT 0,
					snapshot_json TEXT NOT NULL DEFAULT '{}',
					created_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000),
					FOREIGN KEY(run_id) REFERENCES requery_run(run_id) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_requery_checkpoint_run
				ON requery_checkpoint(run_id, created_at_unix_ms DESC);
				CREATE INDEX IF NOT EXISTS idx_requery_checkpoint_config_key
				ON requery_checkpoint(config_key, created_at_unix_ms DESC);
			`,
		},
	}
}
