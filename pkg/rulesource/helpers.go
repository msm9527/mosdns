package rulesource

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func normalizeTextLines(data []byte) []string {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lines := make([]string, 0)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func parseJSONAny(data []byte) (any, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func parseYAMLAny(data []byte) (any, error) {
	var value any
	if err := yaml.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func asStringSlice(value any) []string {
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		return []string{strings.TrimSpace(v)}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if strings.TrimSpace(item) != "" {
				out = append(out, strings.TrimSpace(item))
			}
		}
		return out
	default:
		return nil
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func uniquePrefixes(values []netip.Prefix) []netip.Prefix {
	seen := make(map[string]netip.Prefix, len(values))
	for _, value := range values {
		seen[value.String()] = value
	}
	out := make([]netip.Prefix, 0, len(seen))
	for _, value := range seen {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].String() < out[j].String()
	})
	return out
}
