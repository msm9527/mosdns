package server_utils

import (
	"context"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestResolveRequestSource(t *testing.T) {
	for _, tc := range []struct {
		entry, option string
		want          server.RequestSource
	}{
		{"sequence_6666", "", server.RequestSourceUser},
		{"sequence_requery", "", server.RequestSourcePrewarm},
		{"sequence_requery_refresh", "", server.RequestSourceRefresh},
		{"my_requery", "", server.RequestSourceUser},
		{"sequence_requery_suffix", "", server.RequestSourceUser},
		{"sequence_requery_refresh_suffix", "", server.RequestSourceUser},
		{"Sequence_requery", "", server.RequestSourceUser},
		{"sequence_requery", "user", server.RequestSourceUser},
		{"sequence_requery_refresh", "prewarm", server.RequestSourcePrewarm},
		{"renamed", "refresh", server.RequestSourceRefresh},
	} {
		got, err := ResolveRequestSource(tc.entry, tc.option)
		if err != nil || got != tc.want {
			t.Errorf("entry %q option %q = %v/%v, want %v", tc.entry, tc.option, got, err, tc.want)
		}
	}
	for _, option := range []string{"USER", " user", "user ", "background", "unknown", " "} {
		if _, err := ResolveRequestSource("sequence_requery", option); err == nil {
			t.Errorf("accepted invalid request_source %q", option)
		}
	}
}

func TestNewHandlerCompatibility(t *testing.T) {
	for _, entry := range []string{"sequence_6666", "sequence_requery", "sequence_requery_refresh"} {
		want, _ := ResolveRequestSource(entry, "")
		m := coremain.NewTestMosdnsWithPlugins(map[string]any{
			entry: sequence.ExecutableFunc(func(_ context.Context, qCtx *query_context.Context) error {
				if qCtx.ServerMeta.RequestSource != want {
					t.Errorf("entry %s source = %v, want %v", entry, qCtx.ServerMeta.RequestSource, want)
				}
				return nil
			}),
		})
		h, err := NewHandler(coremain.NewBP("source_test", m), entry, false)
		if err != nil {
			t.Fatal(err)
		}
		q := new(dns.Msg)
		q.SetQuestion("source.example.", dns.TypeA)
		payload := h.Handle(context.Background(), q, server.QueryMeta{}, func(m *dns.Msg) (*[]byte, error) {
			wire, err := m.Pack()
			buf := pool.GetBuf(len(wire))
			copy(*buf, wire)
			return buf, err
		})
		if payload == nil {
			t.Fatal("expected response")
		}
		pool.ReleaseBuf(payload)
	}
}
