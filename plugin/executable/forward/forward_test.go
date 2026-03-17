package fastforward

import (
	"context"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

type testUpstream struct {
	delay time.Duration
	resp  *dns.Msg
	err   error
	calls int
}

func (u *testUpstream) ExchangeContext(ctx context.Context, m []byte) (*[]byte, error) {
	u.calls++
	if u.delay > 0 {
		timer := time.NewTimer(u.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
	if u.err != nil {
		return nil, u.err
	}
	packed, err := pool.PackBuffer(u.resp)
	if err != nil {
		return nil, err
	}
	return packed, nil
}

func (u *testUpstream) Close() error { return nil }

func testForwardQuery(name string) *query_context.Context {
	msg := new(dns.Msg)
	msg.SetQuestion(name, dns.TypeA)
	return query_context.NewContext(msg)
}

func testReply(name string, ip []byte) *dns.Msg {
	query := new(dns.Msg)
	query.SetQuestion(name, dns.TypeA)
	reply := new(dns.Msg)
	reply.SetReply(query)
	reply.Answer = append(reply.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   ip,
	})
	return reply
}

func TestNormalizeConcurrentCapsToDistinctUpstreams(t *testing.T) {
	if got := normalizeConcurrent(3, 2); got != 2 {
		t.Fatalf("expected concurrent cap 2, got %d", got)
	}
}

func TestPickUpstreamsPrefersHealthyOnes(t *testing.T) {
	now := time.Now()
	slow := newWrapper(0, UpstreamConfig{Tag: "slow", Addr: "udp://slow"}, "test")
	slow.u = &testUpstream{resp: testReply("slow.example.", []byte{1, 1, 1, 1})}
	slow.ewmaLatencyUs.Store(int64(80 * time.Millisecond / time.Microsecond))

	fast := newWrapper(1, UpstreamConfig{Tag: "fast", Addr: "udp://fast"}, "test")
	fast.u = &testUpstream{resp: testReply("fast.example.", []byte{2, 2, 2, 2})}
	fast.ewmaLatencyUs.Store(int64(10 * time.Millisecond / time.Microsecond))

	unhealthy := newWrapper(2, UpstreamConfig{Tag: "bad", Addr: "udp://bad"}, "test")
	unhealthy.u = &testUpstream{resp: testReply("bad.example.", []byte{3, 3, 3, 3})}
	unhealthy.ewmaLatencyUs.Store(int64(5 * time.Millisecond / time.Microsecond))
	unhealthy.unhealthyUntil.Store(now.Add(time.Minute).UnixNano())

	selected := pickUpstreams([]*upstreamWrapper{unhealthy, slow, fast}, 2, now)
	if len(selected) != 2 {
		t.Fatalf("expected 2 upstreams, got %d", len(selected))
	}
	if selected[0] != fast || selected[1] != slow {
		t.Fatalf("unexpected selection order: %#v", []string{selected[0].cfg.Tag, selected[1].cfg.Tag})
	}
}

func TestForwardExchangeHonorsParentCancellation(t *testing.T) {
	blocking := &testUpstream{delay: time.Second}
	wrapper := newWrapper(0, UpstreamConfig{
		Tag:                  "cancel",
		Addr:                 "udp://cancel",
		UpstreamQueryTimeout: 1000,
	}, "test")
	wrapper.u = blocking

	forward := &Forward{
		args:   &Args{Concurrent: 1},
		logger: zap.NewNop(),
		us:     []*upstreamWrapper{wrapper},
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	start := time.Now()
	_, err := forward.exchange(ctx, testForwardQuery("cancel.example."), forward.args, forward.us)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("exchange did not stop promptly, elapsed=%s", elapsed)
	}
	if blocking.calls != 1 {
		t.Fatalf("expected exactly one upstream call, got %d", blocking.calls)
	}
}
