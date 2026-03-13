package domain_output

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDomainOutputPromoteAfterThreshold(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	d := newDomainOutput(&Args{
		FileStat: filepath.Join(dir, "realip.txt"),
		FileRule: filepath.Join(dir, "realip.rule"),
		Policy: &PolicyArgs{
			Kind:         "realip",
			PromoteAfter: 2,
			TrackQType:   true,
			PublishMode:  "promoted_only",
			DecayDays:    30,
		},
	})

	d.processRecord(&logItem{name: "example.com.", qtype: 1, source: "live"})
	d.performWrite(WriteModeSave)

	raw, err := os.ReadFile(filepath.Join(dir, "realip.rule"))
	if err != nil {
		t.Fatalf("read rule file: %v", err)
	}
	if strings.TrimSpace(string(raw)) != "" {
		t.Fatalf("expected no promoted rules after first observation, got %q", string(raw))
	}

	d.processRecord(&logItem{name: "example.com.", qtype: 1, source: "live"})
	d.performWrite(WriteModeSave)

	raw, err = os.ReadFile(filepath.Join(dir, "realip.rule"))
	if err != nil {
		t.Fatalf("read rule file: %v", err)
	}
	if !strings.Contains(string(raw), "full:example.com") {
		t.Fatalf("expected promoted rule after threshold, got %q", string(raw))
	}
}

func TestDomainOutputNov4RequiresAQueries(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	d := newDomainOutput(&Args{
		FileStat: filepath.Join(dir, "nov4.txt"),
		FileRule: filepath.Join(dir, "nov4.rule"),
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
	d.performWrite(WriteModeSave)

	raw, err := os.ReadFile(filepath.Join(dir, "nov4.rule"))
	if err != nil {
		t.Fatalf("read rule file: %v", err)
	}
	output := string(raw)
	if strings.Contains(output, "full:ipv6-only.example") {
		t.Fatalf("unexpected AAAA-only promotion in nov4 rules: %q", output)
	}
	if !strings.Contains(output, "full:ipv4-miss.example") {
		t.Fatalf("expected A-only promotion in nov4 rules: %q", output)
	}
}

func TestDomainOutputLoadLegacyStatFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statPath := filepath.Join(dir, "legacy.txt")
	if err := os.WriteFile(statPath, []byte("0000000003 2026-03-01 legacy.example.com\n"), 0644); err != nil {
		t.Fatalf("write legacy stat file: %v", err)
	}

	d := newDomainOutput(&Args{FileStat: statPath})
	d.loadFromFile()

	d.mu.Lock()
	entry := d.stats["legacy.example.com"]
	d.mu.Unlock()
	if entry == nil || entry.Count != 3 {
		t.Fatalf("expected legacy entry count 3, got %#v", entry)
	}
}

func TestDomainOutputLoadFromRuntimeDatasetWhenFileMissing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statPath := filepath.Join(dir, "realip.txt")
	rulePath := filepath.Join(dir, "realip.rule")
	genPath := filepath.Join(dir, "realip.gen")

	d := newDomainOutput(&Args{
		FileStat: statPath,
		FileRule: rulePath,
		GenRule:  genPath,
		Pattern:  "full:DOMAIN",
		Policy: &PolicyArgs{
			Kind:         "realip",
			PromoteAfter: 1,
			PublishMode:  "all",
			DecayDays:    30,
		},
	})
	d.processRecord(&logItem{name: "runtime.example.", qtype: 1, source: "live"})
	d.performWrite(WriteModeSave)

	if err := os.Remove(statPath); err != nil {
		t.Fatalf("remove stat file: %v", err)
	}

	reloaded := newDomainOutput(&Args{
		FileStat: statPath,
		FileRule: rulePath,
		GenRule:  genPath,
	})
	reloaded.loadFromFile()

	reloaded.mu.Lock()
	entry := reloaded.stats["runtime.example"]
	reloaded.mu.Unlock()
	if entry == nil || entry.Count != 1 {
		t.Fatalf("expected runtime dataset entry count 1, got %#v", entry)
	}
}

func TestDomainOutputMaxEntriesHardCap(t *testing.T) {
	t.Parallel()

	d := newDomainOutput(&Args{
		MaxEntries: 1,
	})

	d.processRecord(&logItem{name: "first.example.", source: "live"})
	d.processRecord(&logItem{name: "second.example.", source: "live"})

	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.stats) != 1 {
		t.Fatalf("expected hard cap to keep 1 entry, got %d", len(d.stats))
	}
	if _, ok := d.stats["first.example"]; !ok {
		t.Fatalf("expected first entry to remain after cap")
	}
	if _, ok := d.stats["second.example"]; ok {
		t.Fatalf("unexpected second entry when cap reached")
	}
	if got := atomic.LoadInt64(&d.droppedByCapCount); got != 1 {
		t.Fatalf("droppedByCapCount = %d, want 1", got)
	}
}

