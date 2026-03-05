package coremain

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONBodyStrict(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	t.Run("success", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok"}`))
		w := httptest.NewRecorder()

		var p payload
		if err := decodeJSONBodyStrict(w, req, &p, false); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Name != "ok" {
			t.Fatalf("unexpected payload: %+v", p)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok","unknown":1}`))
		w := httptest.NewRecorder()

		var p payload
		err := decodeJSONBodyStrict(w, req, &p, false)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("extra json value", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok"} {"name":"extra"}`))
		w := httptest.NewRecorder()

		var p payload
		err := decodeJSONBodyStrict(w, req, &p, false)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, errJSONBodyExtraValue) {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("allow empty", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", http.NoBody)
		w := httptest.NewRecorder()

		var p payload
		if err := decodeJSONBodyStrict(w, req, &p, true); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("too large", func(t *testing.T) {
		oversize := bytes.Repeat([]byte("a"), int(maxJSONBodyBytes)+1)
		body := `{"name":"` + string(oversize) + `"}`
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		w := httptest.NewRecorder()

		var p payload
		err := decodeJSONBodyStrict(w, req, &p, false)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, errJSONBodyTooLarge) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
