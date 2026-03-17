package domain_memory_pool

import (
	"container/heap"
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

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

func (d *domainMemoryPool) SnapshotDomainStats() coremain.DomainStatsSnapshot {
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

func (d *domainMemoryPool) domainHasDirtyVariantLocked(domain string) bool {
	for key, entry := range d.stats {
		bare, _ := splitStorageKey(key)
		if bare == domain && entry.RefreshState == "dirty" {
			return true
		}
	}
	return false
}

func (d *domainMemoryPool) SnapshotRefreshCandidates(req coremain.DomainRefreshCandidateRequest) []coremain.DomainRefreshCandidate {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UTC()
	aggregated := make(map[string]coremain.DomainRefreshCandidate, d.domainCount)
	for key, entry := range d.stats {
		domain, _ := splitStorageKey(key)
		if domain == "" {
			continue
		}
		reason, state := d.classifyRefreshCandidate(entry, now)
		item := aggregated[domain]
		item.Domain = domain
		item.QTypeMask |= entry.QTypeMask
		item.Weight += entry.Score*100 + entry.Count
		item.MemoryID = d.memoryID
		item.Promoted = item.Promoted || entry.Promoted
		item.LastSeenAt = maxStringByValue(item.LastSeenAt, entry.LastSeenAt)
		item.LastDirtyAt = maxStringByValue(item.LastDirtyAt, entry.LastDirtyAt)
		item.LastVerifiedAt = maxStringByValue(item.LastVerifiedAt, entry.LastVerifiedAt)
		item.CooldownUntil = maxStringByValue(item.CooldownUntil, entry.CooldownUntil)
		if priorityForReason(reason) >= priorityForReason(item.Reason) {
			item.Reason = reason
			item.RefreshState = state
		}
		aggregated[domain] = item
	}

	candidates := make([]coremain.DomainRefreshCandidate, 0, len(aggregated))
	for _, item := range aggregated {
		if item.QTypeMask == 0 {
			item.QTypeMask = qtypeMaskA | qtypeMaskAAAA
		}
		if !includeCandidate(req, item) {
			continue
		}
		candidates = append(candidates, item)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Weight == candidates[j].Weight {
			return candidates[i].Domain < candidates[j].Domain
		}
		return candidates[i].Weight > candidates[j].Weight
	})
	if req.Limit > 0 && len(candidates) > req.Limit {
		candidates = candidates[:req.Limit]
	}
	return candidates
}

func includeCandidate(req coremain.DomainRefreshCandidateRequest, item coremain.DomainRefreshCandidate) bool {
	if req.IncludeDirty && (item.RefreshState == "dirty" || item.Reason == "observed" || item.Reason == "dirty") {
		return true
	}
	if req.IncludeStale && item.Reason == "stale" {
		return true
	}
	if req.IncludeHot && (item.Promoted || item.Weight > 0) {
		return true
	}
	return false
}

func priorityForReason(reason string) int {
	switch strings.ToLower(reason) {
	case "conflict", "error":
		return 100
	case "stale":
		return 80
	case "refresh_due":
		return 70
	case "observed", "dirty":
		return 60
	default:
		return 0
	}
}

func (d *domainMemoryPool) classifyRefreshCandidate(entry *statEntry, now time.Time) (string, string) {
	state := strings.ToLower(strings.TrimSpace(entry.RefreshState))
	reason := strings.ToLower(strings.TrimSpace(entry.DirtyReason))
	if state == "dirty" {
		if reason == "" {
			reason = "dirty"
		}
		return reason, state
	}
	if d.policy.staleAfterMinutes > 0 && entry.LastDirtyAt != "" {
		ts, err := time.Parse(time.RFC3339, entry.LastDirtyAt)
		if err == nil && now.Sub(ts) >= time.Duration(d.policy.staleAfterMinutes)*time.Minute {
			return "stale", "stale"
		}
	}
	if d.isVerificationDue(entry, now) {
		return "refresh_due", "due"
	}
	return reason, state
}

func (d *domainMemoryPool) isVerificationDue(entry *statEntry, now time.Time) bool {
	if !entry.Promoted && entry.Count < 3 {
		return false
	}
	threshold := 30 * time.Minute
	if d.policy.staleAfterMinutes > 0 {
		candidate := time.Duration(d.policy.staleAfterMinutes) * time.Minute / 2
		if candidate > threshold {
			threshold = candidate
		}
	}
	lastStamp := firstNonEmpty(entry.LastVerifiedAt, firstNonEmpty(entry.LastDirtyAt, entry.LastSeenAt))
	if lastStamp == "" {
		return entry.Promoted
	}
	ts, err := time.Parse(time.RFC3339, lastStamp)
	if err != nil {
		return false
	}
	return now.Sub(ts) >= threshold
}

func (d *domainMemoryPool) SaveToDisk(_ context.Context) error {
	return d.performWrite(WriteModeSave)
}

func (d *domainMemoryPool) FlushRuntime(_ context.Context) error {
	return d.performWrite(WriteModeFlush)
}

func (d *domainMemoryPool) MarkDomainVerified(_ context.Context, domain, verifiedAt string) (int, error) {
	domain = strings.TrimSpace(strings.TrimSuffix(domain, "."))
	if domain == "" {
		return 0, fmt.Errorf("domain is empty")
	}
	if verifiedAt == "" {
		verifiedAt = time.Now().UTC().Format(time.RFC3339)
	}

	d.mu.Lock()
	updated := 0
	for key, entry := range d.stats {
		bare, _ := splitStorageKey(key)
		if bare != domain {
			continue
		}
		entry.LastVerifiedAt = verifiedAt
		entry.RefreshState = "clean"
		entry.DirtyReason = ""
		entry.CooldownUntil = ""
		entry.LastDirtyAt = verifiedAt
		updated++
	}
	d.mu.Unlock()
	if updated == 0 {
		return 0, fmt.Errorf("domain not found")
	}
	return updated, d.performWrite(WriteModeSave)
}

func (d *domainMemoryPool) MemoryEntries(query string, offset, limit int) ([]coremain.MemoryEntry, int, error) {
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

func firstNonEmpty(first, fallback string) string {
	if strings.TrimSpace(first) != "" {
		return first
	}
	return fallback
}
