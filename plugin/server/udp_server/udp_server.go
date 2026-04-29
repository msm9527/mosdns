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

package udp_server

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"net/netip"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/utils"
	"github.com/IrineSistiana/mosdns/v5/plugin/server/server_utils"
	"github.com/IrineSistiana/mosdns/v5/plugin/switch/switchmeta"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

const (
	PluginType = "udp_server"
	cacheWays  = 4
	cacheSize  = 65536
	cacheMask  = cacheSize - 1
	ruleWays   = 4
	ruleSize   = 32768

	defaultFastBypassWarmupMain    = 3
	defaultFastBypassWarmupRequery = 1
	defaultStaleRefreshRetrySec    = 10
	defaultMainListenerWorkers     = 4

	fastQNameHashOffset64 = 1469598103934665603
	fastQNameHashPrime64  = 1099511628211
)

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
}

type Args struct {
	Entry                     string   `yaml:"entry"`
	Listen                    string   `yaml:"listen"`
	EnableAudit               bool     `yaml:"enable_audit"`
	FastCacheInternalTTL      int      `yaml:"fast_cache_internal_ttl"`
	FastCacheStaleRetrySec    int      `yaml:"fast_cache_stale_retry_seconds"`
	FastCacheStaleMaxSec      int      `yaml:"fast_cache_stale_max_seconds"`
	FastCacheTTLMin           uint32   `yaml:"fast_cache_ttl_min"`
	FastCacheTTLMax           uint32   `yaml:"fast_cache_ttl_max"`
	FastCacheBypassDomainSets []string `yaml:"fast_cache_bypass_domain_sets"`
	FastMetricsLogInterval    int      `yaml:"fast_metrics_log_interval"`
	FastBypassWarmupSec       int      `yaml:"fast_bypass_warmup_seconds"`
	FastListenerWorkers       int      `yaml:"fast_listener_workers"`
}

func (a *Args) init() {
	utils.SetDefaultString(&a.Listen, "127.0.0.1:53")
	utils.SetDefaultUnsignNum(&a.FastCacheInternalTTL, 120)
	utils.SetDefaultUnsignNum(&a.FastCacheStaleRetrySec, defaultStaleRefreshRetrySec)
	utils.SetDefaultUnsignNum(&a.FastCacheStaleMaxSec, 300)
	utils.SetDefaultNum(&a.FastCacheTTLMax, uint32(30))
	utils.SetDefaultUnsignNum(&a.FastMetricsLogInterval, 60)
	if a.FastBypassWarmupSec <= 0 {
		a.FastBypassWarmupSec = inferFastBypassWarmupSec(a.Entry, a.Listen)
	}
	if a.FastListenerWorkers <= 0 {
		a.FastListenerWorkers = inferFastListenerWorkers(a.Entry, a.Listen)
	}
	if a.FastCacheTTLMax > 0 && a.FastCacheTTLMin > a.FastCacheTTLMax {
		a.FastCacheTTLMin = a.FastCacheTTLMax
	}
	if a.FastBypassWarmupSec < 0 {
		a.FastBypassWarmupSec = 0
	}
	if a.FastListenerWorkers < 1 {
		a.FastListenerWorkers = 1
	}
	if a.FastListenerWorkers > defaultMainListenerWorkers {
		a.FastListenerWorkers = defaultMainListenerWorkers
	}
	a.FastCacheBypassDomainSets = normalizeFastCacheDomainSetTokens(a.FastCacheBypassDomainSets)
}

func inferFastBypassWarmupSec(entry, listen string) int {
	lentry := strings.ToLower(entry)
	llisten := strings.ToLower(listen)
	if strings.Contains(lentry, "requery") || strings.Contains(llisten, ":7766") {
		return defaultFastBypassWarmupRequery
	}
	return defaultFastBypassWarmupMain
}

func inferFastListenerWorkers(entry, listen string) int {
	if runtime.GOOS != "linux" {
		return 1
	}
	lentry := strings.ToLower(entry)
	llisten := strings.ToLower(listen)
	if strings.Contains(lentry, "requery") || strings.Contains(llisten, ":7766") {
		return 1
	}
	if strings.Contains(lentry, "sequence_6666") || strings.HasSuffix(llisten, ":53") {
		return min(runtime.GOMAXPROCS(0), defaultMainListenerWorkers)
	}
	return 1
}

func normalizeFastCacheDomainSetTokens(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool {
			return r == '|' || r == ',' || r == '，'
		}) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if _, ok := seen[part]; ok {
				continue
			}
			seen[part] = struct{}{}
			out = append(out, part)
		}
	}
	sort.Strings(out)
	return out
}

func fastCacheDomainSetContainsAny(domainSet string, tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	domainSet = strings.TrimSpace(domainSet)
	if domainSet == "" {
		return false
	}
	for {
		part, rest, ok := strings.Cut(domainSet, "|")
		part = strings.TrimSpace(part)
		for _, token := range tokens {
			if part == strings.TrimSpace(token) {
				return true
			}
		}
		if !ok {
			return false
		}
		domainSet = rest
	}
}

type UdpServer struct {
	args  *Args
	conns []net.PacketConn
	fc    *fastCache
}

var (
	_ coremain.RuntimeCacheController = (*UdpServer)(nil)
	_ coremain.CacheStatsProvider     = (*UdpServer)(nil)
)

