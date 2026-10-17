package server_handler

import (
	"context"
	"errors"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestEntryHandlerResponseProvenance(t *testing.T) {
	for _, tc := range []struct {
		name          string
		packed, stale bool
		err           error
	}{
		{name: "parsed_fresh"},
		{name: "parsed_stale", stale: true},
		{name: "packed_fresh", packed: true},
		{name: "packed_stale", packed: true, stale: true},
		{name: "parsed_exit", stale: true, err: sequence.ErrExit},
		{name: "packed_exit", packed: true, stale: true, err: sequence.ErrExit},
		{name: "parsed_error", stale: true, err: errors.New("upstream failed")},
		{name: "packed_error", packed: true, stale: true, err: errors.New("upstream failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewEntryHandler(EntryHandlerOpts{Entry: sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
				r := new(dns.Msg)
				r.SetReply(qCtx.Q())
				r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: qCtx.Q().Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 5}, A: []byte{1, 2, 3, 4}}}
				if tc.packed {
					wire, err := r.Pack()
					if err != nil {
						t.Fatal(err)
					}
					qCtx.SetResponsePayload(&query_context.ResponsePayload{Wire: wire, Stale: tc.stale})
				} else {
					qCtx.SetResponse(r)
					qCtx.SetCacheResponseStale(tc.stale)
				}
				return tc.err
			})})
			q := new(dns.Msg)
			q.SetQuestion("provenance.example.", dns.TypeA)
			payload, stale := server.HandleResponse(h, context.Background(), q, server.QueryMeta{}, pool.PackBuffer)
			if payload == nil {
				t.Fatal("handler returned no response")
			}
			defer pool.ReleaseBuf(payload)
			var r dns.Msg
			if err := r.Unpack(*payload); err != nil {
				t.Fatal(err)
			}
			wantStale := tc.stale && (tc.err == nil || tc.err == sequence.ErrExit)
			wantRcode := dns.RcodeSuccess
			if tc.err != nil && tc.err != sequence.ErrExit {
				wantRcode = dns.RcodeServerFailure
			}
			if stale != wantStale || r.Rcode != wantRcode {
				t.Fatalf("response stale=%v rcode=%d, want %v/%d", stale, r.Rcode, wantStale, wantRcode)
			}
		})
	}
}

func TestEntryHandlerPackingFailureClearsProvenance(t *testing.T) {
	h := NewEntryHandler(EntryHandlerOpts{Entry: sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
		qCtx.SetResponse(new(dns.Msg))
		qCtx.SetCacheResponseStale(true)
		return nil
	})})
	q := new(dns.Msg)
	q.SetQuestion("pack-failure.example.", dns.TypeA)
	payload, stale := h.HandleResponse(context.Background(), q, server.QueryMeta{}, func(*dns.Msg) (*[]byte, error) {
		return nil, errors.New("packing failed")
	})
	if payload != nil || stale {
		t.Fatal("packing failure returned payload or stale provenance")
	}
}
