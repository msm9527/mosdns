package domain_mapper

import (
	"encoding/binary"
	"math/bits"
	"slices"
	"strings"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
)

type sharedRuleExporter interface {
	GetRulesShared() ([]string, error)
}

type runtimeValidationAwareExporter interface {
	HasRuntimeHotRuleValidation() bool
}

type providerSet struct {
	inline uint64
	extra  []uint64
}

type providerSetCacheKey struct {
	inline   uint64
	overflow string
}

type providerResultBuilder struct {
	marks []uint8
	tags  []string

	seenMarks map[uint8]struct{}
	seenTags  map[string]struct{}
}

func buildProviderResults(ruleConfigs []RuleConfig) map[string]*MatchResult {
	builders := make(map[string]*providerResultBuilder)
	for _, rule := range ruleConfigs {
		providerTag := strings.TrimSpace(rule.Tag)
		if providerTag == "" {
			continue
		}
		builder := builders[providerTag]
		if builder == nil {
			builder = &providerResultBuilder{
				seenMarks: make(map[uint8]struct{}, 4),
				seenTags:  make(map[string]struct{}, 4),
			}
			builders[providerTag] = builder
		}
		if rule.Mark > 0 {
			if _, ok := builder.seenMarks[rule.Mark]; !ok {
				builder.seenMarks[rule.Mark] = struct{}{}
				builder.marks = append(builder.marks, rule.Mark)
			}
		}
		tag := strings.TrimSpace(rule.OutputTag)
		if tag == "" {
			tag = providerTag
		}
		if tag == "" {
			continue
		}
		if _, ok := builder.seenTags[tag]; ok {
			continue
		}
		builder.seenTags[tag] = struct{}{}
		builder.tags = append(builder.tags, tag)
	}

	results := make(map[string]*MatchResult, len(builders))
	for providerTag, builder := range builders {
		if len(builder.marks) == 0 && len(builder.tags) == 0 {
			continue
		}
		slices.Sort(builder.marks)
		results[providerTag] = &MatchResult{
			Marks:      append([]uint8(nil), builder.marks...),
			JoinedTags: strings.Join(builder.tags, "|"),
		}
	}
	return results
}

func buildProviderValidators(providers map[string]data_provider.RuleExporter) map[string]coremain.HotRuleRuntimeValidator {
	validators := make(map[string]coremain.HotRuleRuntimeValidator, len(providers))
	for tag, provider := range providers {
		validator, ok := provider.(coremain.HotRuleRuntimeValidator)
		if !ok || validator == nil {
			continue
		}
		if aware, ok := provider.(runtimeValidationAwareExporter); ok && !aware.HasRuntimeHotRuleValidation() {
			continue
		}
		validators[tag] = validator
	}
	return validators
}

func buildProviderRegistry(ruleConfigs []RuleConfig, providers map[string]data_provider.RuleExporter) *providerRegistry {
	results := buildProviderResults(ruleConfigs)
	validators := buildProviderValidators(providers)
	registry := &providerRegistry{
		byTag:     make(map[string]uint16, len(results)),
		providers: make([]providerRuntime, 0, len(results)),
	}
	for _, rule := range ruleConfigs {
		tag := strings.TrimSpace(rule.Tag)
		if tag == "" {
			continue
		}
		if _, exists := registry.byTag[tag]; exists {
			continue
		}
		result := results[tag]
		if result == nil {
			continue
		}
		registry.byTag[tag] = uint16(len(registry.providers))
		registry.providers = append(registry.providers, providerRuntime{
			result:    result,
			validator: validators[tag],
		})
	}
	return registry
}

func (r *providerRegistry) ref(tag string) (uint16, bool) {
	if r == nil {
		return 0, false
	}
	ref, ok := r.byTag[strings.TrimSpace(tag)]
	return ref, ok
}

func (r *providerRegistry) provider(ref uint16) *providerRuntime {
	if r == nil || int(ref) >= len(r.providers) {
		return nil
	}
	return &r.providers[ref]
}

func loadExporterRules(exporter data_provider.RuleExporter) ([]string, error) {
	if shared, ok := exporter.(sharedRuleExporter); ok {
		return shared.GetRulesShared()
	}
	return exporter.GetRules()
}

func (s *providerSet) add(ref uint16) bool {
	if ref < 64 {
		bit := uint64(1) << ref
		if s.inline&bit != 0 {
			return false
		}
		s.inline |= bit
		return true
	}
	ref -= 64
	idx := int(ref / 64)
	bit := uint64(1) << (ref % 64)
	if idx >= len(s.extra) {
		extra := make([]uint64, idx+1)
		copy(extra, s.extra)
		s.extra = extra
	}
	if s.extra[idx]&bit != 0 {
		return false
	}
	s.extra[idx] |= bit
	return true
}

