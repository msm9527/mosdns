package cache

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/miekg/dns"
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

func (c *Cache) deleteL1Keys(keys []key) {
	if len(keys) == 0 {
		return
	}
	for _, k := range keys {
		shard := c.shards[k.Sum()%shardCount]
		shard.Lock()
		delete(shard.items, k)
		delete(shard.ref, k)
		for i, existing := range shard.order {
			if existing == k {
				shard.order[i] = ""
			}
		}
		shard.Unlock()
	}
}

func domainSetContainsToken(domainSet, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || strings.TrimSpace(domainSet) == "" {
		return false
	}
	for _, part := range strings.Split(domainSet, "|") {
		if strings.TrimSpace(part) == token {
			return true
		}
	}
	return false
}

type cacheKeyMeta struct {
	QName string
	QType uint16
}

func parseCacheKeyMeta(k key) (cacheKeyMeta, bool) {
	data := []byte(k)
	offset := 0

	if len(data) < offset+1 {
		return cacheKeyMeta{}, false
	}
	offset++

	if len(data) < offset+2 {
		return cacheKeyMeta{}, false
	}
	qtype := dns.Type(binaryBigEndianUint16(data[offset : offset+2]))
	offset += 2

	if len(data) < offset+1 {
		return cacheKeyMeta{}, false
	}
	nameLen := int(data[offset])
	offset++
	if len(data) < offset+nameLen {
		return cacheKeyMeta{}, false
	}
	qname := string(data[offset : offset+nameLen])
	return cacheKeyMeta{
		QName: qname,
		QType: uint16(qtype),
	}, true
}

func binaryBigEndianUint16(b []byte) uint16 {
	_ = b[1]
	return uint16(b[0])<<8 | uint16(b[1])
}
