package domain_output

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
)

type mockListController struct {
	mu     sync.Mutex
	values []string
}

func (m *mockListController) ListEntries(_ string, _, _ int) ([]coremain.ListEntry, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]coremain.ListEntry, 0, len(m.values))
	for _, value := range m.values {
		items = append(items, coremain.ListEntry{Value: value})
	}
	return items, len(items), nil
}

func (m *mockListController) ReplaceListRuntime(_ context.Context, values []string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values = append([]string(nil), values...)
	return len(values), nil
}

func (m *mockListController) snapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.values...)
}

type mockRefreshEnqueuer struct {
	mu   sync.Mutex
	jobs []coremain.DomainRefreshJob
}

func (m *mockRefreshEnqueuer) EnqueueDomainRefresh(_ context.Context, job coremain.DomainRefreshJob) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobs = append(m.jobs, job)
	return true
}

func (m *mockRefreshEnqueuer) snapshot() []coremain.DomainRefreshJob {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]coremain.DomainRefreshJob(nil), m.jobs...)
}

func withRuntimeBaseDir(t *testing.T) string {
	t.Helper()
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})
	return coremain.MainConfigBaseDir
}

func mustLoadDatasetContent(t *testing.T, key string) string {
	t.Helper()
	dataset, ok, err := coremain.LoadGeneratedDatasetFromPath(coremain.RuntimeStateDBPath(), key)
	if err != nil {
		t.Fatalf("LoadGeneratedDatasetFromPath(%q): %v", key, err)
	}
	if !ok {
		t.Fatalf("expected generated dataset for %q", key)
	}
	return dataset.Content
}

func newTestDomainOutput(t *testing.T, pluginTag string, plugins map[string]any, args *Args) *domainOutput {
	t.Helper()
	manager := coremain.NewTestMosdnsWithPlugins(plugins)
	return newDomainOutput(pluginTag, manager, nil, args)
}

func TestDomainOutputPromoteAfterThreshold(t *testing.T) {
	withRuntimeBaseDir(t)

	list := &mockListController{}
	d := newTestDomainOutput(t, "my_realiplist", map[string]any{
		"my_realiprule": list,
	}, &Args{
		PublishTo: "my_realiprule",
		Policy: &PolicyArgs{
			Kind:         "realip",
			PromoteAfter: 2,
			TrackQType:   true,
			PublishMode:  "promoted_only",
			DecayDays:    30,
		},
	})

	d.processRecord(&logItem{name: "example.com.", qtype: 1, source: "live"})
	if err := d.performWrite(WriteModeSave); err != nil {
		t.Fatalf("performWrite first: %v", err)
	}
	if got := strings.TrimSpace(mustLoadDatasetContent(t, coremain.DomainOutputRuleDatasetKey("my_realiplist"))); got != "" {
		t.Fatalf("expected empty rule dataset after first observation, got %q", got)
	}

	d.processRecord(&logItem{name: "example.com.", qtype: 1, source: "live"})
	if err := d.performWrite(WriteModeSave); err != nil {
		t.Fatalf("performWrite second: %v", err)
	}

	raw := mustLoadDatasetContent(t, coremain.DomainOutputRuleDatasetKey("my_realiplist"))
	if !strings.Contains(raw, "full:example.com") {
		t.Fatalf("expected promoted rule after threshold, got %q", raw)
	}
	if got := list.snapshot(); len(got) != 1 || got[0] != "full:example.com" {
		t.Fatalf("unexpected published values: %#v", got)
	}
}

