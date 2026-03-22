package coremain

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const (
	upstreamProtocolUDP    = "udp"
	upstreamProtocolTCP    = "tcp"
	upstreamProtocolTLS    = "tls"
	upstreamProtocolHTTPS  = "https"
	upstreamProtocolQUIC   = "quic"
	upstreamProtocolAliAPI = "aliapi"
	upstreamAddrInvalid    = "UPSTREAM_ADDR_INVALID"
	upstreamProtoInvalid   = "UPSTREAM_PROTOCOL_INVALID"
)

func normalizeGlobalUpstreamConfig(cfg GlobalUpstreamOverrides) (GlobalUpstreamOverrides, string, string, bool) {
	normalized := make(GlobalUpstreamOverrides, len(cfg))
	for pluginTag, upstreams := range cfg {
		tag := strings.TrimSpace(pluginTag)
		if tag == "" {
			return nil, "PLUGIN_TAG_REQUIRED", "plugin_tag is required", false
		}
		items, code, msg, ok := normalizeUpstreamList(upstreams)
		if !ok {
			return nil, code, msg, false
		}
		normalized[tag] = items
	}
	return normalized, "", "", true
}

func normalizeUpstreamList(upstreams []UpstreamOverrideConfig) ([]UpstreamOverrideConfig, string, string, bool) {
	tagSeen := make(map[string]struct{}, len(upstreams))
	normalized := make([]UpstreamOverrideConfig, 0, len(upstreams))
	for i, u := range upstreams {
		item, code, msg, ok := normalizeUpstreamEntry(u, i)
		if !ok {
			return nil, code, msg, false
		}
		if _, duplicated := tagSeen[item.Tag]; duplicated {
			return nil, "UPSTREAM_TAG_DUPLICATED", fmt.Sprintf("duplicated upstream tag: %s", item.Tag), false
		}
		tagSeen[item.Tag] = struct{}{}
		normalized = append(normalized, item)
	}
	return normalized, "", "", true
}

func normalizeUpstreamEntry(u UpstreamOverrideConfig, idx int) (UpstreamOverrideConfig, string, string, bool) {
	itemPos := idx + 1
	u.Tag = strings.TrimSpace(u.Tag)
	u.Protocol = strings.TrimSpace(strings.ToLower(u.Protocol))
	u.Addr = strings.TrimSpace(u.Addr)
	if u.Tag == "" {
		return u, "UPSTREAM_TAG_REQUIRED", fmt.Sprintf("Item #%d: tag (name) is required", itemPos), false
	}

	protocol, err := resolveUpstreamProtocol(u.Protocol, u.Addr)
	if err != nil {
		return u, upstreamProtoInvalid, fmt.Sprintf("Item #%d (%s): %v", itemPos, u.Tag, err), false
	}
	u.Protocol = protocol
	if !u.Enabled {
		return u, "", "", true
	}
	if protocol == upstreamProtocolAliAPI {
		if strings.TrimSpace(u.AccountID) == "" || strings.TrimSpace(u.AccessKeyID) == "" || strings.TrimSpace(u.AccessKeySecret) == "" {
			return u, "ALIAPI_CREDENTIALS_REQUIRED", fmt.Sprintf("Item #%d (%s): AliAPI requires account_id, access_key_id, and access_key_secret", itemPos, u.Tag), false
		}
		return u, "", "", true
	}
	if u.Addr == "" {
		return u, "UPSTREAM_ADDR_REQUIRED", fmt.Sprintf("Item #%d (%s): addr is required for DNS types", itemPos, u.Tag), false
	}

	addr, err := normalizeDNSUpstreamAddr(protocol, u.Addr)
	if err != nil {
		return u, upstreamAddrInvalid, fmt.Sprintf("Item #%d (%s): %v", itemPos, u.Tag, err), false
	}
	u.Addr = addr
	return u, "", "", true
}

func resolveUpstreamProtocol(rawProtocol, addr string) (string, error) {
	if raw := strings.TrimSpace(rawProtocol); raw != "" {
		return canonicalUpstreamProtocol(raw)
	}
	if scheme, _, ok := splitUpstreamScheme(addr); ok {
		return canonicalUpstreamProtocol(scheme)
	}
	return upstreamProtocolUDP, nil
}

func canonicalUpstreamProtocol(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", upstreamProtocolUDP:
		return upstreamProtocolUDP, nil
	case upstreamProtocolTCP, "tcp+pipeline":
		return upstreamProtocolTCP, nil
	case upstreamProtocolTLS, "dot", "tls+pipeline":
		return upstreamProtocolTLS, nil
	case upstreamProtocolHTTPS, "doh", "h3":
		return upstreamProtocolHTTPS, nil
	case upstreamProtocolQUIC, "doq":
		return upstreamProtocolQUIC, nil
	case upstreamProtocolAliAPI:
		return upstreamProtocolAliAPI, nil
	default:
		return "", fmt.Errorf("unsupported protocol %q", raw)
	}
}

