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
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	pcache "github.com/IrineSistiana/mosdns/v5/pkg/cache"
	"github.com/IrineSistiana/mosdns/v5/pkg/concurrent_map"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	_ "github.com/IrineSistiana/mosdns/v5/plugin/matcher/has_resp"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"gopkg.in/yaml.v3"
)

func boolPtr(v bool) *bool { return &v }

const purgeDomainRuntimeTestCacheSize = 3 * concurrent_map.MapShardSize

func Test_cachePlugin_Dump(t *testing.T) {
	c := NewCache(&Args{Size: 16 * dumpBlockSize}, Opts{}) // Big enough to create dump fragments.

	resp := new(dns.Msg)
	resp.SetQuestion("test.", dns.TypeA)

	// Fix: Pack the dns.Msg to []byte because item.resp is now []byte
	packedResp, err := resp.Pack()
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	hourLater := now.Add(time.Hour)
	v := &item{
		resp:           packedResp,
		storedUnixNano: now.UnixNano(),
		expireUnixNano: hourLater.UnixNano(),
	}

	// Fill the cache
	for i := 0; i < 32*dumpBlockSize; i++ {
		c.backend.Store(key(strconv.Itoa(i)), v, hourLater)
	}

	buf := new(bytes.Buffer)
	enw, err := c.writeDump(buf)
	if err != nil {
		t.Fatal(err)
	}
	enr, err := c.readDump(buf)
	if err != nil {
		t.Fatal(err)
	}

	if enw != enr {
		t.Fatalf("read err, wrote %d entries, read %d", enw, enr)
	}
}

func Test_cachePlugin_WALReplay(t *testing.T) {
	dir := t.TempDir()
	args := &Args{
		Size:            64,
		DumpFile:        filepath.Join(dir, "cache.dump"),
		DumpInterval:    3600,
		WALFile:         filepath.Join(dir, "cache.wal"),
		WALSyncInterval: 1,
	}

	c := NewCache(args, Opts{})
	defer c.backend.Close()
	if err := c.dumpCache(); err != nil {
		t.Fatal(err)
	}

	qCtx := testQueryContext(t, "wal.example.", net.IPv4(1, 1, 1, 1))
	if _, ok := c.saveRespToCache("wal-key", qCtx); !ok {
		t.Fatal("expected response to be cached")
	}
	if err := c.persistence.close(); err != nil {
		t.Fatal(err)
	}

	c2 := NewCache(args, Opts{})
	defer c2.Close()
	resp, lazy, _ := getRespFromCache("wal-key", c2.backend, 0, expiredMsgTtl)
	if resp == nil {
		t.Fatal("expected wal replay to restore cache entry")
	}
	if lazy {
		t.Fatal("expected restored response to be fresh")
	}
}

