package coremain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockHotRuleSnapshotProvider struct {
	rules []string
}

func (m *mockHotRuleSnapshotProvider) SnapshotHotRules() ([]string, error) {
	return append([]string(nil), m.rules...), nil
}

func TestShuntAnalyzerExplain(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `
block_response: "on"
block_query_type: "on"
block_ipv6: "off"
ad_block: "off"
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: whitelist
    type: domain_set_light
    args:
      files:
        - rule/whitelist.txt
  - name: greylist
    type: domain_set_light
    args:
      files:
        - rule/greylist.txt
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: greylist
          mark: 7
          output_tag: 灰名单
        - tag: whitelist
          mark: 8
          output_tag: 白名单
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "rule", "whitelist.txt"), "domain:bing.com\n")
	mustWriteShuntFile(t, filepath.Join(baseDir, "rule", "greylist.txt"), "domain:bing.com\n")

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	result, err := analyzer.Explain("bing.com", "A")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if len(result.Matches) != 2 {
		t.Fatalf("unexpected matches: %+v", result.Matches)
	}
	if result.Decision.Action != "sequence_fakeip" || result.Decision.Matched != 7 {
		t.Fatalf("unexpected decision: %+v", result.Decision)
	}
}

func TestShuntAnalyzerExplainRespectsCoreMode(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `
block_response: "on"
block_query_type: "on"
block_ipv6: "off"
ad_block: "off"
core_mode: "compat"
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules: []
`)

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	result, err := analyzer.Explain("unknown.example", "A")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if result.Decision.Action != "not_in_list_leak_a" {
		t.Fatalf("unexpected compat decision: %+v", result.Decision)
	}

	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `
block_response: "on"
block_query_type: "on"
block_ipv6: "off"
ad_block: "off"
core_mode: "secure"
`)
	analyzer, err = newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer secure: %v", err)
	}
	result, err = analyzer.Explain("unknown.example", "A")
	if err != nil {
		t.Fatalf("Explain secure: %v", err)
	}
	if result.Decision.Action != "not_in_list_noleak_a" {
		t.Fatalf("unexpected secure decision: %+v", result.Decision)
	}
}

func TestShuntAnalyzerConflicts(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: whitelist
    type: domain_set_light
    args:
      files:
        - rule/whitelist.txt
  - name: greylist
    type: domain_set_light
    args:
      files:
        - rule/greylist.txt
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: greylist
          mark: 7
          output_tag: 灰名单
        - tag: whitelist
          mark: 8
          output_tag: 白名单
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "rule", "whitelist.txt"), "domain:bing.com\n")
	mustWriteShuntFile(t, filepath.Join(baseDir, "rule", "greylist.txt"), "domain:bing.com\n")

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	conflicts := analyzer.Conflicts()
	if len(conflicts) != 1 || conflicts[0].RuleKey != "domain:bing.com" {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
}

func TestShuntAnalyzerUsesGeneratedSources(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: my_fakeiprule
    type: domain_set_light
    args:
      generated_from: my_fakeiplist
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: my_fakeiprule
          mark: 12
          output_tag: 记忆代理
`)
	if err := SaveDomainPoolStateToPath(runtimeStateDBPathForBaseDir(baseDir), DomainPoolState{
		Meta: DomainPoolMeta{
			PoolTag:              "my_fakeiplist",
			PoolKind:             DomainPoolKindMemory,
			MemoryID:             "fakeip",
			Policy:               DefaultDomainPoolPolicy("my_fakeiplist"),
			DomainCount:          1,
			PromotedDomainCount:  1,
			PublishedDomainCount: 1,
		},
		Domains: []DomainPoolDomain{{
			PoolTag:    "my_fakeiplist",
			Domain:     "proxy.example",
			TotalCount: 3,
			Score:      3,
			Promoted:   true,
		}},
	}); err != nil {
		t.Fatalf("SaveDomainPoolStateToPath: %v", err)
	}

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	result, err := analyzer.Explain("proxy.example", "A")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if result.Decision.Matched != 12 || result.Decision.Action != "sequence_fakeip" {
		t.Fatalf("unexpected decision: %+v", result.Decision)
	}
}

