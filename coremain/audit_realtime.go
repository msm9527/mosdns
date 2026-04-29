package coremain

import (
	"math"
	"sync"
	"time"
)

type auditRealtimeBucket struct {
	SecondUnix            int64
	QueryCount            uint64
	DurationSumMs         float64
	MaxDurationMs         float64
	ResolvedQueryCount    uint64
	ResolvedDurationSumMs float64
	ResolvedMaxDurationMs float64
	ErrorCount            uint64
	NoResponseCount       uint64
	CacheHitCount         uint64
	DroppedCount          uint64
}

type auditRealtimeStore struct {
	mu      sync.RWMutex
	buckets []auditRealtimeBucket
}

func newAuditRealtimeStore(size int) *auditRealtimeStore {
	if size <= 0 {
		size = auditRealtimeBucketCount
	}
	return &auditRealtimeStore{buckets: make([]auditRealtimeBucket, size)}
}

func (s *auditRealtimeStore) Record(log AuditLog) {
	s.recordAt(log.QueryTime, func(bucket *auditRealtimeBucket) {
		bucket.QueryCount++
		bucket.DurationSumMs += log.DurationMs
		bucket.MaxDurationMs = math.Max(bucket.MaxDurationMs, log.DurationMs)
		if isAuditResolvedCode(log.ResponseCode) {
			bucket.ResolvedQueryCount++
			bucket.ResolvedDurationSumMs += log.DurationMs
			bucket.ResolvedMaxDurationMs = math.Max(bucket.ResolvedMaxDurationMs, log.DurationMs)
		}
		if isAuditErrorCode(log.ResponseCode) {
			bucket.ErrorCount++
		}
		if log.ResponseCode == "NO_RESPONSE" {
			bucket.NoResponseCount++
		}
		if isAuditCacheHit(log.CacheStatus) {
			bucket.CacheHitCount++
		}
	})
}

func (s *auditRealtimeStore) RecordDrop(at time.Time) {
	s.recordAt(at, func(bucket *auditRealtimeBucket) {
		bucket.DroppedCount++
	})
}

func (s *auditRealtimeStore) Snapshot(windowSeconds int) AuditOverview {
	if windowSeconds <= 0 {
		windowSeconds = auditDefaultOverviewWindowSeconds
	}
	if windowSeconds > len(s.buckets) {
		windowSeconds = len(s.buckets)
	}
	now := time.Now().Unix()
	cutoff := now - int64(windowSeconds) + 1
	var overview AuditOverview

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, bucket := range s.buckets {
		if bucket.SecondUnix < cutoff || bucket.SecondUnix > now {
			continue
		}
		overview.QueryCount += bucket.QueryCount
		overview.AverageDurationMs += bucket.DurationSumMs
		overview.MaxDurationMs = math.Max(overview.MaxDurationMs, bucket.MaxDurationMs)
		overview.ResolvedQueryCount += bucket.ResolvedQueryCount
		overview.ResolvedAverageDurationMs += bucket.ResolvedDurationSumMs
		overview.ResolvedMaxDurationMs = math.Max(overview.ResolvedMaxDurationMs, bucket.ResolvedMaxDurationMs)
		overview.ErrorCount += bucket.ErrorCount
		overview.NoResponseCount += bucket.NoResponseCount
		overview.CacheHitCount += bucket.CacheHitCount
		overview.DroppedEvents += bucket.DroppedCount
	}
	fillAuditOverviewRates(&overview, windowSeconds)
	return overview
}

func (s *auditRealtimeStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	clear(s.buckets)
}

func (s *auditRealtimeStore) recordAt(at time.Time, apply func(bucket *auditRealtimeBucket)) {
	second := at.Unix()
	index := int(second % int64(len(s.buckets)))

	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := &s.buckets[index]
	if bucket.SecondUnix != second {
		s.buckets[index] = auditRealtimeBucket{SecondUnix: second}
		bucket = &s.buckets[index]
	}
	apply(bucket)
}

func fillAuditOverviewRates(overview *AuditOverview, windowSeconds int) {
	if overview == nil {
		return
	}
	overview.WindowSeconds = windowSeconds
	if overview.QueryCount == 0 {
		return
	}
	overview.QPS = float64(overview.QueryCount) / float64(windowSeconds)
	overview.AverageDurationMs = overview.AverageDurationMs / float64(overview.QueryCount)
	overview.ErrorRate = float64(overview.ErrorCount) / float64(overview.QueryCount)
	overview.CacheHitRate = float64(overview.CacheHitCount) / float64(overview.QueryCount)
	if overview.ResolvedQueryCount > 0 {
		overview.ResolvedQPS = float64(overview.ResolvedQueryCount) / float64(windowSeconds)
		overview.ResolvedAverageDurationMs = overview.ResolvedAverageDurationMs / float64(overview.ResolvedQueryCount)
	}
}

func isAuditCacheHit(status string) bool {
	return status == AuditCacheHit || status == AuditCacheLazy
}

func isAuditErrorCode(code string) bool {
	switch code {
	case "", "NOERROR", "NXDOMAIN":
		return false
	case "NO_RESPONSE":
		return true
	default:
		return true
	}
}

func isAuditResolvedCode(code string) bool {
	switch code {
	case "NOERROR", "NXDOMAIN":
		return true
	default:
		return false
	}
}
