package client_map

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

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
	args := parseArgs("&/path/to/file1.txt &/path/to/file2.txt tag1 tag2")
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
