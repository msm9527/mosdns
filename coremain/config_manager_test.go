package coremain

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValidateConfigUpdateURL(t *testing.T) {
	tests := []struct {
		name    string
		rawURL  string
		wantErr bool
	}{
		{name: "zip http", rawURL: "http://example.com/config.zip", wantErr: false},
		{name: "zip https", rawURL: "https://example.com/config.zip", wantErr: false},
		{name: "github tree", rawURL: "https://github.com/msm9527/mosdns/tree/main/config", wantErr: false},
		{name: "plain page url", rawURL: "https://example.com/config", wantErr: true},
		{name: "unsupported scheme", rawURL: "ftp://example.com/config.zip", wantErr: true},
		{name: "missing host", rawURL: "https:///config.zip", wantErr: true},
		{name: "invalid url", rawURL: "://bad url", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfigUpdateURL(tc.rawURL)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tc.rawURL)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error for %q, got %v", tc.rawURL, err)
			}
		})
	}
}

func TestResolveConfigRemoteSource(t *testing.T) {
	t.Run("default source", func(t *testing.T) {
		spec, err := resolveConfigRemoteSource("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.DisplayURL != configRemoteSourceURL {
			t.Fatalf("unexpected display url: %q", spec.DisplayURL)
		}
		if spec.Subtree != "config" {
			t.Fatalf("unexpected subtree: %q", spec.Subtree)
		}
		if spec.DownloadURL != "https://codeload.github.com/msm9527/mosdns/zip/refs/heads/main" {
			t.Fatalf("unexpected download url: %q", spec.DownloadURL)
		}
	})

	t.Run("github tree source", func(t *testing.T) {
		spec, err := resolveConfigRemoteSource("https://github.com/foo/bar/tree/dev/config/rules")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.DownloadURL != "https://codeload.github.com/foo/bar/zip/refs/heads/dev" {
			t.Fatalf("unexpected download url: %q", spec.DownloadURL)
		}
		if spec.Subtree != "config/rules" {
			t.Fatalf("unexpected subtree: %q", spec.Subtree)
		}
	})

	t.Run("zip source", func(t *testing.T) {
		spec, err := resolveConfigRemoteSource("https://example.com/mosdns-config.zip")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if spec.DownloadURL != "https://example.com/mosdns-config.zip" {
			t.Fatalf("unexpected download url: %q", spec.DownloadURL)
		}
		if spec.Subtree != "" {
			t.Fatalf("unexpected subtree: %q", spec.Subtree)
		}
	})
}

func TestValidateConfigTargetDir(t *testing.T) {
	validDir := t.TempDir()
	filePath := filepath.Join(validDir, "x.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0644); err != nil {
		t.Fatalf("prepare file failed: %v", err)
	}

	tests := []struct {
		name    string
		rawDir  string
		wantErr bool
	}{
		{name: "valid directory", rawDir: validDir, wantErr: false},
		{name: "valid with spaces", rawDir: "  " + validDir + "  ", wantErr: false},
		{name: "file path", rawDir: filePath, wantErr: true},
		{name: "not exists", rawDir: filepath.Join(validDir, "not-exists"), wantErr: true},
		{name: "empty", rawDir: "   ", wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateConfigTargetDir(tc.rawDir)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tc.rawDir)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error for %q, got %v", tc.rawDir, err)
			}
		})
	}
}

func TestResolveConfigTargetDir(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	got, err := resolveConfigTargetDir("")
	if err != nil {
		t.Fatalf("resolve current config dir failed: %v", err)
	}
	if got != MainConfigBaseDir {
		t.Fatalf("unexpected dir: got %q, want %q", got, MainConfigBaseDir)
	}
}

