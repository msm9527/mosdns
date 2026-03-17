package domain_set_light

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

type mockRuleExporter struct {
	mu          sync.Mutex
	rules       []string
	subscribers []func()
}

func (m *mockRuleExporter) GetRules() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.rules...), nil
}

func (m *mockRuleExporter) Subscribe(cb func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subscribers = append(m.subscribers, cb)
}

func (m *mockRuleExporter) setRules(rules []string) {
	m.mu.Lock()
	m.rules = append([]string(nil), rules...)
	subscribers := append([]func(){}, m.subscribers...)
	m.mu.Unlock()
	for _, cb := range subscribers {
		go cb()
	}
}

func TestPollWatchedFilesReloadsRules(t *testing.T) {
	dir := t.TempDir()
	ruleFile := filepath.Join(dir, "ddnslist.txt")
	if err := os.WriteFile(ruleFile, []byte("domain:old.example\n"), 0o644); err != nil {
		t.Fatalf("write initial rule file: %v", err)
	}

	ds := &DomainSetLight{
		curArgs:     &Args{Files: []string{ruleFile}},
		subscribers: make([]func(), 0, 1),
		fileStates:  make(map[string]watchedFileState),
	}
	initialRules, _, err := (&DomainSetLight{}).initAndLoadRules(nil, []string{ruleFile}, "")
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

func TestLoadGeneratedRulesFromExporter(t *testing.T) {
	exporter := &mockRuleExporter{rules: []string{"full:runtime.example"}}
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"my_nov4list": exporter,
	})

	ds := &DomainSetLight{
		bp:      coremain.NewBP("my_nov4rule", m),
		curArgs: &Args{GeneratedFrom: "my_nov4list"},
	}

	rules, resolvedExporter, err := ds.initAndLoadRules(nil, nil, "my_nov4list")
	if err != nil {
		t.Fatalf("initAndLoadRules: %v", err)
	}
	if resolvedExporter != exporter {
		t.Fatal("expected generated exporter to be resolved directly")
	}
	if len(rules) != 1 || rules[0] != "full:runtime.example" {
		t.Fatalf("unexpected generated rules: %#v", rules)
	}
}

func TestGeneratedExporterSubscriptionReloadsRules(t *testing.T) {
	exporter := &mockRuleExporter{rules: []string{"full:old.example"}}
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"my_nov6list": exporter,
	})

	ds := &DomainSetLight{
		bp:                     coremain.NewBP("my_nov6rule", m),
		curArgs:                &Args{GeneratedFrom: "my_nov6list"},
		subscribers:            make([]func(), 0, 1),
		fileStates:             make(map[string]watchedFileState),
		generatedSubscriptions: make(map[string]struct{}),
	}
	initialRules, resolvedExporter, err := ds.initAndLoadRules(nil, nil, "my_nov6list")
	if err != nil {
		t.Fatalf("initAndLoadRules: %v", err)
	}
	ds.rules = initialRules
	ds.generatedExporter = resolvedExporter
	ds.subscribeGeneratedSource("my_nov6list", resolvedExporter)

	reloaded := make(chan struct{}, 1)
	ds.Subscribe(func() {
		reloaded <- struct{}{}
	})

	exporter.setRules([]string{"full:new.example"})

	select {
	case <-reloaded:
	case <-time.After(2 * time.Second):
		t.Fatal("expected exporter subscription reload")
	}

	rules, err := ds.GetRules()
	if err != nil {
		t.Fatalf("GetRules: %v", err)
	}
	if len(rules) != 1 || rules[0] != "full:new.example" {
		t.Fatalf("unexpected rules after exporter reload: %#v", rules)
	}
}

func TestReplaceListRuntimeRejectsGeneratedSource(t *testing.T) {
	ds := &DomainSetLight{
		pluginTag:     "my_nov6rule",
		generatedFrom: "my_nov6list",
	}

	if _, err := ds.ReplaceListRuntime(nil, []string{"full:example.com"}); err == nil {
		t.Fatal("expected ReplaceListRuntime to reject generated sources")
	}
}
