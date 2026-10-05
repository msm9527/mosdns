package domain_memory_pool

import (
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
)

// The sequence owns classification. The pool checks only response success and
// qtype evidence before accepting a background classification as verified.
func (d *domainMemoryPool) backgroundClassificationVerified(qCtx *query_context.Context, qtype uint16) bool {
	if qCtx.CacheResponseStale() {
		return false
	}
	if payload := qCtx.ResponsePayload(); payload != nil && payload.Stale {
		return false
	}
	resp := qCtx.ResponseMsg()
	if resp == nil {
		return false
	}
	switch d.policy.kind {
	case "nov4", "nodenov4":
		return qtype == dns.TypeA && resp.Rcode == dns.RcodeNameError
	case "nov6", "nodenov6":
		return qtype == dns.TypeAAAA && resp.Rcode == dns.RcodeNameError
	case "realip", "fakeip":
		if resp.Rcode != dns.RcodeSuccess {
			return false
		}
		for _, answer := range resp.Answer {
			switch rr := answer.(type) {
			case *dns.A:
				if qtype == dns.TypeA && rr.A.To4() != nil {
					return true
				}
			case *dns.AAAA:
				if qtype == dns.TypeAAAA && rr.AAAA.To16() != nil && rr.AAAA.To4() == nil {
					return true
				}
			}
		}
	}
	return false
}
