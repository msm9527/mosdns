package cache

import (
	"encoding/json"
	"net/http"
)

func (c *Cache) resetL1() {
	for i := 0; i < shardCount; i++ {
		c.shards[i].Lock()
		c.shards[i].items = make(map[key]*l1Item, shardMaxSize)
		c.shards[i].order = make([]key, shardMaxSize)
		c.shards[i].pos = 0
		c.shards[i].ref = make(map[key]bool, shardMaxSize)
		c.shards[i].Unlock()
	}
}

func (c *Cache) writeStats(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(c.snapshotStats())
}
