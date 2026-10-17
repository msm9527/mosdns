package udp_server

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func TestStaleRefreshKeepsClientDirectVariant(t *testing.T) {
	q := refreshTestQuestion()
	h := &fastHandler{fc: refreshTestCache()}
	other := refreshTestSeed(t, h, q, server.QueryMeta{}, "9.9.9.9")
	meta := server.QueryMeta{PreFastStaleRefresh: true, PreFastFlags: 1<<39 | 1<<48}
	old := refreshTestSeed(t, h, q, meta, "1.1.1.1")
	h.next = refreshTestHandler{resolve: func(q *dns.Msg, meta server.QueryMeta) (*[]byte, bool) {
		if meta.PreFastFlags != 0 || meta.PreFastStaleRefresh || meta.RequestSource != server.RequestSourceRefresh {
			t.Error("background resolver retained stale policy metadata")
		}
		return refreshTestPack(t, refreshTestResponse(q, "ordinary")), false
	}}
	stale := readStalePolicyResponse(t, h.Handle(context.Background(), q.Copy(), meta, pool.PackBuffer))
	if stale.Answer[0].(*dns.A).A.String() != "1.1.1.1" {
		t.Fatal("direct client received the other client's stale variant")
	}
	deadline := time.Now().Add(time.Second)
	for {
		current, _ := h.fc.findItem(old.hash, old.qname, old.qtype)
		if current != old {
			if current == nil || !current.clientDirect || current.ruleFlags != meta.PreFastFlags {
				t.Fatal("background refresh lost direct client flags or its cache variant")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("direct client's stale cache was never refreshed")
		}
		time.Sleep(time.Millisecond)
	}
	if got, _ := h.fc.findItem(other.hash, other.qname, other.qtype); got != other {
		t.Fatal("direct refresh modified the unrelated client variant")
	}
}

func TestSourceAwareRefreshedResponseRemainsFastCacheHit(t *testing.T) {
	q := refreshTestQuestion()
	mapper := newUDPValidationMapper(t, &udpStaticRules{rules: []string{"full:refresh-transition.example"}})
	h := &fastHandler{fc: refreshTestCache(), dm: mapper}
	bypass := udpValidationBypass(mapper, h.fc, server.RequestSourceUser)
	req, err := q.Pack()
	if err != nil {
		t.Fatal(err)
	}
	addr := netip.MustParseAddrPort("127.0.0.1:5300")
	_, _, flags, dset, matched, _, evidence := bypass(len(req), append([]byte(nil), req...), addr)
	meta := server.QueryMeta{PreFastFlags: flags, PreFastDomainSet: dset, PreFastDomainMatched: matched, PreFastRuleMatch: evidence}
	old := refreshTestSeed(t, h, q, meta, "1.1.1.1")
	h.next = refreshTestHandler{resolve: func(q *dns.Msg, _ server.QueryMeta) (*[]byte, bool) {
		return refreshTestPack(t, refreshTestResponse(q, "ordinary")), false
	}}
	h.refreshExpiredCache(context.Background(), q.Copy(), meta, pool.PackBuffer, old)
	item, _ := h.fc.findItem(old.hash, old.qname, old.qtype)
	if item == nil || item == old || item.ruleMatch != evidence || !item.ruleMatch.Known {
		t.Fatal("refresh lost initial source-aware matcher evidence")
	}
	buf := make([]byte, 512)
	copy(buf, req)
	action, _, _, _, _, stale, _ := bypass(len(req), buf, addr)
	if action != server.FastActionReply || stale {
		t.Fatal("successfully refreshed response no longer hits the fast cache")
	}
}

func TestRefreshDoesNotAttachOldResponseToChangedRules(t *testing.T) {
	for _, change := range []string{"validation-expired", "rules-replaced", "control-changed"} {
		t.Run(change, func(t *testing.T) {
			q := refreshTestQuestion()
			provider := &udpExpiringHotRules{domain: "refresh-transition.example"}
			provider.verified.Store(time.Now().UnixMilli())
			mapper := newUDPValidationMapper(t, provider)
			h := &fastHandler{fc: refreshTestCache(), dm: mapper}
			marks, set, matched := mapper.FastMatchWithSource(q.Question[0].Name, server.RequestSourceRefresh)
			meta := server.QueryMeta{PreFastFlags: fastMarksFromList(marks), PreFastDomainSet: set, PreFastRuleMatch: server.FastRuleMatchMeta{Known: true, Flags: fastMarksFromList(marks), DomainSet: set, Matched: matched}}
			old := refreshTestSeed(t, h, q, meta, "1.1.1.1")
			h.next = refreshTestHandler{resolve: func(q *dns.Msg, _ server.QueryMeta) (*[]byte, bool) {
				switch change {
				case "validation-expired":
					provider.verified.Store(time.Now().Add(-2 * time.Hour).UnixMilli())
				case "rules-replaced":
					if err := mapper.ReplaceHotRules("memory", nil); err != nil {
						t.Fatal(err)
					}
				case "control-changed":
					h.blockSwitch = newFastSwitchValue(testSwitchPlugin{value: "on"})
				}
				return refreshTestPack(t, refreshTestResponse(q, "ordinary")), false
			}}
			h.refreshExpiredCache(context.Background(), q.Copy(), meta, pool.PackBuffer, old)
			if item, _ := h.fc.findItem(old.hash, old.qname, old.qtype); item != nil {
				t.Fatal("in-flight response was stored under changed routing/policy evidence")
			}
			if provider.demand.Load() != 0 {
				t.Fatal("background rule validation triggered user demand work")
			}
		})
	}
}
