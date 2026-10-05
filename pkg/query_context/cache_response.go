package query_context

var cacheResponseStaleKey = RegKey()

// SetCacheResponseStale records whether the current response came from stale
// cache data, including parsed responses that have no ResponsePayload.
// Response replacement does not reset this metadata. The cache owns its timing.
func (ctx *Context) SetCacheResponseStale(stale bool) {
	if ctx == nil {
		return
	}
	if stale {
		ctx.StoreValue(cacheResponseStaleKey, true)
	} else {
		ctx.DeleteValue(cacheResponseStaleKey)
	}
}

// CacheResponseStale reports stale-cache provenance independently of transport.
func (ctx *Context) CacheResponseStale() bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.GetValue(cacheResponseStaleKey)
	stale, _ := value.(bool)
	return stale
}
