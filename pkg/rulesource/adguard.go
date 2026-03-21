package rulesource

import (
	"fmt"
	"strings"
)

type adguardParsedLine struct {
	key          string
	rules        []adguardCompiledRule
	badfilterKey string
}

type adguardCompiledRule struct {
	rule      string
	allow     bool
	important bool
}

func parseAdguardLines(lines []string) (AdguardResult, error) {
	parsed := make([]adguardParsedLine, 0, len(lines))
	badfilters := make(map[string]struct{})
	for _, line := range lines {
		if isAdguardMetadataLine(line) {
			continue
		}
		item, err := parseAdguardRuleLine(line)
		if err != nil {
			return AdguardResult{}, err
		}
		if item == nil {
			continue
		}
		if item.badfilterKey != "" {
			badfilters[item.badfilterKey] = struct{}{}
			continue
		}
		parsed = append(parsed, *item)
	}
	return buildAdguardResult(parsed, badfilters), nil
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

func isAdguardMetadataLine(line string) bool {
	return strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]")
}

func buildAdguardResult(parsed []adguardParsedLine, badfilters map[string]struct{}) AdguardResult {
	var result AdguardResult
	for _, item := range parsed {
		if _, blocked := badfilters[item.key]; blocked {
			continue
		}
		for _, rule := range item.rules {
			switch {
			case rule.allow && rule.important:
				result.ImportantAllow = append(result.ImportantAllow, rule.rule)
			case rule.allow:
				result.Allow = append(result.Allow, rule.rule)
			case rule.important:
				result.ImportantDeny = append(result.ImportantDeny, rule.rule)
			default:
				result.Deny = append(result.Deny, rule.rule)
			}
		}
	}
	result.Allow = uniqueStrings(result.Allow)
	result.Deny = uniqueStrings(result.Deny)
	result.ImportantAllow = uniqueStrings(result.ImportantAllow)
	result.ImportantDeny = uniqueStrings(result.ImportantDeny)
	return result
}
