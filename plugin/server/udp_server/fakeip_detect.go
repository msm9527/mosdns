package udp_server

import (
	"net"
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
	if len(payload) == 0 {
		return false
	}
	var msg dns.Msg
	if err := msg.Unpack(payload); err != nil {
		return false
	}
	for _, rr := range msg.Answer {
		switch v := rr.(type) {
		case *dns.A:
			if isFakeIPAddr(v.A) {
				return true
			}
		case *dns.AAAA:
			if isFakeIPAddr(v.AAAA) {
				return true
			}
		}
	}
	return false
}

func isFakeIPAddr(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	for _, prefix := range fakeIPPrefixes {
		if prefix.Contains(addr.Unmap()) {
			return true
		}
	}
	return false
}
