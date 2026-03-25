package udp_server

import (
	"context"
	"hash/maphash"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

type staticHandler struct {
	payload []byte
}

func (h staticHandler) Handle(_ context.Context, _ *dns.Msg, _ server.QueryMeta, _ func(*dns.Msg) (*[]byte, error)) *[]byte {
	payload := append([]byte(nil), h.payload...)
	return &payload
}

func makeRcodeResponse(t *testing.T, name string, qtype uint16, id uint16, rcode int) []byte {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	q.Id = id

	resp := new(dns.Msg)
	resp.SetRcode(q, rcode)
	return mustPack(t, resp)
}

func TestShouldStoreFastResponse(t *testing.T) {
	name := "rcode.example."

	tests := []struct {
		name    string
		payload []byte
		want    bool
	}{
		{
			name:    "cache noerror answer",
			payload: makeAnswer(t, name, dns.TypeA, 0x1111, 30),
			want:    true,
		},
		{
			name:    "cache nxdomain",
			payload: makeRcodeResponse(t, name, dns.TypeA, 0x1111, dns.RcodeNameError),
			want:    true,
		},
		{
			name:    "skip servfail",
			payload: makeRcodeResponse(t, name, dns.TypeA, 0x1111, dns.RcodeServerFailure),
			want:    false,
		},
		{
			name:    "skip malformed payload",
			payload: []byte{0x01, 0x02, 0x03},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldStoreFastResponse(tt.payload); got != tt.want {
				t.Fatalf("shouldStoreFastResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFastHandlerSkipsCachingServerFailure(t *testing.T) {
	name := "servfail.example."
	qtype := uint16(dns.TypeA)
	resp := makeRcodeResponse(t, name, qtype, 0x2222, dns.RcodeServerFailure)

	fc := newFastCache(fastCacheConfig{
		internalTTL: time.Minute,
		ttlMax:      30,
	}, &fastStats{})
	handler := &fastHandler{
		next: staticHandler{payload: resp},
		fc:   fc,
		sw:   testSwitchPlugin{value: "on"},
	}

	q := new(dns.Msg)
	q.SetQuestion(name, qtype)
	q.Id = 0x9999
	handler.Handle(context.Background(), q, server.QueryMeta{}, nil)

	query := makeQuery(t, name, qtype, 0x9999)
	buf := make([]byte, len(resp))
	copy(buf, query)
	hash := maphash.String(maphashSeed, name) ^ uint64(qtype)
	action, _, _, _, _ := fc.GetOrUpdating(hash, buf, name, qtype, true)
	if action != server.FastActionContinue {
		t.Fatalf("expected SERVFAIL response to skip fast cache, got action=%d", action)
	}
}
