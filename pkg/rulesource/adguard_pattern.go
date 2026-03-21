package rulesource

import (
	"fmt"
	"net/netip"
	"strings"

	domainmatcher "github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
)

type adguardMatchSpec struct {
	rule string
}

func splitAdguardRule(line string) (string, string, error) {
	line = strings.TrimSpace(line)
	raw := line
	if strings.HasPrefix(raw, "@@") {
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "@@"))
	}
	if strings.HasPrefix(raw, "/") {
		end, ok := findAdguardRegexEnd(raw)
		if !ok {
			return line, "", nil
		}
		prefixLen := len(line) - len(raw)
		patternEnd := prefixLen + end + 1
		if patternEnd == len(line) {
			return line, "", nil
		}
		if line[patternEnd] != '$' {
			return "", "", fmt.Errorf("invalid adguard rule %q", line)
		}
		return line[:patternEnd], line[patternEnd+1:], nil
	}
	index := indexUnescapedByte(line, '$')
	if index < 0 {
		return line, "", nil
	}
	return line[:index], line[index+1:], nil
}

func findAdguardRegexEnd(value string) (int, bool) {
	escaped := false
	for i := 1; i < len(value); i++ {
		switch {
		case escaped:
			escaped = false
		case value[i] == '\\':
			escaped = true
		case value[i] == '/':
			return i, true
		}
	}
	return 0, false
}

func indexUnescapedByte(value string, target byte) int {
	escaped := false
	for i := 0; i < len(value); i++ {
		switch {
		case escaped:
			escaped = false
		case value[i] == '\\':
			escaped = true
		case value[i] == target:
			return i
		}
	}
	return -1
}

func compileAdguardPatternRules(pattern string) ([]string, bool, error) {
	if hostsRules, ok, err := parseAdguardHostsRules(pattern); err != nil {
		return nil, false, err
	} else if ok {
		return hostsRules, true, nil
	}
	spec, ok, err := compileAdguardPattern(pattern)
	if err != nil || !ok {
		return nil, ok, err
	}
	return []string{spec.rule}, true, nil
}

func buildAdguardDenyAllowRules(domains []string) []string {
	out := make([]string, 0, len(domains))
	for _, item := range domains {
		out = append(out, "domain:"+item)
	}
	return uniqueStrings(out)
}

func parseAdguardHostsRules(pattern string) ([]string, bool, error) {
	fields := strings.Fields(pattern)
	if len(fields) < 2 {
		return nil, false, nil
	}
	if _, err := netip.ParseAddr(fields[0]); err != nil {
		return nil, false, nil
	}
	out := make([]string, 0, len(fields)-1)
	for _, host := range fields[1:] {
		if strings.HasPrefix(host, "#") {
			break
		}
		normalized, ok := normalizeAdguardDomainToken(host)
		if !ok {
			return nil, false, fmt.Errorf("invalid adguard hosts rule %q", pattern)
		}
		out = append(out, "full:"+normalized)
	}
	return uniqueStrings(out), len(out) > 0, nil
}

func compileAdguardPattern(pattern string) (adguardMatchSpec, bool, error) {
	if expr, ok := unwrapAdguardRegex(pattern); ok {
		return adguardMatchSpec{
			rule: "regexp:" + expr,
		}, true, nil
	}
	subdomainAnchor := strings.HasPrefix(pattern, "||")
	startAnchor := strings.HasPrefix(pattern, "|")
	if subdomainAnchor {
		pattern = strings.TrimPrefix(pattern, "||")
	} else if startAnchor {
		pattern = strings.TrimPrefix(pattern, "|")
	}
	endAnchor := strings.HasSuffix(pattern, "|")
	if endAnchor {
		pattern = strings.TrimSuffix(pattern, "|")
	}
	if strings.HasSuffix(pattern, "^") {
		endAnchor = true
		pattern = strings.TrimSuffix(pattern, "^")
	}
	if strings.Contains(pattern, "^") {
		return adguardMatchSpec{}, false, nil
	}
	if subdomainAnchor {
		if normalized, ok := normalizeAdguardDomainToken(pattern); ok {
			return adguardMatchSpec{
				rule: "domain:" + normalized,
			}, true, nil
		}
	}
	if normalized, ok := normalizeAdguardDomainToken(pattern); ok && !startAnchor && !endAnchor {
		return adguardMatchSpec{
			rule: "full:" + normalized,
		}, true, nil
	}
	regexBody, ok := buildAdguardWildcardRegex(pattern)
	if !ok {
		return adguardMatchSpec{}, false, nil
	}
	fullRegex := buildAdguardAnchoredRegex(regexBody, subdomainAnchor, startAnchor, endAnchor)
	return adguardMatchSpec{
		rule: "regexp:" + fullRegex,
	}, true, nil
}

func unwrapAdguardRegex(pattern string) (string, bool) {
	if len(pattern) < 2 || pattern[0] != '/' {
		return "", false
	}
	if pattern[len(pattern)-1] != '/' {
		return normalizeCompatAdguardRegex(pattern[1:]), true
	}
	return pattern[1 : len(pattern)-1], true
}

func normalizeCompatAdguardRegex(body string) string {
	if strings.HasSuffix(body, "^") && !strings.HasSuffix(body, `\^`) {
		return strings.TrimSuffix(body, "^") + "$"
	}
	return body
}

func normalizeAdguardDomainToken(value string) (string, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(value, "*."), "."))
	if value == "" || strings.ContainsAny(value, "/*|^$@ ") {
		return "", false
	}
	return domainmatcher.NormalizeDomain(value), true
}

func buildAdguardWildcardRegex(pattern string) (string, bool) {
	var out strings.Builder
	for _, r := range pattern {
		switch r {
		case '*':
			out.WriteString(".*")
		case '.', '+', '?', '(', ')', '[', ']', '{', '}', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		default:
			if r == ' ' {
				return "", false
			}
			out.WriteRune(r)
		}
	}
	return out.String(), true
}

func buildAdguardAnchoredRegex(body string, subdomainAnchor, startAnchor, endAnchor bool) string {
	var out strings.Builder
	out.WriteByte('^')
	switch {
	case subdomainAnchor:
		out.WriteString("(?:.+\\.)?")
	case !startAnchor:
		out.WriteString(".*")
	}
	out.WriteString(body)
	if !endAnchor {
		out.WriteString(".*")
	}
	out.WriteByte('$')
	return out.String()
}
