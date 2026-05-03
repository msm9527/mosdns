package coremain

import "sync/atomic"

// CloneGlobalOverrides returns a deep copy of src.
func CloneGlobalOverrides(src *GlobalOverrides) *GlobalOverrides {
	if src == nil {
		return nil
	}

	dst := &GlobalOverrides{
		Socks5:      src.Socks5,
		ECS:         src.ECS,
		DomesticECS: src.DomesticECS,
		ForeignECS:  src.ForeignECS,
	}

	if len(src.Replacements) > 0 {
		dst.Replacements = make([]*ReplacementRule, 0, len(src.Replacements))
		for _, r := range src.Replacements {
			if r == nil {
				continue
			}
			copied := &ReplacementRule{
				Original: r.Original,
				New:      r.New,
				Comment:  r.Comment,
			}
			copied.appliedCount = atomic.LoadInt64(&r.appliedCount)
			dst.Replacements = append(dst.Replacements, copied)
		}
	}

	if src.lookupMap != nil {
		dst.Prepare()
	}
	return dst
}

func GlobalOverridesEqual(a, b *GlobalOverrides) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Socks5 != b.Socks5 ||
		a.ECS != b.ECS ||
		a.DomesticECS != b.DomesticECS ||
		a.ForeignECS != b.ForeignECS {
		return false
	}
	if len(a.Replacements) != len(b.Replacements) {
		return false
	}
	for i := range a.Replacements {
		ar := a.Replacements[i]
		br := b.Replacements[i]
		if ar == nil || br == nil {
			if ar != br {
				return false
			}
			continue
		}
		if ar.Original != br.Original || ar.New != br.New || ar.Comment != br.Comment {
			return false
		}
	}
	return true
}
