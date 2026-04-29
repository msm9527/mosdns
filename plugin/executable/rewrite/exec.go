package rewrite

import (
	"context"
	"net/netip"

	"github.com/IrineSistiana/mosdns/v5/pkg/dnsutils"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

type responseState struct {
	answers []dns.RR
	seen    map[string]struct{}

	syntheticNoData    bool
	noDataResp         *dns.Msg
	nxdomainResp       *dns.Msg
	fallbackResp       *dns.Msg
	fallbackRcode      int
	hasFallbackRcode   bool
	recursionAvailable bool
	authenticatedData  bool
}

func (r *Rewrite) Exec(ctx context.Context, qCtx *query_context.Context, next sequence.ChainWalker) error {
	question, ok := rewriteQuestion(qCtx.Q())
	if !ok || !shouldRewriteQuestion(question) {
		return next.ExecNext(ctx, qCtx)
	}

	rule, ok := r.matchRule(question.Name)
	if !ok {
		return next.ExecNext(ctx, qCtx)
	}

	qCtx.StoreValue(query_context.KeyDomainSet, "重定向")
	qCtx.SetFastFlag(rewriteFastMark)
	qCtx.SetResponse(r.buildResponse(ctx, qCtx.Q(), rule))
	return nil
}

func rewriteQuestion(msg *dns.Msg) (dns.Question, bool) {
	if len(msg.Question) != 1 {
		return dns.Question{}, false
	}
	return msg.Question[0], true
}

func shouldRewriteQuestion(question dns.Question) bool {
	if question.Qclass != dns.ClassINET {
		return false
	}
	return question.Qtype == dns.TypeA || question.Qtype == dns.TypeAAAA
}

func (r *Rewrite) matchRule(name string) (*rewriteRule, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.matcher.Match(name)
}

func (r *Rewrite) buildResponse(ctx context.Context, query *dns.Msg, rule *rewriteRule) *dns.Msg {
	question := query.Question[0]
	state := newResponseState()

	collectStaticTargets(&state, question, rule.targets)
	r.collectDomainTargets(ctx, &state, query, rule.targets)
	return finalizeRewriteResponse(query, state)
}

func newResponseState() responseState {
	return responseState{seen: make(map[string]struct{})}
}

func collectStaticTargets(state *responseState, question dns.Question, targets []rewriteTarget) {
	hasStaticTarget := false
	for _, target := range targets {
		if target.kind != targetIP {
			continue
		}
		hasStaticTarget = true
		if rr := newAddressAnswer(question.Name, question.Qtype, target.ip); rr != nil {
			state.addAnswer(rr)
		}
	}
	if hasStaticTarget {
		state.syntheticNoData = true
	}
}

func newAddressAnswer(name string, qtype uint16, ip netip.Addr) dns.RR {
	switch {
	case qtype == dns.TypeA && ip.Is4():
		return &dns.A{
			Hdr: dns.RR_Header{
				Name:   name,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    fixedTTL,
			},
			A: ip.AsSlice(),
		}
	case qtype == dns.TypeAAAA && ip.Is6():
		return &dns.AAAA{
			Hdr: dns.RR_Header{
				Name:   name,
				Rrtype: dns.TypeAAAA,
				Class:  dns.ClassINET,
				Ttl:    fixedTTL,
			},
			AAAA: ip.AsSlice(),
		}
	default:
		return nil
	}
}

func (r *Rewrite) collectDomainTargets(ctx context.Context, state *responseState, query *dns.Msg, targets []rewriteTarget) {
	for _, target := range targets {
		if target.kind != targetDomain {
			continue
		}
		state.mergeResolution(r.resolveDomainTarget(ctx, query, target.domain))
	}
}

func cloneFlattenedAnswer(answer dns.RR, qName string, qtype uint16) dns.RR {
	if answer.Header().Rrtype != qtype {
		return nil
	}
	rr := dns.Copy(answer)
	rr.Header().Name = qName
	rr.Header().Ttl = fixedTTL
	return rr
}

func (s *responseState) addAnswer(rr dns.RR) {
	key := rr.String()
	if _, ok := s.seen[key]; ok {
		return
	}
	s.seen[key] = struct{}{}
	s.answers = append(s.answers, rr)
}

func (s *responseState) mergeResolution(result targetResolution) {
	s.recursionAvailable = s.recursionAvailable || result.recursionAvailable
	s.authenticatedData = s.authenticatedData || result.authenticatedData

	for _, answer := range result.answers {
		s.addAnswer(answer)
	}
	if result.syntheticNoData {
		s.syntheticNoData = true
	}
	switch result.negativeKind {
	case negativeNoData:
		if s.noDataResp == nil {
			s.noDataResp = result.negativeResp
		}
	case negativeNXDomain:
		if s.nxdomainResp == nil {
			s.nxdomainResp = result.negativeResp
		}
	case negativeFallback:
		if s.fallbackResp == nil {
			s.fallbackResp = result.negativeResp
		}
	}
	if result.hasFallbackRcode && !s.hasFallbackRcode {
		s.fallbackRcode = result.fallbackRcode
		s.hasFallbackRcode = true
	}
}

func finalizeRewriteResponse(query *dns.Msg, state responseState) *dns.Msg {
	if len(state.answers) > 0 {
		resp := new(dns.Msg).SetReply(query)
		resp.Answer = state.answers
		applyResponseFlags(resp, state)
		return resp
	}
	if state.noDataResp != nil {
		resp := cloneNegativeResponse(query, state.noDataResp)
		applyResponseFlags(resp, state)
		return resp
	}
	if state.syntheticNoData {
		resp := newEmptyRewriteReply(query, dns.RcodeSuccess)
		applyResponseFlags(resp, state)
		return resp
	}
	if state.nxdomainResp != nil {
		resp := cloneNegativeResponse(query, state.nxdomainResp)
		applyResponseFlags(resp, state)
		return resp
	}
	if state.fallbackResp != nil {
		resp := cloneNegativeResponse(query, state.fallbackResp)
		applyResponseFlags(resp, state)
		return resp
	}
	if state.hasFallbackRcode {
		resp := new(dns.Msg).SetRcode(query, state.fallbackRcode)
		applyResponseFlags(resp, state)
		return resp
	}
	return new(dns.Msg).SetRcode(query, dns.RcodeServerFailure)
}

func cloneNegativeResponse(query, source *dns.Msg) *dns.Msg {
	resp := source.Copy()
	resp.Id = query.Id
	resp.Question = append(resp.Question[:0], query.Question...)
	resp.Answer = nil
	return resp
}

func applyResponseFlags(resp *dns.Msg, state responseState) {
	resp.RecursionAvailable = state.recursionAvailable
	resp.AuthenticatedData = state.authenticatedData
}

func newEmptyRewriteReply(query *dns.Msg, rcode int) *dns.Msg {
	resp := new(dns.Msg).SetRcode(query, rcode)
	name := "."
	if len(query.Question) > 0 {
		name = query.Question[0].Name
	}
	resp.Ns = []dns.RR{dnsutils.FakeSOA(name)}
	return resp
}
