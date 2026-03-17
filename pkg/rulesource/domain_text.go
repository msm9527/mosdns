package rulesource

import (
	"fmt"
	"strings"
)

func parseDomainTextLines(lines []string) ([]string, error) {
	rules := make([]string, 0, len(lines))
	for _, line := range lines {
		rule, err := normalizeDomainLine(line)
		if err != nil {
			return nil, err
		}
		if rule == "" {
			continue
		}
		rules = append(rules, rule)
	}
	return uniqueStrings(rules), nil
}

func normalizeDomainLine(line string) (string, error) {
	switch {
	case hasRulePrefix(line):
		return line, nil
	case strings.HasPrefix(line, "+."):
		return "domain:" + strings.TrimPrefix(line, "+."), nil
	case strings.HasPrefix(strings.ToUpper(line), "DOMAIN-SUFFIX,"):
		return "domain:" + afterComma(line), nil
	case strings.HasPrefix(strings.ToUpper(line), "DOMAIN,"):
		return "full:" + afterComma(line), nil
	case strings.HasPrefix(strings.ToUpper(line), "DOMAIN-KEYWORD,"):
		return "keyword:" + afterComma(line), nil
	case strings.HasPrefix(strings.ToUpper(line), "DOMAIN-REGEX,"):
		return "regexp:" + afterComma(line), nil
	case strings.HasPrefix(line, "/") && strings.HasSuffix(line, "/"):
		return "regexp:" + strings.TrimSuffix(strings.TrimPrefix(line, "/"), "/"), nil
	case strings.Contains(line, "/"):
		return "", fmt.Errorf("invalid domain rule %q", line)
	default:
		return "domain:" + line, nil
	}
}

func hasRulePrefix(line string) bool {
	prefixes := []string{"full:", "domain:", "keyword:", "regexp:"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func afterComma(line string) string {
	parts := strings.SplitN(line, ",", 2)
	if len(parts) != 2 {
		return ""
	}
	return strings.TrimSpace(parts[1])
}
