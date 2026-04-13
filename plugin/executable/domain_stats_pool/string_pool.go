package domain_stats_pool

func (d *domainStatsPool) acquireEntryKey(domain string, flagsMask uint8) (string, entryKey) {
	canonicalDomain := d.strings.Acquire(domain)
	return canonicalDomain, buildEntryKey(canonicalDomain, flagsMask)
}

func (d *domainStatsPool) acquireEntryKeyFromFlags(domain string, flagsMask uint8) (string, entryKey) {
	return d.acquireEntryKey(domain, flagsMask)
}

func (d *domainStatsPool) releaseDomain(domain string) {
	d.strings.Release(domain)
}
