package server

import "testing"

func TestRequestSourceClassification(t *testing.T) {
	for _, tc := range []struct {
		source     RequestSource
		name       string
		background bool
	}{
		{RequestSourceUnspecified, "", false},
		{RequestSourceUser, "user", false},
		{RequestSourcePrewarm, "prewarm", true},
		{RequestSourceRefresh, "refresh", true},
	} {
		if tc.source.String() != tc.name || tc.source.IsBackground() != tc.background {
			t.Errorf("source %d = %q/%v, want %q/%v", tc.source, tc.source.String(), tc.source.IsBackground(), tc.name, tc.background)
		}
	}
}
