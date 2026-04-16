package client_map

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/neigh"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
)

const testMapContent = `# 测试映射文件
# 双栈设备分组
my_pc 192.168.1.100 fd00::100
kid   192.168.1.200 192.168.1.201 fd00::200 fd00::201
iot   192.168.1.0/28 fd00::1:0/112
`

func writeTestFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "client_map.txt")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestQCtx(clientAddr string) *query_context.Context {
	q := new(dns.Msg)
	q.SetQuestion("example.com.", dns.TypeA)
	qCtx := query_context.NewContext(q)
	if clientAddr != "" {
		qCtx.ServerMeta.ClientAddr = netip.MustParseAddr(clientAddr)
	}
	return qCtx
}

func TestParseArgs(t *testing.T) {
	args := parseArgs([]string{"&/path/to/file1.txt", "&/path/to/file2.txt", "tag1", "tag2"})
	if len(args.files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(args.files))
	}
	if args.files[0] != "/path/to/file1.txt" || args.files[1] != "/path/to/file2.txt" {
		t.Fatalf("unexpected files: %v", args.files)
	}
	if len(args.tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(args.tags))
	}
	if args.tags[0] != "tag1" || args.tags[1] != "tag2" {
		t.Fatalf("unexpected tags: %v", args.tags)
	}
}

func TestParsePrefix(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"192.168.1.1", false},
		{"192.168.1.0/24", false},
		{"fd00::1", false},
		{"fd00::/64", false},
		{"invalid", true},
		{"999.999.999.999", true},
	}
	for _, tt := range tests {
		_, err := parsePrefix(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parsePrefix(%q): err=%v, wantErr=%v", tt.input, err, tt.wantErr)
		}
	}
}

func TestLoadMapFile(t *testing.T) {
	path := writeTestFile(t, testMapContent)
	tagMap := make(map[string][]netip.Prefix)
	if err := loadMapFile(path, tagMap); err != nil {
		t.Fatal(err)
	}

	// my_pc: 2 entries (1 v4 + 1 v6)
	if len(tagMap["my_pc"]) != 2 {
		t.Errorf("my_pc: expected 2 prefixes, got %d", len(tagMap["my_pc"]))
	}
	// kid: 4 entries (2 v4 + 2 v6)
	if len(tagMap["kid"]) != 4 {
		t.Errorf("kid: expected 4 prefixes, got %d", len(tagMap["kid"]))
	}
	// iot: 2 entries (1 v4 CIDR + 1 v6 CIDR)
	if len(tagMap["iot"]) != 2 {
		t.Errorf("iot: expected 2 prefixes, got %d", len(tagMap["iot"]))
	}
}

func TestLoadMapFileErrors(t *testing.T) {
	// 只有 tag 没有 IP 的行应报错
	path := writeTestFile(t, "bad_line_tag\n")
	tagMap := make(map[string][]netip.Prefix)
	if err := loadMapFile(path, tagMap); err == nil {
		t.Error("expected error for line with only tag, got nil")
	}

	// 无效 IP 应报错
	path = writeTestFile(t, "tag invalid_ip\n")
	tagMap = make(map[string][]netip.Prefix)
	if err := loadMapFile(path, tagMap); err == nil {
		t.Error("expected error for invalid IP, got nil")
	}
}

func TestLoadMapFileEmpty(t *testing.T) {
	// 空文件和纯注释文件应正常加载
	path := writeTestFile(t, "# only comments\n\n")
	tagMap := make(map[string][]netip.Prefix)
	if err := loadMapFile(path, tagMap); err != nil {
		t.Fatalf("empty/comment-only file should not error: %v", err)
	}
	if len(tagMap) != 0 {
		t.Errorf("expected empty tagMap, got %d entries", len(tagMap))
	}
}

func TestLoadMapFileMultiLineTag(t *testing.T) {
	// 同一 tag 出现在多行，地址应合并
	content := "dev 192.168.1.1\ndev fd00::1\n"
	path := writeTestFile(t, content)
	tagMap := make(map[string][]netip.Prefix)
	if err := loadMapFile(path, tagMap); err != nil {
		t.Fatal(err)
	}
	if len(tagMap["dev"]) != 2 {
		t.Errorf("expected 2 prefixes for 'dev', got %d", len(tagMap["dev"]))
	}
}

