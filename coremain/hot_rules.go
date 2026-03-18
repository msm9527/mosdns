package coremain

import "strings"

type HotRuleSnapshotProvider interface {
	SnapshotHotRules() ([]string, error)
}

type HotRuleConsumer interface {
	AddHotRules(providerTag string, rules []string) error
	ReplaceHotRules(providerTag string, rules []string) error
}

type PluginSnapshotter interface {
	SnapshotPlugins() map[string]any
}

func (m *Mosdns) SnapshotPlugins() map[string]any {
	if m == nil {
		return nil
	}
	snapshot := make(map[string]any, len(m.plugins))
	for tag, plugin := range m.plugins {
		snapshot[tag] = plugin
	}
	return snapshot
}

func DispatchHotRulesAdd(snapshotter PluginSnapshotter, providerTag string, rules []string) error {
	return dispatchHotRules(snapshotter, providerTag, rules, false)
}

func DispatchHotRulesReplace(snapshotter PluginSnapshotter, providerTag string, rules []string) error {
	return dispatchHotRules(snapshotter, providerTag, rules, true)
}

func dispatchHotRules(snapshotter PluginSnapshotter, providerTag string, rules []string, replace bool) error {
	if snapshotter == nil {
		return nil
	}
	providerTag = strings.TrimSpace(providerTag)
	if providerTag == "" {
		return nil
	}
	var firstErr error
	for _, plugin := range snapshotter.SnapshotPlugins() {
		consumer, ok := plugin.(HotRuleConsumer)
		if !ok || consumer == nil {
			continue
		}
		var err error
		if replace {
			err = consumer.ReplaceHotRules(providerTag, rules)
		} else {
			err = consumer.AddHotRules(providerTag, rules)
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
