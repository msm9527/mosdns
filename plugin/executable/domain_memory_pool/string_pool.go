package domain_memory_pool

func (d *domainMemoryPool) acquireEntryKey(domain string, flagsMask uint8) (string, entryKey) {
	canonicalDomain := d.strings.Acquire(domain)
	return canonicalDomain, buildEntryKey(canonicalDomain, flagsMask)
}

func (d *domainMemoryPool) acquireEntryKeyFromFlags(domain string, flagsMask uint8) (string, entryKey) {
	return d.acquireEntryKey(domain, flagsMask)
}

func (d *domainMemoryPool) releaseDomain(domain string) {
	d.strings.Release(domain)
}
