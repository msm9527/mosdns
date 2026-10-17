package udp_server

import (
	"encoding/binary"

	"github.com/miekg/dns"
)

const dnsHeaderSize = 12

func shouldStoreFastResponse(payload []byte) bool {
	rcode, ok := responseRcode(payload)
	if !ok || rcode != dns.RcodeSuccess || payload[2]&0x02 != 0 || payload[3]&0x20 != 0 || binary.BigEndian.Uint16(payload[6:8]) == 0 {
		return false
	}
	// Negative lifetimes belong to the shared response cache. Fast-cache
	// internal TTLs and stale policies must never extend NXDOMAIN or NODATA.
	// AD responses also require the original TTL, which client TTL governance
	// may already have changed before this payload reached the fast cache.
	return !hasNegativeFastAuthority(payload) && !fastResponseHasSignatures(payload)
}

func hasNegativeFastAuthority(payload []byte) bool {
	if binary.BigEndian.Uint16(payload[8:10]) == 0 {
		return false
	}
	// CNAME + SOA can be NODATA despite a nonempty answer section. This
	// uncommon store path may unpack; ordinary positive answers only inspect
	// the header, and fast-cache hits do not call this policy.
	var msg dns.Msg
	if err := msg.Unpack(payload); err != nil || len(msg.Question) == 0 {
		return true
	}
	for _, rr := range msg.Ns {
		if rr.Header().Rrtype != dns.TypeSOA {
			continue
		}
		qtype := msg.Question[0].Qtype
		for _, answer := range msg.Answer {
			if answer.Header().Rrtype == qtype || qtype == dns.TypeANY {
				return false
			}
		}
		return true
	}
	return false
}

func responseRcode(payload []byte) (int, bool) {
	if len(payload) < dnsHeaderSize {
		return 0, false
	}
	if payload[2]&0x80 == 0 {
		return 0, false
	}
	return int(payload[3] & 0x0f), true
}
