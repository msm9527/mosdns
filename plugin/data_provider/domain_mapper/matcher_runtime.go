package domain_mapper

import (
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/server"
)

func (dm *DomainMapper) match(qname string, source server.RequestSource) (*MatchResult, bool) {
	if source == server.RequestSourceUnspecified {
		source = server.RequestSourceUser
	}
	mainCompiled, mainOK := dm.loadMatcher().Match(qname)
	lookup := dm.hotLookup.Load().(map[string]*compiledMatch)
	var hotCompiled *compiledMatch
	if len(lookup) != 0 {
		hotCompiled = lookup[fastEnsureFQDN(qname)]
	}
	// Merge evidence before validation so the same dynamic provider present in
	// both the persisted matcher and hot lookup is checked once per query.
	compiled := appendCompiledMatch(mainCompiled, hotCompiled, hotCompiled != nil)
	if compiled == nil {
		return nil, false
	}
	if len(compiled.dynamicProviders) == 0 {
		return compiled.staticResult, mainOK || hotCompiled != nil
	}
	result := dm.resolveCompiledMatch(compiled, qname, time.Now(), source)
	return result, result != nil
}

type compiledMatchLookup interface {
	Match(qname string) (*compiledMatch, bool)
}

func (dm *DomainMapper) loadMatcher() compiledMatchLookup {
	matcher, ok := dm.matcher.Load().(compiledMatchLookup)
	if !ok || matcher == nil {
		panic("domain_mapper matcher must implement Match(string)")
	}
	return matcher
}

func (dm *DomainMapper) loadProviderRegistry() *providerRegistry {
	registry, _ := dm.registry.Load().(*providerRegistry)
	return registry
}
