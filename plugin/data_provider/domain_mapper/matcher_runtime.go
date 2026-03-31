package domain_mapper

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
