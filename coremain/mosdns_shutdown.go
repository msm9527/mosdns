package coremain

import (
	"io"
	"sort"

	"go.uber.org/zap"
)

const (
	shutdownPriorityServer = iota
	shutdownPriorityDefault
	shutdownPriorityPersistence
)

type shutdownPlugin struct {
	tag    string
	typ    string
	plugin any
}

func (m *Mosdns) shutdownPlugins() {
	m.logger.Info("starting shutdown sequences")
	for _, entry := range m.pluginsForShutdown() {
		closer, _ := entry.plugin.(io.Closer)
		if closer == nil {
			continue
		}
		m.logger.Info("closing plugin", zap.String("tag", entry.tag), zap.String("type", entry.typ))
		_ = closer.Close()
	}
	m.logger.Info("all plugins were closed")
}

func (m *Mosdns) pluginsForShutdown() []shutdownPlugin {
	entries := make([]shutdownPlugin, 0, len(m.plugins))
	for tag, plugin := range m.plugins {
		entries = append(entries, shutdownPlugin{
			tag:    tag,
			typ:    m.pluginType(tag),
			plugin: plugin,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		left := shutdownPriority(entries[i].typ)
		right := shutdownPriority(entries[j].typ)
		if left != right {
			return left < right
		}
		return entries[i].tag < entries[j].tag
	})
	return entries
}

func (m *Mosdns) pluginType(tag string) string {
	if m == nil || m.pluginTypes == nil {
		return ""
	}
	return m.pluginTypes[tag]
}

func shutdownPriority(typ string) int {
	switch {
	case isServerPluginType(typ):
		return shutdownPriorityServer
	case isPersistencePluginType(typ):
		return shutdownPriorityPersistence
	default:
		return shutdownPriorityDefault
	}
}

func isServerPluginType(typ string) bool {
	switch typ {
	case "udp_server", "tcp_server", "quic_server", "http_server":
		return true
	default:
		return false
	}
}

func isPersistencePluginType(typ string) bool {
	switch typ {
	case "cache", "domain_memory_pool", "domain_stats_pool":
		return true
	default:
		return false
	}
}
