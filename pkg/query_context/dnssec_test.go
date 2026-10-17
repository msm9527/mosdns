package query_context

import (
	"testing"

	"github.com/miekg/dns"
)

func TestNewContextPreservesDNSSECDemandWithoutForwardingOtherEDNSOptions(t *testing.T) {
	for _, kind := range []string{"no-edns", "edns", "dnssec"} {
		t.Run(kind, func(t *testing.T) {
			q := new(dns.Msg)
			q.SetQuestion("dnssec-context.example.", dns.TypeA)
			var clientOpt *dns.OPT
			if kind != "no-edns" {
				q.SetEdns0(4096, kind == "dnssec")
				clientOpt = q.IsEdns0()
				clientOpt.Option = []dns.EDNS0{&dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "0102030405060708"}}
			}
			qCtx := NewContext(q)
			wantDO := kind == "dnssec"
			if got := qCtx.QOpt().Do(); got != wantDO {
				t.Fatalf("upstream DO=%v, want %v", got, wantDO)
			}
			if qCtx.ClientOpt() != clientOpt || len(qCtx.QOpt().Option) != 0 || qCtx.QOpt().UDPSize() != edns0Size {
				t.Fatal("OPT normalization changed client options or forwarded unselected options")
			}
			if clientOpt == nil {
				if qCtx.RespOpt() != nil {
					t.Fatal("non-EDNS client unexpectedly received OPT")
				}
			} else if qCtx.RespOpt() == nil || qCtx.RespOpt().Do() != wantDO || len(qCtx.ClientOpt().Option) != 1 {
				t.Fatal("client OPT or response DNSSEC demand was lost")
			}
			if qCtx.Copy().QOpt().Do() != wantDO {
				t.Fatal("copied context lost upstream DNSSEC demand")
			}
		})
	}
}
