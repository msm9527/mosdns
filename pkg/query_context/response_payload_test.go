package query_context

import (
	"testing"

	"github.com/miekg/dns"
)

func TestResponseMsgParsesPayloadOnce(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("payload.example.", dns.TypeA)
	resp := new(dns.Msg)
	resp.SetReply(q)
	resp.Answer = append(resp.Answer, &dns.A{
		Hdr: dns.RR_Header{
			Name:   "payload.example.",
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    60,
		},
		A: []byte{1, 2, 3, 4},
	})
	wire, err := resp.Pack()
	if err != nil {
		t.Fatal(err)
	}

	qCtx := NewContext(q)
	qCtx.SetResponsePayload(&ResponsePayload{Wire: wire})

	first := qCtx.ResponseMsg()
	if first == nil || len(first.Answer) != 1 {
		t.Fatalf("unexpected parsed response: %+v", first)
	}
	second := qCtx.ResponseMsg()
	if first != second {
		t.Fatal("expected payload parser to cache parsed message")
	}
}
