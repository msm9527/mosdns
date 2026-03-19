package requery

import "github.com/IrineSistiana/mosdns/v5/coremain"

func (p *Requery) memoryTargetTags() ([]string, error) {
	if p.pluginTag != "" {
		return coremain.RequeryTargetDomainPoolTagsForBaseDir(p.baseDir, p.pluginTag)
	}

	// Test-only fallback for direct struct construction without plugin init.
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.actionTagsLocked(p.config.URLActions.SaveRules, "save"), nil
}
