package fallback

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

func TestFallbackTreatsErrExitWithResponseAsSuccess(t *testing.T) {
	primary := sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
		setFallbackTestA(qCtx, net.IPv4(1, 1, 1, 1))
		return sequence.ErrExit
	})
	secondaryCalled := false
	secondary := sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
		secondaryCalled = true
		setFallbackTestA(qCtx, net.IPv4(2, 2, 2, 2))
		return nil
	})
	f := &fallback{
		logger:               zap.NewNop(),
		primary:              primary,
		secondary:            secondary,
		fastFallbackDuration: time.Second,
	}

	q := new(dns.Msg)
	q.SetQuestion("fallback-cache-hit.example.", dns.TypeA)
	qCtx := query_context.NewContext(q)
	if err := f.Exec(context.Background(), qCtx); err != nil {
		t.Fatalf("fallback Exec: %v", err)
	}
	if secondaryCalled {
		t.Fatal("secondary should not run when primary returns ErrExit with response")
	}
	resp := qCtx.R()
	if resp == nil || len(resp.Answer) != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok || !a.A.Equal(net.IPv4(1, 1, 1, 1)) {
		t.Fatalf("unexpected answer: %+v", resp.Answer)
	}
}

func setFallbackTestA(qCtx *query_context.Context, ip net.IP) {
	q := qCtx.Q()
	resp := new(dns.Msg)
	resp.SetReply(q)
	resp.Answer = append(resp.Answer, &dns.A{
		Hdr: dns.RR_Header{
			Name:   q.Question[0].Name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    60,
		},
		A: ip.To4(),
	})
	qCtx.SetResponse(resp)
}