func TestShuntAnalyzerUsesLiveHotRulesFromManager(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: my_fakeiprule
    type: domain_set_light
    args:
      generated_from: my_fakeiplist
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: my_fakeiprule
          mark: 12
          output_tag: 记忆代理
`)
	m := NewTestMosdnsWithPlugins(map[string]any{
		"my_fakeiplist": &mockHotRuleSnapshotProvider{rules: []string{"full:live-proxy.example"}},
	})

	analyzer, err := newShuntAnalyzerWithManager(baseDir, m)
	if err != nil {
		t.Fatalf("newShuntAnalyzerWithManager: %v", err)
	}
	result, err := analyzer.Explain("live-proxy.example", "A")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if result.Decision.Matched != 12 || result.Decision.Action != "sequence_fakeip" {
		t.Fatalf("unexpected live decision: %+v matches=%+v", result.Decision, result.Matches)
	}
	if len(result.Matches) != 1 || len(result.Matches[0].SourceFiles) != 1 || !strings.Contains(result.Matches[0].SourceFiles[0], "live://domain_pool_hot/") {
		t.Fatalf("expected live source ref in matches, got %+v", result.Matches)
	}
}

func TestShuntAnalyzerUsesBoundDiversionSourcesFromBaseDir(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: geosite_no_cn
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: geosite_no_cn
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: geosite_no_cn
          mark: 14
          output_tag: 国外分流
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "diversion_sources.yaml"), `
sources:
  - id: remote_geosite
    name: geosite remote
    bind_to: geosite_no_cn
    enabled: true
    behavior: domain
    match_mode: domain_set
    format: list
    source_kind: local
    path: diversion/geosite-no-cn.list
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "diversion", "geosite-no-cn.list"), "proxy.example\n")

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	result, err := analyzer.Explain("proxy.example", "A")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if result.Decision.Matched != 14 || result.Decision.Action != "sequence_fakeip_addlist" {
		t.Fatalf("unexpected decision: %+v matches=%+v", result.Decision, result.Matches)
	}
}

func TestShuntAnalyzerPrefersCuscnSupplementOverGeositeNoCn(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: cuscn
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: cuscn
  - name: geosite_no_cn
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: geosite_no_cn
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: cuscn
          mark: 13
          output_tag: 直连补充
        - tag: geosite_no_cn
          mark: 14
          output_tag: 国外分流
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "diversion_sources.yaml"), `
sources:
  - id: cuscn_local
    name: cuscn local
    bind_to: cuscn
    enabled: true
    behavior: domain
    match_mode: domain_set
    format: list
    source_kind: local
    path: diversion/cuscn.list
  - id: geosite_no_cn
    name: geosite no cn
    bind_to: geosite_no_cn
    enabled: true
    behavior: domain
    match_mode: domain_set
    format: list
    source_kind: local
    path: diversion/geosite-no-cn.list
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "diversion", "cuscn.list"), "domain:logitech.com\n")
	mustWriteShuntFile(t, filepath.Join(baseDir, "diversion", "geosite-no-cn.list"), "domain:logitech.com\n")

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	result, err := analyzer.Explain("www.logitech.com", "A")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if result.Decision.Matched != 13 || result.Decision.Action != "sequence_local" {
		t.Fatalf("unexpected decision: %+v matches=%+v", result.Decision, result.Matches)
	}
}

func TestShuntAnalyzerPrefersCusnocnSupplementOverGeositeCn(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: cusnocn
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: cusnocn
  - name: geosite_cn
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: geosite_cn
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: cusnocn
          mark: 15
          output_tag: 代理补充
        - tag: geosite_cn
          mark: 16
          output_tag: 国内分流
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "diversion_sources.yaml"), `
sources:
  - id: cusnocn_local
    name: cusnocn local
    bind_to: cusnocn
    enabled: true
    behavior: domain
    match_mode: domain_set
    format: list
    source_kind: local
    path: diversion/cusnocn.list
  - id: geosite_cn
    name: geosite cn
    bind_to: geosite_cn
    enabled: true
    behavior: domain
    match_mode: domain_set
    format: list
    source_kind: local
    path: diversion/geosite-cn.list
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "diversion", "cusnocn.list"), "domain:googleapis.cn\n")
	mustWriteShuntFile(t, filepath.Join(baseDir, "diversion", "geosite-cn.list"), "domain:googleapis.cn\n")

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	result, err := analyzer.Explain("translate.googleapis.cn", "A")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if result.Decision.Matched != 15 || result.Decision.Action != "sequence_fakeip_addlist" {
		t.Fatalf("unexpected decision: %+v matches=%+v", result.Decision, result.Matches)
	}
}

