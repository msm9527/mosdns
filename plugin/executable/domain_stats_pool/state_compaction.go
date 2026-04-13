package domain_stats_pool

const (
	stateCompactionMinEntries    = 1024
	stateCompactionShrinkDivisor = 2
)

func (d *domainStatsPool) noteStatePeaksLocked() {
	if size := len(d.stats); size > d.statsPeak {
		d.statsPeak = size
	}
	if size := len(d.domainVariantCount); size > d.domainVariantPeak {
		d.domainVariantPeak = size
	}
}

func (d *domainStatsPool) maybeCompactStateLocked() {
	if shouldCompactStateMap(len(d.stats), d.statsPeak) {
		d.stats = cloneStatEntries(d.stats)
		d.statsPeak = len(d.stats)
	}
	if shouldCompactStateMap(len(d.domainVariantCount), d.domainVariantPeak) {
		d.domainVariantCount = cloneDomainVariantCounts(d.domainVariantCount)
		d.domainVariantPeak = len(d.domainVariantCount)
	}
}

func (d *domainStatsPool) resetStateStorageLocked() {
	d.stats = make(map[entryKey]*statEntry)
	d.domainVariantCount = make(map[string]uint8)
	d.strings.Reset()
	d.statsPeak = 0
	d.domainVariantPeak = 0
}

func shouldCompactStateMap(current, peak int) bool {
	switch {
	case current == 0:
		return true
	case peak < stateCompactionMinEntries:
		return false
	default:
		return current*stateCompactionShrinkDivisor <= peak
	}
}

func cloneStatEntries(src map[entryKey]*statEntry) map[entryKey]*statEntry {
	if len(src) == 0 {
		return make(map[entryKey]*statEntry)
	}
	dst := make(map[entryKey]*statEntry, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneDomainVariantCounts(src map[string]uint8) map[string]uint8 {
	if len(src) == 0 {
		return make(map[string]uint8)
	}
	dst := make(map[string]uint8, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
