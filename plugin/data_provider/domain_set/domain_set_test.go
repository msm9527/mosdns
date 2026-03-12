package domain_set

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
)

func TestPollWatchedFilesReloadsMatcher(t *testing.T) {
	dir := t.TempDir()
	ruleFile := filepath.Join(dir, "rules.txt")
	if err := os.WriteFile(ruleFile, []byte("domain:old.example\n"), 0o644); err != nil {
		t.Fatalf("write initial rule file: %v", err)
	}

	ds := &DomainSet{
		curArgs:      &Args{Files: []string{ruleFile}},
		mixM:         domain.NewDomainMixMatcher(),
		subscribers:  make([]func(), 0, 1),
		fileStates:   make(map[string]watchedFileState),
	}
	initialRules, err := ds.initAndLoadRules(nil, []string{ruleFile})
	if err != nil {
		t.Fatalf("load initial rules: %v", err)
	}
	ds.rules = initialRules
	ds.updateWatchedFilesLocked([]string{ruleFile})

	reloaded := make(chan struct{}, 1)
	ds.Subscribe(func() {
		reloaded <- struct{}{}
	})

	if err := os.WriteFile(ruleFile, []byte("domain:new.example\n"), 0o644); err != nil {
		t.Fatalf("rewrite rule file: %v", err)
	}
	ts := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(ruleFile, ts, ts); err != nil {
		t.Fatalf("update modtime: %v", err)
	}

	if err := ds.pollWatchedFiles(); err != nil {
		t.Fatalf("poll watched files: %v", err)
	}

	select {
	case <-reloaded:
	case <-time.After(time.Second):
		t.Fatal("expected subscriber notification after file reload")
	}

	if _, ok := ds.Match("new.example."); !ok {
		t.Fatal("expected matcher to include reloaded rule")
	}
	if _, ok := ds.Match("old.example."); ok {
		t.Fatal("expected old rule to be replaced")
	}
}