func TestDomainOutputNov4RequiresAQueries(t *testing.T) {
	withRuntimeBaseDir(t)

	d := newTestDomainOutput(t, "my_nov4list", map[string]any{
		"my_nov4rule": &mockListController{},
	}, &Args{
		PublishTo: "my_nov4rule",
		Policy: &PolicyArgs{
			Kind:         "nov4",
			PromoteAfter: 2,
			TrackQType:   true,
			PublishMode:  "promoted_only",
			DecayDays:    30,
		},
	})

	d.processRecord(&logItem{name: "ipv6-only.example.", qtype: 28, source: "live"})
	d.processRecord(&logItem{name: "ipv6-only.example.", qtype: 28, source: "live"})
	d.processRecord(&logItem{name: "ipv4-miss.example.", qtype: 1, source: "live"})
	d.processRecord(&logItem{name: "ipv4-miss.example.", qtype: 1, source: "live"})
	if err := d.performWrite(WriteModeSave); err != nil {
		t.Fatalf("performWrite: %v", err)
	}

	output := mustLoadDatasetContent(t, coremain.DomainOutputRuleDatasetKey("my_nov4list"))
	if strings.Contains(output, "full:ipv6-only.example") {
		t.Fatalf("unexpected AAAA-only promotion in nov4 rules: %q", output)
	}
	if !strings.Contains(output, "full:ipv4-miss.example") {
		t.Fatalf("expected A-only promotion in nov4 rules: %q", output)
	}
}

func TestDomainOutputLoadFromRuntimeDataset(t *testing.T) {
	withRuntimeBaseDir(t)

	key := coremain.DomainOutputStatDatasetKey("my_realiplist")
	body := "0000000001 2026-03-01 runtime.example qmask=1 score=1 promoted=1\n"
	if err := coremain.SaveGeneratedDatasetToPath(
		coremain.RuntimeStateDBPath(),
		key,
		coremain.GeneratedDatasetFormatDomainOutputStat,
		body,
	); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	d := newDomainOutput("my_realiplist", nil, nil, &Args{PublishTo: "my_realiprule"})
	if err := d.loadFromDataset(); err != nil {
		t.Fatalf("loadFromDataset: %v", err)
	}

	d.mu.Lock()
	entry := d.stats["runtime.example"]
	d.mu.Unlock()
	if entry == nil || entry.Count != 1 {
		t.Fatalf("expected runtime dataset entry count 1, got %#v", entry)
	}
}

func TestDomainOutputMaxEntriesHardCap(t *testing.T) {
	t.Parallel()

	d := newDomainOutput("top_domains", nil, nil, &Args{MaxEntries: 1})
	d.processRecord(&logItem{name: "first.example.", source: "live"})
	d.processRecord(&logItem{name: "second.example.", source: "live"})

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.stats) != 1 {
		t.Fatalf("expected hard cap to keep 1 entry, got %d", len(d.stats))
	}
	if _, ok := d.stats["first.example"]; !ok {
		t.Fatal("expected first entry to remain after cap")
	}
	if _, ok := d.stats["second.example"]; ok {
		t.Fatal("unexpected second entry when cap reached")
	}
	if got := atomic.LoadInt64(&d.droppedByCapCount); got != 1 {
		t.Fatalf("droppedByCapCount = %d, want 1", got)
	}
}

func TestDomainOutputNotifyDirtyAndVerify(t *testing.T) {
	withRuntimeBaseDir(t)

	list := &mockListController{}
	requery := &mockRefreshEnqueuer{}
	d := newTestDomainOutput(t, "my_realiplist", map[string]any{
		"my_realiprule": list,
		"requery":       requery,
	}, &Args{
		PublishTo: "my_realiprule",
		Policy: &PolicyArgs{
			Kind:                   "realip",
			PromoteAfter:           1,
			TrackQType:             true,
			PublishMode:            "promoted_only",
			DecayDays:              30,
			StaleAfterMinutes:      1,
			RefreshCooldownMinutes: 1,
			RequeryTag:             "requery",
		},
	})

	d.processRecord(&logItem{name: "example.com.", qtype: 1, source: "live"})
	time.Sleep(50 * time.Millisecond)

	jobs := requery.snapshot()
	if len(jobs) != 1 {
		t.Fatalf("expected one dirty refresh job, got %d", len(jobs))
	}
	if jobs[0].Domain != "example.com" || jobs[0].VerifyTag != "my_realiplist" {
		t.Fatalf("unexpected refresh job: %#v", jobs[0])
	}

	if _, err := d.MarkDomainVerified(context.Background(), "example.com", ""); err != nil {
		t.Fatalf("MarkDomainVerified: %v", err)
	}

	d.mu.Lock()
	entry := d.stats["example.com"]
	d.mu.Unlock()
	if entry == nil || entry.RefreshState != "clean" || entry.LastVerifiedAt == "" {
		t.Fatalf("expected clean verified entry, got %#v", entry)
	}
}

