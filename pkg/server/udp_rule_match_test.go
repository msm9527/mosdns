package server

import (
	"context"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type udpRuleMatchHandler struct{ seen chan QueryMeta }

func (h *udpRuleMatchHandler) Handle(_ context.Context, q *dns.Msg, meta QueryMeta, pack func(*dns.Msg) (*[]byte, error)) *[]byte {
	h.seen <- meta
	r := new(dns.Msg)
	r.SetReply(q)
	payload, _ := pack(r)
	return payload
}

func TestUDPTransfersOriginalRuleMatchAndPreservesLegacyCallback(t *testing.T) {
	for _, kind := range []string{"legacy", "known-positive", "known-negative"} {
		t.Run(kind, func(t *testing.T) {
			conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			defer func() {
				_ = conn.Close()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(time.Second):
					t.Error("UDP listener did not stop")
				}
			}()
			h := &udpRuleMatchHandler{seen: make(chan QueryMeta, 1)}
			var legacyCalls atomic.Uint64
			opts := UDPServerOpts{FastBypass: func(int, []byte, netip.AddrPort) (int, int, uint64, string, bool, bool) {
				legacyCalls.Add(1)
				return FastActionContinue, 0, 1 << 48, "response-owner", false, false
			}}
			want := FastRuleMatchMeta{}
			if kind != "legacy" {
				want.Known = true
				if kind == "known-positive" {
					want.Flags, want.DomainSet, want.Matched = 1<<51, "mapper-owner", true
				}
				opts.FastBypassWithRuleMatch = func(int, []byte, netip.AddrPort) (int, int, uint64, string, bool, bool, FastRuleMatchMeta) {
					return FastActionContinue, 0, 1<<48 | want.Flags, "response-owner", want.Matched, false, want
				}
			}
			go func() { done <- ServeUDP(conn, h, opts) }()
			q := new(dns.Msg)
			q.SetQuestion("rule-meta.example.", dns.TypeA)
			client := &dns.Client{Net: "udp", Timeout: time.Second}
			if _, _, err := client.Exchange(q, conn.LocalAddr().String()); err != nil {
				t.Fatal(err)
			}
			select {
			case meta := <-h.seen:
				if meta.PreFastRuleMatch != want || meta.PreFastFlags != 1<<48|want.Flags || meta.PreFastDomainSet != "response-owner" {
					t.Fatalf("lost mapper evidence or mixed ownership %+v", meta)
				}
			case <-time.After(time.Second):
				t.Fatal("handler did not receive query")
			}
			wantLegacyCalls := uint64(0)
			if kind == "legacy" {
				wantLegacyCalls = 1
			}
			if legacyCalls.Load() != wantLegacyCalls {
				t.Fatal("callback precedence changed")
			}
		})
	}
}
