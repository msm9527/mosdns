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

	// 加载后强制裁剪到 max_domains。
	// cap 仅在新条目写入时强制（ensureCapacityForNewEntryLocked），加载路径原本不裁剪，
	// 因此旧版本（缺写入期 cap enforcement）遗留的超限持久化数据会跨重启长期残留
	// （实测 realiplist 8167 / cap 6000）。这里按 LRU 淘汰最旧域名，使超限池在重启后自愈。
	if d.policy.maxDomains > 0 && d.domainCount > d.policy.maxDomains {
		excess := d.domainCount - d.policy.maxDomains
		evicted := d.evictLRUEntriesLocked(excess)
		// 淘汰可能移除了已 promoted 域名，按裁剪后的存活集合重建活跃热规则。
		// 已持有 d.mu，使用 *Locked 变体（与上方 replaceActiveHotRulesLocked 一致），不可自锁。
		promoted := make(map[string]struct{}, d.domainCount)
		for key, entry := range d.stats {
			if entry.Promoted && key.domain != "" {
				promoted[key.domain] = struct{}{}
			}
		}
		rules := make([]string, 0, len(promoted))
		for domain := range promoted {
			rules = append(rules, "full:"+domain)
		}
		d.replaceActiveHotRulesLocked(rules)
		d.lastRulesHash = hashPromotedDomains(state.Domains)
		atomic.StoreInt64(&d.publishedCount, int64(len(d.hotActiveRules)))
		if d.logger != nil {
			d.logger.Info("加载后裁剪超限记忆池",
				zap.String("pool", d.pluginTag),
				zap.Int("max_domains", d.policy.maxDomains),
				zap.Int("excess", excess),
				zap.Int("evicted", evicted),
				zap.Int("remaining", d.domainCount))
		}
	}
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
