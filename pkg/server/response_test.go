package server

import (
	"context"
	"testing"

	"github.com/miekg/dns"
)

type legacyResponseTestHandler struct {
	call func(context.Context, *dns.Msg, QueryMeta, func(*dns.Msg) (*[]byte, error)) *[]byte
}

func (h legacyResponseTestHandler) Handle(ctx context.Context, q *dns.Msg, meta QueryMeta, pack func(*dns.Msg) (*[]byte, error)) *[]byte {
	return h.call(ctx, q, meta, pack)
}

type staleResponseTestHandler struct {
	legacyResponseTestHandler
}

func (h staleResponseTestHandler) HandleResponse(ctx context.Context, q *dns.Msg, meta QueryMeta, pack func(*dns.Msg) (*[]byte, error)) (*[]byte, bool) {
	return h.call(ctx, q, meta, pack), true
}

func TestHandleResponsePreservesProvenanceAndLegacyCompatibility(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("response.example.", dns.TypeA)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	meta := QueryMeta{FromUDP: true, PreFastDomainSet: "routing-policy"}
	wire := []byte{1, 2, 3}
	packCalls := 0
	pack := func(msg *dns.Msg) (*[]byte, error) {
		packCalls++
		if msg != q {
			t.Fatal("dispatch replaced query")
		}
		return &wire, nil
	}
	legacy := legacyResponseTestHandler{call: func(gotCtx context.Context, gotQ *dns.Msg, gotMeta QueryMeta, gotPack func(*dns.Msg) (*[]byte, error)) *[]byte {
		if gotCtx != ctx || gotQ != q || gotMeta != meta {
			t.Fatal("dispatch lost request context or metadata")
		}
		payload, err := gotPack(gotQ)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}}
	for _, tc := range []struct {
		name  string
		h     Handler
		stale bool
	}{
		{"legacy", legacy, false},
		{"metadata", staleResponseTestHandler{legacy}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, stale := HandleResponse(tc.h, ctx, q, meta, pack)
			if payload != &wire || stale != tc.stale {
				t.Fatalf("dispatch changed response: payload=%p stale=%v", payload, stale)
			}
		})
	}
	if packCalls != 2 {
		t.Fatalf("pack calls = %d, want 2", packCalls)
	}
}
