package rulesource

import "strings"

func parseAdguardRuleLine(line string) (*adguardParsedLine, error) {
	patternText, modifierText, err := splitAdguardRule(line)
	if err != nil {
		return nil, err
	}
	modifiers, canonicalKey, ignore := parseAdguardModifiers(patternText, modifierText)
	if ignore {
		return nil, nil
	}
	patternText = strings.TrimSpace(patternText)
	allow := strings.HasPrefix(patternText, "@@")
	if allow {
		patternText = strings.TrimSpace(strings.TrimPrefix(patternText, "@@"))
	}
	if patternText == "" {
		return nil, nil
	}
	if modifiers.badfilter {
		return &adguardParsedLine{badfilterKey: canonicalKey}, nil
	}
	if len(modifiers.denyAllow) > 0 && allow {
		return nil, nil
	}
	rules, ok, err := compileAdguardPatternRules(patternText)
	if err != nil {
		return nil, err
	}
	if !ok || len(rules) == 0 {
		return nil, nil
	}
	out := make([]adguardCompiledRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, adguardCompiledRule{
			rule:      rule,
			allow:     allow,
			important: modifiers.important,
		})
	}
	if len(modifiers.denyAllow) > 0 {
		for _, rule := range buildAdguardDenyAllowRules(modifiers.denyAllow) {
			out = append(out, adguardCompiledRule{
				rule:      rule,
				allow:     true,
				important: modifiers.important,
			})
		}
	}
	return &adguardParsedLine{key: canonicalKey, rules: out}, nil
}
