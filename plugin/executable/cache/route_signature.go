package cache

import (
	"sort"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
)

func normalizeDomainSetSignature(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	parts := strings.Split(raw, "|")
	seen := make(map[string]struct{}, len(parts))
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) == 0 {
		return ""
	}
	sort.Strings(tags)
	return strings.Join(tags, "|")
}

func currentRouteSignature(qCtx *query_context.Context) string {
	if qCtx == nil {
		return ""
	}
	value, ok := qCtx.GetValue(query_context.KeyDomainSet)
	if !ok {
		return ""
	}
	domainSet, ok := value.(string)
	if !ok {
		return ""
	}
	return normalizeDomainSetSignature(domainSet)
}

func shouldBypassForRouteChange(cachedDomainSet, currentSig string) bool {
	return normalizeDomainSetSignature(cachedDomainSet) != normalizeDomainSetSignature(currentSig)
}
