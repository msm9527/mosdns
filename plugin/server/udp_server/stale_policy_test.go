package udp_server

import (
	"testing"

	"github.com/miekg/dns"
)

func TestCapFastStaleTTLIncludesAllRecordsAndPreservesOPT(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("stale-sections.example.", dns.TypeA)
	r := new(dns.Msg)
	r.SetReply(q)
	r.Compress = true
	r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 1}, A: []byte{1, 2, 3, 4}}}
	r.Ns = []dns.RR{&dns.NS{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeNS, Class: dns.ClassINET, Ttl: 30}, Ns: "ns.example."}}
	opt := &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}}
	opt.SetUDPSize(1232)
	opt.SetDo()
	opt.SetExtendedRcode(dns.RcodeBadCookie)
	r.Extra = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "ns.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 600}, A: []byte{2, 3, 4, 5}}, opt}
	wire, err := r.Pack()
	if err != nil {
		t.Fatal(err)
	}
	capFastStaleTTL(wire)
	var capped dns.Msg
	if err := capped.Unpack(wire); err != nil {
		t.Fatal(err)
	}
	if capped.Answer[0].Header().Ttl != 1 || capped.Ns[0].Header().Ttl != 5 || capped.Extra[0].Header().Ttl != 5 {
		t.Fatalf("stale TTL policy changed: %v / %v / %v", capped.Answer, capped.Ns, capped.Extra)
	}
	if got := capped.IsEdns0(); got == nil || got.Hdr.Ttl != opt.Hdr.Ttl || got.UDPSize() != 1232 {
		t.Fatalf("stale policy changed OPT flags: %v", got)
	}
	// Truncated packets can appear on malformed-input paths; scans must be bounded.
	for n := 0; n < len(wire); n++ {
		capFastStaleTTL(append([]byte(nil), wire[:n]...))
	}
}
