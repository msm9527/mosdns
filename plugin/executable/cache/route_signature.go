package cache

import (
	"fmt"
	"sort"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
)

const routeMetadataSeparator = "\x1f"

var cacheRouteSwitchDependencies = []string{
	"client_proxy_mode",
	"core_mode",
	"domestic_ecs",
	"foreign_ecs",
	"fakeip_cache",
	"block_ipv6",
	"cn_answer_mode",
}

type cacheSwitchValueProvider interface {
	GetValue() string
}

type cacheSwitchRevisionProvider interface {
	ValueCode() uint64
}

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

func normalizeDomainSetTokens(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	tags := make([]string, 0, len(values))
	for _, value := range values {
		for _, tag := range splitDomainSetTokenList(value) {
			tags = append(tags, tag)
		}
	}
	normalized := normalizeDomainSetSignature(strings.Join(tags, "|"))
	if normalized == "" {
		return nil
	}
	return strings.Split(normalized, "|")
}

func splitDomainSetTokenList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}

	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '|' || r == ',' || r == '，'
	})
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
		tag := strings.TrimSpace(part)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func domainSetContainsAnyToken(domainSet string, tokens []string) bool {
	return domainSetTokensContainAny(storedDomainSet(domainSet), tokens)
}

func domainSetTokensContainAny(domainSet string, tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	domainSet = strings.TrimSpace(domainSet)
	if domainSet == "" {
		return false
	}

	for {
		part, rest, ok := strings.Cut(domainSet, "|")
		part = strings.TrimSpace(part)
		for _, token := range tokens {
			if part == strings.TrimSpace(token) {
				return true
			}
		}
		if !ok {
			return false
		}
		domainSet = rest
	}
}

func mergeDependencySets(values ...string) string {
	merged := make([]string, 0, len(values)*2)
	for _, value := range values {
		value = normalizeDomainSetSignature(value)
		if value == "" {
			continue
		}
		merged = append(merged, strings.Split(value, "|")...)
	}
	return normalizeDomainSetSignature(strings.Join(merged, "|"))
}

func resolveRouteSignature(raw string, pluginLookup func(string) any) string {
	raw = normalizeDomainSetSignature(raw)
	if raw == "" && pluginLookup == nil {
		return ""
	}

	var resolved []string
	if raw != "" {
		parts := strings.Split(raw, "|")
		resolved = make([]string, 0, len(parts)+len(cacheRouteSwitchDependencies))
		for _, part := range parts {
			tag := strings.TrimSpace(part)
			if tag == "" {
				continue
			}
			if rev := resolveTagRevision(tag, pluginLookup); rev != "" {
				resolved = append(resolved, tag+"@"+rev)
				continue
			}
			resolved = append(resolved, tag)
		}
	}
	if switchSig := resolveCacheSwitchSignature(pluginLookup); switchSig != "" {
		resolved = append(resolved, switchSig)
	}
	return normalizeDomainSetSignature(strings.Join(resolved, "|"))
}

func resolveTagRevision(tag string, pluginLookup func(string) any) string {
	if pluginLookup == nil {
		return ""
	}
	provider, ok := pluginLookup(strings.TrimSpace(tag)).(coremain.CacheRevisionProvider)
	if !ok || provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.CacheRevision())
}

func resolveCacheSwitchSignature(pluginLookup func(string) any) string {
	if pluginLookup == nil {
		return ""
	}
	parts := make([]string, 0, len(cacheRouteSwitchDependencies))
	for _, name := range cacheRouteSwitchDependencies {
		if sig := resolveSwitchRevision(name, pluginLookup(name)); sig != "" {
			parts = append(parts, sig)
		}
	}
	return strings.Join(parts, "|")
}

func resolveSwitchRevision(name string, plugin any) string {
	name = strings.TrimSpace(name)
	if name == "" || plugin == nil {
		return ""
	}
	if provider, ok := plugin.(cacheSwitchRevisionProvider); ok && provider != nil {
		return fmt.Sprintf("switch:%s@%d", name, provider.ValueCode())
	}
	if provider, ok := plugin.(cacheSwitchValueProvider); ok && provider != nil {
		value := strings.TrimSpace(provider.GetValue())
		if value != "" {
			return "switch:" + name + "@" + value
		}
	}
	return ""
}

