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
