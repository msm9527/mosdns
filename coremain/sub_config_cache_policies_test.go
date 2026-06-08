package coremain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCachePolicyConfigFromSubConfigDefaults(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	cfg, ok, err := LoadCachePolicyConfigFromSubConfig()
	if err != nil {
		t.Fatalf("LoadCachePolicyConfigFromSubConfig: %v", err)
	}
	if ok {
		t.Fatal("expected cache policy file to be absent")
	}
	if cfg.Response["cache_main"].Size <= 0 {
		t.Fatalf("expected default cache_main policy, got %+v", cfg.Response["cache_main"])
	}
	if cfg.Response["cache_main"].LazyCacheTTL != 2592000 {
		t.Fatalf("expected default cache_main lazy cache ttl 2592000, got %+v", cfg.Response["cache_main"])
	}
	if cfg.Response["cache_main"].LazyStaleTTL != 2592000 {
		t.Fatalf("expected default cache_main lazy stale ttl 2592000, got %+v", cfg.Response["cache_main"])
	}
	if cfg.Response["cache_main"].ClientTTLMin != 120 || cfg.Response["cache_main"].ClientTTLMax != 900 {
		t.Fatalf("expected default cache_main client ttl clamp 120-900, got %+v", cfg.Response["cache_main"])
	}
	if cfg.Response["cache_main"].ServfailTTL != 1 || cfg.Response["cache_main"].ColdQueryWaitMs != 80 {
		t.Fatalf("expected default cache_main short servfail ttl and bounded cold query wait, got %+v", cfg.Response["cache_main"])
	}
	if got := cfg.Response["cache_main"].BypassDomainSets; len(got) != 1 || got[0] != "DDNS域名" {
		t.Fatalf("expected default cache_main bypass domain sets, got %+v", got)
	}
	assertHighChurnTTLPolicy(t, cfg.Response["cache_main"].DomainSetTTL, 43200, 43200)
	if got := cfg.Response["cache_main"].ExcludeIPs; !sameStringSlice(got, defaultRealCacheExcludeIPs()) {
		t.Fatalf("expected default cache_main fakeip/sinkhole exclusions, got %+v", got)
	}
	if cfg.Response["cache_fakeip_proxy"].Persist {
		t.Fatalf("expected fakeip proxy cache to default to non-persistent, got %+v", cfg.Response["cache_fakeip_proxy"])
	}
	if got := cfg.Response["cache_fakeip_proxy"].ExcludeIPs; len(got) != 0 {
		t.Fatalf("expected fakeip proxy cache not to exclude fakeip ranges, got %+v", got)
	}
	if cfg.Response["cache_fakeip_proxy"].LazyCacheTTL != 14400 || cfg.Response["cache_fakeip_proxy"].LazyStaleTTL != 14400 {
		t.Fatalf("expected fakeip proxy cache to retain backend entries and stale replies, got %+v", cfg.Response["cache_fakeip_proxy"])
	}
	if cfg.Response["cache_fakeip_proxy"].ClientTTLMin != 600 || cfg.Response["cache_fakeip_proxy"].ClientTTLMax != 600 {
		t.Fatalf("expected fakeip proxy cache to return stable client ttl, got %+v", cfg.Response["cache_fakeip_proxy"])
	}
	if cfg.UDPFastPath.InternalTTL != 7200 || cfg.UDPFastPath.StaleRetry != 30 || cfg.UDPFastPath.StaleMax != 7200 || cfg.UDPFastPath.TTLMin != 120 || cfg.UDPFastPath.TTLMax != 900 {
		t.Fatalf("unexpected udp fast policy: %+v", cfg.UDPFastPath)
	}
	if got := cfg.UDPFastPath.BypassDomainSets; len(got) != 1 || got[0] != "DDNS域名" {
		t.Fatalf("expected default udp fast bypass domain sets, got %+v", got)
	}
	assertHighChurnTTLPolicy(t, cfg.UDPFastPath.DomainSetTTL, 43200, 43200)
}

