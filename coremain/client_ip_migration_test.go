package coremain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyClientIPListForBaseDirWhitelistMode(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteMigrationTestFile(t, filepath.Join(baseDir, legacyClientIPListRelPath), "\n# old\n192.0.2.1\n198.51.100.0/24\n")
	mustWriteMigrationTestFile(t, filepath.Join(baseDir, customConfigDirname, switchesConfigFilename), "client_proxy_mode: whitelist\n")

	if err := migrateLegacyClientIPListForBaseDir(baseDir); err != nil {
		t.Fatalf("migrateLegacyClientIPListForBaseDir: %v", err)
	}

	assertMigrationFileEquals(t, filepath.Join(baseDir, clientIPWhitelistListRelPath), "192.0.2.1\n198.51.100.0/24\n")
	assertMigrationFileEquals(t, filepath.Join(baseDir, clientIPBlacklistListRelPath), "")
	assertMigrationFileAbsent(t, filepath.Join(baseDir, legacyClientIPListRelPath))
}

func TestMigrateLegacyClientIPListForBaseDirBlacklistMode(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteMigrationTestFile(t, filepath.Join(baseDir, legacyClientIPListRelPath), "203.0.113.10\n")
	mustWriteMigrationTestFile(t, filepath.Join(baseDir, customConfigDirname, switchesConfigFilename), "client_proxy_mode: blacklist\n")

	if err := migrateLegacyClientIPListForBaseDir(baseDir); err != nil {
		t.Fatalf("migrateLegacyClientIPListForBaseDir: %v", err)
	}

	assertMigrationFileEquals(t, filepath.Join(baseDir, clientIPWhitelistListRelPath), "")
	assertMigrationFileEquals(t, filepath.Join(baseDir, clientIPBlacklistListRelPath), "203.0.113.10\n")
	assertMigrationFileAbsent(t, filepath.Join(baseDir, legacyClientIPListRelPath))
}

func TestMigrateLegacyClientIPListForBaseDirDefaultAllWritesBoth(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteMigrationTestFile(t, filepath.Join(baseDir, legacyClientIPListRelPath), "203.0.113.11\n")

	if err := migrateLegacyClientIPListForBaseDir(baseDir); err != nil {
		t.Fatalf("migrateLegacyClientIPListForBaseDir: %v", err)
	}

	want := "203.0.113.11\n"
	assertMigrationFileEquals(t, filepath.Join(baseDir, clientIPWhitelistListRelPath), want)
	assertMigrationFileEquals(t, filepath.Join(baseDir, clientIPBlacklistListRelPath), want)
	assertMigrationFileAbsent(t, filepath.Join(baseDir, legacyClientIPListRelPath))
}

func TestMigrateLegacyClientIPListForBaseDirSkipsWhenNewFilesAlreadyPopulated(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteMigrationTestFile(t, filepath.Join(baseDir, legacyClientIPListRelPath), "203.0.113.11\n")
	mustWriteMigrationTestFile(t, filepath.Join(baseDir, clientIPWhitelistListRelPath), "198.51.100.1\n")

	if err := migrateLegacyClientIPListForBaseDir(baseDir); err != nil {
		t.Fatalf("migrateLegacyClientIPListForBaseDir: %v", err)
	}

	assertMigrationFileEquals(t, filepath.Join(baseDir, legacyClientIPListRelPath), "203.0.113.11\n")
	assertMigrationFileEquals(t, filepath.Join(baseDir, clientIPWhitelistListRelPath), "198.51.100.1\n")
	if _, err := os.Stat(filepath.Join(baseDir, clientIPBlacklistListRelPath)); !os.IsNotExist(err) {
		t.Fatalf("expected blacklist file to stay absent, got err=%v", err)
	}
}

func mustWriteMigrationTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func assertMigrationFileEquals(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(raw) != want {
		t.Fatalf("file %s = %q, want %q", path, string(raw), want)
	}
}

func assertMigrationFileAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be absent, got err=%v", path, err)
	}
}
