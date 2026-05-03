package coremain

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewServerAPIReadyDoesNotWaitForAuditStorageOpen(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	oldConfigPath := MainConfigFilePath
	oldRuntimeDBPath := runtimeStateDBPathOverride
	upstreamOverridesLock.RLock()
	oldUpstreamOverridesDir := upstreamOverridesDir
	upstreamOverridesLock.RUnlock()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
		MainConfigFilePath = oldConfigPath
		runtimeStateDBPathOverride = oldRuntimeDBPath
		setUpstreamOverridesBaseDir(oldUpstreamOverridesDir)
	})

	dir := t.TempDir()
	httpAddr := reserveLoopbackAddr(t)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`
version: v2
log:
  file: logs/mosdns.log
api:
  http: %s
audit:
  enabled: true
  sqlite_path: audit_logs/audit.db
`, httpAddr)), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	origOpenAuditStorage := openAuditStorage
	block := make(chan struct{})
	storageOpenDone := make(chan struct{})
	openAuditStorage = func(settings AuditSettings, configBaseDir string) (*SQLiteAuditStorage, error) {
		defer close(storageOpenDone)
		<-block
		return nil, fmt.Errorf("blocked audit storage open")
	}
	t.Cleanup(func() {
		openAuditStorage = origOpenAuditStorage
		MainConfigBaseDir = oldBaseDir
		MainConfigFilePath = oldConfigPath
		runtimeStateDBPathOverride = oldRuntimeDBPath
		setUpstreamOverridesBaseDir(oldUpstreamOverridesDir)
	})

	done := make(chan struct {
		m   *Mosdns
		err error
	}, 1)
	started := time.Now()
	go func() {
		m, err := NewServer(&serverFlags{c: path})
		done <- struct {
			m   *Mosdns
			err error
		}{m: m, err: err}
	}()

	var m *Mosdns
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("NewServer() error = %v", result.err)
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("NewServer() waited for audit storage: elapsed=%v", elapsed)
		}
		m = result.m
	case <-time.After(500 * time.Millisecond):
		close(block)
		t.Fatal("NewServer() blocked on audit storage open")
	}

	if err := waitForHTTPReady("http://"+httpAddr+"/metrics", 3*time.Second); err != nil {
		m.GetSafeClose().SendCloseSignal(nil)
		_ = m.GetSafeClose().WaitClosed()
		close(block)
		t.Fatalf("HTTP API was not ready within 3s while audit storage was blocked: %v", err)
	}

	m.GetSafeClose().SendCloseSignal(nil)
	_ = m.GetSafeClose().WaitClosed()

	close(block)
	select {
	case <-storageOpenDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("audit storage open goroutine did not exit")
	}
}

func reserveLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return addr
}

func waitForHTTPReady(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{
		Timeout: 200 * time.Millisecond,
		Transport: &http.Transport{
			Proxy: nil,
		},
	}
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("unexpected status %s", resp.Status)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	return lastErr
}