func TestDefaultCachePolicyConfigUsesMainPersistentBranchShortTermProfile(t *testing.T) {
	cfg := defaultCachePolicyConfig()

	totalSize := 0
	totalL1Cap := 0
	for _, policy := range cfg.Response {
		totalSize += policy.Size
		totalL1Cap += policy.L1TotalCap
	}

	if cfg.Response["cache_main"].Size != defaultCacheMainSize {
		t.Fatalf("cache_main size = %d, want %d", cfg.Response["cache_main"].Size, defaultCacheMainSize)
	}
	if cfg.Response["cache_branch_foreign"].LazyCacheTTL != 900 {
		t.Fatalf("cache_branch_foreign lazy cache ttl = %d, want 900", cfg.Response["cache_branch_foreign"].LazyCacheTTL)
	}
	if cfg.Response["cache_branch_foreign"].LazyStaleTTL != 300 {
		t.Fatalf("cache_branch_foreign lazy stale ttl = %d, want 300", cfg.Response["cache_branch_foreign"].LazyStaleTTL)
	}
	if cfg.Response["cache_branch_foreign"].ClientTTLMin != 120 || cfg.Response["cache_branch_foreign"].ClientTTLMax != 900 {
		t.Fatalf("cache_branch_foreign client ttl clamp = %+v, want 120-900", cfg.Response["cache_branch_foreign"])
	}
	if cfg.Response["cache_branch_foreign"].ServfailTTL != 1 || cfg.Response["cache_branch_foreign"].ColdQueryWaitMs != 80 {
		t.Fatalf("cache_branch_foreign should use short servfail ttl and bounded cold query wait, got %+v", cfg.Response["cache_branch_foreign"])
	}
	if cfg.Response["cache_branch_foreign"].Persist {
		t.Fatal("cache_branch_foreign should be short-term only")
	}
	if cfg.Response["cache_fakeip_domestic"].LazyCacheTTL != 14400 || cfg.Response["cache_fakeip_domestic"].ClientTTLMin != 600 || cfg.Response["cache_fakeip_domestic"].ClientTTLMax != 600 {
		t.Fatalf("cache_fakeip_domestic should use long backend retention and stable client ttl, got %+v", cfg.Response["cache_fakeip_domestic"])
	}
	if cfg.Response["cache_probe"].ClientTTLMin != 120 || cfg.Response["cache_probe"].ClientTTLMax != 900 {
		t.Fatalf("cache_probe should clamp client ttl, got %+v", cfg.Response["cache_probe"])
	}
	if got := cfg.Response["cache_probe"].ExcludeIPs; !sameStringSlice(got, defaultRealCacheExcludeIPs()) {
		t.Fatalf("cache_probe should exclude fakeip/sinkhole ranges, got %+v", got)
	}
	if totalSize > 1200000 {
		t.Fatalf("default cache total size is too large: %d", totalSize)
	}
	if totalL1Cap > 25000 {
		t.Fatalf("default cache total l1 cap is too large: %d", totalL1Cap)
	}
}

func TestRepoCachePoliciesTemplateUsesMainPersistentBranchShortTermProfile(t *testing.T) {
	baseDir := filepath.Join("..", "config")
	cfg, ok, err := LoadCachePolicyConfigFromSubConfigForBaseDir(baseDir)
	if err != nil {
		t.Fatalf("LoadCachePolicyConfigFromSubConfigForBaseDir: %v", err)
	}
	if !ok {
		t.Fatal("expected repo cache policy template to exist")
	}

	totalSize := 0
	totalL1Cap := 0
	for _, policy := range cfg.Response {
		totalSize += policy.Size
		totalL1Cap += policy.L1TotalCap
	}

	if cfg.Response["cache_main"].Size != defaultCacheMainSize {
		t.Fatalf("template cache_main size = %d, want %d", cfg.Response["cache_main"].Size, defaultCacheMainSize)
	}
	if cfg.Response["cache_branch_domestic"].Persist || cfg.Response["cache_branch_foreign"].Persist {
		t.Fatal("template branch real caches should be non-persistent")
	}
	if cfg.Response["cache_main"].ClientTTLMin != 120 || cfg.Response["cache_main"].ClientTTLMax != 900 {
		t.Fatalf("template cache_main should clamp client ttl 120-900, got %+v", cfg.Response["cache_main"])
	}
	if cfg.Response["cache_main"].ServfailTTL != 1 || cfg.Response["cache_main"].ColdQueryWaitMs != 80 {
		t.Fatalf("template cache_main should use short servfail ttl and bounded cold query wait, got %+v", cfg.Response["cache_main"])
	}
	if got := cfg.Response["cache_main"].BypassDomainSets; len(got) != 1 || got[0] != "DDNS域名" {
		t.Fatalf("template cache_main should bypass only DDNS domain set, got %+v", got)
	}
	assertHighChurnTTLPolicy(t, cfg.Response["cache_main"].DomainSetTTL, 43200, 43200)
	assertHighChurnTTLPolicy(t, cfg.UDPFastPath.DomainSetTTL, 43200, 43200)
	if got := cfg.Response["cache_main"].ExcludeIPs; !sameStringSlice(got, defaultRealCacheExcludeIPs()) {
		t.Fatalf("template cache_main should exclude fakeip/sinkhole ranges, got %+v", got)
	}
	if got := cfg.Response["cache_fakeip_proxy"].ExcludeIPs; len(got) != 0 {
		t.Fatalf("template fakeip proxy cache should not exclude fakeip ranges, got %+v", got)
	}
	if cfg.Response["cache_fakeip_proxy"].LazyCacheTTL != 14400 || cfg.Response["cache_fakeip_proxy"].ClientTTLMin != 600 || cfg.Response["cache_fakeip_proxy"].ClientTTLMax != 600 {
		t.Fatalf("template fakeip proxy cache should use stable client ttl, got %+v", cfg.Response["cache_fakeip_proxy"])
	}
	if totalSize > 1200000 {
		t.Fatalf("template cache total size is too large: %d", totalSize)
	}
	if totalL1Cap > 25000 {
		t.Fatalf("template cache total l1 cap is too large: %d", totalL1Cap)
	}
}