func (s *UdpServer) Close() error {
	var firstErr error
	for _, c := range s.conns {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *UdpServer) RuntimeCacheKind() string {
	return "udp_fast"
}

func (s *UdpServer) FlushRuntimeCache(_ context.Context) error {
	if s == nil || s.fc == nil {
		return nil
	}
	s.fc.Flush()
	return nil
}

func (s *UdpServer) PurgeDomainsRuntimeCache(_ context.Context, domains []string, qtypes []uint16) (int, error) {
	if s == nil || s.fc == nil {
		return 0, nil
	}
	return s.fc.PurgeDomains(domains, qtypes), nil
}

func (s *UdpServer) RuntimeCacheEntryCount() int {
	if s == nil || s.fc == nil {
		return 0
	}
	return s.fc.Len()
}

func (s *UdpServer) SnapshotCacheStats() coremain.CacheStatsSnapshot {
	var cfg fastCacheConfig
	var snapshot fastStatsSnapshot
	backendSize := 0
	listenerWorkers := 0
	if s != nil && s.fc != nil {
		cfg = s.fc.cfg
		backendSize = s.fc.Len()
		listenerWorkers = len(s.conns)
		if s.fc.stats != nil {
			snapshot = s.fc.stats.snapshot()
		}
	}

	return coremain.CacheStatsSnapshot{
		Name:        "UDP fast path",
		BackendSize: backendSize,
		Counters:    fastStatsCounters(snapshot),
		Config: map[string]interface{}{
			"size":                        cacheSize * cacheWays,
			"internal_ttl":                int(cfg.internalTTL / time.Second),
			"stale_retry_seconds":         int(cfg.staleRetry / time.Second),
			"stale_max_seconds":           int(cfg.staleMax / time.Second),
			"ttl_min":                     cfg.ttlMin,
			"ttl_max":                     cfg.ttlMax,
			"bypass_domain_sets":          append([]string(nil), cfg.bypassDomainSets...),
			"runtime_cache_kind":          "udp_fast",
			"fakeip_requires_switch_on":   true,
			"cache_hits_bypass_audit_log": true,
			"listener_workers":            listenerWorkers,
			"rule_meta_size":              ruleSize * ruleWays,
		},
	}
}

func fastStatsCounters(s fastStatsSnapshot) map[string]uint64 {
	return map[string]uint64{
		"query_total":        s.CacheLookup,
		"hit_total":          s.CacheHit,
		"lazy_hit_total":     s.BypassStaleReply,
		"miss_total":         s.CacheMiss,
		"bypass_requests":    s.BypassRequests,
		"bypass_bad_packet":  s.BypassBadPacket,
		"bypass_rule_reply":  s.BypassRuleReply,
		"bypass_cache_reply": s.BypassCacheReply,
		"bypass_stale_reply": s.BypassStaleReply,
		"bypass_warmup_skip": s.BypassWarmupSkip,
		"refresh_requested":  s.RefreshRequested,
		"refresh_miss":       s.RefreshMiss,
		"refresh_no_payload": s.RefreshNoPayload,
		"refresh_store":      s.RefreshStore,
		"refresh_store_skip": s.RefreshStoreSkip,
		"cache_lookup":       s.CacheLookup,
		"cache_store":        s.CacheStore,
		"cache_hit":          s.CacheHit,
		"cache_miss":         s.CacheMiss,
		"cache_collision":    s.CacheCollision,
		"cache_expired":      s.CacheExpired,
		"cache_eviction":     s.CacheEviction,
	}
}

type SwitchPlugin interface{ GetValue() string }
type DomainMapperPlugin interface {
	FastMatch(qname string) ([]uint8, string, bool)
}
type DomainMapperRevisionPlugin interface {
	CacheRevision() string
}
type NumericCacheRevisionPlugin interface {
	CacheRevisionUint64() uint64
}
type IPSetPlugin interface{ Match(addr netip.Addr) bool }

type fastRevisionValue struct {
	text    string
	number  uint64
	numeric bool
	set     bool
}

func (v fastRevisionValue) empty() bool {
	return !v.set
}

func (v fastRevisionValue) equal(other fastRevisionValue) bool {
	if v.set != other.set {
		return false
	}
	if !v.set {
		return true
	}
	if v.numeric != other.numeric {
		return false
	}
	if v.numeric {
		return v.number == other.number
	}
	return v.text == other.text
}

func (v fastRevisionValue) String() string {
	if !v.set {
		return ""
	}
	if v.numeric {
		return strconv.FormatUint(v.number, 10)
	}
	return v.text
}

func fastTextRevisionValue(text string) fastRevisionValue {
	if text == "" {
		return fastRevisionValue{}
	}
	return fastRevisionValue{text: text, set: true}
}

func fastNumericRevisionValue(number uint64) fastRevisionValue {
	return fastRevisionValue{number: number, numeric: true, set: true}
}

type fastRuleRevision struct {
	domainMapper fastRevisionValue
	rewrite      fastRevisionValue
}

func (r fastRuleRevision) empty() bool {
	return r.domainMapper.empty() && r.rewrite.empty()
}

func (r fastRuleRevision) matches(item *fastCacheItem) bool {
	return item != nil &&
		item.domainMapperRevision.equal(r.domainMapper) &&
		item.rewriteRevision.equal(r.rewrite)
}

func (r fastRuleRevision) String() string {
	domainMapper := r.domainMapper.String()
	rewrite := r.rewrite.String()
	if domainMapper == "" {
		return rewrite
	}
	if rewrite == "" {
		return domainMapper
	}
	return domainMapper + "|" + rewrite
}

func fastRuleRevisionFromJoined(revision string) fastRuleRevision {
	if revision == "" {
		return fastRuleRevision{}
	}
	dmRevision, rewriteRevision, ok := strings.Cut(revision, "|")
	if !ok {
		return fastRuleRevision{domainMapper: fastTextRevisionValue(revision)}
	}
	return fastRuleRevision{
		domainMapper: fastTextRevisionValue(dmRevision),
		rewrite:      fastTextRevisionValue(rewriteRevision),
	}
}

type fastCacheItem struct {
	// Keep the atomic field first so 32-bit ARM gets 8-byte alignment.
	expire int64

	resp                 []byte
	updating             uint32
	domainSet            string
	domainMapperRevision fastRevisionValue
	rewriteRevision      fastRevisionValue
	fakeIP               bool
	hash                 uint64
	ruleFlags            uint64
	qname                string
	qtype                uint16
}

type fastCacheBucket struct {
	slots [cacheWays]atomic.Pointer[fastCacheItem]
}

type fastCacheTable struct {
	buckets []fastCacheBucket
	mask    uint64
}

type fastRuleCacheItem struct {
	revision  fastRevisionValue
	domainSet string
	hash      uint64
	ruleFlags uint64
	qname     string
	matched   bool
}

type fastRuleCacheBucket struct {
	slots [ruleWays]atomic.Pointer[fastRuleCacheItem]
}

type fastRuleCacheTable struct {
	buckets []fastRuleCacheBucket
	mask    uint64
}

type fastCache struct {
	table    atomic.Pointer[fastCacheTable]
	rules    atomic.Pointer[fastRuleCacheTable]
	initOnce sync.Once
	ruleOnce sync.Once
	cfg      fastCacheConfig
	stats    *fastStats
}

type fastCacheConfig struct {
	internalTTL      time.Duration
	staleRetry       time.Duration
	staleMax         time.Duration
	ttlMin           uint32
	ttlMax           uint32
	bypassDomainSets []string
}

type fastStats struct {
	bypassRequests   atomic.Uint64
	bypassBadPacket  atomic.Uint64
	bypassRuleReply  atomic.Uint64
	bypassCacheReply atomic.Uint64
	bypassStaleReply atomic.Uint64
	bypassWarmupSkip atomic.Uint64

	refreshRequested atomic.Uint64
	refreshMiss      atomic.Uint64
	refreshNoPayload atomic.Uint64
	refreshStore     atomic.Uint64
	refreshStoreSkip atomic.Uint64

	cacheLookup    atomic.Uint64
	cacheStore     atomic.Uint64
	cacheHit       atomic.Uint64
	cacheMiss      atomic.Uint64
	cacheCollision atomic.Uint64
	cacheExpired   atomic.Uint64
	cacheEviction  atomic.Uint64
}

type fastStatsSnapshot struct {
	BypassRequests   uint64
	BypassBadPacket  uint64
	BypassRuleReply  uint64
	BypassCacheReply uint64
	BypassStaleReply uint64
	BypassWarmupSkip uint64
	RefreshRequested uint64
	RefreshMiss      uint64
	RefreshNoPayload uint64
	RefreshStore     uint64
	RefreshStoreSkip uint64
	CacheLookup      uint64
	CacheStore       uint64
	CacheHit         uint64
	CacheMiss        uint64
	CacheCollision   uint64
	CacheExpired     uint64
	CacheEviction    uint64
}

func (s *fastStats) snapshot() fastStatsSnapshot {
	if s == nil {
		return fastStatsSnapshot{}
	}
	return fastStatsSnapshot{
		BypassRequests:   s.bypassRequests.Load(),
		BypassBadPacket:  s.bypassBadPacket.Load(),
		BypassRuleReply:  s.bypassRuleReply.Load(),
		BypassCacheReply: s.bypassCacheReply.Load(),
		BypassStaleReply: s.bypassStaleReply.Load(),
		BypassWarmupSkip: s.bypassWarmupSkip.Load(),
		RefreshRequested: s.refreshRequested.Load(),
		RefreshMiss:      s.refreshMiss.Load(),
		RefreshNoPayload: s.refreshNoPayload.Load(),
		RefreshStore:     s.refreshStore.Load(),
		RefreshStoreSkip: s.refreshStoreSkip.Load(),
		CacheLookup:      s.cacheLookup.Load(),
		CacheStore:       s.cacheStore.Load(),
		CacheHit:         s.cacheHit.Load(),
		CacheMiss:        s.cacheMiss.Load(),
		CacheCollision:   s.cacheCollision.Load(),
		CacheExpired:     s.cacheExpired.Load(),
		CacheEviction:    s.cacheEviction.Load(),
	}
}

func newFastCache(cfg fastCacheConfig, stats *fastStats) *fastCache {
	if cfg.staleRetry <= 0 {
		cfg.staleRetry = defaultStaleRefreshRetrySec * time.Second
	}
	if cfg.staleMax <= 0 {
		cfg.staleMax = 300 * time.Second
	}
	cfg.bypassDomainSets = normalizeFastCacheDomainSetTokens(cfg.bypassDomainSets)
	return &fastCache{cfg: cfg, stats: stats}
}

func (fc *fastCache) shouldBypassDomainSet(domainSet string) bool {
	if fc == nil {
		return false
	}
	return fastCacheDomainSetContainsAny(domainSet, fc.cfg.bypassDomainSets)
}

func (fc *fastCache) loadTable() *fastCacheTable {
	if fc == nil {
		return nil
	}
	return fc.table.Load()
}

func (fc *fastCache) ensureTable() *fastCacheTable {
	if fc == nil {
		return nil
	}
	fc.initOnce.Do(func() {
		fc.table.Store(&fastCacheTable{
			buckets: make([]fastCacheBucket, cacheSize),
			mask:    cacheSize - 1,
		})
	})
	return fc.table.Load()
}

func (fc *fastCache) loadRuleTable() *fastRuleCacheTable {
	if fc == nil {
		return nil
	}
	return fc.rules.Load()
}

func (fc *fastCache) ensureRuleTable() *fastRuleCacheTable {
	if fc == nil {
		return nil
	}
	fc.ruleOnce.Do(func() {
		fc.rules.Store(&fastRuleCacheTable{
			buckets: make([]fastRuleCacheBucket, ruleSize),
			mask:    ruleSize - 1,
		})
	})
	return fc.rules.Load()
}

func (fc *fastCache) GetOrUpdating(hash uint64, buf []byte, qname string, qtype uint16, allowFakeIP bool) (int, int, uint64, string, bool) {
	return fc.getOrUpdating(hash, buf, qname, qtype, allowFakeIP, fastRuleRevision{})
}

func (fc *fastCache) getOrUpdating(hash uint64, buf []byte, qname string, qtype uint16, allowFakeIP bool, expectedRuleRevision fastRuleRevision) (int, int, uint64, string, bool) {
	ptr, occupied := fc.findItem(hash, qname, qtype)
	return fc.replyFromItem(ptr, occupied, buf, allowFakeIP, expectedRuleRevision)
}

func (fc *fastCache) getOrUpdatingWire(hash uint64, buf []byte, qnameWire []byte, qtype uint16, allowFakeIP bool, expectedRuleRevision fastRuleRevision) (int, int, uint64, string, bool) {
	ptr, occupied := fc.findItemWire(hash, qnameWire, qtype)
	return fc.replyFromItem(ptr, occupied, buf, allowFakeIP, expectedRuleRevision)
}

func (fc *fastCache) replyFromItem(ptr *fastCacheItem, occupied bool, buf []byte, allowFakeIP bool, expectedRuleRevision fastRuleRevision) (int, int, uint64, string, bool) {
	if ptr == nil {
		if fc.stats != nil {
			if occupied {
				fc.stats.cacheCollision.Add(1)
			}
			fc.stats.cacheMiss.Add(1)
		}
		return server.FastActionContinue, 0, 0, "", false
	}
	if !expectedRuleRevision.empty() && !expectedRuleRevision.matches(ptr) {
		if fc.stats != nil {
			fc.stats.cacheMiss.Add(1)
		}
		return server.FastActionContinue, 0, 0, ptr.domainSet, false
	}
	if ptr.fakeIP && !allowFakeIP {
		if fc.stats != nil {
			fc.stats.cacheMiss.Add(1)
		}
		return server.FastActionContinue, 0, 0, "", false
	}
	if fc.shouldBypassDomainSet(ptr.domainSet) {
		return server.FastActionContinue, 0, 0, ptr.domainSet, false
	}

	now := time.Now().Unix()
	expire := atomic.LoadInt64(&ptr.expire)
	staleAllowed := true
	if now > expire {
		if fc.stats != nil {
			fc.stats.cacheExpired.Add(1)
		}
		if fc.cfg.staleMax > 0 && now-expire > int64(fc.cfg.staleMax/time.Second) {
			staleAllowed = false
		}
		if atomic.CompareAndSwapUint32(&ptr.updating, 0, 1) {
			if fc.stats != nil {
				fc.stats.cacheMiss.Add(1)
				if ptr.resp != nil {
					fc.stats.refreshRequested.Add(1)
				}
			}
			return server.FastActionContinue, 0, ptr.ruleFlags, ptr.domainSet, ptr.resp != nil && staleAllowed
		}
		retryAfter := int64(fc.cfg.staleRetry / time.Second)
		if now-expire > retryAfter && atomic.CompareAndSwapUint32(&ptr.updating, 1, 0) {
			if atomic.CompareAndSwapUint32(&ptr.updating, 0, 1) {
				if fc.stats != nil {
					fc.stats.cacheMiss.Add(1)
					if ptr.resp != nil {
						fc.stats.refreshRequested.Add(1)
					}
				}
				return server.FastActionContinue, 0, ptr.ruleFlags, ptr.domainSet, ptr.resp != nil && staleAllowed
			}
		}
		if !staleAllowed {
			if fc.stats != nil {
				fc.stats.cacheMiss.Add(1)
			}
			return server.FastActionContinue, 0, 0, ptr.domainSet, false
		}
	}

	if ptr.resp != nil {
		if fc.stats != nil {
			fc.stats.cacheHit.Add(1)
		}
		respLen := len(ptr.resp)
		txid0, txid1 := buf[0], buf[1]
		copy(buf, ptr.resp)
		buf[0], buf[1] = txid0, txid1
		return server.FastActionReply, respLen, ptr.ruleFlags, ptr.domainSet, false
	}
	if fc.stats != nil {
		fc.stats.cacheMiss.Add(1)
	}
	return server.FastActionContinue, 0, 0, "", false
}

func (fc *fastCache) Store(qname string, qtype uint16, resp []byte, dset string, fakeIP bool) bool {
	return fc.storeWithMeta(qname, qtype, resp, dset, fakeIP, 0, "")
}

func (fc *fastCache) storeWithMeta(qname string, qtype uint16, resp []byte, dset string, fakeIP bool, ruleFlags uint64, ruleRevision string) bool {
	return fc.storeWithRuleRevision(qname, qtype, resp, dset, fakeIP, ruleFlags, fastRuleRevisionFromJoined(ruleRevision))
}

func (fc *fastCache) storeWithRuleRevision(qname string, qtype uint16, resp []byte, dset string, fakeIP bool, ruleFlags uint64, ruleRevision fastRuleRevision) bool {
	if fc.shouldBypassDomainSet(dset) {
		return false
	}
	h := fastQNameHashString(qname, qtype)

	bakedResp := make([]byte, len(resp))
	copy(bakedResp, resp)
	offsets := findTTLOffsets(bakedResp)
	for _, off := range offsets {
		if off+4 <= len(bakedResp) {
			ttl := binary.BigEndian.Uint32(bakedResp[off : off+4])
			binary.BigEndian.PutUint32(bakedResp[off:off+4], clampTTL(ttl, fc.cfg.ttlMin, fc.cfg.ttlMax))
		}
	}

	item := &fastCacheItem{
		resp:                 bakedResp,
		expire:               time.Now().Add(fc.cfg.internalTTL).Unix(),
		updating:             0,
		domainSet:            dset,
		domainMapperRevision: ruleRevision.domainMapper,
		rewriteRevision:      ruleRevision.rewrite,
		fakeIP:               fakeIP,
		hash:                 h,
		ruleFlags:            ruleFlags,
		qname:                qname,
		qtype:                qtype,
	}
	fc.storeItem(item)
	if fc.stats != nil {
		fc.stats.cacheStore.Add(1)
	}
	return true
}

func (fc *fastCache) CopyResponse(txid uint16, qname string, qtype uint16, allowFakeIP bool) *[]byte {
	hash := fastQNameHashString(qname, qtype)
	ptr, _ := fc.findItem(hash, qname, qtype)
	if ptr == nil {
		return nil
	}
	if ptr.fakeIP && !allowFakeIP {
		return nil
	}
	if fc.shouldBypassDomainSet(ptr.domainSet) {
		return nil
	}
	if len(ptr.resp) == 0 {
		return nil
	}
	resp := pool.GetBuf(len(ptr.resp))
	copy(*resp, ptr.resp)
	binary.BigEndian.PutUint16((*resp)[:2], txid)
	return resp
}

func (fc *fastCache) findItem(hash uint64, qname string, qtype uint16) (*fastCacheItem, bool) {
	table := fc.loadTable()
	if table == nil {
		return nil, false
	}
	bucket := &table.buckets[fastCacheBucketIndex(hash, table.mask)]
	occupied := false
	for i := range bucket.slots {
		ptr := bucket.slots[i].Load()
		if ptr == nil {
			continue
		}
		occupied = true
		if ptr.hash == hash && ptr.qtype == qtype && fastQNameEqualString(ptr.qname, qname) {
			return ptr, true
		}
	}
	return nil, occupied
}

func (fc *fastCache) findItemWire(hash uint64, qnameWire []byte, qtype uint16) (*fastCacheItem, bool) {
	table := fc.loadTable()
	if table == nil {
		return nil, false
	}
	bucket := &table.buckets[fastCacheBucketIndex(hash, table.mask)]
	occupied := false
	for i := range bucket.slots {
		ptr := bucket.slots[i].Load()
		if ptr == nil {
			continue
		}
		occupied = true
		if ptr.hash == hash && ptr.qtype == qtype && fastQNameWireEqualString(qnameWire, ptr.qname) {
			return ptr, true
		}
	}
	return nil, occupied
}

func (fc *fastCache) getRuleMetaWire(hash uint64, qnameWire []byte, revision fastRevisionValue) (*fastRuleCacheItem, bool) {
	if revision.empty() {
		return nil, false
	}
	table := fc.loadRuleTable()
	if table == nil {
		return nil, false
	}
	bucket := &table.buckets[fastCacheBucketIndex(hash, table.mask)]
	for i := range bucket.slots {
		ptr := bucket.slots[i].Load()
		if ptr == nil {
			continue
		}
		if ptr.hash == hash && ptr.revision.equal(revision) && fastQNameWireEqualString(qnameWire, ptr.qname) {
			return ptr, true
		}
	}
	return nil, false
}

func (fc *fastCache) storeRuleMeta(qname string, hash uint64, revision fastRevisionValue, ruleFlags uint64, domainSet string, matched bool) {
	if revision.empty() {
		return
	}
	table := fc.ensureRuleTable()
	if table == nil {
		return
	}
	item := &fastRuleCacheItem{
		revision:  revision,
		domainSet: domainSet,
		hash:      hash,
		ruleFlags: ruleFlags,
		qname:     qname,
		matched:   matched,
	}
	bucket := &table.buckets[fastCacheBucketIndex(hash, table.mask)]
	var emptySlot *atomic.Pointer[fastRuleCacheItem]
	for i := range bucket.slots {
		slot := &bucket.slots[i]
		current := slot.Load()
		if current == nil {
			if emptySlot == nil {
				emptySlot = slot
			}
			continue
		}
		if current.hash == item.hash && current.revision.equal(item.revision) && fastQNameEqualString(current.qname, item.qname) {
			slot.Store(item)
			return
		}
	}
	if emptySlot != nil {
		emptySlot.Store(item)
		return
	}
	bucket.slots[(hash>>16)%ruleWays].Store(item)
}

func (fc *fastCache) storeItem(item *fastCacheItem) {
	table := fc.ensureTable()
	if table == nil {
		return
	}
	bucket := &table.buckets[fastCacheBucketIndex(item.hash, table.mask)]
	var emptySlot *atomic.Pointer[fastCacheItem]
	var victimSlot *atomic.Pointer[fastCacheItem]
	victimExpire := int64(math.MaxInt64)

	for i := range bucket.slots {
		slot := &bucket.slots[i]
		current := slot.Load()
		if current == nil {
			if emptySlot == nil {
				emptySlot = slot
			}
			continue
		}
		if current.hash == item.hash && current.qtype == item.qtype && fastQNameEqualString(current.qname, item.qname) {
			slot.Store(item)
			return
		}
		expire := atomic.LoadInt64(&current.expire)
		if expire < victimExpire {
			victimExpire = expire
			victimSlot = slot
		}
	}

	if emptySlot != nil {
		emptySlot.Store(item)
		return
	}
	if victimSlot != nil {
		victimSlot.Store(item)
		if fc.stats != nil {
			fc.stats.cacheEviction.Add(1)
		}
	}
}

func fastCacheBucketIndex(hash uint64, mask uint64) uint64 {
	hash ^= hash >> 33
	hash *= 0xff51afd7ed558ccd
	hash ^= hash >> 33
	return hash & mask
}

func fastQNameHashByte(hash uint64, b byte) uint64 {
	if b >= 'A' && b <= 'Z' {
		b += 'a' - 'A'
	}
	return fastHashRawByte(hash, b)
}

func fastHashRawByte(hash uint64, b byte) uint64 {
	hash ^= uint64(b)
	hash *= fastQNameHashPrime64
	return hash
}

func fastQNameHashFinish(hash uint64, qtype uint16) uint64 {
	hash = fastHashRawByte(hash, byte(qtype>>8))
	hash = fastHashRawByte(hash, byte(qtype))
	return hash
}

func fastQNameHashString(qname string, qtype uint16) uint64 {
	hash := uint64(fastQNameHashOffset64)
	if qname == "" {
		return fastQNameHashFinish(hash, qtype)
	}
	for i := 0; i < len(qname); i++ {
		hash = fastQNameHashByte(hash, qname[i])
	}
	if qname[len(qname)-1] != '.' {
		hash = fastQNameHashByte(hash, '.')
	}
	return fastQNameHashFinish(hash, qtype)
}

func fastASCIIEqualFold(a, b byte) bool {
	if a >= 'A' && a <= 'Z' {
		a += 'a' - 'A'
	}
	if b >= 'A' && b <= 'Z' {
		b += 'a' - 'A'
	}
	return a == b
}

func fastQNameTrimDotLen(name string) int {
	if len(name) > 1 && name[len(name)-1] == '.' {
		return len(name) - 1
	}
	return len(name)
}

func fastQNameEqualString(a, b string) bool {
	if a == b {
		return true
	}
	la := fastQNameTrimDotLen(a)
	lb := fastQNameTrimDotLen(b)
	if la != lb {
		return false
	}
	for i := 0; i < la; i++ {
		if !fastASCIIEqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

func fastQNameWireEqualString(wire []byte, qname string) bool {
	if len(wire) == 0 {
		return false
	}
	if len(wire) == 1 && wire[0] == 0 {
		return qname == "."
	}

	offset := 0
	nameOffset := 0
	for offset < len(wire) {
		labelLen := int(wire[offset])
		if labelLen == 0 {
			return nameOffset == len(qname)
		}
		if labelLen&0xC0 != 0 {
			return false
		}
		offset++
		if offset+labelLen > len(wire) || nameOffset+labelLen > len(qname) {
			return false
		}
		for i := 0; i < labelLen; i++ {
			if !fastASCIIEqualFold(wire[offset+i], qname[nameOffset+i]) {
				return false
			}
		}
		offset += labelLen
		nameOffset += labelLen

		if offset < len(wire) && wire[offset] != 0 {
			if nameOffset >= len(qname) || qname[nameOffset] != '.' {
				return false
			}
			nameOffset++
			continue
		}
		if nameOffset < len(qname) {
			if qname[nameOffset] != '.' {
				return false
			}
			nameOffset++
		}
	}
	return false
}

func (fc *fastCache) Flush() {
	if fc == nil {
		return
	}
	table := fc.loadTable()
	if table != nil {
		for i := range table.buckets {
			for j := range table.buckets[i].slots {
				table.buckets[i].slots[j].Store(nil)
			}
		}
	}
	rules := fc.loadRuleTable()
	if rules != nil {
		for i := range rules.buckets {
			for j := range rules.buckets[i].slots {
				rules.buckets[i].slots[j].Store(nil)
			}
		}
	}
}

func (fc *fastCache) Len() int {
	if fc == nil {
		return 0
	}
	table := fc.loadTable()
	if table == nil {
		return 0
	}
	count := 0
	for i := range table.buckets {
		for j := range table.buckets[i].slots {
			if table.buckets[i].slots[j].Load() != nil {
				count++
			}
		}
	}
	return count
}

func (fc *fastCache) PurgeDomains(domains []string, qtypes []uint16) int {
	if fc == nil {
		return 0
	}
	table := fc.loadTable()
	if table == nil {
		return 0
	}
	domainSet := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		name := dns.Fqdn(strings.TrimSpace(domain))
		if name == "" || name == "." {
			continue
		}
		domainSet[name] = struct{}{}
	}
	if len(domainSet) == 0 {
		return 0
	}
	qtypeSet := make(map[uint16]struct{}, len(qtypes))
	for _, qtype := range qtypes {
		if qtype == 0 {
			continue
		}
		qtypeSet[qtype] = struct{}{}
	}
	purged := 0
	for i := range table.buckets {
		for j := range table.buckets[i].slots {
			slot := &table.buckets[i].slots[j]
			current := slot.Load()
			if current == nil {
				continue
			}
			if _, ok := domainSet[current.qname]; !ok {
				continue
			}
			if len(qtypeSet) > 0 {
				if _, ok := qtypeSet[current.qtype]; !ok {
					continue
				}
			}
			slot.Store(nil)
			purged++
		}
	}
	return purged
}

type fastHandler struct {
	next            server.Handler
	fc              *fastCache
	dm              DomainMapperPlugin
	rewriteRevision DomainMapperRevisionPlugin
	sw              SwitchPlugin
	fakeCacheSwitch SwitchPlugin
}

func (h *fastHandler) Handle(ctx context.Context, q *dns.Msg, meta server.QueryMeta, pack func(*dns.Msg) (*[]byte, error)) *[]byte {
	if meta.PreFastStaleRefresh {
		if payload := h.serveStaleWhileRefresh(ctx, q, meta, pack); payload != nil {
			return payload
		}
	}

	payload := h.next.Handle(ctx, q, meta, pack)
	h.storeFastResponse(q, meta, payload)
	return payload
}

func (h *fastHandler) serveStaleWhileRefresh(ctx context.Context, q *dns.Msg, meta server.QueryMeta, pack func(*dns.Msg) (*[]byte, error)) *[]byte {
	if !h.fastCacheEnabled() || q == nil || q.Opcode != dns.OpcodeQuery || len(q.Question) == 0 {
		return nil
	}
	if h.fc.shouldBypassDomainSet(meta.PreFastDomainSet) {
		return nil
	}
	question := q.Question[0]
	stalePayload := h.fc.CopyResponse(q.Id, question.Name, question.Qtype, h.allowFakeIPCache())
	if stalePayload == nil {
		if h.fc != nil && h.fc.stats != nil {
			h.fc.stats.refreshMiss.Add(1)
		}
		return nil
	}

	if h.fc != nil && h.fc.stats != nil {
		h.fc.stats.bypassStaleReply.Add(1)
	}
	go h.refreshExpiredCache(ctx, q.Copy(), meta, pack)
	return stalePayload
}

func (h *fastHandler) refreshExpiredCache(ctx context.Context, q *dns.Msg, meta server.QueryMeta, pack func(*dns.Msg) (*[]byte, error)) {
	meta.PreFastStaleRefresh = false
	payload := h.next.Handle(ctx, q, meta, pack)
	if payload == nil {
		if h.fc != nil && h.fc.stats != nil {
			h.fc.stats.refreshNoPayload.Add(1)
		}
		return
	}
	defer pool.ReleaseBuf(payload)
	if h.storeFastResponse(q, meta, payload) {
		if h.fc != nil && h.fc.stats != nil {
			h.fc.stats.refreshStore.Add(1)
		}
		return
	}
	if h.fc != nil && h.fc.stats != nil {
		h.fc.stats.refreshStoreSkip.Add(1)
	}
}

func (h *fastHandler) storeFastResponse(q *dns.Msg, meta server.QueryMeta, payload *[]byte) bool {
	if h.sw != nil && h.sw.GetValue() != "on" {
		return false
	}
	if payload == nil || (meta.PreFastFlags&(1<<39)) != 0 || q == nil || q.Opcode != dns.OpcodeQuery || len(q.Question) == 0 {
		return false
	}
	if !shouldStoreFastResponse(*payload) {
		return false
	}
	dsetName := meta.PreFastDomainSet
	if dsetName == "" && h.dm != nil {
		_, dsetName, _ = h.dm.FastMatch(q.Question[0].Name)
	}
	if h.fc.shouldBypassDomainSet(dsetName) {
		return false
	}
	fakeIP := isFakeIPResponse(*payload)
	if fakeIP && !h.allowFakeIPCache() {
		return false
	}
	return h.fc.storeWithRuleRevision(
		q.Question[0].Name,
		q.Question[0].Qtype,
		*payload,
		dsetName,
		fakeIP,
		meta.PreFastFlags,
		fastRuntimeRevisionParts(h.dm, h.rewriteRevision),
	)
}

func (h *fastHandler) fastCacheEnabled() bool {
	return h.sw == nil || h.sw.GetValue() == "on"
}

func (h *fastHandler) allowFakeIPCache() bool {
	return h.fakeCacheSwitch != nil && h.fakeCacheSwitch.GetValue() == "on"
}

func fastCacheRevisionOf(plugin any) fastRevisionValue {
	if plugin == nil {
		return fastRevisionValue{}
	}
	if provider, ok := plugin.(NumericCacheRevisionPlugin); ok && provider != nil {
		return fastNumericRevisionValue(provider.CacheRevisionUint64())
	}
	provider, ok := plugin.(DomainMapperRevisionPlugin)
	if !ok || provider == nil {
		return fastRevisionValue{}
	}
	return fastTextRevisionValue(provider.CacheRevision())
}

func fastRuntimeRevisionParts(dm DomainMapperPlugin, rewriteRevision DomainMapperRevisionPlugin) fastRuleRevision {
	dmRevision := fastCacheRevisionOf(dm)
	rewriteRev := fastCacheRevisionOf(rewriteRevision)
	return fastRuleRevision{domainMapper: dmRevision, rewrite: rewriteRev}
}

func fastRuleReject(reqLen int, buf []byte, qEnd int, qtype uint16, marks uint64, sw1, sw7 SwitchPlugin) (int, bool) {
	if sw1 != nil {
		sw1Val := sw1.GetValue()
		if (marks&(1<<1)) != 0 && sw1Val == "on" {
			return makeReject(reqLen, buf, qEnd, 3), true
		}
		if (marks&(1<<2)) != 0 && qtype == 1 && sw1Val == "on" {
			return makeReject(reqLen, buf, qEnd, 0), true
		}
		if (marks&(1<<3)) != 0 && qtype == 28 && sw1Val == "on" {
			return makeReject(reqLen, buf, qEnd, 0), true
		}
	}
	if sw7 != nil && (marks&(1<<5)) != 0 && sw7.GetValue() == "on" {
		return makeReject(reqLen, buf, qEnd, 3), true
	}
	return 0, false
}

func fastMarksFromList(marks []uint8) uint64 {
	var out uint64
	for _, v := range marks {
		if v < 64 {
			out |= (1 << v)
		}
	}
	return out
}

func Init(bp *coremain.BP, args any) (any, error) {
	a := args.(*Args)
	a.init()
	return StartServer(bp, a)
}

func StartServer(bp *coremain.BP, args *Args) (*UdpServer, error) {
	dh, err := server_utils.NewHandler(bp, args.Entry, args.EnableAudit)
	if err != nil {
		return nil, fmt.Errorf("failed to init dns handler, %w", err)
	}

	var dm DomainMapperPlugin
	if p := bp.Plugin("unified_matcher1"); p != nil {
		dm, _ = p.(DomainMapperPlugin)
	}
	var rewriteRevision DomainMapperRevisionPlugin
	if p := bp.Plugin("rewrite"); p != nil {
		rewriteRevision, _ = p.(DomainMapperRevisionPlugin)
	}

	var sw15 SwitchPlugin
	sw15 = findSwitchPlugin(bp, switchmeta.MustLookup("udp_fast_path"))
	swFake := findSwitchPlugin(bp, switchmeta.MustLookup("fakeip_cache"))

	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL:      time.Duration(args.FastCacheInternalTTL) * time.Second,
		staleRetry:       time.Duration(args.FastCacheStaleRetrySec) * time.Second,
		staleMax:         time.Duration(args.FastCacheStaleMaxSec) * time.Second,
		ttlMin:           args.FastCacheTTLMin,
		ttlMax:           args.FastCacheTTLMax,
		bypassDomainSets: args.FastCacheBypassDomainSets,
	}, stats)
	wrappedHandler := &fastHandler{next: dh, fc: fc, dm: dm, rewriteRevision: rewriteRevision, sw: sw15, fakeCacheSwitch: swFake}
	fastBypass := buildFastBypass(bp, fc, stats, time.Duration(args.FastBypassWarmupSec)*time.Second)

	socketOpt := server_utils.ListenerSocketOpts{
		SO_REUSEPORT: true,
		SO_RCVBUF:    2 * 1024 * 1024,
	}
	lc := net.ListenConfig{Control: server_utils.ListenerControl(socketOpt)}
	listenerWorkers := args.FastListenerWorkers
	if listenerWorkers < 1 {
		listenerWorkers = 1
	}
	conns := make([]net.PacketConn, 0, listenerWorkers)
	for i := 0; i < listenerWorkers; i++ {
		c, err := lc.ListenPacket(context.Background(), "udp", args.Listen)
		if err != nil {
			for _, existing := range conns {
				_ = existing.Close()
			}
			return nil, fmt.Errorf("failed to create socket worker %d/%d, %w", i+1, listenerWorkers, err)
		}
		conns = append(conns, c)
	}
	bp.L().Info("udp server started with extreme bypass",
		zap.Stringer("addr", conns[0].LocalAddr()),
		zap.Int("listener_workers", len(conns)),
	)
	if args.FastMetricsLogInterval > 0 {
		bp.AttachShutdown(func(done func(), closeSignal <-chan struct{}) {
			defer done()
			ticker := time.NewTicker(time.Duration(args.FastMetricsLogInterval) * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-closeSignal:
					return
				case <-ticker.C:
					s := stats.snapshot()
					bp.L().Debug("udp fast-path stats",
						zap.Uint64("bypass_requests", s.BypassRequests),
						zap.Uint64("bypass_bad_packet", s.BypassBadPacket),
						zap.Uint64("bypass_rule_reply", s.BypassRuleReply),
						zap.Uint64("bypass_cache_reply", s.BypassCacheReply),
						zap.Uint64("bypass_stale_reply", s.BypassStaleReply),
						zap.Uint64("bypass_warmup_skip", s.BypassWarmupSkip),
						zap.Uint64("refresh_requested", s.RefreshRequested),
						zap.Uint64("refresh_miss", s.RefreshMiss),
						zap.Uint64("refresh_no_payload", s.RefreshNoPayload),
						zap.Uint64("refresh_store", s.RefreshStore),
						zap.Uint64("refresh_store_skip", s.RefreshStoreSkip),
						zap.Uint64("cache_lookup", s.CacheLookup),
						zap.Uint64("cache_store", s.CacheStore),
						zap.Uint64("cache_hit", s.CacheHit),
						zap.Uint64("cache_miss", s.CacheMiss),
						zap.Uint64("cache_collision", s.CacheCollision),
						zap.Uint64("cache_expired", s.CacheExpired),
						zap.Uint64("cache_eviction", s.CacheEviction),
					)
				}
			}
		})
	}

	for _, c := range conns {
		udpConn, ok := c.(*net.UDPConn)
		if !ok {
			for _, existing := range conns {
				_ = existing.Close()
			}
			return nil, fmt.Errorf("unexpected packet conn type %T", c)
		}
		go func(c *net.UDPConn) {
			defer c.Close()
			err := server.ServeUDP(c, wrappedHandler, server.UDPServerOpts{
				Logger:     bp.L(),
				FastBypass: fastBypass,
			})
			bp.CloseWithErr(err)
		}(udpConn)
	}
	return &UdpServer{args: args, conns: conns, fc: fc}, nil
}

