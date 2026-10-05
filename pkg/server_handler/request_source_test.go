package server_handler

import (
	"context"
	"net/netip"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestEntryHandlerResolvesRequestSource(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		listener, meta, want server.RequestSource
	}{
		{"default_loopback", server.RequestSourceUnspecified, server.RequestSourceUnspecified, server.RequestSourceUser},
		{"listener_prewarm", server.RequestSourcePrewarm, server.RequestSourceUnspecified, server.RequestSourcePrewarm},
		{"listener_refresh", server.RequestSourceRefresh, server.RequestSourceUnspecified, server.RequestSourceRefresh},
		{"internal_refresh", server.RequestSourceUser, server.RequestSourceRefresh, server.RequestSourceRefresh},
		{"internal_prewarm", server.RequestSourceRefresh, server.RequestSourcePrewarm, server.RequestSourcePrewarm},
		{"explicit_user_meta", server.RequestSourcePrewarm, server.RequestSourceUser, server.RequestSourceUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewEntryHandler(EntryHandlerOpts{
				RequestSource: tc.listener,
				Entry: sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
					if qCtx.ServerMeta.RequestSource != tc.want {
						t.Errorf("source = %v, want %v", qCtx.ServerMeta.RequestSource, tc.want)
					}
					if qCtx.ServerMeta.ClientAddr != netip.MustParseAddr("127.0.0.1") || !qCtx.HasFastFlag(39) {
						t.Error("normal request lost client metadata or fast mark")
					}
					return nil
				}),
			})
			q := new(dns.Msg)
			q.SetQuestion("source.example.", dns.TypeA)
			payload := h.Handle(context.Background(), q, server.QueryMeta{
				RequestSource: tc.meta,
				ClientAddr:    netip.MustParseAddr("127.0.0.1"),
				PreFastFlags:  1 << 39,
			}, func(m *dns.Msg) (*[]byte, error) {
				wire, err := m.Pack()
				buf := pool.GetBuf(len(wire))
				copy(*buf, wire)
				return buf, err
			})
			if payload == nil {
				t.Fatal("expected response")
			}
			pool.ReleaseBuf(payload)
		})
	}
}