func TestRepoHighChurnTemplateUsesPreciseVideoCDNSeeds(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "config", "rule", "high_churnlist.txt"))
	if err != nil {
		t.Fatalf("read high churn template: %v", err)
	}
	body := string(data)
	for _, rule := range []string{
		"domain:douyinvod.com",
		"domain:huoshanvideo.net",
		"regexp:^v[0-9]+-dy-.*\\.zjcdn\\.com$",
		"domain:bilivideo.com",
		"domain:yximgs.com",
		"full:mpvideo.qpic.cn",
		"full:finder.video.qq.com",
		"domain:steamcontent.com",
		"keyword:httpdns",
	} {
		if !strings.Contains(body, rule+"\n") {
			t.Fatalf("high churn template missing precise seed %q", rule)
		}
	}
	for _, broadRule := range []string{
		"domain:bilibili.com",
		"domain:hdslb.com",
		"domain:biliimg.com",
		"domain:biliapi.net",
		"domain:kuaishou.com",
		"domain:gifshow.com",
		"domain:kwimgs.com",
		"domain:xiaohongshu.com",
		"domain:xhscdn.com",
		"domain:weixin.qq.com",
		"domain:qpic.cn",
		"domain:tc.qq.com",
		"domain:bytedance.com",
		"domain:douyin.com",
		"domain:iqiyi.com",
		"domain:xmcdn.com",
		"domain:snssdk.com",
		"domain:byteimg.com",
		"keyword:pcdn",
		"keyword:p2p",
		"keyword:stun",
	} {
		if strings.Contains(body, broadRule+"\n") {
			t.Fatalf("high churn template must not include broad/stable rule %q", broadRule)
		}
	}
}

func TestRepoCacheUpstreamTemplateUsesCompatibleCacheArgs(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "21-data-cache-upstreams.yaml"))
	if err != nil {
		t.Fatalf("read cache upstream template: %v", err)
	}
	body := string(data)
	if strings.Contains(body, "exit_on_hit") {
		t.Fatalf("repo cache upstream template must not require exit_on_hit; target upgrade packages reject that cache arg")
	}
	if strings.Contains(body, "default_upstream_query_timeout") {
		t.Fatalf("repo cache upstream template must not use legacy aliapi default_upstream_query_timeout")
	}
	if strings.Contains(body, "failure_suppress_ttl: 5") || strings.Contains(body, "persistent_servfail_ttl: 15") {
		t.Fatalf("repo cache upstream template should keep transient SERVFAIL suppression short")
	}
}

func TestRepoFakeIPAndProbeCacheHitsExitWithoutExitOnHitArg(t *testing.T) {
	mainResolution, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "31-main-resolution.yaml"))
	if err != nil {
		t.Fatalf("read main resolution template: %v", err)
	}
	mainResolutionBody := string(mainResolution)
	for _, marker := range []string{
		"exec: $cache_main\n      - matches: has_resp\n        exec: exit",
		"exec: $cache_branch_domestic\n      - matches: has_resp\n        exec: exit",
		"exec: $cache_branch_foreign\n      - matches: has_resp\n        exec: exit",
		"exec: $cache_fakeip_domestic\n      - matches: has_resp\n        exec: exit",
		"exec: $cache_fakeip_proxy\n      - matches: has_resp\n        exec: exit",
		"exec: $sequence_fakeip_generated\n      - matches: has_resp\n        exec: exit",
		"exec: $sequence_fakeip_generated_addlist\n      - matches: has_resp\n        exec: exit",
	} {
		if !strings.Contains(mainResolutionBody, marker) {
			t.Fatalf("main resolution template missing cache hit exit marker %q", marker)
		}
	}

	mainIPv4V6, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "33-main-ipv4v6.yaml"))
	if err != nil {
		t.Fatalf("read ipv4/v6 template: %v", err)
	}
	if !strings.Contains(string(mainIPv4V6), "exec: $cache_probe\n      - matches: has_resp\n        exec: exit") {
		t.Fatalf("ipv4/v6 template missing probe cache hit exit marker")
	}
}

