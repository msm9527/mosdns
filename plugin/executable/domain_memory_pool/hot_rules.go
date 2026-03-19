package domain_memory_pool

import (
	"slices"
	"strings"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

var _ coremain.HotRuleSnapshotProvider = (*domainMemoryPool)(nil)
var _ coremain.HotRuleRuntimeValidator = (*domainMemoryPool)(nil)

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

func (d *domainMemoryPool) AllowHotRule(domain string, now time.Time) bool {
	domain = strings.TrimSpace(strings.TrimSuffix(domain, "."))
	if domain == "" {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}

	var (
		allow       bool
		shouldDirty bool
		notify      *coremain.DomainRefreshJob
	)

	d.mu.Lock()
	for key, entry := range d.stats {
		bare, _ := splitStorageKey(key)
		if bare != domain || !entry.Promoted {
			continue
		}
		if d.allowHotRuleEntryLocked(entry, now) {
			allow = true
			break
		}
		shouldDirty = true
	}
	if !allow && shouldDirty {
		notify = d.markHotRuleRefreshLocked(domain, now)
	}
	d.mu.Unlock()

	if !allow && notify != nil {
		d.dirtyPending.Store(true)
		go d.notifyDirty(*notify)
	}
	return allow
}

func (d *domainMemoryPool) allowHotRuleEntryLocked(entry *statEntry, now time.Time) bool {
	if entry == nil || !entry.Promoted {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(entry.RefreshState), "dirty") {
		return false
	}
	if d.policy.staleAfterMinutes <= 0 {
		return true
	}
	lastStamp := firstNonEmpty(entry.LastVerifiedAt, firstNonEmpty(entry.LastDirtyAt, entry.LastSeenAt))
	if strings.TrimSpace(lastStamp) == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, lastStamp)
	if err != nil {
		return false
	}
	return now.Sub(ts) < time.Duration(d.policy.staleAfterMinutes)*time.Minute
}

func (d *domainMemoryPool) markHotRuleRefreshLocked(domain string, now time.Time) *coremain.DomainRefreshJob {
	if d.policy.requeryTag == "" {
		return nil
	}

	nowStamp := now.UTC().Format(time.RFC3339)
	var (
		qTypeMask   uint8
		shouldQueue bool
	)

	for key, entry := range d.stats {
		bare, _ := splitStorageKey(key)
		if bare != domain || !entry.Promoted {
			continue
		}
		qTypeMask |= entry.QTypeMask
		if entry.CooldownUntil != "" {
			if ts, err := time.Parse(time.RFC3339, entry.CooldownUntil); err == nil && now.Before(ts) {
				continue
			}
		}
		entry.RefreshState = "dirty"
		entry.DirtyReason = "stale"
		entry.LastDirtyAt = nowStamp
		if d.policy.refreshCooldownMinute > 0 {
			entry.CooldownUntil = now.Add(time.Duration(d.policy.refreshCooldownMinute) * time.Minute).Format(time.RFC3339)
		}
		shouldQueue = true
	}

	if !shouldQueue {
		return nil
	}
	return &coremain.DomainRefreshJob{
		Domain:     domain,
		MemoryID:   d.memoryID,
		QTypeMask:  qTypeMask,
		Reason:     "stale",
		VerifyTag:  d.pluginTag,
		ObservedAt: now,
	}
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
