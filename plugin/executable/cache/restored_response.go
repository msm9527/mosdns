package cache

import (
	"encoding/binary"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/dnsutils"
	"github.com/miekg/dns"
)

// Old snapshots can outlive negative SOA bounds or authenticated RR TTLs.
// Recheck persisted responses before they can enter either L1 or L2.
func (c *Cache) normalizeRestoredResponse(v *item, expiration time.Time) (time.Time, bool) {
	if v == nil || !restoredResponseMatchesOrdinaryKey(v.resp) {
		return expiration, false
	}
	v.refreshTTLOffsets()
	if positiveCacheWire(v.resp) && !v.dnssec {
		return expiration, true
	}
	var msg dns.Msg
	if err := msg.Unpack(v.resp); err != nil {
		return expiration, false
	}
	msgTTL, _, cacheable := c.responseCacheTTLAt(&msg, storedDomainSet(v.domainSet), time.Unix(0, v.storedUnixNano))
	if !cacheable || v.storedUnixNano <= 0 {
		return expiration, false
	}
	limit := v.storedUnixNano + int64(msgTTL)
	v.expireUnixNano = min(v.expireUnixNano, limit)
	if v.expireUnixNano <= time.Now().UnixNano() {
		return expiration, false
	}
	expiration = minTime(expiration, time.Unix(0, v.expireUnixNano))
	dnsutils.ApplyMaximumTTL(&msg, uint32(msgTTL/time.Second))
	wire, err := msg.Pack()
	if err != nil {
		return expiration, false
	}
	v.resp = wire
	v.refreshTTLOffsets()
	return expiration, true
}

// Legacy keys omitted QCLASS and RD. Their persisted answers can therefore
// belong to requests that now bypass the ordinary recursive IN cache.
// Inspect only the header and question so positive restores avoid unpacking.
func restoredResponseMatchesOrdinaryKey(wire []byte) bool {
	if len(wire) < 12 || wire[2]&0x80 == 0 || wire[2]&0x01 == 0 || wire[2]&0x02 != 0 ||
		wire[2]&0x78 != 0 || binary.BigEndian.Uint16(wire[4:6]) != 1 {
		return false
	}
	end, ok := skipWireName(wire, 12)
	return ok && end+4 <= len(wire) && binary.BigEndian.Uint16(wire[end+2:end+4]) == dns.ClassINET
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
