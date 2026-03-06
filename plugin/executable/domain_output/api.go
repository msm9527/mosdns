package domain_output

import (
	"container/heap"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

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
	Kind                string `json:"kind"`
	PromoteAfter        int    `json:"promote_after"`
	DecayDays           int    `json:"decay_days"`
	PublishMode         string `json:"publish_mode"`
	TrackQType          bool   `json:"track_qtype"`
	TotalEntries        int    `json:"total_entries"`
	PromotedEntries     int64  `json:"promoted_entries"`
	PublishedRules      int64  `json:"published_rules"`
	TotalObservations   int64  `json:"total_observations"`
	DroppedObservations int64  `json:"dropped_observations"`
}

func (d *domainOutput) Api() *chi.Mux {
	r := chi.NewRouter()

	r.Get("/flush", coremain.WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
		d.performWrite(WriteModeFlush)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("domain_output flushed"))
	}))

	r.Get("/save", coremain.WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
		d.performWrite(WriteModeSave)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("domain_output saved"))
	}))

	r.Get("/show", coremain.WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
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
	}))

	r.Get("/stats", coremain.WithAsyncGC(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		totalEntries := len(d.stats)
		d.mu.Unlock()
		resp := statsResponse{
			Kind:                d.policy.kind,
			PromoteAfter:        d.policy.promoteAfter,
			DecayDays:           d.policy.decayDays,
			PublishMode:         d.policy.publishMode,
			TrackQType:          d.policy.trackQType,
			TotalEntries:        totalEntries,
			PromotedEntries:     atomic.LoadInt64(&d.promotedCount),
			PublishedRules:      atomic.LoadInt64(&d.publishedCount),
			TotalObservations:   atomic.LoadInt64(&d.totalCount),
			DroppedObservations: atomic.LoadInt64(&d.droppedCount),
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
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
