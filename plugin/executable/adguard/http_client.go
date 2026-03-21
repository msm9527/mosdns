package adguard_rule

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

func cloneArgs(src *Args) *Args {
	if src == nil {
		return &Args{}
	}
	return &Args{Socks5: src.Socks5, ConfigFile: src.ConfigFile}
}

func newHTTPClient(socks5 string) (*http.Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if strings.TrimSpace(socks5) == "" {
		return &http.Client{Timeout: syncTimeout, Transport: transport}, nil
	}
	dialer, err := proxy.SOCKS5("tcp", socks5, nil, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("%s: create socks5 dialer: %w", PluginType, err)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("%s: socks5 dialer does not support context", PluginType)
	}
	transport.DialContext = contextDialer.DialContext
	transport.Proxy = nil
	return &http.Client{Timeout: syncTimeout, Transport: transport}, nil
}
