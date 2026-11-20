package udp_server

import (
	"encoding/binary"
	"sync/atomic"
)

// Retention controls eviction, not DNS freshness. Refresh real answers when the
// earliest record expires, even if internal_ttl keeps the entry hot much longer.
func (item *fastCacheItem) freshnessDeadline() int64 {
	deadline := atomic.LoadInt64(&item.expire)
	if item.freshUntil > 0 {
		deadline = min(deadline, item.freshUntil)
	}
	return deadline
}

// Offsets are computed once at admission. Hits neither unpack DNS nor allocate;
// only the caller's reply buffer is changed, never the shared cached payload.
func capFastFreshTTL(payload []byte, item *fastCacheItem, expire, now int64) {
	if item.freshUntil == 0 || now >= expire {
		return
	}
	remaining := uint32(min(expire-now, int64(^uint32(0))))
	for _, off := range item.ttlOffsets {
		if off+4 <= len(payload) {
			ttl := binary.BigEndian.Uint32(payload[off : off+4])
			binary.BigEndian.PutUint32(payload[off:off+4], min(ttl, remaining))
		}
	}
}
