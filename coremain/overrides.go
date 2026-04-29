package coremain

import (
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"go.uber.org/zap"
)

const overridesFilename = "config_overrides.json"

const (
	domesticECSPlaceholder = "__domestic_ecs__"
	foreignECSPlaceholder  = "__foreign_ecs__"
	defaultECSOverride     = "auto"
)

// ReplacementRule defines a single replacement rule.
type ReplacementRule struct {
	Original string `json:"original" yaml:"original"`
	New      string `json:"new" yaml:"new"`
	Comment  string `json:"comment" yaml:"comment"`

	// appliedCount is an in-memory counter for successful replacements.
	// It is not exported to JSON file, but used for API response.
	appliedCount int64
}

// GlobalOverrides defines the structure of the config_overrides.json file.
type GlobalOverrides struct {
	// User-facing global override fields.
	Socks5      string `json:"socks5,omitempty" yaml:"socks5,omitempty"`
	ECS         string `json:"ecs,omitempty" yaml:"ecs,omitempty"`
	DomesticECS string `json:"domestic_ecs,omitempty" yaml:"domestic_ecs,omitempty"`
	ForeignECS  string `json:"foreign_ecs,omitempty" yaml:"foreign_ecs,omitempty"`

	// New generic replacements
	Replacements []*ReplacementRule `json:"replacements,omitempty" yaml:"replacements,omitempty"`

	// lookupMap is used for fast lookup during application.
	// Key is the "Original" string.
	lookupMap map[string]*ReplacementRule
}

var (
	// These variables cache the settings discovered from the original YAML config.
	discoveredSocks5 string
	discoveredECS    string
	// [Debug] Exported for api_upstream.go
	discoveredAliAPITags []string
)

// Prepare builds the lookup map for efficient execution.
func (g *GlobalOverrides) Prepare() {
	g.lookupMap = make(map[string]*ReplacementRule)
	if g.Replacements != nil {
		for _, r := range g.Replacements {
			if r.Original == "" {
				continue
			}
			g.lookupMap[r.Original] = r
			// Reset count on prepare (startup)
			r.appliedCount = 0
		}
	}
}

// DiscoverAndCacheSettings scans the config to find specific settings.
// [Modified] Added heavy debug logging to trace plugin discovery.
func DiscoverAndCacheSettings(cfg *Config) {
	// [Debug] Log start
	mlog.L().Info("[Debug Discovery] >>> Starting configuration discovery...")

	var socks5Found, ecsFound bool
	discoveredSocks5 = ""
	discoveredECS = ""
	discoveredAliAPITags = make([]string, 0)
	tm := make(map[string]bool)

	// Recursive function to traverse config and includes
	var discover func(c *Config, sourceFile string)
	discover = func(c *Config, sourceFile string) {
		if c == nil {
			return
		}

		mlog.L().Info("[Debug Discovery] Scanning config scope",
			zap.String("source", sourceFile),
			zap.Int("plugins_count", len(c.Plugins)),
			zap.Int("includes_count", len(c.Include)))

		// 1. Scan Plugins in current config scope
		// [FIXED] replaced unused 'i' with '_'
		for _, pluginConf := range c.Plugins {
			// [Debug] Print every plugin encountered (Commented out to reduce noise, enable if needed)
			// mlog.L().Debug("[Debug Discovery] Checking plugin", zap.String("type", pluginConf.Type), zap.String("tag", pluginConf.Tag))

			// Check for aliapi
			if pluginConf.Type == "aliapi" && pluginConf.Tag != "" {
				if !tm[pluginConf.Tag] {
					mlog.L().Info("[Debug Discovery] FOUND aliapi tag", zap.String("tag", pluginConf.Tag), zap.String("source", sourceFile))
					discoveredAliAPITags = append(discoveredAliAPITags, pluginConf.Tag)
					tm[pluginConf.Tag] = true
				} else {
					mlog.L().Info("[Debug Discovery] Skipping duplicate aliapi tag", zap.String("tag", pluginConf.Tag))
				}
			}

			// Check for socks5/ecs (Original logic)
			if !socks5Found || !ecsFound {
				discoverRecursive(pluginConf.Args, &socks5Found, &ecsFound)
			}
		}

		// 2. Recurse into Includes
		for _, includePath := range c.Include {
			resolvedPath := includePath
			if len(c.baseDir) > 0 && !filepath.IsAbs(includePath) {
				resolvedPath = filepath.Join(c.baseDir, includePath)
			}

			mlog.L().Info("[Debug Discovery] Reading include file", zap.String("path", resolvedPath))

			subCfg, _, err := loadConfig(resolvedPath)
			if err == nil {
				discover(subCfg, resolvedPath)
			} else {
				mlog.L().Warn("[Debug Discovery] Failed to load include file", zap.String("path", resolvedPath), zap.Error(err))
			}
		}
	}

	discover(cfg, "root_config")

	mlog.L().Info("[Debug Discovery] <<< Discovery finished",
		zap.Strings("all_aliapi_tags", discoveredAliAPITags),
		zap.String("socks5", discoveredSocks5),
		zap.String("ecs", discoveredECS))
}

