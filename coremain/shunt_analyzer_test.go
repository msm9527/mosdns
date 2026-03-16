package coremain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShuntAnalyzerExplain(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `
block_response: "on"
block_query_type: "on"
block_ipv6: "off"
ad_block: "off"
`)
	mustWriteFile(t, filepath.Join(baseDir, "rule", "whitelist.txt"), "domain:bing.com\n")
	mustWriteFile(t, filepath.Join(baseDir, "rule", "greylist.txt"), "domain:bing.com\n")
	mustWriteFile(t, filepath.Join(baseDir, "sub_config", "rule_set.yaml"), `
plugins:
  - tag: whitelist
    type: domain_set_light
    args:
      files:
        - "rule/whitelist.txt"
  - tag: greylist
    type: domain_set_light
    args:
      files:
        - "rule/greylist.txt"
  - tag: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: "未命中"
      rules:
        - tag: greylist
          mark: 7
          output_tag: "灰名单"
        - tag: whitelist
          mark: 8
          output_tag: "白名单"
`)

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
	if len(result.DecisionPath) == 0 {
		t.Fatalf("expected decision path")
	}
	if result.DecisionPath[len(result.DecisionPath)-1].DecisionHit && result.DecisionPath[len(result.DecisionPath)-1].Action == result.Decision.Action {
		// okay for fallback case
	} else {
		foundWinner := false
		for _, step := range result.DecisionPath {
			if step.DecisionHit && step.Action == result.Decision.Action {
				foundWinner = true
				break
			}
		}
		if !foundWinner {
			t.Fatalf("expected winning step in decision path: %+v", result.DecisionPath)
		}
	}
}

func TestShuntAnalyzerConflicts(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteFile(t, filepath.Join(baseDir, "rule", "whitelist.txt"), "domain:bing.com\n")
	mustWriteFile(t, filepath.Join(baseDir, "rule", "greylist.txt"), "domain:bing.com\n")
	mustWriteFile(t, filepath.Join(baseDir, "sub_config", "rule_set.yaml"), `
plugins:
  - tag: whitelist
    type: domain_set_light
    args:
      files:
        - "rule/whitelist.txt"
  - tag: greylist
    type: domain_set_light
    args:
      files:
        - "rule/greylist.txt"
  - tag: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: "未命中"
      rules:
        - tag: greylist
          mark: 7
          output_tag: "灰名单"
        - tag: whitelist
          mark: 8
          output_tag: "白名单"
`)

	analyzer, err := newShuntAnalyzer(baseDir)
	if err != nil {
		t.Fatalf("newShuntAnalyzer: %v", err)
	}
	conflicts := analyzer.Conflicts()
	if len(conflicts) != 1 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if conflicts[0].RuleKey != "domain:bing.com" {
		t.Fatalf("unexpected conflict rule key: %+v", conflicts[0])
	}
}

func TestRuntimeShuntConflictsCmd(t *testing.T) {
	baseDir := t.TempDir()
	mustWriteFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteFile(t, filepath.Join(baseDir, "rule", "whitelist.txt"), "domain:bing.com\n")
	mustWriteFile(t, filepath.Join(baseDir, "rule", "greylist.txt"), "domain:bing.com\n")
	mustWriteFile(t, filepath.Join(baseDir, "sub_config", "rule_set.yaml"), `
plugins:
  - tag: whitelist
    type: domain_set_light
    args:
      files:
        - "rule/whitelist.txt"
  - tag: greylist
    type: domain_set_light
    args:
      files:
        - "rule/greylist.txt"
  - tag: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: "未命中"
      rules:
        - tag: greylist
          mark: 7
          output_tag: "灰名单"
        - tag: whitelist
          mark: 8
          output_tag: "白名单"
`)

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
	mustWriteFile(t, filepath.Join(baseDir, "custom_config", "switches.yaml"), `
block_response: "on"
block_query_type: "on"
block_ipv6: "off"
ad_block: "off"
`)
	mustWriteFile(t, filepath.Join(baseDir, "rule", "whitelist.txt"), "domain:bing.com\n")
	mustWriteFile(t, filepath.Join(baseDir, "rule", "greylist.txt"), "domain:bing.com\n")
	mustWriteFile(t, filepath.Join(baseDir, "sub_config", "rule_set.yaml"), `
plugins:
  - tag: whitelist
    type: domain_set_light
    args:
      files:
        - "rule/whitelist.txt"
  - tag: greylist
    type: domain_set_light
    args:
      files:
        - "rule/greylist.txt"
  - tag: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: "未命中"
      rules:
        - tag: greylist
          mark: 7
          output_tag: "灰名单"
        - tag: whitelist
          mark: 8
          output_tag: "白名单"
`)

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

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
