package coremain

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type mockListController struct {
	text      string
	values    []string
	replaced  int
	replaceErr error
}

func (m *mockListController) WriteListContent(w http.ResponseWriter, query string, offset, limit int) error {
	_, _ = w.Write([]byte(m.text))
	return nil
}

func (m *mockListController) ReplaceListRuntime(ctx context.Context, values []string) (int, error) {
	m.values = append([]string(nil), values...)
	return m.replaced, m.replaceErr
}

func TestListsAPI_Get(t *testing.T) {
	m := &Mosdns{plugins: map[string]any{"whitelist": &mockListController{text: "a\nb\n"}}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/lists/whitelist", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tag", "whitelist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handleGetListContent(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rr.Code)
	}
	if rr.Body.String() != "a\nb\n" {
		t.Fatalf("unexpected body %q", rr.Body.String())
	}
}

func TestListsAPI_Put(t *testing.T) {
	controller := &mockListController{replaced: 2}
	m := &Mosdns{plugins: map[string]any{"whitelist": controller}}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/lists/whitelist", bytes.NewBufferString(`{"values":["a","b"]}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tag", "whitelist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handleReplaceListContent(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rr.Code)
	}
	if len(controller.values) != 2 || controller.values[0] != "a" || controller.values[1] != "b" {
		t.Fatalf("unexpected replaced values %#v", controller.values)
	}
	if !strings.Contains(rr.Body.String(), `"replaced":2`) {
		t.Fatalf("unexpected body %q", rr.Body.String())
	}
}

