package coremain

import (
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
)

const (
	defaultRestartHTTPAddr = "127.0.0.1:9099"
	defaultRestartPath     = "/api/v1/system/restart"
)

var (
	DefaultRestartEndpoint = buildRestartEndpointFromHTTPAddr(defaultRestartHTTPAddr)

	configuredRestartEndpointMu sync.RWMutex
	configuredRestartEndpoint   string
)

func SetConfiguredRestartEndpointFromHTTPAddr(httpAddr string) {
	configuredRestartEndpointMu.Lock()
	configuredRestartEndpoint = buildRestartEndpointFromHTTPAddr(httpAddr)
	configuredRestartEndpointMu.Unlock()
}

func ClearConfiguredRestartEndpoint() {
	configuredRestartEndpointMu.Lock()
	configuredRestartEndpoint = ""
	configuredRestartEndpointMu.Unlock()
}

func ResolveRestartEndpoint(fallbackEndpoint string) string {
	if endpoint := strings.TrimSpace(os.Getenv("MOSDNS_RESTART_ENDPOINT")); endpoint != "" {
		return endpoint
	}
	if endpoint := currentConfiguredRestartEndpoint(); endpoint != "" {
		return endpoint
	}
	return fallbackEndpoint
}

func currentConfiguredRestartEndpoint() string {
	configuredRestartEndpointMu.RLock()
	defer configuredRestartEndpointMu.RUnlock()
	return configuredRestartEndpoint
}

func buildRestartEndpointFromHTTPAddr(httpAddr string) string {
	host, port, ok := splitRestartHTTPAddr(httpAddr)
	if !ok {
		return ""
	}
	return (&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(normalizeRestartEndpointHost(host), port),
		Path:   defaultRestartPath,
	}).String()
}

func splitRestartHTTPAddr(httpAddr string) (string, string, bool) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(httpAddr))
	if err != nil {
		return "", "", false
	}
	return strings.Trim(host, "[]"), port, true
}

func normalizeRestartEndpointHost(host string) string {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	if trimmed == "" {
		return "127.0.0.1"
	}
	if ip := net.ParseIP(trimmed); ip != nil && ip.IsUnspecified() {
		return "127.0.0.1"
	}
	return trimmed
}