func (s *providerSet) merge(other providerSet) {
	s.inline |= other.inline
	if len(other.extra) == 0 {
		return
	}
	if len(s.extra) < len(other.extra) {
		extra := make([]uint64, len(other.extra))
		copy(extra, s.extra)
		s.extra = extra
	}
	for i, word := range other.extra {
		s.extra[i] |= word
	}
}

func (s providerSet) clone() providerSet {
	if len(s.extra) == 0 {
		return s
	}
	return providerSet{
		inline: s.inline,
		extra:  append([]uint64(nil), s.extra...),
	}
}

func (s providerSet) empty() bool {
	if s.inline != 0 {
		return false
	}
	for _, word := range s.extra {
		if word != 0 {
			return false
		}
	}
	return true
}

func (s providerSet) forEach(fn func(ref uint16)) {
	word := s.inline
	for word != 0 {
		offset := bits.TrailingZeros64(word)
		fn(uint16(offset))
		word &= word - 1
	}
	for i, extraWord := range s.extra {
		word = extraWord
		for word != 0 {
			offset := bits.TrailingZeros64(word)
			fn(uint16(64 + i*64 + offset))
			word &= word - 1
		}
	}
}

func trimTrailingZeroWords(words []uint64) []uint64 {
	n := len(words)
	for n > 0 && words[n-1] == 0 {
		n--
	}
	return words[:n]
}

func (s providerSet) cacheKey() providerSetCacheKey {
	extra := trimTrailingZeroWords(s.extra)
	if len(extra) == 0 {
		return providerSetCacheKey{inline: s.inline}
	}
	buf := make([]byte, len(extra)*8)
	for i, word := range extra {
		binary.LittleEndian.PutUint64(buf[i*8:], word)
	}
	return providerSetCacheKey{
		inline:   s.inline,
		overflow: string(buf),
	}
}

func appendUniqueDynamicProvider(dst []*providerRuntime, provider *providerRuntime) []*providerRuntime {
	if provider == nil {
		return dst
	}
	for _, existing := range dst {
		if existing == provider {
			return dst
		}
	}
	return append(dst, provider)
}

func appendDynamicProviders(dst []*providerRuntime, src []*providerRuntime) []*providerRuntime {
	for _, provider := range src {
		dst = appendUniqueDynamicProvider(dst, provider)
	}
	return dst
}

func cloneDynamicProviders(src []*providerRuntime) []*providerRuntime {
	if len(src) == 0 {
		return nil
	}
	return append([]*providerRuntime(nil), src...)
}

func buildCompiledMatch(registry *providerRegistry, sources providerSet) *compiledMatch {
	if registry == nil || sources.empty() {
		return nil
	}
	var staticResult *MatchResult
	var dynamicProviders []*providerRuntime
	sources.forEach(func(ref uint16) {
		provider := registry.provider(ref)
		if provider == nil || provider.result == nil {
			return
		}
		if provider.validator != nil {
			dynamicProviders = appendUniqueDynamicProvider(dynamicProviders, provider)
			return
		}
		staticResult = mergeMatchResult(staticResult, provider.result)
	})
	if staticResult == nil && len(dynamicProviders) == 0 {
		return nil
	}
	return &compiledMatch{
		staticResult:     staticResult,
		dynamicProviders: dynamicProviders,
	}
}

func getOrBuildCompiledMatch(
	cache map[providerSetCacheKey]*compiledMatch,
	registry *providerRegistry,
	sources providerSet,
) *compiledMatch {
	if sources.empty() {
		return nil
	}
	key := sources.cacheKey()
	if compiled := cache[key]; compiled != nil {
		return compiled
	}
	compiled := buildCompiledMatch(registry, sources)
	if compiled != nil {
		cache[key] = compiled
	}
	return compiled
}

func allowProvider(provider *providerRuntime, domain string, now time.Time) bool {
	if provider == nil || provider.validator == nil {
		return true
	}
	if domain == "" {
		return false
	}
	return provider.validator.AllowHotRule(domain, now)
}

func normalizedValidationDomain(qname string) string {
	return strings.TrimSuffix(ensureFQDN(qname), ".")
}

func (dm *DomainMapper) resolveCompiledMatch(compiled *compiledMatch, qname string, now time.Time) *MatchResult {
	if compiled == nil {
		return nil
	}
	if len(compiled.dynamicProviders) == 0 {
		return compiled.staticResult
	}
	domain := normalizedValidationDomain(qname)
	merged := cloneMatchResult(compiled.staticResult)
	for _, provider := range compiled.dynamicProviders {
		if !allowProvider(provider, domain, now) {
			continue
		}
		merged = mergeMatchResult(merged, provider.result)
	}
	return merged
}
