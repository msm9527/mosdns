/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package cache

import (
	"sync"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/concurrent_map"
)

type testKey int

func (t testKey) Sum() uint64 {
	return uint64(t)
}

func Test_Cache(t *testing.T) {
	c := New[testKey, int](Opts{
		Size: 1024,
	})
	for i := 0; i < 128; i++ {
		key := testKey(i)
		c.Store(key, i, time.Now().Add(time.Millisecond*200))
		v, _, ok := c.Get(key)

		if v != i {
			t.Fatal("cache kv mismatched")
		}
		if !ok {
			t.Fatal()
		}
	}

	for i := 0; i < 1024*4; i++ {
		key := testKey(i)
		c.Store(key, i, time.Now().Add(time.Millisecond*200))
	}

	if l := c.Len(); l > 1024 {
		t.Fatal("cache overflow")
	}
}

func Test_memCache_cleaner(t *testing.T) {
	c := New[testKey, int](Opts{
		Size:            1024,
		CleanerInterval: time.Millisecond * 10,
	})
	defer c.Close()
	for i := 0; i < 64; i++ {
		key := testKey(i)
		c.Store(key, i, time.Now().Add(time.Millisecond*10))
	}

	time.Sleep(time.Millisecond * 100)
	if c.Len() != 0 {
		t.Fatal()
	}
}

func Test_memCache_race(t *testing.T) {
	c := New[testKey, int](Opts{
		Size: 1024,
	})
	defer c.Close()

	wg := sync.WaitGroup{}
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 256; i++ {
				key := testKey(i)
				c.Store(key, i, time.Now().Add(time.Minute))
				_, _, _ = c.Get(key)
				c.gc(time.Now())
			}
		}()
	}
	wg.Wait()
}

func Test_Cache_Delete(t *testing.T) {
	c := New[testKey, int](Opts{Size: 16})
	key := testKey(1)
	c.Store(key, 42, time.Now().Add(time.Minute))
	c.Delete(key)
	if _, _, ok := c.Get(key); ok {
		t.Fatal("expected key to be deleted")
	}
}

func TestCacheOnEvicted(t *testing.T) {
	c := New[testKey, int](Opts{Size: 1})
	defer c.Close()

	evicted := make(map[testKey]int)
	c.SetOnEvicted(func(key testKey, v int) {
		evicted[key] = v
	})

	c.Store(0, 1, time.Now().Add(time.Minute))
	c.Store(testKey(concurrent_map.MapShardSize), 2, time.Now().Add(time.Minute))

	if got := len(evicted); got != 1 {
		t.Fatalf("len(evicted) = %d, want 1", got)
	}
	if evicted[0] != 1 {
		t.Fatalf("expected key 0 to be evicted with value 1, got %#v", evicted)
	}
}
