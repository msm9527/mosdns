package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenDropsLegacyControlConfigTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control.db")

	seedDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer seedDB.Close()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (id TEXT PRIMARY KEY, applied_at_unix_ms INTEGER NOT NULL);`,
		`INSERT INTO schema_migrations (id, applied_at_unix_ms) VALUES ('0008_global_override_state', 1);`,
		`INSERT INTO schema_migrations (id, applied_at_unix_ms) VALUES ('0009_upstream_override_item', 1);`,
		`CREATE TABLE global_override_state (scope_key TEXT PRIMARY KEY, socks5 TEXT NOT NULL DEFAULT '');`,
		`CREATE TABLE upstream_override_item (plugin_tag TEXT NOT NULL, upstream_tag TEXT NOT NULL, payload_json TEXT NOT NULL, PRIMARY KEY (plugin_tag, upstream_tag));`,
	}
	for _, stmt := range stmts {
		if _, err := seedDB.Exec(stmt); err != nil {
			t.Fatalf("seed legacy tables: %v", err)
		}
	}

	rdb, err := Open(dbPath, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rdb.Close()

	for _, table := range []string{"global_override_state", "upstream_override_item"} {
		var count int
		if err := rdb.DB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&count); err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("expected legacy table %s to be dropped", table)
		}
	}
}

func TestOpenDropsLegacySwitchStateTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control.db")

	seedDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open seed db: %v", err)
	}
	defer seedDB.Close()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (id TEXT PRIMARY KEY, applied_at_unix_ms INTEGER NOT NULL);`,
		`INSERT INTO schema_migrations (id, applied_at_unix_ms) VALUES ('0006_switch_state', 1);`,
		`CREATE TABLE switch_state (file_path TEXT NOT NULL, switch_name TEXT NOT NULL, value TEXT NOT NULL, PRIMARY KEY (file_path, switch_name));`,
	}
	for _, stmt := range stmts {
		if _, err := seedDB.Exec(stmt); err != nil {
			t.Fatalf("seed legacy switch_state table: %v", err)
		}
	}

	rdb, err := Open(dbPath, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rdb.Close()

	for _, table := range []string{"switch_state"} {
		var count int
		if err := rdb.DB().QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
			table,
		).Scan(&count); err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("expected legacy table %s to be dropped", table)
		}
	}
}
