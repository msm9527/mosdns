package rewrite

import (
	"context"

	"github.com/miekg/dns"
)

const maxCNAMEHops = 8

type negativeKind uint8

const (
	negativeNone negativeKind = iota
	negativeNoData
	negativeNXDomain
	negativeFallback
)

type targetResolution struct {
	answers []dns.RR

	negativeResp       *dns.Msg
	negativeKind       negativeKind
	syntheticNoData    bool
	fallbackRcode      int
	hasFallbackRcode   bool
	recursionAvailable bool
	authenticatedData  bool
}

func (r *Rewrite) resolveDomainTarget(ctx context.Context, query *dns.Msg, targetDomain string) targetResolution {
	result := targetResolution{}
	current := dns.Fqdn(targetDomain)
	seen := make(map[string]struct{}, maxCNAMEHops)

	for hop := 0; hop < maxCNAMEHops; hop++ {
		if _, ok := seen[current]; ok {
			result.hasFallbackRcode = true
			result.fallbackRcode = dns.RcodeServerFailure
			return result
		}
		seen[current] = struct{}{}

		resp, err := r.exchangeUpstream(ctx, newUpstreamQuery(query, current))
		if err != nil || resp == nil {
			result.hasFallbackRcode = true
			result.fallbackRcode = dns.RcodeServerFailure
			return result
		}

		result.recursionAvailable = result.recursionAvailable || resp.RecursionAvailable
		result.authenticatedData = result.authenticatedData || resp.AuthenticatedData

		if resp.Rcode != dns.RcodeSuccess {
			result.negativeResp = resp.Copy()
			if resp.Rcode == dns.RcodeNameError {
				result.negativeKind = negativeNXDomain
				return result
			}
			result.negativeKind = negativeFallback
			result.fallbackRcode = resp.Rcode
			result.hasFallbackRcode = true
			return result
		}

		result.answers = collectFlattenedAnswers(resp.Answer, query.Question[0].Name, query.Question[0].Qtype)
		if len(result.answers) > 0 {
			return result
		}

		next, ok := findCNAMETarget(current, resp.Answer)
		if !ok {
			result.negativeResp = resp.Copy()
			result.negativeKind = negativeNoData
			result.syntheticNoData = true
			return result
		}
		current = next
	}

	result.hasFallbackRcode = true
	result.fallbackRcode = dns.RcodeServerFailure
	return result
}

func newUpstreamQuery(query *dns.Msg, targetDomain string) *dns.Msg {
	msg := new(dns.Msg)
	msg.SetQuestion(targetDomain, query.Question[0].Qtype)
	msg.AuthenticatedData = query.AuthenticatedData
	msg.RecursionDesired = query.RecursionDesired
	msg.CheckingDisabled = query.CheckingDisabled

	if opt := query.IsEdns0(); opt != nil {
		msg.Extra = append(msg.Extra, dns.Copy(opt))
	}
	return msg
}

func collectFlattenedAnswers(answers []dns.RR, qName string, qtype uint16) []dns.RR {
	out := make([]dns.RR, 0, len(answers))
	for _, answer := range answers {
		rr := cloneFlattenedAnswer(answer, qName, qtype)
		if rr != nil {
			out = append(out, rr)
		}
	}
	return out
}

func findCNAMETarget(current string, answers []dns.RR) (string, bool) {
	for _, answer := range answers {
		cname, ok := answer.(*dns.CNAME)
		if !ok {
			continue
		}
		if dns.Fqdn(cname.Hdr.Name) != current {
			continue
		}
		return dns.Fqdn(cname.Target), true
	}
	return "", false
}
