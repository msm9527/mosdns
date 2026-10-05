package sqlite

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestDomainPoolSeenIndexMigrationPreservesHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.db")
	seed, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	migrations := baseMigrations()
	old := make([]Migration, 0, len(migrations))
	for _, migration := range migrations {
		if migration.ID != "0020_drop_unused_domain_pool_seen_indexes" {
			old = append(old, migration)
		}
	}
	if err := ensureSchema(seed, old); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO domain_pool_meta(pool_tag,pool_kind,total_observations) VALUES ('pool','stats',42)`,
		`INSERT INTO domain_pool_domain(pool_tag,domain,total_count,score,last_seen_at_unix_ms,updated_at_unix_ms) VALUES ('pool','kept.example',42,19,123456,789)`,
		`INSERT INTO domain_pool_variant(pool_tag,domain,variant_key,total_count,score,last_seen_at_unix_ms,updated_at_unix_ms) VALUES ('pool','kept.example','f:0',42,19,123456,789)`,
	} {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	indexes := func(db *sql.DB) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name IN ('idx_domain_pool_domain_seen','idx_domain_pool_variant_seen')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if indexes(seed) != 2 {
		t.Fatal("legacy fixture lacks indexes")
	}
	// A failure later in the same migration transaction must retain the old schema.
	failing := append(append([]Migration{}, migrations...), Migration{ID: "test_failure", Up: "INSERT INTO missing_table VALUES (1)"})
	if err := ensureSchema(seed, failing); err == nil {
		t.Fatal("injected migration should fail")
	}
	if indexes(seed) != 2 {
		t.Fatal("failed migration partially removed indexes")
	}
	db, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if indexes(db.DB()) != 0 {
		t.Fatal("unused indexes remain")
	}
	for _, table := range []string{"domain_pool_domain", "domain_pool_variant"} {
		var count, score, seen, updated int64
		if err := db.DB().QueryRow("SELECT total_count,score,last_seen_at_unix_ms,updated_at_unix_ms FROM "+table+" WHERE pool_tag='pool' AND domain='kept.example'").Scan(&count, &score, &seen, &updated); err != nil {
			t.Fatal(err)
		}
		if count != 42 || score != 19 || seen != 123456 || updated != 789 {
			t.Fatalf("%s history changed", table)
		}
		rows, err := db.DB().Query("EXPLAIN QUERY PLAN SELECT domain FROM " + table + " WHERE pool_tag='pool' ORDER BY score DESC,total_count DESC,domain ASC LIMIT 20")
		if err != nil {
			t.Fatal(err)
		}
		usedScore := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(detail, "TEMP B-TREE") {
				t.Fatal("ranking lost its ordered index")
			}
			usedScore = usedScore || strings.Contains(detail, "idx_"+table+"_score")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !usedScore {
			t.Fatalf("%s ranking index not used", table)
		}
	}
	var before, after int
	if err := db.DB().QueryRow("PRAGMA schema_version").Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := ensureSchema(db.DB(), migrations); err != nil {
		t.Fatal(err)
	}
	if err := db.DB().QueryRow("PRAGMA schema_version").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("reopen repeated schema changes")
	}
}
