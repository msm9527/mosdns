package rulesource

import (
	"strings"
)

type adguardModifiers struct {
	important bool
	badfilter bool
	denyAllow []string
}

func parseAdguardModifiers(patternText, modifierText string) (adguardModifiers, string, bool) {
	key := strings.TrimSpace(patternText)
	if modifierText == "" {
		return adguardModifiers{}, key, false
	}
	parts := splitAdguardEscaped(modifierText, ',')
	modifiers := adguardModifiers{}
	kept := make([]string, 0, len(parts))
	for _, raw := range parts {
		token := strings.TrimSpace(raw)
		if token == "" {
			continue
		}
		name, value, hasValue := cutAdguardModifier(token)
		switch name {
		case "important":
			if hasValue {
				return adguardModifiers{}, "", true
			}
			modifiers.important = true
			kept = append(kept, token)
		case "badfilter":
			if hasValue {
				return adguardModifiers{}, "", true
			}
			modifiers.badfilter = true
		case "denyallow":
			if !hasValue {
				return adguardModifiers{}, "", true
			}
			domains, ok := parseAdguardDenyAllow(value)
			if !ok {
				return adguardModifiers{}, "", true
			}
			modifiers.denyAllow = domains
			kept = append(kept, token)
		default:
			return adguardModifiers{}, "", true
		}
	}
	if len(kept) > 0 {
		key += "$" + strings.Join(kept, ",")
	}
	return modifiers, key, false
}

func cutAdguardModifier(token string) (string, string, bool) {
	name, value, found := strings.Cut(token, "=")
	if !found {
		return strings.ToLower(strings.TrimSpace(token)), "", false
	}
	return strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value), true
}

func parseAdguardDenyAllow(value string) ([]string, bool) {
	items := splitAdguardEscaped(value, '|')
	out := make([]string, 0, len(items))
	for _, item := range items {
		normalized, ok := normalizeAdguardDomainToken(item)
		if !ok {
			return nil, false
		}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil, false
	}
	return uniqueStrings(out), true
}

func splitAdguardEscaped(value string, sep rune) []string {
	out := make([]string, 0, 4)
	var current strings.Builder
	escaped := false
	for _, r := range value {
		switch {
		case escaped:
			current.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == sep:
			out = append(out, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	out = append(out, current.String())
	return out
}
