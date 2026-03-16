package domain_memory_pool

import (
	"fmt"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

type Args struct{}

type writePolicy struct {
	raw                   coremain.DomainPoolPolicy
	kind                  string
	promoteAfter          int
	decayDays             int
	trackQType            bool
	trackFlags            bool
	staleAfterMinutes     int
	refreshCooldownMinute int
	requeryTag            string
	maxDomains            int
	maxVariantsPerDomain  int
	flushEvery            time.Duration
	publishDebounce       time.Duration
	pruneEvery            time.Duration
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
	if raw.Kind != coremain.DomainPoolKindMemory {
		return writePolicy{}, fmt.Errorf("%s requires memory pool policy, got %s", pluginTag, raw.Kind)
	}

	policy := writePolicy{
		raw:                   raw,
		kind:                  raw.MemoryID,
		promoteAfter:          raw.PromoteAfter,
		trackQType:            raw.TrackQType,
		trackFlags:            raw.TrackFlags,
		staleAfterMinutes:     raw.StaleAfterMinutes,
		refreshCooldownMinute: raw.RefreshCooldownMinutes,
		requeryTag:            raw.RequeryTag,
		maxDomains:            raw.MaxDomains,
		maxVariantsPerDomain:  raw.MaxVariantsPerDomain,
		flushEvery:            time.Duration(raw.FlushIntervalMS) * time.Millisecond,
		publishDebounce:       time.Duration(raw.PublishDebounceMS) * time.Millisecond,
		pruneEvery:            time.Duration(raw.PruneIntervalSec) * time.Second,
	}
	policy.decayDays = defaultDecayDays(policy.kind)
	return policy, nil
}

func defaultDecayDays(kind string) int {
	switch kind {
	case "realip", "fakeip":
		return 21
	case "nov4", "nov6", "nodenov4", "nodenov6":
		return 14
	default:
		return 30
	}
}
