// Package neigh 提供邻居表（ARP/NDP）的缓存与查询能力。
// Cache 是全局共享的单例，后台定时刷新，供 ip_set_auto 等插件查询双栈兄弟地址。
package neigh

import (
	"net/netip"
	"sync"
	"time"
)

// DefaultRefreshInterval 默认邻居表刷新间隔。
const DefaultRefreshInterval = 30 * time.Second

// Cache 维护 IP↔MAC 映射索引，支持通过 MAC 发现同设备的全部 IP（双栈兄弟）。
type Cache struct {
	mu         sync.RWMutex
	ipToMAC    map[netip.Addr]string
	macToIPs   map[string][]netip.Addr
	version    uint64
	subscribers []func()

	stopOnce sync.Once
	stopCh   chan struct{}
}

var (
	defaultCache     *Cache
	defaultCacheOnce sync.Once
)

// DefaultCache 返回全局共享的 Cache 单例，首次调用时启动后台刷新。
func DefaultCache() *Cache {
	defaultCacheOnce.Do(func() {
		defaultCache = NewCache(DefaultRefreshInterval)
	})
	return defaultCache
}

// NewCache 创建带后台刷新的邻居缓存。interval <= 0 时不启动后台刷新。
func NewCache(interval time.Duration) *Cache {
	c := &Cache{
		ipToMAC:  make(map[netip.Addr]string),
		macToIPs: make(map[string][]netip.Addr),
		stopCh:   make(chan struct{}),
	}
	// 立即刷新一次
	c.refresh()
	if interval > 0 {
		go c.loop(interval)
	}
	return c
}

// SiblingIPs 返回与 addr 同一 MAC（同设备）的所有 IP 地址。
// 若未找到对应 MAC，返回 nil。
func (c *Cache) SiblingIPs(addr netip.Addr) []netip.Addr {
	c.mu.RLock()
	defer c.mu.RUnlock()
	mac, ok := c.ipToMAC[addr]
	if !ok {
		return nil
	}
	return c.macToIPs[mac]
}

// Subscribe 注册回调，每次邻居表刷新后触发。
func (c *Cache) Subscribe(fn func()) {
	c.mu.Lock()
	c.subscribers = append(c.subscribers, fn)
	c.mu.Unlock()
}

// Version 返回当前刷新版本号（单调递增），可用于变更检测。
func (c *Cache) Version() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

// Stop 停止后台刷新协程。
func (c *Cache) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
}

func (c *Cache) loop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.refresh()
		case <-c.stopCh:
			return
		}
	}
}

func (c *Cache) refresh() {
	entries, err := ReadAll()
	if err != nil {
		// 非 Linux 或读取失败：清空索引但不崩溃
		c.mu.Lock()
		c.ipToMAC = make(map[netip.Addr]string)
		c.macToIPs = make(map[string][]netip.Addr)
		c.version++
		subs := make([]func(), len(c.subscribers))
		copy(subs, c.subscribers)
		c.mu.Unlock()
		for _, fn := range subs {
			fn()
		}
		return
	}

	ipToMAC := make(map[netip.Addr]string, len(entries))
	macToIPs := make(map[string][]netip.Addr)

	for _, e := range entries {
		// 确保存储的是 native 形式（IPv4 为 4 字节，IPv6 为 16 字节）
		addr := e.IP.Unmap()
		mac := e.MAC
		ipToMAC[addr] = mac
		macToIPs[mac] = append(macToIPs[mac], addr)
	}

	c.mu.Lock()
	c.ipToMAC = ipToMAC
	c.macToIPs = macToIPs
	c.version++
	subs := make([]func(), len(c.subscribers))
	copy(subs, c.subscribers)
	c.mu.Unlock()

	for _, fn := range subs {
		fn()
	}
}