func buildFastBypass(bp *coremain.BP, fc *fastCache, stats *fastStats, warmup time.Duration) func(int, []byte, netip.AddrPort) (int, int, uint64, string, bool, bool) {
	var once sync.Once
	var sw15, sw5, sw6, sw1, sw7, clientProxyMode, fakeipCache SwitchPlugin
	var dm DomainMapperPlugin
	var rewriteRevision DomainMapperRevisionPlugin
	var clientWhitelist, clientBlacklist IPSetPlugin
	revisionTracked := false
	readyAt := time.Now().Add(warmup)

	return func(reqLen int, buf []byte, remoteAddr netip.AddrPort) (int, int, uint64, string, bool, bool) {
		if stats != nil {
			stats.bypassRequests.Add(1)
		}
		if warmup > 0 && time.Now().Before(readyAt) {
			if stats != nil {
				stats.bypassWarmupSkip.Add(1)
			}
			return server.FastActionContinue, 0, 0, "", false, false
		}
		once.Do(func() {
			sw15 = findSwitchPlugin(bp, switchmeta.MustLookup("udp_fast_path"))
			sw5 = findSwitchPlugin(bp, switchmeta.MustLookup("block_query_type"))
			sw6 = findSwitchPlugin(bp, switchmeta.MustLookup("block_ipv6"))
			sw1 = findSwitchPlugin(bp, switchmeta.MustLookup("block_response"))
			sw7 = findSwitchPlugin(bp, switchmeta.MustLookup("ad_block"))
			clientProxyMode = findSwitchPlugin(bp, switchmeta.MustLookup("client_proxy_mode"))
			fakeipCache = findSwitchPlugin(bp, switchmeta.MustLookup("fakeip_cache"))
			if p := bp.Plugin("unified_matcher1"); p != nil {
				dm, _ = p.(DomainMapperPlugin)
			}
			if p := bp.Plugin("rewrite"); p != nil {
				rewriteRevision, _ = p.(DomainMapperRevisionPlugin)
			}
			revisionTracked = dm != nil || rewriteRevision != nil
			if p := bp.Plugin("client_ip_whitelist"); p != nil {
				clientWhitelist, _ = p.(IPSetPlugin)
			}
			if p := bp.Plugin("client_ip_blacklist"); p != nil {
				clientBlacklist, _ = p.(IPSetPlugin)
			}
		})

		if sw15 == nil || sw15.GetValue() != "on" {
			return server.FastActionContinue, 0, 0, "", false, false
		}
		question, ok := parseFastQuestionMeta(reqLen, buf)
		if !ok {
			if stats != nil {
				stats.bypassBadPacket.Add(1)
			}
			return server.FastActionContinue, 0, 0, "", false, false
		}
		qtype := question.qtype
		qEnd := question.end

		if qtype == 6 || qtype == 12 || qtype == 65 {
			if sw5 != nil && sw5.GetValue() == "on" {
				if stats != nil {
					stats.bypassRuleReply.Add(1)
				}
				return server.FastActionReply, makeReject(reqLen, buf, qEnd, 0), 0, "", false, false
			}
		}
		if qtype == 28 {
			if sw6 != nil && sw6.GetValue() == "on" {
				if stats != nil {
					stats.bypassRuleReply.Add(1)
				}
				return server.FastActionReply, makeReject(reqLen, buf, qEnd, 0), 0, "", false, false
			}
		}

		var marks uint64
		whitelistMatch := false
		blacklistMatch := false
		clientListChecked := false
		if clientWhitelist != nil || clientBlacklist != nil {
			addr := remoteAddr.Addr().Unmap()
			if clientWhitelist != nil {
				whitelistMatch = clientWhitelist.Match(addr)
				clientListChecked = true
			}
			if clientBlacklist != nil {
				blacklistMatch = clientBlacklist.Match(addr)
				clientListChecked = true
			}
		}
		if clientListChecked {
			marks |= (1 << 48)
		}
		mode := "all"
		if clientProxyMode != nil {
			mode = clientProxyMode.GetValue()
		}

		if mode == "whitelist" && !whitelistMatch {
			marks |= (1 << 39)
		} else if mode == "blacklist" && blacklistMatch {
			marks |= (1 << 39)
		}

		var ruleRevision fastRuleRevision
		if revisionTracked {
			ruleRevision = fastRuntimeRevisionParts(dm, rewriteRevision)
		}
		hKey := question.hash
		qnameWire := question.qnameWire(buf)
		allowFakeIP := fakeipCache != nil && fakeipCache.GetValue() == "on"
		earlyCacheTried := false
		var dset string
		var dsetMatched bool
		ruleMetaHit := false
		if !ruleRevision.empty() && (marks&(1<<39)) == 0 {
			earlyCacheTried = true
			if stats != nil {
				stats.cacheLookup.Add(1)
			}
			action, rLen, ruleFlags, ds, staleRefresh := fc.getOrUpdatingWire(hKey, buf, qnameWire, qtype, allowFakeIP, ruleRevision)
			if action == server.FastActionReply {
				if rejectLen, ok := fastRuleReject(reqLen, buf, qEnd, qtype, ruleFlags, sw1, sw7); ok {
					if stats != nil {
						stats.bypassRuleReply.Add(1)
					}
					return server.FastActionReply, rejectLen, 0, "", false, false
				}
				if stats != nil {
					stats.bypassCacheReply.Add(1)
				}
				return action, rLen, 0, ds, false, false
			}
			if staleRefresh {
				return server.FastActionContinue, 0, marks | ruleFlags, ds, ds != "", true
			}
		}

		if dm != nil && !ruleRevision.domainMapper.empty() {
			if meta, ok := fc.getRuleMetaWire(hKey, qnameWire, ruleRevision.domainMapper); ok {
				marks |= meta.ruleFlags
				dset = meta.domainSet
				dsetMatched = meta.matched
				ruleMetaHit = true
			}
		}

		if dm != nil && !ruleMetaHit {
			qname := question.qnameString(buf)
			if mList, dsName, match := dm.FastMatch(qname); match {
				ruleFlags := fastMarksFromList(mList)
				marks |= ruleFlags
				dset = dsName
				dsetMatched = true
				fc.storeRuleMeta(qname, hKey, ruleRevision.domainMapper, ruleFlags, dset, true)
			} else {
				fc.storeRuleMeta(qname, hKey, ruleRevision.domainMapper, 0, "", false)
			}
		}

		if rejectLen, ok := fastRuleReject(reqLen, buf, qEnd, qtype, marks, sw1, sw7); ok {
			if stats != nil {
				stats.bypassRuleReply.Add(1)
			}
			return server.FastActionReply, rejectLen, 0, "", false, false
		}

		if !earlyCacheTried && (marks&(1<<39)) == 0 && !fc.shouldBypassDomainSet(dset) {
			if stats != nil {
				stats.cacheLookup.Add(1)
			}
			action, rLen, ruleFlags, ds, staleRefresh := fc.getOrUpdatingWire(hKey, buf, qnameWire, qtype, allowFakeIP, ruleRevision)
			if action == server.FastActionReply {
				if rejectLen, ok := fastRuleReject(reqLen, buf, qEnd, qtype, ruleFlags, sw1, sw7); ok {
					if stats != nil {
						stats.bypassRuleReply.Add(1)
					}
					return server.FastActionReply, rejectLen, 0, "", false, false
				}
				if stats != nil {
					stats.bypassCacheReply.Add(1)
				}
				return action, rLen, 0, ds, false, false
			}
			if staleRefresh {
				return server.FastActionContinue, 0, marks, dset, dsetMatched, true
			}
		}
		return server.FastActionContinue, 0, marks, dset, dsetMatched, false
	}
}

