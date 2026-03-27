package coremain

import (
	"fmt"
	"sort"
	"strings"
)

const (
	DomainPoolKindMemory      = "memory"
	DomainPoolKindStats       = "stats"
	domainPoolEvictionLRU     = "lru"
	domainPoolEvictionLFU     = "lfu"
	memoryPoolsConfigFilename = "memory_pools.yaml"

	defaultTopDomainsMaxDomains   = 50000
	defaultRealIPPoolMaxDomains   = 30000
	defaultFakeIPPoolMaxDomains   = 30000
	defaultNoV6PoolMaxDomains     = 40000
	defaultNoV4PoolMaxDomains     = 30000
	defaultNodeNoV6PoolMaxDomains = 12000
	defaultNodeNoV4PoolMaxDomains = 12000
	defaultGenericPoolMaxDomains  = 20000
)

type DomainPoolPolicy struct {
	Kind                   string `yaml:"kind" json:"kind"`
	PublishTo              string `yaml:"publish_to,omitempty" json:"publish_to,omitempty"`
	RequeryTag             string `yaml:"requery_tag,omitempty" json:"requery_tag,omitempty"`
	PromoteAfter           int    `yaml:"promote_after" json:"promote_after"`
	TrackQType             bool   `yaml:"track_qtype" json:"track_qtype"`
	TrackFlags             bool   `yaml:"track_flags" json:"track_flags"`
	MaxDomains             int    `yaml:"max_domains" json:"max_domains"`
	MaxVariantsPerDomain   int    `yaml:"max_variants_per_domain" json:"max_variants_per_domain"`
	EvictionPolicy         string `yaml:"eviction_policy" json:"eviction_policy"`
	StaleAfterMinutes      int    `yaml:"stale_after_minutes" json:"stale_after_minutes"`
	RefreshCooldownMinutes int    `yaml:"refresh_cooldown_minutes" json:"refresh_cooldown_minutes"`
	FlushIntervalMS        int    `yaml:"flush_interval_ms" json:"flush_interval_ms"`
	PublishDebounceMS      int    `yaml:"publish_debounce_ms" json:"publish_debounce_ms"`
	PruneIntervalSec       int    `yaml:"prune_interval_sec" json:"prune_interval_sec"`
	MemoryID               string `yaml:"-" json:"memory_id,omitempty"`
}

type domainPoolPolicyFile struct {
	Kind                   string `yaml:"kind,omitempty"`
	PublishTo              string `yaml:"publish_to,omitempty"`
	RequeryTag             string `yaml:"requery_tag,omitempty"`
	PromoteAfter           *int   `yaml:"promote_after,omitempty"`
	TrackQType             *bool  `yaml:"track_qtype,omitempty"`
	TrackFlags             *bool  `yaml:"track_flags,omitempty"`
	MaxDomains             int    `yaml:"max_domains,omitempty"`
	MaxVariantsPerDomain   int    `yaml:"max_variants_per_domain,omitempty"`
	EvictionPolicy         string `yaml:"eviction_policy,omitempty"`
	StaleAfterMinutes      *int   `yaml:"stale_after_minutes,omitempty"`
	RefreshCooldownMinutes *int   `yaml:"refresh_cooldown_minutes,omitempty"`
	FlushIntervalMS        int    `yaml:"flush_interval_ms,omitempty"`
	PublishDebounceMS      *int   `yaml:"publish_debounce_ms,omitempty"`
	PruneIntervalSec       int    `yaml:"prune_interval_sec,omitempty"`
}

func defaultDomainPoolPolicyMap() map[string]DomainPoolPolicy {
	values := make(map[string]DomainPoolPolicy, len(defaultDomainPoolTags()))
	for _, tag := range defaultDomainPoolTags() {
		values[tag] = defaultDomainPoolPolicy(tag)
	}
	return values
}

func defaultDomainPoolTags() []string {
	return []string{
		"top_domains",
		"my_realiplist",
		"my_fakeiplist",
		"my_nov6list",
		"my_nov4list",
		"my_nodenov6list",
		"my_nodenov4list",
	}
}

func normalizeDomainPoolPolicyMap(raw map[string]domainPoolPolicyFile) (map[string]DomainPoolPolicy, error) {
	normalized := defaultDomainPoolPolicyMap()
	if raw == nil {
		return normalized, nil
	}
	for tag, item := range raw {
		cleanTag := strings.TrimSpace(tag)
		if cleanTag == "" {
			return nil, fmt.Errorf("memory pool tag is empty")
		}
		policy, err := normalizeDomainPoolPolicy(cleanTag, item)
		if err != nil {
			return nil, err
		}
		normalized[cleanTag] = policy
	}
	return normalized, nil
}