func TestShuntAnalyzerAutoConfirmDirectOverridesGeositeAndMemory(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: auto_confirm_direct
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: auto_confirm_direct
  - name: geosite_no_cn
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: geosite_no_cn
  - name: my_fakeiprule
    type: domain_set_light
    args:
      generated_from: my_fakeiplist
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: auto_confirm_direct
          mark: 30
          output_tag: 自动确认直连
        - tag: geosite_no_cn
          mark: 14
          output_tag: 国外分流
        - tag: my_fakeiprule
          mark: 12
          output_tag: 记忆代理
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "diversion_sources.yaml"), `
sources:
  - id: auto_confirm_direct_local
    name: 自动确认直连
    bind_to: auto_confirm_direct
    enabled: true
    behavior: domain
    match_mode: domain_set
    format: list
    source_kind: local
    path: diversion/auto_confirm_direct.list
  - id: geosite_no_cn
    name: geosite no cn
    bind_to: geosite_no_cn
    enabled: true
    behavior: domain
    match_mode: domain_set
    format: list
    source_kind: local
    path: diversion/geosite-no-cn.list
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "diversion", "auto_confirm_direct.list"), "domain:logitech.com\n")
	mustWriteShuntFile(t, filepath.Join(baseDir, "diversion", "geosite-no-cn.list"), "domain:logitech.com\n")
	if err := SaveDomainPoolStateToPath(runtimeStateDBPathForBaseDir(baseDir), DomainPoolState{
		Meta: DomainPoolMeta{
			PoolTag:              "my_fakeiplist",
			PoolKind:             DomainPoolKindMemory,
			MemoryID:             "fakeip",
			Policy:               DefaultDomainPoolPolicy("my_fakeiplist"),
			DomainCount:          1,
			PromotedDomainCount:  1,
			PublishedDomainCount: 1,
		},
		Domains: []DomainPoolDomain{{
			PoolTag:    "my_fakeiplist",
			Domain:     "logitech.com",
			TotalCount: 3,
			Score:      3,
			Promoted:   true,
		}},
	}); err != nil {
		t.Fatalf("SaveDomainPoolStateToPath: %v", err)
	}

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	result, err := analyzer.Explain("www.logitech.com", "A")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if result.Decision.Matched != 30 || result.Decision.Action != "sequence_local" {
		t.Fatalf("unexpected decision: %+v matches=%+v", result.Decision, result.Matches)
	}
}

func TestShuntAnalyzerAutoConfirmProxyOverridesGeositeAndMemory(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: auto_confirm_proxy
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: auto_confirm_proxy
  - name: geosite_cn
    type: sd_set_light
    args:
      config_file: custom_config/diversion_sources.yaml
      bind_to: geosite_cn
  - name: my_realiprule
    type: domain_set_light
    args:
      generated_from: my_realiplist
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: auto_confirm_proxy
          mark: 31
          output_tag: 自动确认代理
        - tag: geosite_cn
          mark: 16
          output_tag: 国内分流
        - tag: my_realiprule
          mark: 11
          output_tag: 记忆直连
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "diversion_sources.yaml"), `
sources:
  - id: auto_confirm_proxy_local
    name: 自动确认代理
    bind_to: auto_confirm_proxy
    enabled: true
    behavior: domain
    match_mode: domain_set
    format: list
    source_kind: local
    path: diversion/auto_confirm_proxy.list
  - id: geosite_cn
    name: geosite cn
    bind_to: geosite_cn
    enabled: true
    behavior: domain
    match_mode: domain_set
    format: list
    source_kind: local
    path: diversion/geosite-cn.list
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "diversion", "auto_confirm_proxy.list"), "domain:example.cn\n")
	mustWriteShuntFile(t, filepath.Join(baseDir, "diversion", "geosite-cn.list"), "domain:example.cn\n")
	if err := SaveDomainPoolStateToPath(runtimeStateDBPathForBaseDir(baseDir), DomainPoolState{
		Meta: DomainPoolMeta{
			PoolTag:              "my_realiplist",
			PoolKind:             DomainPoolKindMemory,
			MemoryID:             "realip",
			Policy:               DefaultDomainPoolPolicy("my_realiplist"),
			DomainCount:          1,
			PromotedDomainCount:  1,
			PublishedDomainCount: 1,
		},
		Domains: []DomainPoolDomain{{
			PoolTag:    "my_realiplist",
			Domain:     "example.cn",
			TotalCount: 3,
			Score:      3,
			Promoted:   true,
		}},
	}); err != nil {
		t.Fatalf("SaveDomainPoolStateToPath: %v", err)
	}

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	result, err := analyzer.Explain("media.example.cn", "A")
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if result.Decision.Matched != 31 || result.Decision.Action != "sequence_fakeip" {
		t.Fatalf("unexpected decision: %+v matches=%+v", result.Decision, result.Matches)
	}
}

