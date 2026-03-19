package fastforward

import (
	"context"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

func (f *Forward) configureStatsPersistence(controlDBPath string) error {
	f.controlDBPath = strings.TrimSpace(controlDBPath)
	f.bindStatsCallbacks(f.us)
	if err := f.restorePersistentStats(f.us); err != nil {
		return err
	}
	f.statsFlusher = coremain.NewUpstreamRuntimeStatsFlusher(f.logger, f.flushPersistentStats)
	return nil
}

func (f *Forward) bindStatsCallbacks(us []*upstreamWrapper) {
	for _, u := range us {
		if u != nil {
			u.onStatsChanged = f.markStatsDirty
		}
	}
}

func (f *Forward) restorePersistentStats(us []*upstreamWrapper) error {
	if f.controlDBPath == "" || f.pluginTag == "" {
		return nil
	}
	values, err := coremain.LoadUpstreamRuntimeStatsByPlugin(f.controlDBPath, f.pluginTag)
	if err != nil {
		return err
	}
	for _, u := range us {
		if u == nil || u.cfg.Tag == "" {
			continue
		}
		if stats, ok := values[u.cfg.Tag]; ok {
			u.applyPersistentStats(stats)
		}
	}
	return nil
}

func (f *Forward) flushPersistentStats() error {
	if f.controlDBPath == "" || f.pluginTag == "" {
		return nil
	}
	return coremain.SaveUpstreamRuntimeStats(f.controlDBPath, f.snapshotPersistentStats())
}

func (f *Forward) snapshotPersistentStats() []coremain.UpstreamRuntimeStats {
	_, us := f.snapshotRuntime()
	items := make([]coremain.UpstreamRuntimeStats, 0, len(us))
	for _, u := range us {
		if item, ok := u.snapshotPersistentStats(f.pluginTag); ok {
			items = append(items, item)
		}
	}
	return items
}

func (f *Forward) markStatsDirty() {
	if f != nil && f.statsFlusher != nil {
		f.statsFlusher.MarkDirty()
	}
}

func (f *Forward) closeStatsFlusher() error {
	if f == nil || f.statsFlusher == nil {
		return nil
	}
	err := f.statsFlusher.Close()
	f.statsFlusher = nil
	return err
}

func (f *Forward) ResetUpstreamStats(_ context.Context, upstreamTag string) (int, error) {
	_, us := f.snapshotRuntime()
	count := 0
	for _, u := range us {
		if u == nil {
			continue
		}
		if upstreamTag != "" && u.cfg.Tag != upstreamTag {
			continue
		}
		if u.resetPersistentStats() {
			count++
		}
	}
	return count, nil
}
