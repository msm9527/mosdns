package coremain

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeDNSUpstreamAddr(t *testing.T) {
	t.Run("uses selected protocol for bare host", func(t *testing.T) {
		cases := []struct {
			name     string
			protocol string
			addr     string
			want     string
		}{
			{name: "tcp ipv4", protocol: "tcp", addr: "8.8.8.8", want: "tcp://8.8.8.8"},
			{name: "tls ipv6", protocol: "tls", addr: "2001:db8::1", want: "tls://[2001:db8::1]"},
			{name: "https ipv6 with path", protocol: "https", addr: "2001:db8::1/dns-query", want: "https://[2001:db8::1]/dns-query"},
			{name: "udp ipv6", protocol: "udp", addr: "2001:db8::1", want: "2001:db8::1"},
		}

		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				got, err := normalizeDNSUpstreamAddr(tc.protocol, tc.addr)
				if err != nil {
					t.Fatalf("normalizeDNSUpstreamAddr: %v", err)
				}
				if got != tc.want {
					t.Fatalf("unexpected addr: got %q want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("rejects scheme mismatch", func(t *testing.T) {
		_, err := normalizeDNSUpstreamAddr("tcp", "https://dns.google/dns-query")
		if err == nil || !strings.Contains(err.Error(), "does not match protocol") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects invalid ipv6", func(t *testing.T) {
		_, err := normalizeDNSUpstreamAddr("udp", "2400:3200:1")
		if err == nil || !strings.Contains(err.Error(), "invalid IPv6 address") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestHandleSetUpstreamConfigWithMosdns_NormalizesAddr(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	upstreamOverridesLock.Lock()
	oldOverrides := upstreamOverrides
	upstreamOverrides = nil
	upstreamOverridesLock.Unlock()

	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
		upstreamOverridesLock.Lock()
		upstreamOverrides = oldOverrides
		upstreamOverridesLock.Unlock()
	})

	m := NewTestMosdnsWithPlugins(map[string]any{
		"test_plugin": &testRuntimeReloader{},
	})

	reqBody := `{"plugin_tag":"test_plugin","upstreams":[{"tag":"u1","enabled":true,"protocol":"tcp","addr":"2001:db8::1"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upstream/config", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleSetUpstreamConfigWithMosdns(w, req, m)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status code: got %d, body=%s", w.Code, w.Body.String())
	}

	cfg, ok, err := loadUpstreamOverridesFromCustomConfig()
	if err != nil {
		t.Fatalf("loadUpstreamOverridesFromCustomConfig: %v", err)
	}
	item := cfg["test_plugin"][0]
	if !ok || item.Protocol != "tcp" || item.Addr != "tcp://[2001:db8::1]" {
		t.Fatalf("unexpected upstream config: %+v", cfg)
	}
}

func TestHandleReplaceUpstreamConfigWithMosdns_RejectsInvalidIPv6(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	upstreamOverridesLock.Lock()
	oldOverrides := upstreamOverrides
	upstreamOverrides = nil
	upstreamOverridesLock.Unlock()

	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
		upstreamOverridesLock.Lock()
		upstreamOverrides = oldOverrides
		upstreamOverridesLock.Unlock()
	})

	reqBody := `{
		"config": {
			"test_plugin": [
				{"tag":"u1","enabled":true,"protocol":"udp","addr":"2400:3200:1"}
			]
		},
		"apply": false
	}`
	req := httptest.NewRequest(http.MethodPut, "/api/v1/upstream/config", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleReplaceUpstreamConfigWithMosdns(w, req, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status code: got %d, body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":"UPSTREAM_ADDR_INVALID"`) {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "invalid IPv6 address") {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
}
