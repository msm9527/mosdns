package requery

import (
	"context"
	"strings"
)

func (p *Requery) prewarmChangedDomainsAfterPublish(ctx context.Context, domains []domainCandidate) error {
	if len(domains) == 0 {
		return nil
	}
	profile := p.profileForMode("quick_prewarm", len(domains))
	profile.ResolverAddr = p.config.ExecutionSettings.ResolverAddress
	profile.PostWarm = false
	return p.resendDNSQueries(ctx, domains, false, profile)
}

func domainCandidatesFromNames(domains []string) []domainCandidate {
	if len(domains) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(domains))
	out := make([]domainCandidate, 0, len(domains))
	for _, domain := range domains {
		name := strings.TrimSpace(strings.TrimSuffix(domain, "."))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, domainCandidate{Name: name, QTypeMask: qtypeMaskA | qtypeMaskAAAA})
	}
	return out
}
