package coremain

import "net/http"

// ManualGC 保留 API 形状，但不再主动干预 Go runtime 的 GC 行为。
func ManualGC() {}

// WithAsyncGC 保留兼容包装层，但不再在请求后主动触发 GC。
func WithAsyncGC(handler http.HandlerFunc) http.HandlerFunc {
	return handler
}
