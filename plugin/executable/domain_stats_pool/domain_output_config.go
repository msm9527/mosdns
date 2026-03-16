package domain_stats_pool

import (
	"fmt"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

type Args struct{}

type writePolicy struct {
	raw                  coremain.DomainPoolPolicy
	kind                 string
	trackQType           bool
	trackFlags           bool
	maxDomains           int
	maxVariantsPerDomain int
	flushEvery           time.Duration
	pruneEvery           time.Duration
	retentionDays        int
}

func resolveWritePolicy(pluginTag string) (writePolicy, error) {
	values, _, err := coremain.LoadMemoryPoolPoliciesFromCustomConfig()
	if err != nil {
		return writePolicy{}, err
	}
	raw, err := coremain.ResolveDomainPoolPolicy(pluginTag, values)
	if err != nil {
		return writePolicy{}, err
	}
	if raw.Kind != coremain.DomainPoolKindStats {
		return writePolicy{}, fmt.Errorf("%s requires stats pool policy, got %s", pluginTag, raw.Kind)
	}
	return writePolicy{
		raw:                  raw,
		kind:                 raw.MemoryID,
		trackQType:           raw.TrackQType,
		trackFlags:           raw.TrackFlags,
		maxDomains:           raw.MaxDomains,
		maxVariantsPerDomain: raw.MaxVariantsPerDomain,
		flushEvery:           time.Duration(raw.FlushIntervalMS) * time.Millisecond,
		pruneEvery:           time.Duration(raw.PruneIntervalSec) * time.Second,
		retentionDays:        defaultRetentionDays(raw.MemoryID),
	}, nil
}

func defaultRetentionDays(memoryID string) int {
	switch memoryID {
	case "top":
		return 30
	default:
		return 21
	}
}