// discoverRecursive correctly handles nested map[string]any and []any.
func discoverRecursive(data any, socks5Found, ecsFound *bool) {
	if data == nil || (*socks5Found && *ecsFound) {
		return
	}

	switch v := data.(type) {
	case map[string]any:
		if !*socks5Found {
			if sockVal, ok := v["socks5"]; ok {
				if socks5Str, isString := sockVal.(string); isString && socks5Str != "" {
					discoveredSocks5 = socks5Str
					*socks5Found = true
				}
			}
		}
		for _, val := range v {
			if *socks5Found && *ecsFound {
				return
			}
			discoverRecursive(val, socks5Found, ecsFound)
		}
	case []any:
		for _, item := range v {
			if *socks5Found && *ecsFound {
				return
			}
			discoverRecursive(item, socks5Found, ecsFound)
		}
	case string:
		if !*ecsFound && strings.HasPrefix(v, "ecs ") {
			parts := strings.SplitN(v, " ", 2)
			if len(parts) == 2 && parts[1] != "" {
				discoveredECS = parts[1]
				*ecsFound = true
			}
		}
	}
}

// ApplyOverrides modifies a single PluginConfig based on the loaded overrides.
func ApplyOverrides(tag string, pluginConf *PluginConfig, overrides *GlobalOverrides) {
	pluginConf.Args = applyRecursive(tag, pluginConf.Args, overrides)
}

// ApplyOverrideString applies ECS and replacement rules to a single string value.
func ApplyOverrideString(tag string, currentVal string, overrides *GlobalOverrides) string {
	if applied, ok := applyECSOverrideString(currentVal, overrides); ok {
		return applied
	}
	if overrides == nil {
		return currentVal
	}
	if overrides.lookupMap != nil {
		if rule, ok := overrides.lookupMap[currentVal]; ok {
			atomic.AddInt64(&rule.appliedCount, 1)
			mlog.L().Info("config replacement applied",
				zap.String("plugin_tag", tag),
				zap.String("original", rule.Original),
				zap.String("new", rule.New),
				zap.String("comment", rule.Comment))
			return rule.New
		}
	}
	return currentVal
}

func applyECSOverrideString(currentVal string, overrides *GlobalOverrides) (string, bool) {
	if !strings.HasPrefix(currentVal, "ecs ") {
		return "", false
	}

	value := strings.TrimSpace(strings.TrimPrefix(currentVal, "ecs "))
	switch value {
	case domesticECSPlaceholder:
		if overrides == nil {
			return "ecs " + defaultECSOverride, true
		}
		return "ecs " + resolveECSOverrideValue(overrides.DomesticECS, overrides.ECS), true
	case foreignECSPlaceholder:
		if overrides == nil {
			return "ecs " + defaultECSOverride, true
		}
		return "ecs " + resolveECSOverrideValue(overrides.ForeignECS), true
	default:
		if overrides != nil && strings.TrimSpace(overrides.ECS) != "" {
			return "ecs " + strings.TrimSpace(overrides.ECS), true
		}
		return currentVal, true
	}
}

func resolveECSOverrideValue(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return defaultECSOverride
}

// applyRecursive is a generic function that traverses and modifies the config data structure.
func applyRecursive(tag string, data any, overrides *GlobalOverrides) any {
	if data == nil {
		return nil
	}

	switch v := data.(type) {
	case map[string]any:
		if overrides.Socks5 != "" {
			if _, ok := v["socks5"]; ok {
				v["socks5"] = overrides.Socks5
			}
		}
		for key, val := range v {
			v[key] = applyRecursive(tag, val, overrides)
		}
		return v
	case []any:
		for i, item := range v {
			v[i] = applyRecursive(tag, item, overrides)
		}
		return v
	case string:
		return ApplyOverrideString(tag, v, overrides)
	default:
		return data
	}
}

// GetCount returns the integer count of replacements.
func (r *ReplacementRule) GetCount() int64 {
	return atomic.LoadInt64(&r.appliedCount)
}
