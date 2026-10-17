package cache

import (
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/dnsutils"
	"github.com/miekg/dns"
)

// Old snapshots can carry a negative fresh lifetime that predates the SOA
// bounds. Recheck persisted responses before they can enter either L1 or L2.
func (c *Cache) normalizeRestoredResponse(v *item, expiration time.Time) (time.Time, bool) {
	if v == nil || len(v.resp) < 12 || v.resp[2]&0x02 != 0 {
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

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
