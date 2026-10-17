package udp_server

import (
	"encoding/binary"
	"time"

	"github.com/miekg/dns"
)

func fastResponseHasSignatures(payload []byte) bool {
	offset := dnsHeaderSize
	for i := 0; i < int(binary.BigEndian.Uint16(payload[4:6])); i++ {
		next, ok := skipDNSName(payload, offset)
		if !ok || next+4 > len(payload) {
			return true
		}
		offset = next + 4
	}
	count := int(binary.BigEndian.Uint16(payload[6:8])) + int(binary.BigEndian.Uint16(payload[8:10])) + int(binary.BigEndian.Uint16(payload[10:12]))
	for i := 0; i < count; i++ {
		header, ok := skipDNSName(payload, offset)
		if !ok || header+10 > len(payload) {
			return true
		}
		if binary.BigEndian.Uint16(payload[header:header+2]) == dns.TypeRRSIG {
			return true
		}
		offset = header + 10 + int(binary.BigEndian.Uint16(payload[header+8:header+10]))
		if offset > len(payload) {
			return true
		}
	}
	return false
}

func fastAuthenticatedUntil(payload []byte, ttlOffsets []int, now time.Time) int64 {
	minimumTTL := ^uint32(0)
	for _, offset := range ttlOffsets {
		if offset >= 4 && offset+4 <= len(payload) && binary.BigEndian.Uint16(payload[offset-4:offset-2]) != dns.TypeOPT {
			minimumTTL = min(minimumTTL, binary.BigEndian.Uint32(payload[offset:offset+4]))
		}
	}
	if minimumTTL == ^uint32(0) {
		minimumTTL = 0
	}
	return now.Add(time.Duration(minimumTTL) * time.Second).Unix()
}

func clearFastStaleAD(payload []byte, item *fastCacheItem, expire, now int64) {
	if len(payload) >= dnsHeaderSize && payload[3]&0x20 != 0 && (now > expire || now >= item.authenticatedUntil) {
		payload[3] &^= 0x20
	}
}