func TestClientMapMatcher(t *testing.T) {
	path := writeTestFile(t, testMapContent)

	tests := []struct {
		name       string
		setupArgs  string
		clientAddr string
		wantMatch  bool
	}{
		// my_pc: 精确 IPv4 匹配
		{"my_pc v4 match", "&" + path + " my_pc", "192.168.1.100", true},
		// my_pc: 精确 IPv6 匹配
		{"my_pc v6 match", "&" + path + " my_pc", "fd00::100", true},
		// my_pc: 不匹配的 IP
		{"my_pc no match", "&" + path + " my_pc", "192.168.1.200", false},
		// kid: 多 tag 匹配
		{"kid v4 match", "&" + path + " kid", "192.168.1.200", true},
		{"kid v6 match", "&" + path + " kid", "fd00::201", true},
		// iot: CIDR 匹配
		{"iot v4 cidr", "&" + path + " iot", "192.168.1.5", true},
		{"iot v6 cidr", "&" + path + " iot", "fd00::1:ff", true},
		{"iot v4 cidr no match", "&" + path + " iot", "192.168.1.16", false},
		// 多个 tag 一起匹配
		{"multi tag match pc", "&" + path + " my_pc kid", "192.168.1.100", true},
		{"multi tag match kid", "&" + path + " my_pc kid", "fd00::200", true},
		{"multi tag no match", "&" + path + " my_pc kid", "10.0.0.1", false},
		// 无效 ClientAddr
		{"empty addr", "&" + path + " my_pc", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := QuickSetup(nil, tt.setupArgs)
			if err != nil {
				t.Fatalf("QuickSetup failed: %v", err)
			}

			qCtx := newTestQCtx(tt.clientAddr)
			got, err := m.Match(context.Background(), qCtx)
			if err != nil {
				t.Fatalf("Match returned error: %v", err)
			}
			if got != tt.wantMatch {
				t.Errorf("Match(%q) = %v, want %v", tt.clientAddr, got, tt.wantMatch)
			}
		})
	}
}

