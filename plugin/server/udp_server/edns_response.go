package udp_server

import (
	"encoding/binary"

	"github.com/miekg/dns"
)

const fastEmptyOPTSize = 11

// Match the 1200-byte receive size advertised by query_context's response OPT.
var fastEmptyOPTWire = [fastEmptyOPTSize]byte{0, 0, byte(dns.TypeOPT), 4, 176, 0, 0, 0, 0, 0, 0}

// A cached wire response can serve both ordinary and empty-EDNS requests only
// when its additional section is absent or contains one trailing empty OPT.
// Other additional data/options need the full response handling path.
func fastResponseWithoutOPTLen(wire []byte) (uint16, bool) {
	if len(wire) < dnsHeaderSize || len(wire) > dns.MaxMsgSize || binary.BigEndian.Uint16(wire[4:6]) != 1 {
		return 0, false
	}
	offset, ok := skipDNSName(wire, dnsHeaderSize)
	if !ok || offset+4 > len(wire) {
		return 0, false
	}
	offset += 4
	count := int(binary.BigEndian.Uint16(wire[6:8])) + int(binary.BigEndian.Uint16(wire[8:10]))
	for i := 0; i < count; i++ {
		header, ok := skipDNSName(wire, offset)
		if !ok || header+10 > len(wire) || binary.BigEndian.Uint16(wire[header:header+2]) == dns.TypeOPT {
			return 0, false
		}
		offset = header + 10 + int(binary.BigEndian.Uint16(wire[header+8:header+10]))
		if offset > len(wire) {
			return 0, false
		}
	}
	switch binary.BigEndian.Uint16(wire[10:12]) {
	case 0:
		return uint16(offset), offset == len(wire)
	case 1:
		opt := wire[offset:]
		if len(opt) != fastEmptyOPTSize || opt[0] != 0 || binary.BigEndian.Uint16(opt[1:3]) != dns.TypeOPT ||
			binary.BigEndian.Uint32(opt[5:9]) != 0 || binary.BigEndian.Uint16(opt[9:11]) != 0 {
			return 0, false
		}
		return uint16(offset), true
	default:
		return 0, false
	}
}

func (item *fastCacheItem) replySize(withOPT bool) int {
	size := int(item.withoutOPTLen)
	if withOPT {
		size += fastEmptyOPTSize
	}
	return size
}

// The precomputed boundary lets fresh cache hits adapt OPT using only copies
// and header writes, without parsing a DNS message or allocating a buffer.
func (item *fastCacheItem) copyReply(dst []byte, withOPT bool) int {
	size := item.replySize(withOPT)
	if size > len(dst) || size > dns.MaxMsgSize || size < dnsHeaderSize {
		return 0
	}
	copy(dst[:size], item.resp)
	dst[10], dst[11] = 0, 0
	if withOPT {
		dst[11] = 1
		if len(item.resp) == int(item.withoutOPTLen) {
			copy(dst[item.withoutOPTLen:size], fastEmptyOPTWire[:])
		}
	}
	return size
}