func cloneDomainPoolPolicyMap(src map[string]DomainPoolPolicy) map[string]DomainPoolPolicy {
	if src == nil {
		return map[string]DomainPoolPolicy{}
	}
	dst := make(map[string]DomainPoolPolicy, len(src))
	for tag, policy := range src {
		dst[tag] = policy
	}
	return dst
}

func DefaultDomainPoolPolicy(tag string) DomainPoolPolicy {
	return defaultDomainPoolPolicy(tag)
}

func ResolveDomainPoolPolicy(tag string, values map[string]DomainPoolPolicy) (DomainPoolPolicy, error) {
	policy, ok := values[strings.TrimSpace(tag)]
	if !ok {
		policy = defaultDomainPoolPolicy(tag)
	}
	if err := validateDomainPoolPolicy(tag, &policy); err != nil {
		return DomainPoolPolicy{}, err
	}
	return policy, nil
}

func orderedDomainPoolPolicyKeys(values map[string]DomainPoolPolicy) []string {
	seen := make(map[string]struct{}, len(values))
	keys := make([]string, 0, len(values))
	for _, tag := range defaultDomainPoolTags() {
		if _, ok := values[tag]; ok {
			keys = append(keys, tag)
			seen[tag] = struct{}{}
		}
	}
	extras := make([]string, 0, len(values))
	for tag := range values {
		if _, ok := seen[tag]; ok {
			continue
		}
		extras = append(extras, tag)
	}
	sort.Strings(extras)
	return append(keys, extras...)
}

func defaultDomainPoolPolicy(tag string) DomainPoolPolicy {
	hint := inferDomainPoolHint(tag)
	switch hint {
	case "top":
		return DomainPoolPolicy{
			Kind:                 DomainPoolKindStats,
			TrackFlags:           true,
			MaxDomains:           defaultTopDomainsMaxDomains,
			MaxVariantsPerDomain: 8,
			EvictionPolicy:       domainPoolEvictionLFU,
			FlushIntervalMS:      30000,
			PublishDebounceMS:    0,
			PruneIntervalSec:     600,
			MemoryID:             "top",
		}
	case "realip":
		return defaultMemoryDomainPoolPolicy("realip", "my_realiprule", defaultRealIPPoolMaxDomains, 360)
	case "fakeip":
		return defaultMemoryDomainPoolPolicy("fakeip", "my_fakeiprule", defaultFakeIPPoolMaxDomains, 240)
	case "nov6":
		return defaultMemoryDomainPoolPolicy("nov6", "my_nov6rule", defaultNoV6PoolMaxDomains, 180)
	case "nov4":
		return defaultMemoryDomainPoolPolicy("nov4", "my_nov4rule", defaultNoV4PoolMaxDomains, 180)
	case "nodenov6":
		return defaultMemoryDomainPoolPolicy("nodenov6", "my_nodenov6rule", defaultNodeNoV6PoolMaxDomains, 180)
	case "nodenov4":
		return defaultMemoryDomainPoolPolicy("nodenov4", "my_nodenov4rule", defaultNodeNoV4PoolMaxDomains, 180)
	default:
		return defaultMemoryDomainPoolPolicy("generic", "", defaultGenericPoolMaxDomains, 180)
	}
}

func defaultMemoryDomainPoolPolicy(memoryID, publishTo string, maxDomains, staleMinutes int) DomainPoolPolicy {
	return DomainPoolPolicy{
		Kind:                   DomainPoolKindMemory,
		PublishTo:              publishTo,
		RequeryTag:             "requery",
		PromoteAfter:           2,
		TrackQType:             true,
		MaxDomains:             maxDomains,
		MaxVariantsPerDomain:   4,
		EvictionPolicy:         domainPoolEvictionLRU,
		StaleAfterMinutes:      staleMinutes,
		RefreshCooldownMinutes: 120,
		FlushIntervalMS:        30000,
		PublishDebounceMS:      5000,
		PruneIntervalSec:       600,
		MemoryID:               memoryID,
	}
}

