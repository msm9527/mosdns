package domain_mapper

import (
	"github.com/IrineSistiana/mosdns/v5/pkg/concurrent_map"
	"github.com/cespare/xxhash/v2"
)

const compiledMatchCacheSize = 8192

type compiledQueryKey string

func (k compiledQueryKey) Sum() uint64 { return xxhash.Sum64String(string(k)) }

type compiledMatchCache = concurrent_map.Map[compiledQueryKey, *compiledMatch]

// Each immutable matcher snapshot owns its bounded cache, including misses.
// Only provider evidence is cached; dynamic validity and the current hot lookup
// are still checked by DomainMapper on every query.
func (m *compiledMatcherSet) matchCache() *compiledMatchCache {
	if cache := m.cache.Load(); cache != nil {
		return cache
	}
	cache := concurrent_map.NewMapCache[compiledQueryKey, *compiledMatch](compiledMatchCacheSize)
	if m.cache.CompareAndSwap(nil, cache) {
		return cache
	}
	return m.cache.Load()
}
