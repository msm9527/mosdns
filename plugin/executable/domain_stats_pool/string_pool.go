package domain_stats_pool

func (d *domainStatsPool) acquireStorageKey(domain string, item *logItem) (string, string) {
	canonicalDomain := d.strings.Acquire(domain)
	storageKey := buildStorageKey(canonicalDomain, item, d.enableFlags)
	return canonicalDomain, d.strings.Acquire(storageKey)
}

func (d *domainStatsPool) acquireStorageKeyFromFlags(domain string, flagsMask uint8) (string, string) {
	canonicalDomain := d.strings.Acquire(domain)
	storageKey := buildStorageKeyFromFlags(canonicalDomain, flagsMask)
	return canonicalDomain, d.strings.Acquire(storageKey)
}

func (d *domainStatsPool) releaseStorageKey(storageKey string) {
	d.strings.Release(storageKey)
}

func (d *domainStatsPool) releaseDomain(domain string) {
	d.strings.Release(domain)
}
