package domain_memory_pool

const (
	stateCompactionMinEntries    = 1024
	stateCompactionShrinkDivisor = 2
)

func (d *domainMemoryPool) noteStatePeaksLocked() {
	if size := len(d.stats); size > d.statsPeak {
		d.statsPeak = size
	}
	if size := len(d.domainVariantCount); size > d.domainVariantPeak {
		d.domainVariantPeak = size
	}
}

func (d *domainMemoryPool) maybeCompactStateLocked() {
	if shouldCompactStateMap(len(d.stats), d.statsPeak) {
		d.stats = cloneStatEntries(d.stats)
		d.statsPeak = len(d.stats)
	}
	if shouldCompactStateMap(len(d.domainVariantCount), d.domainVariantPeak) {
		d.domainVariantCount = cloneDomainVariantCounts(d.domainVariantCount)
		d.domainVariantPeak = len(d.domainVariantCount)
	}
}

func (d *domainMemoryPool) resetStateStorageLocked() {
	d.stats = make(map[string]*statEntry)
	d.domainVariantCount = make(map[string]int)
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

func cloneStatEntries(src map[string]*statEntry) map[string]*statEntry {
	if len(src) == 0 {
		return make(map[string]*statEntry)
	}
	dst := make(map[string]*statEntry, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneDomainVariantCounts(src map[string]int) map[string]int {
	if len(src) == 0 {
		return make(map[string]int)
	}
	dst := make(map[string]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
