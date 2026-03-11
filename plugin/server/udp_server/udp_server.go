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
	"hash/maphash"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/utils"
	"github.com/IrineSistiana/mosdns/v5/plugin/server/server_utils"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

const (
	PluginType = "udp_server"
	cacheSize  = 65536
	cacheMask  = cacheSize - 1
)

var maphashSeed = maphash.MakeSeed()

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
}

type Args struct {
	Entry                  string `yaml:"entry"`
	Listen                 string `yaml:"listen"`
	EnableAudit            bool   `yaml:"enable_audit"`
	FastCacheInternalTTL   int    `yaml:"fast_cache_internal_ttl"`
	FastCacheTTLMin        uint32 `yaml:"fast_cache_ttl_min"`
	FastCacheTTLMax        uint32 `yaml:"fast_cache_ttl_max"`
	FastMetricsLogInterval int    `yaml:"fast_metrics_log_interval"`
	FastBypassWarmupSec    int    `yaml:"fast_bypass_warmup_seconds"`
}

func (a *Args) init() {
	utils.SetDefaultString(&a.Listen, "127.0.0.1:53")
	utils.SetDefaultUnsignNum(&a.FastCacheInternalTTL, 5)
	utils.SetDefaultNum(&a.FastCacheTTLMax, uint32(30))
	utils.SetDefaultUnsignNum(&a.FastMetricsLogInterval, 60)
	utils.SetDefaultUnsignNum(&a.FastBypassWarmupSec, 3)
	if a.FastCacheTTLMax > 0 && a.FastCacheTTLMin > a.FastCacheTTLMax {
		a.FastCacheTTLMin = a.FastCacheTTLMax
	}
	if a.FastBypassWarmupSec < 0 {
		a.FastBypassWarmupSec = 0
	}
}

type UdpServer struct {
	args *Args
	c    net.PacketConn
}

func (s *UdpServer) Close() error {
	return s.c.Close()
}

type SwitchPlugin interface{ GetValue() string }
type DomainMapperPlugin interface {
	FastMatch(qname string) ([]uint8, string, bool)
}
type IPSetPlugin interface{ Match(addr netip.Addr) bool }

type fastCacheItem struct {
	// Keep the atomic field first so 32-bit ARM gets 8-byte alignment.
	expire int64

	resp      []byte
	updating  uint32
	domainSet string
	hash      uint64
	qname     string
	qtype     uint16
}

type fastCache struct {
	m     [cacheSize]atomic.Pointer[fastCacheItem]
	cfg   fastCacheConfig
	stats *fastStats
}

type fastCacheConfig struct {
	internalTTL time.Duration
	ttlMin      uint32
	ttlMax      uint32
}

type fastStats struct {
	bypassRequests   atomic.Uint64
	bypassBadPacket  atomic.Uint64
	bypassRuleReply  atomic.Uint64
	bypassCacheReply atomic.Uint64
	bypassWarmupSkip atomic.Uint64

	cacheLookup    atomic.Uint64
	cacheStore     atomic.Uint64
	cacheHit       atomic.Uint64
	cacheMiss      atomic.Uint64
	cacheCollision atomic.Uint64
	cacheExpired   atomic.Uint64
}

type fastStatsSnapshot struct {
	BypassRequests   uint64
	BypassBadPacket  uint64
	BypassRuleReply  uint64
	BypassCacheReply uint64
	BypassWarmupSkip uint64
	CacheLookup      uint64
	CacheStore       uint64
	CacheHit         uint64
	CacheMiss        uint64
	CacheCollision   uint64
	CacheExpired     uint64
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
		BypassWarmupSkip: s.bypassWarmupSkip.Load(),
		CacheLookup:      s.cacheLookup.Load(),
		CacheStore:       s.cacheStore.Load(),
		CacheHit:         s.cacheHit.Load(),
		CacheMiss:        s.cacheMiss.Load(),
		CacheCollision:   s.cacheCollision.Load(),
		CacheExpired:     s.cacheExpired.Load(),
	}
}

func newFastCache(cfg fastCacheConfig, stats *fastStats) *fastCache {
	return &fastCache{cfg: cfg, stats: stats}
}

