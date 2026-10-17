package domain_mapper

import (
	"reflect"
	"slices"

	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
)

// Main and hot matchers can use separate registries. Keep a comparable source
// identity so duplicate evidence from those registries shares one validation.
// An exporter with an uncomparable value simply retains separate validation.
func comparableProviderKey(exporter data_provider.RuleExporter) any {
	if exporter == nil || !reflect.ValueOf(exporter).Comparable() {
		return nil
	}
	return exporter
}

func sameMatchResult(a, b *MatchResult) bool {
	if a == b {
		return true
	}
	return a != nil && b != nil && a.JoinedTags == b.JoinedTags && slices.Equal(a.Marks, b.Marks)
}

func sameDynamicProvider(a, b *providerRuntime) bool {
	if a == b {
		return true
	}
	return a != nil && b != nil && a.sourceKey != nil && a.sourceKey == b.sourceKey &&
		(a.validator != nil) == (b.validator != nil) &&
		(a.validatorWithSource != nil) == (b.validatorWithSource != nil) && sameMatchResult(a.result, b.result)
}

func containsCompiledMatch(dst, src *compiledMatch) bool {
	if src.staticResult != nil && !sameMatchResult(dst.staticResult, src.staticResult) {
		return false
	}
	for _, provider := range src.dynamicProviders {
		found := false
		for _, existing := range dst.dynamicProviders {
			if sameDynamicProvider(existing, provider) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
