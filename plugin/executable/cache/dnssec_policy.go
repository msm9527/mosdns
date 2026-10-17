package cache

import (
	"encoding/binary"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/dnsutils"
	"github.com/miekg/dns"
)

// This only bounds cached data by upstream signature metadata; it does not
// validate signatures or turn an upstream answer into a validation result.
func signedResponseTTL(msg *dns.Msg, at time.Time) (time.Duration, bool) {
	ttl := time.Duration(dnsutils.GetMinimalTTL(msg)) * time.Second
	signed := false
	for _, section := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, rr := range section {
			sig, ok := rr.(*dns.RRSIG)
			if !ok {
				continue
			}
			signed = true
			if !sig.ValidityPeriod(at) {
				return 0, true
			}
			remaining := time.Duration(int32(sig.Expiration-uint32(at.Unix()))) * time.Second
			ttl = min(ttl, min(time.Duration(sig.OrigTtl)*time.Second, remaining))
		}
	}
	return ttl, signed
}

func responseHasSignatures(msg *dns.Msg) bool {
	for _, section := range [][]dns.RR{msg.Answer, msg.Ns, msg.Extra} {
		for _, rr := range section {
			if rr.Header().Rrtype == dns.TypeRRSIG {
				return true
			}
		}
	}
	return false
}

func (v *item) refreshResponsePolicy() {
	// A validating upstream can set AD without RRSIG when the client omits DO.
	v.dnssec = len(v.resp) >= 12 && v.resp[3]&0x20 != 0
	v.authenticatedUntilUnixNano = v.storedUnixNano
	minimumTTL := ^uint32(0)
	for _, offset16 := range v.ttlOffsets {
		offset := int(offset16)
		if offset < 4 || offset+4 > len(v.resp) {
			continue
		}
		rrtype := binary.BigEndian.Uint16(v.resp[offset-4 : offset-2])
		if rrtype == dns.TypeRRSIG {
			v.dnssec = true
		}
		if rrtype != dns.TypeOPT {
			minimumTTL = min(minimumTTL, binary.BigEndian.Uint32(v.resp[offset:offset+4]))
		}
	}
	if minimumTTL != ^uint32(0) {
		v.authenticatedUntilUnixNano += int64(minimumTTL) * int64(time.Second)
	}
}