func (fc *fastCache) GetOrUpdating(hash uint64, buf []byte, qname string, qtype uint16) (int, int, uint64, string) {
	ptr := fc.m[hash&cacheMask].Load()
	if ptr == nil {
		if fc.stats != nil {
			fc.stats.cacheMiss.Add(1)
		}
		return server.FastActionContinue, 0, 0, ""
	}
	if ptr.hash != hash || ptr.qtype != qtype || ptr.qname != qname {
		if fc.stats != nil {
			fc.stats.cacheCollision.Add(1)
			fc.stats.cacheMiss.Add(1)
		}
		return server.FastActionContinue, 0, 0, ""
	}

	now := time.Now().Unix()
	if now > atomic.LoadInt64(&ptr.expire) {
		if fc.stats != nil {
			fc.stats.cacheExpired.Add(1)
		}
		if atomic.CompareAndSwapUint32(&ptr.updating, 0, 1) {
			if fc.stats != nil {
				fc.stats.cacheMiss.Add(1)
			}
			return server.FastActionContinue, 0, 0, ""
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
		return server.FastActionReply, respLen, 0, ptr.domainSet
	}
	if fc.stats != nil {
		fc.stats.cacheMiss.Add(1)
	}
	return server.FastActionContinue, 0, 0, ""
}

func (fc *fastCache) Store(qname string, qtype uint16, resp []byte, dset string) {
	h := maphash.String(maphashSeed, qname) ^ uint64(qtype)

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
		resp:      bakedResp,
		expire:    time.Now().Add(fc.cfg.internalTTL).Unix(),
		updating:  0,
		domainSet: dset,
		hash:      h,
		qname:     qname,
		qtype:     qtype,
	}
	fc.m[h&cacheMask].Store(item)
	if fc.stats != nil {
		fc.stats.cacheStore.Add(1)
	}
}

type fastHandler struct {
	next server.Handler
	fc   *fastCache
	dm   DomainMapperPlugin
	sw   SwitchPlugin
}

func (h *fastHandler) Handle(ctx context.Context, q *dns.Msg, meta server.QueryMeta, pack func(*dns.Msg) (*[]byte, error)) *[]byte {
	payload := h.next.Handle(ctx, q, meta, pack)

	if h.sw != nil && h.sw.GetValue() != "A" {
		return payload
	}

	if payload != nil && (meta.PreFastFlags&(1<<39)) == 0 && q.Opcode == dns.OpcodeQuery && len(q.Question) > 0 {
		dsetName := meta.PreFastDomainSet
		if dsetName == "" && h.dm != nil {
			_, dsetName, _ = h.dm.FastMatch(q.Question[0].Name)
		}
		h.fc.Store(q.Question[0].Name, q.Question[0].Qtype, *payload, dsetName)
	}
	return payload
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
	if p := bp.M().GetPlugin("unified_matcher1"); p != nil {
		dm, _ = p.(DomainMapperPlugin)
	}

	var sw15 SwitchPlugin
	if p := bp.M().GetPlugin("switch15"); p != nil {
		sw15, _ = p.(SwitchPlugin)
	}

	stats := &fastStats{}
	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Duration(args.FastCacheInternalTTL) * time.Second,
		ttlMin:      args.FastCacheTTLMin,
		ttlMax:      args.FastCacheTTLMax,
	}, stats)
	wrappedHandler := &fastHandler{next: dh, fc: fc, dm: dm, sw: sw15}
	fastBypass := buildFastBypass(bp, fc, stats, time.Duration(args.FastBypassWarmupSec)*time.Second)

	socketOpt := server_utils.ListenerSocketOpts{
		SO_REUSEPORT: true,
		SO_RCVBUF:    2 * 1024 * 1024,
	}
	lc := net.ListenConfig{Control: server_utils.ListenerControl(socketOpt)}
	c, err := lc.ListenPacket(context.Background(), "udp", args.Listen)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket, %w", err)
	}
	bp.L().Info("udp server started with extreme bypass", zap.Stringer("addr", c.LocalAddr()))
	if args.FastMetricsLogInterval > 0 {
		bp.M().GetSafeClose().Attach(func(done func(), closeSignal <-chan struct{}) {
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
						zap.Uint64("bypass_warmup_skip", s.BypassWarmupSkip),
						zap.Uint64("cache_lookup", s.CacheLookup),
						zap.Uint64("cache_store", s.CacheStore),
						zap.Uint64("cache_hit", s.CacheHit),
						zap.Uint64("cache_miss", s.CacheMiss),
						zap.Uint64("cache_collision", s.CacheCollision),
						zap.Uint64("cache_expired", s.CacheExpired),
					)
				}
			}
		})
	}

	go func() {
		defer c.Close()
		err := server.ServeUDP(c.(*net.UDPConn), wrappedHandler, server.UDPServerOpts{
			Logger:     bp.L(),
			FastBypass: fastBypass,
		})
		bp.M().GetSafeClose().SendCloseSignal(err)
	}()
	return &UdpServer{args: args, c: c}, nil
}