func findSwitchPlugin(bp *coremain.BP, def switchmeta.Definition) SwitchPlugin {
	if p := bp.Plugin(def.Name); p != nil {
		if sw, ok := p.(SwitchPlugin); ok {
			return sw
		}
	}
	return nil
}

func makeReject(reqLen int, buf []byte, offset int, rcode byte) int {
	if offset > reqLen {
		offset = reqLen
	}
	buf[2] |= 0x80
	buf[3] |= 0x80
	buf[3] = (buf[3] & 0xF0) | (rcode & 0x0F)
	buf[6], buf[7] = 0, 0
	buf[8], buf[9] = 0, 0
	buf[10], buf[11] = 0, 0
	return offset
}

type fastQuestion struct {
	qtype    uint16
	end      int
	qnameEnd int
	hash     uint64
}

func (q fastQuestion) qnameWire(buf []byte) []byte {
	if q.qnameEnd <= 12 || q.qnameEnd > len(buf) {
		return nil
	}
	return buf[12:q.qnameEnd]
}

func (q fastQuestion) qnameString(buf []byte) string {
	return fastQNameWireString(q.qnameWire(buf))
}

func parseFastQuestion(reqLen int, buf []byte) (qname string, qtype uint16, end int, ok bool) {
	question, ok := parseFastQuestionMeta(reqLen, buf)
	if !ok {
		return "", 0, 0, false
	}
	return question.qnameString(buf), question.qtype, question.end, true
}