func TestDomainOutputPeriodicSkipWhenNotDirty(t *testing.T) {
	withRuntimeBaseDir(t)

	d := newDomainOutput("top_domains", nil, nil, &Args{
		EnableFlags: false,
		Policy: &PolicyArgs{
			Kind:        "generic",
			PublishMode: "all",
			DecayDays:   30,
		},
	})

	d.processRecord(&logItem{name: "skip.example.", qtype: 1, source: "live"})
	if err := d.performWrite(WriteModePeriodic); err != nil {
		t.Fatalf("performWrite first: %v", err)
	}

	key := coremain.DomainOutputStatDatasetKey("top_domains")
	first := mustLoadDatasetContent(t, key)
	time.Sleep(1100 * time.Millisecond)
	if err := d.performWrite(WriteModePeriodic); err != nil {
		t.Fatalf("performWrite second: %v", err)
	}
	second := mustLoadDatasetContent(t, key)
	if second != first {
		t.Fatalf("expected periodic clean write to be skipped, content changed: %q -> %q", first, second)
	}
}

func TestNormalizePolicyDefaults(t *testing.T) {
	t.Parallel()

	p := normalizePolicy("my_realiplist", &Args{PublishTo: "my_realiprule"})
	if p.kind != "realip" {
		t.Fatalf("kind = %q, want realip", p.kind)
	}
	if p.promoteAfter != 2 {
		t.Fatalf("promoteAfter = %d, want 2", p.promoteAfter)
	}
	if p.decayDays != 21 {
		t.Fatalf("decayDays = %d, want 21", p.decayDays)
	}
	if p.requeryTag != "requery" {
		t.Fatalf("requeryTag = %q, want requery", p.requeryTag)
	}
}

func TestNormalizePolicyNodeNov4(t *testing.T) {
	t.Parallel()

	p := normalizePolicy("my_nodenov4list", &Args{PublishTo: "my_nodenov4rule"})
	if p.kind != "nov4" {
		t.Fatalf("kind = %q, want nov4", p.kind)
	}
	if p.decayDays != 14 {
		t.Fatalf("decayDays = %d, want 14", p.decayDays)
	}
}

func TestNormalizePolicyWithoutPublishTarget(t *testing.T) {
	t.Parallel()

	p := normalizePolicy("my_nov4list", &Args{})
	if p.kind != "nov4" {
		t.Fatalf("kind = %q, want nov4", p.kind)
	}
	if p.requeryTag != "" {
		t.Fatalf("requeryTag = %q, want empty", p.requeryTag)
	}
}

func TestDomainOutputGeneratedRulesRemainListCompatible(t *testing.T) {
	withRuntimeBaseDir(t)

	list := &mockListController{}
	d := newTestDomainOutput(t, "my_fakeiplist", map[string]any{
		"my_fakeiprule": list,
	}, &Args{
		PublishTo: "my_fakeiprule",
		Policy: &PolicyArgs{
			Kind:         "fakeip",
			PromoteAfter: 1,
			TrackQType:   true,
			PublishMode:  "promoted_only",
			DecayDays:    30,
		},
	})

	d.processRecord(&logItem{name: "runtime.example.", qtype: 1, source: "live"})
	if err := d.performWrite(WriteModeSave); err != nil {
		t.Fatalf("performWrite: %v", err)
	}

	m := domain.NewDomainMixMatcher()
	for _, rule := range list.snapshot() {
		if err := m.Add(rule, struct{}{}); err != nil {
			t.Fatalf("matcher add %q: %v", rule, err)
		}
	}
	if _, ok := m.Match("runtime.example."); !ok {
		t.Fatal("expected published list values to remain domain_set compatible")
	}
}
