package coremain

import (
	"sort"
	"time"
)

type UpstreamHealthSnapshot struct {
	PluginTag           string  `json:"plugin_tag"`
	PluginType          string  `json:"plugin_type"`
	UpstreamTag         string  `json:"upstream_tag"`
	Address             string  `json:"address"`
	Score               int64   `json:"score"`
	AverageLatencyMs    float64 `json:"average_latency_ms"`
	ObservedAverageMs   float64 `json:"observed_average_latency_ms"`
	QueryTotal          uint64  `json:"query_total"`
	ErrorTotal          uint64  `json:"error_total"`
	WinnerTotal         uint64  `json:"winner_total"`
	Inflight            int64   `json:"inflight"`
	ConsecutiveFailures uint32  `json:"consecutive_failures"`
	Healthy             bool    `json:"healthy"`
	UnhealthyUntilMs    int64   `json:"unhealthy_until_unix_ms,omitempty"`
}

type UpstreamHealthProvider interface {
	SnapshotUpstreamHealth() []UpstreamHealthSnapshot
}

type upstreamHealthOverview struct {
	Total      int                      `json:"total"`
	Healthy    int                      `json:"healthy"`
	Degraded   int                      `json:"degraded"`
	Unhealthy  int                      `json:"unhealthy"`
	WorstScore int64                    `json:"worst_score"`
	Items      []UpstreamHealthSnapshot `json:"items,omitempty"`
}

func collectUpstreamHealth(m *Mosdns) upstreamHealthOverview {
	if m == nil {
		return upstreamHealthOverview{}
	}

	items := make([]UpstreamHealthSnapshot, 0)
	for tag, plugin := range m.plugins {
		provider, ok := plugin.(UpstreamHealthProvider)
		if !ok || provider == nil {
			continue
		}
		snapshots := provider.SnapshotUpstreamHealth()
		for i := range snapshots {
			if snapshots[i].PluginTag == "" {
				snapshots[i].PluginTag = tag
			}
		}
		items = append(items, snapshots...)
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Score == items[j].Score {
			if items[i].PluginTag == items[j].PluginTag {
				return items[i].UpstreamTag < items[j].UpstreamTag
			}
			return items[i].PluginTag < items[j].PluginTag
		}
		return items[i].Score > items[j].Score
	})

	overview := upstreamHealthOverview{Items: items, Total: len(items)}
	for _, item := range items {
		if item.Score > overview.WorstScore {
			overview.WorstScore = item.Score
		}
		switch {
		case !item.Healthy:
			overview.Unhealthy++
		case item.ConsecutiveFailures > 0 || item.Inflight > 0:
			overview.Degraded++
		default:
			overview.Healthy++
		}
	}
	return overview
}

func UnhealthyUntilUnixMilli(unixNano int64) int64 {
	if unixNano <= 0 {
		return 0
	}
	return time.Unix(0, unixNano).UnixMilli()
}

func AverageLatencyMsFromTotals(totalLatencyUs, sampleCount uint64) float64 {
	if sampleCount == 0 {
		return 0
	}
	return float64(totalLatencyUs) / float64(sampleCount) / 1000.0
}
