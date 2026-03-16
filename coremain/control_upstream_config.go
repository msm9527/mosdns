package coremain

import (
	"net/url"
	"sort"
	"strings"
)

type ControlUpstreamProvider interface {
	SnapshotControlUpstreams() (string, []UpstreamOverrideConfig)
}

func collectRuntimeUpstreamConfigs(m *Mosdns) GlobalUpstreamOverrides {
	result := make(GlobalUpstreamOverrides)
	if m == nil {
		return result
	}
	for _, plugin := range m.plugins {
		provider, ok := plugin.(ControlUpstreamProvider)
		if !ok || provider == nil {
			continue
		}
		tag, items := provider.SnapshotControlUpstreams()
		if tag == "" || len(items) == 0 {
			continue
		}
		copied := make([]UpstreamOverrideConfig, len(items))
		copy(copied, items)
		result[tag] = copied
	}
	return result
}

func collectRuntimeUpstreamTags(m *Mosdns) []string {
	configs := collectRuntimeUpstreamConfigs(m)
	tags := make([]string, 0, len(configs))
	for tag := range configs {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func mergeRuntimeAndOverrideUpstreams(runtimeCfg, overrideCfg GlobalUpstreamOverrides) GlobalUpstreamOverrides {
	merged := cloneGlobalUpstreamOverrides(runtimeCfg)
	for tag, items := range overrideCfg {
		copied := make([]UpstreamOverrideConfig, len(items))
		copy(copied, items)
		merged[tag] = copied
	}
	if merged == nil {
		return make(GlobalUpstreamOverrides)
	}
	return merged
}

func UpstreamProtocolFromAddr(addr string) string {
	raw := strings.TrimSpace(addr)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		return "udp"
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch parsed.Scheme {
	case "tls":
		return "dot"
	case "https":
		return "doh"
	case "quic":
		return "doq"
	case "tcp+pipeline":
		return "tcp"
	case "tls+pipeline":
		return "dot"
	default:
		return parsed.Scheme
	}
}
