package udp_server

import "time"

// Reserve one attempt per retry interval, measured from the last reservation.
// Using the item's expiration as the retry clock starts a refresh on every
// request once an old entry is more than one retry interval past its deadline.
func (item *fastCacheItem) scheduleRefresh(now int64, retry time.Duration) bool {
	after := item.refreshAfter.Load()
	if now < after {
		return false
	}
	return item.refreshAfter.CompareAndSwap(after, now+max(1, int64(retry/time.Second)))
}

func (fc *fastCache) withinStaleWindow(item *fastCacheItem, expire, now int64) bool {
	staleMax := fc.cfg.staleMax
	if _, override, ok := fc.domainSetTTLOverride(item.domainSet); ok && override > 0 {
		staleMax = override
	}
	return staleMax <= 0 || now-expire <= int64(staleMax/time.Second)
}
