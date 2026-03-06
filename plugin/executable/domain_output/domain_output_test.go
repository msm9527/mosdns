package domain_output

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
