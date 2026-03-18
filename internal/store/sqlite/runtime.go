package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

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

type sharedRuntimeDB struct {
	db   *RuntimeDB
	refs int
}

var sharedRuntimeDBs struct {
	mu    sync.Mutex
	paths map[string]*sharedRuntimeDB
}

func Open(path string, extraMigrations []Migration) (*RuntimeDB, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is required")
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create sqlite directory: %w", err)
	}

	if db, ok := retainSharedRuntimeDB(path); ok {
		if err := ensureSchema(db.DB(), append(baseMigrations(), extraMigrations...)); err != nil {
			_ = db.Close()
			return nil, err
		}
		return db, nil
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := applyPragmas(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureSchema(db, append(baseMigrations(), extraMigrations...)); err != nil {
		_ = db.Close()
		return nil, err
	}

	runtimeDB := &RuntimeDB{db: db, path: path}
	return storeSharedRuntimeDB(runtimeDB), nil
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
	return releaseSharedRuntimeDB(r)
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

func (r *RuntimeDB) QuickCheck() (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("sqlite db is not open")
	}
	var result string
	if err := r.db.QueryRow(`PRAGMA quick_check;`).Scan(&result); err != nil {
		return "", fmt.Errorf("run sqlite quick_check: %w", err)
	}
	return result, nil
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
			ID: "0001_webinfo_state",
			Up: `
				CREATE TABLE IF NOT EXISTS webinfo_state (
					file_path TEXT PRIMARY KEY,
					payload_json TEXT NOT NULL,
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000)
				);
			`,
		},
		{
			ID: "0002_requery_state",
			Up: `
				CREATE TABLE IF NOT EXISTS requery_state (
					file_path TEXT NOT NULL,
					state_kind TEXT NOT NULL,
					payload_json TEXT NOT NULL,
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000),
					PRIMARY KEY (file_path, state_kind)
				);
				CREATE INDEX IF NOT EXISTS idx_requery_state_file
				ON requery_state(file_path, updated_at_unix_ms DESC);
				CREATE INDEX IF NOT EXISTS idx_requery_state_kind
				ON requery_state(state_kind, updated_at_unix_ms DESC);
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
		{
			ID: "0010_adguard_rule_item",
			Up: `
				CREATE TABLE IF NOT EXISTS adguard_rule_item (
					config_key TEXT NOT NULL,
					rule_id TEXT NOT NULL,
					name TEXT NOT NULL DEFAULT '',
					url TEXT NOT NULL DEFAULT '',
					enabled INTEGER NOT NULL DEFAULT 0,
					auto_update INTEGER NOT NULL DEFAULT 0,
					update_interval_hours INTEGER NOT NULL DEFAULT 0,
					payload_json TEXT NOT NULL,
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000),
					PRIMARY KEY (config_key, rule_id)
				);
				CREATE INDEX IF NOT EXISTS idx_adguard_rule_item_config
				ON adguard_rule_item(config_key, updated_at_unix_ms DESC);
				CREATE INDEX IF NOT EXISTS idx_adguard_rule_item_enabled
				ON adguard_rule_item(enabled, updated_at_unix_ms DESC);
			`,
		},
		{
			ID: "0011_diversion_rule_source",
			Up: `
				CREATE TABLE IF NOT EXISTS diversion_rule_source (
					config_key TEXT NOT NULL,
					source_name TEXT NOT NULL,
					source_type TEXT NOT NULL DEFAULT '',
					files TEXT NOT NULL DEFAULT '',
					url TEXT NOT NULL DEFAULT '',
					enabled INTEGER NOT NULL DEFAULT 0,
					auto_update INTEGER NOT NULL DEFAULT 0,
					update_interval_hours INTEGER NOT NULL DEFAULT 0,
					payload_json TEXT NOT NULL,
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000),
					PRIMARY KEY (config_key, source_name)
				);
				CREATE INDEX IF NOT EXISTS idx_diversion_rule_source_config
				ON diversion_rule_source(config_key, updated_at_unix_ms DESC);
				CREATE INDEX IF NOT EXISTS idx_diversion_rule_source_enabled
				ON diversion_rule_source(enabled, updated_at_unix_ms DESC);
			`,
		},
		{
			ID: "0013_drop_legacy_control_config_tables",
			Up: `
				DROP TABLE IF EXISTS global_override_state;
				DROP TABLE IF EXISTS upstream_override_item;
			`,
		},
		{
			ID: "0014_drop_switch_state",
			Up: `
				DROP TABLE IF EXISTS switch_state;
			`,
		},
		{
			ID: "0015_domain_pool_state",
			Up: `
				CREATE TABLE IF NOT EXISTS domain_pool_meta (
					pool_tag TEXT PRIMARY KEY,
					pool_kind TEXT NOT NULL,
					memory_id TEXT NOT NULL DEFAULT '',
					policy_json TEXT NOT NULL DEFAULT '{}',
					domain_count INTEGER NOT NULL DEFAULT 0,
					variant_count INTEGER NOT NULL DEFAULT 0,
					dirty_domain_count INTEGER NOT NULL DEFAULT 0,
					promoted_domain_count INTEGER NOT NULL DEFAULT 0,
					published_domain_count INTEGER NOT NULL DEFAULT 0,
					total_observations INTEGER NOT NULL DEFAULT 0,
					dropped_observations INTEGER NOT NULL DEFAULT 0,
					dropped_by_buffer INTEGER NOT NULL DEFAULT 0,
					dropped_by_cap INTEGER NOT NULL DEFAULT 0,
					evicted_domains INTEGER NOT NULL DEFAULT 0,
					evicted_variants INTEGER NOT NULL DEFAULT 0,
					last_ingested_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					last_flush_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					last_publish_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					last_prune_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000)
				);
				CREATE TABLE IF NOT EXISTS domain_pool_domain (
					pool_tag TEXT NOT NULL,
					domain TEXT NOT NULL,
					total_count INTEGER NOT NULL DEFAULT 0,
					score INTEGER NOT NULL DEFAULT 0,
					qtype_mask INTEGER NOT NULL DEFAULT 0,
					flags_mask INTEGER NOT NULL DEFAULT 0,
					variant_count INTEGER NOT NULL DEFAULT 0,
					dirty_variant_count INTEGER NOT NULL DEFAULT 0,
					promoted INTEGER NOT NULL DEFAULT 0,
					last_source TEXT NOT NULL DEFAULT '',
					last_seen_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					last_dirty_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					last_verified_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					cooldown_until_unix_ms INTEGER NOT NULL DEFAULT 0,
					dirty_reason TEXT NOT NULL DEFAULT '',
					refresh_state TEXT NOT NULL DEFAULT '',
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000),
					PRIMARY KEY (pool_tag, domain),
					FOREIGN KEY (pool_tag) REFERENCES domain_pool_meta(pool_tag) ON DELETE CASCADE
				);
				CREATE TABLE IF NOT EXISTS domain_pool_variant (
					pool_tag TEXT NOT NULL,
					domain TEXT NOT NULL,
					variant_key TEXT NOT NULL,
					total_count INTEGER NOT NULL DEFAULT 0,
					score INTEGER NOT NULL DEFAULT 0,
					qtype_mask INTEGER NOT NULL DEFAULT 0,
					flags_mask INTEGER NOT NULL DEFAULT 0,
					promoted INTEGER NOT NULL DEFAULT 0,
					last_source TEXT NOT NULL DEFAULT '',
					last_seen_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					last_dirty_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					last_verified_at_unix_ms INTEGER NOT NULL DEFAULT 0,
					cooldown_until_unix_ms INTEGER NOT NULL DEFAULT 0,
					dirty_reason TEXT NOT NULL DEFAULT '',
					refresh_state TEXT NOT NULL DEFAULT '',
					conflict_count INTEGER NOT NULL DEFAULT 0,
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000),
					PRIMARY KEY (pool_tag, domain, variant_key),
					FOREIGN KEY (pool_tag, domain) REFERENCES domain_pool_domain(pool_tag, domain) ON DELETE CASCADE
				);
				CREATE INDEX IF NOT EXISTS idx_domain_pool_domain_score
				ON domain_pool_domain(pool_tag, score DESC, total_count DESC, domain ASC);
				CREATE INDEX IF NOT EXISTS idx_domain_pool_domain_seen
				ON domain_pool_domain(pool_tag, last_seen_at_unix_ms DESC);
				CREATE INDEX IF NOT EXISTS idx_domain_pool_variant_score
				ON domain_pool_variant(pool_tag, score DESC, total_count DESC, domain ASC, variant_key ASC);
				CREATE INDEX IF NOT EXISTS idx_domain_pool_variant_seen
				ON domain_pool_variant(pool_tag, last_seen_at_unix_ms DESC);
			`,
		},
		{
			ID: "0016_drop_generated_dataset",
			Up: `
				DROP TABLE IF EXISTS generated_dataset;
			`,
		},
		{
			ID: "0017_rule_source_status",
			Up: `
				CREATE TABLE IF NOT EXISTS rule_source_status (
					scope TEXT NOT NULL,
					source_id TEXT NOT NULL,
					rule_count INTEGER NOT NULL DEFAULT 0,
					last_updated_unix_ms INTEGER NOT NULL DEFAULT 0,
					last_error TEXT NOT NULL DEFAULT '',
					updated_at_unix_ms INTEGER NOT NULL DEFAULT (unixepoch('subsec') * 1000),
					PRIMARY KEY (scope, source_id)
				);
				CREATE INDEX IF NOT EXISTS idx_rule_source_status_scope
				ON rule_source_status(scope, updated_at_unix_ms DESC);
			`,
		},
		{
			ID: "0018_drop_audit_state",
			Up: `
				DROP TABLE IF EXISTS audit_state;
			`,
		},
	}
}

func retainSharedRuntimeDB(path string) (*RuntimeDB, bool) {
	sharedRuntimeDBs.mu.Lock()
	defer sharedRuntimeDBs.mu.Unlock()

	if sharedRuntimeDBs.paths == nil {
		sharedRuntimeDBs.paths = make(map[string]*sharedRuntimeDB)
	}
	entry := sharedRuntimeDBs.paths[path]
	if entry == nil || entry.db == nil {
		return nil, false
	}
	entry.refs++
	return entry.db, true
}

func storeSharedRuntimeDB(db *RuntimeDB) *RuntimeDB {
	sharedRuntimeDBs.mu.Lock()
	defer sharedRuntimeDBs.mu.Unlock()

	if sharedRuntimeDBs.paths == nil {
		sharedRuntimeDBs.paths = make(map[string]*sharedRuntimeDB)
	}
	if entry := sharedRuntimeDBs.paths[db.path]; entry != nil && entry.db != nil {
		entry.refs++
		_ = db.db.Close()
		return entry.db
	}
	sharedRuntimeDBs.paths[db.path] = &sharedRuntimeDB{db: db, refs: 1}
	return db
}

func releaseSharedRuntimeDB(runtimeDB *RuntimeDB) error {
	sharedRuntimeDBs.mu.Lock()
	defer sharedRuntimeDBs.mu.Unlock()

	entry := sharedRuntimeDBs.paths[runtimeDB.path]
	if entry == nil || entry.db != runtimeDB {
		return runtimeDB.db.Close()
	}
	entry.refs--
	if entry.refs > 0 {
		return nil
	}
	delete(sharedRuntimeDBs.paths, runtimeDB.path)
	return runtimeDB.db.Close()
}