func buildFastBypass(bp *coremain.BP, fc *fastCache, stats *fastStats, warmup time.Duration) func(int, []byte, netip.AddrPort) (int, int, uint64, string, bool) {
	var once sync.Once
	var sw15, sw5, sw6, sw1, sw7, sw2, sw12 SwitchPlugin
	var dm DomainMapperPlugin
	var ipSet IPSetPlugin
	readyAt := time.Now().Add(warmup)

	return func(reqLen int, buf []byte, remoteAddr netip.AddrPort) (int, int, uint64, string, bool) {
		if stats != nil {
			stats.bypassRequests.Add(1)
		}
		if warmup > 0 && time.Now().Before(readyAt) {
			if stats != nil {
				stats.bypassWarmupSkip.Add(1)
			}
			return server.FastActionContinue, 0, 0, "", false
		}
		once.Do(func() {
			if p := bp.M().GetPlugin("switch15"); p != nil {
				sw15, _ = p.(SwitchPlugin)
			}
			if p := bp.M().GetPlugin("switch5"); p != nil {
				sw5, _ = p.(SwitchPlugin)
			}
			if p := bp.M().GetPlugin("switch6"); p != nil {
				sw6, _ = p.(SwitchPlugin)
			}
			if p := bp.M().GetPlugin("switch1"); p != nil {
				sw1, _ = p.(SwitchPlugin)
			}
			if p := bp.M().GetPlugin("switch7"); p != nil {
				sw7, _ = p.(SwitchPlugin)
			}
			if p := bp.M().GetPlugin("switch2"); p != nil {
				sw2, _ = p.(SwitchPlugin)
			}
			if p := bp.M().GetPlugin("switch12"); p != nil {
				sw12, _ = p.(SwitchPlugin)
			}
			if p := bp.M().GetPlugin("unified_matcher1"); p != nil {
				dm, _ = p.(DomainMapperPlugin)
			}
			if p := bp.M().GetPlugin("client_ip"); p != nil {
				ipSet, _ = p.(IPSetPlugin)
			}
		})

		if sw15 == nil || sw15.GetValue() != "A" {
			return server.FastActionContinue, 0, 0, "", false
		}
		qname, qtype, qEnd, ok := parseFastQuestion(reqLen, buf)
		if !ok {
			if stats != nil {
				stats.bypassBadPacket.Add(1)
			}
			return server.FastActionContinue, 0, 0, "", false
		}

		if qtype == 6 || qtype == 12 || qtype == 65 {
			if sw5 != nil && sw5.GetValue() == "A" {
				if stats != nil {
					stats.bypassRuleReply.Add(1)
				}
				return server.FastActionReply, makeReject(reqLen, buf, qEnd, 0), 0, "", false
			}
		}
		if qtype == 28 {
			if sw6 != nil && sw6.GetValue() == "A" {
				if stats != nil {
					stats.bypassRuleReply.Add(1)
				}
				return server.FastActionReply, makeReject(reqLen, buf, qEnd, 0), 0, "", false
			}
		}

		var marks uint64
		var dset string
		var dsetMatched bool
		if dm != nil {
			if mList, dsName, match := dm.FastMatch(qname); match {
				for _, v := range mList {
					if v < 64 {
						marks |= (1 << v)
					}
				}
				dset = dsName
				dsetMatched = true
			}
		}

		if sw1 != nil {
			sw1Val := sw1.GetValue()
			if (marks&(1<<1)) != 0 && sw1Val == "A" {
				if stats != nil {
					stats.bypassRuleReply.Add(1)
				}
				return server.FastActionReply, makeReject(reqLen, buf, qEnd, 3), 0, "", false
			}
			if (marks&(1<<2)) != 0 && qtype == 1 && sw1Val == "A" {
				if stats != nil {
					stats.bypassRuleReply.Add(1)
				}
				return server.FastActionReply, makeReject(reqLen, buf, qEnd, 0), 0, "", false
			}
			if (marks&(1<<3)) != 0 && qtype == 28 && sw1Val == "A" {
				if stats != nil {
					stats.bypassRuleReply.Add(1)
				}
				return server.FastActionReply, makeReject(reqLen, buf, qEnd, 0), 0, "", false
			}
		}
		if sw7 != nil {
			if (marks&(1<<5)) != 0 && sw7.GetValue() == "A" {
				if stats != nil {
					stats.bypassRuleReply.Add(1)
				}
				return server.FastActionReply, makeReject(reqLen, buf, qEnd, 3), 0, "", false
			}
		}

		ipMatch := false
		if ipSet != nil {
			ipMatch = ipSet.Match(remoteAddr.Addr().Unmap())
			marks |= (1 << 48)
		}
		sw2Val, sw12Val := "", ""
		if sw2 != nil {
			sw2Val = sw2.GetValue()
		}
		if sw12 != nil {
			sw12Val = sw12.GetValue()
		}

		if sw2Val == "A" && sw12Val == "B" && !ipMatch {
			marks |= (1 << 39)
		} else if sw2Val == "B" && sw12Val == "A" && ipMatch {
			marks |= (1 << 39)
		}

		if (marks & (1 << 39)) == 0 {
			hKey := maphash.String(maphashSeed, qname) ^ uint64(qtype)
			if stats != nil {
				stats.cacheLookup.Add(1)
			}
			action, rLen, _, ds := fc.GetOrUpdating(hKey, buf, qname, qtype)
			if action == server.FastActionReply {
				if stats != nil {
					stats.bypassCacheReply.Add(1)
				}
				return action, rLen, 0, ds, false
			}
		}
		return server.FastActionContinue, 0, marks, dset, dsetMatched
	}
}

