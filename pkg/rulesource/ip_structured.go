package rulesource

import (
	"fmt"
	"net/netip"
)

func parseIPStructured(parse parseStructuredFunc, data []byte) ([]netip.Prefix, error) {
	root, err := parse(data)
	if err != nil {
		return nil, err
	}
	prefixes := make([]netip.Prefix, 0)
	if err := collectIPRules(root, &prefixes); err != nil {
		return nil, err
	}
	return uniquePrefixes(prefixes), nil
}

func collectIPRules(value any, prefixes *[]netip.Prefix) error {
	switch v := value.(type) {
	case map[string]any:
		if payload, ok := v["payload"]; ok {
			items, err := parseIPTextLines(asStringSlice(payload))
			if err != nil {
				return err
			}
			*prefixes = append(*prefixes, items...)
		}
		appendIPField(v["ip_cidr"], prefixes)
		appendIPField(v["source_ip_cidr"], prefixes)
		return collectIPChildren(v, prefixes)
	case []any:
		for _, item := range v {
			if err := collectIPRules(item, prefixes); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendIPField(value any, prefixes *[]netip.Prefix) {
	for _, item := range asStringSlice(value) {
		if prefix, err := normalizeIPLine(item); err == nil {
			*prefixes = append(*prefixes, prefix)
		}
	}
}

func collectIPChildren(values map[string]any, prefixes *[]netip.Prefix) error {
	for key, value := range values {
		switch key {
		case "payload", "ip_cidr", "source_ip_cidr":
			continue
		}
		if err := collectIPRules(value, prefixes); err != nil {
			return fmt.Errorf("collect ip rules from %q: %w", key, err)
		}
	}
	return nil
}
