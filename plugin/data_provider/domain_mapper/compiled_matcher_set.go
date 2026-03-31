package domain_mapper

import (
	"fmt"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
)

type compiledMatcherSet struct {
	full    *domain.FullMatcher[*compiledMatch]
	domain  *domain.SubDomainMatcher[*compiledMatch]
	regex   *domain.RegexMatcher[*compiledMatch]
	keyword *domain.KeywordMatcher[*compiledMatch]
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
	sources := make([]matchSource, 0, 4)
	matched := false
	fullMatch, fullOK := m.full.Match(qname)
	sources, matched = appendCompiledMatchSources(sources, fullMatch, fullOK, matched)
	domainMatch, domainOK := m.domain.Match(qname)
	sources, matched = appendCompiledMatchSources(sources, domainMatch, domainOK, matched)
	regexMatch, regexOK := m.regex.Match(qname)
	sources, matched = appendCompiledMatchSources(sources, regexMatch, regexOK, matched)
	keywordMatch, keywordOK := m.keyword.Match(qname)
	sources, matched = appendCompiledMatchSources(sources, keywordMatch, keywordOK, matched)
	if !matched || len(sources) == 0 {
		return nil, false
	}
	return &compiledMatch{sources: sources}, true
}

func appendCompiledMatchSources(
	dst []matchSource,
	compiled *compiledMatch,
	ok bool,
	matched bool,
) ([]matchSource, bool) {
	if !ok || compiled == nil {
		return dst, matched
	}
	return appendMatchSources(dst, compiled.sources), true
}
