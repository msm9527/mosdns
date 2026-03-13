package coremain

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type mockListController struct {
	items      []ListEntry
	total      int
	values     []string
	replaced   int
	replaceErr error
}

func (m *mockListController) ListEntries(query string, offset, limit int) ([]ListEntry, int, error) {
	return m.items, m.total, nil
}

func (m *mockListController) ReplaceListRuntime(ctx context.Context, values []string) (int, error) {
	m.values = append([]string(nil), values...)
	return m.replaced, m.replaceErr
}

func TestListsAPI_Get(t *testing.T) {
	m := &Mosdns{plugins: map[string]any{"whitelist": &mockListController{items: []ListEntry{{Value: "a"}, {Value: "b"}}, total: 2}}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/lists/whitelist", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tag", "whitelist")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rr := httptest.NewRecorder()

	handleGetListContent(m).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rr.Code)
	}
	var body ListEntriesResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Total != 2 || len(body.Items) != 2 || body.Items[0].Value != "a" || body.Items[1].Value != "b" {
		t.Fatalf("unexpected body %#v", body)
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
