package domain_memory_pool

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
)

func hotRuleLookupPool(n int, scenario string) (*domainMemoryPool, time.Time) {
	now := time.Unix(1_800_000_000, 0).UTC()
	d := &domainMemoryPool{stats: make(map[entryKey]*statEntry, n)}
	d.policy.staleAfterMinutes = 60
	d.policy.refreshCooldownMinute = 10
	d.policy.requeryTag = "verify"
	unrelated := n
	if scenario != "absent" {
		unrelated -= 2
	}
	for i := 0; i < unrelated; i++ {
		d.stats[buildEntryKey(fmt.Sprintf("unrelated-%06d.example", i), 0)] = &statEntry{Promoted: true, RefreshState: "clean", LastVerifiedAtUnixMS: now.UnixMilli()}
	}
	if scenario == "absent" {
		return d, now
	}
	// High flags exercise persisted masks that runtime AD/CD/DO builders don't emit.
	d.stats[buildEntryKey("target.example", 0)] = &statEntry{Promoted: true, RefreshState: "dirty", QTypeMask: qtypeMaskA, CooldownUntilUnixMS: now.Add(time.Hour).UnixMilli()}
	d.stats[buildEntryKey("target.example", 255)] = &statEntry{Promoted: true, RefreshState: "dirty", QTypeMask: qtypeMaskAAAA, CooldownUntilUnixMS: now.Add(time.Hour).UnixMilli()}
	switch scenario {
	case "valid_low":
		d.stats[buildEntryKey("target.example", 0)].RefreshState = "clean"
		d.stats[buildEntryKey("target.example", 0)].LastVerifiedAtUnixMS = now.UnixMilli()
	case "valid":
		d.stats[buildEntryKey("target.example", 255)].RefreshState = "clean"
		d.stats[buildEntryKey("target.example", 255)].LastVerifiedAtUnixMS = now.UnixMilli()
	case "refresh":
		d.stats[buildEntryKey("target.example", 255)].CooldownUntilUnixMS = 0
		d.policy.refreshCooldownMinute = 0
	}
	return d, now
}

func TestHotRuleDomainLookupContract(t *testing.T) {
	for _, size := range []int{32, 256, 257, 1000} {
		for _, flags := range []uint8{0, 1, 7, 8, 127, 128, 254, 255} {
			t.Run(fmt.Sprintf("n%d/flags%d", size, flags), func(t *testing.T) {
				d, now := hotRuleLookupPool(size-1, "absent")
				key := buildEntryKey("target.example", flags)
				d.stats[key] = &statEntry{Promoted: true, RefreshState: "clean", LastVerifiedAtUnixMS: now.UnixMilli(), QTypeMask: qtypeMaskA}
				if !d.AllowHotRuleWithSource("target.example.", now, server.RequestSourceUser) {
					t.Fatal("valid variant was rejected")
				}
				d.stats[key].RefreshState = "dirty"
				before := *d.stats[key]
				if d.AllowHotRuleWithSource("target.example", now, server.RequestSourceRefresh) || *d.stats[key] != before || d.dirtyPending.Load() {
					t.Fatal("background rejection changed refresh state")
				}
			})
		}
	}
	for _, size := range []int{32, 256, 257, 1000} {
		t.Run(fmt.Sprintf("n%d/mixed_variants", size), func(t *testing.T) {
			d, now := hotRuleLookupPool(size, "valid")
			before := *d.stats[buildEntryKey("target.example", 0)]
			if !d.AllowHotRule("target.example", now) || *d.stats[buildEntryKey("target.example", 0)] != before || d.dirtyPending.Load() {
				t.Fatal("one valid variant must prevent dirtying rejected variants")
			}
			d, now = hotRuleLookupPool(size, "stale_cooldown")
			if d.AllowHotRule("target.example", now) || d.dirtyPending.Load() {
				t.Fatal("all variants in cooldown must reject without enqueueing")
			}
			if job := d.markHotRuleRefreshLocked("target.example", now); job != nil {
				t.Fatal("all variants in cooldown produced a refresh job")
			}
			d, now = hotRuleLookupPool(size, "refresh")
			before = *d.stats[buildEntryKey("target.example", 0)]
			delete(d.stats, buildEntryKey("unrelated-000000.example", 0))
			d.stats[buildEntryKey("target.example", 128)] = &statEntry{Promoted: false, QTypeMask: 128}
			job := d.markHotRuleRefreshLocked("target.example", now)
			if job == nil || job.QTypeMask != qtypeMaskA|qtypeMaskAAAA {
				t.Fatalf("job must aggregate all promoted variants including cooldown, got %#v", job)
			}
			if !reflect.DeepEqual(*d.stats[buildEntryKey("target.example", 0)], before) {
				t.Fatal("cooldown variant changed")
			}
			if got := d.stats[buildEntryKey("target.example", 255)]; got.RefreshState != "dirty" || got.DirtyReason != "stale" || got.LastDirtyAtUnixMS != now.UnixMilli() {
				t.Fatalf("eligible high-flags variant not marked dirty, got %#v", got)
			}
		})
	}
}

func TestHotRuleDomainLookupVisitsAllFlags(t *testing.T) {
	for _, unrelated := range []int{0, 1} {
		t.Run(fmt.Sprint(unrelated), func(t *testing.T) {
			d := &domainMemoryPool{stats: make(map[entryKey]*statEntry)}
			for flags := 0; flags < 256; flags++ {
				d.stats[buildEntryKey("target.example", uint8(flags))] = &statEntry{Promoted: true, Count: flags}
			}
			if unrelated != 0 {
				d.stats[buildEntryKey("unrelated.example", 255)] = &statEntry{Promoted: true, Count: 255}
			}
			seen := make(map[int]int)
			d.visitPromotedDomainEntriesLocked("target.example", func(entry *statEntry) bool {
				seen[entry.Count]++
				return true
			})
			if len(seen) != 256 {
				t.Fatalf("visited %d flags, want 256", len(seen))
			}
			for flags, count := range seen {
				if count != 1 {
					t.Fatalf("visited flags %d %d times", flags, count)
				}
			}
			calls := 0
			d.visitPromotedDomainEntriesLocked("target.example", func(*statEntry) bool {
				calls++
				return false
			})
			if calls != 1 {
				t.Fatalf("short circuit made %d calls", calls)
			}
		})
	}
}

var hotRuleLookupAllowed bool
var hotRuleLookupMask uint8

func BenchmarkHotRuleDomainLookup(b *testing.B) {
	for _, n := range []int{32, 64, 256, 257, 1000, 10000, 100000} {
		for _, scenario := range []string{"absent", "valid", "valid_low", "stale_cooldown", "refresh"} {
			b.Run(fmt.Sprintf("n%d/%s", n, scenario), func(b *testing.B) {
				d, now := hotRuleLookupPool(n, scenario)
				b.ReportAllocs()
				b.ResetTimer()
				if scenario == "refresh" {
					for i := 0; i < b.N; i++ {
						d.mu.Lock()
						job := d.markHotRuleRefreshLocked("target.example", now)
						d.mu.Unlock()
						hotRuleLookupMask = job.QTypeMask
					}
					return
				}
				for i := 0; i < b.N; i++ {
					hotRuleLookupAllowed = d.AllowHotRuleWithSource("target.example", now, server.RequestSourceUser)
				}
			})
		}
	}
}