func TestRepoRealResolutionTemplatesTagCacheRouteBeforeMainCache(t *testing.T) {
	mainResolution, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "31-main-resolution.yaml"))
	if err != nil {
		t.Fatalf("read main resolution template: %v", err)
	}
	mainBody := string(mainResolution)
	assertSequenceOrder(t, mainBody, "sequence_local",
		"exec: cache_route_tag chain:domestic-real",
		"exec: $cache_main",
	)
	assertSequenceOrder(t, mainBody, "sequence_google",
		"exec: cache_route_tag chain:foreign-real",
		"exec: $cache_main",
	)
	assertSequenceOrder(t, mainBody, "sequence_google_node",
		"exec: cache_route_tag chain:foreign-real",
		"exec: $cache_main",
	)

	refreshResolution, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "40-refresh-resolution.yaml"))
	if err != nil {
		t.Fatalf("read refresh resolution template: %v", err)
	}
	refreshBody := string(refreshResolution)
	for _, sequenceName := range []string{"sequence_local_refresh", "sequence_local_exit_refresh"} {
		assertSequenceOrder(t, refreshBody, sequenceName,
			"exec: cache_route_tag chain:domestic-real",
			"exec: $domestic",
		)
	}
	for _, sequenceName := range []string{"sequence_google_refresh", "sequence_google_node_refresh"} {
		assertSequenceOrder(t, refreshBody, sequenceName,
			"exec: cache_route_tag chain:foreign-real",
			"exec: $foreign",
		)
	}
}

func TestRepoFakeIPTemplatesTagCacheRouteBeforeFakeIPCache(t *testing.T) {
	mainResolution, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "31-main-resolution.yaml"))
	if err != nil {
		t.Fatalf("read main resolution template: %v", err)
	}
	mainBody := string(mainResolution)
	assertSequenceOrder(t, mainBody, "sequence_local_fake_generated",
		"exec: cache_route_tag chain:domestic-fakeip",
		"exec: $cache_fakeip_domestic",
	)
	for _, sequenceName := range []string{"sequence_fakeip_generated", "sequence_fakeip_generated_addlist"} {
		assertSequenceOrder(t, mainBody, sequenceName,
			"exec: cache_route_tag chain:proxy-fakeip",
			"exec: $cache_fakeip_proxy",
		)
	}

	refreshResolution, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "40-refresh-resolution.yaml"))
	if err != nil {
		t.Fatalf("read refresh resolution template: %v", err)
	}
	refreshBody := string(refreshResolution)
	for _, sequenceName := range []string{"sequence_local_fake_refresh", "sequence_local_fake_exit_refresh"} {
		assertSequenceOrder(t, refreshBody, sequenceName,
			"exec: cache_route_tag chain:domestic-fakeip",
			"exec: $cnfake",
		)
	}
	for _, sequenceName := range []string{"sequence_fakeip_refresh", "sequence_fakeip_addlist_refresh"} {
		assertSequenceOrder(t, refreshBody, sequenceName,
			"exec: cache_route_tag chain:proxy-fakeip",
			"exec: $nocnfake",
		)
	}
}

