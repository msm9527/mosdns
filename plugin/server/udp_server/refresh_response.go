package udp_server

import (
	"context"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/miekg/dns"
)

func (h *fastHandler) refreshExpiredCache(ctx context.Context, q *dns.Msg, meta server.QueryMeta, pack func(*dns.Msg) (*[]byte, error), targets ...*fastCacheItem) {
	cacheableRequest := fastRequestCacheable(q)
	// Capture the exact stale generation before running the resolver. The normal
	// stale reply supplies the item that was copied before starting this goroutine.
	var target *fastCacheItem
	if len(targets) > 0 {
		target = targets[0]
	} else if q != nil && len(q.Question) == 1 {
		hash := fastCachePolicyHash(fastQNameHashString(q.Question[0].Name, q.Question[0].Qtype), meta.PreFastFlags&(1<<39) != 0)
		target, _ = h.fc.findItem(hash, q.Question[0].Name, q.Question[0].Qtype)
	}
	storeMeta := meta
	revision := h.runtimeRevision()
	initialMatch, sourceAware := h.refreshRuleMatch(q)
	stable := target != nil && revision.matches(target)
	if sourceAware {
		stable = stable && storeMeta.PreFastRuleMatch.Known && storeMeta.PreFastRuleMatch == initialMatch
	}

	// The resolver must classify this background request again. Keep the original
	// client variant and matcher evidence separately for conditional admission.
	meta.RequestSource = server.RequestSourceRefresh
	meta.PreFastStaleRefresh = false
	meta.PreFastDomainMatched = false
	meta.PreFastDomainSet = ""
	meta.PreFastFlags = 0
	meta.PreFastRuleMatch = server.FastRuleMatchMeta{}
	payload, stale := server.HandleResponse(h.next, ctx, q, meta, pack)
	if payload == nil {
		if h.fc != nil && h.fc.stats != nil {
			h.fc.stats.refreshNoPayload.Add(1)
		}
		return
	}
	defer pool.ReleaseBuf(payload)
	if !stale && cacheableRequest && validFastRefreshResponse(q, *payload) {
		stable = stable && revision == h.runtimeRevision()
		if sourceAware {
			currentMatch, _ := h.refreshRuleMatch(q)
			stable = stable && currentMatch == initialMatch
		}
		if stable && h.storeRefreshedResponse(q, storeMeta, *payload, revision, target) {
			if h.fc.stats != nil {
				h.fc.stats.refreshStore.Add(1)
			}
			return
		}
		// A fresh negative or authenticated/signed response belongs in the shared
		// cache. It still supersedes the old fast answer. Never remove a newer item.
		h.fc.replaceRefreshedItem(target, nil)
	}
	if h.fc != nil && h.fc.stats != nil {
		h.fc.stats.refreshStoreSkip.Add(1)
	}
}

func (h *fastHandler) refreshRuleMatch(q *dns.Msg) (server.FastRuleMatchMeta, bool) {
	mapper, ok := h.dm.(DomainMapperWithSourcePlugin)
	if !ok || q == nil || len(q.Question) != 1 {
		return server.FastRuleMatchMeta{}, false
	}
	marks, domainSet, matched := mapper.FastMatchWithSource(q.Question[0].Name, server.RequestSourceRefresh)
	return server.FastRuleMatchMeta{Known: true, Flags: fastMarksFromList(marks), DomainSet: domainSet, Matched: matched}, true
}

func validFastRefreshResponse(q *dns.Msg, wire []byte) bool {
	if q == nil || len(q.Question) != 1 {
		return false
	}
	var r dns.Msg
	if r.Unpack(wire) != nil || !r.Response || r.Truncated || r.Id != q.Id || r.Opcode != q.Opcode ||
		(r.Rcode != dns.RcodeSuccess && r.Rcode != dns.RcodeNameError) || len(r.Question) != 1 {
		return false
	}
	return r.Question[0].Qtype == q.Question[0].Qtype && r.Question[0].Qclass == q.Question[0].Qclass &&
		fastQNameEqualString(r.Question[0].Name, q.Question[0].Name)
}

func (h *fastHandler) storeRefreshedResponse(q *dns.Msg, meta server.QueryMeta, payload []byte, revision fastRuleRevision, target *fastCacheItem) bool {
	if h.fc == nil || !h.fastCacheEnabled() || !fastRequestCacheable(q) || !shouldStoreFastResponse(payload) {
		return false
	}
	domainSet := meta.PreFastDomainSet
	if domainSet == "" {
		domainSet = meta.PreFastRuleMatch.DomainSet
	}
	fakeIP := isFakeIPResponse(payload)
	if fakeIP && !h.allowFakeIPCache() {
		return false
	}
	item := h.fc.newResponseItem(q.Question[0].Name, q.Question[0].Qtype, payload, domainSet, fakeIP,
		meta.PreFastFlags&(1<<39) != 0, meta.PreFastFlags, revision, meta.PreFastRuleMatch)
	if item == nil || !h.fc.replaceRefreshedItem(target, item) {
		return false
	}
	h.fc.recordResponseStore(item)
	return true
}

func (fc *fastCache) replaceRefreshedItem(target, replacement *fastCacheItem) bool {
	if target == nil || (replacement != nil && (replacement.hash != target.hash || replacement.qname != target.qname || replacement.qtype != target.qtype)) {
		return false
	}
	table := fc.loadTable()
	if table == nil {
		return false
	}
	bucket := &table.buckets[fastCacheBucketIndex(target.hash, table.mask)]
	for i := range bucket.slots {
		if bucket.slots[i].CompareAndSwap(target, replacement) {
			return true
		}
	}
	return false
}