func Test_cachePlugin_WALReplayMultipleStores(t *testing.T) {
	dir := t.TempDir()
	args := &Args{
		Size:            purgeDomainRuntimeTestCacheSize,
		WALFile:         filepath.Join(dir, "cache.wal"),
		WALSyncInterval: 60,
	}

	c := NewCache(args, Opts{})
	qCtxA := testQueryContext(t, "wal-a.example.", net.IPv4(1, 1, 1, 1))
	qCtxB := testQueryContext(t, "wal-b.example.", net.IPv4(2, 2, 2, 2))

	keyABuf, ptrA := getMsgKeyBytes(qCtxA.Q(), qCtxA, false, nil)
	keyBBuf, ptrB := getMsgKeyBytes(qCtxB.Q(), qCtxB, false, nil)
	keyA := string(keyABuf)
	keyB := string(keyBBuf)
	releaseKeyBuffer(ptrA)
	releaseKeyBuffer(ptrB)

	if _, ok := c.saveRespToCache(keyA, qCtxA); !ok {
		t.Fatal("expected first response to be cached")
	}
	if _, ok := c.saveRespToCache(keyB, qCtxB); !ok {
		t.Fatal("expected second response to be cached")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2 := NewCache(args, Opts{})
	defer c2.Close()
	if resp, _, _ := getRespFromCache(keyA, c2.backend, 0, expiredMsgTtl); resp == nil {
		t.Fatal("expected first wal entry to be restored")
	}
	if resp, _, _ := getRespFromCache(keyB, c2.backend, 0, expiredMsgTtl); resp == nil {
		t.Fatal("expected second wal entry to be restored")
	}
}

func Test_cachePlugin_WALReplayDelete(t *testing.T) {
	dir := t.TempDir()
	args := &Args{
		Size:            purgeDomainRuntimeTestCacheSize,
		WALFile:         filepath.Join(dir, "cache.wal"),
		WALSyncInterval: 60,
	}

	c := NewCache(args, Opts{})
	qCtxPurge := testQueryContext(t, "wal-purge.example.", net.IPv4(3, 3, 3, 3))
	qCtxKeep := testQueryContext(t, "wal-keep.example.", net.IPv4(4, 4, 4, 4))

	keyPurgeBuf, ptrPurge := getMsgKeyBytes(qCtxPurge.Q(), qCtxPurge, false, nil)
	keyKeepBuf, ptrKeep := getMsgKeyBytes(qCtxKeep.Q(), qCtxKeep, false, nil)
	keyPurge := string(keyPurgeBuf)
	keyKeep := string(keyKeepBuf)
	releaseKeyBuffer(ptrPurge)
	releaseKeyBuffer(ptrKeep)

	if _, ok := c.saveRespToCache(keyPurge, qCtxPurge); !ok {
		t.Fatal("expected purge response to be cached")
	}
	if _, ok := c.saveRespToCache(keyKeep, qCtxKeep); !ok {
		t.Fatal("expected keep response to be cached")
	}

	purged, err := c.PurgeDomainRuntime(context.Background(), "wal-purge.example", 0)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 1 {
		t.Fatalf("expected one purged entry, got %d", purged)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2 := NewCache(args, Opts{})
	defer c2.Close()
	if resp, _, _ := getRespFromCache(keyPurge, c2.backend, 0, expiredMsgTtl); resp != nil {
		t.Fatal("expected deleted wal entry to stay deleted after replay")
	}
	if resp, _, _ := getRespFromCache(keyKeep, c2.backend, 0, expiredMsgTtl); resp == nil {
		t.Fatal("expected unrelated wal entry to remain")
	}
}

func Test_cachePlugin_WALReplayFlush(t *testing.T) {
	dir := t.TempDir()
	args := &Args{
		Size:            purgeDomainRuntimeTestCacheSize,
		WALFile:         filepath.Join(dir, "cache.wal"),
		WALSyncInterval: 60,
	}

	c := NewCache(args, Opts{})
	qCtxBefore := testQueryContext(t, "wal-before-flush.example.", net.IPv4(5, 5, 5, 5))
	qCtxAfter := testQueryContext(t, "wal-after-flush.example.", net.IPv4(6, 6, 6, 6))

	keyBeforeBuf, ptrBefore := getMsgKeyBytes(qCtxBefore.Q(), qCtxBefore, false, nil)
	keyAfterBuf, ptrAfter := getMsgKeyBytes(qCtxAfter.Q(), qCtxAfter, false, nil)
	keyBefore := string(keyBeforeBuf)
	keyAfter := string(keyAfterBuf)
	releaseKeyBuffer(ptrBefore)
	releaseKeyBuffer(ptrAfter)

	if _, ok := c.saveRespToCache(keyBefore, qCtxBefore); !ok {
		t.Fatal("expected before-flush response to be cached")
	}
	if err := c.FlushRuntimeCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.saveRespToCache(keyAfter, qCtxAfter); !ok {
		t.Fatal("expected after-flush response to be cached")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2 := NewCache(args, Opts{})
	defer c2.Close()
	if resp, _, _ := getRespFromCache(keyBefore, c2.backend, 0, expiredMsgTtl); resp != nil {
		t.Fatal("expected flushed wal entry to stay deleted after replay")
	}
	if resp, _, _ := getRespFromCache(keyAfter, c2.backend, 0, expiredMsgTtl); resp == nil {
		t.Fatal("expected post-flush wal entry to remain")
	}
}

func Test_cachePlugin_WALOnlyDumpDoesNotResetWAL(t *testing.T) {
	dir := t.TempDir()
	args := &Args{
		Size:            purgeDomainRuntimeTestCacheSize,
		WALFile:         filepath.Join(dir, "cache.wal"),
		WALSyncInterval: 60,
	}

	c := NewCache(args, Opts{})
	qCtx := testQueryContext(t, "wal-only-dump.example.", net.IPv4(7, 7, 7, 7))
	keyBuf, ptr := getMsgKeyBytes(qCtx.Q(), qCtx, false, nil)
	msgKey := string(keyBuf)
	releaseKeyBuffer(ptr)

	if _, ok := c.saveRespToCache(msgKey, qCtx); !ok {
		t.Fatal("expected response to be cached")
	}
	if err := c.dumpCache(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2 := NewCache(args, Opts{})
	defer c2.Close()
	if resp, _, _ := getRespFromCache(msgKey, c2.backend, 0, expiredMsgTtl); resp == nil {
		t.Fatal("expected wal-only dump not to reset wal")
	}
}

func Test_getRespFromCache_NoLazyStaleForDDNS(t *testing.T) {
	backend := pcache.New[key, *item](pcache.Opts{Size: 64})
	defer backend.Close()

	msg := new(dns.Msg)
	msg.SetQuestion("ddns.example.", dns.TypeA)
	msg.Answer = append(msg.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: "ddns.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   net.IPv4(1, 2, 3, 4),
	})
	packed, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	backend.Store("ddns-key", &item{
		resp:           packed,
		storedUnixNano: now.Add(-10 * time.Minute).UnixNano(),
		expireUnixNano: now.Add(-1 * time.Minute).UnixNano(),
		domainSet:      "DDNS域名",
	}, now.Add(time.Hour))

	resp, lazy, domainSet := getRespFromCache("ddns-key", backend, 3600, expiredMsgTtl)
	if resp != nil || lazy || domainSet != "" {
		t.Fatalf("expected ddns stale cache to be bypassed, got resp=%v lazy=%v domainSet=%q", resp != nil, lazy, domainSet)
	}
}

func Test_cachePlugin_NoLazyStaleForConfiguredBypassDomainSet(t *testing.T) {
	c := NewCache(&Args{Size: 64, LazyStaleTTL: 3600, BypassDomainSets: []string{"高变化域名"}}, Opts{})
	defer c.Close()

	const qname = "high-churn-stale.example."
	qCtx := testQueryContext(t, qname, net.IPv4(1, 2, 3, 4))
	msgKey := cacheKeyForQuery(t, qname)
	cachedItem, ok := c.saveRespToCache(msgKey, qCtx)
	if !ok {
		t.Fatal("expected seed response to be cached before bypass policy changed")
	}
	now := time.Now()
	cachedItem.expireUnixNano = now.Add(-time.Second).UnixNano()
	cachedItem.domainSet = encodeStoredRouteMetadata("未命中|高变化域名", "未命中|高变化域名", "")

	resp, lazy, domainSet, corrupt := c.respFromCacheItem(cachedItem, c.args.LazyStaleTTL, expiredMsgTtl)
	if corrupt {
		t.Fatal("unexpected corrupt cached response")
	}
	if resp != nil || lazy || domainSet != "" {
		t.Fatalf("expected configured bypass domain set to skip lazy stale response, resp=%v lazy=%v domainSet=%q", resp != nil, lazy, domainSet)
	}
}

func Test_cachePlugin_RespFromNilCacheItemIsMiss(t *testing.T) {
	c := NewCache(&Args{Size: 64, DomainSetTTL: []DomainSetTTLPolicy{{
		Sets:         []string{"高变化域名"},
		LazyCacheTTL: 43200,
		LazyStaleTTL: 43200,
	}}}, Opts{})
	defer c.Close()

	resp, lazy, domainSet, corrupt := c.respFromCacheItem(nil, 3600, expiredMsgTtl)
	if resp != nil || lazy || domainSet != "" || corrupt {
		t.Fatalf("expected nil item to be cache miss, resp=%v lazy=%v domainSet=%q corrupt=%v", resp != nil, lazy, domainSet, corrupt)
	}
}

func Test_getRespFromCache_LazyStaleWindow(t *testing.T) {
	backend := pcache.New[key, *item](pcache.Opts{Size: 64})
	defer backend.Close()

	msg := new(dns.Msg)
	msg.SetQuestion("stale-window.example.", dns.TypeA)
	msg.Answer = append(msg.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: "stale-window.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   net.IPv4(1, 2, 3, 4),
	})
	packed, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	backend.Store("stale-window-key", &item{
		resp:           packed,
		storedUnixNano: now.Add(-10 * time.Minute).UnixNano(),
		expireUnixNano: now.Add(-1 * time.Minute).UnixNano(),
	}, now.Add(time.Hour))

	resp, lazy, _ := getRespFromCache("stale-window-key", backend, 1800, expiredMsgTtl)
	if resp == nil || !lazy {
		t.Fatalf("expected stale response inside lazy_stale_ttl window, got resp=%v lazy=%v", resp != nil, lazy)
	}
	if len(resp.Answer) != 1 || resp.Answer[0].Header().Ttl != expiredMsgTtl {
		t.Fatalf("expected stale response ttl %d, got %+v", expiredMsgTtl, resp.Answer)
	}

	resp, lazy, _ = getRespFromCache("stale-window-key", backend, 30, expiredMsgTtl)
	if resp != nil || lazy {
		t.Fatalf("expected stale response outside lazy_stale_ttl window to miss, got resp=%v lazy=%v", resp != nil, lazy)
	}
	if stored, _, _ := backend.Get("stale-window-key"); stored == nil {
		t.Fatal("expected backend entry to remain after stale window miss")
	}
}

func Test_cacheArgs_LazyStaleTTLCompatibility(t *testing.T) {
	var legacy Args
	if err := yaml.Unmarshal([]byte("lazy_cache_ttl: 42\n"), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.init()
	if legacy.LazyStaleTTL != 42 {
		t.Fatalf("expected omitted lazy_stale_ttl to inherit lazy_cache_ttl, got %d", legacy.LazyStaleTTL)
	}

	var explicitDisabled Args
	if err := yaml.Unmarshal([]byte("lazy_cache_ttl: 42\nlazy_stale_ttl: 0\n"), &explicitDisabled); err != nil {
		t.Fatal(err)
	}
	explicitDisabled.init()
	if explicitDisabled.LazyStaleTTL != 0 {
		t.Fatalf("expected explicit lazy_stale_ttl=0 to stay disabled, got %d", explicitDisabled.LazyStaleTTL)
	}
}

func Test_cacheArgs_ColdQueryWaitDefault(t *testing.T) {
	var args Args
	args.init()
	if got := time.Duration(args.ColdQueryWaitMs) * time.Millisecond; got != defaultColdQueryWait {
		t.Fatalf("default cold query wait = %s, want %s", got, defaultColdQueryWait)
	}
}

func Test_shouldBypassForRouteChange(t *testing.T) {
	if shouldBypassForRouteChange("", "", "", func(string) any {
		return &testCacheSwitchProvider{value: "on"}
	}) {
		t.Fatal("expected metadata-free cache entry to stay valid across route signature checks")
	}
	if !shouldBypassForRouteChange("", "白名单", "", nil) {
		t.Fatal("expected metadata-free cache entry to be bypassed when current request has an explicit route")
	}
	if !shouldBypassForRouteChange("", "", "chain:domestic-real", nil) {
		t.Fatal("expected metadata-free cache entry to be bypassed when current request has a hidden route dependency")
	}
	if shouldBypassForRouteChange(encodeStoredRouteMetadata("记忆直连|白名单", "记忆直连|白名单", "白名单|记忆直连"), "白名单|记忆直连", "", nil) {
		t.Fatal("expected reordered tags to share the same signature")
	}
	if !shouldBypassForRouteChange(encodeStoredRouteMetadata("记忆直连", "记忆直连", "记忆直连"), "未命中", "", nil) {
		t.Fatal("expected route change to bypass cached entry")
	}
	if !shouldBypassForRouteChange(encodeStoredRouteMetadata("未命中", "chain:domestic-real", "chain:domestic-real"), "未命中", "chain:foreign-real", nil) {
		t.Fatal("expected hidden route dependency change to bypass cached entry")
	}
}

func Test_cachePlugin_ExecBypassesStaleRouteCache(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	seedCtx := testQueryContext(t, "route-change.example.", net.IPv4(1, 1, 1, 1))
	seedCtx.StoreValue(query_context.KeyDomainSet, "记忆直连")

	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false, nil)
	msgKey := string(keyBuf)
	releaseKeyBuffer(bufPtr)

	if _, ok := c.saveRespToCache(msgKey, seedCtx); !ok {
		t.Fatal("expected seed response to be cached")
	}

	k := key(msgKey)
	stored, _, _ := c.backend.Get(k)
	if stored == nil {
		t.Fatal("expected stored cache item")
	}
	c.shards[k.Sum()%shardCount].updateL1(k, stored)

	qCtx := testQueryContext(t, "route-change.example.", net.IPv4(2, 2, 2, 2))
	qCtx.StoreValue(query_context.KeyDomainSet, "未命中")

	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	resp := qCtx.R()
	if resp == nil || len(resp.Answer) != 1 {
		t.Fatalf("expected response after cache exec, got %+v", resp)
	}
	gotA, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A response, got %T", resp.Answer[0])
	}
	if !gotA.A.Equal(net.IPv4(2, 2, 2, 2)) {
		t.Fatalf("expected stale cache bypass to keep fresh response, got %s", gotA.A.String())
	}

	updated, _, _ := c.backend.Get(k)
	if updated == nil {
		t.Fatal("expected old route cache entry to remain isolated")
	}
	if storedDomainSet(updated.domainSet) != "记忆直连" {
		t.Fatalf("expected old route entry to stay unchanged, got %q", storedDomainSet(updated.domainSet))
	}
	nextKeyBuf, nextBufPtr := getMsgKeyBytes(qCtx.Q(), qCtx, false, c.plugin)
	nextMsgKey := string(nextKeyBuf)
	releaseKeyBuffer(nextBufPtr)
	if nextMsgKey == msgKey {
		t.Fatal("expected display route to produce a distinct cache key")
	}
	nextItem, _, _ := c.backend.Get(key(nextMsgKey))
	if nextItem == nil {
		t.Fatal("expected current route cache entry to be written separately")
	}
	if storedDomainSet(nextItem.domainSet) != "未命中" {
		t.Fatalf("expected current route cache entry, got %q", storedDomainSet(nextItem.domainSet))
	}
}

func Test_cachePlugin_ExecBypassesSameRouteWhenRevisionChanges(t *testing.T) {
	provider := &testCacheRevisionProvider{revision: "rev1"}
	c := NewCache(&Args{Size: 64}, Opts{
		Plugin: func(tag string) any {
			if tag == "my_realiplist" {
				return provider
			}
			return nil
		},
	})
	defer c.Close()

	seedCtx := testQueryContext(t, "route-revision.example.", net.IPv4(1, 1, 1, 1))
	query_context.AppendDependencyTag(seedCtx, "my_realiplist")

	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false, nil)
	msgKey := string(keyBuf)
	releaseKeyBuffer(bufPtr)

	if _, ok := c.saveRespToCache(msgKey, seedCtx); !ok {
		t.Fatal("expected seed response to be cached")
	}

	provider.revision = "rev2"

	qCtx := testQueryContext(t, "route-revision.example.", net.IPv4(2, 2, 2, 2))
	query_context.AppendDependencyTag(qCtx, "my_realiplist")

	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	resp := qCtx.R()
	if resp == nil || len(resp.Answer) != 1 {
		t.Fatalf("expected response after cache exec, got %+v", resp)
	}
	gotA, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A response, got %T", resp.Answer[0])
	}
	if !gotA.A.Equal(net.IPv4(2, 2, 2, 2)) {
		t.Fatalf("expected revision mismatch to bypass cached entry, got %s", gotA.A.String())
	}

	oldItem, _, _ := c.backend.Get(key(msgKey))
	if oldItem == nil {
		t.Fatal("expected old revision cache entry to remain isolated")
	}
	if got := storedRouteSignature(oldItem.domainSet); !strings.Contains(got, "rev1") {
		t.Fatalf("expected old route signature to keep rev1, got %q", got)
	}
	rev2KeyBuf, rev2BufPtr := getMsgKeyBytes(qCtx.Q(), qCtx, false, c.plugin)
	rev2MsgKey := string(rev2KeyBuf)
	releaseKeyBuffer(rev2BufPtr)
	if rev2MsgKey == msgKey {
		t.Fatal("expected revision change to produce a distinct cache key")
	}
	updated, _, _ := c.backend.Get(key(rev2MsgKey))
	if updated == nil {
		t.Fatal("expected new revision cache entry to be written separately")
	}
	if storedDomainSet(updated.domainSet) != "" {
		t.Fatalf("expected empty display domain set for dependency-only cache, got %q", storedDomainSet(updated.domainSet))
	}
	if storedDependencySet(updated.domainSet) != "my_realiplist" {
		t.Fatalf("unexpected stored dependency set: %q", storedDependencySet(updated.domainSet))
	}
	if got := storedRouteSignature(updated.domainSet); !strings.Contains(got, "rev2") {
		t.Fatalf("expected updated route signature to include new revision, got %q", got)
	}
}