func TestRepoKnownSubscriptionDirectAlwaysUsesRealIP(t *testing.T) {
	mainIPv4V6, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "33-main-ipv4v6.yaml"))
	if err != nil {
		t.Fatalf("read ipv4/v6 template: %v", err)
	}
	mainBody := string(mainIPv4V6)
	for _, sequenceName := range []string{"sequence_ipv4_known_domain", "sequence_ipv6_known_domain"} {
		assertSequenceOrder(t, mainBody, sequenceName,
			"matches: fast_mark 7\n        exec:\n          - $sequence_fakeip\n          - exit",
			"matches: fast_mark 13\n        exec: $sequence_local",
			"matches: fast_mark 13\n        exec: exit",
			"matches: fast_mark 15\n        exec:\n          - $sequence_fakeip_addlist\n          - exit",
			"matches: fast_mark 30\n        exec: $sequence_local",
			"matches: fast_mark 30\n        exec: exit",
			"matches: fast_mark 31\n        exec:\n          - $sequence_fakeip\n          - exit",
			"matches:\n          - fast_mark 14\n          - '!fast_mark 13'\n          - '!fast_mark 30'\n        exec:\n          - $sequence_fakeip_addlist\n          - exit",
			"matches: fast_mark 16\n        exec: $sequence_local",
			"matches: fast_mark 16\n        exec: exit",
			"matches:\n          - fast_mark 12\n          - '!fast_mark 13'\n          - '!fast_mark 16'\n          - '!fast_mark 30'\n          - '!fast_mark 31'\n        exec:\n          - $sequence_fakeip\n          - exit",
			"matches:\n          - fast_mark 11\n          - '!fast_mark 30'\n          - '!fast_mark 31'\n        exec: $sequence_local_divert",
		)
		if strings.Contains(mainBody, "fast_mark 13\n          - switch 'cn_answer_mode:fakeip'") ||
			strings.Contains(mainBody, "fast_mark 16\n          - switch 'cn_answer_mode:fakeip'") {
			t.Fatalf("%s should not send subscription direct marks through domestic fakeip mode", sequenceName)
		}
	}

	refreshIPv4V6, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "42-refresh-ipv4v6.yaml"))
	if err != nil {
		t.Fatalf("read refresh ipv4/v6 template: %v", err)
	}
	refreshBody := string(refreshIPv4V6)
	for _, sequenceName := range []string{"sequence_ipv4_known_domain_refresh", "sequence_ipv6_known_domain_refresh"} {
		assertSequenceOrder(t, refreshBody, sequenceName,
			"matches: fast_mark 7\n        exec:\n          - $sequence_fakeip_refresh\n          - exit",
			"matches: fast_mark 13\n        exec: $sequence_local_refresh",
			"matches: fast_mark 13\n        exec: exit",
			"matches: fast_mark 15\n        exec:\n          - $sequence_fakeip_addlist_refresh\n          - exit",
			"matches: fast_mark 30\n        exec: $sequence_local_refresh",
			"matches: fast_mark 30\n        exec: exit",
			"matches: fast_mark 31\n        exec:\n          - $sequence_fakeip_refresh\n          - exit",
			"matches:\n          - fast_mark 14\n          - '!fast_mark 13'\n          - '!fast_mark 30'\n        exec:\n          - $sequence_fakeip_addlist_refresh\n          - exit",
			"matches: fast_mark 16\n        exec: $sequence_local_refresh",
			"matches: fast_mark 16\n        exec: exit",
			"matches:\n          - fast_mark 12\n          - '!fast_mark 13'\n          - '!fast_mark 16'\n          - '!fast_mark 30'\n          - '!fast_mark 31'\n        exec:\n          - $sequence_fakeip_refresh\n          - exit",
			"matches:\n          - fast_mark 11\n          - '!fast_mark 30'\n          - '!fast_mark 31'\n        exec: $sequence_local_divert_refresh",
		)
		if strings.Contains(refreshBody, "fast_mark 13\n          - switch 'cn_answer_mode:fakeip'") ||
			strings.Contains(refreshBody, "fast_mark 16\n          - switch 'cn_answer_mode:fakeip'") {
			t.Fatalf("%s should not send subscription direct marks through domestic fakeip mode", sequenceName)
		}
	}
}

func TestRepoNoLeakNXDomainExitsBeforeForeignFallback(t *testing.T) {
	mainNotInList, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "32-main-not-in-list.yaml"))
	if err != nil {
		t.Fatalf("read main not-in-list template: %v", err)
	}
	mainBody := string(mainNotInList)
	assertSequenceOrder(t, mainBody, "sequence_not_in_list_noleak_v4",
		"exec: $sequence_google_node_resilient",
		"matches: rcode 3",
		"exec:\n          - $my_nov4list\n          - exit",
		"matches: '!resp_ip 0.0.0.0/0'\n        exec: $sequence_google",
	)
	assertSequenceOrder(t, mainBody, "sequence_not_in_list_noleak_v6",
		"exec: $sequence_google_node_resilient",
		"matches: rcode 3",
		"exec:\n          - $my_nov6list\n          - exit",
		"matches: '!resp_ip 2000::/3'\n        exec: $sequence_google",
	)

	refreshNotInList, err := os.ReadFile(filepath.Join("..", "config", "sub_config", "41-refresh-not-in-list.yaml"))
	if err != nil {
		t.Fatalf("read refresh not-in-list template: %v", err)
	}
	refreshBody := string(refreshNotInList)
	assertSequenceOrder(t, refreshBody, "sequence_not_in_list_noleak_v4_refresh",
		"exec: $sequence_google_node_refresh_resilient",
		"matches: rcode 3",
		"exec:\n          - $my_nov4list\n          - exit",
		"matches: '!resp_ip 0.0.0.0/0'\n        exec: $sequence_google_refresh",
	)
	assertSequenceOrder(t, refreshBody, "sequence_not_in_list_noleak_v6_refresh",
		"exec: $sequence_google_node_refresh_resilient",
		"matches: rcode 3",
		"exec:\n          - $my_nov6list\n          - exit",
		"matches: '!resp_ip 2000::/3'\n        exec: $sequence_google_refresh",
	)
}

func assertSequenceOrder(t *testing.T, body, sequenceName string, markers ...string) {
	t.Helper()

	sequenceStart := strings.Index(body, "- name: "+sequenceName+"\n")
	if sequenceStart < 0 {
		t.Fatalf("template missing sequence %s", sequenceName)
	}
	sequenceBody := body[sequenceStart:]
	if nextSequenceRel := strings.Index(sequenceBody[1:], "\n  - name: "); nextSequenceRel >= 0 {
		sequenceBody = sequenceBody[:nextSequenceRel+1]
	}

	lastIndex := -1
	for _, marker := range markers {
		index := strings.Index(sequenceBody, marker)
		if index < 0 {
			t.Fatalf("sequence %s missing marker %q", sequenceName, marker)
		}
		if index <= lastIndex {
			t.Fatalf("sequence %s marker %q appears out of order", sequenceName, marker)
		}
		lastIndex = index
	}
}

