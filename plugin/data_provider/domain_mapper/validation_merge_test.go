package domain_mapper

import (
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
)

func TestValidationDomainRetainsNormalization(t *testing.T) {
	for _, name := range []string{"", " . ", "stale.example.", " STALE.Example. ", "stale.example..", "Ä.example."} {
		want := ensureFQDN(name)
		if len(want) > 0 {
			want = want[:len(want)-1]
		}
		if got := normalizedValidationDomain(name); got != want {
			t.Fatalf("normalize %q: got %q want %q", name, got, want)
		}
	}
}

func TestDistinctDynamicSourcesValidateAndMergeIndependently(t *testing.T) {
	first := &sourceValidationSpy{mockRuleExporter: &mockRuleExporter{rules: []string{"full:stale.example"}, hotRules: []string{"full:stale.example"}}, expires: time.Now().Add(time.Hour)}
	second := &sourceValidationSpy{mockRuleExporter: &mockRuleExporter{rules: []string{"domain:example"}, hotRules: []string{"full:stale.example"}}, expires: time.Now().Add(time.Hour)}
	m := coremain.NewTestMosdnsWithPlugins(map[string]any{"first": first, "second": second})
	v, err := NewMapper(coremain.NewBP("mapper", m), &Args{Rules: []RuleConfig{{Tag: "first", Mark: 11}, {Tag: "second", Mark: 12}}})
	if err != nil {
		t.Fatal(err)
	}
	dm := v.(*DomainMapper)
	marks, tags, ok := dm.FastMatchWithSource("stale.example.", server.RequestSourceUser)
	if !ok || !reflect.DeepEqual(marks, []uint8{11, 12}) || tags != "first|second" {
		t.Fatalf("unexpected combined result %v %q %v", marks, tags, ok)
	}
	if len(first.calls) != 1 || len(second.calls) != 1 {
		t.Fatalf("first calls %d, second calls %d", len(first.calls), len(second.calls))
	}
	second.expires = time.Now().Add(-time.Hour)
	nextMarks, nextTags, ok := dm.FastMatchWithSource("stale.example.", server.RequestSourceRefresh)
	if !ok || !reflect.DeepEqual(nextMarks, []uint8{11}) || nextTags != "first" {
		t.Fatalf("expired source was reused: %v %q %v", nextMarks, nextTags, ok)
	}
	if !reflect.DeepEqual(marks, []uint8{11, 12}) || second.notifications != 0 {
		t.Fatal("revalidation changed an earlier result or created background demand")
	}
	first.expires = time.Now().Add(-time.Hour)
	if _, _, ok := dm.FastMatchWithSource("stale.example.", server.RequestSourceRefresh); ok {
		t.Fatal("both expired sources still matched")
	}
}

type uncomparableRuleExporter struct {
	rules []string
	calls *atomic.Uint64
}

func (p uncomparableRuleExporter) GetRules() ([]string, error) { return p.rules, nil }
func (uncomparableRuleExporter) Subscribe(func())              {}
func (p uncomparableRuleExporter) SnapshotHotRules() ([]string, error) {
	return p.rules, nil
}
func (p uncomparableRuleExporter) AllowHotRule(string, time.Time) bool {
	p.calls.Add(1)
	return true
}

func TestUncomparableExporterRetainsValidation(t *testing.T) {
	calls := new(atomic.Uint64)
	dm := mapperWithSourceProvider(t, uncomparableRuleExporter{rules: []string{"full:stale.example"}, calls: calls})
	marks, tag, ok := dm.FastMatch("stale.example.")
	if !ok || !reflect.DeepEqual(marks, []uint8{11}) || tag != "memory" || calls.Load() == 0 {
		t.Fatalf("unexpected uncomparable exporter result %v %q %v calls=%d", marks, tag, ok, calls.Load())
	}
}