func parseFastQuestionMeta(reqLen int, buf []byte) (fastQuestion, bool) {
	if reqLen < 12 {
		return fastQuestion{}, false
	}
	flags0 := buf[2]
	if flags0&0x80 != 0 {
		return fastQuestion{}, false
	}
	if ((flags0 >> 3) & 0x0f) != 0 {
		return fastQuestion{}, false
	}
	if binary.BigEndian.Uint16(buf[4:6]) != 1 {
		return fastQuestion{}, false
	}

	offset := 12
	hash := uint64(fastQNameHashOffset64)
	nameLen := 0
	terminated := false
	for offset < reqLen {
		l := int(buf[offset])
		if l == 0 {
			offset++
			if nameLen == 0 {
				hash = fastQNameHashByte(hash, '.')
				nameLen = 1
			}
			terminated = true
			break
		}
		if l&0xC0 != 0 {
			return fastQuestion{}, false
		}
		offset++
		if offset+l > reqLen || nameLen+l+1 > 256 {
			return fastQuestion{}, false
		}
		for i := 0; i < l; i++ {
			hash = fastQNameHashByte(hash, buf[offset+i])
		}
		hash = fastQNameHashByte(hash, '.')
		nameLen += l + 1
		offset += l
	}
	if !terminated || offset+4 > reqLen {
		return fastQuestion{}, false
	}
	qtype := binary.BigEndian.Uint16(buf[offset : offset+2])
	return fastQuestion{
		qtype:    qtype,
		end:      offset + 4,
		qnameEnd: offset,
		hash:     fastQNameHashFinish(hash, qtype),
	}, true
}