func Test_cachePlugin_ExecBypassesSameDisplayRouteWhenDependencyTagChanges(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	seedCtx := testQueryContext(t, "route-dependency.example.", net.IPv4(1, 1, 1, 1))
	seedCtx.StoreValue(query_context.KeyDomainSet, "未命中")
	query_context.AppendDependencyTag(seedCtx, "chain:domestic-real")

	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false, nil)
	msgKey := string(keyBuf)
	releaseKeyBuffer(bufPtr)

	if _, ok := c.saveRespToCache(msgKey, seedCtx); !ok {
		t.Fatal("expected seed response to be cached")
	}
	stored, _, _ := c.backend.Get(key(msgKey))
	if stored == nil {
		t.Fatal("expected stored cache item")
	}
	if got := storedDomainSet(stored.domainSet); got != "未命中" {
		t.Fatalf("display domain set = %q, want 未命中", got)
	}
	if got := storedDependencySet(stored.domainSet); got != "chain:domestic-real" {
		t.Fatalf("dependency set = %q, want hidden route tag", got)
	}

	qCtx := testQueryContext(t, "route-dependency.example.", net.IPv4(2, 2, 2, 2))
	qCtx.StoreValue(query_context.KeyDomainSet, "未命中")
	query_context.AppendDependencyTag(qCtx, "chain:foreign-real")

	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	resp := qCtx.R()
	if resp == nil || len(resp.Answer) != 1 {
		t.Fatalf("expected fresh response after dependency mismatch, got %+v", resp)
	}
	gotA, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A response, got %T", resp.Answer[0])
	}
	if !gotA.A.Equal(net.IPv4(2, 2, 2, 2)) {
		t.Fatalf("expected dependency mismatch to bypass cached entry, got %s", gotA.A.String())
	}
	oldItem, _, _ := c.backend.Get(key(msgKey))
	if oldItem == nil {
		t.Fatal("expected old route cache entry to remain isolated")
	}
	if got := storedDependencySet(oldItem.domainSet); got != "chain:domestic-real" {
		t.Fatalf("old dependency set = %q, want domestic hidden route tag", got)
	}

	foreignKeyBuf, foreignBufPtr := getMsgKeyBytes(qCtx.Q(), qCtx, false, c.plugin)
	foreignMsgKey := string(foreignKeyBuf)
	releaseKeyBuffer(foreignBufPtr)
	if foreignMsgKey == msgKey {
		t.Fatal("expected route dependency to produce a distinct cache key")
	}
	updated, _, _ := c.backend.Get(key(foreignMsgKey))
	if updated == nil {
		t.Fatal("expected foreign route cache entry to be written separately")
	}
	if got := storedDomainSet(updated.domainSet); got != "未命中" {
		t.Fatalf("updated display domain set = %q, want 未命中", got)
	}
	if got := storedDependencySet(updated.domainSet); got != "chain:foreign-real" {
		t.Fatalf("updated dependency set = %q, want foreign hidden route tag", got)
	}
}

func Test_cachePlugin_ExecBypassesSameRouteWhenSwitchChanges(t *testing.T) {
	answerMode := &testCacheSwitchProvider{value: "realip"}
	c := NewCache(&Args{Size: 64}, Opts{
		Plugin: func(tag string) any {
			if tag == "cn_answer_mode" {
				return answerMode
			}
			return nil
		},
	})
	defer c.Close()

	seedCtx := testQueryContext(t, "switch-route.example.", net.IPv4(1, 1, 1, 1))
	seedCtx.StoreValue(query_context.KeyDomainSet, "记忆直连")

	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false, nil)
	msgKey := string(keyBuf)
	releaseKeyBuffer(bufPtr)

	if _, ok := c.saveRespToCache(msgKey, seedCtx); !ok {
		t.Fatal("expected seed response to be cached")
	}
	stored, _, _ := c.backend.Get(key(msgKey))
	if stored == nil || !strings.Contains(storedRouteSignature(stored.domainSet), "switch:cn_answer_mode@realip") {
		t.Fatalf("expected stored signature to include realip switch, got %+v", stored)
	}

	answerMode.value = "fakeip"
	qCtx := testQueryContext(t, "switch-route.example.", net.IPv4(2, 2, 2, 2))
	qCtx.StoreValue(query_context.KeyDomainSet, "记忆直连")

	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	resp := qCtx.R()
	if resp == nil || len(resp.Answer) != 1 {
		t.Fatalf("expected response after switch change, got %+v", resp)
	}
	gotA, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A response, got %T", resp.Answer[0])
	}
	if !gotA.A.Equal(net.IPv4(2, 2, 2, 2)) {
		t.Fatalf("expected switch change to bypass cached entry, got %s", gotA.A.String())
	}
	oldItem, _, _ := c.backend.Get(key(msgKey))
	if oldItem == nil || !strings.Contains(storedRouteSignature(oldItem.domainSet), "switch:cn_answer_mode@realip") {
		t.Fatalf("expected old signature to stay realip, got %+v", oldItem)
	}
	fakeipKeyBuf, fakeipBufPtr := getMsgKeyBytes(qCtx.Q(), qCtx, false, c.plugin)
	fakeipMsgKey := string(fakeipKeyBuf)
	releaseKeyBuffer(fakeipBufPtr)
	if fakeipMsgKey == msgKey {
		t.Fatal("expected switch change to produce a distinct cache key")
	}
	updated, _, _ := c.backend.Get(key(fakeipMsgKey))
	if updated == nil || !strings.Contains(storedRouteSignature(updated.domainSet), "switch:cn_answer_mode@fakeip") {
		t.Fatalf("expected new signature to include fakeip switch, got %+v", updated)
	}
}

func Test_cachePlugin_ExecBypassesCachedResponseDuringRefresh(t *testing.T) {
	c := NewCache(&Args{Size: 64, LazyCacheTTL: 3600}, Opts{})
	defer c.Close()

	seedCtx := testQueryContext(t, "refresh-bypass.example.", net.IPv4(1, 1, 1, 1))
	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false, nil)
	msgKey := string(keyBuf)
	releaseKeyBuffer(bufPtr)

	cachedItem, ok := c.saveRespToCache(msgKey, seedCtx)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}

	now := time.Now()
	cachedItem.expireUnixNano = now.Add(-time.Second).UnixNano()
	k := key(msgKey)
	c.backend.Store(k, cachedItem, now.Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	qCtx := testQueryContext(t, "refresh-bypass.example.", net.IPv4(2, 2, 2, 2))
	markCacheRefreshBypass(qCtx)

	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	resp := qCtx.R()
	if resp == nil || len(resp.Answer) != 1 {
		t.Fatalf("expected response after cache refresh, got %+v", resp)
	}
	gotA, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A response, got %T", resp.Answer[0])
	}
	if !gotA.A.Equal(net.IPv4(2, 2, 2, 2)) {
		t.Fatalf("expected refresh bypass to keep fresh response, got %s", gotA.A.String())
	}

	updated, _, _ := c.backend.Get(k)
	refreshedResp, lazy, _, corrupt := respFromCacheItem(updated, 0, expiredMsgTtl)
	if corrupt {
		t.Fatal("expected refreshed cache entry to decode")
	}
	if lazy {
		t.Fatal("expected refreshed cache entry to be fresh")
	}
	if refreshedResp == nil || len(refreshedResp.Answer) != 1 {
		t.Fatalf("expected refreshed cache response, got %+v", refreshedResp)
	}
	refreshedA, ok := refreshedResp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected refreshed A response, got %T", refreshedResp.Answer[0])
	}
	if !refreshedA.A.Equal(net.IPv4(2, 2, 2, 2)) {
		t.Fatalf("expected cache to store refreshed response, got %s", refreshedA.A.String())
	}
}

