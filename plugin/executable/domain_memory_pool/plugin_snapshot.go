package domain_memory_pool

type pluginSnapshotterFunc func() map[string]any

func (fn pluginSnapshotterFunc) SnapshotPlugins() map[string]any {
	if fn == nil {
		return nil
	}
	return fn()
}