func currentRouteDomainSet(qCtx *query_context.Context) string {
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

func currentCacheDependencies(qCtx *query_context.Context) string {
	if qCtx == nil {
		return ""
	}
	value, ok := qCtx.GetValue(query_context.KeyCacheDependencySet)
	if !ok {
		return ""
	}
	dependencies, ok := value.(string)
	if !ok {
		return ""
	}
	return normalizeDomainSetSignature(dependencies)
}

func currentCacheRouteDependencySet(qCtx *query_context.Context) string {
	if dependencies := currentCacheDependencies(qCtx); dependencies != "" {
		return dependencies
	}
	return currentRouteDomainSet(qCtx)
}

type cacheRouteSnapshot struct {
	domainSet      string
	dependencySet  string
	routeSignature string
}

func newCacheRouteSnapshot(qCtx *query_context.Context, pluginLookup func(string) any) cacheRouteSnapshot {
	dependencySet := currentCacheRouteDependencySet(qCtx)
	return cacheRouteSnapshot{
		domainSet:      currentRouteDomainSet(qCtx),
		dependencySet:  dependencySet,
		routeSignature: resolveRouteSignature(dependencySet, pluginLookup),
	}
}

func encodeStoredRouteMetadata(domainSet, dependencySet, routeSignature string) string {
	domainSet = normalizeDomainSetSignature(domainSet)
	dependencySet = normalizeDomainSetSignature(dependencySet)
	routeSignature = normalizeDomainSetSignature(routeSignature)
	if domainSet == "" && dependencySet == "" {
		return ""
	}
	if dependencySet == "" {
		dependencySet = domainSet
	}
	if routeSignature == "" {
		return domainSet + routeMetadataSeparator + dependencySet
	}
	return domainSet + routeMetadataSeparator + dependencySet + routeMetadataSeparator + routeSignature
}

func decodeStoredRouteMetadata(stored string) (domainSet string, dependencySet string, routeSignature string) {
	if stored == "" {
		return "", "", ""
	}
	parts := strings.SplitN(stored, routeMetadataSeparator, 3)
	switch len(parts) {
	case 1:
		domainSet = normalizeDomainSetSignature(parts[0])
		return domainSet, domainSet, ""
	case 2:
		domainSet = normalizeDomainSetSignature(parts[0])
		dependencySet = normalizeDomainSetSignature(parts[1])
		if dependencySet == "" {
			dependencySet = domainSet
		}
		return domainSet, dependencySet, ""
	default:
		domainSet = normalizeDomainSetSignature(parts[0])
		dependencySet = normalizeDomainSetSignature(parts[1])
		if dependencySet == "" {
			dependencySet = domainSet
		}
		return domainSet, dependencySet, normalizeDomainSetSignature(parts[2])
	}
}

func storedDomainSet(stored string) string {
	domainSet, _, _ := decodeStoredRouteMetadata(stored)
	return domainSet
}

func storedDependencySet(stored string) string {
	_, dependencySet, _ := decodeStoredRouteMetadata(stored)
	return dependencySet
}

func storedRouteSignature(stored string) string {
	_, _, routeSignature := decodeStoredRouteMetadata(stored)
	return routeSignature
}

func shouldBypassForRouteChange(cachedStored, currentDomainSet, currentDependencySet string, pluginLookup func(string) any) bool {
	cachedDomainSet, dependencySet, cachedSig := decodeStoredRouteMetadata(cachedStored)
	currentDomainSet = normalizeDomainSetSignature(currentDomainSet)
	currentDependencySet = normalizeDomainSetSignature(currentDependencySet)
	if cachedDomainSet == "" && dependencySet == "" && cachedSig == "" && currentDomainSet == "" && currentDependencySet == "" {
		return false
	}

	cachedScope := normalizeDomainSetSignature(dependencySet)
	if cachedScope == "" {
		cachedScope = normalizeDomainSetSignature(cachedDomainSet)
	}
	currentScope := currentDependencySet
	if currentScope == "" {
		currentScope = currentDomainSet
	}

	currentSig := resolveRouteSignature(currentScope, pluginLookup)
	if cachedSig == "" {
		return normalizeDomainSetSignature(cachedScope) != normalizeDomainSetSignature(currentScope)
	}
	return normalizeDomainSetSignature(cachedSig) != normalizeDomainSetSignature(currentSig)
}
