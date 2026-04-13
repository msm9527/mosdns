package domain_stats_pool

import (
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

func (d *domainStatsPool) loadFromStore() error {
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
	d.hasRulesHash = true
	atomicStoreIfGreater(&d.totalCount, state.Meta.TotalObservations)
	return nil
}

func (d *domainStatsPool) saveState(state coremain.DomainPoolState) error {
	state.Meta.PoolTag = d.pluginTag
	for i := range state.Domains {
		state.Domains[i].PoolTag = d.pluginTag
	}
	return coremain.SaveDomainPoolStateToPath(d.dbPath, state)
}

func (d *domainStatsPool) notifyDirty(job coremain.DomainRefreshJob) {
	_ = job
}

func (d *domainStatsPool) GetRules() ([]string, error) {
	return nil, nil
}

func (d *domainStatsPool) Subscribe(cb func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subscribers = append(d.subscribers, cb)
}

func (d *domainStatsPool) notifySubscribers() {
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
