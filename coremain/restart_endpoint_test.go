package coremain

import (
	"os"
	"testing"
)

func TestBuildRestartEndpointFromHTTPAddr(t *testing.T) {
	tests := []struct {
		name     string
		httpAddr string
		want     string
	}{
		{name: "loopback", httpAddr: "127.0.0.1:19099", want: "http://127.0.0.1:19099/api/v1/system/restart"},
		{name: "wildcard", httpAddr: ":9099", want: "http://127.0.0.1:9099/api/v1/system/restart"},
		{name: "ipv6 wildcard", httpAddr: "[::]:9099", want: "http://127.0.0.1:9099/api/v1/system/restart"},
		{name: "ipv6 loopback", httpAddr: "[::1]:9099", want: "http://[::1]:9099/api/v1/system/restart"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := buildRestartEndpointFromHTTPAddr(tc.httpAddr); got != tc.want {
				t.Fatalf("buildRestartEndpointFromHTTPAddr() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveRestartEndpointPrefersConfiguredAndEnv(t *testing.T) {
	ClearConfiguredRestartEndpoint()
	t.Cleanup(ClearConfiguredRestartEndpoint)

	SetConfiguredRestartEndpointFromHTTPAddr("127.0.0.1:18080")
	if got := ResolveRestartEndpoint(DefaultRestartEndpoint); got != "http://127.0.0.1:18080/api/v1/system/restart" {
		t.Fatalf("ResolveRestartEndpoint() = %q", got)
	}

	if err := os.Setenv("MOSDNS_RESTART_ENDPOINT", "http://127.0.0.1:28080/api/v1/system/restart"); err != nil {
		t.Fatalf("Setenv() error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("MOSDNS_RESTART_ENDPOINT")
	})

	if got := ResolveRestartEndpoint(DefaultRestartEndpoint); got != "http://127.0.0.1:28080/api/v1/system/restart" {
		t.Fatalf("ResolveRestartEndpoint() with env = %q", got)
	}
}
