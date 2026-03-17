package domain_stats_pool

import (
	"container/heap"
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

type outputRankHeap []outputRankItem

func (h outputRankHeap) Len() int           { return len(h) }
func (h outputRankHeap) Less(i, j int) bool { return h[i].Count < h[j].Count }
func (h outputRankHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *outputRankHeap) Push(x any)        { *h = append(*h, x.(outputRankItem)) }
func (h *outputRankHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func (d *domainStatsPool) SnapshotDomainStats() coremain.DomainStatsSnapshot {
	d.mu.Lock()
	totalEntries := d.domainCount
	dirtyEntries := 0
	for domain, variants := range d.domainVariantCount {
		if variants == 0 || domain == "" {
			continue
		}
		if d.domainHasDirtyVariantLocked(domain) {
			dirtyEntries++
		}
	}
	d.mu.Unlock()
	return coremain.DomainStatsSnapshot{
		MemoryID:            d.memoryID,
		Kind:                d.policy.kind,
		TotalEntries:        totalEntries,
		DirtyEntries:        dirtyEntries,
		PromotedEntries:     atomic.LoadInt64(&d.promotedCount),
		PublishedRules:      atomic.LoadInt64(&d.publishedCount),
		TotalObservations:   atomic.LoadInt64(&d.totalCount),
		DroppedObservations: atomic.LoadInt64(&d.droppedCount),
		DroppedByBuffer:     atomic.LoadInt64(&d.droppedBufferCount),
		DroppedByCap:        atomic.LoadInt64(&d.droppedByCapCount),
	}
}

func (d *domainStatsPool) domainHasDirtyVariantLocked(domain string) bool {
	for key, entry := range d.stats {
		bare, _ := splitStorageKey(key)
		if bare == domain && entry.RefreshState == "dirty" {
			return true
		}
	}
	return false
}

func (d *domainStatsPool) SnapshotRefreshCandidates(req coremain.DomainRefreshCandidateRequest) []coremain.DomainRefreshCandidate {
	_ = req
	return nil
}

func (d *domainStatsPool) SaveToDisk(_ context.Context) error {
	return d.performWrite(WriteModeSave)
}

func (d *domainStatsPool) FlushRuntime(_ context.Context) error {
	return d.performWrite(WriteModeFlush)
}

func (d *domainStatsPool) MarkDomainVerified(_ context.Context, domain, verifiedAt string) (int, error) {
	_ = verifiedAt
	if strings.TrimSpace(strings.TrimSuffix(domain, ".")) == "" {
		return 0, fmt.Errorf("domain is empty")
	}
	return 0, fmt.Errorf("stats pool does not support verify")
}

func (d *domainStatsPool) MemoryEntries(query string, offset, limit int) ([]coremain.MemoryEntry, int, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	offset, limit = normalizePagination(offset, limit)
	heapItems := &outputRankHeap{}
	heap.Init(heapItems)
	maxHeapSize := offset + limit

	d.mu.Lock()
	aggregated := make(map[string]outputRankItem, d.domainCount)
	for key, entry := range d.stats {
		domain, _ := splitStorageKey(key)
		if query != "" && !strings.Contains(strings.ToLower(domain), query) {
			continue
		}
		item := aggregated[domain]
		item.Domain = domain
		item.Count += entry.Count
		item.Score += entry.Score
		item.QMask |= entry.QTypeMask
		item.Prom = item.Prom || entry.Promoted
		item.Date = maxStringByValue(item.Date, entry.LastDate)
		aggregated[domain] = item
	}
	d.mu.Unlock()

	for _, item := range aggregated {
		if heapItems.Len() < maxHeapSize {
			heap.Push(heapItems, item)
			continue
		}
		if item.Count > (*heapItems)[0].Count {
			heap.Pop(heapItems)
			heap.Push(heapItems, item)
		}
	}

	total := len(aggregated)
	resultCount := heapItems.Len()
	sortedItems := make([]outputRankItem, resultCount)
	for i := resultCount - 1; i >= 0; i-- {
		sortedItems[i] = heap.Pop(heapItems).(outputRankItem)
	}

	items := make([]coremain.MemoryEntry, 0, max(0, resultCount-offset))
	for i := offset; i < resultCount; i++ {
		item := sortedItems[i]
		items = append(items, coremain.MemoryEntry{
			Domain:    item.Domain,
			Count:     item.Count,
			Date:      item.Date,
			QTypeMask: item.QMask,
			Score:     item.Score,
			Promoted:  item.Prom,
		})
	}
	return items, total, nil
}

func normalizePagination(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = 100
	}
	return offset, limit
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
