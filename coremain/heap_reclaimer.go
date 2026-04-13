package coremain

import (
	"runtime"
	"runtime/debug"
	"time"
)

const (
	backgroundHeapReclaimCheckPeriod     = 5 * time.Second
	backgroundHeapReclaimQuietWindow     = 8 * time.Second
	backgroundHeapReclaimMinInterval     = 15 * time.Second
	backgroundHeapReclaimMinIdleBytes    = 8 << 20
	backgroundHeapReclaimGrowthTolerance = 16 << 20
)

type heapStatsSnapshot struct {
	HeapAlloc    uint64
	HeapIdle     uint64
	HeapReleased uint64
}

type heapReclaimState struct {
	lastAlloc      uint64
	candidateSince time.Time
	lastReclaim    time.Time
}

func (s *heapReclaimState) observe(stats heapStatsSnapshot, now time.Time) bool {
	reclaimable := stats.HeapIdle - stats.HeapReleased
	if reclaimable < backgroundHeapReclaimMinIdleBytes {
		s.lastAlloc = stats.HeapAlloc
		s.candidateSince = time.Time{}
		return false
	}
	if s.lastAlloc == 0 {
		s.lastAlloc = stats.HeapAlloc
		s.candidateSince = now
		return false
	}
	if stats.HeapAlloc > s.lastAlloc+backgroundHeapReclaimGrowthTolerance {
		s.lastAlloc = stats.HeapAlloc
		s.candidateSince = now
		return false
	}
	if stats.HeapAlloc < s.lastAlloc {
		s.lastAlloc = stats.HeapAlloc
	}
	if s.candidateSince.IsZero() {
		s.lastAlloc = stats.HeapAlloc
		s.candidateSince = now
		return false
	}
	if now.Sub(s.candidateSince) < backgroundHeapReclaimQuietWindow {
		s.lastAlloc = stats.HeapAlloc
		return false
	}
	if !s.lastReclaim.IsZero() && now.Sub(s.lastReclaim) < backgroundHeapReclaimMinInterval {
		s.lastAlloc = stats.HeapAlloc
		return false
	}
	s.lastAlloc = stats.HeapAlloc
	s.lastReclaim = now
	s.candidateSince = time.Time{}
	return true
}

func (m *Mosdns) startBackgroundHeapReclaimer() {
	ticker := time.NewTicker(backgroundHeapReclaimCheckPeriod)
	state := new(heapReclaimState)
	m.sc.Attach(func(done func(), closeSignal <-chan struct{}) {
		defer done()
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var ms runtime.MemStats
				runtime.ReadMemStats(&ms)
				if !state.observe(heapStatsSnapshot{
					HeapAlloc:    ms.HeapAlloc,
					HeapIdle:     ms.HeapIdle,
					HeapReleased: ms.HeapReleased,
				}, time.Now()) {
					continue
				}
				debug.FreeOSMemory()
			case <-closeSignal:
				return
			}
		}
	})
}
