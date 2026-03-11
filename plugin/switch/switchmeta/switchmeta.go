package switchmeta

import (
	"fmt"
	"strings"
)

type Definition struct {
	Name         string
	DefaultValue string
	aliases      map[string]string
}

func (d Definition) NormalizeValue(raw string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return d.DefaultValue, nil
	}
	if value, ok := d.aliases[key]; ok {
		return value, nil
	}
	return "", fmt.Errorf("unsupported value %q for switch %q", raw, d.Name)
}

var ordered = []Definition{
	{Name: "block_response", DefaultValue: "on", aliases: onOffAliases()},
	{Name: "client_proxy_mode", DefaultValue: "all", aliases: map[string]string{
		"all":       "all",
		"blacklist": "blacklist",
		"whitelist": "whitelist",
	}},
	{Name: "core_mode", DefaultValue: "secure", aliases: map[string]string{
		"compat":     "compat",
		"compatible": "compat",
		"secure":     "secure",
	}},
	{Name: "branch_cache", DefaultValue: "on", aliases: onOffAliases()},
	{Name: "block_query_type", DefaultValue: "on", aliases: onOffAliases()},
	{Name: "block_ipv6", DefaultValue: "off", aliases: onOffAliases()},
	{Name: "ad_block", DefaultValue: "off", aliases: onOffAliases()},
	{Name: "prefer_ipv4", DefaultValue: "off", aliases: onOffAliases()},
	{Name: "cn_answer_mode", DefaultValue: "realip", aliases: map[string]string{
		"realip": "realip",
		"fakeip": "fakeip",
	}},
	{Name: "prefer_ipv6", DefaultValue: "off", aliases: onOffAliases()},
	{Name: "main_cache", DefaultValue: "on", aliases: onOffAliases()},
	{Name: "udp_fast_path", DefaultValue: "on", aliases: onOffAliases()},
}

var byName map[string]Definition

func init() {
	byName = make(map[string]Definition, len(ordered))
	for _, def := range ordered {
		byName[def.Name] = def
	}
}

func Ordered() []Definition {
	out := make([]Definition, len(ordered))
	copy(out, ordered)
	return out
}

func Lookup(name string) (Definition, bool) {
	def, ok := byName[name]
	return def, ok
}

func MustLookup(name string) Definition {
	def, ok := Lookup(name)
	if !ok {
		panic("unknown switch definition: " + name)
	}
	return def
}

func onOffAliases() map[string]string {
	return map[string]string{
		"on":  "on",
		"off": "off",
	}
}
