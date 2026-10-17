package udp_server

import (
	"net/netip"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func ednsHitFixture(tb testing.TB, storedOPT, requestOPT bool) func() bool {
	tb.Helper()
	const name = "edns-bench.example."
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	q.Id = 1234
	if requestOPT {
		q.SetEdns0(1232, false)
	}
	var r dns.Msg
	if err := r.Unpack(makeAnswerNoTest(name, dns.TypeA, 4321, 60)); err != nil {
		tb.Fatal(err)
	}
	if storedOPT {
		r.SetEdns0(1200, false)
	}
	fc := refreshTestCache()
	if !fc.Store(name, dns.TypeA, mustPackNoTest(&r), "", false) {
		tb.Fatal("failed to seed fast EDNS benchmark")
	}
	manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"udp_fast_path": testSwitchPlugin{value: "on"}})
	bypass := buildFastBypassWithSourceRuleMatch(coremain.NewBP("edns-bench", manager), fc, nil, 0, server.RequestSourceUser)
	req, buf := mustPackNoTest(q), make([]byte, 4096)
	addr := netip.MustParseAddrPort("127.0.0.1:5300")
	hit := func() bool {
		copy(buf, req)
		action, length, _, _, _, _, _ := bypass(len(req), buf, addr)
		return action == server.FastActionReply && length > 0 && (buf[11] == 1) == requestOPT
	}
	if !hit() {
		tb.Fatal("EDNS fixture did not hit compatible cache response")
	}
	return hit
}

func ednsScenario(storedOPT, requestOPT bool) string {
	name := "plain-to-"
	if storedOPT {
		name = "edns-to-"
	}
	if requestOPT {
		return name + "edns"
	}
	return name + "plain"
}

func TestFastEDNSResponseHitDoesNotAllocate(t *testing.T) {
	for _, storedOPT := range []bool{false, true} {
		for _, requestOPT := range []bool{false, true} {
			t.Run(ednsScenario(storedOPT, requestOPT), func(t *testing.T) {
				hit := ednsHitFixture(t, storedOPT, requestOPT)
				allocations := testing.AllocsPerRun(1000, func() {
					if !hit() {
						t.Fatal("fast hit disappeared")
					}
				})
				if allocations != 0 {
					t.Fatalf("ordinary/empty EDNS hit allocated %g times", allocations)
				}
			})
		}
	}
}

func BenchmarkFastEDNSResponseHit(b *testing.B) {
	for _, storedOPT := range []bool{false, true} {
		for _, requestOPT := range []bool{false, true} {
			b.Run(ednsScenario(storedOPT, requestOPT), func(b *testing.B) {
				hit := ednsHitFixture(b, storedOPT, requestOPT)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if !hit() {
						b.Fatal("fast hit disappeared")
					}
				}
			})
		}
	}
}
