package cache

import (
	"encoding/binary"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/dnsutils"
	"github.com/miekg/dns"
)

func positiveCacheResponse(msg *dns.Msg) bool {
	if msg == nil || msg.Rcode != dns.RcodeSuccess || len(msg.Answer) == 0 {
		return false
	}
	for _, rr := range msg.Ns {
		if rr.Header().Rrtype != dns.TypeSOA {
			continue
		}
		// NODATA can contain a CNAME chain followed by an authority SOA.
		if len(msg.Question) == 0 {
			return false
		}
		qtype := msg.Question[0].Qtype
		for _, answer := range msg.Answer {
			if answer.Header().Rrtype == qtype || qtype == dns.TypeANY {
				return true
			}
		}
		return false
	}
	return true
}

func positiveCacheWire(wire []byte) bool {
	if len(wire) < 12 || wire[3]&0x0f != dns.RcodeSuccess || binary.BigEndian.Uint16(wire[6:8]) == 0 {
		return false
	}
	if binary.BigEndian.Uint16(wire[8:10]) == 0 {
		return true
	}
	// Ordinary answers without authority records only need the header check.
	// Scan record types without unpacking or allocating for CNAME + SOA NODATA.
	offset := 12
	var qtype uint16
	for i := 0; i < int(binary.BigEndian.Uint16(wire[4:6])); i++ {
		next, ok := skipWireName(wire, offset)
		if !ok || next+4 > len(wire) {
			return false
		}
		if i == 0 {
			qtype = binary.BigEndian.Uint16(wire[next : next+2])
		}
		offset = next + 4
	}
	for i := 0; i < int(binary.BigEndian.Uint16(wire[6:8])); i++ {
		rrtype, next, ok := cacheWireRecord(wire, offset)
		if !ok {
			return false
		}
		if rrtype == qtype || qtype == dns.TypeANY {
			return true
		}
		offset = next
	}
	for i := 0; i < int(binary.BigEndian.Uint16(wire[8:10])); i++ {
		rrtype, next, ok := cacheWireRecord(wire, offset)
		if !ok || rrtype == dns.TypeSOA {
			return false
		}
		offset = next
	}
	return true
}

func cacheWireRecord(wire []byte, offset int) (uint16, int, bool) {
	header, ok := skipWireName(wire, offset)
	if !ok || header+10 > len(wire) {
		return 0, 0, false
	}
	next := header + 10 + int(binary.BigEndian.Uint16(wire[header+8:header+10]))
	return binary.BigEndian.Uint16(wire[header : header+2]), next, next <= len(wire)
}

func (c *Cache) responseCacheTTL(msg *dns.Msg, domainSet string) (msgTTL, cacheTTL time.Duration, cacheable bool) {
	return c.responseCacheTTLAt(msg, domainSet, time.Now())
}

func (c *Cache) responseCacheTTLAt(msg *dns.Msg, domainSet string, at time.Time) (msgTTL, cacheTTL time.Duration, cacheable bool) {
	positive := positiveCacheResponse(msg)
	switch msg.Rcode {
	case dns.RcodeNameError:
		msgTTL = time.Duration(c.args.NXDomainTTL) * time.Second
	case dns.RcodeServerFailure:
		msgTTL = time.Duration(c.args.ServfailTTL) * time.Second
	case dns.RcodeSuccess:
		msgTTL = time.Duration(dnsutils.GetMinimalTTL(msg)) * time.Second
		if !positive {
			msgTTL = min(msgTTL, 300*time.Second)
		} else if c.args.LazyCacheTTL > 0 {
			cacheTTL = time.Duration(c.args.LazyCacheTTL) * time.Second
		}
	}

	if !positive {
		// RFC 2308: the SOA TTL and MINIMUM bound negative caching. A zero
		// bound must not fall through to the short fallback for SOA-free replies.
		for _, rr := range msg.Ns {
			if soa, ok := rr.(*dns.SOA); ok {
				msgTTL = min(msgTTL, time.Duration(min(soa.Hdr.Ttl, soa.Minttl))*time.Second)
				if msgTTL <= 0 {
					return 0, 0, false
				}
			}
		}
	}
	if signedTTL, signed := signedResponseTTL(msg, at); signed {
		msgTTL = min(msgTTL, signedTTL)
		return msgTTL, msgTTL, msgTTL > 0
	}
	if msgTTL <= 0 {
		msgTTL = 5 * time.Second
	}
	if positive && c.args.ClientTTLMin > 0 {
		msgTTL = max(msgTTL, time.Duration(c.args.ClientTTLMin)*time.Second)
	}
	cacheTTL = max(cacheTTL, msgTTL)
	if positive {
		if overrideTTL, overrideStaleTTL, ok := c.domainSetTTLOverride(domainSet); ok {
			if overrideTTL > 0 {
				cacheTTL = time.Duration(overrideTTL) * time.Second
			}
			if overrideStaleTTL > 0 {
				cacheTTL = max(cacheTTL, msgTTL+time.Duration(overrideStaleTTL)*time.Second)
			}
			cacheTTL = max(cacheTTL, msgTTL)
		}
	}
	return msgTTL, cacheTTL, true
}
