package domain_set

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
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

func TestPollWatchedFilesReloadsMatcher(t *testing.T) {
	dir := t.TempDir()
	ruleFile := filepath.Join(dir, "rules.txt")
	if err := os.WriteFile(ruleFile, []byte("domain:old.example\n"), 0o644); err != nil {
		t.Fatalf("write initial rule file: %v", err)
	}

	ds := &DomainSet{
		curArgs:     &Args{Files: []string{ruleFile}},
		mixM:        domain.NewDomainMixMatcher(),
		subscribers: make([]func(), 0, 1),
		fileStates:  make(map[string]watchedFileState),
	}
	initialRules, _, err := ds.initAndLoadRules(nil, []string{ruleFile}, "")
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

func TestLoadGeneratedRulesFromExporter(t *testing.T) {
	exporter := &mockRuleExporter{rules: []string{"full:runtime.example"}}
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"my_realiplist": exporter,
	})

	ds := &DomainSet{
		bp:         coremain.NewBP("my_realiprule", m),
		curArgs:    &Args{GeneratedFrom: "my_realiplist"},
		mixM:       domain.NewDomainMixMatcher(),
		fileStates: make(map[string]watchedFileState),
	}

	rules, resolvedExporter, err := ds.initAndLoadRules(nil, nil, "my_realiplist")
	if err != nil {
		t.Fatalf("initAndLoadRules: %v", err)
	}
	if resolvedExporter != exporter {
		t.Fatal("expected generated exporter to be resolved directly")
	}
	if len(rules) != 1 || rules[0] != "full:runtime.example" {
		t.Fatalf("unexpected generated rules: %#v", rules)
	}
	if _, ok := ds.Match("runtime.example."); !ok {
		t.Fatal("expected matcher to load rules from exporter")
	}
}

func TestGeneratedExporterSubscriptionReloadsMatcher(t *testing.T) {
	exporter := &mockRuleExporter{rules: []string{"full:old.example"}}
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"my_realiplist": exporter,
	})

	ds := &DomainSet{
		bp:                     coremain.NewBP("my_realiprule", m),
		curArgs:                &Args{GeneratedFrom: "my_realiplist"},
		mixM:                   domain.NewDomainMixMatcher(),
		subscribers:            make([]func(), 0, 1),
		fileStates:             make(map[string]watchedFileState),
		generatedSubscriptions: make(map[string]struct{}),
	}
	initialRules, resolvedExporter, err := ds.initAndLoadRules(nil, nil, "my_realiplist")
	if err != nil {
		t.Fatalf("initAndLoadRules: %v", err)
	}
	ds.rules = initialRules
	ds.generatedExporter = resolvedExporter
	ds.subscribeGeneratedSource("my_realiplist", resolvedExporter)

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

	if _, ok := ds.Match("new.example."); !ok {
		t.Fatal("expected matcher to include reloaded exporter rules")
	}
	if _, ok := ds.Match("old.example."); ok {
		t.Fatal("expected old exporter rules to be replaced")
	}
}

func TestReplaceListRuntimeRejectsGeneratedSource(t *testing.T) {
	ds := &DomainSet{
		pluginTag:     "my_fakeiprule",
		generatedFrom: "my_fakeiplist",
		mixM:          domain.NewDomainMixMatcher(),
	}

	if _, err := ds.ReplaceListRuntime(nil, []string{"full:example.com"}); err == nil {
		t.Fatal("expected ReplaceListRuntime to reject generated sources")
	}
}
