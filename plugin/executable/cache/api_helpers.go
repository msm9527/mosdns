package cache

import (
	"encoding/json"
	"net/http"
)

func (c *Cache) resetL1() {
	capHint := c.l1ShardCap
	if capHint < 1 {
		capHint = 1
	}
	for i := 0; i < shardCount; i++ {
		c.shards[i].Lock()
		c.shards[i].items = make(map[key]*l1Item, capHint)
		c.shards[i].order = make([]key, capHint)
		c.shards[i].pos = 0
		c.shards[i].ref = make(map[key]bool, capHint)
		c.shards[i].maxSize = c.l1ShardCap
		c.shards[i].Unlock()
	}
}

func (c *Cache) writeStats(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(c.snapshotStats())
}