func normalizeDomainPoolPolicy(tag string, raw domainPoolPolicyFile) (DomainPoolPolicy, error) {
	policy := defaultDomainPoolPolicy(tag)

	if value := strings.ToLower(strings.TrimSpace(raw.Kind)); value != "" {
		policy.Kind = value
	}
	policy.PublishTo = strings.TrimSpace(firstNonEmpty(raw.PublishTo, policy.PublishTo))
	policy.RequeryTag = strings.TrimSpace(firstNonEmpty(raw.RequeryTag, policy.RequeryTag))
	if raw.PromoteAfter != nil {
		policy.PromoteAfter = *raw.PromoteAfter
	}
	if raw.TrackQType != nil {
		policy.TrackQType = *raw.TrackQType
	}
	if raw.TrackFlags != nil {
		policy.TrackFlags = *raw.TrackFlags
	}
	if raw.MaxDomains > 0 {
		policy.MaxDomains = raw.MaxDomains
	}
	if raw.MaxVariantsPerDomain > 0 {
		policy.MaxVariantsPerDomain = raw.MaxVariantsPerDomain
	}
	if value := strings.ToLower(strings.TrimSpace(raw.EvictionPolicy)); value != "" {
		policy.EvictionPolicy = value
	}
	if raw.StaleAfterMinutes != nil {
		policy.StaleAfterMinutes = *raw.StaleAfterMinutes
	}
	if raw.RefreshCooldownMinutes != nil {
		policy.RefreshCooldownMinutes = *raw.RefreshCooldownMinutes
	}
	if raw.FlushIntervalMS > 0 {
		policy.FlushIntervalMS = raw.FlushIntervalMS
	}
	if raw.PublishDebounceMS != nil {
		policy.PublishDebounceMS = *raw.PublishDebounceMS
	}
	if raw.PruneIntervalSec > 0 {
		policy.PruneIntervalSec = raw.PruneIntervalSec
	}

	if err := validateDomainPoolPolicy(tag, &policy); err != nil {
		return DomainPoolPolicy{}, err
	}
	return policy, nil
}

func validateDomainPoolPolicy(tag string, policy *DomainPoolPolicy) error {
	policy.MemoryID = inferDomainPoolMemoryID(tag)
	switch policy.Kind {
	case DomainPoolKindMemory, DomainPoolKindStats:
	default:
		return fmt.Errorf("memory pool %s has unsupported kind %q", tag, policy.Kind)
	}
	switch policy.EvictionPolicy {
	case domainPoolEvictionLRU, domainPoolEvictionLFU:
	default:
		return fmt.Errorf("memory pool %s has unsupported eviction_policy %q", tag, policy.EvictionPolicy)
	}
	if policy.MaxDomains <= 0 {
		return fmt.Errorf("memory pool %s requires max_domains > 0", tag)
	}
	if policy.MaxVariantsPerDomain <= 0 {
		return fmt.Errorf("memory pool %s requires max_variants_per_domain > 0", tag)
	}
	if policy.FlushIntervalMS <= 0 {
		return fmt.Errorf("memory pool %s requires flush_interval_ms > 0", tag)
	}
	if policy.PublishDebounceMS < 0 {
		return fmt.Errorf("memory pool %s requires publish_debounce_ms >= 0", tag)
	}
	if policy.PruneIntervalSec <= 0 {
		return fmt.Errorf("memory pool %s requires prune_interval_sec > 0", tag)
	}
	if policy.Kind == DomainPoolKindStats {
		policy.PublishTo = ""
		policy.RequeryTag = ""
		policy.PromoteAfter = 0
		policy.TrackQType = false
		policy.RefreshCooldownMinutes = 0
	}
	return nil
}

func inferDomainPoolMemoryID(tag string) string {
	switch inferDomainPoolHint(tag) {
	case "top":
		return "top"
	case "realip":
		return "realip"
	case "fakeip":
		return "fakeip"
	case "nodenov4":
		return "nodenov4"
	case "nodenov6":
		return "nodenov6"
	case "nov4":
		return "nov4"
	case "nov6":
		return "nov6"
	default:
		return "generic"
	}
}

func inferDomainPoolHint(tag string) string {
	lower := strings.ToLower(strings.TrimSpace(tag))
	switch {
	case strings.Contains(lower, "top_domains"), strings.Contains(lower, "top"):
		return "top"
	case strings.Contains(lower, "realip"):
		return "realip"
	case strings.Contains(lower, "fakeip"):
		return "fakeip"
	case strings.Contains(lower, "nodenov4"):
		return "nodenov4"
	case strings.Contains(lower, "nodenov6"):
		return "nodenov6"
	case strings.Contains(lower, "nov4"):
		return "nov4"
	case strings.Contains(lower, "nov6"):
		return "nov6"
	default:
		return "generic"
	}
}

func firstNonEmpty(first, fallback string) string {
	if strings.TrimSpace(first) != "" {
		return first
	}
	return fallback
}
