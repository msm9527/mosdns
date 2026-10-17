package udp_server

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestFastEDNSRejectsIncompatibleResponses(t *testing.T) {
	for _, mode := range []string{"do", "version", "extended-rcode", "cookie", "padding", "ecs", "non-root-opt", "multiple-opt", "non-final-opt", "other-additional", "trailing-data", "truncated-opt"} {
		t.Run(mode, func(t *testing.T) {
			q := refreshTestQuestion()
			r := refreshTestResponse(q, "ordinary")
			r.SetEdns0(1232, false)
			opt := r.IsEdns0()
			switch mode {
			case "do":
				opt.SetDo()
			case "version":
				opt.SetVersion(1)
			case "extended-rcode":
				r.Rcode = dns.RcodeBadVers // Low header bits are NOERROR.
			case "cookie":
				opt.Option = []dns.EDNS0{&dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "0102030405060708"}}
			case "padding":
				opt.Option = []dns.EDNS0{&dns.EDNS0_PADDING{Padding: []byte{0, 0}}}
			case "ecs":
				opt.Option = []dns.EDNS0{&dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 24, Address: net.IPv4(192, 0, 2, 0)}}
			case "non-root-opt":
				opt.Hdr.Name = "not-root.example."
			case "multiple-opt":
				r.Extra = append(r.Extra, dns.Copy(opt))
			case "non-final-opt":
				r.Extra = append(r.Extra, dns.Copy(r.Answer[0]))
			case "other-additional":
				r.Extra = []dns.RR{dns.Copy(r.Answer[0])}
			}
			wire := mustPack(t, r)
			if mode == "trailing-data" {
				wire = append(wire, 0)
			}
			if mode == "truncated-opt" {
				wire = wire[:len(wire)-1]
			}
			fc := refreshTestCache()
			if fc.Store(q.Question[0].Name, dns.TypeA, wire, "", false) || fc.Len() != 0 {
				t.Fatal("response requiring full EDNS/additional handling entered simple fast cache")
			}
		})
	}
}

func TestFastEDNSLegacyCopyRetainsOriginalOPT(t *testing.T) {
	q := refreshTestQuestion()
	for _, withOPT := range []bool{false, true} {
		r := refreshTestResponse(q, "ordinary")
		if withOPT {
			r.SetEdns0(1232, false)
		}
		fc := refreshTestCache()
		if !fc.Store(q.Question[0].Name, dns.TypeA, mustPack(t, r), "", false) {
			t.Fatal("failed to seed legacy copy fixture")
		}
		wire, _ := fc.CopyResponseWithAudit(4321, q.Question[0].Name, dns.TypeA, false, fastRuleRevision{})
		if wire == nil {
			t.Fatal("legacy copy returned no response")
		}
		var got dns.Msg
		err := got.Unpack(*wire)
		pool.ReleaseBuf(wire)
		if err != nil || got.Id != 4321 || (got.IsEdns0() != nil) != withOPT || (withOPT && got.IsEdns0().UDPSize() != 1232) {
			t.Fatal("legacy request-less API changed its stored wire semantics")
		}
	}
}

func TestFastEDNSUsesAdaptedLengthForUDPLimit(t *testing.T) {
	for _, storedOPT := range []bool{false, true} {
		q := new(dns.Msg)
		q.SetQuestion("size.example.", dns.TypeNULL)
		r := new(dns.Msg)
		r.SetReply(q)
		rr := &dns.NULL{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeNULL, Class: dns.ClassINET, Ttl: 60}}
		r.Answer = []dns.RR{rr}
		rr.Data = strings.Repeat("x", dns.MinMsgSize-len(mustPack(t, r)))
		if storedOPT {
			r.SetEdns0(1200, false)
		}
		response := mustPack(t, r)
		fc := refreshTestCache()
		if !fc.Store(q.Question[0].Name, dns.TypeNULL, response, "", false) {
			t.Fatal("failed to seed UDP boundary fixture")
		}
		manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"udp_fast_path": testSwitchPlugin{value: "on"}})
		bypass := buildFastBypass(coremain.NewBP("opt-size", manager), fc, nil, 0)
		for _, limit := range []uint16{0, 128, 512, 523} {
			query := q.Copy()
			if limit != 0 {
				query.SetEdns0(limit, false)
			}
			req := mustPack(t, query)
			buf := make([]byte, 4096)
			copy(buf, req)
			action, length, _, _, _, _ := bypass(len(req), buf, netip.MustParseAddrPort("127.0.0.1:5300"))
			wantHit := limit == 0 || limit == 523
			if (action == server.FastActionReply) != wantHit {
				t.Fatalf("storedOPT=%v clientLimit=%d action=%d length=%d", storedOPT, limit, action, length)
			}
			if wantHit {
				wantSize := dns.MinMsgSize
				if limit != 0 {
					wantSize += fastEmptyOPTSize
				}
				if length != wantSize {
					t.Fatalf("adapted reply size=%d want=%d", length, wantSize)
				}
			}
			// The stale copy must obey the same final length boundary.
			h := &fastHandler{fc: fc, next: requestPolicyHandler{call: func() {}}}
			payload := h.serveStaleWhileRefresh(context.Background(), query, server.QueryMeta{}, pool.PackBuffer, time.Time{})
			if (payload != nil) != wantHit {
				t.Fatalf("stale-copy size policy differs: storedOPT=%v clientLimit=%d", storedOPT, limit)
			}
			if payload != nil {
				pool.ReleaseBuf(payload)
			}
		}
	}
}
