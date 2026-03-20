package aliapi

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/upstream"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

func newTestWrapper(u upstream.Upstream, addr string) *upstreamWrapper {
	return &upstreamWrapper{
		u:                 u,
		cfg:               UpstreamConfig{Addr: addr},
		mQueryTotal:       prometheus.NewCounter(prometheus.CounterOpts{Name: addr + "_query_total", Help: "test"}),
		mErrorTotal:       prometheus.NewCounter(prometheus.CounterOpts{Name: addr + "_error_total", Help: "test"}),
		mWinnerTotal:      prometheus.NewCounter(prometheus.CounterOpts{Name: addr + "_winner_total", Help: "test"}),
		mInflight:         prometheus.NewGauge(prometheus.GaugeOpts{Name: addr + "_inflight", Help: "test"}),
		mResponseLatency:  prometheus.NewHistogram(prometheus.HistogramOpts{Name: addr + "_latency", Help: "test"}),
		mConnOpened:       prometheus.NewCounter(prometheus.CounterOpts{Name: addr + "_conn_open_total", Help: "test"}),
		mConnClosed:       prometheus.NewCounter(prometheus.CounterOpts{Name: addr + "_conn_close_total", Help: "test"}),
		mCircuitOpenTotal: prometheus.NewCounter(prometheus.CounterOpts{Name: addr + "_circuit_open_total", Help: "test"}),
		mCircuitSkipTotal: prometheus.NewCounter(prometheus.CounterOpts{Name: addr + "_circuit_skip_total", Help: "test"}),
	}
}

type fakeUpstream struct {
	responses []*dns.Msg
	errs      []error
	calls     int
}

func (f *fakeUpstream) ExchangeContext(ctx context.Context, m []byte) (*[]byte, error) {
	idx := f.calls
	f.calls++
	if idx < len(f.errs) && f.errs[idx] != nil {
		return nil, f.errs[idx]
	}
	if idx < len(f.responses) && f.responses[idx] != nil {
		packed, err := pool.PackBuffer(f.responses[idx])
		if err != nil {
			return nil, err
		}
		return packed, nil
	}
	return nil, errors.New("no fake response")
}

func (f *fakeUpstream) Close() error { return nil }

type repeatUpstream struct {
	resp *dns.Msg
	err  error
}

func (r *repeatUpstream) ExchangeContext(ctx context.Context, m []byte) (*[]byte, error) {
	if r.err != nil {
		return nil, r.err
	}
	packed, err := pool.PackBuffer(r.resp)
	if err != nil {
		return nil, err
	}
	return packed, nil
}

func (r *repeatUpstream) Close() error { return nil }

type cancelOnContextUpstream struct {
	started chan struct{}
}

func (u *cancelOnContextUpstream) ExchangeContext(ctx context.Context, m []byte) (*[]byte, error) {
	if u.started != nil {
		close(u.started)
	}
	<-ctx.Done()
	return nil, context.Cause(ctx)
}

func (u *cancelOnContextUpstream) Close() error { return nil }

func TestAliAPI_TransportFailureReturnsSyntheticServfail(t *testing.T) {
	bad := &fakeUpstream{errs: []error{context.DeadlineExceeded}}
	f := &AliAPI{
		args:     &Args{Concurrent: 1, FailureSuppressTTL: 10, PersistentServfailThreshold: 3, PersistentServfailTTL: 60, UpstreamFailureThreshold: 3, UpstreamCircuitBreakSeconds: 60},
		logger:   zap.NewNop(),
		us:       []*upstreamWrapper{newTestWrapper(bad, "test_bad_timeout")},
		failures: make(map[string]failureRecord),
	}

	q := new(dns.Msg)
	q.SetQuestion("timeout.example.", dns.TypeA)
	qCtx := testAliAPIContext(q)

	r, err := f.exchange(context.Background(), qCtx, f.args, f.us)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rcode != dns.RcodeServerFailure {
		t.Fatalf("unexpected rcode %d", r.Rcode)
	}
	if bad.calls != 1 {
		t.Fatalf("unexpected upstream calls %d", bad.calls)
	}
	if got := f.us[0].errorCount.Load(); got != 1 {
		t.Fatalf("expected one recorded upstream error, got %d", got)
	}

	r, err = f.exchange(context.Background(), qCtx, f.args, f.us)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rcode != dns.RcodeServerFailure {
		t.Fatalf("unexpected cached failure rcode %d", r.Rcode)
	}
	if bad.calls != 1 {
		t.Fatalf("expected suppressed repeat query, got calls=%d", bad.calls)
	}
	if got := f.us[0].errorCount.Load(); got != 1 {
		t.Fatalf("expected suppressed query to avoid new upstream errors, got %d", got)
	}
}

