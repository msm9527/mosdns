package coremain

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestInitHttpMux_RootSupportsHeadRedirectForExternalUI(t *testing.T) {
	baseDir := t.TempDir()
	uiDir := filepath.Join(baseDir, "ui")
	if err := os.MkdirAll(uiDir, 0o755); err != nil {
		t.Fatalf("mkdir ui dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(uiDir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write ui index: %v", err)
	}

	m := NewTestMosdnsWithPluginsAndEnv(nil, RuntimeEnv{BaseDir: baseDir})
	m.initHttpMux()

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, "/", nil)
		rec := httptest.NewRecorder()
		m.GetAPIRouter().ServeHTTP(rec, req)

		if rec.Code != http.StatusFound {
			t.Fatalf("%s / status = %d, want %d", method, rec.Code, http.StatusFound)
		}
		if got := rec.Header().Get("Location"); got != "/ui/" {
			t.Fatalf("%s / location = %q, want %q", method, got, "/ui/")
		}
	}
}
