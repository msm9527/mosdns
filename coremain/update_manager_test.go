package coremain

import "testing"

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
