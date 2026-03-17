package coremain

import "strings"

type HotRuleSnapshotProvider interface {
	SnapshotHotRules() ([]string, error)
}

type HotRuleConsumer interface {
	AddHotRules(providerTag string, rules []string) error
	ReplaceHotRules(providerTag string, rules []string) error
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

func DispatchHotRulesAdd(m *Mosdns, providerTag string, rules []string) error {
	return dispatchHotRules(m, providerTag, rules, false)
}

func DispatchHotRulesReplace(m *Mosdns, providerTag string, rules []string) error {
	return dispatchHotRules(m, providerTag, rules, true)
}

func dispatchHotRules(m *Mosdns, providerTag string, rules []string, replace bool) error {
	if m == nil {
		return nil
	}
	providerTag = strings.TrimSpace(providerTag)
	if providerTag == "" {
		return nil
	}
	var firstErr error
	for _, plugin := range m.SnapshotPlugins() {
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
