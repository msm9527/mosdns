package cache

import (
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestStaleClientTTLDoesNotInheritFreshMinimum(t *testing.T) {
	for _, tc := range []struct {
		name           string
		min, max, want uint32
	}{
		{"fresh_minimum", 120, 900, 5},
		{"short_maximum", 120, 3, 3},
		{"unbounded", 120, 0, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCache(&Args{Size: 64, LazyCacheTTL: 3600, LazyStaleTTL: 3600, ClientTTLMin: tc.min, ClientTTLMax: tc.max}, Opts{})
			defer c.Close()
			const name = "stale-ttl.example."
			item, ok := c.saveRespToCache(cacheKeyForQuery(t, name), testQueryContextWithTTL(t, name, net.IPv4(1, 2, 3, 4), 30))
			if !ok {
				t.Fatal("failed to seed response")
			}
			item.storedUnixNano = time.Now().Add(-180 * time.Second).UnixNano()
			item.expireUnixNano = time.Now().Add(-time.Second).UnixNano()
			msg, lazy, _, corrupt := c.respFromCacheItem(item, 3600, expiredMsgTtl)
			if corrupt || !lazy || msg == nil || msg.Answer[0].Header().Ttl != tc.want {
				t.Fatalf("message stale=%v corrupt=%v result=%v, want TTL %d", lazy, corrupt, msg, tc.want)
			}
			wire := buildCachedWirePayload(item, 42, true, expiredMsgTtl, time.Now().UnixNano(), c.args.ClientTTLMin, c.args.ClientTTLMax, 512)
			var packed dns.Msg
			if err := packed.Unpack(wire); err != nil {
				t.Fatal(err)
			}
			if packed.Answer[0].Header().Ttl != tc.want {
				t.Fatalf("packed stale TTL = %d, want %d", packed.Answer[0].Header().Ttl, tc.want)
			}
		})
	}
}
