package rulesource

import "fmt"

type parseStructuredFunc func([]byte) (any, error)

func parseDomainStructured(parse parseStructuredFunc, data []byte) ([]string, error) {
	root, err := parse(data)
	if err != nil {
		return nil, err
	}
	rules := make([]string, 0)
	if err := collectDomainRules(root, &rules); err != nil {
		return nil, err
	}
	return uniqueStrings(rules), nil
}

func collectDomainRules(value any, rules *[]string) error {
	switch v := value.(type) {
	case map[string]any:
		if payload, ok := v["payload"]; ok {
			items, err := parseDomainTextLines(asStringSlice(payload))
			if err != nil {
				return err
			}
			*rules = append(*rules, items...)
		}
		appendDomainField(v["domain"], "full:", rules)
		appendDomainField(v["domain_suffix"], "domain:", rules)
		appendDomainField(v["domain_keyword"], "keyword:", rules)
		appendDomainField(v["domain_regex"], "regexp:", rules)
		return collectDomainChildren(v, rules)
	case []any:
		for _, item := range v {
			if err := collectDomainRules(item, rules); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendDomainField(value any, prefix string, rules *[]string) {
	for _, item := range asStringSlice(value) {
		*rules = append(*rules, prefix+item)
	}
}

func collectDomainChildren(values map[string]any, rules *[]string) error {
	for key, value := range values {
		switch key {
		case "payload", "domain", "domain_suffix", "domain_keyword", "domain_regex":
			continue
		}
		if err := collectDomainRules(value, rules); err != nil {
			return fmt.Errorf("collect domain rules from %q: %w", key, err)
		}
	}
	return nil
}
