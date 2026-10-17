package domain_mapper

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
)

type compiledMatcherSet struct {
	full    *domain.FullMatcher[*compiledMatch]
	domain  *domain.SubDomainMatcher[*compiledMatch]
	regex   *domain.RegexMatcher[*compiledMatch]
	keyword *domain.KeywordMatcher[*compiledMatch]
	cache   atomic.Pointer[compiledMatchCache]
}

func newCompiledMatcherSet() *compiledMatcherSet {
	return &compiledMatcherSet{
		full:    domain.NewFullMatcher[*compiledMatch](),
		domain:  domain.NewSubDomainMatcher[*compiledMatch](),
		regex:   domain.NewRegexMatcher[*compiledMatch](),
		keyword: domain.NewKeywordMatcher[*compiledMatch](),
	}
}

func (m *compiledMatcherSet) Add(rule string, compiled *compiledMatch) error {
	rule = strings.TrimSpace(rule)
	if rule == "" || compiled == nil {
		return nil
	}
	// Production builds a new matcher before publishing it. Also invalidate
	// memoized evidence when a caller adds a rule to an unpublished test matcher.
	if cache := m.cache.Load(); cache != nil {
		cache.Flush()
	}
	typ, pattern, ok := strings.Cut(rule, ":")
	if !ok {
		typ = domain.MatcherDomain
		pattern = rule
	}
	switch typ {
	case domain.MatcherFull:
		return m.full.Add(pattern, compiled)
	case domain.MatcherDomain:
		return m.domain.Add(pattern, compiled)
	case domain.MatcherRegexp:
		return m.regex.Add(pattern, compiled)
	case domain.MatcherKeyword:
		return m.keyword.Add(pattern, compiled)
	default:
		return fmt.Errorf("unsupported match type [%s]", typ)
	}
}

func (m *compiledMatcherSet) Match(qname string) (*compiledMatch, bool) {
	cache := m.matchCache()
	key := compiledQueryKey(qname)
	if compiled, ok := cache.Get(key); ok {
		return compiled, compiled != nil
	}
	compiled, ok := m.matchUncached(qname)
	cache.Set(key, compiled)
	return compiled, ok
}

func (m *compiledMatcherSet) matchUncached(qname string) (*compiledMatch, bool) {
	var combined *compiledMatch
	fullMatch, fullOK := m.full.Match(qname)
	combined = appendCompiledMatch(combined, fullMatch, fullOK)
	domainMatch, domainOK := m.domain.Match(qname)
	combined = appendCompiledMatch(combined, domainMatch, domainOK)
	regexMatch, regexOK := m.regex.Match(qname)
	combined = appendCompiledMatch(combined, regexMatch, regexOK)
	keywordMatch, keywordOK := m.keyword.Match(qname)
	combined = appendCompiledMatch(combined, keywordMatch, keywordOK)
	if combined == nil {
		return nil, false
	}
	return combined, true
}

func appendCompiledMatch(dst *compiledMatch, compiled *compiledMatch, ok bool) *compiledMatch {
	if !ok || compiled == nil {
		return dst
	}
	if dst == nil {
		return compiled
	}
	if containsCompiledMatch(dst, compiled) {
		return dst
	}
	merged := &compiledMatch{
		staticResult: mergeMatchResult(dst.staticResult, compiled.staticResult),
	}
	merged.dynamicProviders = cloneDynamicProviders(dst.dynamicProviders)
	merged.dynamicProviders = appendDynamicProviders(merged.dynamicProviders, compiled.dynamicProviders)
	if merged.staticResult == nil && len(merged.dynamicProviders) == 0 {
		return nil
	}
	return merged
}
