package rulesource

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

var (
	adguardBlockRule = regexp.MustCompile(`^\|\|([\w\.\-\*]+)\^$`)
	adguardAllowRule = regexp.MustCompile(`^@@\|\|([\w\.\-\*]+)\^$`)
	adguardRegexRule = regexp.MustCompile(`^\/(.*)\/$`)
	adguardFullRule  = regexp.MustCompile(`^([\w\.\-]+)$`)
)

func parseAdguardLines(lines []string) (AdguardResult, error) {
	var result AdguardResult
	for _, line := range lines {
		allow, deny, err := normalizeAdguardLine(line)
		if err != nil {
			return AdguardResult{}, err
		}
		if allow != "" {
			result.Allow = append(result.Allow, allow)
		}
		if deny != "" {
			result.Deny = append(result.Deny, deny)
		}
	}
	result.Allow = uniqueStrings(result.Allow)
	result.Deny = uniqueStrings(result.Deny)
	return result, nil
}

func parseAdguardStructured(parse parseStructuredFunc, data []byte) (AdguardResult, error) {
	root, err := parse(data)
	if err != nil {
		return AdguardResult{}, err
	}
	lines, err := collectAdguardLines(root)
	if err != nil {
		return AdguardResult{}, err
	}
	return parseAdguardLines(lines)
}

func collectAdguardLines(root any) ([]string, error) {
	switch v := root.(type) {
	case map[string]any:
		if payload, ok := v["payload"]; ok {
			return asStringSlice(payload), nil
		}
	case []any:
		return asStringSlice(v), nil
	}
	return nil, fmt.Errorf("adguard structured source must be a string array or payload list")
}

func normalizeAdguardLine(line string) (string, string, error) {
	if matches := adguardAllowRule.FindStringSubmatch(line); len(matches) > 1 {
		return convertWildcardDomain(matches[1]), "", nil
	}
	if matches := adguardBlockRule.FindStringSubmatch(line); len(matches) > 1 {
		return "", convertWildcardDomain(matches[1]), nil
	}
	if matches := adguardRegexRule.FindStringSubmatch(line); len(matches) > 1 {
		return "", "regexp:" + matches[1], nil
	}
	if matches := adguardFullRule.FindStringSubmatch(line); len(matches) > 1 {
		return "", "full:" + matches[1], nil
	}
	return "", "", nil
}

func convertWildcardDomain(value string) string {
	clean := strings.TrimPrefix(strings.TrimPrefix(value, "*."), ".")
	if !strings.Contains(clean, "*") {
		return "domain:" + clean
	}
	replacer := strings.NewReplacer(".", `\.`, "*", ".*")
	return "regexp:" + replacer.Replace(clean)
}

func parseAdguardFromReader(data []byte) (AdguardResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lines := make([]string, 0)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return parseAdguardLines(lines)
}
