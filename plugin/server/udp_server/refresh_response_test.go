package udp_server

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

type refreshTestHandler struct {
	resolve func(*dns.Msg, server.QueryMeta) (*[]byte, bool)
}

func (h refreshTestHandler) Handle(ctx context.Context, q *dns.Msg, meta server.QueryMeta, pack func(*dns.Msg) (*[]byte, error)) *[]byte {
	wire, _ := h.HandleResponse(ctx, q, meta, pack)
	return wire
}

func (h refreshTestHandler) HandleResponse(_ context.Context, q *dns.Msg, meta server.QueryMeta, _ func(*dns.Msg) (*[]byte, error)) (*[]byte, bool) {
	return h.resolve(q, meta)
}

func refreshTestResponse(q *dns.Msg, mode string) *dns.Msg {
	r := new(dns.Msg)
	r.SetReply(q)
	r.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60}, A: net.IPv4(2, 2, 2, 2)}}
	switch mode {
	case "AD":
		r.AuthenticatedData = true
	case "RRSIG":
		r.Answer = append(r.Answer, &dns.RRSIG{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeRRSIG, Class: dns.ClassINET, Ttl: 60}, TypeCovered: dns.TypeA, Algorithm: 8, Labels: 2, OrigTtl: 60, Inception: uint32(time.Now().Add(-time.Hour).Unix()), Expiration: uint32(time.Now().Add(time.Hour).Unix()), SignerName: "example.", Signature: "AA=="})
	case "NXDOMAIN", "NODATA", "CNAME_NODATA":
		r.Answer = nil
		r.Ns = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60}, Ns: "ns.example.", Mbox: "hostmaster.example.", Minttl: 60}}
		if mode == "NXDOMAIN" {
			r.Rcode = dns.RcodeNameError
		}
		if mode == "CNAME_NODATA" {
			r.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: q.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60}, Target: "target.example."}}
		}
	case "SERVFAIL":
		r.Rcode = dns.RcodeServerFailure
		r.Answer = nil
	case "truncated":
		r.Truncated = true
	case "wrong_id":
		r.Id++
	case "wrong_name":
		r.Question[0].Name = "other.example."
	case "wrong_type":
		r.Question[0].Qtype = dns.TypeAAAA
	case "wrong_class":
		r.Question[0].Qclass = dns.ClassCHAOS
	case "not_response":
		r.Response = false
	}
	return r
}

func refreshTestPack(t *testing.T, r *dns.Msg) *[]byte {
	t.Helper()
	wire, err := pool.PackBuffer(r)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func refreshTestSeed(t *testing.T, h *fastHandler, q *dns.Msg, meta server.QueryMeta, ip string) *fastCacheItem {
	t.Helper()
	wire := makeAnswerWithIP(t, q.Question[0].Name, q.Question[0].Qtype, q.Id, 60, ip)
	if !h.storeFastResponse(q, meta, &wire) {
		t.Fatal("failed to seed response cache")
	}
	hash := fastCachePolicyHash(fastQNameHashString(q.Question[0].Name, q.Question[0].Qtype), meta.PreFastFlags&(1<<39) != 0)
	item, _ := h.fc.findItem(hash, q.Question[0].Name, q.Question[0].Qtype)
	if item == nil {
		t.Fatal("seed item missing")
	}
	atomic.StoreInt64(&item.expire, fastNowUnix()-1)
	atomic.StoreUint32(&item.updating, 1)
	return item
}

func refreshTestCache() *fastCache {
	return newFastCache(fastCacheConfig{internalTTL: time.Hour, staleMax: time.Hour, staleRetry: 30 * time.Second, ttlMin: 120, ttlMax: 900, responseSlots: 64, ruleSlots: 64}, nil)
}

func refreshTestQuestion() *dns.Msg {
	q := new(dns.Msg)
	q.SetQuestion("refresh-transition.example.", dns.TypeA)
	return q
}

func TestFreshRefreshSupersedesOldFastAnswer(t *testing.T) {
	for _, mode := range []string{"ordinary", "AD", "RRSIG", "NXDOMAIN", "NODATA", "CNAME_NODATA"} {
		t.Run(mode, func(t *testing.T) {
			q := refreshTestQuestion()
			h := &fastHandler{fc: refreshTestCache()}
			old := refreshTestSeed(t, h, q, server.QueryMeta{}, "1.1.1.1")
			fresh := refreshTestResponse(q, mode)
			h.next = refreshTestHandler{resolve: func(*dns.Msg, server.QueryMeta) (*[]byte, bool) {
				return refreshTestPack(t, fresh), false
			}}
			h.refreshExpiredCache(context.Background(), q.Copy(), server.QueryMeta{}, pool.PackBuffer, old)
			got, _ := h.fc.findItem(old.hash, old.qname, old.qtype)
			if mode == "ordinary" {
				if got == nil || got == old {
					t.Fatal("fresh ordinary response did not replace the old item")
				}
				r := new(dns.Msg)
				if err := r.Unpack(got.resp); err != nil || r.Answer[0].(*dns.A).A.String() != "2.2.2.2" {
					t.Fatalf("wrong refreshed response: %v, %v", r.Answer, err)
				}
			} else if got != nil {
				t.Fatal("old fast answer survived a fresh response that belongs in the shared cache")
			}
			// The next full-chain query must see the new positive or negative result.
			wire := h.Handle(context.Background(), q.Copy(), server.QueryMeta{}, pool.PackBuffer)
			defer pool.ReleaseBuf(wire)
			var r dns.Msg
			if wire == nil || r.Unpack(*wire) != nil || r.Rcode != fresh.Rcode || r.AuthenticatedData != fresh.AuthenticatedData || len(r.Answer) != len(fresh.Answer) {
				t.Fatal("next query did not see the fresh response")
			}
		})
	}
}

func TestFailedOrStaleRefreshPreservesOldFastAnswer(t *testing.T) {
	for _, mode := range []string{"nil", "SERVFAIL", "truncated", "malformed", "wrong_id", "wrong_name", "wrong_type", "wrong_class", "not_response", "shared_stale"} {
		t.Run(mode, func(t *testing.T) {
			q := refreshTestQuestion()
			h := &fastHandler{fc: refreshTestCache()}
			old := refreshTestSeed(t, h, q, server.QueryMeta{}, "1.1.1.1")
			h.next = refreshTestHandler{resolve: func(q *dns.Msg, _ server.QueryMeta) (*[]byte, bool) {
				if mode == "nil" {
					return nil, false
				}
				if mode == "malformed" {
					wire := pool.GetBuf(13)
					copy(*wire, []byte{0, 1, 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0, 255})
					return wire, false
				}
				return refreshTestPack(t, refreshTestResponse(q, mode)), mode == "shared_stale"
			}}
			h.refreshExpiredCache(context.Background(), q.Copy(), server.QueryMeta{}, pool.PackBuffer, old)
			if got, _ := h.fc.findItem(old.hash, old.qname, old.qtype); got != old {
				t.Fatal("failed or stale refresh removed/replaced the last useful stale answer")
			}
		})
	}
}
