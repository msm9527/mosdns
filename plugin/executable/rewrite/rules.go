package rewrite

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/pkg/utils"
	"github.com/miekg/dns"
)

func loadRulesFromFiles(files []string) (*domain.MixMatcher[*rewriteRule], []string, error) {
	acc := newRuleAccumulator()

	for i, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, nil, fmt.Errorf("failed to read file #%d %s, %w", i, file, err)
		}
		if err := acc.load(bytes.NewReader(b)); err != nil {
			return nil, nil, fmt.Errorf("failed to load rules from file #%d %s, %w", i, file, err)
		}
	}

	return acc.buildMatcher()
}

func loadRulesFromReader(reader io.Reader, matcher *domain.MixMatcher[*rewriteRule]) ([]string, error) {
	acc := newRuleAccumulator()
	if err := acc.load(reader); err != nil {
		return nil, err
	}
	loadedRules := append([]string(nil), acc.rawRules...)
	for _, pattern := range acc.order {
		if err := matcher.Add(pattern, acc.merged[pattern]); err != nil {
			return nil, fmt.Errorf("invalid rule pattern %s: %w", pattern, err)
		}
	}
	return loadedRules, nil
}

func normalizeRuleLine(line string) string {
	line = utils.RemoveComment(line, "#")
	return strings.TrimSpace(line)
}

type ruleAccumulator struct {
	order    []string
	merged   map[string]*rewriteRule
	rawRules []string
}

func newRuleAccumulator() *ruleAccumulator {
	return &ruleAccumulator{
		order:  make([]string, 0),
		merged: make(map[string]*rewriteRule),
	}
}

func (acc *ruleAccumulator) load(reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	for lineNum := 1; scanner.Scan(); lineNum++ {
		line := normalizeRuleLine(scanner.Text())
		if line == "" {
			continue
		}
		pattern, target, err := parseRewriteRuleLine(line)
		if err != nil {
			return fmt.Errorf("error on line %d: %w", lineNum, err)
		}
		acc.add(pattern, target, line)
	}
	return scanner.Err()
}

func (acc *ruleAccumulator) add(pattern string, target rewriteTarget, raw string) {
	if _, ok := acc.merged[pattern]; !ok {
		acc.order = append(acc.order, pattern)
		acc.merged[pattern] = &rewriteRule{}
	}
	acc.merged[pattern].targets = appendUniqueTarget(acc.merged[pattern].targets, target)
	acc.rawRules = append(acc.rawRules, raw)
}

func (acc *ruleAccumulator) buildMatcher() (*domain.MixMatcher[*rewriteRule], []string, error) {
	matcher := newRewriteMatcher()
	for _, pattern := range acc.order {
		if err := matcher.Add(pattern, acc.merged[pattern]); err != nil {
			return nil, nil, fmt.Errorf("invalid rule pattern %s: %w", pattern, err)
		}
	}
	return matcher, append([]string(nil), acc.rawRules...), nil
}

func parseRewriteRuleLine(s string) (string, rewriteTarget, error) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return "", rewriteTarget{}, fmt.Errorf("rule must have 2 fields, but got %d", len(fields))
	}
	target, err := parseRewriteTarget(fields[1])
	if err != nil {
		return "", rewriteTarget{}, err
	}
	return fields[0], target, nil
}

func parseRewriteTarget(value string) (rewriteTarget, error) {
	if ip, err := netip.ParseAddr(value); err == nil {
		return rewriteTarget{kind: targetIP, ip: ip}, nil
	}
	if _, ok := dns.IsDomainName(value); !ok {
		return rewriteTarget{}, fmt.Errorf("invalid target syntax: %s", value)
	}
	return rewriteTarget{kind: targetDomain, domain: dns.Fqdn(value)}, nil
}

func appendUniqueTarget(dst []rewriteTarget, target rewriteTarget) []rewriteTarget {
	key := rewriteTargetKey(target)
	for _, existing := range dst {
		if rewriteTargetKey(existing) == key {
			return dst
		}
	}
	return append(dst, target)
}

func rewriteTargetKey(target rewriteTarget) string {
	if target.kind == targetIP {
		return "ip:" + target.ip.String()
	}
	return "domain:" + target.domain
}

func (r *Rewrite) ListEntries(query string, offset, limit int) ([]coremain.ListEntry, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	filter := strings.ToLower(strings.TrimSpace(query))
	items := make([]coremain.ListEntry, 0, len(r.rules))
	for _, rule := range r.rules {
		if filter != "" && !strings.Contains(strings.ToLower(rule), filter) {
			continue
		}
		items = append(items, coremain.ListEntry{Value: rule})
	}
	return paginateEntries(items, offset, limit), len(items), nil
}

func paginateEntries(items []coremain.ListEntry, offset, limit int) []coremain.ListEntry {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []coremain.ListEntry{}
	}
	if limit <= 0 || offset+limit > len(items) {
		return items[offset:]
	}
	return items[offset : offset+limit]
}

func (r *Rewrite) ReplaceListRuntime(_ context.Context, values []string) (int, error) {
	r.mu.RLock()
	ruleFiles := append([]string(nil), r.ruleFiles...)
	r.mu.RUnlock()

	if err := validateEditableRuleFiles(ruleFiles); err != nil {
		return 0, err
	}

	matcher, rules, err := buildRuntimeRules(values)
	if err != nil {
		return 0, fmt.Errorf("failed to parse new rules: %w", err)
	}
	if err := writeRulesToFiles(ruleFiles, rules); err != nil {
		return 0, err
	}

	r.mu.Lock()
	r.matcher = matcher
	r.rules = rules
	r.mu.Unlock()
	return len(rules), nil
}

func buildRuntimeRules(values []string) (*domain.MixMatcher[*rewriteRule], []string, error) {
	acc := newRuleAccumulator()
	if err := acc.load(strings.NewReader(strings.Join(values, "\n"))); err != nil {
		return nil, nil, err
	}
	return acc.buildMatcher()
}

func validateEditableRuleFiles(files []string) error {
	switch {
	case len(files) == 0:
		return fmt.Errorf("no rule file configured, cannot replace")
	}

	for _, file := range files {
		if !strings.EqualFold(filepath.Ext(file), ".txt") {
			return fmt.Errorf("runtime replace requires txt rule files")
		}
	}
	return nil
}

func writeRulesToFiles(paths []string, rules []string) error {
	for i, path := range paths {
		fileRules := []string(nil)
		if i == 0 {
			fileRules = rules
		}
		if err := writeRulesToFile(path, fileRules); err != nil {
			return err
		}
	}
	return nil
}

func writeRulesToFile(path string, rules []string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create rule directory: %w", err)
	}

	tempFile, err := os.CreateTemp(dir, "rewrite-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %w", err)
	}
	defer os.Remove(tempFile.Name())

	writer := bufio.NewWriter(tempFile)
	if _, err := writer.WriteString(encodeRules(rules)); err != nil {
		tempFile.Close()
		return fmt.Errorf("failed to write to temporary file: %w", err)
	}
	if err := writer.Flush(); err != nil {
		tempFile.Close()
		return fmt.Errorf("failed to flush temporary file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary file: %w", err)
	}
	if err := os.Rename(tempFile.Name(), path); err != nil {
		return fmt.Errorf("failed to rename temporary file to final destination: %w", err)
	}
	return nil
}

func encodeRules(rules []string) string {
	if len(rules) == 0 {
		return ""
	}
	return strings.Join(rules, "\n") + "\n"
}
