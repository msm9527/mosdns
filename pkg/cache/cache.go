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
	"github.com/IrineSistiana/mosdns/v5/pkg/concurrent_lru"
	"github.com/IrineSistiana/mosdns/v5/pkg/concurrent_map"
	"github.com/IrineSistiana/mosdns/v5/pkg/utils"
	"sync/atomic"
	"time"
)

const (
	defaultCleanerInterval = time.Second * 10
)

type Key interface {
	concurrent_lru.Hashable
}

type Value interface {
	any
}

// Cache is a simple map cache that stores values in memory.
// It is safe for concurrent use.
type Cache[K Key, V Value] struct {
	opts Opts

	closed      atomic.Bool
	closeNotify chan struct{}
	m           *concurrent_map.Map[K, *elem[V]]
	onEvicted   func(key K, v V)
}

type Opts struct {
	Size            int
	CleanerInterval time.Duration
}

func (opts *Opts) init() {
	utils.SetDefaultNum(&opts.Size, 1024)
	utils.SetDefaultNum(&opts.CleanerInterval, defaultCleanerInterval)
}

type elem[V Value] struct {
	v                  V
	expirationUnixNano int64
}

// New initializes a Cache.
// If opts.Size <= 0, a default size will be used.
// cleanerInterval specifies the interval that Cache scans
// and discards expired values. If cleanerInterval <= 0, a default
// interval will be used.
func New[K Key, V Value](opts Opts) *Cache[K, V] {
	opts.init()
	c := &Cache[K, V]{
		closeNotify: make(chan struct{}),
		m:           concurrent_map.NewMapCache[K, *elem[V]](opts.Size),
	}
	go c.gcLoop(opts.CleanerInterval)
	return c
}

// Close closes the inner cleaner of this cache.
func (c *Cache[K, V]) Close() error {
	if ok := c.closed.CompareAndSwap(false, true); ok {
		close(c.closeNotify)
	}
	return nil
}

func (c *Cache[K, V]) SetOnEvicted(f func(key K, v V)) {
	c.onEvicted = f
}

func (c *Cache[K, V]) Get(key K) (v V, expirationTime time.Time, ok bool) {
	if e, hasEntry := c.m.Get(key); hasEntry {
		if e.expirationUnixNano < time.Now().UnixNano() {
			c.Delete(key)
			return
		}
		return e.v, time.Unix(0, e.expirationUnixNano), true
	}
	return
}

// Range calls f through all entries. If f returns an error, the same error will be returned
// by Range.
func (c *Cache[K, V]) Range(f func(key K, v V, expirationTime time.Time) error) error {
	cf := func(key K, v *elem[V]) (newV *elem[V], setV bool, delV bool, err error) {
		return nil, false, false, f(key, v.v, time.Unix(0, v.expirationUnixNano))
	}
	return c.m.RangeDo(cf)
}

// Store stores this kv in cache. If expirationTime is before time.Now(),
// Store is an noop.
func (c *Cache[K, V]) Store(key K, v V, expirationTime time.Time) {
	now := time.Now()
	if now.After(expirationTime) {
		return
	}

	e := &elem[V]{
		v:                  v,
		expirationUnixNano: expirationTime.UnixNano(),
	}
	c.m.SetWithEvicted(key, e, func(key K, v *elem[V]) {
		if c.onEvicted != nil {
			c.onEvicted(key, v.v)
		}
	})
	return
}

// Delete removes key from cache if it exists.
func (c *Cache[K, V]) Delete(key K) {
	var evicted *elem[V]
	c.m.TestAndSet(key, func(v *elem[V], ok bool) (newV *elem[V], setV, delV bool) {
		if !ok {
			return nil, false, false
		}
		evicted = v
		return nil, false, true
	})
	c.fireEvicted(key, evicted)
}

func (c *Cache[K, V]) gcLoop(interval time.Duration) {
	if interval <= 0 {
		interval = defaultCleanerInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closeNotify:
			return
		case now := <-ticker.C:
			c.gc(now)
		}
	}
}

func (c *Cache[K, V]) gc(now time.Time) {
	type evictedItem struct {
		key  K
		elem *elem[V]
	}
	evicted := make([]evictedItem, 0)
	f := func(key K, v *elem[V]) (newV *elem[V], setV, delV bool, err error) {
		if now.UnixNano() > v.expirationUnixNano {
			evicted = append(evicted, evictedItem{key: key, elem: v})
			return nil, false, true, nil
		}
		return nil, false, false, nil
	}
	_ = c.m.RangeDo(f)
	for _, item := range evicted {
		c.fireEvicted(item.key, item.elem)
	}
}

// Len returns the current size of this cache.
func (c *Cache[K, V]) Len() int {
	return c.m.Len()
}

// Flush removes all stored entries from this cache.
func (c *Cache[K, V]) Flush() {
	type evictedItem struct {
		key  K
		elem *elem[V]
	}
	evicted := make([]evictedItem, 0)
	_ = c.m.RangeDo(func(key K, v *elem[V]) (newV *elem[V], setV, delV bool, err error) {
		evicted = append(evicted, evictedItem{key: key, elem: v})
		return nil, false, true, nil
	})
	for _, item := range evicted {
		c.fireEvicted(item.key, item.elem)
	}
}

func (c *Cache[K, V]) fireEvicted(key K, e *elem[V]) {
	if c.onEvicted == nil || e == nil {
		return
	}
	c.onEvicted(key, e.v)
}