func TestDomainOutputNotifyDirtyAndVerify(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var (
		mu     sync.Mutex
		events []dirtyEvent
	)
	notifySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var event dirtyEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Fatalf("decode dirty event: %v", err)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer notifySrv.Close()

	d := newDomainOutput(&Args{
		FileStat: filepath.Join(dir, "realip.txt"),
		FileRule: filepath.Join(dir, "realip.rule"),
		Policy: &PolicyArgs{
			Kind:                   "realip",
			PromoteAfter:           1,
			TrackQType:             true,
			PublishMode:            "promoted_only",
			DecayDays:              30,
			StaleAfterMinutes:      1,
			RefreshCooldownMinutes: 1,
			OnDirtyURL:             notifySrv.URL,
			VerifyURL:              "http://verify.local/realip",
		},
	})

	d.processRecord(&logItem{name: "example.com.", qtype: 1, source: "live"})
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	if len(events) != 1 {
		t.Fatalf("expected one dirty event, got %d", len(events))
	}
	if events[0].Domain != "example.com" || events[0].VerifyURL != "http://verify.local/realip" {
		t.Fatalf("unexpected dirty event: %#v", events[0])
	}
	mu.Unlock()

	if _, err := d.MarkDomainVerified(context.Background(), "example.com", ""); err != nil {
		t.Fatalf("verify returned error: %v", err)
	}

	d.mu.Lock()
	entry := d.stats["example.com"]
	d.mu.Unlock()
	if entry == nil || entry.RefreshState != "clean" || entry.LastVerifiedAt == "" {
		t.Fatalf("expected clean verified entry, got %#v", entry)
	}
}

func TestDomainOutputPeriodicSkipWhenNotDirty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	statPath := filepath.Join(dir, "stats.txt")
	rulePath := filepath.Join(dir, "rules.txt")
	d := newDomainOutput(&Args{
		FileStat: statPath,
		FileRule: rulePath,
		Policy: &PolicyArgs{
			Kind:        "generic",
			PublishMode: "all",
			DecayDays:   30,
		},
	})

	d.processRecord(&logItem{name: "skip.example.", qtype: 1, source: "live"})
	d.performWrite(WriteModePeriodic)

	st, err := os.Stat(statPath)
	if err != nil {
		t.Fatalf("stat first write: %v", err)
	}
	firstMod := st.ModTime()
	time.Sleep(1100 * time.Millisecond)

	d.performWrite(WriteModePeriodic)
	st2, err := os.Stat(statPath)
	if err != nil {
		t.Fatalf("stat second write: %v", err)
	}
	if !st2.ModTime().Equal(firstMod) {
		t.Fatalf("expected periodic clean write to be skipped, mod time changed: %v -> %v", firstMod, st2.ModTime())
	}
}

func TestDomainOutputInferPolicyDefaultsWithoutPolicyBlock(t *testing.T) {
	t.Parallel()

	cfg := &Args{
		FileStat:     "gen/realiplist.txt",
		FileRule:     "gen/realiprule.txt",
		DomainSetURL: "http://127.0.0.1:9099/api/v1/lists/my_realiprule",
	}
	p := normalizePolicy(cfg)
	if p.kind != "realip" {
		t.Fatalf("kind = %q, want realip", p.kind)
	}
	if p.promoteAfter != 2 {
		t.Fatalf("promoteAfter = %d, want 2", p.promoteAfter)
	}
	if p.decayDays != 21 {
		t.Fatalf("decayDays = %d, want 21", p.decayDays)
	}
	if p.onDirtyURL != "http://127.0.0.1:9099/api/v1/runtime/requery/enqueue" {
		t.Fatalf("onDirtyURL = %q", p.onDirtyURL)
	}
	if p.verifyURL != "http://127.0.0.1:9099/api/v1/memory/my_realiplist/verify" {
		t.Fatalf("verifyURL = %q", p.verifyURL)
	}
}

func TestDomainOutputInferPolicyNodeNov4(t *testing.T) {
	t.Parallel()

	cfg := &Args{
		FileStat:     "gen/nodenov4list.txt",
		FileRule:     "gen/nodenov4rule.txt",
		DomainSetURL: "http://127.0.0.1:9099/api/v1/lists/my_nodenov4rule",
	}
	p := normalizePolicy(cfg)
	if p.kind != "nov4" {
		t.Fatalf("kind = %q, want nov4", p.kind)
	}
	if p.decayDays != 14 {
		t.Fatalf("decayDays = %d, want 14", p.decayDays)
	}
	if p.verifyURL != "http://127.0.0.1:9099/api/v1/memory/my_nodenov4list/verify" {
		t.Fatalf("verifyURL = %q", p.verifyURL)
	}
}

func TestDomainOutputInferPolicyWithoutAPIBase(t *testing.T) {
	t.Parallel()

	cfg := &Args{
		FileStat: "gen/nov4list.txt",
		FileRule: "gen/nov4rule.txt",
	}
	p := normalizePolicy(cfg)
	if p.kind != "nov4" {
		t.Fatalf("kind = %q, want nov4", p.kind)
	}
	if p.onDirtyURL != "" {
		t.Fatalf("onDirtyURL = %q, want empty", p.onDirtyURL)
	}
	if p.verifyURL != "" {
		t.Fatalf("verifyURL = %q, want empty", p.verifyURL)
	}
}