func TestPerformLocalBackup_PreservesFileMode(t *testing.T) {
	source := t.TempDir()
	dest := filepath.Join(t.TempDir(), "backup")

	srcFile := filepath.Join(source, "run.sh")
	if err := os.WriteFile(srcFile, []byte("#!/bin/sh\necho ok\n"), 0755); err != nil {
		t.Fatalf("write source file failed: %v", err)
	}

	if err := performLocalBackup(source, dest); err != nil {
		t.Fatalf("perform backup failed: %v", err)
	}

	info, err := os.Stat(filepath.Join(dest, "run.sh"))
	if err != nil {
		t.Fatalf("stat backup file failed: %v", err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatalf("unexpected mode: got %o, want %o", info.Mode().Perm(), os.FileMode(0755))
	}
}

func TestHandleConfigExport_SkipsOnlyRootBackupDir(t *testing.T) {
	root := t.TempDir()

	writeFileForTest(t, filepath.Join(root, "normal.txt"), "n")
	writeFileForTest(t, filepath.Join(root, configBackupDirName, "root.txt"), "root-backup")
	writeFileForTest(t, filepath.Join(root, "sub", configBackupDirName, "nested.txt"), "nested-backup")

	reqBody := `{"dir":"` + root + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/config/export", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleConfigExport(w, req)

	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("unexpected content-type: %q", ct)
	}
	if w.Body.Len() == 0 {
		t.Fatal("expected zip body, got empty")
	}

	zr, err := zip.NewReader(bytes.NewReader(w.Body.Bytes()), int64(w.Body.Len()))
	if err != nil {
		t.Fatalf("invalid zip output: %v", err)
	}

	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}

	if slices.Contains(names, filepath.Join(configBackupDirName, "root.txt")) {
		t.Fatalf("root backup file should be skipped, names=%v", names)
	}
	if !slices.Contains(names, filepath.Join("sub", configBackupDirName, "nested.txt")) {
		t.Fatalf("nested backup file should be kept, names=%v", names)
	}
	if !slices.Contains(names, "normal.txt") {
		t.Fatalf("normal file missing, names=%v", names)
	}
}

func TestHandleConfigInfo(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/info", nil)
	w := httptest.NewRecorder()

	handleConfigInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: got %d, want %d", w.Code, http.StatusOK)
	}

	var resp ConfigManagerInfo
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if resp.Dir != MainConfigBaseDir {
		t.Fatalf("unexpected dir: got %q, want %q", resp.Dir, MainConfigBaseDir)
	}
	if resp.RemoteSource != configRemoteSourceURL {
		t.Fatalf("unexpected remote source: got %q", resp.RemoteSource)
	}
	if !strings.Contains(resp.Warning, "覆盖所有配置") {
		t.Fatalf("unexpected warning: %q", resp.Warning)
	}
}

