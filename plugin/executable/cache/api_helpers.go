package cache

import (
	"encoding/json"
	"net/http"

	"github.com/miekg/dns"
	"go.uber.org/zap"
)

func (c *Cache) resetL1() {
	for i := 0; i < shardCount; i++ {
		c.shards[i].Lock()
		c.shards[i].items = nil
		c.shards[i].order = nil
		c.shards[i].pos = 0
		c.shards[i].ref = nil
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
		if entry, ok := shard.items[k]; ok {
			delete(shard.items, k)
			if entry.slot >= 0 && entry.slot < len(shard.order) {
				shard.order[entry.slot] = ""
			}
			if entry.slot >= 0 && entry.slot < len(shard.ref) {
				shard.ref[entry.slot] = false
			}
		}
		shard.Unlock()
	}
}

func (c *Cache) deleteL1Key(k key) {
	c.deleteL1Keys([]key{k})
}

func (c *Cache) deleteRuntimeCacheKey(k key, reason string) {
	c.backend.Delete(k)
	c.deleteL1Key(k)
	c.appendRuntimeDelete(k, reason)
}

func (c *Cache) appendRuntimeDelete(k key, reason string) {
	if c.persistence == nil {
		return
	}
	if err := c.persistence.appendDelete(k); err != nil {
		c.recordWALAppendError("delete", reason, err)
		return
	}
	c.recordWALAppendSuccess(1)
}

func (c *Cache) appendRuntimeDeleteBatch(keys []key, reason string) error {
	if c.persistence == nil || len(keys) == 0 {
		return nil
	}
	if err := c.persistence.appendDeleteBatch(keys); err != nil {
		c.recordWALAppendError("delete", reason, err)
		return err
	}
	c.recordWALAppendSuccess(len(keys))
	return nil
}

func (c *Cache) appendRuntimeFlush(reason string) error {
	if c.persistence == nil {
		return nil
	}
	if err := c.persistence.appendFlush(); err != nil {
		c.recordWALAppendError("flush", reason, err)
		return err
	}
	c.recordWALAppendSuccess(1)
	return nil
}

func (c *Cache) recordWALAppendSuccess(records int) {
	if c.args != nil && c.args.WALFile != "" && records > 0 {
		c.walAppendCounter.Add(float64(records))
	}
}

func (c *Cache) recordWALAppendError(op, reason string, err error) {
	c.walAppendErrorCounter.Inc()
	c.logger.Warn("failed to append cache wal",
		zap.String("op", op),
		zap.String("reason", reason),
		zap.Error(err),
	)
}

func domainSetContainsToken(domainSet, token string) bool {
	return domainSetContainsAnyToken(domainSet, []string{token})
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
