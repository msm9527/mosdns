package udp_server

import "github.com/miekg/dns"

const dnsHeaderSize = 12

func shouldStoreFastResponse(payload []byte) bool {
	rcode, ok := responseRcode(payload)
	if !ok {
		return false
	}
	return rcode == dns.RcodeSuccess || rcode == dns.RcodeNameError
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
