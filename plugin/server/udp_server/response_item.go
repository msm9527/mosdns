package udp_server

import (
	"encoding/binary"
	"strings"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
)

func (fc *fastCache) newResponseItem(qname string, qtype uint16, resp []byte, dset string, fakeIP bool, clientDirect bool, ruleFlags uint64, ruleRevision fastRuleRevision, ruleMatch ...server.FastRuleMatchMeta) *fastCacheItem {
	if !shouldStoreFastResponse(resp) || fc.shouldBypassDomainSet(dset) {
		return nil
	}
	withoutOPTLen, ok := fastResponseWithoutOPTLen(resp)
	if !ok {
		return nil
	}
	qname = fastNormalizeQNameString(qname)
	baseHash := fastQNameHashString(qname, qtype)
	h := fastCachePolicyHash(baseHash, clientDirect)

	bakedResp := make([]byte, len(resp))
	copy(bakedResp, resp)
	offsets := findTTLOffsets(bakedResp)
	now := time.Now()
	authenticatedUntil := fastAuthenticatedUntil(resp, offsets, now)
	for _, off := range offsets {
		if off+4 <= len(bakedResp) {
			ttl := binary.BigEndian.Uint32(bakedResp[off : off+4])
			binary.BigEndian.PutUint32(bakedResp[off:off+4], clampTTL(ttl, fc.cfg.ttlMin, fc.cfg.ttlMax))
		}
	}
	internalTTL := fc.cfg.internalTTL
	if overrideTTL, _, ok := fc.domainSetTTLOverride(dset); ok && overrideTTL > 0 {
		internalTTL = overrideTTL
	}

	item := &fastCacheItem{
		resp:                 bakedResp,
		audit:                fastAuditResponseMetaFromPayloadIfEnabled(fc.cfg.auditEnabled, strings.TrimSuffix(qname, "."), bakedResp),
		expire:               now.Add(internalTTL).Unix(),
		authenticatedUntil:   authenticatedUntil,
		updating:             0,
		domainSet:            dset,
		domainMapperRevision: ruleRevision.domainMapper,
		rewriteRevision:      ruleRevision.rewrite,
		controlRevision:      ruleRevision.control,
		fakeIP:               fakeIP,
		clientDirect:         clientDirect,
		hash:                 h,
		ruleFlags:            ruleFlags,
		qname:                qname,
		qtype:                qtype,
		withoutOPTLen:        withoutOPTLen,
	}
	if len(ruleMatch) > 0 {
		item.ruleMatch = ruleMatch[0]
	}
	return item
}

func (fc *fastCache) recordResponseStore(item *fastCacheItem) {
	if fc.stats != nil {
		fc.stats.cacheStore.AddShard(item.hash, 1)
	}
}

func (fc *fastCache) copyResponseItem(txid uint16, qname string, qtype uint16, allowFakeIP, clientDirect bool, expectedRuleRevision fastRuleRevision, withOPT ...bool) (*[]byte, fastAuditResponseMeta, *fastCacheItem) {
	hash := fastCachePolicyHash(fastQNameHashString(qname, qtype), clientDirect)
	ptr, _ := fc.findItem(hash, qname, qtype)
	if ptr == nil || ptr.clientDirect != clientDirect {
		return nil, fastAuditResponseMeta{}, nil
	}
	if !expectedRuleRevision.empty() && !expectedRuleRevision.matches(ptr) {
		return nil, fastAuditResponseMeta{}, nil
	}
	if ptr.fakeIP && !allowFakeIP {
		return nil, fastAuditResponseMeta{}, nil
	}
	if fc.shouldBypassDomainSet(ptr.domainSet) {
		return nil, fastAuditResponseMeta{}, nil
	}
	if len(ptr.resp) == 0 {
		return nil, fastAuditResponseMeta{}, nil
	}
	size := len(ptr.resp)
	if len(withOPT) > 0 {
		size = ptr.replySize(withOPT[0])
	}
	resp := pool.GetBuf(size)
	if len(withOPT) == 0 {
		// The legacy API has no request OPT policy; retain its original wire form.
		copy(*resp, ptr.resp)
	} else if ptr.copyReply(*resp, withOPT[0]) == 0 {
		pool.ReleaseBuf(resp)
		return nil, fastAuditResponseMeta{}, nil
	}
	binary.BigEndian.PutUint16((*resp)[:2], txid)
	applyFastCacheReplyPolicy(*resp, ptr, atomic.LoadInt64(&ptr.expire), fastNowUnix())
	return resp, ptr.audit, ptr
}