func normalizeDNSUpstreamAddr(protocol, raw string) (string, error) {
	addr := strings.TrimSpace(raw)
	scheme, rest, hasScheme := splitUpstreamScheme(addr)
	if hasScheme {
		addrProtocol, err := canonicalUpstreamProtocol(scheme)
		if err != nil {
			return "", fmt.Errorf("unsupported addr scheme %q", scheme)
		}
		if addrProtocol != protocol {
			return "", fmt.Errorf("addr scheme %q does not match protocol %q", scheme, protocol)
		}
		return buildDNSUpstreamAddr(protocol, rest, true)
	}
	return buildDNSUpstreamAddr(protocol, addr, false)
}

func splitUpstreamScheme(raw string) (string, string, bool) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(raw), "://")
	if !ok {
		return "", raw, false
	}
	return scheme, rest, true
}

func buildDNSUpstreamAddr(protocol, raw string, schemeExplicit bool) (string, error) {
	if protocol == upstreamProtocolHTTPS || protocol == upstreamProtocolQUIC {
		return buildURLDNSUpstreamAddr(protocol, raw)
	}
	authority, err := normalizeDNSAuthority(raw)
	if err != nil {
		return "", err
	}
	if suffix := splitAuthoritySuffix(raw); suffix != "" {
		return "", fmt.Errorf("protocol %q does not support path or query in addr", protocol)
	}
	if protocol == upstreamProtocolUDP && !schemeExplicit {
		return authority, nil
	}
	return protocol + "://" + formatDNSAuthority(authority, true), nil
}

func buildURLDNSUpstreamAddr(protocol, raw string) (string, error) {
	authority, suffix := splitDNSAuthorityAndSuffix(raw)
	normalized, err := normalizeDNSAuthority(authority)
	if err != nil {
		return "", err
	}
	return protocol + "://" + formatDNSAuthority(normalized, true) + suffix, nil
}

func splitAuthoritySuffix(raw string) string {
	for i, r := range strings.TrimSpace(raw) {
		switch r {
		case '/', '?', '#':
			return strings.TrimSpace(raw)[i:]
		}
	}
	return ""
}

func splitDNSAuthorityAndSuffix(raw string) (string, string) {
	suffix := splitAuthoritySuffix(raw)
	if suffix == "" {
		return strings.TrimSpace(raw), ""
	}
	trimmed := strings.TrimSpace(raw)
	return trimmed[:len(trimmed)-len(suffix)], suffix
}

func normalizeDNSAuthority(raw string) (string, error) {
	authority := strings.TrimSpace(raw)
	if authority == "" {
		return "", fmt.Errorf("addr is empty")
	}
	base := authority
	if suffix := splitAuthoritySuffix(authority); suffix != "" {
		base = authority[:len(authority)-len(suffix)]
	}
	host, port, hasPort, err := parseDNSHostPort(base)
	if err != nil {
		return "", err
	}
	if hasPort {
		return net.JoinHostPort(host, port), nil
	}
	return host, nil
}

func parseDNSHostPort(raw string) (string, string, bool, error) {
	hostPort := strings.TrimSpace(raw)
	if hostPort == "" {
		return "", "", false, fmt.Errorf("addr is empty")
	}
	if host, port, err := net.SplitHostPort(hostPort); err == nil {
		normalizedHost, err := normalizeDNSHost(host)
		if err != nil {
			return "", "", false, err
		}
		normalizedPort, err := normalizePort(port)
		if err != nil {
			return "", "", false, err
		}
		return normalizedHost, normalizedPort, true, nil
	}
	if strings.HasPrefix(hostPort, "[") && strings.HasSuffix(hostPort, "]") {
		host, err := normalizeDNSHost(strings.TrimSuffix(strings.TrimPrefix(hostPort, "["), "]"))
		if err != nil {
			return "", "", false, err
		}
		return host, "", false, nil
	}
	if addr, err := netip.ParseAddr(hostPort); err == nil {
		return addr.String(), "", false, nil
	}
	if strings.Count(hostPort, ":") > 1 {
		return "", "", false, fmt.Errorf("invalid IPv6 address %q", hostPort)
	}
	return hostPort, "", false, nil
}

func normalizeDNSHost(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", fmt.Errorf("host is empty")
	}
	if !strings.Contains(host, ":") {
		return host, nil
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "", fmt.Errorf("invalid IPv6 address %q", host)
	}
	return addr.String(), nil
}

func normalizePort(raw string) (string, error) {
	port := strings.TrimSpace(raw)
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return "", fmt.Errorf("invalid port %q", raw)
	}
	return strconv.FormatUint(n, 10), nil
}

func formatDNSAuthority(authority string, wrapIPv6 bool) string {
	host, port, err := net.SplitHostPort(authority)
	if err == nil {
		return net.JoinHostPort(host, port)
	}
	if wrapIPv6 && strings.Contains(authority, ":") {
		return "[" + authority + "]"
	}
	return authority
}
