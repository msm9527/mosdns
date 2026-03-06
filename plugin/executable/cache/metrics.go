package cache

import "github.com/prometheus/client_golang/prometheus"

func (c *Cache) initMetrics(lb map[string]string) {
	c.queryTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "query_total",
		Help:        "The total number of processed queries",
		ConstLabels: lb,
	})
	c.hitTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "hit_total",
		Help:        "The total number of queries that hit the cache",
		ConstLabels: lb,
	})
	c.lazyHitTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "lazy_hit_total",
		Help:        "The total number of queries that hit the expired cache",
		ConstLabels: lb,
	})
	c.l1HitTotalMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "l1_hit_total",
		Help:        "The total number of queries that hit the L1 cache",
		ConstLabels: lb,
	})
	c.l2HitTotalMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "l2_hit_total",
		Help:        "The total number of queries that hit the L2 cache",
		ConstLabels: lb,
	})
	c.lazyUpdateTotalMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "lazy_update_total",
		Help:        "The total number of lazy update attempts",
		ConstLabels: lb,
	})
	c.lazyUpdateDroppedMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "lazy_update_drop_total",
		Help:        "The total number of dropped lazy updates due to concurrency guard",
		ConstLabels: lb,
	})
	c.dumpTotalCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "dump_total",
		Help:        "The total number of snapshot dumps",
		ConstLabels: lb,
	})
	c.dumpErrorCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "dump_error_total",
		Help:        "The total number of snapshot dump errors",
		ConstLabels: lb,
	})
	c.loadTotalCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "load_total",
		Help:        "The total number of snapshot loads",
		ConstLabels: lb,
	})
	c.loadErrorCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "load_error_total",
		Help:        "The total number of snapshot load errors",
		ConstLabels: lb,
	})
	c.walAppendCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "wal_append_total",
		Help:        "The total number of WAL append operations",
		ConstLabels: lb,
	})
	c.walAppendErrorCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "wal_append_error_total",
		Help:        "The total number of WAL append errors",
		ConstLabels: lb,
	})
	c.walReplayCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "wal_replay_total",
		Help:        "The total number of WAL replay operations",
		ConstLabels: lb,
	})
	c.walReplayErrorCounter = prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "wal_replay_error_total",
		Help:        "The total number of WAL replay errors",
		ConstLabels: lb,
	})
	c.dumpDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:        "dump_duration_seconds",
		Help:        "Duration of snapshot dumps in seconds",
		ConstLabels: lb,
		Buckets:     prometheus.DefBuckets,
	})
	c.loadDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:        "load_duration_seconds",
		Help:        "Duration of snapshot loads in seconds",
		ConstLabels: lb,
		Buckets:     prometheus.DefBuckets,
	})
	c.walReplayDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:        "wal_replay_duration_seconds",
		Help:        "Duration of WAL replay in seconds",
		ConstLabels: lb,
		Buckets:     prometheus.DefBuckets,
	})
	c.size = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "size_current",
		Help:        "Current cache size in records",
		ConstLabels: lb,
	}, func() float64 {
		return float64(c.backend.Len())
	})
	c.l1SizeMetric = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "l1_size_current",
		Help:        "Current L1 cache size in records",
		ConstLabels: lb,
	}, func() float64 {
		return float64(c.l1Len())
	})
}
