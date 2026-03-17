package domain_memory_pool

import (
	"slices"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

var _ coremain.HotRuleSnapshotProvider = (*domainMemoryPool)(nil)

func (d *domainMemoryPool) SnapshotHotRules() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotActiveHotRulesLocked(), nil
}

func (d *domainMemoryPool) snapshotActiveHotRulesLocked() []string {
	rules := make([]string, 0, len(d.hotActiveRules))
	for rule := range d.hotActiveRules {
		rules = append(rules, rule)
	}
	slices.Sort(rules)
	return rules
}

func (d *domainMemoryPool) replaceActiveHotRulesLocked(rules []string) {
	d.hotActiveRules = make(map[string]struct{}, len(rules))
	for _, rule := range normalizePoolHotRules(rules) {
		d.hotActiveRules[rule] = struct{}{}
	}
}

func (d *domainMemoryPool) addActiveHotRules(rules []string) int {
	normalized := normalizePoolHotRules(rules)
	if len(normalized) == 0 {
		return d.activeHotRuleCount()
	}
	d.mu.Lock()
	for _, rule := range normalized {
		d.hotActiveRules[rule] = struct{}{}
	}
	count := len(d.hotActiveRules)
	d.mu.Unlock()
	return count
}

func (d *domainMemoryPool) replaceActiveHotRules(rules []string) int {
	d.mu.Lock()
	d.replaceActiveHotRulesLocked(rules)
	count := len(d.hotActiveRules)
	d.mu.Unlock()
	return count
}

func (d *domainMemoryPool) activeHotRuleCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.hotActiveRules)
}

func (d *domainMemoryPool) pushHotRulesAdd(rules []string) {
	if d.hotCmdChan == nil {
		return
	}
	d.hotCmdChan <- hotPublishRequest{mode: hotPublishAdd, rules: normalizePoolHotRules(rules)}
}

func (d *domainMemoryPool) pushHotRulesReplace(rules []string) error {
	normalized := normalizePoolHotRules(rules)
	if d.hotCmdChan == nil {
		d.replaceActiveHotRules(normalized)
		return nil
	}
	resp := make(chan error, 1)
	d.hotCmdChan <- hotPublishRequest{mode: hotPublishReplace, rules: normalized, resp: resp}
	return <-resp
}

func (d *domainMemoryPool) publishTarget() string {
	return strings.TrimSpace(d.policy.raw.PublishTo)
}

func normalizePoolHotRules(rules []string) []string {
	normalized := make([]string, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		key := strings.TrimSpace(rule)
		if !strings.HasPrefix(key, "full:") {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(key, "full:"))
		if key == "" {
			continue
		}
		normalizedRule := "full:" + key
		if _, exists := seen[normalizedRule]; exists {
			continue
		}
		seen[normalizedRule] = struct{}{}
		normalized = append(normalized, normalizedRule)
	}
	slices.Sort(normalized)
	return normalized
}
