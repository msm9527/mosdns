package e2e_test

import (
	"fmt"
	"sort"
	"strings"
	"time"

	coremain "github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/miekg/dns"
)

const (
	serviceE2ELoadWorkers    = 8
	serviceE2ELoadIterations = 25
)

type serviceE2ELoadStats struct {
	Workers     int
	Iterations  int
	Total       int
	Successes   int
	Failures    int
	Duration    time.Duration
	AvgLatency  time.Duration
	P95Latency  time.Duration
	MaxLatency  time.Duration
	MinLatency  time.Duration
	QueriesPerS float64
}

func recordDNSCheck(
	rec *e2eCaseRecorder,
	name, network, listener, domainName string,
	qtype uint16,
	resp *dns.Msg,
) {
	rec.AddCheck(name, formatDNSCheck(network, listener, domainName, qtype, resp))
}

func formatDNSCheck(network, listener, domainName string, qtype uint16, resp *dns.Msg) string {
	return fmt.Sprintf(
		"%s %s %s %s -> rcode=%s answers=%s",
		strings.ToUpper(network),
		listener,
		domainName,
		dns.TypeToString[qtype],
		dns.RcodeToString[resp.Rcode],
		formatDNSAnswers(resp),
	)
}

func formatDNSAnswers(resp *dns.Msg) string {
	if resp == nil || len(resp.Answer) == 0 {
		return "[]"
	}
	items := make([]string, 0, len(resp.Answer))
	for _, answer := range resp.Answer {
		items = append(items, answer.String())
	}
	return "[" + strings.Join(items, "; ") + "]"
}

func recordCacheMetric(rec *e2eCaseRecorder, item coremain.CacheStatsSnapshot) {
	rec.AddMetric(
		item.Tag,
		fmt.Sprintf("hit=%d/%d", item.Counters["hit_total"], item.Counters["query_total"]),
		fmt.Sprintf("entries=%d misses=%d", item.Counters["entry_count"], item.Counters["miss_total"]),
	)
}

func summarizeServiceE2ELoadStats(
	workers int,
	iterations int,
	latencies []time.Duration,
	failures int,
	totalDuration time.Duration,
) serviceE2ELoadStats {
	stats := serviceE2ELoadStats{
		Workers:    workers,
		Iterations: iterations,
		Total:      workers * iterations,
		Failures:   failures,
		Duration:   totalDuration,
	}
	stats.Successes = len(latencies)
	if len(latencies) == 0 {
		return stats
	}

	sorted := append([]time.Duration(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i] < sorted[j]
	})
	stats.MinLatency = sorted[0]
	stats.MaxLatency = sorted[len(sorted)-1]
	stats.P95Latency = sorted[(len(sorted)*95-1)/100]
	stats.AvgLatency = averageServiceE2ELatency(sorted)
	if totalDuration > 0 {
		stats.QueriesPerS = float64(stats.Successes) / totalDuration.Seconds()
	}
	return stats
}

func averageServiceE2ELatency(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return total / time.Duration(len(values))
}
