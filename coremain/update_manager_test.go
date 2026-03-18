package coremain

import (
	"context"
	"net/http"
	"testing"
)

func TestNormalizeVersion(t *testing.T) {
	cases := map[string]string{
		"msm-v5.0.7": "5.0.7",
		"v5.0.7":     "5.0.7",
		"5.0.7":      "5.0.7",
		"Release msm-v5.0.7 | 来源: mosdns-v5 + ph-v5": "5.0.7",
		"DEV": "dev",
	}

	for input, want := range cases {
		if got := normalizeVersion(input); got != want {
			t.Fatalf("normalizeVersion(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCanonicalReleaseTag(t *testing.T) {
	cases := map[string]string{
		"msm-v5.0.7": "msm-v5.0.7",
		"v5.0.7":     "msm-v5.0.7",
		"5.0.7":      "msm-v5.0.7",
		"dev":        "",
		"":           "",
	}

	for input, want := range cases {
		if got := canonicalReleaseTag(input); got != want {
			t.Fatalf("canonicalReleaseTag(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPreferredReleaseTagUsesCanonicalTag(t *testing.T) {
	m := &UpdateManager{currentVersion: "5.0.7"}
	if got := m.preferredReleaseTag(); got != "msm-v5.0.7" {
		t.Fatalf("preferredReleaseTag() = %q, want %q", got, "msm-v5.0.7")
	}
}

func TestGetHttpClientForUpdatePrefersCustomConfig(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := saveGlobalOverridesToCustomConfig(&GlobalOverrides{Socks5: "127.0.0.1:1080"}); err != nil {
		t.Fatalf("saveGlobalOverridesToCustomConfig: %v", err)
	}

	m := &UpdateManager{httpClient: &http.Client{}}
	client, isProxy, err := m.getHttpClientForUpdate()
	if err != nil {
		t.Fatalf("getHttpClientForUpdate() error = %v", err)
	}
	if !isProxy {
		t.Fatal("expected proxy client from custom config")
	}
	if client == nil || client.Transport == nil {
		t.Fatalf("unexpected proxy client: %#v", client)
	}
}

func TestGetHttpClientForUpdateFallsBackToDirect(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	m := &UpdateManager{httpClient: &http.Client{}}
	client, isProxy, err := m.getHttpClientForUpdate()
	if err != nil {
		t.Fatalf("getHttpClientForUpdate() error = %v", err)
	}
	if isProxy {
		t.Fatal("expected direct client without runtime overrides")
	}
	if client == nil || client != m.httpClient {
		t.Fatalf("unexpected proxy client: %#v", client)
	}
}

func TestSelectAssetNoMatchReturnsNil(t *testing.T) {
	assets := []githubAsset{
		{Name: "foo.zip"},
		{Name: "bar.zip"},
	}
	if got := selectAsset(assets); got != nil {
		t.Fatalf("selectAsset() = %#v, want nil", got)
	}
}

func TestTriggerPostUpgradeHookUsesRuntimeScheduler(t *testing.T) {
	var gotDelay int
	cleanup := registerInternalRestartScheduler(func(delayMs int) error {
		gotDelay = delayMs
		return nil
	})
	defer cleanup()

	m := &UpdateManager{httpClient: &http.Client{}}
	if err := m.triggerPostUpgradeHook(context.Background()); err != nil {
		t.Fatalf("triggerPostUpgradeHook() error = %v", err)
	}
	if gotDelay != 500 {
		t.Fatalf("triggerPostUpgradeHook() delay = %d, want %d", gotDelay, 500)
	}
}
