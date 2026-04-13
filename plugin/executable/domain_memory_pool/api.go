package domain_memory_pool

import (
	"container/heap"
	"context"
	"fmt"
	"sort"
	"strconv"
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
	dirtyEntries, promotedEntries := d.snapshotDomainCountersLocked()
	d.mu.Unlock()
	hotRules := atomic.LoadInt64(&d.publishedCount)
	return coremain.DomainStatsSnapshot{
		MemoryID:             d.memoryID,
		Kind:                 d.policy.kind,
		TotalEntries:         totalEntries,
		DirtyEntries:         dirtyEntries,
		PromotedEntries:      int64(promotedEntries),
		PublishedRules:       hotRules,
		HotRules:             hotRules,
		HotPendingRules:      atomic.LoadInt64(&d.hotPendingCount),
		HotAddTotal:          atomic.LoadInt64(&d.hotAddTotal),
		HotReplaceTotal:      atomic.LoadInt64(&d.hotReplaceTotal),
		HotDispatchFailTotal: atomic.LoadInt64(&d.hotDispatchFailTotal),
		LastHotSyncAtUnixMS:  atomic.LoadInt64(&d.lastHotSyncAtUnixMS),
		TotalObservations:    atomic.LoadInt64(&d.totalCount),
		DroppedObservations:  atomic.LoadInt64(&d.droppedCount),
		DroppedByBuffer:      atomic.LoadInt64(&d.droppedBufferCount),
		DroppedByCap:         atomic.LoadInt64(&d.droppedByCapCount),
	}
}

func (d *domainMemoryPool) CacheRevision() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.hasRulesHash {
		return ""
	}
	return strconv.FormatUint(d.lastRulesHash, 16)
}

func (d *domainMemoryPool) snapshotDomainCountersLocked() (int, int) {
	dirty := make(map[string]struct{}, len(d.domainVariantCount))
	promoted := make(map[string]struct{}, len(d.domainVariantCount))
	for key, entry := range d.stats {
		bare := key.domain
		if bare == "" {
			continue
		}
		if entry.RefreshState == "dirty" {
			dirty[bare] = struct{}{}
		}
		if entry.Promoted {
			promoted[bare] = struct{}{}
		}
	}
	return len(dirty), len(promoted)
}

func (d *domainMemoryPool) SnapshotRefreshCandidates(req coremain.DomainRefreshCandidateRequest) []coremain.DomainRefreshCandidate {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UTC()
	aggregated := make(map[string]coremain.DomainRefreshCandidate, d.domainCount)
	for key, entry := range d.stats {
		domain := key.domain
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
		item.LastSeenAt = formatLatestStamp(item.LastSeenAt, entry.LastSeenAtUnixMS)
		item.LastDirtyAt = formatLatestStamp(item.LastDirtyAt, entry.LastDirtyAtUnixMS)
		item.LastVerifiedAt = formatLatestStamp(item.LastVerifiedAt, entry.LastVerifiedAtUnixMS)
		item.CooldownUntil = formatLatestStamp(item.CooldownUntil, entry.CooldownUntilUnixMS)
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
	if d.policy.staleAfterMinutes > 0 && entry.LastDirtyAtUnixMS > 0 {
		if now.Sub(time.UnixMilli(entry.LastDirtyAtUnixMS)) >= time.Duration(d.policy.staleAfterMinutes)*time.Minute {
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
	lastUnixMS := latestNonZero(entry.LastVerifiedAtUnixMS, entry.LastDirtyAtUnixMS, entry.LastSeenAtUnixMS)
	if lastUnixMS <= 0 {
		return entry.Promoted
	}
	return now.Sub(time.UnixMilli(lastUnixMS)) >= threshold
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
	verifiedAtUnixMS := parseStampUnixMS(verifiedAt)
	if verifiedAtUnixMS <= 0 {
		verifiedAtUnixMS = time.Now().UTC().UnixMilli()
	}

	d.mu.Lock()
	updated := 0
	for key, entry := range d.stats {
		bare := key.domain
		if bare != domain {
			continue
		}
		entry.LastVerifiedAtUnixMS = verifiedAtUnixMS
		entry.RefreshState = "clean"
		entry.DirtyReason = ""
		entry.CooldownUntilUnixMS = 0
		entry.LastDirtyAtUnixMS = verifiedAtUnixMS
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
		domain := key.domain
		if query != "" && !strings.Contains(strings.ToLower(domain), query) {
			continue
		}
		item := aggregated[domain]
		item.Domain = domain
		item.Count += entry.Count
		item.Score += entry.Score
		item.QMask |= entry.QTypeMask
		item.Prom = item.Prom || entry.Promoted
		item.DateUnixMS = maxInt64(item.DateUnixMS, entry.LastSeenAtUnixMS)
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
			Date:      formatDate(item.DateUnixMS),
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

func formatLatestStamp(current string, unixMS int64) string {
	if unixMS <= 0 {
		return current
	}
	next := formatStamp(unixMS)
	if next > current {
		return next
	}
	return current
}

func latestNonZero(values ...int64) int64 {
	var latest int64
	for _, value := range values {
		if value > latest {
			latest = value
		}
	}
	return latest
}

func parseStampUnixMS(value string) int64 {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0
	}
	return ts.UnixMilli()
}