func TestRepoBattleNetRulesUseDomesticDirectAndForeignFakeIP(t *testing.T) {
	whitelist, err := os.ReadFile(filepath.Join("..", "config", "rule", "whitelist.txt"))
	if err != nil {
		t.Fatalf("read whitelist template: %v", err)
	}
	whitelistBody := string(whitelist)
	for _, rule := range []string{
		"domain:battlenet.com.cn",
		"domain:blizzard.cn",
		"domain:blzstatic.cn",
		"full:cn.battle.net",
	} {
		if !strings.Contains(whitelistBody, rule) {
			t.Fatalf("whitelist template missing domestic Battle.net rule %q", rule)
		}
	}

	greylist, err := os.ReadFile(filepath.Join("..", "config", "rule", "greylist.txt"))
	if err != nil {
		t.Fatalf("read greylist template: %v", err)
	}
	greylistBody := string(greylist)
	for _, rule := range []string{
		"domain:blizzard.com",
		"domain:akamaized.net",
		"domain:cloudfront.net",
		"full:geo.battle.net",
		"full:rum.battle.net",
	} {
		if !strings.Contains(greylistBody, rule) {
			t.Fatalf("greylist template missing foreign Battle.net rule %q", rule)
		}
	}

	for _, rule := range []string{"cloudfront.net", "akamaized.net"} {
		if strings.Contains(whitelistBody, rule) {
			t.Fatalf("whitelist template should not contain foreign CDN rule %q", rule)
		}
	}
	for _, rule := range []string{"battlenet.com.cn", "blzstatic.cn"} {
		if strings.Contains(greylistBody, rule) {
			t.Fatalf("greylist template should not contain domestic Battle.net rule %q", rule)
		}
	}
}

func TestRepoWhitelistKeepsGlobalBrandDirectPriorityGaps(t *testing.T) {
	whitelist, err := os.ReadFile(filepath.Join("..", "config", "rule", "whitelist.txt"))
	if err != nil {
		t.Fatalf("read whitelist template: %v", err)
	}
	whitelistBody := string(whitelist)

	for _, rule := range []string{
		"domain:logi.com",
		"domain:logitech.com",
		"domain:dell.com",
		"domain:hp.com",
		"domain:intel.com",
		"domain:amd.com",
		"domain:nvidia.com",
		"domain:adobe.com",
		"domain:oracle.com",
		"domain:sony.com",
	} {
		if !strings.Contains(whitelistBody, rule) {
			t.Fatalf("whitelist template missing global brand direct-priority rule %q", rule)
		}
	}

	for _, rule := range []string{
		"domain:lenovo.com",
		"domain:cloudflare-cn.com",
		"domain:amazonaws.cn",
		"full:time.amazonaws.cn",
	} {
		if strings.Contains(whitelistBody, rule) {
			t.Fatalf("whitelist template duplicates CN source covered rule %q", rule)
		}
	}
}

func TestRepoRequeryUIDefaultsMatchSchedulerDefaults(t *testing.T) {
	indexHTML, err := os.ReadFile(filepath.Join("..", "config", "ui", "index.html"))
	if err != nil {
		t.Fatalf("read index html: %v", err)
	}
	indexBody := string(indexHTML)
	for _, marker := range []string{
		`id="requery-full-qps-input" min="1" value="10"`,
		`id="requery-quick-qps-input" min="1" value="15"`,
		`id="requery-prewarm-qps-input" min="1" value="20"`,
	} {
		if !strings.Contains(indexBody, marker) {
			t.Fatalf("index html missing scheduler default marker %q", marker)
		}
	}

	logJS, err := os.ReadFile(filepath.Join("..", "config", "ui", "assets", "js", "log.js"))
	if err != nil {
		t.Fatalf("read log js: %v", err)
	}
	logBody := string(logJS)
	for _, marker := range []string{
		"REQUERY_FULL_QPS_DEFAULT: 10",
		"REQUERY_QUICK_QPS_DEFAULT: 15",
		"REQUERY_PREWARM_QPS_DEFAULT: 20",
	} {
		if !strings.Contains(logBody, marker) {
			t.Fatalf("log js missing scheduler default marker %q", marker)
		}
	}
}

