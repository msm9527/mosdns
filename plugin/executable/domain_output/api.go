package domain_output

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/go-chi/chi/v5"
)

type outputRankHeap []outputRankItem

func (h outputRankHeap) Len() int           { return len(h) }
func (h outputRankHeap) Less(i, j int) bool { return h[i].Count < h[j].Count }
func (h outputRankHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *outputRankHeap) Push(x any)        { *h = append(*h, x.(outputRankItem)) }
func (h *outputRankHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

type statsResponse struct {
	MemoryID               string `json:"memory_id"`
	Kind                   string `json:"kind"`
	PromoteAfter           int    `json:"promote_after"`
	DecayDays              int    `json:"decay_days"`
	PublishMode            string `json:"publish_mode"`
	TrackQType             bool   `json:"track_qtype"`
	StaleAfterMinutes      int    `json:"stale_after_minutes"`
	RefreshCooldownMinutes int    `json:"refresh_cooldown_minutes"`
	MaxEntries             int    `json:"max_entries"`
	TotalEntries           int    `json:"total_entries"`
	DirtyEntries           int    `json:"dirty_entries"`
	PromotedEntries        int64  `json:"promoted_entries"`
	PublishedRules         int64  `json:"published_rules"`
	TotalObservations      int64  `json:"total_observations"`
	DroppedObservations    int64  `json:"dropped_observations"`
	DroppedByBuffer        int64  `json:"dropped_by_buffer"`
	DroppedByCap           int64  `json:"dropped_by_cap"`
}

func (d *domainOutput) SnapshotDomainStats() coremain.DomainStatsSnapshot {
	d.mu.Lock()
	totalEntries := len(d.stats)
	dirtyEntries := 0
	for _, entry := range d.stats {
		if entry.RefreshState == "dirty" {
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

func (d *domainOutput) SnapshotRefreshCandidates(req coremain.DomainRefreshCandidateRequest) []coremain.DomainRefreshCandidate {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UTC()
	candidates := make([]coremain.DomainRefreshCandidate, 0, len(d.stats))
	for key, entry := range d.stats {
		domain := strings.TrimSpace(key)
		if domain == "" {
			continue
		}

		reason, state := d.classifyRefreshCandidate(entry, now)
		include := false
		weight := entry.Score*100 + entry.Count
		if entry.Promoted {
			weight += 50000
		}

		switch reason {
		case "conflict", "error":
			weight += 1000000
		case "stale":
			weight += 900000
		case "refresh_due":
			weight += 700000
		case "observed", "dirty":
			weight += 800000
		}

		if req.IncludeDirty && (state == "dirty" || reason == "observed" || reason == "dirty" || reason == "conflict" || reason == "error") {
			include = true
		}
		if req.IncludeStale && reason == "stale" {
			include = true
		}
		if req.IncludeHot && (entry.Promoted || entry.Score > 0 || entry.Count > 0) {
			include = true
		}
		if !include {
			continue
		}

		qmask := entry.QTypeMask
		if qmask == 0 {
			qmask = qtypeMaskA | qtypeMaskAAAA
		}

		candidates = append(candidates, coremain.DomainRefreshCandidate{
			Domain:         domain,
			QTypeMask:      qmask,
			Weight:         weight,
			MemoryID:       d.memoryID,
			Reason:         reason,
			RefreshState:   state,
			LastSeenAt:     entry.LastSeenAt,
			LastDirtyAt:    entry.LastDirtyAt,
			LastVerifiedAt: entry.LastVerifiedAt,
			CooldownUntil:  entry.CooldownUntil,
			Promoted:       entry.Promoted,
		})
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

func (d *domainOutput) classifyRefreshCandidate(entry *statEntry, now time.Time) (reason string, state string) {
	state = strings.ToLower(strings.TrimSpace(entry.RefreshState))
	reason = strings.ToLower(strings.TrimSpace(entry.DirtyReason))

	if state == "dirty" {
		if reason == "" {
			reason = "dirty"
		}
		return reason, state
	}

	if d.policy.staleAfterMinutes > 0 && entry.LastDirtyAt != "" {
		if ts, err := time.Parse(time.RFC3339, entry.LastDirtyAt); err == nil &&
			now.Sub(ts) >= time.Duration(d.policy.staleAfterMinutes)*time.Minute {
			return "stale", "stale"
		}
	}

	if d.isVerificationDue(entry, now) {
		return "refresh_due", "due"
	}

	if reason != "" {
		return reason, state
	}
	return "", state
}

func (d *domainOutput) isVerificationDue(entry *statEntry, now time.Time) bool {
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

	lastStamp := entry.LastVerifiedAt
	if lastStamp == "" {
		lastStamp = entry.LastDirtyAt
	}
	if lastStamp == "" {
		lastStamp = entry.LastSeenAt
	}
	if lastStamp == "" {
		return entry.Promoted
	}

	ts, err := time.Parse(time.RFC3339, lastStamp)
	if err != nil {
		return false
	}
	return now.Sub(ts) >= threshold
}

type verifyRequest struct {
	Domain     string `json:"domain"`
	VerifiedAt string `json:"verified_at,omitempty"`
}

func (d *domainOutput) SaveToDisk(_ context.Context) error {
	d.performWrite(WriteModeSave)
	return nil
}

func (d *domainOutput) FlushRuntime(_ context.Context) error {
	d.performWrite(WriteModeFlush)
	return nil
}

func (d *domainOutput) MarkDomainVerified(_ context.Context, domain, verifiedAt string) (int, error) {
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
		if strings.Split(key, "|")[0] != domain {
			continue
		}
		entry.LastVerifiedAt = verifiedAt
		entry.RefreshState = "clean"
		entry.DirtyReason = ""
		entry.CooldownUntil = ""
		entry.LastDirtyAt = verifiedAt
		d.stats[key] = entry
		updated++
	}
	d.mu.Unlock()
	if updated == 0 {
		return 0, fmt.Errorf("domain not found")
	}
	d.performWrite(WriteModeSave)
	return updated, nil
}

func (d *domainOutput) Api() *chi.Mux {
	r := chi.NewRouter()

	r.Get("/flush", coremain.WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
		if err := d.FlushRuntime(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("domain_output flushed"))
	}))

	r.Get("/save", coremain.WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
		if err := d.SaveToDisk(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("domain_output saved"))
	}))

	r.Get("/show", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		query := strings.ToLower(r.URL.Query().Get("q"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if limit <= 0 {
			limit = 100
		}
		if offset < 0 {
			offset = 0
		}
		h := &outputRankHeap{}
		heap.Init(h)
		maxHeapSize := offset + limit

		d.mu.Lock()
		totalFiltered := 0
		for domain, entry := range d.stats {
			if query != "" && !strings.Contains(strings.ToLower(domain), query) {
				continue
			}
			totalFiltered++
			item := outputRankItem{Domain: domain, Count: entry.Count, Date: entry.LastDate, Score: entry.Score, QMask: entry.QTypeMask, Prom: entry.Promoted}
			if h.Len() < maxHeapSize {
				heap.Push(h, item)
			} else if item.Count > (*h)[0].Count {
				heap.Pop(h)
				heap.Push(h, item)
			}
		}
		d.mu.Unlock()

		w.Header().Set("X-Total-Count", strconv.Itoa(totalFiltered))
		w.Header().Set("Access-Control-Expose-Headers", "X-Total-Count")

		resultCount := h.Len()
		sortedResult := make([]outputRankItem, resultCount)
		for i := resultCount - 1; i >= 0; i-- {
			sortedResult[i] = heap.Pop(h).(outputRankItem)
		}
		for i := offset; i < resultCount; i++ {
			stat := sortedResult[i]
			_, _ = fmt.Fprintf(w, "%010d %s %s qmask=%d score=%d promoted=%d\n", stat.Count, stat.Date, stat.Domain, stat.QMask, stat.Score, boolToInt(stat.Prom))
		}
	})

	r.Get("/stats", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		totalEntries := len(d.stats)
		dirtyEntries := 0
		for _, entry := range d.stats {
			if entry.RefreshState == "dirty" {
				dirtyEntries++
			}
		}
		d.mu.Unlock()
		resp := statsResponse{
			MemoryID:               d.memoryID,
			Kind:                   d.policy.kind,
			PromoteAfter:           d.policy.promoteAfter,
			DecayDays:              d.policy.decayDays,
			PublishMode:            d.policy.publishMode,
			TrackQType:             d.policy.trackQType,
			StaleAfterMinutes:      d.policy.staleAfterMinutes,
			RefreshCooldownMinutes: d.policy.refreshCooldownMinutes,
			MaxEntries:             d.maxEntries,
			TotalEntries:           totalEntries,
			DirtyEntries:           dirtyEntries,
			PromotedEntries:        atomic.LoadInt64(&d.promotedCount),
			PublishedRules:         atomic.LoadInt64(&d.publishedCount),
			TotalObservations:      atomic.LoadInt64(&d.totalCount),
			DroppedObservations:    atomic.LoadInt64(&d.droppedCount),
			DroppedByBuffer:        atomic.LoadInt64(&d.droppedBufferCount),
			DroppedByCap:           atomic.LoadInt64(&d.droppedByCapCount),
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	r.Post("/verify", coremain.WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
		var req verifyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Domain) == "" {
			http.Error(w, "invalid verify request", http.StatusBadRequest)
			return
		}
		updated, err := d.MarkDomainVerified(r.Context(), req.Domain, req.VerifiedAt)
		if err != nil {
			if err.Error() == "domain not found" {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "updated": updated})
	}))

	r.Get("/restartall", func(w http.ResponseWriter, req *http.Request) {
		_ = d.Close()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("mosdns restarting"))
		go restartSelf()
	})

	return r
}

func (d *domainOutput) sortedDomains() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	domains := make([]string, 0, len(d.stats))
	for domain := range d.stats {
		domains = append(domains, domain)
	}
	sort.Strings(domains)
	return domains
}
