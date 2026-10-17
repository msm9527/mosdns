package udp_server

import (
	"encoding/binary"

	"github.com/miekg/dns"
)

// The fast response key only represents an ordinary recursive IN question.
// DNSSEC, ECS, cookies and other request options need the full handler/cache key.
// Empty EDNS remains eligible without allocating a parsed message.
func fastRequestUDPLimit(wire []byte, questionEnd int) (int, bool) {
	if len(wire) < 12 || questionEnd < 16 || questionEnd > len(wire) || wire[2] != 1 || wire[3] != 0 ||
		binary.BigEndian.Uint16(wire[4:6]) != 1 || binary.BigEndian.Uint16(wire[6:8]) != 0 ||
		binary.BigEndian.Uint16(wire[8:10]) != 0 || binary.BigEndian.Uint16(wire[questionEnd-2:questionEnd]) != dns.ClassINET {
		return 0, false
	}
	switch binary.BigEndian.Uint16(wire[10:12]) {
	case 0:
		return dns.MinMsgSize, questionEnd == len(wire)
	case 1:
		opt := wire[questionEnd:]
		if len(opt) != 11 || opt[0] != 0 || binary.BigEndian.Uint16(opt[1:3]) != dns.TypeOPT ||
			binary.BigEndian.Uint32(opt[5:9]) != 0 || binary.BigEndian.Uint16(opt[9:11]) != 0 {
			return 0, false
		}
		return max(dns.MinMsgSize, int(binary.BigEndian.Uint16(opt[3:5]))), true
	default:
		return 0, false
	}
}

func fastRequestCacheable(q *dns.Msg) bool {
	if q == nil || q.Response || q.Opcode != dns.OpcodeQuery || !q.RecursionDesired || q.Authoritative ||
		q.Truncated || q.RecursionAvailable || q.Zero || q.AuthenticatedData || q.CheckingDisabled || q.Rcode != 0 ||
		len(q.Question) != 1 || q.Question[0].Qclass != dns.ClassINET || len(q.Answer) != 0 || len(q.Ns) != 0 {
		return false
	}
	if len(q.Extra) == 0 {
		return true
	}
	if len(q.Extra) != 1 {
		return false
	}
	opt, ok := q.Extra[0].(*dns.OPT)
	return ok && opt.Hdr.Name == "." && opt.Hdr.Ttl == 0 && len(opt.Option) == 0
}
