package domain_memory_pool

import (
	"reflect"
	"sync/atomic"
	"testing"
	"unsafe"
)

func TestMemoryPoolAtomicAlignment(t *testing.T) {
	pool := new(domainMemoryPool)
	fields := reflect.TypeOf(pool).Elem()
	for _, target := range []struct {
		name string
		ptr  *int64
	}{
		{"totalCount", &pool.totalCount}, {"droppedCount", &pool.droppedCount},
		{"droppedBufferCount", &pool.droppedBufferCount}, {"droppedByCapCount", &pool.droppedByCapCount},
		{"promotedCount", &pool.promotedCount}, {"publishedCount", &pool.publishedCount},
		{"hotPendingCount", &pool.hotPendingCount}, {"hotAddTotal", &pool.hotAddTotal},
		{"hotReplaceTotal", &pool.hotReplaceTotal}, {"hotDispatchFailTotal", &pool.hotDispatchFailTotal},
		{"lastHotSyncAtUnixMS", &pool.lastHotSyncAtUnixMS}, {"lastEnqueueWarnAtMS", &pool.lastEnqueueWarnAtMS},
	} {
		field, _ := fields.FieldByName(target.name)
		if field.Offset%8 != 0 || uintptr(unsafe.Pointer(target.ptr))%8 != 0 {
			t.Fatalf("%s is not aligned for 64-bit atomics: offset=%d", target.name, field.Offset)
		}
		atomic.StoreInt64(target.ptr, 1)
		if atomic.AddInt64(target.ptr, 1) != 2 || !atomic.CompareAndSwapInt64(target.ptr, 2, 3) || atomic.LoadInt64(target.ptr) != 3 {
			t.Fatalf("atomic operations failed for %s", target.name)
		}
	}
}