func Test_cachePlugin_LazyRefreshBypassesNestedCache(t *testing.T) {
	mainCache := NewCache(&Args{Size: 64, LazyCacheTTL: 3600}, Opts{})
	defer mainCache.Close()
	branchCache := NewCache(&Args{Size: 64, LazyCacheTTL: 3600}, Opts{})
	defer branchCache.Close()

	const qname = "nested-refresh-bypass.example."
	seedStaleCacheEntry(t, mainCache, qname, net.IPv4(1, 1, 1, 1))
	seedStaleCacheEntry(t, branchCache, qname, net.IPv4(1, 1, 1, 1))

	raw := &testResponseExec{ip: net.IPv4(2, 2, 2, 2)}
	plugins := map[string]any{
		"main_cache":   mainCache,
		"branch_cache": branchCache,
		"raw":          raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", m)), []sequence.RuleArgs{
		{Exec: "$main_cache"},
		{Exec: "$branch_cache"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	q := new(dns.Msg)
	q.SetQuestion(qname, dns.TypeA)
	qCtx := query_context.NewContext(q)
	if err := s.Exec(context.Background(), qCtx); err != nil && !errors.Is(err, sequence.ErrExit) {
		t.Fatalf("Exec: %v", err)
	}

	mainKey := cacheKeyForQuery(t, qname)
	var refreshedResp *dns.Msg
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, _, _ := mainCache.backend.Get(key(mainKey))
		refreshedResp, _, _, _ = respFromCacheItem(stored, 0, expiredMsgTtl)
		if responseHasA(refreshedResp, net.IPv4(2, 2, 2, 2)) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := raw.calls.Load(); got == 0 {
		t.Fatal("expected nested cache refresh to reach raw executor")
	}
	t.Fatalf("expected main cache lazy refresh to store raw response, got %+v", refreshedResp)
}

func Test_cachePlugin_LazyStaleHitReturnsImmediatelyDuringRefresh(t *testing.T) {
	c := NewCache(&Args{Size: 64, LazyCacheTTL: 3600, LazyStaleTTL: 3600}, Opts{})
	defer c.Close()

	const qname = "lazy-no-wait.example."
	staleIP := net.IPv4(1, 1, 1, 1)
	freshIP := net.IPv4(2, 2, 2, 2)
	seedStaleCacheEntry(t, c, qname, staleIP)

	raw := &testResponseExec{
		ip:    freshIP,
		delay: defaultLazyWaitTimeout + 150*time.Millisecond,
	}
	plugins := map[string]any{
		"cache": c,
		"raw":   raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", m)), []sequence.RuleArgs{
		{Exec: "$cache"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	first := queryThroughSequence(t, s, qname)
	if !responseHasA(first.R(), staleIP) {
		t.Fatalf("expected first stale response, got %+v", first.R())
	}
	if !responseFromStaleCache(first) {
		t.Fatal("expected first response to be marked stale")
	}

	start := time.Now()
	second := queryThroughSequence(t, s, qname)
	elapsed := time.Since(start)
	if !responseHasA(second.R(), staleIP) {
		t.Fatalf("expected second stale response while refresh is running, got %+v", second.R())
	}
	if !responseFromStaleCache(second) {
		t.Fatal("expected second response to be marked stale")
	}
	if elapsed >= defaultLazyWaitTimeout/2 {
		t.Fatalf("expected stale response to return without waiting for lazy refresh, elapsed=%s", elapsed)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, _, _ := c.backend.Get(key(cacheKeyForQuery(t, qname)))
		refreshedResp, lazy, _, corrupt := respFromCacheItem(stored, 0, expiredMsgTtl)
		if !corrupt && !lazy && responseHasA(refreshedResp, freshIP) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected background refresh to eventually store fresh response, raw_calls=%d", raw.calls.Load())
}

func Test_cachePlugin_BackendRetainedExpiredEntryStaysHot(t *testing.T) {
	c := NewCache(&Args{Size: 64, LazyCacheTTL: 900, LazyStaleTTL: 900, ClientTTLMin: 120, ClientTTLMax: 900}, Opts{})
	defer c.Close()

	const qname = "backend-retained-stale.example."
	staleIP := net.IPv4(10, 10, 10, 1)
	freshIP := net.IPv4(10, 10, 10, 2)
	msgKey := cacheKeyForQuery(t, qname)
	seed := testQueryContextWithTTL(t, qname, staleIP, 5)
	cachedItem, ok := c.saveRespToCache(msgKey, seed)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}

	now := time.Now()
	cachedItem.storedUnixNano = now.Add(-5 * time.Minute).UnixNano()
	cachedItem.expireUnixNano = now.Add(-time.Second).UnixNano()
	cachedItem.refreshTTLOffsets()
	k := key(msgKey)
	c.backend.Store(k, cachedItem, now.Add(15*time.Minute))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	raw := &testResponseExec{
		ip:    freshIP,
		delay: 250 * time.Millisecond,
	}
	plugins := map[string]any{
		"cache": c,
		"raw":   raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", m)), []sequence.RuleArgs{
		{Exec: "$cache"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	first := queryThroughSequence(t, s, qname)
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Fatalf("expected backend-retained stale cache to return immediately, elapsed=%s", elapsed)
	}
	if !responseHasA(first.R(), staleIP) {
		t.Fatalf("expected first response from stale cache, got %+v", first.R())
	}
	if !responseFromStaleCache(first) {
		t.Fatal("expected backend-retained response to be marked stale")
	}
	waitForRawCalls(t, raw, 1, 100*time.Millisecond)

	second := queryThroughSequence(t, s, qname)
	if !responseHasA(second.R(), staleIP) {
		t.Fatalf("expected concurrent stale response while refresh runs, got %+v", second.R())
	}
	if got := raw.calls.Load(); got != 1 {
		t.Fatalf("expected lazy refresh to be singleflight, got raw calls=%d", got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, _, _ := c.backend.Get(k)
		refreshedResp, lazy, _, corrupt := respFromCacheItem(stored, 0, expiredMsgTtl)
		if !corrupt && !lazy && responseHasA(refreshedResp, freshIP) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected background refresh to replace stale entry, raw_calls=%d", raw.calls.Load())
}

func Test_cachePlugin_CachedUDPHitSetsWirePayload(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	const qname = "wire-hit.example."
	seed := testQueryContext(t, qname, net.IPv4(10, 0, 0, 1))
	msgKey := cacheKeyForQuery(t, qname)
	cachedItem, ok := c.saveRespToCache(msgKey, seed)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}
	cachedItem.storedUnixNano = time.Now().Add(-10 * time.Second).UnixNano()
	cachedItem.refreshTTLOffsets()
	k := key(msgKey)
	c.backend.Store(k, cachedItem, time.Now().Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	q := new(dns.Msg)
	q.SetQuestion(qname, dns.TypeA)
	q.Id = 0xBEEF
	qCtx := query_context.NewContext(q)
	qCtx.ServerMeta.FromUDP = true

	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil && !errors.Is(err, sequence.ErrExit) {
		t.Fatal(err)
	}
	payload := qCtx.ResponsePayload()
	if payload == nil || len(payload.Wire) == 0 {
		t.Fatal("expected cached UDP hit to set wire payload")
	}
	if got := binary.BigEndian.Uint16(payload.Wire[:2]); got != q.Id {
		t.Fatalf("wire txid = %#x, want %#x", got, q.Id)
	}
	if payload.Wire[3]&0x80 == 0 {
		t.Fatal("expected cached wire payload to set recursion-available bit")
	}
	resp := new(dns.Msg)
	if err := resp.Unpack(payload.Wire); err != nil {
		t.Fatalf("unpack payload: %v", err)
	}
	if !responseHasA(resp, net.IPv4(10, 0, 0, 1)) {
		t.Fatalf("expected cached A response, got %+v", resp)
	}
	if len(resp.Answer) != 1 || resp.Answer[0].Header().Ttl >= 60 {
		t.Fatalf("expected payload TTL to be reduced, got %+v", resp.Answer)
	}
}

func Test_cachePlugin_ClientTTLClampAppliesToMessageAndWirePayload(t *testing.T) {
	c := NewCache(&Args{Size: 64, ClientTTLMin: 600, ClientTTLMax: 600}, Opts{})
	defer c.Close()

	const qname = "client-ttl.example."
	seed := testQueryContext(t, qname, net.IPv4(28, 0, 1, 11))
	msgKey := cacheKeyForQuery(t, qname)
	cachedItem, ok := c.saveRespToCache(msgKey, seed)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}
	cachedItem.storedUnixNano = time.Now().Add(-59 * time.Second).UnixNano()
	cachedItem.expireUnixNano = time.Now().Add(time.Second).UnixNano()
	cachedItem.refreshTTLOffsets()
	k := key(msgKey)
	c.backend.Store(k, cachedItem, time.Now().Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	q := new(dns.Msg)
	q.SetQuestion(qname, dns.TypeA)
	q.Id = 0xCAFE
	qCtx := query_context.NewContext(q)
	qCtx.ServerMeta.FromUDP = true

	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil && !errors.Is(err, sequence.ErrExit) {
		t.Fatal(err)
	}
	payload := qCtx.ResponsePayload()
	if payload == nil || len(payload.Wire) == 0 {
		t.Fatal("expected cached UDP hit to set wire payload")
	}
	var wire dns.Msg
	if err := wire.Unpack(payload.Wire); err != nil {
		t.Fatalf("unpack payload: %v", err)
	}
	if len(wire.Answer) != 1 || wire.Answer[0].Header().Ttl != 600 {
		t.Fatalf("expected cached wire TTL to be clamped to 600, got %+v", wire.Answer)
	}
}

func Test_cachePlugin_ClientTTLMinExtendsFreshWindow(t *testing.T) {
	c := NewCache(&Args{Size: 64, LazyCacheTTL: 5, ClientTTLMin: 120, ClientTTLMax: 900}, Opts{})
	defer c.Close()

	const qname = "client-ttl-fresh-window.example."
	seed := testQueryContextWithTTL(t, qname, net.IPv4(1, 2, 3, 4), 5)
	msgKey := cacheKeyForQuery(t, qname)
	cachedItem, ok := c.saveRespToCache(msgKey, seed)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}

	freshWindow := time.Duration(cachedItem.expireUnixNano-cachedItem.storedUnixNano) * time.Nanosecond
	if freshWindow < 120*time.Second {
		t.Fatalf("expected client_ttl_min to extend cache fresh window to at least 120s, got %s", freshWindow)
	}
	if cachedItemTTL, _, _, corrupt := c.respFromCacheItem(cachedItem, 0, expiredMsgTtl); corrupt || cachedItemTTL == nil || len(cachedItemTTL.Answer) != 1 || cachedItemTTL.Answer[0].Header().Ttl != 120 {
		t.Fatalf("expected cached response TTL to be governed to 120, corrupt=%v resp=%+v", corrupt, cachedItemTTL)
	}
}

func Test_cachePlugin_CachedUDPHitSkipsWirePayloadWhenTooLargeForPlainUDP(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	qCtx := testQueryContext(t, "large-wire-hit.example.", net.IPv4(10, 0, 0, 1))
	resp := qCtx.R()
	for i := 0; i < 40; i++ {
		resp.Answer = append(resp.Answer, &dns.TXT{
			Hdr: dns.RR_Header{
				Name:   fmt.Sprintf("txt-%02d.large-wire-hit.example.", i),
				Rrtype: dns.TypeTXT,
				Class:  dns.ClassINET,
				Ttl:    60,
			},
			Txt: []string{"0123456789abcdef0123456789abcdef"},
		})
	}
	msgKey := cacheKeyForQuery(t, "large-wire-hit.example.")
	cachedItem, ok := c.saveRespToCache(msgKey, qCtx)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}
	if len(cachedItem.resp) <= dns.MinMsgSize {
		t.Fatalf("test response is too small: %d", len(cachedItem.resp))
	}
	k := key(msgKey)
	c.backend.Store(k, cachedItem, time.Now().Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	q := new(dns.Msg)
	q.SetQuestion("large-wire-hit.example.", dns.TypeA)
	qCtx = query_context.NewContext(q)
	qCtx.ServerMeta.FromUDP = true

	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil && !errors.Is(err, sequence.ErrExit) {
		t.Fatal(err)
	}
	if payload := qCtx.ResponsePayload(); payload != nil {
		t.Fatal("expected oversized plain UDP cache hit to use normal pack/truncate path")
	}
	if qCtx.R() == nil {
		t.Fatal("expected cached response to remain available")
	}
}

func Test_cachePlugin_CachedUDPLargeHitUsesEDNSPayload(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	qCtx := testQueryContext(t, "large-edns-wire-hit.example.", net.IPv4(10, 0, 0, 1))
	resp := qCtx.R()
	for i := 0; i < 11; i++ {
		resp.Answer = append(resp.Answer, &dns.TXT{
			Hdr: dns.RR_Header{
				Name:   fmt.Sprintf("txt-%02d.large-edns-wire-hit.example.", i),
				Rrtype: dns.TypeTXT,
				Class:  dns.ClassINET,
				Ttl:    60,
			},
			Txt: []string{"0123456789abcdef0123456789abcdef0123456789abcdef"},
		})
	}
	msgKey := cacheKeyForQuery(t, "large-edns-wire-hit.example.")
	cachedItem, ok := c.saveRespToCache(msgKey, qCtx)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}
	if len(cachedItem.resp) <= dns.MinMsgSize || len(cachedItem.resp) >= 1232 {
		t.Fatalf("test response should require EDNS UDP payload path, got %d bytes", len(cachedItem.resp))
	}
	k := key(msgKey)
	c.backend.Store(k, cachedItem, time.Now().Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	q := new(dns.Msg)
	q.SetQuestion("large-edns-wire-hit.example.", dns.TypeA)
	q.SetEdns0(1232, false)
	qCtx = query_context.NewContext(q)
	qCtx.ServerMeta.FromUDP = true

	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil && !errors.Is(err, sequence.ErrExit) {
		t.Fatal(err)
	}
	payload := qCtx.ResponsePayload()
	if payload == nil || len(payload.Wire) == 0 {
		t.Fatal("expected EDNS UDP cache hit to use pre-packed payload")
	}
	if len(payload.Wire) <= dns.MinMsgSize {
		t.Fatalf("expected payload to stay above plain UDP size, got %d bytes", len(payload.Wire))
	}
	var wire dns.Msg
	if err := wire.Unpack(payload.Wire); err != nil {
		t.Fatalf("unpack payload: %v", err)
	}
	if opt := wire.IsEdns0(); opt == nil || opt.UDPSize() != 1200 {
		t.Fatalf("expected response OPT to be preserved with server udp size 1200, got %+v", opt)
	}
}

func Test_cachePlugin_CachedHitStopsFollowingExecutors(t *testing.T) {
	c := NewCache(&Args{Size: 64, ExitOnHit: true}, Opts{})
	defer c.Close()

	const qname = "hit-stops-next.example."
	seed := testQueryContext(t, qname, net.IPv4(10, 0, 0, 1))
	msgKey := cacheKeyForQuery(t, qname)
	cachedItem, ok := c.saveRespToCache(msgKey, seed)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}
	k := key(msgKey)
	c.backend.Store(k, cachedItem, time.Now().Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	childRaw := &testResponseExec{ip: net.IPv4(10, 0, 0, 2)}
	parentRaw := &testResponseExec{ip: net.IPv4(10, 0, 0, 3)}
	plugins := map[string]any{
		"cache":      c,
		"child_raw":  childRaw,
		"parent_raw": parentRaw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	child, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("child", m)), []sequence.RuleArgs{
		{Exec: "$cache"},
		{Exec: "$child_raw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	plugins["child"] = child
	parent, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("parent", m)), []sequence.RuleArgs{
		{Exec: "$child"},
		{Exec: "$parent_raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	qCtx := queryThroughSequence(t, parent, qname)
	if !responseHasA(qCtx.R(), net.IPv4(10, 0, 0, 1)) {
		t.Fatalf("expected cached response, got %+v", qCtx.R())
	}
	if got := childRaw.calls.Load(); got != 0 {
		t.Fatalf("child raw calls = %d, want 0 after terminal cache hit", got)
	}
	if got := parentRaw.calls.Load(); got != 0 {
		t.Fatalf("parent raw calls = %d, want 0 after terminal cache hit", got)
	}
}

func Test_cachePlugin_DefaultCachedHitAllowsParentSequenceToContinue(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	const qname = "hit-continues-next.example."
	seed := testQueryContext(t, qname, net.IPv4(10, 0, 0, 1))
	msgKey := cacheKeyForQuery(t, qname)
	cachedItem, ok := c.saveRespToCache(msgKey, seed)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}
	k := key(msgKey)
	c.backend.Store(k, cachedItem, time.Now().Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	childRaw := &testResponseExec{ip: net.IPv4(10, 0, 0, 2)}
	parentRaw := &testResponseExec{ip: net.IPv4(10, 0, 0, 3)}
	plugins := map[string]any{
		"cache":      c,
		"child_raw":  childRaw,
		"parent_raw": parentRaw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	child, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("child", m)), []sequence.RuleArgs{
		{Exec: "$cache"},
		{Exec: "$child_raw"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	plugins["child"] = child
	parent, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("parent", m)), []sequence.RuleArgs{
		{Exec: "$child"},
		{Exec: "$parent_raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	qCtx := queryThroughSequence(t, parent, qname)
	if !responseHasA(qCtx.R(), net.IPv4(10, 0, 0, 3)) {
		t.Fatalf("expected parent sequence to continue after default cache hit, got %+v", qCtx.R())
	}
	if got := childRaw.calls.Load(); got != 0 {
		t.Fatalf("child raw calls = %d, want 0 after cache hit", got)
	}
	if got := parentRaw.calls.Load(); got != 1 {
		t.Fatalf("parent raw calls = %d, want 1 after default cache hit", got)
	}
}

func Test_cachePlugin_ColdMissSingleflightSharesResponse(t *testing.T) {
	c := NewCache(&Args{Size: 64, ColdQueryWaitMs: 250}, Opts{})
	defer c.Close()

	const qname = "cold-share.example."
	raw := &testResponseExec{
		ip:    net.IPv4(10, 0, 0, 2),
		delay: 100 * time.Millisecond,
	}
	plugins := map[string]any{
		"cache": c,
		"raw":   raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", m)), []sequence.RuleArgs{
		{Exec: "$cache"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	errCh := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			qCtx := queryThroughSequence(t, s, qname)
			if !responseHasA(qCtx.R(), net.IPv4(10, 0, 0, 2)) {
				errCh <- fmt.Errorf("unexpected response: %+v", qCtx.R())
				return
			}
			errCh <- nil
		}()
	}
	close(start)
	for i := 0; i < workers; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if got := raw.calls.Load(); got != 1 {
		t.Fatalf("raw calls = %d, want 1", got)
	}
}

func Test_cachePlugin_ColdMissSharedWaitTimeoutFallsBackDirect(t *testing.T) {
	c := NewCache(&Args{Size: 64, ColdQueryWaitMs: 25}, Opts{})
	defer c.Close()

	const qname = "cold-share-timeout.example."
	raw := &testColdFallbackExec{
		ip:          net.IPv4(10, 0, 0, 4),
		leaderDelay: 180 * time.Millisecond,
	}
	plugins := map[string]any{
		"cache": c,
		"raw":   raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", m)), []sequence.RuleArgs{
		{Exec: "$cache"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		qCtx := queryThroughSequence(t, s, qname)
		if !responseHasA(qCtx.R(), net.IPv4(10, 0, 0, 4)) {
			errCh <- fmt.Errorf("leader unexpected response: %+v", qCtx.R())
			return
		}
		errCh <- nil
	}()
	waitForColdFallbackCalls(t, raw, 1, 100*time.Millisecond)

	start := time.Now()
	qCtx := queryThroughSequence(t, s, qname)
	elapsed := time.Since(start)
	if !responseHasA(qCtx.R(), net.IPv4(10, 0, 0, 4)) {
		t.Fatalf("waiter unexpected response: %+v", qCtx.R())
	}
	if elapsed >= 120*time.Millisecond {
		t.Fatalf("expected waiter to bypass blocked leader quickly, elapsed=%s", elapsed)
	}
	if got := raw.calls.Load(); got < 2 {
		t.Fatalf("raw calls = %d, want direct fallback call", got)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func Test_cachePlugin_ColdMissSingleflightFallsBackWhenUncacheable(t *testing.T) {
	c := NewCache(&Args{Size: 64, ExcludeIPs: []string{"10.0.0.0/24"}}, Opts{})
	defer c.Close()

	const qname = "cold-uncacheable.example."
	raw := &testResponseExec{
		ip:    net.IPv4(10, 0, 0, 3),
		delay: 100 * time.Millisecond,
	}
	plugins := map[string]any{
		"cache": c,
		"raw":   raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", m)), []sequence.RuleArgs{
		{Exec: "$cache"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 4
	errCh := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			qCtx := queryThroughSequence(t, s, qname)
			if !responseHasA(qCtx.R(), net.IPv4(10, 0, 0, 3)) {
				errCh <- fmt.Errorf("unexpected response: %+v", qCtx.R())
				return
			}
			errCh <- nil
		}()
	}
	close(start)
	for i := 0; i < workers; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if got := raw.calls.Load(); got < 2 {
		t.Fatalf("raw calls = %d, want fallback calls for uncacheable response", got)
	}
}

func Test_cachePlugin_ReadDumpSkipsExcludedResponses(t *testing.T) {
	src := NewCache(&Args{Size: 64}, Opts{})
	t.Cleanup(func() { _ = src.Close() })
	if _, ok := src.saveRespToCache("fake-key", testQueryContext(t, "fake.example.", net.IPv4(28, 0, 1, 2))); !ok {
		t.Fatal("expected fake response to be cached in source")
	}
	if _, ok := src.saveRespToCache("real-key", testQueryContext(t, "real.example.", net.IPv4(1, 1, 1, 1))); !ok {
		t.Fatal("expected real response to be cached in source")
	}

	buf := new(bytes.Buffer)
	if _, err := src.writeDump(buf); err != nil {
		t.Fatalf("writeDump: %v", err)
	}

	dst := NewCache(&Args{Size: 64, ExcludeIPs: []string{"28.0.0.0/8"}}, Opts{})
	t.Cleanup(func() { _ = dst.Close() })
	restored, err := dst.readDump(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("readDump: %v", err)
	}
	if restored != 1 {
		t.Fatalf("restored entries = %d, want 1", restored)
	}
	if _, _, ok := dst.backend.Get(key("fake-key")); ok {
		t.Fatal("expected fakeip response to be skipped when loading dump")
	}
	if _, _, ok := dst.backend.Get(key("real-key")); !ok {
		t.Fatal("expected real response to be restored from dump")
	}
}

func Test_cachePlugin_SaveResponseSkipsExcludedResponses(t *testing.T) {
	c := NewCache(&Args{Size: 64, ExcludeIPs: []string{"28.0.0.0/8", "f2b0::/18"}}, Opts{})
	t.Cleanup(func() { _ = c.Close() })

	if _, ok := c.saveRespToCache("fake-v4-key", testQueryContext(t, "fake-v4.example.", net.IPv4(28, 0, 1, 2))); ok {
		t.Fatal("expected fake IPv4 response to be skipped")
	}
	if _, _, ok := c.backend.Get(key("fake-v4-key")); ok {
		t.Fatal("expected fake IPv4 response not to enter backend cache")
	}

	if _, ok := c.saveRespToCache("real-key", testQueryContext(t, "real.example.", net.IPv4(223, 5, 5, 5))); !ok {
		t.Fatal("expected real response to be cached")
	}
	if _, _, ok := c.backend.Get(key("real-key")); !ok {
		t.Fatal("expected real response to enter backend cache")
	}
}

func Test_cachePlugin_ProbeSequenceHitsPayloadCache(t *testing.T) {
	c := NewCache(&Args{Size: 64, LazyCacheTTL: 600, ClientTTLMin: 120, ClientTTLMax: 900}, Opts{})
	defer c.Close()

	raw := &testResponseExec{ip: net.IPv4(8, 8, 8, 8)}
	plugins := map[string]any{
		"cache_probe": c,
		"raw":         raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("probe_seq", m)), []sequence.RuleArgs{
		{Exec: "$cache_probe"},
		{Matches: []string{"has_resp"}, Exec: "exit"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	qname := "probe-payload.example."
	first := queryThroughSequence(t, s, qname)
	if !responseHasA(first.ResponseMsg(), net.IPv4(8, 8, 8, 8)) {
		t.Fatalf("expected first response from raw executor, got %+v", first.ResponseMsg())
	}
	second := queryThroughSequence(t, s, qname)
	if !responseHasA(second.ResponseMsg(), net.IPv4(8, 8, 8, 8)) {
		t.Fatalf("expected second response from cache payload, got %+v", second.ResponseMsg())
	}
	if got := raw.calls.Load(); got != 1 {
		t.Fatalf("expected raw executor to run once, got %d", got)
	}
	if c.hitCount.Load() != 1 {
		t.Fatalf("expected one cache hit, got %d", c.hitCount.Load())
	}
}

func Test_cachePlugin_ProbeSequenceCachesAAAAFallbackResponse(t *testing.T) {
	c := NewCache(&Args{Size: 64, LazyCacheTTL: 600, ClientTTLMin: 120, ClientTTLMax: 900}, Opts{})
	defer c.Close()

	raw := &testAAAAResponseExec{ip: net.ParseIP("2001:db8::8")}
	plugins := map[string]any{
		"cache_probe": c,
		"raw":         raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("probe_seq", m)), []sequence.RuleArgs{
		{Exec: "$cache_probe"},
		{Matches: []string{"has_resp"}, Exec: "exit"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	first := queryThroughSequenceType(t, s, "probe-aaaa.example.", dns.TypeAAAA, true)
	if !responseHasAAAA(first.ResponseMsg(), net.ParseIP("2001:db8::8")) {
		t.Fatalf("expected first response from raw executor, got %+v", first.ResponseMsg())
	}
	second := queryThroughSequenceType(t, s, "probe-aaaa.example.", dns.TypeAAAA, true)
	if !responseHasAAAA(second.ResponseMsg(), net.ParseIP("2001:db8::8")) {
		t.Fatalf("expected second response from cache payload, got %+v", second.ResponseMsg())
	}
	if got := raw.calls.Load(); got != 1 {
		t.Fatalf("expected raw executor to run once, got %d", got)
	}
	if c.hitCount.Load() != 1 {
		t.Fatalf("expected one cache hit, got %d", c.hitCount.Load())
	}
}

func Test_cachePlugin_DoesNotPromoteNestedStaleResponse(t *testing.T) {
	mainCache := NewCache(&Args{Size: 64, LazyCacheTTL: 3600}, Opts{})
	defer mainCache.Close()
	branchCache := NewCache(&Args{Size: 64, LazyCacheTTL: 3600}, Opts{})
	defer branchCache.Close()

	const qname = "nested-stale-promote.example."
	seedStaleCacheEntry(t, branchCache, qname, net.IPv4(1, 1, 1, 1))

	raw := &testResponseExec{ip: net.IPv4(2, 2, 2, 2)}
	plugins := map[string]any{
		"main_cache":   mainCache,
		"branch_cache": branchCache,
		"raw":          raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", m)), []sequence.RuleArgs{
		{Exec: "$main_cache"},
		{Exec: "$branch_cache"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	q := new(dns.Msg)
	q.SetQuestion(qname, dns.TypeA)
	qCtx := query_context.NewContext(q)
	if err := s.Exec(context.Background(), qCtx); err != nil && !errors.Is(err, sequence.ErrExit) {
		t.Fatalf("Exec: %v", err)
	}
	if !responseHasA(qCtx.R(), net.IPv4(1, 1, 1, 1)) {
		t.Fatalf("expected first response from branch stale cache, got %+v", qCtx.R())
	}

	mainKey := cacheKeyForQuery(t, qname)
	if stored, _, _ := mainCache.backend.Get(key(mainKey)); stored != nil {
		t.Fatal("expected main cache not to store stale response returned by nested cache")
	}
}

func Test_cachePlugin_LongRunLargeCacheAddressChangeDoesNotKeepOldAddress(t *testing.T) {
	mainCache := NewCache(&Args{Size: 400000, LazyCacheTTL: 21600, LazyStaleTTL: 1800}, Opts{})
	defer mainCache.Close()
	branchCache := NewCache(&Args{Size: 400000, LazyCacheTTL: 21600, LazyStaleTTL: 1800}, Opts{})
	defer branchCache.Close()

	const fillerEntries = 20000
	seedLargeCacheSet(t, mainCache, "main-large-fill", fillerEntries)
	seedLargeCacheSet(t, branchCache, "branch-large-fill", fillerEntries)
	if got := mainCache.backend.Len(); got < fillerEntries {
		t.Fatalf("expected large main cache to contain at least %d entries, got %d", fillerEntries, got)
	}
	if got := branchCache.backend.Len(); got < fillerEntries {
		t.Fatalf("expected large branch cache to contain at least %d entries, got %d", fillerEntries, got)
	}

	const qname = "video-cdn-address-change.example."
	oldIP := net.IPv4(1, 1, 1, 1)
	newIP := net.IPv4(2, 2, 2, 2)
	seedStaleCacheEntry(t, branchCache, qname, oldIP)

	raw := &testResponseExec{ip: newIP}
	plugins := map[string]any{
		"main_cache":   mainCache,
		"branch_cache": branchCache,
		"raw":          raw,
	}
	m := coremain.NewTestMosdnsWithPlugins(plugins)
	s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("test", m)), []sequence.RuleArgs{
		{Exec: "$main_cache"},
		{Exec: "$branch_cache"},
		{Exec: "$raw"},
	})
	if err != nil {
		t.Fatal(err)
	}

	first := queryThroughSequence(t, s, qname)
	if !responseHasA(first.R(), oldIP) {
		t.Fatalf("expected first long-run access to see branch stale old address, got %+v", first.R())
	}

	mainKey := cacheKeyForQuery(t, qname)
	if stored, _, _ := mainCache.backend.Get(key(mainKey)); stored != nil {
		t.Fatal("expected main cache not to promote branch stale old address")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stored, _, _ := branchCache.backend.Get(key(mainKey))
		refreshedResp, lazy, _, corrupt := respFromCacheItem(stored, 0, expiredMsgTtl)
		if !corrupt && !lazy && responseHasA(refreshedResp, newIP) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := raw.calls.Load(); got == 0 {
		t.Fatal("expected background refresh to reach changed upstream address")
	}

	second := queryThroughSequence(t, s, qname)
	if !responseHasA(second.R(), newIP) {
		t.Fatalf("expected second access to use refreshed new address, got %+v", second.R())
	}

	stored, _, _ := mainCache.backend.Get(key(mainKey))
	storedResp, lazy, _, corrupt := respFromCacheItem(stored, 0, expiredMsgTtl)
	if corrupt || lazy || !responseHasA(storedResp, newIP) {
		t.Fatalf("expected main cache to store only fresh new address, resp=%+v lazy=%v corrupt=%v", storedResp, lazy, corrupt)
	}
}

func Test_cachePlugin_ExecBypassesConfiguredDomainSet(t *testing.T) {
	c := NewCache(&Args{Size: 64, BypassDomainSets: []string{"高变CDN"}}, Opts{})
	defer c.Close()

	seedCtx := testQueryContext(t, "domainset-bypass.example.", net.IPv4(1, 1, 1, 1))
	seedCtx.StoreValue(query_context.KeyDomainSet, "国内分流|高变CDN")
	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false, nil)
	msgKey := string(keyBuf)
	releaseKeyBuffer(bufPtr)

	msgToCache := copyNoOpt(seedCtx.R())
	packedMsg, err := msgToCache.Pack()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cachedItem := &item{
		resp:           packedMsg,
		storedUnixNano: now.UnixNano(),
		expireUnixNano: now.Add(time.Minute).UnixNano(),
		domainSet:      encodeStoredRouteMetadata("国内分流|高变CDN", "国内分流|高变CDN", ""),
	}
	c.prepareCacheItemForStore(cachedItem)
	k := key(msgKey)
	c.backend.Store(k, cachedItem, now.Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	qCtx := testQueryContext(t, "domainset-bypass.example.", net.IPv4(2, 2, 2, 2))
	qCtx.StoreValue(query_context.KeyDomainSet, "国内分流|高变CDN")
	if err := c.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec: %v", err)
	}

	resp := qCtx.R()
	if resp == nil || len(resp.Answer) != 1 {
		t.Fatalf("expected response after cache bypass, got %+v", resp)
	}
	gotA, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected A response, got %T", resp.Answer[0])
	}
	if !gotA.A.Equal(net.IPv4(2, 2, 2, 2)) {
		t.Fatalf("expected configured domain-set bypass to keep fresh response, got %s", gotA.A.String())
	}

	stored, _, _ := c.backend.Get(k)
	storedResp, lazy, _, corrupt := respFromCacheItem(stored, 0, expiredMsgTtl)
	if corrupt || lazy || storedResp == nil || len(storedResp.Answer) != 1 {
		t.Fatalf("expected old cache entry to remain unread and unchanged, resp=%+v lazy=%v corrupt=%v", storedResp, lazy, corrupt)
	}
	storedA, ok := storedResp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("expected stored A response, got %T", storedResp.Answer[0])
	}
	if !storedA.A.Equal(net.IPv4(1, 1, 1, 1)) {
		t.Fatalf("expected bypass not to overwrite old cache entry, got %s", storedA.A.String())
	}
}

func Test_cachePlugin_SaveSkipsConfiguredDomainSet(t *testing.T) {
	c := NewCache(&Args{Size: 64, BypassDomainSets: []string{"DDNS域名"}}, Opts{})
	defer c.Close()

	qCtx := testQueryContext(t, "ddns-save-bypass.example.", net.IPv4(4, 4, 4, 4))
	qCtx.StoreValue(query_context.KeyDomainSet, "DDNS域名")
	if _, ok := c.saveRespToCache("ddns-save-bypass-key", qCtx); ok {
		t.Fatal("expected configured domain-set response not to be cached")
	}
	if got := c.backend.Len(); got != 0 {
		t.Fatalf("expected cache to remain empty, got %d entries", got)
	}
}

func Test_cachePlugin_SaveCachesHighChurnDomainSetWithTTLOverride(t *testing.T) {
	c := NewCache(&Args{
		Size:         64,
		LazyCacheTTL: 1,
		LazyStaleTTL: 1,
		DomainSetTTL: []DomainSetTTLPolicy{{
			Sets:         []string{"高变化域名"},
			LazyCacheTTL: 43200,
			LazyStaleTTL: 43200,
		}},
	}, Opts{})
	defer c.Close()

	qCtx := testQueryContext(t, "steamcontent.com.", net.IPv4(5, 5, 5, 5))
	qCtx.StoreValue(query_context.KeyDomainSet, "白名单|高变化域名")
	cachedItem, ok := c.saveRespToCache("high-churn-save-key", qCtx)
	if !ok {
		t.Fatal("expected high-churn response to be cached")
	}
	if got := c.backend.Len(); got != 1 {
		t.Fatalf("expected cache to store high-churn response, got %d entries", got)
	}
	now := time.Now()
	cachedItem.storedUnixNano = now.Add(-2 * time.Second).UnixNano()
	cachedItem.expireUnixNano = now.Add(-time.Second).UnixNano()
	resp, lazy, domainSet, corrupt := c.respFromCacheItem(cachedItem, c.args.LazyStaleTTL, expiredMsgTtl)
	if corrupt || resp == nil || !lazy {
		t.Fatalf("expected high-churn stale response inside domain-set ttl window, resp=%v lazy=%v corrupt=%v", resp != nil, lazy, corrupt)
	}
	if domainSet != "白名单|高变化域名" {
		t.Fatalf("expected high-churn domain set metadata, got %q", domainSet)
	}
}

func Test_cachePlugin_PurgeDomainRuntime(t *testing.T) {
	dir := t.TempDir()
	args := &Args{
		// The cache backend spreads capacity across 64 shards. Keep room for
		// three colliding keys so this test does not depend on maphash seed.
		Size:            purgeDomainRuntimeTestCacheSize,
		DumpFile:        filepath.Join(dir, "cache.dump"),
		DumpInterval:    3600,
		WALFile:         filepath.Join(dir, "cache.wal"),
		WALSyncInterval: 1,
	}
	c := NewCache(args, Opts{})
	defer c.Close()

	qCtxA := testQueryContext(t, "purge.example.", net.IPv4(1, 1, 1, 1))
	qCtxAAAA := testAAAAQueryContext(t, "purge.example.", net.ParseIP("2001:db8::1"))
	qCtxOther := testQueryContext(t, "keep.example.", net.IPv4(2, 2, 2, 2))

	keyABuf, ptrA := getMsgKeyBytes(qCtxA.Q(), qCtxA, false, nil)
	keyAAAABuf, ptrAAAA := getMsgKeyBytes(qCtxAAAA.Q(), qCtxAAAA, false, nil)
	keyOtherBuf, ptrOther := getMsgKeyBytes(qCtxOther.Q(), qCtxOther, false, nil)
	defer releaseKeyBuffer(ptrA)
	defer releaseKeyBuffer(ptrAAAA)
	defer releaseKeyBuffer(ptrOther)

	if _, ok := c.saveRespToCache(string(keyABuf), qCtxA); !ok {
		t.Fatal("expected A response to be cached")
	}
	if _, ok := c.saveRespToCache(string(keyAAAABuf), qCtxAAAA); !ok {
		t.Fatal("expected AAAA response to be cached")
	}
	if _, ok := c.saveRespToCache(string(keyOtherBuf), qCtxOther); !ok {
		t.Fatal("expected other response to be cached")
	}

	purged, err := c.PurgeDomainRuntime(context.Background(), "purge.example", 0)
	if err != nil {
		t.Fatal(err)
	}
	if purged != 2 {
		t.Fatalf("expected to purge 2 entries, got %d", purged)
	}

	if resp, _, _ := getRespFromCache(string(keyABuf), c.backend, 0, expiredMsgTtl); resp != nil {
		t.Fatal("expected A entry to be purged")
	}
	if resp, _, _ := getRespFromCache(string(keyAAAABuf), c.backend, 0, expiredMsgTtl); resp != nil {
		t.Fatal("expected AAAA entry to be purged")
	}
	if resp, _, _ := getRespFromCache(string(keyOtherBuf), c.backend, 0, expiredMsgTtl); resp == nil {
		t.Fatal("expected unrelated entry to remain")
	}
}

func Test_cachePlugin_PurgeDomainAPI(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	qCtx := testQueryContext(t, "api-purge.example.", net.IPv4(3, 3, 3, 3))
	keyBuf, bufPtr := getMsgKeyBytes(qCtx.Q(), qCtx, false, nil)
	defer releaseKeyBuffer(bufPtr)
	if _, ok := c.saveRespToCache(string(keyBuf), qCtx); !ok {
		t.Fatal("expected response to be cached")
	}

	req := httptest.NewRequest(http.MethodPost, "/purge_domain", bytes.NewBufferString(`{"qname":"api-purge.example"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	c.Api().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("unexpected status code %d: %s", resp.Code, resp.Body.String())
	}

	var body struct {
		QName  string `json:"qname"`
		Purged int    `json:"purged"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.QName != "api-purge.example." || body.Purged != 1 {
		t.Fatalf("unexpected purge response: %+v", body)
	}
}

func Test_cachePlugin_ShouldPrefetch(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	now := time.Now()
	if !c.shouldPrefetch(now, now.Add(-60*time.Second).UnixNano(), now.Add(2*time.Second).UnixNano(), "未命中") {
		t.Fatal("expected near-expiration item to trigger prefetch")
	}
	if c.shouldPrefetch(now, now.Add(-60*time.Second).UnixNano(), now.Add(40*time.Second).UnixNano(), "未命中") {
		t.Fatal("did not expect long-remaining item to trigger prefetch")
	}
	if !c.shouldPrefetch(now, now.Add(-20*time.Second).UnixNano(), now.Add(8*time.Second).UnixNano(), "DDNS域名") {
		t.Fatal("expected ddns item to use more aggressive prefetch window")
	}
}

func Test_cachePlugin_StatsAPI(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{MetricsTag: "stats_tag"})
	defer c.Close()

	qCtx := testQueryContext(t, "stats.example.", net.IPv4(8, 8, 8, 8))
	if _, ok := c.saveRespToCache("stats-key", qCtx); !ok {
		t.Fatal("expected response to be cached")
	}

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	resp := httptest.NewRecorder()
	c.Api().ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("unexpected status code %d", resp.Code)
	}

	var stats cacheStatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		t.Fatal(err)
	}
	if stats.Tag != "stats_tag" {
		t.Fatalf("unexpected tag %q", stats.Tag)
	}
	if stats.BackendSize != 1 {
		t.Fatalf("unexpected backend size %d", stats.BackendSize)
	}
}

func TestCacheDomainSetInterningReleasesOnDeleteAndFlush(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	first := testQueryContext(t, "intern-a.example.", net.IPv4(1, 1, 1, 1))
	first.StoreValue(query_context.KeyDomainSet, "记忆直连|白名单")
	second := testQueryContext(t, "intern-b.example.", net.IPv4(2, 2, 2, 2))
	second.StoreValue(query_context.KeyDomainSet, "记忆直连|白名单")

	if _, ok := c.saveRespToCache("intern-a", first); !ok {
		t.Fatal("expected first response to be cached")
	}
	if _, ok := c.saveRespToCache("intern-b", second); !ok {
		t.Fatal("expected second response to be cached")
	}
	if got := c.domainSets.Len(); got != 1 {
		t.Fatalf("domainSets.Len() = %d, want 1", got)
	}

	c.backend.Delete("intern-a")
	if got := c.domainSets.Len(); got != 1 {
		t.Fatalf("domainSets.Len() after delete = %d, want 1", got)
	}

	c.backend.Flush()
	if got := c.domainSets.Len(); got != 0 {
		t.Fatalf("domainSets.Len() after flush = %d, want 0", got)
	}
}

func Test_cachePlugin_ServfailTTL(t *testing.T) {
	c := NewCache(&Args{Size: 64, ServfailTTL: 42, ClientTTLMin: 120, ClientTTLMax: 900}, Opts{})
	defer c.Close()

	q := new(dns.Msg)
	q.SetQuestion("servfail.example.", dns.TypeA)
	qCtx := query_context.NewContext(q)
	r := new(dns.Msg)
	r.SetRcode(q, dns.RcodeServerFailure)
	r.Ns = append(r.Ns, &dns.SOA{
		Hdr: dns.RR_Header{
			Name:   "example.",
			Rrtype: dns.TypeSOA,
			Class:  dns.ClassINET,
			Ttl:    42,
		},
		Ns:      "ns.example.",
		Mbox:    "hostmaster.example.",
		Serial:  1,
		Refresh: 60,
		Retry:   60,
		Expire:  60,
		Minttl:  42,
	})
	qCtx.SetResponse(r)

	if _, ok := c.saveRespToCache("servfail-key", qCtx); !ok {
		t.Fatal("expected servfail response to be cached")
	}

	stored, _, _ := c.backend.Get(key("servfail-key"))
	if stored == nil {
		t.Fatal("expected cached item")
	}
	remaining := time.Duration(stored.expireUnixNano - stored.storedUnixNano)
	if remaining < 40*time.Second || remaining > 43*time.Second {
		t.Fatalf("unexpected servfail ttl %s", remaining)
	}
	cachedResp, lazy, _, corrupt := c.respFromCacheItem(stored, 0, expiredMsgTtl)
	if corrupt || lazy || cachedResp == nil {
		t.Fatalf("expected fresh cached servfail response, lazy=%v corrupt=%v resp=%+v", lazy, corrupt, cachedResp)
	}
	if cachedResp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("expected SERVFAIL response, got %s", dns.RcodeToString[cachedResp.Rcode])
	}
	if len(cachedResp.Ns) != 1 {
		t.Fatalf("expected cached authority SOA, got %+v", cachedResp.Ns)
	}
	if cachedResp.Ns[0].Header().Ttl > 42 {
		t.Fatalf("negative response TTL was clamped by client_ttl_min: %d", cachedResp.Ns[0].Header().Ttl)
	}
}

func Test_cachePlugin_L1Disabled(t *testing.T) {
	c := NewCache(&Args{
		Size:      64,
		L1Enabled: boolPtr(false),
	}, Opts{})
	defer c.Close()

	if c.l1Enabled {
		t.Fatal("expected l1Enabled=false")
	}
	if got := c.l1Len(); got != 0 {
		t.Fatalf("expected L1 length 0 when disabled, got %d", got)
	}

	qCtx := testQueryContext(t, "nol1.example.", net.IPv4(9, 9, 9, 9))
	if _, ok := c.saveRespToCache("nol1-key", qCtx); !ok {
		t.Fatal("expected response to be cached")
	}
	stats := c.snapshotStats()
	if stats.Config["l1_enabled"] != false {
		t.Fatalf("expected stats config l1_enabled=false, got %#v", stats.Config["l1_enabled"])
	}
}

func TestArgsUnmarshalYAMLNormalizesExcludeIPTokens(t *testing.T) {
	var args Args
	err := yaml.Unmarshal([]byte(`
size: 64
exclude_ip: "28.0.0.0/8, 30.0.0.0/8|f2b0::/18 127.0.0.0/8"
`), &args)
	if err != nil {
		t.Fatalf("unmarshal scalar exclude_ip: %v", err)
	}
	want := []string{"28.0.0.0/8", "30.0.0.0/8", "f2b0::/18", "127.0.0.0/8"}
	if strings.Join(args.ExcludeIPs, "|") != strings.Join(want, "|") {
		t.Fatalf("exclude_ip = %+v, want %+v", args.ExcludeIPs, want)
	}

	err = yaml.Unmarshal([]byte(`
size: 64
exclude_ip:
  - "0.0.0.0/32,127.0.0.0/8"
  - "::/128|::1/128"
`), &args)
	if err != nil {
		t.Fatalf("unmarshal list exclude_ip: %v", err)
	}
	want = []string{"0.0.0.0/32", "127.0.0.0/8", "::/128", "::1/128"}
	if strings.Join(args.ExcludeIPs, "|") != strings.Join(want, "|") {
		t.Fatalf("exclude_ip = %+v, want %+v", args.ExcludeIPs, want)
	}
}

func Test_computeL1ShardCap(t *testing.T) {
	tests := []struct {
		name    string
		args    *Args
		enabled bool
		want    int
	}{
		{
			name:    "disabled",
			args:    &Args{},
			enabled: false,
			want:    0,
		},
		{
			name: "custom shard cap",
			args: &Args{
				L1ShardCap: 64,
			},
			enabled: true,
			want:    64,
		},
		{
			name: "from total cap",
			args: &Args{
				L1TotalCap: 1024,
			},
			enabled: true,
			want:    4,
		},
		{
			name: "limit max",
			args: &Args{
				L1ShardCap: maxL1ShardCap + 1,
			},
			enabled: true,
			want:    maxL1ShardCap,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeL1ShardCap(tt.args, tt.enabled); got != tt.want {
				t.Fatalf("computeL1ShardCap() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestInferDefaultL1TotalCap(t *testing.T) {
	if got := inferDefaultL1TotalCap(300000); got != defaultL1SmallCap {
		t.Fatalf("inferDefaultL1TotalCap(300000) = %d, want %d", got, defaultL1SmallCap)
	}
	if got := inferDefaultL1TotalCap(800000); got != defaultL1TotalCap {
		t.Fatalf("inferDefaultL1TotalCap(800000) = %d, want %d", got, defaultL1TotalCap)
	}
}

func TestInferWALFileFromDump(t *testing.T) {
	tests := []struct {
		dump string
		want string
	}{
		{dump: "", want: ""},
		{dump: "cache/a.dump", want: "cache/a.wal"},
		{dump: "cache/a.snapshot", want: "cache/a.snapshot.wal"},
	}
	for _, tt := range tests {
		if got := inferWALFileFromDump(tt.dump); got != tt.want {
			t.Fatalf("inferWALFileFromDump(%q) = %q, want %q", tt.dump, got, tt.want)
		}
	}
}

func TestResetKeyBuffer(t *testing.T) {
	t.Run("keep normal capacity", func(t *testing.T) {
		buf := make([]byte, 8, defaultKeyBufferCap)
		got := resetKeyBuffer(buf)
		if len(got) != 0 {
			t.Fatalf("expected len 0, got %d", len(got))
		}
		if cap(got) != defaultKeyBufferCap {
			t.Fatalf("expected cap %d, got %d", defaultKeyBufferCap, cap(got))
		}
	})

	t.Run("shrink oversized buffer", func(t *testing.T) {
		buf := make([]byte, 8, maxPooledKeyBufferCap+1)
		got := resetKeyBuffer(buf)
		if len(got) != 0 {
			t.Fatalf("expected len 0, got %d", len(got))
		}
		if cap(got) != defaultKeyBufferCap {
			t.Fatalf("expected cap %d after shrink, got %d", defaultKeyBufferCap, cap(got))
		}
	})
}

func TestResetDNSMsg(t *testing.T) {
	m := new(dns.Msg)
	m.SetQuestion("reset.example.", dns.TypeA)
	m.Answer = append(m.Answer, &dns.A{
		Hdr: dns.RR_Header{Name: "reset.example.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   net.IPv4(1, 1, 1, 1),
	})
	m.Extra = append(m.Extra, &dns.OPT{Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT}})

	resetDNSMsg(m)

	if m.Id != 0 || m.Response || m.Opcode != 0 {
		t.Fatalf("expected header fields to be reset, got %+v", m.MsgHdr)
	}
	if m.Question != nil || m.Answer != nil || m.Ns != nil || m.Extra != nil {
		t.Fatalf("expected slices to be cleared, got question=%v answer=%v ns=%v extra=%v", m.Question, m.Answer, m.Ns, m.Extra)
	}
}

func TestCacheCloseSkipsDumpWithoutPendingUpdates(t *testing.T) {
	dir := t.TempDir()
	args := &Args{
		Size:            64,
		DumpFile:        filepath.Join(dir, "cache.dump"),
		DumpInterval:    3600,
		WALFile:         filepath.Join(dir, "cache.wal"),
		WALSyncInterval: 1,
	}

	c := NewCache(args, Opts{})
	if err := c.dumpCache(); err != nil {
		t.Fatal(err)
	}

	before := counterValue(t, c.dumpTotalCounter)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	after := counterValue(t, c.dumpTotalCounter)
	if after != before {
		t.Fatalf("expected close without pending updates to skip dump, before=%v after=%v", before, after)
	}
}

func TestCacheCloseDumpsWhenUpdatesPending(t *testing.T) {
	dir := t.TempDir()
	args := &Args{
		Size:            64,
		DumpFile:        filepath.Join(dir, "cache.dump"),
		DumpInterval:    3600,
		WALFile:         filepath.Join(dir, "cache.wal"),
		WALSyncInterval: 1,
	}

	c := NewCache(args, Opts{})
	if err := c.dumpCache(); err != nil {
		t.Fatal(err)
	}

	qCtx := testQueryContext(t, "close.example.", net.IPv4(1, 1, 1, 1))
	if _, ok := c.saveRespToCache("close-key", qCtx); !ok {
		t.Fatal("expected response to be cached")
	}
	c.updatedKey.Add(1)

	before := counterValue(t, c.dumpTotalCounter)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	after := counterValue(t, c.dumpTotalCounter)
	if after != before+1 {
		t.Fatalf("expected close with pending updates to dump once, before=%v after=%v", before, after)
	}
}

type testingHelper interface {
	Helper()
	Fatal(args ...interface{})
}

type testCacheRevisionProvider struct {
	revision string
}

type testCacheSwitchProvider struct {
	value string
}

type testResponseExec struct {
	ip    net.IP
	delay time.Duration
	calls atomic.Uint64
}

type testColdFallbackExec struct {
	ip          net.IP
	leaderDelay time.Duration
	calls       atomic.Uint64
}

type testAAAAResponseExec struct {
	ip    net.IP
	calls atomic.Uint64
}

func (e *testResponseExec) Exec(ctx context.Context, qCtx *query_context.Context) error {
	e.calls.Add(1)
	if e.delay > 0 {
		timer := time.NewTimer(e.delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	q := qCtx.Q()
	resp := new(dns.Msg)
	resp.SetReply(q)
	resp.Answer = append(resp.Answer, &dns.A{
		Hdr: dns.RR_Header{
			Name:   q.Question[0].Name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    60,
		},
		A: e.ip.To4(),
	})
	qCtx.SetResponse(resp)
	return nil
}

func (e *testColdFallbackExec) Exec(ctx context.Context, qCtx *query_context.Context) error {
	call := e.calls.Add(1)
	if call == 1 && e.leaderDelay > 0 {
		timer := time.NewTimer(e.leaderDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	q := qCtx.Q()
	resp := new(dns.Msg)
	resp.SetReply(q)
	resp.Answer = append(resp.Answer, &dns.A{
		Hdr: dns.RR_Header{
			Name:   q.Question[0].Name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    60,
		},
		A: e.ip.To4(),
	})
	qCtx.SetResponse(resp)
	return nil
}

func (e *testAAAAResponseExec) Exec(_ context.Context, qCtx *query_context.Context) error {
	e.calls.Add(1)
	q := qCtx.Q()
	resp := new(dns.Msg)
	resp.SetReply(q)
	resp.Answer = append(resp.Answer, &dns.AAAA{
		Hdr: dns.RR_Header{
			Name:   q.Question[0].Name,
			Rrtype: dns.TypeAAAA,
			Class:  dns.ClassINET,
			Ttl:    60,
		},
		AAAA: e.ip,
	})
	qCtx.SetResponse(resp)
	return nil
}

func (p *testCacheRevisionProvider) CacheRevision() string {
	return p.revision
}

func (p *testCacheSwitchProvider) GetValue() string {
	return p.value
}

func cacheKeyForQuery(t testingHelper, name string) string {
	t.Helper()
	qCtx := testQueryContext(t, name, net.IPv4(127, 0, 0, 1))
	keyBuf, bufPtr := getMsgKeyBytes(qCtx.Q(), qCtx, false, nil)
	defer releaseKeyBuffer(bufPtr)
	return string(keyBuf)
}

func seedStaleCacheEntry(t testingHelper, c *Cache, name string, ip net.IP) {
	t.Helper()
	qCtx := testQueryContext(t, name, ip)
	msgKey := cacheKeyForQuery(t, name)
	cachedItem, ok := c.saveRespToCache(msgKey, qCtx)
	if !ok {
		t.Fatal("expected seed response to be cached")
	}
	now := time.Now()
	cachedItem.expireUnixNano = now.Add(-time.Second).UnixNano()
	k := key(msgKey)
	c.backend.Store(k, cachedItem, now.Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)
}

func seedLargeCacheSet(t testingHelper, c *Cache, prefix string, entries int) {
	t.Helper()
	for i := 0; i < entries; i++ {
		name := fmt.Sprintf("%s-%05d.example.", prefix, i)
		ip := net.IPv4(10, byte(i>>16), byte(i>>8), byte(i))
		qCtx := testQueryContext(t, name, ip)
		msgKey := cacheKeyForQuery(t, name)
		if _, ok := c.saveRespToCache(msgKey, qCtx); !ok {
			t.Fatal("expected filler response to be cached")
		}
	}
}

func queryThroughSequence(t testingHelper, s *sequence.Sequence, name string) *query_context.Context {
	t.Helper()
	return queryThroughSequenceType(t, s, name, dns.TypeA, false)
}

func queryThroughSequenceType(t testingHelper, s *sequence.Sequence, name string, qtype uint16, fromUDP bool) *query_context.Context {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	qCtx := query_context.NewContext(q)
	qCtx.ServerMeta.FromUDP = fromUDP
	if err := s.Exec(context.Background(), qCtx); err != nil && !errors.Is(err, sequence.ErrExit) {
		t.Fatal(err)
	}
	return qCtx
}

func responseHasA(resp *dns.Msg, ip net.IP) bool {
	if resp == nil {
		return false
	}
	for _, answer := range resp.Answer {
		a, ok := answer.(*dns.A)
		if ok && a.A.Equal(ip.To4()) {
			return true
		}
	}
	return false
}

func responseHasAAAA(resp *dns.Msg, ip net.IP) bool {
	if resp == nil {
		return false
	}
	for _, answer := range resp.Answer {
		aaaa, ok := answer.(*dns.AAAA)
		if ok && aaaa.AAAA.Equal(ip) {
			return true
		}
	}
	return false
}

func counterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	metric := new(dto.Metric)
	if err := counter.Write(metric); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return metric.GetCounter().GetValue()
}

func waitForRawCalls(t *testing.T, exec *testResponseExec, want uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := exec.calls.Load(); got >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expected raw calls >= %d within %s, got %d", want, timeout, exec.calls.Load())
}

func waitForColdFallbackCalls(t *testing.T, exec *testColdFallbackExec, want uint64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := exec.calls.Load(); got >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("expected raw calls >= %d within %s, got %d", want, timeout, exec.calls.Load())
}

func testQueryContext(t testingHelper, name string, ip net.IP) *query_context.Context {
	return testQueryContextWithTTL(t, name, ip, 60)
}

func testQueryContextWithTTL(t testingHelper, name string, ip net.IP, ttl uint32) *query_context.Context {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeA)
	q.Id = 1
	qCtx := query_context.NewContext(q)

	resp := new(dns.Msg)
	resp.SetReply(q)
	resp.Answer = append(resp.Answer, &dns.A{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET,
			Ttl:    ttl,
		},
		A: ip.To4(),
	})
	qCtx.SetResponse(resp)
	return qCtx
}

func testAAAAQueryContext(t testingHelper, name string, ip net.IP) *query_context.Context {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, dns.TypeAAAA)
	q.Id = 1
	qCtx := query_context.NewContext(q)

	resp := new(dns.Msg)
	resp.SetReply(q)
	resp.Answer = append(resp.Answer, &dns.AAAA{
		Hdr: dns.RR_Header{
			Name:   name,
			Rrtype: dns.TypeAAAA,
			Class:  dns.ClassINET,
			Ttl:    60,
		},
		AAAA: ip,
	})
	qCtx.SetResponse(resp)
	return qCtx
}
