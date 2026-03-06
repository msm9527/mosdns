package domain_output

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	req := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(`{"domain":"example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	d.Api().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("verify status code = %d body=%s", rr.Code, rr.Body.String())
	}

	d.mu.Lock()
	entry := d.stats["example.com"]
	d.mu.Unlock()
	if entry == nil || entry.RefreshState != "clean" || entry.LastVerifiedAt == "" {
		t.Fatalf("expected clean verified entry, got %#v", entry)
	}
}
