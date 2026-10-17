package server

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

type batchHandlerEvent struct {
	query *dns.Msg
	meta  QueryMeta
}
type batchTestHandler struct {
	gate   <-chan struct{}
	events chan batchHandlerEvent
}

func (h *batchTestHandler) Handle(_ context.Context, q *dns.Msg, meta QueryMeta, pack func(*dns.Msg) (*[]byte, error)) *[]byte {
	<-h.gate
	h.events <- batchHandlerEvent{q, meta}
	r := new(dns.Msg)
	r.SetReply(q)
	r.Extra = q.Extra
	payload, _ := pack(r)
	return payload
}

func TestUDPBatchMixedClientsAndReusedBuffers(t *testing.T) {
	for _, tc := range []struct{ name, network, bind, destination string }{
		{"ipv4", "udp4", "127.0.0.1", "127.0.0.1"},
		{"ipv4 wildcard source", "udp4", "0.0.0.0", "127.0.0.2"},
		{"ipv6", "udp6", "::1", "::1"},
		{"ipv6 wildcard source", "udp6", "::", "::1"},
		{"dual stack ipv4", "udp", "::", "127.0.0.2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := net.ListenUDP(tc.network, &net.UDPAddr{IP: net.ParseIP(tc.bind)})
			if err != nil {
				t.Skipf("address unavailable: %v", err)
			}
			defer conn.Close()
			const count = 24
			clients := make([]*net.UDPConn, count)
			queries := make([]*dns.Msg, count)
			for i := range clients {
				destination := tc.destination
				if tc.destination == "127.0.0.2" && i%2 == 0 {
					destination = "127.0.0.1"
				}
				clients[i], err = net.DialUDP(tc.network, nil, &net.UDPAddr{IP: net.ParseIP(destination), Port: conn.LocalAddr().(*net.UDPAddr).Port})
				if err != nil {
					t.Fatal(err)
				}
				defer clients[i].Close()
				_ = clients[i].SetDeadline(time.Now().Add(3 * time.Second))
				q := new(dns.Msg)
				q.SetQuestion(fmt.Sprintf("client-%02d.example.", i), dns.TypeA)
				q.Id = uint16(i)
				// Alternate tiny packets and packets larger than a typical DNS UDP buffer.
				if i%2 == 1 {
					q.Extra = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: "padding.example.", Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 1}, Txt: []string{strings.Repeat("a", 200), strings.Repeat("b", 200), strings.Repeat("c", 200), strings.Repeat("d", 200), strings.Repeat("e", 200), strings.Repeat("f", 200)}}}
				}
				queries[i] = q
				wire, err := q.Pack()
				if err != nil {
					t.Fatal(err)
				}
				if _, err = clients[i].Write(wire); err != nil {
					t.Fatal(err)
				}
			}
			gate := make(chan struct{})
			defer func() {
				select {
				case <-gate:
				default:
					close(gate)
				}
			}()
			h := &batchTestHandler{gate: gate, events: make(chan batchHandlerEvent, count)}
			seen := make(chan uint16, count)
			opts := UDPServerOpts{FastBypassWithRuleMatch: func(n int, buf []byte, _ netip.AddrPort) (int, int, uint64, string, bool, bool, FastRuleMatchMeta) {
				id := uint16(buf[0])<<8 | uint16(buf[1])
				seen <- id
				if id%3 == 0 {
					buf[2] |= 0x80
					return FastActionReply, n, 0, "", false, false, FastRuleMatchMeta{}
				}
				return FastActionContinue, 0, uint64(id), fmt.Sprint(id), id%2 == 0, id%5 == 0, FastRuleMatchMeta{Known: true, Flags: uint64(id), DomainSet: fmt.Sprint(id), Matched: id%2 == 0}
			}}
			done := make(chan error, 1)
			go func() { done <- ServeUDP(conn, h, opts) }()
			defer func() {
				_ = conn.Close()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(time.Second):
					t.Error("listener did not stop")
				}
			}()
			for i := 0; i < count; i++ {
				select {
				case <-seen:
				case <-time.After(time.Second):
					t.Fatal("not all queued queries processed")
				}
			}
			close(gate) // Full-path handlers reply after all receive slots have been reused.
			for i, client := range clients {
				buf := make([]byte, 4096)
				n, err := client.Read(buf)
				if err != nil {
					t.Fatalf("client %d: %v", i, err)
				}
				reply := new(dns.Msg)
				if err := reply.Unpack(buf[:n]); err != nil {
					t.Fatal(err)
				}
				if reply.Id != queries[i].Id || !reply.Response || len(reply.Question) != 1 || reply.Question[0] != queries[i].Question[0] || len(reply.Extra) != len(queries[i].Extra) {
					t.Fatalf("client %d received another request or truncated payload: %v", i, reply)
				}
				if len(reply.Extra) > 0 && reply.Extra[0].String() != queries[i].Extra[0].String() {
					t.Fatal("large payload changed after short reply")
				}
			}
			for i := 0; i < count-count/3; i++ {
				select {
				case event := <-h.events:
					id := event.query.Id
					meta := event.meta
					if meta.ClientAddr.Unmap() != netip.MustParseAddr(tc.destination).Unmap() && !meta.ClientAddr.IsLoopback() {
						t.Fatalf("invalid client address %v", meta.ClientAddr)
					}
					if !meta.FromUDP || meta.PreFastFlags != uint64(id) || meta.PreFastDomainSet != fmt.Sprint(id) || meta.PreFastDomainMatched != (id%2 == 0) || meta.PreFastStaleRefresh != (id%5 == 0) || meta.PreFastRuleMatch != (FastRuleMatchMeta{Known: true, Flags: uint64(id), DomainSet: fmt.Sprint(id), Matched: id%2 == 0}) {
						t.Fatalf("metadata leaked between clients: id=%d %+v", id, meta)
					}
				case <-time.After(time.Second):
					t.Fatal("missing full-path metadata")
				}
			}
			// A lone packet must be answered immediately, without waiting for a batch.
			lone := new(dns.Msg)
			lone.SetQuestion("lone.example.", dns.TypeAAAA)
			lone.Id = 300
			wire, _ := lone.Pack()
			_ = clients[0].SetDeadline(time.Now().Add(time.Second))
			_, _ = clients[0].Write(wire)
			if _, err := clients[0].Read(make([]byte, 4096)); err != nil {
				t.Fatalf("single packet waited for batch: %v", err)
			}
		})
	}
}
