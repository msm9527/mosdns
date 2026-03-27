package cache

func (c *Cache) prepareCacheItemForStore(v *item) *item {
	if c == nil || v == nil || v.domainSet == "" {
		return v
	}
	v.domainSet = c.domainSets.Acquire(v.domainSet)
	return v
}

func (c *Cache) releaseCacheItemResources(v *item) {
	if c == nil || v == nil || v.domainSet == "" {
		return
	}
	c.domainSets.Release(v.domainSet)
}