func TestAliAPIUpstreamWrapperCanceledDoesNotIncrementErrorTotal(t *testing.T) {
	u := &cancelOnContextUpstream{started: make(chan struct{})}
	w := newTestWrapper(u, "test_cancel_not_error")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := w.ExchangeContext(ctx, []byte{1})
		errCh <- err
	}()

	<-u.started
	cancel()

	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if got := w.errorCount.Load(); got != 0 {
		t.Fatalf("expected canceled exchange to avoid error_total, got %d", got)
	}
}

func TestUpstreamWrapperCircuitBreaker(t *testing.T) {
	w := newTestWrapper(&fakeUpstream{}, "test_circuit_state")
	now := time.Now()
	w.recordFailure(now, 2, time.Minute)
	if w.isCircuitOpen(now) {
		t.Fatal("circuit opened too early")
	}
	w.recordFailure(now, 2, time.Minute)
	if !w.isCircuitOpen(now) {
		t.Fatal("expected circuit to be open after threshold")
	}
	w.recordSuccess()
	if w.consecutiveFailures.Load() != 0 {
		t.Fatal("expected consecutive failures to reset")
	}
}

func TestAliAPI_ExchangeSkipsOpenCircuitUpstream(t *testing.T) {
	bad := &fakeUpstream{errs: []error{context.DeadlineExceeded}}
	goodResp := new(dns.Msg)
	query := new(dns.Msg)
	query.SetQuestion("healthy.example.", dns.TypeA)
	goodResp.SetReply(query)
	goodResp.Answer = append(goodResp.Answer, &dns.A{Hdr: dns.RR_Header{Name: "healthy.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: []byte{1, 1, 1, 1}})
	good := &fakeUpstream{responses: []*dns.Msg{goodResp}}
	badWrapper := newTestWrapper(bad, "test_bad_unhealthy")
	badWrapper.circuitOpenUntil.Store(time.Now().Add(time.Minute).UnixNano())

	f := &AliAPI{
		args:   &Args{Concurrent: 2, FailureSuppressTTL: 10, PersistentServfailThreshold: 3, PersistentServfailTTL: 60, UpstreamFailureThreshold: 2, UpstreamCircuitBreakSeconds: 60},
		logger: zap.NewNop(),
		us: []*upstreamWrapper{
			badWrapper,
			newTestWrapper(good, "test_good_healthy"),
		},
		failures: make(map[string]failureRecord),
	}

	q := new(dns.Msg)
	q.SetQuestion("healthy.example.", dns.TypeA)
	qCtx := testAliAPIContext(q)

	r, err := f.exchange(context.Background(), qCtx, f.args, f.us)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rcode != dns.RcodeSuccess {
		t.Fatalf("unexpected rcode %d", r.Rcode)
	}
	if bad.calls != 0 {
		t.Fatalf("expected open-circuit upstream to be skipped, got %d calls", bad.calls)
	}
	if good.calls != 1 {
		t.Fatalf("expected healthy upstream to continue serving, got %d calls", good.calls)
	}
}

func TestAliAPI_PersistentServfailExtendsSuppressWindow(t *testing.T) {
	f := &AliAPI{
		args: &Args{
			FailureSuppressTTL:          10,
			PersistentServfailThreshold: 3,
			PersistentServfailTTL:       60,
		},
		failures: make(map[string]failureRecord),
	}

	key := buildFailureKey(dns.Question{Name: "114menhu.com.", Qclass: dns.ClassINET, Qtype: dns.TypeA})
	for i := 0; i < 3; i++ {
		f.putFailure(key, dns.RcodeServerFailure, f.args)
	}

	rec, ok := f.getFailure(key, f.args)
	if !ok {
		t.Fatal("expected persistent servfail suppression to be active")
	}
	if rec.hits != 3 {
		t.Fatalf("unexpected hit count %d", rec.hits)
	}
	remaining := time.Until(rec.expiresAt)
	if remaining < 55*time.Second {
		t.Fatalf("expected extended suppress ttl, got %s", remaining)
	}
}

func TestAliAPI_PersistentServfailAccumulatesAcrossShortExpiredWindows(t *testing.T) {
	f := &AliAPI{
		args: &Args{
			FailureSuppressTTL:          1,
			PersistentServfailThreshold: 3,
			PersistentServfailTTL:       10,
		},
		failures: make(map[string]failureRecord),
	}

	key := buildFailureKey(dns.Question{Name: "114menhu.com.", Qclass: dns.ClassINET, Qtype: dns.TypeA})
	for i := 0; i < 3; i++ {
		f.putFailure(key, dns.RcodeServerFailure, f.args)
		f.failureMu.Lock()
		rec := f.failures[key]
		rec.expiresAt = time.Now().Add(-time.Millisecond)
		rec.lastSeen = time.Now().Add(time.Duration(-2*(2-i)) * time.Second)
		f.failures[key] = rec
		f.failureMu.Unlock()
	}

	rec, ok := f.getFailure(key, f.args)
	if ok {
		t.Fatal("expected expired record to require a fresh lookup before promotion is observed")
	}

	f.putFailure(key, dns.RcodeServerFailure, f.args)
	rec, ok = f.getFailure(key, f.args)
	if !ok {
		t.Fatal("expected record to be active after fresh promotion")
	}
	if rec.hits < 3 {
		t.Fatalf("expected accumulated hotspot hits, got %d", rec.hits)
	}
	if remaining := time.Until(rec.expiresAt); remaining < 9*time.Second {
		t.Fatalf("expected persistent suppression after accumulation, got %s", remaining)
	}
}

func TestSnapshotUpstreamHealthIncludesObservedStats(t *testing.T) {
	wrapper := newTestWrapper(&fakeUpstream{}, "stats_test")
	wrapper.cfg.Tag = "u1"
	wrapper.queryCount.Store(30)
	wrapper.errorCount.Store(3)
	wrapper.winnerCount.Store(20)
	wrapper.latencyTotalUs.Store(90000)
	wrapper.latencyCount.Store(15)
	wrapper.ewmaLatencyUs.Store(12000)

	f := &AliAPI{
		pluginTag: "ali",
		us:        []*upstreamWrapper{wrapper},
	}

	items := f.SnapshotUpstreamHealth()
	if len(items) != 1 {
		t.Fatalf("expected one item, got %d", len(items))
	}
	item := items[0]
	if item.QueryTotal != 30 || item.ErrorTotal != 3 || item.WinnerTotal != 20 {
		t.Fatalf("unexpected counters: %+v", item)
	}
	if item.ObservedAverageMs != 6 {
		t.Fatalf("unexpected observed average latency: %+v", item)
	}
	if item.AverageLatencyMs != 12 {
		t.Fatalf("unexpected health latency: %+v", item)
	}
}

func TestAliAPIPersistentStatsRestoreResetAndFlush(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control.db")
	if err := coremain.SaveUpstreamRuntimeStats(dbPath, []coremain.UpstreamRuntimeStats{
		{PluginTag: "ali", UpstreamTag: "u1", QueryTotal: 15, ErrorTotal: 1, WinnerTotal: 10, LatencyTotalUs: 75000, LatencyCount: 15},
	}); err != nil {
		t.Fatalf("SaveUpstreamRuntimeStats: %v", err)
	}

	wrapper := newTestWrapper(&fakeUpstream{}, "persist_stats")
	wrapper.cfg.Tag = "u1"
	f := &AliAPI{
		logger:        zap.NewNop(),
		pluginTag:     "ali",
		controlDBPath: dbPath,
		us:            []*upstreamWrapper{wrapper},
	}

	if err := f.restorePersistentStats(f.us); err != nil {
		t.Fatalf("restorePersistentStats: %v", err)
	}
	item := f.SnapshotUpstreamHealth()[0]
	if item.QueryTotal != 15 || item.ErrorTotal != 1 || item.WinnerTotal != 10 || item.ObservedAverageMs != 5 {
		t.Fatalf("unexpected restored stats: %+v", item)
	}

	count, err := f.ResetUpstreamStats(context.Background(), "u1")
	if err != nil {
		t.Fatalf("ResetUpstreamStats: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one reset item, got %d", count)
	}
	if err := f.flushPersistentStats(); err != nil {
		t.Fatalf("flushPersistentStats: %v", err)
	}

	values, err := coremain.LoadUpstreamRuntimeStatsByPlugin(dbPath, "ali")
	if err != nil {
		t.Fatalf("LoadUpstreamRuntimeStatsByPlugin: %v", err)
	}
	if len(values) != 1 || values["u1"].QueryTotal != 0 || values["u1"].LatencyCount != 0 {
		t.Fatalf("unexpected flushed stats: %+v", values)
	}
}

func testAliAPIContext(q *dns.Msg) *query_context.Context {
	q.Id = 1
	return query_context.NewContext(q)
}

func benchmarkAliAPIInstance(concurrent int, upstreams ...*upstreamWrapper) *AliAPI {
	return &AliAPI{
		args: &Args{
			Concurrent:                  concurrent,
			FailureSuppressTTL:          10,
			PersistentServfailThreshold: 3,
			PersistentServfailTTL:       60,
			UpstreamFailureThreshold:    3,
			UpstreamCircuitBreakSeconds: 60,
		},
		logger:   zap.NewNop(),
		us:       upstreams,
		failures: make(map[string]failureRecord),
	}
}

func BenchmarkAliAPIExchangeSingleSuccess(b *testing.B) {
	query := new(dns.Msg)
	query.SetQuestion("bench.example.", dns.TypeA)
	resp := new(dns.Msg)
	resp.SetReply(query)
	resp.Answer = append(resp.Answer, &dns.A{Hdr: dns.RR_Header{Name: "bench.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: []byte{1, 1, 1, 1}})
	u := &repeatUpstream{resp: resp}
	f := benchmarkAliAPIInstance(1, newTestWrapper(u, "bench_success"))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := new(dns.Msg)
		q.SetQuestion("bench.example.", dns.TypeA)
		qCtx := testAliAPIContext(q)
		r, err := f.exchange(context.Background(), qCtx, f.args, f.us)
		if err != nil {
			b.Fatal(err)
		}
		if r.Rcode != dns.RcodeSuccess {
			b.Fatalf("unexpected rcode %d", r.Rcode)
		}
	}
}

func BenchmarkAliAPIExchangeSuppressedFailure(b *testing.B) {
	query := new(dns.Msg)
	query.SetQuestion("fail.example.", dns.TypeA)
	resp := new(dns.Msg)
	resp.SetReply(query)
	f := benchmarkAliAPIInstance(1, newTestWrapper(&repeatUpstream{resp: resp}, "bench_suppressed"))
	key := buildFailureKey(query.Question[0])
	f.putFailure(key, dns.RcodeServerFailure, f.args)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := new(dns.Msg)
		q.SetQuestion("fail.example.", dns.TypeA)
		qCtx := testAliAPIContext(q)
		r, err := f.exchange(context.Background(), qCtx, f.args, f.us)
		if err != nil {
			b.Fatal(err)
		}
		if r.Rcode != dns.RcodeServerFailure {
			b.Fatalf("unexpected rcode %d", r.Rcode)
		}
	}
}

func BenchmarkAliAPIExchangeDualUpstreamFirstHit(b *testing.B) {
	query := new(dns.Msg)
	query.SetQuestion("dual.example.", dns.TypeA)
	resp := new(dns.Msg)
	resp.SetReply(query)
	resp.Answer = append(resp.Answer, &dns.A{Hdr: dns.RR_Header{Name: "dual.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: []byte{8, 8, 8, 8}})

	u1 := &repeatUpstream{resp: resp}
	u2 := &repeatUpstream{resp: resp}
	f := benchmarkAliAPIInstance(2,
		newTestWrapper(u1, "bench_dual_1"),
		newTestWrapper(u2, "bench_dual_2"),
	)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := new(dns.Msg)
		q.SetQuestion("dual.example.", dns.TypeA)
		qCtx := testAliAPIContext(q)
		r, err := f.exchange(context.Background(), qCtx, f.args, f.us)
		if err != nil {
			b.Fatal(err)
		}
		if r.Rcode != dns.RcodeSuccess {
			b.Fatalf("unexpected rcode %d", r.Rcode)
		}
	}
}
