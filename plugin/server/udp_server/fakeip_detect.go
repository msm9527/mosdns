package udp_server

import (
	"encoding/binary"
	"net/netip"

	"github.com/miekg/dns"
)

var fakeIPPrefixes = mustParseFakeIPPrefixes(
	"28.0.0.0/8",
	"30.0.0.0/8",
	"f2b0::/18",
	"2400::1/64",
)

func mustParseFakeIPPrefixes(raw ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, item := range raw {
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			panic(err)
		}
		out = append(out, prefix)
	}
	return out
}

func isFakeIPResponse(payload []byte) bool {
	if len(payload) < dnsHeaderSize {
		return false
	}
	qdcount := binary.BigEndian.Uint16(payload[4:6])
	ancount := binary.BigEndian.Uint16(payload[6:8])
	if ancount == 0 {
		return false
	}

	offset := dnsHeaderSize
	for i := 0; i < int(qdcount); i++ {
		nextOffset, ok := skipDNSName(payload, offset)
		if !ok || nextOffset+4 > len(payload) {
			return false
		}
		offset = nextOffset + 4
	}

	for i := 0; i < int(ancount); i++ {
		nextOffset, ok := skipDNSName(payload, offset)
		if !ok || nextOffset+10 > len(payload) {
			return false
		}
		offset = nextOffset
		rrType := binary.BigEndian.Uint16(payload[offset : offset+2])
		offset += 2
		offset += 2
		offset += 4
		rdlen := int(binary.BigEndian.Uint16(payload[offset : offset+2]))
		offset += 2
		if offset+rdlen > len(payload) {
			return false
		}

		switch rrType {
		case dns.TypeA:
			if rdlen == 4 {
				addr, ok := netip.AddrFromSlice(payload[offset : offset+rdlen])
				if ok && isFakeIPAddr(addr.Unmap()) {
					return true
				}
			}
		case dns.TypeAAAA:
			if rdlen == 16 {
				addr, ok := netip.AddrFromSlice(payload[offset : offset+rdlen])
				if ok && isFakeIPAddr(addr.Unmap()) {
					return true
				}
			}
		}
		offset += rdlen
	}
	return false
}

func isFakeIPAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, prefix := range fakeIPPrefixes {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}
