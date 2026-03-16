package configv2

import (
	"fmt"
	"sort"
	"strings"
)

func orderPlugins(plugins []PluginConfig) ([]PluginConfig, error) {
	if len(plugins) < 2 {
		return plugins, nil
	}

	tags := make(map[string]struct{}, len(plugins))
	for _, plugin := range plugins {
		if plugin.Tag == "" {
			continue
		}
		tags[plugin.Tag] = struct{}{}
	}

	deps := make([]map[string]struct{}, len(plugins))
	indegree := make([]int, len(plugins))
	dependents := make(map[string][]int, len(tags))
	for i, plugin := range plugins {
		deps[i] = extractPluginDeps(plugin, tags)
		indegree[i] = len(deps[i])
		for dep := range deps[i] {
			dependents[dep] = append(dependents[dep], i)
		}
	}

	ready := make([]int, 0, len(plugins))
	for i := range plugins {
		if indegree[i] == 0 {
			ready = append(ready, i)
		}
	}

	ordered := make([]PluginConfig, 0, len(plugins))
	for len(ready) > 0 {
		next := ready[0]
		ready = ready[1:]
		ordered = append(ordered, plugins[next])
		for _, dependent := range dependents[plugins[next].Tag] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Ints(ready)
			}
		}
	}

	if len(ordered) == len(plugins) {
		return ordered, nil
	}
	return nil, fmt.Errorf("plugin dependency cycle detected: %s", unresolvedPluginTags(plugins, indegree))
}

func unresolvedPluginTags(plugins []PluginConfig, indegree []int) string {
	tags := make([]string, 0, len(plugins))
	for i, plugin := range plugins {
		if indegree[i] <= 0 {
			continue
		}
		if plugin.Tag == "" {
			tags = append(tags, fmt.Sprintf("#%d", i))
			continue
		}
		tags = append(tags, plugin.Tag)
	}
	sort.Strings(tags)
	return strings.Join(tags, ", ")
}

func extractPluginDeps(plugin PluginConfig, tags map[string]struct{}) map[string]struct{} {
	deps := make(map[string]struct{})
	collectPluginDeps(plugin.Args, "", tags, deps)
	delete(deps, plugin.Tag)
	return deps
}

func collectPluginDeps(value any, key string, tags map[string]struct{}, deps map[string]struct{}) {
	switch v := value.(type) {
	case map[string]any:
		for childKey, childValue := range v {
			collectPluginDeps(childValue, childKey, tags, deps)
		}
	case []any:
		for _, item := range v {
			collectPluginDeps(item, key, tags, deps)
		}
	case []map[string]any:
		for _, item := range v {
			collectPluginDeps(item, key, tags, deps)
		}
	case string:
		collectDollarRefs(v, tags, deps)
		if (key == "entry" || key == "tag") && hasTag(v, tags) {
			deps[v] = struct{}{}
		}
	}
}

func collectDollarRefs(input string, tags map[string]struct{}, deps map[string]struct{}) {
	for i := 0; i < len(input); i++ {
		if input[i] != '$' {
			continue
		}
		start := i + 1
		end := start
		for end < len(input) && !isPluginRefDelimiter(input[end]) {
			end++
		}
		if start == end {
			continue
		}
		ref := input[start:end]
		if hasTag(ref, tags) {
			deps[ref] = struct{}{}
		}
		i = end - 1
	}
}

func hasTag(tag string, tags map[string]struct{}) bool {
	_, ok := tags[strings.TrimSpace(tag)]
	return ok
}

func isPluginRefDelimiter(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\'', '"', ',', ':', ';', '[', ']', '{', '}', '(', ')':
		return true
	default:
		return false
	}
}