func TestLoadCachePolicyConfigFromSubConfigOverride(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	path := filepath.Join(MainConfigBaseDir, cachePoliciesConfigRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	const body = `
response:
  cache_main:
    size: 2048
    lazy_cache_ttl: 120
    lazy_stale_ttl: 30
    client_ttl_min: 10
    client_ttl_max: 60
    bypass_domain_sets:
      - 高变CDN
      - DDNS域名
    exclude_ip: 10.0.0.0/8, 192.168.0.0/16
    persist: false
udp_fast_path:
  internal_ttl: 3
  stale_retry_seconds: 9
  stale_max_seconds: 33
  ttl_min: 1
  ttl_max: 3
  bypass_domain_sets:
    - 高变CDN
    - DDNS域名
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, ok, err := LoadCachePolicyConfigFromSubConfig()
	if err != nil {
		t.Fatalf("LoadCachePolicyConfigFromSubConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected cache policy file to exist")
	}
	if cfg.Response["cache_main"].Size != 2048 || cfg.Response["cache_main"].Persist {
		t.Fatalf("unexpected cache_main policy: %+v", cfg.Response["cache_main"])
	}
	if cfg.Response["cache_main"].LazyCacheTTL != 120 || cfg.Response["cache_main"].LazyStaleTTL != 30 {
		t.Fatalf("unexpected cache_main lazy ttl split: %+v", cfg.Response["cache_main"])
	}
	if cfg.Response["cache_main"].ClientTTLMin != 10 || cfg.Response["cache_main"].ClientTTLMax != 60 {
		t.Fatalf("unexpected cache_main client ttl clamp: %+v", cfg.Response["cache_main"])
	}
	if got := cfg.Response["cache_main"].BypassDomainSets; len(got) != 2 || got[0] != "DDNS域名" || got[1] != "高变CDN" {
		t.Fatalf("unexpected cache_main bypass domain sets: %+v", got)
	}
	if got := cfg.Response["cache_main"].ExcludeIPs; len(got) != 2 || got[0] != "10.0.0.0/8" || got[1] != "192.168.0.0/16" {
		t.Fatalf("unexpected cache_main exclude ip list: %+v", got)
	}
	if cfg.UDPFastPath.InternalTTL != 3 || cfg.UDPFastPath.StaleRetry != 9 || cfg.UDPFastPath.StaleMax != 33 || cfg.UDPFastPath.TTLMax != 3 {
		t.Fatalf("unexpected udp fast policy: %+v", cfg.UDPFastPath)
	}
	if got := cfg.UDPFastPath.BypassDomainSets; len(got) != 2 || got[0] != "DDNS域名" || got[1] != "高变CDN" {
		t.Fatalf("unexpected udp fast bypass domain sets: %+v", got)
	}
}

func TestLoadCachePolicyConfigFromSubConfigIgnoresUnknownPolicy(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	path := filepath.Join(MainConfigBaseDir, cachePoliciesConfigRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	const body = `
response:
  cache_main:
    size: 4096
  cache_legacy_removed:
    size: 1
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, ok, err := LoadCachePolicyConfigFromSubConfig()
	if err != nil {
		t.Fatalf("LoadCachePolicyConfigFromSubConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected cache policy file to exist")
	}
	if cfg.Response["cache_main"].Size != 4096 {
		t.Fatalf("expected cache_main override to survive, got %+v", cfg.Response["cache_main"])
	}
	if _, exists := cfg.Response["cache_legacy_removed"]; exists {
		t.Fatalf("expected unknown legacy cache policy to be ignored, got %+v", cfg.Response)
	}
}

func TestLoadCachePolicyConfigFromSubConfigLegacyLazyTTL(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	path := filepath.Join(MainConfigBaseDir, cachePoliciesConfigRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	const body = `
response:
  cache_main:
    lazy_cache_ttl: 120
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, ok, err := LoadCachePolicyConfigFromSubConfig()
	if err != nil {
		t.Fatalf("LoadCachePolicyConfigFromSubConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected cache policy file to exist")
	}
	if cfg.Response["cache_main"].LazyCacheTTL != 120 || cfg.Response["cache_main"].LazyStaleTTL != 120 {
		t.Fatalf("expected legacy lazy_cache_ttl to also set lazy_stale_ttl, got %+v", cfg.Response["cache_main"])
	}
}

func TestApplyRuntimeCachePolicy(t *testing.T) {
	cfg := defaultCachePolicyConfig()
	cfg.Response["cache_main"] = CachePolicy{
		Size: 123, LazyCacheTTL: 45, LazyStaleTTL: 30, ClientTTLMin: 3, ClientTTLMax: 30, NXDomainTTL: 11, ServfailTTL: 12, ColdQueryWaitMs: 34,
		DomainSetTTL: []DomainSetTTLPolicy{{
			Sets:         []string{"高变化域名"},
			LazyCacheTTL: 43200,
			LazyStaleTTL: 43200,
		}},
		L1Enabled: true, L1TotalCap: 22, BypassDomainSets: []string{"DDNS域名"}, ExcludeIPs: []string{"28.0.0.0/8", "f2b0::/18"}, Persist: true,
		DumpFile: "db/cache/custom.dump", DumpInterval: 99, WALSyncInterval: 7,
	}
	cfg.UDPFastPath = UDPFastCachePolicy{
		InternalTTL:    9,
		StaleRetry:     12,
		StaleMax:       60,
		TTLMin:         2,
		TTLMax:         4,
		MemoryBudgetMB: 6,
		ResponseSlots:  1024,
		RuleSlots:      512,
		DomainSetTTL: []DomainSetTTLPolicy{{
			Sets:         []string{"高变化域名"},
			LazyCacheTTL: 43200,
			LazyStaleTTL: 43200,
		}},
		BypassDomainSets: []string{"DDNS域名"},
	}

	pc := PluginConfig{Tag: "cache_main", Type: "cache", Args: map[string]any{"size": 1}}
	if err := ApplyRuntimeCachePolicy(&pc, cfg); err != nil {
		t.Fatalf("ApplyRuntimeCachePolicy(cache): %v", err)
	}
	args := pc.Args.(map[string]any)
	if args["size"] != 123 || args["dump_file"] != "db/cache/custom.dump" || args["lazy_stale_ttl"] != 30 || args["client_ttl_min"] != uint32(3) || args["client_ttl_max"] != uint32(30) || args["cold_query_wait_ms"] != 34 {
		t.Fatalf("unexpected cache args: %+v", args)
	}
	bypassDomainSets, ok := args["bypass_domain_sets"].([]string)
	if !ok || len(bypassDomainSets) != 1 || bypassDomainSets[0] != "DDNS域名" {
		t.Fatalf("unexpected bypass domain sets: %+v", args["bypass_domain_sets"])
	}
	domainSetTTL, ok := args["domain_set_ttl"].([]map[string]any)
	if !ok || len(domainSetTTL) != 1 || domainSetTTL[0]["lazy_cache_ttl"] != 43200 || domainSetTTL[0]["lazy_stale_ttl"] != 43200 {
		t.Fatalf("unexpected cache domain set ttl: %+v", args["domain_set_ttl"])
	}
	excludeIPs, ok := args["exclude_ip"].([]string)
	if !ok || len(excludeIPs) != 2 || excludeIPs[0] != "28.0.0.0/8" || excludeIPs[1] != "f2b0::/18" {
		t.Fatalf("unexpected exclude ip list: %+v", args["exclude_ip"])
	}

	udp := PluginConfig{Tag: "udp_main", Type: "udp_server", Args: map[string]any{}}
	if err := ApplyRuntimeCachePolicy(&udp, cfg); err != nil {
		t.Fatalf("ApplyRuntimeCachePolicy(udp): %v", err)
	}
	udpArgs := udp.Args.(map[string]any)
	if udpArgs["fast_cache_internal_ttl"] != 9 || udpArgs["fast_cache_stale_retry_seconds"] != 12 || udpArgs["fast_cache_stale_max_seconds"] != 60 || udpArgs["fast_cache_ttl_max"] != uint32(4) {
		t.Fatalf("unexpected udp args: %+v", udpArgs)
	}
	if udpArgs["fast_cache_memory_budget_mb"] != 6 || udpArgs["fast_cache_slots"] != 1024 || udpArgs["fast_rule_cache_slots"] != 512 {
		t.Fatalf("unexpected udp slot args: %+v", udpArgs)
	}
	udpBypassDomainSets, ok := udpArgs["fast_cache_bypass_domain_sets"].([]string)
	if !ok || len(udpBypassDomainSets) != 1 || udpBypassDomainSets[0] != "DDNS域名" {
		t.Fatalf("unexpected udp bypass domain sets: %+v", udpArgs["fast_cache_bypass_domain_sets"])
	}
	udpDomainSetTTL, ok := udpArgs["fast_cache_domain_set_ttl"].([]map[string]any)
	if !ok || len(udpDomainSetTTL) != 1 || udpDomainSetTTL[0]["internal_ttl"] != 43200 || udpDomainSetTTL[0]["stale_max_seconds"] != 43200 {
		t.Fatalf("unexpected udp domain set ttl: %+v", udpArgs["fast_cache_domain_set_ttl"])
	}
}

func assertHighChurnTTLPolicy(t *testing.T, got []DomainSetTTLPolicy, wantCacheTTL, wantStaleTTL int) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("expected one high-churn ttl policy, got %+v", got)
	}
	if !sameStringSlice(got[0].Sets, []string{"高变化域名"}) {
		t.Fatalf("expected high-churn ttl policy sets, got %+v", got[0].Sets)
	}
	if got[0].LazyCacheTTL != wantCacheTTL || got[0].LazyStaleTTL != wantStaleTTL {
		t.Fatalf("expected high-churn ttl policy %d/%d, got %+v", wantCacheTTL, wantStaleTTL, got[0])
	}
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
