package coremain

import (
	"net/http"
	"runtime/debug"
	"sync/atomic"
	"time"
)

const (
	manualGCDelay        = 200 * time.Millisecond
	defaultGCMinInterval = 30 * time.Second
)

var (
	gcMinInterval atomic.Int64
	gcLastRunUnix atomic.Int64
	gcInFlight    atomic.Bool
)

func init() {
	gcMinInterval.Store(int64(defaultGCMinInterval))
}

// ManualGC 手动触发 GC，用于清理大量临时内存。
// 异步执行，带短暂延迟，避免阻塞主流程。
// 同时做限频和单并发调度，防止高频接口触发 GC 风暴。
func ManualGC() {
	nowUnix := time.Now().UnixNano()
	minInterval := gcMinInterval.Load()
	if minInterval > 0 {
		last := gcLastRunUnix.Load()
		if last > 0 && nowUnix-last < minInterval {
			return
		}
	}
	if !gcInFlight.CompareAndSwap(false, true) {
		return
	}

	go func() {
		defer gcInFlight.Store(false)

		nowUnix := time.Now().UnixNano()
		minInterval := gcMinInterval.Load()
		if minInterval > 0 {
			last := gcLastRunUnix.Load()
			if last > 0 && nowUnix-last < minInterval {
				return
			}
		}
		gcLastRunUnix.Store(nowUnix)

		time.Sleep(manualGCDelay)
		debug.FreeOSMemory()
	}()
}

// WithAsyncGC 是一个 HTTP 中间件/包装器。
// 它可以包裹任何 http.HandlerFunc，在请求结束后自动触发 ManualGC。
// 这里的 handler 和返回值类型都在 coremain 包内，可以直接调用。
func WithAsyncGC(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 注册清理逻辑
		defer ManualGC()
		// 执行原始逻辑
		handler(w, r)
	}
}
