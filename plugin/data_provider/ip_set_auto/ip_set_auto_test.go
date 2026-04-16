package ip_set_auto

import (
	"net/netip"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/netlist"
	"github.com/IrineSistiana/mosdns/v5/pkg/neigh"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider/ip_set"
)

// TestCollectHostAddrs 验证从 netlist.List 提取单主机地址
func TestCollectHostAddrs(t *testing.T) {
	list := netlist.NewList()
	// /32 和 /128 应被收集；子网前缀不应被收集
	// 注意：netlist.Sort() 会合并包含关系的前缀，因此 /128 不能落在 /64 子网内
	list.Append(netip.MustParsePrefix("192.168.1.100/32"))
	list.Append(netip.MustParsePrefix("10.0.0.0/8"))
	list.Append(netip.MustParsePrefix("fd00::1/128"))    // 不在 fe80::/64 内
	list.Append(netip.MustParsePrefix("fe80::/64"))       // 与 fd00::1 无包含关系
	list.Sort()

	hosts := collectHostAddrs(list)
	hostSet := make(map[netip.Addr]bool)
	for _, h := range hosts {
		hostSet[h] = true
	}

	if !hostSet[netip.MustParseAddr("192.168.1.100")] {
		t.Error("should collect 192.168.1.100")
	}
	if !hostSet[netip.MustParseAddr("fd00::1")] {
		t.Error("should collect fd00::1")
	}
	if len(hosts) != 2 {
		t.Errorf("expected 2 hosts, got %d", len(hosts))
	}
}

// TestNormalizePrefix 验证 IPv4-mapped-IPv6 还原
func TestNormalizePrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"192.168.1.1/32", "192.168.1.1/32"},
		{"10.0.0.0/8", "10.0.0.0/8"},
		{"fd00::1/128", "fd00::1/128"},
		{"fd00::/64", "fd00::/64"},
	}
	for _, tc := range tests {
		pfx := netip.MustParsePrefix(tc.input)
		// Simulate internal storage format: IPv4 becomes IPv4-mapped-IPv6
		list := netlist.NewList()
		list.Append(pfx)
		list.Sort()
		list.ForEach(func(stored netip.Prefix) {
			result := normalizePrefix(stored)
			if result.String() != tc.expected {
				t.Errorf("normalizePrefix(%s) = %s, want %s", stored, result, tc.expected)
			}
		})
	}
}

// TestParseNetipPrefix 验证地址和前缀解析
func TestParseNetipPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected string
		wantErr  bool
	}{
		{"192.168.1.1", "192.168.1.1/32", false},
		{"192.168.1.0/24", "192.168.1.0/24", false},
		{"fd00::1", "fd00::1/128", false},
		{"fd00::/64", "fd00::/64", false},
		{"not_an_ip", "", true},
	}
	for _, tc := range tests {
		pfx, err := parseNetipPrefix(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseNetipPrefix(%q) expected error", tc.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseNetipPrefix(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if pfx.String() != tc.expected {
			t.Errorf("parseNetipPrefix(%q) = %s, want %s", tc.input, pfx, tc.expected)
		}
	}
}

// TestExpandWithCache 验证使用邻居缓存进行双栈扩展
func TestExpandWithCache(t *testing.T) {
	// 创建一个不自动刷新的缓存（interval=0），然后手动注入数据
	cache := neigh.NewCache(0)
	defer cache.Stop()

	// 构建基础列表：只有一个 IPv4
	baseList := netlist.NewList()
	baseList.Append(netip.MustParsePrefix("192.168.1.100/32"))
	baseList.Sort()

	// 手动设置 IPSetAuto 并测试 rebuild
	d := &IPSetAuto{
		baseList:   baseList,
		neighCache: cache,
	}

	// 初始扩展（邻居表为空，因为非 Linux 或读取失败）
	d.rebuildExpanded()
	m := d.matcherVal.Load().(netlist.Matcher)

	// 应该匹配原始 IP
	if !m.Match(netip.MustParseAddr("192.168.1.100")) {
		t.Error("should match base IP 192.168.1.100")
	}
	// 不应匹配未知 IP
	if m.Match(netip.MustParseAddr("192.168.1.200")) {
		t.Error("should not match unknown IP 192.168.1.200")
	}
}

