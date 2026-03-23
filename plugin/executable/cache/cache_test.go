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
	"testing"
	"time"

	pcache "github.com/IrineSistiana/mosdns/v5/pkg/cache"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func boolPtr(v bool) *bool { return &v }

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
		storedTime:     now,
		expirationTime: hourLater,
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
	if !c.saveRespToCache("wal-key", qCtx) {
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
		storedTime:     now.Add(-10 * time.Minute),
		expirationTime: now.Add(-1 * time.Minute),
		domainSet:      "DDNS域名",
	}, now.Add(time.Hour))

	resp, lazy, domainSet := getRespFromCache("ddns-key", backend, true, expiredMsgTtl)
	if resp != nil || lazy || domainSet != "" {
		t.Fatalf("expected ddns stale cache to be bypassed, got resp=%v lazy=%v domainSet=%q", resp != nil, lazy, domainSet)
	}
}

func Test_shouldBypassForRouteChange(t *testing.T) {
	if shouldBypassForRouteChange("记忆直连|白名单", "白名单|记忆直连") {
		t.Fatal("expected reordered tags to share the same signature")
	}
	if !shouldBypassForRouteChange("记忆直连", "未命中") {
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
	keyBufferPool.Put(bufPtr)

	if !c.saveRespToCache(msgKey, seedCtx) {
		t.Fatal("expected seed response to be cached")
	}

	k := key(msgKey)
	stored, _, _ := c.backend.Get(k)
	if stored == nil {
		t.Fatal("expected stored cache item")
	}
	c.shards[k.Sum()%shardCount].updateL1(k, seedCtx.R(), stored.storedTime, stored.expirationTime, stored.domainSet)

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
	if updated.domainSet != "未命中" {
		t.Fatalf("expected cache entry to be rewritten with current route, got %q", updated.domainSet)
	}
}

func Test_cachePlugin_PurgeDomainRuntime(t *testing.T) {
	dir := t.TempDir()
	args := &Args{
		Size:            64,
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
	defer keyBufferPool.Put(ptrA)
	defer keyBufferPool.Put(ptrAAAA)
	defer keyBufferPool.Put(ptrOther)

	if !c.saveRespToCache(string(keyABuf), qCtxA) {
		t.Fatal("expected A response to be cached")
	}
	if !c.saveRespToCache(string(keyAAAABuf), qCtxAAAA) {
		t.Fatal("expected AAAA response to be cached")
	}
	if !c.saveRespToCache(string(keyOtherBuf), qCtxOther) {
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
	defer keyBufferPool.Put(bufPtr)
	if !c.saveRespToCache(string(keyBuf), qCtx) {
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
	if !c.shouldPrefetch(now, now.Add(-60*time.Second), now.Add(2*time.Second), "未命中") {
		t.Fatal("expected near-expiration item to trigger prefetch")
	}
	if c.shouldPrefetch(now, now.Add(-60*time.Second), now.Add(40*time.Second), "未命中") {
		t.Fatal("did not expect long-remaining item to trigger prefetch")
	}
	if !c.shouldPrefetch(now, now.Add(-20*time.Second), now.Add(8*time.Second), "DDNS域名") {
		t.Fatal("expected ddns item to use more aggressive prefetch window")
	}
}

func Test_cachePlugin_StatsAPI(t *testing.T) {
	c := NewCache(&Args{Size: 64}, Opts{MetricsTag: "stats_tag"})
	defer c.Close()

	qCtx := testQueryContext(t, "stats.example.", net.IPv4(8, 8, 8, 8))
	if !c.saveRespToCache("stats-key", qCtx) {
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

func Test_cachePlugin_ServfailTTL(t *testing.T) {
	c := NewCache(&Args{Size: 64, ServfailTTL: 42}, Opts{})
	defer c.Close()

	q := new(dns.Msg)
	q.SetQuestion("servfail.example.", dns.TypeA)
	qCtx := query_context.NewContext(q)
	r := new(dns.Msg)
	r.SetRcode(q, dns.RcodeServerFailure)
	qCtx.SetResponse(r)

	if !c.saveRespToCache("servfail-key", qCtx) {
		t.Fatal("expected servfail response to be cached")
	}

	stored, _, _ := c.backend.Get(key("servfail-key"))
	if stored == nil {
		t.Fatal("expected cached item")
	}
	remaining := stored.expirationTime.Sub(stored.storedTime)
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
	if !c.saveRespToCache("nol1-key", qCtx) {
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
	if !c.saveRespToCache("close-key", qCtx) {
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
