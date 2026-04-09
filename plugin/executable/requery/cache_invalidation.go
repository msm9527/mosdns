package requery

import (
	"context"
	"log"
	"sort"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

const (
	runtimeCachePrecisePurgeMaxDomains = 1000
	runtimeCachePrecisePurgePercentDiv = 100
	runtimeCachePurgeLogMinDomains     = 4
)

type runtimeCacheTarget struct {
	tag        string
	controller coremain.RuntimeCacheController
}

func (p *Requery) invalidateCachesAfterPublish(ctx context.Context, domains []string) bool {
	domains = normalizeInvalidationDomains(domains)
	if len(domains) == 0 {
		return true
	}

	targets := p.snapshotRuntimeCacheTargets()
	if len(targets) == 0 {
		return true
	}

	responseEntries := 0
	for _, target := range targets {
		if target.controller.RuntimeCacheKind() == "response" {
			responseEntries += target.controller.RuntimeCacheEntryCount()
		}
	}

	shouldFlush := len(domains) > runtimeCachePrecisePurgeMaxDomains
	if !shouldFlush && responseEntries > 0 {
		threshold := responseEntries / runtimeCachePrecisePurgePercentDiv
		if threshold < 1 {
			threshold = 1
		}
		shouldFlush = len(domains) > threshold
	}

	if shouldFlush {
		log.Printf("[requery] Step 8: cache invalidation fallback to full flush, changed_domains=%d response_entries=%d", len(domains), responseEntries)
		for _, target := range targets {
			if err := target.controller.FlushRuntimeCache(ctx); err != nil {
				p.setFailedState("failed to flush runtime cache %s: %v", target.tag, err)
				return false
			}
		}
		return true
	}

	if len(domains) >= runtimeCachePurgeLogMinDomains {
		log.Printf("[requery] Step 8: purging %d changed domains from runtime caches...", len(domains))
	}
	for _, target := range targets {
		if _, err := target.controller.PurgeDomainsRuntimeCache(ctx, domains, nil); err != nil {
			p.setFailedState("failed to purge runtime cache %s: %v", target.tag, err)
			return false
		}
	}
	return true
}

func (p *Requery) snapshotRuntimeCacheTargets() []runtimeCacheTarget {
	if p == nil || p.snapshotter == nil {
		return nil
	}
	snapshot := p.snapshotter.SnapshotPlugins()
	targets := make([]runtimeCacheTarget, 0, len(snapshot))
	for tag, plugin := range snapshot {
		controller, ok := plugin.(coremain.RuntimeCacheController)
		if !ok || controller == nil {
			continue
		}
		kind := controller.RuntimeCacheKind()
		if kind != "response" && kind != "udp_fast" {
			continue
		}
		targets = append(targets, runtimeCacheTarget{tag: tag, controller: controller})
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].tag < targets[j].tag })
	return targets
}

func normalizeInvalidationDomains(domains []string) []string {
	seen := make(map[string]struct{}, len(domains))
	out := make([]string, 0, len(domains))
	for _, domain := range domains {
		name := strings.TrimSpace(strings.TrimSuffix(domain, "."))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func flattenRefreshJobDomains(jobs []refreshJob) []string {
	domains := make([]string, 0, len(jobs))
	for _, job := range jobs {
		domains = append(domains, job.Domain)
	}
	return normalizeInvalidationDomains(domains)
}