func fastQNameWireString(wire []byte) string {
	if len(wire) == 0 {
		return ""
	}
	var nameBuf [256]byte
	nameLen := 0
	offset := 0
	for offset < len(wire) {
		labelLen := int(wire[offset])
		if labelLen == 0 {
			if nameLen == 0 {
				return "."
			}
			return string(nameBuf[:nameLen])
		}
		if labelLen&0xC0 != 0 {
			return ""
		}
		offset++
		if offset+labelLen > len(wire) || nameLen+labelLen+1 > len(nameBuf) {
			return ""
		}
		copy(nameBuf[nameLen:], wire[offset:offset+labelLen])
		nameLen += labelLen
		nameBuf[nameLen] = '.'
		nameLen++
		offset += labelLen
	}
	return ""
}

func clampTTL(ttl, ttlMin, ttlMax uint32) uint32 {
	if ttlMax > 0 && ttl > ttlMax {
		ttl = ttlMax
	}
	if ttl < ttlMin {
		ttl = ttlMin
	}
	return ttl
}

func skipDNSName(msg []byte, offset int) (int, bool) {
	for {
		if offset >= len(msg) {
			return 0, false
		}
		l := msg[offset]
		if l == 0 {
			return offset + 1, true
		}
		if l&0xC0 == 0xC0 {
			if offset+1 >= len(msg) {
				return 0, false
			}
			return offset + 2, true
		}
		if l&0xC0 != 0 {
			return 0, false
		}
		offset++
		if offset+int(l) > len(msg) {
			return 0, false
		}
		offset += int(l)
	}
}

func findTTLOffsets(msg []byte) []int {
	if len(msg) < 12 {
		return nil
	}
	qdcount := binary.BigEndian.Uint16(msg[4:6])
	ancount := binary.BigEndian.Uint16(msg[6:8])
	nscount := binary.BigEndian.Uint16(msg[8:10])
	totalRRs := int(ancount) + int(nscount)
	if totalRRs == 0 {
		return nil
	}
	offset := 12
	for i := 0; i < int(qdcount); i++ {
		nextOffset, ok := skipDNSName(msg, offset)
		if !ok || nextOffset+4 > len(msg) {
			return nil
		}
		offset = nextOffset
		offset += 4
	}

	var offsets []int
	for i := 0; i < totalRRs; i++ {
		nextOffset, ok := skipDNSName(msg, offset)
		if !ok || nextOffset+10 > len(msg) {
			break
		}
		offset = nextOffset
		offset += 4
		offsets = append(offsets, offset)
		offset += 4
		rdlen := int(binary.BigEndian.Uint16(msg[offset : offset+2]))
		offset += 2
		if offset+rdlen > len(msg) {
			break
		}
		offset += rdlen
	}
	return offsets
}
