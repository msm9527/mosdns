package coremain

import "strings"

const domainOutputDatasetPrefix = "domain_output"

func DomainOutputStatDatasetKey(pluginTag string) string {
	return domainOutputDatasetKey(pluginTag, "stat")
}

func DomainOutputRuleDatasetKey(pluginTag string) string {
	return domainOutputDatasetKey(pluginTag, "rule")
}

func domainOutputDatasetKey(pluginTag, kind string) string {
	pluginTag = strings.TrimSpace(pluginTag)
	kind = strings.TrimSpace(kind)
	if pluginTag == "" || kind == "" {
		return ""
	}
	return domainOutputDatasetPrefix + "/" + pluginTag + "/" + kind
}
