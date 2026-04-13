package coremain

import (
	"testing"
	"time"
)

func TestHeapReclaimStateObserve(t *testing.T) {
	now := time.Date(2026, 4, 12, 10, 30, 0, 0, time.UTC)
	stats := heapStatsSnapshot{
		HeapAlloc:    40 << 20,
		HeapIdle:     32 << 20,
		HeapReleased: 8 << 20,
	}
	state := new(heapReclaimState)

	if state.observe(stats, now) {
		t.Fatal("unexpected reclaim on first observation")
	}
	if state.observe(stats, now.Add(5*time.Second)) {
		t.Fatal("unexpected reclaim before quiet window")
	}
	if !state.observe(stats, now.Add(16*time.Second)) {
		t.Fatal("expected reclaim after quiet window")
	}
	if state.observe(stats, now.Add(25*time.Second)) {
		t.Fatal("unexpected reclaim inside min interval")
	}
}

func TestHeapReclaimStateObserveResetsOnGrowth(t *testing.T) {
	now := time.Date(2026, 4, 12, 10, 31, 0, 0, time.UTC)
	state := new(heapReclaimState)
	base := heapStatsSnapshot{
		HeapAlloc:    40 << 20,
		HeapIdle:     32 << 20,
		HeapReleased: 8 << 20,
	}
	grown := heapStatsSnapshot{
		HeapAlloc:    base.HeapAlloc + backgroundHeapReclaimGrowthTolerance + (1 << 20),
		HeapIdle:     base.HeapIdle,
		HeapReleased: base.HeapReleased,
	}

	if state.observe(base, now) {
		t.Fatal("unexpected reclaim on baseline")
	}
	if state.observe(grown, now.Add(16*time.Second)) {
		t.Fatal("unexpected reclaim after heap growth")
	}
	if state.observe(base, now.Add(23*time.Second)) {
		t.Fatal("unexpected reclaim immediately after growth reset")
	}
	if !state.observe(base, now.Add(32*time.Second)) {
		t.Fatal("expected reclaim once heap stays stable again")
	}
}
