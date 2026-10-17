package domain_mapper

import (
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type interfacePayloadExporter struct {
	payload any
	calls   *atomic.Uint64
}

func (interfacePayloadExporter) GetRules() ([]string, error) {
	return []string{"full:stale.example"}, nil
}
func (interfacePayloadExporter) Subscribe(func()) {}
func (p interfacePayloadExporter) SnapshotHotRules() ([]string, error) {
	return p.GetRules()
}
func (p interfacePayloadExporter) AllowHotRule(string, time.Time) bool {
	p.calls.Add(1)
	return true
}

func TestProviderIdentityChecksDynamicInterfaceValues(t *testing.T) {
	provider := interfacePayloadExporter{payload: []int{1}, calls: new(atomic.Uint64)}
	if comparableProviderKey(provider) != nil {
		t.Fatal("an interface field containing a slice cannot be compared safely")
	}
	dm := mapperWithSourceProvider(t, provider)
	marks, tag, ok := dm.FastMatch("stale.example.")
	if !ok || !reflect.DeepEqual(marks, []uint8{11}) || tag != "memory" || provider.calls.Load() == 0 {
		t.Fatalf("unexpected interface payload exporter result %v %q %v", marks, tag, ok)
	}
}

func TestFastFQDNMatchesCanonicalNormalization(t *testing.T) {
	for _, name := range []string{"", ".", "..", "example..", "example...", "stale.example.", " STALE.Example. ", "Ä.example.", "ä.example."} {
		if got, want := fastEnsureFQDN(name), ensureFQDN(name); got != want {
			t.Errorf("normalize %q: got %q want %q", name, got, want)
		}
	}
}

func TestHotRuleMatchingNormalizesUnicodeAndTrailingDots(t *testing.T) {
	dm := mapperWithSourceProvider(t, &mockRuleExporter{hotRules: []string{"full:ä.example", "full:stale.example"}})
	for _, name := range []string{"Ä.example.", "ä.example.", "STALE.example.", "stale.example.."} {
		for repeat := 0; repeat < 2; repeat++ {
			if _, _, ok := dm.FastMatch(name); !ok {
				t.Errorf("hot rule failed for %q", name)
			}
		}
	}
}

func TestMergeTagStringsUsesWholeTags(t *testing.T) {
	for _, tt := range []struct{ current, next, want string }{
		{"proxy_direct", "direct", "proxy_direct|direct"},
		{"first|second", "second|third", "first|second|third"},
		{"direct|proxy_direct", "direct", "direct|proxy_direct"},
		{"direct", "direct|direct_extra", "direct|direct_extra"},
	} {
		if got := mergeTagStrings(tt.current, tt.next); got != tt.want {
			t.Errorf("merge %q and %q: got %q want %q", tt.current, tt.next, got, tt.want)
		}
	}
}
