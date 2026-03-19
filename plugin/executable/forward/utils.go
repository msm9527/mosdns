/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package fastforward

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/upstream"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap/zapcore"
)

type upstreamWrapper struct {
	idx             int
	u               upstream.Upstream
	cfg             UpstreamConfig
	onStatsChanged  func()
	consecutiveErrs atomic.Uint32
	inflightCount   atomic.Int64
	ewmaLatencyUs   atomic.Int64
	unhealthyUntil  atomic.Int64
	queryCount      atomic.Uint64
	errorCount      atomic.Uint64
	winnerCount     atomic.Uint64
	latencyTotalUs  atomic.Uint64
	latencyCount    atomic.Uint64

	queryTotal      prometheus.Counter
	errTotal        prometheus.Counter
	winnerTotal     prometheus.Counter
	thread          prometheus.Gauge
	responseLatency prometheus.Histogram

	connOpened prometheus.Counter
	connClosed prometheus.Counter
}

func (uw *upstreamWrapper) OnEvent(typ upstream.Event) {
	switch typ {
	case upstream.EventConnOpen:
		uw.connOpened.Inc()
	case upstream.EventConnClose:
		uw.connClosed.Inc()
	}
}

// newWrapper inits all metrics.
// Note: upstreamWrapper.u still needs to be set.
func newWrapper(idx int, cfg UpstreamConfig, pluginTag string) *upstreamWrapper {
	lb := map[string]string{"upstream": cfg.Tag, "tag": pluginTag}
	return &upstreamWrapper{
		cfg: cfg,
		queryTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "query_total",
			Help:        "The total number of queries processed by this upstream",
			ConstLabels: lb,
		}),
		errTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "err_total",
			Help:        "The total number of queries failed",
			ConstLabels: lb,
		}),
		winnerTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "upstream_winner_total",
			Help:        "The total number of times this upstream result was selected as the final response",
			ConstLabels: lb,
		}),
		thread: prometheus.NewGauge(prometheus.GaugeOpts{
			Name:        "thread",
			Help:        "The number of threads (queries) that are currently being processed",
			ConstLabels: lb,
		}),
		responseLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:        "response_latency_millisecond",
			Help:        "The response latency in millisecond",
			Buckets:     []float64{1, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000},
			ConstLabels: lb,
		}),

		connOpened: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "conn_opened_total",
			Help:        "The total number of connections that are opened",
			ConstLabels: lb,
		}),
		connClosed: prometheus.NewCounter(prometheus.CounterOpts{
			Name:        "conn_closed_total",
			Help:        "The total number of connections that are closed",
			ConstLabels: lb,
		}),
	}
}

func (uw *upstreamWrapper) registerMetricsTo(r prometheus.Registerer) error {
	for _, collector := range [...]prometheus.Collector{
		uw.queryTotal,
		uw.errTotal,
		uw.winnerTotal,
		uw.thread,
		uw.responseLatency,
		uw.connOpened,
		uw.connClosed,
	} {
		if err := r.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

// name returns upstream tag if it was set in the config.
// Otherwise, it returns upstream address.
func (uw *upstreamWrapper) name() string {
	if t := uw.cfg.Tag; len(t) > 0 {
		return uw.cfg.Tag
	}
	return uw.cfg.Addr
}

func (uw *upstreamWrapper) ExchangeContext(ctx context.Context, m []byte) (*[]byte, error) {
	uw.queryTotal.Inc()
	uw.queryCount.Add(1)

	start := time.Now()
	uw.thread.Inc()
	uw.inflightCount.Add(1)
	r, err := uw.u.ExchangeContext(ctx, m)
	latency := time.Since(start)
	uw.inflightCount.Add(-1)
	uw.thread.Dec()

	if err != nil {
		uw.errTotal.Inc()
		uw.errorCount.Add(1)
		uw.recordFailure(time.Now())
	} else {
		uw.responseLatency.Observe(float64(latency.Milliseconds()))
		uw.latencyTotalUs.Add(uint64(latency.Microseconds()))
		uw.latencyCount.Add(1)
		uw.recordSuccess(latency)
	}
	uw.notifyStatsChanged()
	return r, err
}

func (uw *upstreamWrapper) recordDecodeFailure(now time.Time) {
	uw.recordFailure(now)
}

func (uw *upstreamWrapper) recordFailure(now time.Time) {
	failures := uw.consecutiveErrs.Add(1)
	if failures < healthFailureThreshold {
		return
	}
	penalty := healthFailureBackoffBase
	for step := uint32(healthFailureThreshold); step < failures && penalty < healthFailureBackoffMax; step++ {
		penalty *= 2
		if penalty >= healthFailureBackoffMax {
			penalty = healthFailureBackoffMax
			break
		}
	}
	uw.unhealthyUntil.Store(now.Add(penalty).UnixNano())
}

func (uw *upstreamWrapper) recordSuccess(latency time.Duration) {
	uw.consecutiveErrs.Store(0)
	uw.unhealthyUntil.Store(0)

	current := latency.Microseconds()
	if current <= 0 {
		current = 1
	}
	previous := uw.ewmaLatencyUs.Load()
	if previous <= 0 {
		uw.ewmaLatencyUs.Store(current)
		return
	}
	weighted := (previous*healthLatencyWeightOld + current*healthLatencyWeightNew) /
		(healthLatencyWeightOld + healthLatencyWeightNew)
	uw.ewmaLatencyUs.Store(weighted)
}

func (uw *upstreamWrapper) isUnhealthy(now time.Time) bool {
	return now.UnixNano() < uw.unhealthyUntil.Load()
}

func (uw *upstreamWrapper) healthScore(now time.Time) int64 {
	score := uw.ewmaLatencyUs.Load()
	if score <= 0 {
		score = defaultHealthLatencyUs
	}
	score += uw.inflightCount.Load() * inflightPenaltyUs
	score += int64(uw.consecutiveErrs.Load()) * failurePenaltyUs
	if uw.isUnhealthy(now) {
		score += unhealthyPenaltyUs
	}
	return score
}

func (uw *upstreamWrapper) Close() error {
	return uw.u.Close()
}

type queryInfo dns.Msg

func (q *queryInfo) MarshalLogObject(encoder zapcore.ObjectEncoder) error {
	if len(q.Question) != 1 {
		encoder.AddBool("odd_question", true)
	} else {
		question := q.Question[0]
		encoder.AddString("qname", question.Name)
		encoder.AddUint16("qtype", question.Qtype)
		encoder.AddUint16("qclass", question.Qclass)
	}
	return nil
}

func copyPayload(b *[]byte) *[]byte {
	bc := pool.GetBuf(len(*b))
	copy(*bc, *b)
	return bc
}
