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
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	pcache "github.com/IrineSistiana/mosdns/v5/pkg/cache"
	"github.com/IrineSistiana/mosdns/v5/pkg/concurrent_map"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
	resp, lazy, _ := getRespFromCache("wal-key", c2.backend, false, expiredMsgTtl)
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

	keyABuf, ptrA := getMsgKeyBytes(qCtxA.Q(), qCtxA, false)
	keyBBuf, ptrB := getMsgKeyBytes(qCtxB.Q(), qCtxB, false)
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
	if resp, _, _ := getRespFromCache(keyA, c2.backend, false, expiredMsgTtl); resp == nil {
		t.Fatal("expected first wal entry to be restored")
	}
	if resp, _, _ := getRespFromCache(keyB, c2.backend, false, expiredMsgTtl); resp == nil {
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

	keyPurgeBuf, ptrPurge := getMsgKeyBytes(qCtxPurge.Q(), qCtxPurge, false)
	keyKeepBuf, ptrKeep := getMsgKeyBytes(qCtxKeep.Q(), qCtxKeep, false)
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
	if resp, _, _ := getRespFromCache(keyPurge, c2.backend, false, expiredMsgTtl); resp != nil {
		t.Fatal("expected deleted wal entry to stay deleted after replay")
	}
	if resp, _, _ := getRespFromCache(keyKeep, c2.backend, false, expiredMsgTtl); resp == nil {
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

	keyBeforeBuf, ptrBefore := getMsgKeyBytes(qCtxBefore.Q(), qCtxBefore, false)
	keyAfterBuf, ptrAfter := getMsgKeyBytes(qCtxAfter.Q(), qCtxAfter, false)
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
	if resp, _, _ := getRespFromCache(keyBefore, c2.backend, false, expiredMsgTtl); resp != nil {
		t.Fatal("expected flushed wal entry to stay deleted after replay")
	}
	if resp, _, _ := getRespFromCache(keyAfter, c2.backend, false, expiredMsgTtl); resp == nil {
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
	keyBuf, ptr := getMsgKeyBytes(qCtx.Q(), qCtx, false)
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
	if resp, _, _ := getRespFromCache(msgKey, c2.backend, false, expiredMsgTtl); resp == nil {
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

	resp, lazy, domainSet := getRespFromCache("ddns-key", backend, true, expiredMsgTtl)
	if resp != nil || lazy || domainSet != "" {
		t.Fatalf("expected ddns stale cache to be bypassed, got resp=%v lazy=%v domainSet=%q", resp != nil, lazy, domainSet)
	}
}

func Test_shouldBypassForRouteChange(t *testing.T) {
	if shouldBypassForRouteChange(encodeStoredRouteMetadata("记忆直连|白名单", "记忆直连|白名单", "白名单|记忆直连"), "白名单|记忆直连", nil) {
		t.Fatal("expected reordered tags to share the same signature")
	}
	if !shouldBypassForRouteChange(encodeStoredRouteMetadata("记忆直连", "记忆直连", "记忆直连"), "未命中", nil) {
		t.Fatal("expected route change to bypass cached entry")
	}
}

func Test_cachePlugin_ExecBypassesStaleRouteCache(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	seedCtx := testQueryContext(t, "route-change.example.", net.IPv4(1, 1, 1, 1))
	seedCtx.StoreValue(query_context.KeyDomainSet, "记忆直连")

	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false)
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
		t.Fatal("expected cache entry to be rewritten after bypass")
	}
	if storedDomainSet(updated.domainSet) != "未命中" {
		t.Fatalf("expected cache entry to be rewritten with current route, got %q", storedDomainSet(updated.domainSet))
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

	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false)
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

	updated, _, _ := c.backend.Get(key(msgKey))
	if updated == nil {
		t.Fatal("expected cache entry to be rewritten after revision mismatch")
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

func Test_cachePlugin_ExecBypassesCachedResponseDuringRefresh(t *testing.T) {
	c := NewCache(&Args{Size: 64, LazyCacheTTL: 3600}, Opts{})
	defer c.Close()

	seedCtx := testQueryContext(t, "refresh-bypass.example.", net.IPv4(1, 1, 1, 1))
	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false)
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
	refreshedResp, lazy, _, corrupt := respFromCacheItem(updated, false, expiredMsgTtl)
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

func Test_cachePlugin_ExecBypassesConfiguredDomainSet(t *testing.T) {
	c := NewCache(&Args{Size: 64, BypassDomainSets: []string{"高变CDN"}}, Opts{})
	defer c.Close()

	seedCtx := testQueryContext(t, "domainset-bypass.example.", net.IPv4(1, 1, 1, 1))
	seedCtx.StoreValue(query_context.KeyDomainSet, "订阅直连|高变CDN")
	keyBuf, bufPtr := getMsgKeyBytes(seedCtx.Q(), seedCtx, false)
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
		domainSet:      encodeStoredRouteMetadata("订阅直连|高变CDN", "订阅直连|高变CDN", ""),
	}
	c.prepareCacheItemForStore(cachedItem)
	k := key(msgKey)
	c.backend.Store(k, cachedItem, now.Add(time.Hour))
	c.shards[k.Sum()%shardCount].updateL1(k, cachedItem)

	qCtx := testQueryContext(t, "domainset-bypass.example.", net.IPv4(2, 2, 2, 2))
	qCtx.StoreValue(query_context.KeyDomainSet, "订阅直连|高变CDN")
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
	storedResp, lazy, _, corrupt := respFromCacheItem(stored, false, expiredMsgTtl)
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

	keyABuf, ptrA := getMsgKeyBytes(qCtxA.Q(), qCtxA, false)
	keyAAAABuf, ptrAAAA := getMsgKeyBytes(qCtxAAAA.Q(), qCtxAAAA, false)
	keyOtherBuf, ptrOther := getMsgKeyBytes(qCtxOther.Q(), qCtxOther, false)
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

	if resp, _, _ := getRespFromCache(string(keyABuf), c.backend, false, expiredMsgTtl); resp != nil {
		t.Fatal("expected A entry to be purged")
	}
	if resp, _, _ := getRespFromCache(string(keyAAAABuf), c.backend, false, expiredMsgTtl); resp != nil {
		t.Fatal("expected AAAA entry to be purged")
	}
	if resp, _, _ := getRespFromCache(string(keyOtherBuf), c.backend, false, expiredMsgTtl); resp == nil {
		t.Fatal("expected unrelated entry to remain")
	}
}

func Test_cachePlugin_PurgeDomainAPI(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{})
	defer c.Close()

	qCtx := testQueryContext(t, "api-purge.example.", net.IPv4(3, 3, 3, 3))
	keyBuf, bufPtr := getMsgKeyBytes(qCtx.Q(), qCtx, false)
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
	c := NewCache(&Args{Size: 64, ServfailTTL: 42}, Opts{})
	defer c.Close()

	q := new(dns.Msg)
	q.SetQuestion("servfail.example.", dns.TypeA)
	qCtx := query_context.NewContext(q)
	r := new(dns.Msg)
	r.SetRcode(q, dns.RcodeServerFailure)
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

func (p *testCacheRevisionProvider) CacheRevision() string {
	return p.revision
}

func counterValue(t *testing.T, counter prometheus.Counter) float64 {
	t.Helper()
	metric := new(dto.Metric)
	if err := counter.Write(metric); err != nil {
		t.Fatalf("counter.Write: %v", err)
	}
	return metric.GetCounter().GetValue()
}

func testQueryContext(t testingHelper, name string, ip net.IP) *query_context.Context {
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
			Ttl:    60,
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
