package coremain

import (
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	autoMemoryLimitFloor     int64 = 80 << 20
	autoMemoryLimitHeadroom  int64 = 16 << 20
	autoMemoryLimitAlign     int64 = 8 << 20
	autoMemoryLimitHeapScale int64 = 2

	disableAutoMemoryLimitEnv = "MOSDNS_DISABLE_AUTO_MEMORY_LIMIT"
)

type autoMemoryLimitResult struct {
	Applied   bool
	HeapAlloc uint64
	Limit     int64
	Previous  int64
}

func applyAutoMemoryLimit() autoMemoryLimitResult {
	if envTruthy(disableAutoMemoryLimitEnv) {
		return autoMemoryLimitResult{}
	}

	currentLimit := debug.SetMemoryLimit(-1)
	if currentLimit != math.MaxInt64 {
		return autoMemoryLimitResult{}
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	limit := recommendAutoMemoryLimit(ms.HeapAlloc)
	prev := debug.SetMemoryLimit(limit)
	return autoMemoryLimitResult{
		Applied:   true,
		HeapAlloc: ms.HeapAlloc,
		Limit:     limit,
		Previous:  prev,
	}
}

func recommendAutoMemoryLimit(heapAlloc uint64) int64 {
	limit := int64(heapAlloc)*autoMemoryLimitHeapScale + autoMemoryLimitHeadroom
	if limit < autoMemoryLimitFloor {
		limit = autoMemoryLimitFloor
	}
	return alignUpInt64(limit, autoMemoryLimitAlign)
}

func alignUpInt64(v, step int64) int64 {
	if step <= 0 {
		return v
	}
	r := v % step
	if r == 0 {
		return v
	}
	return v + step - r
}

func envTruthy(key string) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return false
	}
	if b, err := strconv.ParseBool(raw); err == nil {
		return b
	}
	return raw == "1"
}
