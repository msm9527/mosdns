package domain_set

import (
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
)

// MatchWithSource validates generated evidence at query time while preserving
// independently configured expressions, files and nested sets.
func (d *DomainSet) MatchWithSource(name string, source server.RequestSource) (struct{}, bool) {
	d.mu.RLock()
	local, generated, others, exporter := d.mixM, d.generatedM, d.otherM, d.generatedExporter
	d.mu.RUnlock()

	if local != nil {
		if _, ok := local.Match(name); ok {
			return struct{}{}, true
		}
	}
	// Other sets retain their own validity independently of this generated
	// source, including static alternatives that need no validation.
	for _, matcher := range others {
		if _, ok := matchWithSource(matcher, name, source); ok {
			return struct{}{}, true
		}
	}
	if generated == nil {
		return struct{}{}, false
	}
	if _, ok := generated.Match(name); !ok {
		return struct{}{}, false
	}
	if runtime, ok := exporter.(sourceMatcher); ok {
		return runtime.MatchWithSource(name, source)
	}
	if source == server.RequestSourceUnspecified {
		source = server.RequestSourceUser
	}
	if validator, ok := exporter.(coremain.HotRuleRuntimeValidatorWithSource); ok {
		return struct{}{}, validator.AllowHotRuleWithSource(domain.NormalizeDomain(name), time.Now(), source)
	}
	if validator, ok := exporter.(coremain.HotRuleRuntimeValidator); ok {
		if source.IsBackground() {
			return struct{}{}, false
		}
		return struct{}{}, validator.AllowHotRule(domain.NormalizeDomain(name), time.Now())
	}
	return struct{}{}, true
}
