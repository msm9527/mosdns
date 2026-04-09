package aliapi

import (
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/prometheus/client_golang/prometheus"
)

func (w *upstreamWrapper) recordWinner() {
	w.winnerCount.Add(1)
	w.mWinnerTotal.Inc()
	w.notifyStatsChanged()
}

func (w *upstreamWrapper) notifyStatsChanged() {
	if w != nil && w.onStatsChanged != nil {
		w.onStatsChanged()
	}
}

func (w *upstreamWrapper) applyPersistentStats(stats coremain.UpstreamRuntimeStats) {
	if w == nil {
		return
	}
	w.queryCount.Store(stats.QueryTotal)
	w.errorCount.Store(stats.ErrorTotal)
	w.winnerCount.Store(stats.WinnerTotal)
	w.latencyTotalUs.Store(stats.LatencyTotalUs)
	w.latencyCount.Store(stats.LatencyCount)
	addCounter(w.mQueryTotal, stats.QueryTotal)
	addCounter(w.mErrorTotal, stats.ErrorTotal)
	addCounter(w.mWinnerTotal, stats.WinnerTotal)
}

func (w *upstreamWrapper) snapshotPersistentStats(pluginTag string) (coremain.UpstreamRuntimeStats, bool) {
	if w == nil || w.cfg.Tag == "" || pluginTag == "" {
		return coremain.UpstreamRuntimeStats{}, false
	}
	return coremain.UpstreamRuntimeStats{
		PluginTag:      pluginTag,
		UpstreamTag:    w.cfg.Tag,
		QueryTotal:     w.queryCount.Load(),
		ErrorTotal:     w.errorCount.Load(),
		WinnerTotal:    w.winnerCount.Load(),
		LatencyTotalUs: w.latencyTotalUs.Load(),
		LatencyCount:   w.latencyCount.Load(),
	}, true
}

func (w *upstreamWrapper) resetPersistentStats() bool {
	if w == nil || w.cfg.Tag == "" {
		return false
	}
	w.queryCount.Store(0)
	w.errorCount.Store(0)
	w.winnerCount.Store(0)
	w.latencyTotalUs.Store(0)
	w.latencyCount.Store(0)
	w.notifyStatsChanged()
	return true
}

func addCounter(counter prometheus.Counter, value uint64) {
	if counter != nil && value > 0 {
		counter.Add(float64(value))
	}
}

func (w *upstreamWrapper) snapshotHealth(pluginTag string, now time.Time) coremain.UpstreamHealthSnapshot {
	winnerTotal := w.winnerCount.Load()
	return coremain.UpstreamHealthSnapshot{
		PluginTag:           pluginTag,
		PluginType:          PluginType,
		UpstreamTag:         w.cfg.Tag,
		Address:             w.cfg.Addr,
		Score:               w.healthScore(now),
		AverageLatencyMs:    float64(w.ewmaLatencyUs.Load()) / 1000.0,
		ObservedAverageMs:   coremain.AverageLatencyMsFromTotals(w.latencyTotalUs.Load(), w.latencyCount.Load()),
		QueryTotal:          winnerTotal,
		AttemptTotal:        w.queryCount.Load(),
		ErrorTotal:          w.errorCount.Load(),
		WinnerTotal:         winnerTotal,
		Inflight:            w.mInflightValue.Load(),
		ConsecutiveFailures: w.consecutiveFailures.Load(),
		Healthy:             !w.isCircuitOpen(now),
		UnhealthyUntilMs:    coremain.UnhealthyUntilUnixMilli(w.circuitOpenUntil.Load()),
	}
}
