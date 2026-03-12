package domain_set_light

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPollWatchedFilesReloadsRules(t *testing.T) {
	dir := t.TempDir()
	ruleFile := filepath.Join(dir, "ddnslist.txt")
	if err := os.WriteFile(ruleFile, []byte("domain:old.example\n"), 0o644); err != nil {
		t.Fatalf("write initial rule file: %v", err)
	}

	ds := &DomainSetLight{
		curArgs:      &Args{Files: []string{ruleFile}},
		subscribers:  make([]func(), 0, 1),
		fileStates:   make(map[string]watchedFileState),
	}
	initialRules, err := (&DomainSetLight{}).initAndLoadRules(nil, []string{ruleFile})
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

	rules, err := ds.GetRules()
	if err != nil {
		t.Fatalf("get rules: %v", err)
	}
	if len(rules) != 1 || rules[0] != "domain:new.example" {
		t.Fatalf("unexpected rules after reload: %#v", rules)
	}
}
