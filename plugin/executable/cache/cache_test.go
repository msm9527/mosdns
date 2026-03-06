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
	"encoding/json"
	"github.com/miekg/dns"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
)

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

type testingHelper interface {
	Helper()
	Fatal(args ...interface{})
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
