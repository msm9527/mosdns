package domain_memory_pool

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"go.uber.org/zap"
)

const enqueueWarnInterval = 30 * time.Second

func (d *domainMemoryPool) loadFromStore() error {
	state, ok, err := coremain.LoadDomainPoolStateFromPath(d.dbPath, d.pluginTag)
	if err != nil || !ok {
		return err
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, variant := range state.Variants {
		domain, key := d.acquireEntryKeyFromFlags(variant.Domain, variant.FlagsMask)
		entry := &statEntry{
			Count:                variant.TotalCount,
			LastSeenAtUnixMS:     variant.LastSeenAtUnixMS,
			LastDirtyAtUnixMS:    variant.LastDirtyAtUnixMS,
			LastVerifiedAtUnixMS: variant.LastVerifiedAtUnixMS,
			CooldownUntilUnixMS:  variant.CooldownUntilUnixMS,
			DirtyReason:          variant.DirtyReason,
			RefreshState:         variant.RefreshState,
			QTypeMask:            variant.QTypeMask,
			Score:                variant.Score,
			Promoted:             variant.Promoted,
			ConflictCount:        variant.ConflictCount,
			LastSource:           variant.LastSource,
		}
		d.stats[key] = entry
		d.trackEntryCreatedLocked(domain)
	}
	rules := buildRulesFromStoredDomains(state.Domains)
	d.replaceActiveHotRulesLocked(rules)
	d.lastRulesHash = hashPromotedDomains(state.Domains)
	d.hasRulesHash = true
	atomicStoreIfGreater(&d.totalCount, state.Meta.TotalObservations)
	atomicStoreIfGreater(&d.promotedCount, int64(state.Meta.PromotedDomainCount))
	atomicStoreIfGreater(&d.publishedCount, int64(len(rules)))
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
	if enqueuer, ok := d.plugin(d.policy.requeryTag).(coremain.DomainRefreshJobResultEnqueuer); ok && enqueuer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		switch result := enqueuer.EnqueueDomainRefreshResult(ctx, job); result {
		case coremain.DomainRefreshEnqueueQueued:
			return
		case coremain.DomainRefreshEnqueueQueueFull:
			if d.logger != nil && d.allowEnqueueWarn(time.Now()) {
				d.logger.Warn(
					"domain_memory_pool requery queue full, skipping on-demand refresh",
					zap.String("plugin", d.pluginTag),
					zap.String("requery_tag", d.policy.requeryTag),
					zap.String("domain", job.Domain),
					zap.String("reason", string(result)),
				)
			}
			return
		default:
			return
		}
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
	if !enqueuer.EnqueueDomainRefresh(ctx, job) && d.logger != nil && d.allowEnqueueWarn(time.Now()) {
		d.logger.Warn(
			"domain_memory_pool requery enqueue skipped",
			zap.String("plugin", d.pluginTag),
			zap.String("requery_tag", d.policy.requeryTag),
			zap.String("domain", job.Domain),
		)
	}
}

func (d *domainMemoryPool) allowEnqueueWarn(now time.Time) bool {
	nowMS := now.UTC().UnixMilli()
	intervalMS := enqueueWarnInterval.Milliseconds()
	for {
		last := atomic.LoadInt64(&d.lastEnqueueWarnAtMS)
		if last > 0 && nowMS-last < intervalMS {
			return false
		}
		if atomic.CompareAndSwapInt64(&d.lastEnqueueWarnAtMS, last, nowMS) {
			return true
		}
	}
}

func (d *domainMemoryPool) GetRules() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotActiveHotRulesLocked(), nil
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
