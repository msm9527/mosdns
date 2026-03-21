package coremain

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

func TestSyncRuleSource_RemoteFailureFallsBackToCachedFile(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, "runtime.db")
	source := rulesource.Source{
		ID:                  "cached",
		Name:                "cached",
		Behavior:            rulesource.BehaviorAdguard,
		MatchMode:           rulesource.MatchModeAdguardNative,
		Format:              rulesource.FormatRules,
		SourceKind:          rulesource.SourceKindRemote,
		Path:                "adguard/cached.rules",
		URL:                 "https://example.invalid/rules.txt",
		AutoUpdate:          true,
		UpdateIntervalHours: 24,
	}
	localPath := filepath.Join(baseDir, "adguard", "cached.rules")
	mustWriteRuleSyncTestFile(t, localPath, "||cached.example^\n")
	lastUpdated := time.Date(2026, 3, 20, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if err := SaveRuleSourceStatus(dbPath, RuleSourceStatus{
		Scope:       string(rulesource.ScopeAdguard),
		SourceID:    source.ID,
		RuleCount:   1,
		LastUpdated: lastUpdated,
	}); err != nil {
		t.Fatalf("SaveRuleSourceStatus: %v", err)
	}

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.EOF
	})}
	result, err := SyncRuleSource(
		context.Background(),
		client,
		dbPath,
		baseDir,
		rulesource.ScopeAdguard,
		source,
		RuleSourceSyncOptions{ForceRemote: true},
	)
	if err != nil {
		t.Fatalf("SyncRuleSource: %v", err)
	}
	if text := string(result.Data); !strings.Contains(text, "cached.example") {
		t.Fatalf("unexpected fallback data: %q", text)
	}
	statuses, err := ListRuleSourceStatusByScope(dbPath, rulesource.ScopeAdguard)
	if err != nil {
		t.Fatalf("ListRuleSourceStatusByScope: %v", err)
	}
	status := statuses[source.ID]
	if status.RuleCount != 1 {
		t.Fatalf("unexpected rule count: %+v", status)
	}
	if !status.LastUpdated.Equal(lastUpdated) {
		t.Fatalf("unexpected last_updated: %+v", status)
	}
	if !strings.Contains(status.LastError, "EOF") {
		t.Fatalf("expected EOF last_error, got %+v", status)
	}
}

func TestSyncRuleSource_RemoteFailureWithoutCacheReturnsError(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, "runtime.db")
	source := rulesource.Source{
		ID:         "missing",
		Name:       "missing",
		Behavior:   rulesource.BehaviorAdguard,
		MatchMode:  rulesource.MatchModeAdguardNative,
		Format:     rulesource.FormatRules,
		SourceKind: rulesource.SourceKindRemote,
		Path:       "adguard/missing.rules",
		URL:        "https://example.invalid/rules.txt",
	}

	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom")
	})}
	_, err := SyncRuleSource(
		context.Background(),
		client,
		dbPath,
		baseDir,
		rulesource.ScopeAdguard,
		source,
		RuleSourceSyncOptions{ForceRemote: true},
	)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", err)
	}
	if !strings.Contains(err.Error(), "fallback cache") || !strings.Contains(err.Error(), "adguard/missing.rules") {
		t.Fatalf("expected fallback cache detail, got %v", err)
	}
}

func TestSyncRuleSource_PreferCacheSkipsRemoteOnStartup(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, "runtime.db")
	source := rulesource.Source{
		ID:                  "cached",
		Name:                "cached",
		Behavior:            rulesource.BehaviorAdguard,
		MatchMode:           rulesource.MatchModeAdguardNative,
		Format:              rulesource.FormatRules,
		SourceKind:          rulesource.SourceKindRemote,
		Path:                "adguard/cached.rules",
		URL:                 "https://example.invalid/rules.txt",
		AutoUpdate:          true,
		UpdateIntervalHours: 24,
	}
	mustWriteRuleSyncTestFile(t, filepath.Join(baseDir, "adguard", "cached.rules"), "||cached.example^\n")
	called := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("should not be called")
	})}
	result, err := SyncRuleSource(
		context.Background(),
		client,
		dbPath,
		baseDir,
		rulesource.ScopeAdguard,
		source,
		RuleSourceSyncOptions{PreferCache: true},
	)
	if err != nil {
		t.Fatalf("SyncRuleSource: %v", err)
	}
	if called {
		t.Fatal("expected startup prefer-cache path to skip remote download")
	}
	if text := string(result.Data); !strings.Contains(text, "cached.example") {
		t.Fatalf("unexpected cached data: %q", text)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func mustWriteRuleSyncTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