func makeReject(reqLen int, buf []byte, offset int, rcode byte) int {
	if offset > reqLen {
		offset = reqLen
	}
	buf[2] |= 0x80
	buf[3] |= 0x80
	buf[3] = (buf[3] & 0xF0) | (rcode & 0x0F)
	return offset
}

func parseFastQuestion(reqLen int, buf []byte) (qname string, qtype uint16, end int, ok bool) {
	if reqLen < 12 {
		return "", 0, 0, false
	}
	flags0 := buf[2]
	if flags0&0x80 != 0 {
		return "", 0, 0, false
	}
	if ((flags0 >> 3) & 0x0f) != 0 {
		return "", 0, 0, false
	}
	if binary.BigEndian.Uint16(buf[4:6]) != 1 {
		return "", 0, 0, false
	}

	offset := 12
	var nameBuf [256]byte
	nameLen := 0
	terminated := false
	for offset < reqLen {
		l := int(buf[offset])
		if l == 0 {
			offset++
			if nameLen == 0 {
				nameBuf[0] = '.'
				nameLen = 1
			}
			terminated = true
			break
		}
		if l&0xC0 != 0 {
			return "", 0, 0, false
		}
		offset++
		if offset+l > reqLen || nameLen+l+1 > len(nameBuf) {
			return "", 0, 0, false
		}
		copy(nameBuf[nameLen:], buf[offset:offset+l])
		nameLen += l
		nameBuf[nameLen] = '.'
		nameLen++
		offset += l
	}
	if !terminated || offset+4 > reqLen {
		return "", 0, 0, false
	}
	qtype = binary.BigEndian.Uint16(buf[offset : offset+2])
	return string(nameBuf[:nameLen]), qtype, offset + 4, true
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
	if ancount == 0 {
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
	for i := 0; i < int(ancount); i++ {
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
