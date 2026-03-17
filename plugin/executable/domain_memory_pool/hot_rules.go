package domain_memory_pool

import (
	"slices"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"go.uber.org/zap"
)

var _ coremain.HotRuleSnapshotProvider = (*domainMemoryPool)(nil)

func (d *domainMemoryPool) SnapshotHotRules() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.currentHotRulesLocked(), nil
}

func (d *domainMemoryPool) currentHotRulesLocked() []string {
	rules := make([]string, 0, d.domainCount)
	seen := make(map[string]struct{}, d.domainCount)
	for storageKey, entry := range d.stats {
		if entry == nil || !entry.Promoted {
			continue
		}
		domain, _ := splitStorageKey(storageKey)
		domain = strings.TrimSpace(domain)
		if domain == "" {
			continue
		}
		rule := "full:" + domain
		if _, exists := seen[rule]; exists {
			continue
		}
		seen[rule] = struct{}{}
		rules = append(rules, rule)
	}
	slices.Sort(rules)
	return rules
}

func (d *domainMemoryPool) pushHotRulesAdd(rules []string) {
	target := d.publishTarget()
	if err := coremain.DispatchHotRulesAdd(d.manager, target, rules); err != nil && d.logger != nil {
		d.logger.Warn("domain_memory_pool push hot add failed", zap.String("publish_to", target), zap.Error(err))
	}
}

func (d *domainMemoryPool) pushHotRulesReplace(rules []string) error {
	target := d.publishTarget()
	if target == "" {
		return nil
	}
	if err := coremain.DispatchHotRulesReplace(d.manager, target, rules); err != nil {
		if d.logger != nil {
			d.logger.Warn("domain_memory_pool push hot replace failed", zap.String("publish_to", target), zap.Error(err))
		}
		return err
	}
	return nil
}

func (d *domainMemoryPool) publishTarget() string {
	return strings.TrimSpace(d.policy.raw.PublishTo)
}