func TestRuntimeShuntConflictsCmd(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: whitelist
    type: domain_set_light
    args:
      files:
        - rule/whitelist.txt
  - name: greylist
    type: domain_set_light
    args:
      files:
        - rule/greylist.txt
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: greylist
          mark: 7
          output_tag: 灰名单
        - tag: whitelist
          mark: 8
          output_tag: 白名单
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "rule", "whitelist.txt"), "domain:bing.com\n")
	mustWriteShuntFile(t, filepath.Join(baseDir, "rule", "greylist.txt"), "domain:bing.com\n")

	cmd := newRuntimeShuntCmd()
	buf := new(strings.Builder)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"-d", baseDir, "conflicts", "--limit", "10"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute: %v", err)
	}

	var payload struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal([]byte(buf.String()), &payload); err != nil {
		t.Fatalf("decode output: %v output=%s", err, buf.String())
	}
	if payload.Count != 1 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestRuntimeShuntExplainCmdTable(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteShuntFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `
block_response: "on"
block_query_type: "on"
block_ipv6: "off"
ad_block: "off"
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: whitelist
    type: domain_set_light
    args:
      files:
        - rule/whitelist.txt
  - name: greylist
    type: domain_set_light
    args:
      files:
        - rule/greylist.txt
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: greylist
          mark: 7
          output_tag: 灰名单
        - tag: whitelist
          mark: 8
          output_tag: 白名单
`)
	mustWriteShuntFile(t, filepath.Join(baseDir, "rule", "whitelist.txt"), "domain:bing.com\n")
	mustWriteShuntFile(t, filepath.Join(baseDir, "rule", "greylist.txt"), "domain:bing.com\n")

	cmd := newRuntimeShuntCmd()
	buf := new(strings.Builder)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"-d", baseDir, "explain", "--domain", "bing.com", "--qtype", "A", "--format", "table"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "DECISION_PATH") || !strings.Contains(out, "sequence_fakeip") || !strings.Contains(out, "greylist") {
		t.Fatalf("unexpected table output: %s", out)
	}
}

func mustWriteShuntFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