func TestClientMapMatcherFastCheck(t *testing.T) {
	path := writeTestFile(t, testMapContent)
	m, err := QuickSetup(nil, "&"+path+" my_pc kid")
	if err != nil {
		t.Fatal(err)
	}

	// clientMapMatcher 应实现 fastMatcher 接口
	type fastMatcher interface {
		GetFastCheck() func(qCtx *query_context.Context) bool
	}
	fm, ok := m.(fastMatcher)
	if !ok {
		t.Fatal("clientMapMatcher should implement fastMatcher interface")
	}

	check := fm.GetFastCheck()
	if check == nil {
		t.Fatal("GetFastCheck returned nil")
	}

	// 快路径应返回与 Match 一致的结果
	cases := []struct {
		addr string
		want bool
	}{
		{"192.168.1.100", true},
		{"fd00::200", true},
		{"10.0.0.1", false},
	}
	for _, c := range cases {
		qCtx := newTestQCtx(c.addr)
		if got := check(qCtx); got != c.want {
			t.Errorf("FastCheck(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestQuickSetupErrors(t *testing.T) {
	// 无文件
	if _, err := QuickSetup(nil, "tag_only"); err == nil {
		t.Error("expected error when no file provided")
	}

	// 无 tag
	if _, err := QuickSetup(nil, "&/some/file.txt"); err == nil {
		t.Error("expected error when no tag provided")
	}

	// 文件不存在
	if _, err := QuickSetup(nil, "&/nonexistent/file.txt some_tag"); err == nil {
		t.Error("expected error for nonexistent file")
	}

	// tag 不存在于文件中
	path := writeTestFile(t, "existing_tag 192.168.1.1\n")
	if _, err := QuickSetup(nil, "&"+path+" missing_tag"); err == nil {
		t.Error("expected error for missing tag")
	}
}

// --- 自动模式测试 ---

// mockNeighReader 返回一个模拟邻居表读取函数。
func mockNeighReader(entries []neigh.Entry, err error) func() ([]neigh.Entry, error) {
	return func() ([]neigh.Entry, error) {
		return entries, err
	}
}

func TestAutoMatcherBasic(t *testing.T) {
	// 模拟邻居表：一台设备 mac1 有 v4 和 v6 两个地址
	entries := []neigh.Entry{
		{IP: netip.MustParseAddr("192.168.1.100"), MAC: "aa:bb:cc:dd:ee:01"},
		{IP: netip.MustParseAddr("fd00::100"), MAC: "aa:bb:cc:dd:ee:01"},
		// 另一台设备
		{IP: netip.MustParseAddr("192.168.1.200"), MAC: "aa:bb:cc:dd:ee:02"},
		{IP: netip.MustParseAddr("fd00::200"), MAC: "aa:bb:cc:dd:ee:02"},
	}

	// 替换全局 neighReader
	origReader := neighReader
	neighReader = mockNeighReader(entries, nil)
	defer func() { neighReader = origReader }()

	// 只给 v4 种子 → 应该自动匹配同 MAC 的 v6
	m, err := QuickSetup(nil, "auto 192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		addr string
		want bool
	}{
		{"192.168.1.100", true},  // 种子 IP 本身
		{"fd00::100", true},      // 同 MAC 的 v6（自动发现）
		{"192.168.1.200", false}, // 其他设备
		{"fd00::200", false},     // 其他设备 v6
	}
	for _, tt := range tests {
		qCtx := newTestQCtx(tt.addr)
		got, err := m.Match(context.Background(), qCtx)
		if err != nil {
			t.Fatalf("Match(%q) error: %v", tt.addr, err)
		}
		if got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

func TestAutoMatcherMultipleSeeds(t *testing.T) {
	entries := []neigh.Entry{
		{IP: netip.MustParseAddr("192.168.1.100"), MAC: "aa:bb:cc:dd:ee:01"},
		{IP: netip.MustParseAddr("fd00::100"), MAC: "aa:bb:cc:dd:ee:01"},
		{IP: netip.MustParseAddr("192.168.1.200"), MAC: "aa:bb:cc:dd:ee:02"},
		{IP: netip.MustParseAddr("fd00::200"), MAC: "aa:bb:cc:dd:ee:02"},
		{IP: netip.MustParseAddr("10.0.0.1"), MAC: "aa:bb:cc:dd:ee:03"},
	}

	origReader := neighReader
	neighReader = mockNeighReader(entries, nil)
	defer func() { neighReader = origReader }()

	// 两个种子 IP，两台设备都匹配
	m, err := QuickSetup(nil, "auto 192.168.1.100 192.168.1.200")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		addr string
		want bool
	}{
		{"192.168.1.100", true},
		{"fd00::100", true},
		{"192.168.1.200", true},
		{"fd00::200", true},
		{"10.0.0.1", false}, // 第三台设备不在种子中
	}
	for _, tt := range tests {
		qCtx := newTestQCtx(tt.addr)
		got, _ := m.Match(context.Background(), qCtx)
		if got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

func TestAutoMatcherSeedFile(t *testing.T) {
	entries := []neigh.Entry{
		{IP: netip.MustParseAddr("192.168.1.100"), MAC: "aa:bb:cc:dd:ee:01"},
		{IP: netip.MustParseAddr("fd00::100"), MAC: "aa:bb:cc:dd:ee:01"},
	}

	origReader := neighReader
	neighReader = mockNeighReader(entries, nil)
	defer func() { neighReader = origReader }()

	// 从种子文件加载
	seedPath := writeTestFile(t, "# 种子 IP\n192.168.1.100\n")
	m, err := QuickSetup(nil, "auto &"+seedPath)
	if err != nil {
		t.Fatal(err)
	}

	qCtx := newTestQCtx("fd00::100")
	got, _ := m.Match(context.Background(), qCtx)
	if !got {
		t.Error("expected match for fd00::100 (same MAC as seed 192.168.1.100)")
	}
}

func TestAutoMatcherNeighFailFallback(t *testing.T) {
	// 邻居表读取失败 → 降级到种子 IP 模式
	origReader := neighReader
	neighReader = mockNeighReader(nil, fmt.Errorf("mock error"))
	defer func() { neighReader = origReader }()

	m, err := QuickSetup(nil, "auto 192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}

	// 种子 IP 本身应该可以匹配
	qCtx := newTestQCtx("192.168.1.100")
	got, _ := m.Match(context.Background(), qCtx)
	if !got {
		t.Error("expected fallback match for seed IP 192.168.1.100")
	}

	// 非种子 IP 不匹配
	qCtx = newTestQCtx("fd00::100")
	got, _ = m.Match(context.Background(), qCtx)
	if got {
		t.Error("expected no match for fd00::100 when neighbor table fails")
	}
}

func TestAutoMatcherRefresh(t *testing.T) {
	callCount := 0
	// 第一次返回少量条目
	entries1 := []neigh.Entry{
		{IP: netip.MustParseAddr("192.168.1.100"), MAC: "aa:bb:cc:dd:ee:01"},
	}
	// 第二次返回更多条目（新设备上线）
	entries2 := []neigh.Entry{
		{IP: netip.MustParseAddr("192.168.1.100"), MAC: "aa:bb:cc:dd:ee:01"},
		{IP: netip.MustParseAddr("fd00::100"), MAC: "aa:bb:cc:dd:ee:01"},
	}

	origReader := neighReader
	neighReader = func() ([]neigh.Entry, error) {
		callCount++
		if callCount <= 1 {
			return entries1, nil
		}
		return entries2, nil
	}
	defer func() { neighReader = origReader }()

	m, err := QuickSetup(nil, "auto 192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}

	// 刚创建时，fd00::100 不在邻居表中
	qCtx := newTestQCtx("fd00::100")
	got, _ := m.Match(context.Background(), qCtx)
	if got {
		t.Error("fd00::100 should not match before refresh")
	}

	// 强制使刷新间隔过期
	am := m.(*autoClientMapMatcher)
	am.mu.Lock()
	am.lastRefresh = time.Time{} // 重置到零值，触发刷新
	am.mu.Unlock()

	// 再次匹配应触发刷新并发现新的 v6 地址
	qCtx = newTestQCtx("fd00::100")
	got, _ = m.Match(context.Background(), qCtx)
	if !got {
		t.Error("fd00::100 should match after refresh with new neighbor entries")
	}
}

func TestAutoMatcherSeedNotInNeighbor(t *testing.T) {
	// 种子 IP 不在邻居表中（可能是手动配置的静态 IP）
	entries := []neigh.Entry{
		{IP: netip.MustParseAddr("192.168.1.200"), MAC: "aa:bb:cc:dd:ee:02"},
	}

	origReader := neighReader
	neighReader = mockNeighReader(entries, nil)
	defer func() { neighReader = origReader }()

	m, err := QuickSetup(nil, "auto 192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}

	// 种子 IP 本身应该始终匹配（即使不在邻居表中）
	qCtx := newTestQCtx("192.168.1.100")
	got, _ := m.Match(context.Background(), qCtx)
	if !got {
		t.Error("seed IP 192.168.1.100 should always match even if not in neighbor table")
	}
}

func TestAutoMatcherEmptyAddr(t *testing.T) {
	origReader := neighReader
	neighReader = mockNeighReader(nil, nil)
	defer func() { neighReader = origReader }()

	m, err := QuickSetup(nil, "auto 192.168.1.100")
	if err != nil {
		t.Fatal(err)
	}

	// 空 ClientAddr 不应匹配
	qCtx := newTestQCtx("")
	got, _ := m.Match(context.Background(), qCtx)
	if got {
		t.Error("empty addr should not match")
	}
}

func TestAutoMatcherSetupErrors(t *testing.T) {
	origReader := neighReader
	neighReader = mockNeighReader(nil, nil)
	defer func() { neighReader = origReader }()

	// auto 无种子 IP
	if _, err := QuickSetup(nil, "auto"); err == nil {
		t.Error("expected error for auto mode without seed IPs")
	}

	// 空参数
	if _, err := QuickSetup(nil, ""); err == nil {
		t.Error("expected error for empty args")
	}

	// auto 无效种子 IP
	if _, err := QuickSetup(nil, "auto not_an_ip"); err == nil {
		t.Error("expected error for invalid seed IP")
	}
}
