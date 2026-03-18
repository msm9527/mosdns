package domain_memory_pool

import (
	"context"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"go.uber.org/zap"
)

func (d *domainMemoryPool) loadFromStore() error {
	state, ok, err := coremain.LoadDomainPoolStateFromPath(d.dbPath, d.pluginTag)
	if err != nil || !ok {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, variant := range state.Variants {
		storageKey := buildStorageKeyFromFlags(variant.Domain, variant.FlagsMask)
		entry := &statEntry{
			Count:          variant.TotalCount,
			LastDate:       formatDate(variant.LastSeenAtUnixMS),
			LastSeenAt:     formatStamp(variant.LastSeenAtUnixMS),
			LastDirtyAt:    formatStamp(variant.LastDirtyAtUnixMS),
			LastVerifiedAt: formatStamp(variant.LastVerifiedAtUnixMS),
			CooldownUntil:  formatStamp(variant.CooldownUntilUnixMS),
			DirtyReason:    variant.DirtyReason,
			RefreshState:   variant.RefreshState,
			QTypeMask:      variant.QTypeMask,
			Score:          variant.Score,
			Promoted:       variant.Promoted,
			ConflictCount:  variant.ConflictCount,
			LastSource:     variant.LastSource,
		}
		d.stats[storageKey] = entry
		d.trackEntryCreatedLocked(variant.Domain)
	}
	d.rules = buildRulesFromStoredDomains(state.Domains)
	d.replaceActiveHotRulesLocked(d.rules)
	atomicStoreIfGreater(&d.totalCount, state.Meta.TotalObservations)
	atomicStoreIfGreater(&d.promotedCount, int64(state.Meta.PromotedDomainCount))
	atomicStoreIfGreater(&d.publishedCount, int64(len(d.rules)))
	atomicStoreIfGreater(&d.lastHotSyncAtUnixMS, state.Meta.LastPublishAtUnixMS)
	return nil
}

func buildRulesFromStoredDomains(items []coremain.DomainPoolDomain) []string {
	rules := make([]string, 0, len(items))
	for _, item := range items {
		if item.Promoted {
			rules = append(rules, "full:"+item.Domain)
		}
	}
	return rules
}

func (d *domainMemoryPool) saveState(state coremain.DomainPoolState) error {
	state.Meta.PoolTag = d.pluginTag
	for i := range state.Domains {
		state.Domains[i].PoolTag = d.pluginTag
	}
	return coremain.SaveDomainPoolStateToPath(d.dbPath, state)
}

func (d *domainMemoryPool) notifyDirty(job coremain.DomainRefreshJob) {
	if d.policy.requeryTag == "" || d.plugin == nil {
		return
	}
	enqueuer, ok := d.plugin(d.policy.requeryTag).(coremain.DomainRefreshJobEnqueuer)
	if !ok || enqueuer == nil {
		if d.logger != nil {
			d.logger.Warn(
				"domain_memory_pool requery plugin not found",
				zap.String("plugin", d.pluginTag),
				zap.String("requery_tag", d.policy.requeryTag),
			)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !enqueuer.EnqueueDomainRefresh(ctx, job) && d.logger != nil {
		d.logger.Warn(
			"domain_memory_pool requery enqueue skipped",
			zap.String("plugin", d.pluginTag),
			zap.String("requery_tag", d.policy.requeryTag),
			zap.String("domain", job.Domain),
		)
	}
}

func (d *domainMemoryPool) GetRules() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.rules...), nil
}

func (d *domainMemoryPool) Subscribe(cb func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subscribers = append(d.subscribers, cb)
}

func (d *domainMemoryPool) notifySubscribers() {
	d.mu.Lock()
	subs := append([]func(){}, d.subscribers...)
	d.mu.Unlock()
	for _, cb := range subs {
		go cb()
	}
}

func formatStamp(unixMS int64) string {
	if unixMS <= 0 {
		return ""
	}
	return time.UnixMilli(unixMS).UTC().Format(time.RFC3339)
}

func formatDate(unixMS int64) string {
	if unixMS <= 0 {
		return ""
	}
	return time.UnixMilli(unixMS).UTC().Format("2006-01-02")
}

func atomicStoreIfGreater(target *int64, value int64) {
	if value <= 0 {
		return
	}
	*target = value
}