func TestDoDownloadWithLimit_RejectsOversizeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("123456"))
	}))
	defer srv.Close()

	_, err := doDownloadWithLimit(srv.URL, "", 4)
	if err == nil {
		t.Fatal("expected oversize download error, got nil")
	}
	if !strings.Contains(err.Error(), "download too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestExtractAndOverwriteWithLimits(t *testing.T) {
	t.Run("entry too large", func(t *testing.T) {
		zipData := mustBuildZip(t, map[string]string{"a.txt": "12345"})
		_, err := extractAndOverwriteWithLimits(zipData, t.TempDir(), 10, 4, 100)
		if err == nil {
			t.Fatal("expected entry size limit error, got nil")
		}
		if !strings.Contains(err.Error(), "zip entry too large") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("zip slip path", func(t *testing.T) {
		zipData := mustBuildZip(t, map[string]string{"../evil.txt": "x"})
		_, err := extractAndOverwriteWithLimits(zipData, t.TempDir(), 10, 10, 100)
		if err == nil {
			t.Fatal("expected zip path error, got nil")
		}
		if !strings.Contains(err.Error(), "invalid zip entry path") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		target := t.TempDir()
		zipData := mustBuildZip(t, map[string]string{
			"sub/a.txt": "abc",
			"b.txt":     "xyz",
		})

		n, err := extractAndOverwriteWithLimits(zipData, target, 10, 10, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 2 {
			t.Fatalf("unexpected extracted count: got %d, want 2", n)
		}

		b, err := os.ReadFile(filepath.Join(target, "sub", "a.txt"))
		if err != nil {
			t.Fatalf("read extracted file failed: %v", err)
		}
		if string(b) != "abc" {
			t.Fatalf("unexpected content: %q", string(b))
		}
	})

	t.Run("relative target dir dot", func(t *testing.T) {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatalf("getwd failed: %v", err)
		}
		tmp := t.TempDir()
		if err := os.Chdir(tmp); err != nil {
			t.Fatalf("chdir temp dir failed: %v", err)
		}
		t.Cleanup(func() {
			_ = os.Chdir(wd)
		})

		zipData := mustBuildZip(t, map[string]string{
			"ok.txt": "ok",
		})

		n, err := extractAndOverwriteWithLimits(zipData, ".", 10, 10, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 1 {
			t.Fatalf("unexpected extracted count: got %d, want 1", n)
		}
		if _, err := os.Stat(filepath.Join(tmp, "ok.txt")); err != nil {
			t.Fatalf("expected extracted file missing: %v", err)
		}
	})
}

func TestExtractZipSubtreeWithLimits(t *testing.T) {
	t.Run("extract config subtree from github archive", func(t *testing.T) {
		target := t.TempDir()
		zipData := mustBuildZip(t, map[string]string{
			"mosdns-main/config/config.yaml":      "a: 1",
			"mosdns-main/config/sub/rules.txt":    "rule",
			"mosdns-main/docs/readme.md":          "ignored",
			"mosdns-main/config/backup/keep.json": "backup-data",
		})

		n, err := extractZipSubtreeWithLimits(zipData, target, "config", 10, 1024, 4096)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 3 {
			t.Fatalf("unexpected extracted count: got %d, want 3", n)
		}
		if _, err := os.Stat(filepath.Join(target, "config.yaml")); err != nil {
			t.Fatalf("expected config.yaml extracted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(target, "sub", "rules.txt")); err != nil {
			t.Fatalf("expected nested rule extracted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(target, "docs", "readme.md")); !os.IsNotExist(err) {
			t.Fatalf("unexpected docs file extracted, stat err=%v", err)
		}
	})

	t.Run("missing subtree", func(t *testing.T) {
		zipData := mustBuildZip(t, map[string]string{
			"mosdns-main/docs/readme.md": "ignored",
		})
		_, err := extractZipSubtreeWithLimits(zipData, t.TempDir(), "config", 10, 1024, 4096)
		if err == nil {
			t.Fatal("expected missing subtree error, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestExtractRemoteConfigWithRollback_ZipSourceStripsSingleRootDir(t *testing.T) {
	targetDir := t.TempDir()
	backupDir := filepath.Join(targetDir, configBackupDirName)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatalf("mkdir backup failed: %v", err)
	}

	zipData := mustBuildZip(t, map[string]string{
		"mosdns-config/config.yaml": "a: 1",
		"mosdns-config/rules.txt":   "rule",
	})

	n, err := extractRemoteConfigWithRollback(zipData, targetDir, backupDir, configRemoteSourceSpec{
		DisplayURL:  "https://example.com/mosdns-config.zip",
		DownloadURL: "https://example.com/mosdns-config.zip",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Fatalf("unexpected applied count: got %d, want 2", n)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "config.yaml")); err != nil {
		t.Fatalf("expected config file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "rules.txt")); err != nil {
		t.Fatalf("expected rules file missing: %v", err)
	}
}

func TestRollbackAppliedFiles(t *testing.T) {
	targetDir := t.TempDir()
	backupDir := filepath.Join(targetDir, configBackupDirName)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatalf("mkdir backup failed: %v", err)
	}

	writeFileForTest(t, filepath.Join(targetDir, "a.txt"), "new-a")
	writeFileForTest(t, filepath.Join(targetDir, "new.txt"), "new-file")
	writeFileForTest(t, filepath.Join(backupDir, "a.txt"), "old-a")

	if err := rollbackAppliedFiles([]string{"a.txt", "new.txt"}, backupDir, targetDir); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(targetDir, "a.txt"))
	if err != nil {
		t.Fatalf("read restored file failed: %v", err)
	}
	if string(b) != "old-a" {
		t.Fatalf("unexpected restored content: %q", string(b))
	}
	if _, err := os.Stat(filepath.Join(targetDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected new.txt removed, stat err=%v", err)
	}
}

func TestApplyStagedFilesWithRollback_RollbackOnFailure(t *testing.T) {
	targetDir := t.TempDir()
	backupDir := filepath.Join(targetDir, configBackupDirName)
	stageDir := t.TempDir()

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatalf("mkdir backup failed: %v", err)
	}

	writeFileForTest(t, filepath.Join(targetDir, "a.txt"), "old-a")
	writeFileForTest(t, filepath.Join(backupDir, "a.txt"), "old-a")
	writeFileForTest(t, filepath.Join(stageDir, "a.txt"), "new-a")

	// Make destination parent path invalid for second file: "blocked" is a file.
	writeFileForTest(t, filepath.Join(targetDir, "blocked"), "not-dir")
	writeFileForTest(t, filepath.Join(stageDir, "blocked", "b.txt"), "new-b")

	_, err := applyStagedFilesWithRollback(stageDir, targetDir, backupDir)
	if err == nil {
		t.Fatal("expected apply failure, got nil")
	}
	if !strings.Contains(err.Error(), "rollback completed") {
		t.Fatalf("expected rollback completion in error, got: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(targetDir, "a.txt"))
	if err != nil {
		t.Fatalf("read rolled back file failed: %v", err)
	}
	if string(b) != "old-a" {
		t.Fatalf("rollback did not restore a.txt, got %q", string(b))
	}
}

func TestApplyStagedFilesWithRollback_RejectsBackupPath(t *testing.T) {
	targetDir := t.TempDir()
	backupDir := filepath.Join(targetDir, configBackupDirName)
	stageDir := t.TempDir()

	writeFileForTest(t, filepath.Join(stageDir, configBackupDirName, "evil.txt"), "x")

	_, err := applyStagedFilesWithRollback(stageDir, targetDir, backupDir)
	if err == nil {
		t.Fatal("expected reserved path error, got nil")
	}
	if !strings.Contains(err.Error(), "reserved path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildRestartRequest(t *testing.T) {
	t.Run("localhost host override", func(t *testing.T) {
		req, err := buildRestartRequest(context.Background(), "http://localhost:9099/api/v1/system/restart")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.Host != "localhost:9099" {
			t.Fatalf("unexpected req.Host: %q", req.Host)
		}
	})

	t.Run("localhost without port", func(t *testing.T) {
		req, err := buildRestartRequest(context.Background(), "http://127.0.0.1/api/v1/system/restart")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.Host != "127.0.0.1" {
			t.Fatalf("unexpected req.Host: %q", req.Host)
		}
	})

	t.Run("non localhost no override", func(t *testing.T) {
		req, err := buildRestartRequest(context.Background(), "http://example.com/restart")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if req.Host != "example.com" {
			t.Fatalf("unexpected req.Host: %q", req.Host)
		}
	})

	t.Run("invalid endpoint", func(t *testing.T) {
		_, err := buildRestartRequest(context.Background(), "://bad")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestTriggerRestartUsesRuntimeScheduler(t *testing.T) {
	var gotDelay int
	cleanup := registerInternalRestartScheduler(func(delayMs int) error {
		gotDelay = delayMs
		return nil
	})
	defer cleanup()

	if err := triggerRestart(context.Background(), 500); err != nil {
		t.Fatalf("triggerRestart() error = %v", err)
	}
	if gotDelay != 500 {
		t.Fatalf("triggerRestart() delay = %d, want %d", gotDelay, 500)
	}
}

func mustBuildZip(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry failed: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry failed: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer failed: %v", err)
	}
	return buf.Bytes()
}

func writeFileForTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir failed for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file failed for %s: %v", path, err)
	}
}
