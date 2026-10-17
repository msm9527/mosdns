package udp_server

import (
	"encoding/binary"

	"github.com/miekg/dns"
)

const fastStaleResponseTTL = uint32(5)

func applyFastCacheReplyPolicy(payload []byte, item *fastCacheItem, expire, now int64) {
	clearFastStaleAD(payload, item, expire, now)
	if now >= expire {
		capFastStaleTTL(payload)
	}
}

// Only expired replies need the record scan. Keep stored bytes and the ordinary
// fresh hit path unchanged; OPT's TTL word contains flags, not an RR lifetime.
func capFastStaleTTL(payload []byte) {
	if len(payload) < dnsHeaderSize {
		return
	}
	offset := dnsHeaderSize
	for i := 0; i < int(binary.BigEndian.Uint16(payload[4:6])); i++ {
		next, ok := skipDNSName(payload, offset)
		if !ok || next+4 > len(payload) {
			return
		}
		offset = next + 4
	}
	count := int(binary.BigEndian.Uint16(payload[6:8])) + int(binary.BigEndian.Uint16(payload[8:10])) + int(binary.BigEndian.Uint16(payload[10:12]))
	for i := 0; i < count; i++ {
		header, ok := skipDNSName(payload, offset)
		if !ok || header+10 > len(payload) {
			return
		}
		if binary.BigEndian.Uint16(payload[header:header+2]) != dns.TypeOPT {
			ttl := binary.BigEndian.Uint32(payload[header+4 : header+8])
			binary.BigEndian.PutUint32(payload[header+4:header+8], min(ttl, fastStaleResponseTTL))
		}
		offset = header + 10 + int(binary.BigEndian.Uint16(payload[header+8:header+10]))
		if offset > len(payload) {
			return
		}
	}
}