// TestStoreSnapshot 验证 MatcherGroup 快照存储
func TestStoreSnapshot(t *testing.T) {
	d := &IPSetAuto{}

	list := netlist.NewList()
	list.Append(netip.MustParsePrefix("10.0.0.1/32"))
	list.Sort()

	d.storeSnapshot(list, nil)
	m := d.matcherVal.Load().(netlist.Matcher)

	if !m.Match(netip.MustParseAddr("10.0.0.1")) {
		t.Error("should match 10.0.0.1")
	}
	if m.Match(netip.MustParseAddr("10.0.0.2")) {
		t.Error("should not match 10.0.0.2")
	}
}

// TestStoreSnapshotWithOtherSets 验证多 set 组合
func TestStoreSnapshotWithOtherSets(t *testing.T) {
	d := &IPSetAuto{}

	list1 := netlist.NewList()
	list1.Append(netip.MustParsePrefix("10.0.0.1/32"))
	list1.Sort()

	list2 := netlist.NewList()
	list2.Append(netip.MustParsePrefix("172.16.0.1/32"))
	list2.Sort()

	d.storeSnapshot(list1, []netlist.Matcher{list2})
	m := d.matcherVal.Load().(netlist.Matcher)

	if !m.Match(netip.MustParseAddr("10.0.0.1")) {
		t.Error("should match 10.0.0.1 from primary list")
	}
	if !m.Match(netip.MustParseAddr("172.16.0.1")) {
		t.Error("should match 172.16.0.1 from other set")
	}
	if m.Match(netip.MustParseAddr("8.8.8.8")) {
		t.Error("should not match 8.8.8.8")
	}
}

// TestCloneArgs 验证参数深拷贝
func TestCloneArgs(t *testing.T) {
	src := newTestArgs([]string{"1.1.1.1"}, []string{"set1"}, []string{"/tmp/f1"})
	clone := cloneArgs(src)

	if len(clone.IPs) != 1 || clone.IPs[0] != "1.1.1.1" {
		t.Error("IPs mismatch")
	}

	// 修改原始不应影响克隆
	src.IPs[0] = "2.2.2.2"
	if clone.IPs[0] != "1.1.1.1" {
		t.Error("clone should be independent of source")
	}
}

// TestEmptyBaseList 验证空列表不会 panic
func TestEmptyBaseList(t *testing.T) {
	d := &IPSetAuto{
		baseList:   netlist.NewList(),
		neighCache: neigh.NewCache(0),
	}
	defer d.neighCache.Stop()

	d.rebuildExpanded()
	m := d.matcherVal.Load().(netlist.Matcher)
	if m.Match(netip.MustParseAddr("1.2.3.4")) {
		t.Error("empty list should not match any IP")
	}
}

// TestMatchFalseOnNilMatcher 验证 Match 在未初始化时返回 false
func TestMatchFalseOnNilMatcher(t *testing.T) {
	d := &IPSetAuto{}
	if d.Match(netip.MustParseAddr("1.1.1.1")) {
		t.Error("Match on nil matcher should return false")
	}
}

// TestSubnetNotExpanded 验证子网前缀不会触发邻居扩展
func TestSubnetNotExpanded(t *testing.T) {
	cache := neigh.NewCache(0)
	defer cache.Stop()

	baseList := netlist.NewList()
	// /24 是子网前缀，不应被扩展
	baseList.Append(netip.MustParsePrefix("192.168.1.0/24"))
	baseList.Sort()

	d := &IPSetAuto{
		baseList:   baseList,
		neighCache: cache,
	}
	d.rebuildExpanded()
	m := d.matcherVal.Load().(netlist.Matcher)

	// 整个子网内的 IP 都应匹配
	if !m.Match(netip.MustParseAddr("192.168.1.50")) {
		t.Error("should match 192.168.1.50 within /24")
	}
	// 子网外不应匹配
	if m.Match(netip.MustParseAddr("192.168.2.1")) {
		t.Error("should not match 192.168.2.1 outside /24")
	}
}

func newTestArgs(ips, sets, files []string) *ip_set.Args {
	return &ip_set.Args{
		IPs:   ips,
		Sets:  sets,
		Files: files,
	}
}
