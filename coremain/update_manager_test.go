package coremain

import (
	"net/http"
	"os"
	"path/filepath"
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

func TestGetHttpClientForUpdatePrefersRuntimeOverrides(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := saveGlobalOverridesToRuntimeStore(&GlobalOverrides{Socks5: "127.0.0.1:1080"}); err != nil {
		t.Fatalf("saveGlobalOverridesToRuntimeStore: %v", err)
	}

	m := &UpdateManager{httpClient: &http.Client{}}
	client, isProxy, err := m.getHttpClientForUpdate()
	if err != nil {
		t.Fatalf("getHttpClientForUpdate() error = %v", err)
	}
	if !isProxy {
		t.Fatal("expected proxy client from runtime overrides")
	}
	if client == nil || client.Transport == nil {
		t.Fatalf("unexpected proxy client: %#v", client)
	}
}

func TestGetHttpClientForUpdateFallsBackToFile(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	file := filepath.Join(MainConfigBaseDir, overridesFilename)
	if err := os.WriteFile(file, []byte(`{"socks5":"127.0.0.1:1080"}`), 0o644); err != nil {
		t.Fatalf("write overrides file: %v", err)
	}

	m := &UpdateManager{httpClient: &http.Client{}}
	client, isProxy, err := m.getHttpClientForUpdate()
	if err != nil {
		t.Fatalf("getHttpClientForUpdate() error = %v", err)
	}
	if !isProxy {
		t.Fatal("expected proxy client from overrides file")
	}
	if client == nil || client.Transport == nil {
		t.Fatalf("unexpected proxy client: %#v", client)
	}
}
