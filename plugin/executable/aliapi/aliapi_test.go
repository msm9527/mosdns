package aliapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

func newTestWrapper(u *fakeUpstream, addr string) *upstreamWrapper {
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

func TestAliAPI_TransportFailureReturnsSyntheticServfail(t *testing.T) {
	bad := &fakeUpstream{errs: []error{context.DeadlineExceeded}}
	f := &AliAPI{
		args:     &Args{Concurrent: 1, FailureSuppressTTL: 10, UpstreamFailureThreshold: 3, UpstreamCircuitBreakSeconds: 60},
		logger:   zap.NewNop(),
		us:       []*upstreamWrapper{newTestWrapper(bad, "test_bad_timeout")},
		failures: make(map[string]failureRecord),
	}

	q := new(dns.Msg)
	q.SetQuestion("timeout.example.", dns.TypeA)
	qCtx := testAliAPIContext(q)

	r, err := f.exchange(context.Background(), qCtx, f.us)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rcode != dns.RcodeServerFailure {
		t.Fatalf("unexpected rcode %d", r.Rcode)
	}
	if bad.calls != 1 {
		t.Fatalf("unexpected upstream calls %d", bad.calls)
	}

	r, err = f.exchange(context.Background(), qCtx, f.us)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rcode != dns.RcodeServerFailure {
		t.Fatalf("unexpected cached failure rcode %d", r.Rcode)
	}
	if bad.calls != 1 {
		t.Fatalf("expected suppressed repeat query, got calls=%d", bad.calls)
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
		args:   &Args{Concurrent: 2, FailureSuppressTTL: 10, UpstreamFailureThreshold: 2, UpstreamCircuitBreakSeconds: 60},
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

	r, err := f.exchange(context.Background(), qCtx, f.us)
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

func testAliAPIContext(q *dns.Msg) *query_context.Context {
	q.Id = 1
	return query_context.NewContext(q)
}
