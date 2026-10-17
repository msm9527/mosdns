package udp_server

import (
	"context"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestRefreshCannotOverwriteOrDeleteNewerGeneration(t *testing.T) {
	for _, mode := range []string{"ordinary", "NXDOMAIN", "AD"} {
		for _, mutation := range []string{"new-answer", "flush"} {
			t.Run(mode+"/"+mutation, func(t *testing.T) {
				q := refreshTestQuestion()
				h := &fastHandler{fc: refreshTestCache()}
				old := refreshTestSeed(t, h, q, server.QueryMeta{}, "1.1.1.1")
				started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				h.next = refreshTestHandler{resolve: func(q *dns.Msg, _ server.QueryMeta) (*[]byte, bool) {
					close(started)
					<-release
					return refreshTestPack(t, refreshTestResponse(q, mode)), false
				}}
				go func() {
					defer close(done)
					h.refreshExpiredCache(context.Background(), q.Copy(), server.QueryMeta{}, pool.PackBuffer, old)
				}()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("refresh did not start")
				}
				var newer *fastCacheItem
				if mutation == "flush" {
					h.fc.Flush()
				} else {
					newer = refreshTestSeed(t, h, q, server.QueryMeta{}, "3.3.3.3")
				}
				close(release)
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("refresh did not finish")
				}
				if got, _ := h.fc.findItem(old.hash, old.qname, old.qtype); got != newer {
					t.Fatal("old refresh overwrote/deleted a newer generation or resurrected a flushed item")
				}
			})
		}
	}
}

func TestRefreshUsesGenerationCapturedBeforeBackgroundLaunch(t *testing.T) {
	for _, mode := range []string{"ordinary", "NXDOMAIN"} {
		t.Run(mode, func(t *testing.T) {
			q := refreshTestQuestion()
			h := &fastHandler{fc: refreshTestCache()}
			old := refreshTestSeed(t, h, q, server.QueryMeta{}, "1.1.1.1")
			payload, _, captured := h.fc.copyResponseItem(q.Id, q.Question[0].Name, dns.TypeA, false, false, h.runtimeRevision())
			if payload == nil || captured != old {
				t.Fatal("stale response copy did not capture its generation")
			}
			pool.ReleaseBuf(payload)
			// Another query finishes before the background goroutine is scheduled.
			newer := refreshTestSeed(t, h, q, server.QueryMeta{}, "3.3.3.3")
			h.next = refreshTestHandler{resolve: func(q *dns.Msg, _ server.QueryMeta) (*[]byte, bool) {
				return refreshTestPack(t, refreshTestResponse(q, mode)), false
			}}
			h.refreshExpiredCache(context.Background(), q.Copy(), server.QueryMeta{}, pool.PackBuffer, captured)
			if got, _ := h.fc.findItem(old.hash, old.qname, old.qtype); got != newer {
				t.Fatal("background launch race selected a newer item as the invalidation target")
			}
		})
	}
}
