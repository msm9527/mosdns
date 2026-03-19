package fastforward

import (
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/prometheus/client_golang/prometheus"
)

func (uw *upstreamWrapper) recordWinner() {
	uw.winnerCount.Add(1)
	uw.winnerTotal.Inc()
	uw.notifyStatsChanged()
}

func (uw *upstreamWrapper) notifyStatsChanged() {
	if uw != nil && uw.onStatsChanged != nil {
		uw.onStatsChanged()
	}
}

func (uw *upstreamWrapper) applyPersistentStats(stats coremain.UpstreamRuntimeStats) {
	if uw == nil {
		return
	}
	uw.queryCount.Store(stats.QueryTotal)
	uw.errorCount.Store(stats.ErrorTotal)
	uw.winnerCount.Store(stats.WinnerTotal)
	uw.latencyTotalUs.Store(stats.LatencyTotalUs)
	uw.latencyCount.Store(stats.LatencyCount)
	addCounter(uw.queryTotal, stats.QueryTotal)
	addCounter(uw.errTotal, stats.ErrorTotal)
	addCounter(uw.winnerTotal, stats.WinnerTotal)
}

func (uw *upstreamWrapper) snapshotPersistentStats(pluginTag string) (coremain.UpstreamRuntimeStats, bool) {
	if uw == nil || uw.cfg.Tag == "" || pluginTag == "" {
		return coremain.UpstreamRuntimeStats{}, false
	}
	return coremain.UpstreamRuntimeStats{
		PluginTag:      pluginTag,
		UpstreamTag:    uw.cfg.Tag,
		QueryTotal:     uw.queryCount.Load(),
		ErrorTotal:     uw.errorCount.Load(),
		WinnerTotal:    uw.winnerCount.Load(),
		LatencyTotalUs: uw.latencyTotalUs.Load(),
		LatencyCount:   uw.latencyCount.Load(),
	}, true
}

func (uw *upstreamWrapper) resetPersistentStats() bool {
	if uw == nil || uw.cfg.Tag == "" {
		return false
	}
	uw.queryCount.Store(0)
	uw.errorCount.Store(0)
	uw.winnerCount.Store(0)
	uw.latencyTotalUs.Store(0)
	uw.latencyCount.Store(0)
	uw.notifyStatsChanged()
	return true
}

func addCounter(counter prometheus.Counter, value uint64) {
	if counter != nil && value > 0 {
		counter.Add(float64(value))
	}
}

func (uw *upstreamWrapper) snapshotHealth(pluginTag string, now time.Time) coremain.UpstreamHealthSnapshot {
	return coremain.UpstreamHealthSnapshot{
		PluginTag:           pluginTag,
		PluginType:          PluginType,
		UpstreamTag:         uw.cfg.Tag,
		Address:             uw.cfg.Addr,
		Score:               uw.healthScore(now),
		AverageLatencyMs:    float64(uw.ewmaLatencyUs.Load()) / 1000.0,
		ObservedAverageMs:   coremain.AverageLatencyMsFromTotals(uw.latencyTotalUs.Load(), uw.latencyCount.Load()),
		QueryTotal:          uw.queryCount.Load(),
		ErrorTotal:          uw.errorCount.Load(),
		WinnerTotal:         uw.winnerCount.Load(),
		Inflight:            uw.inflightCount.Load(),
		ConsecutiveFailures: uw.consecutiveErrs.Load(),
		Healthy:             !uw.isUnhealthy(now),
		UnhealthyUntilMs:    coremain.UnhealthyUntilUnixMilli(uw.unhealthyUntil.Load()),
	}
}
