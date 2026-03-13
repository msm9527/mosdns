package coremain

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

type ListContentController interface {
	WriteListContent(w http.ResponseWriter, query string, offset, limit int) error
	ReplaceListRuntime(ctx context.Context, values []string) (int, error)
}

type listPayload struct {
	Values []string `json:"values"`
}

func RegisterListsAPI(router *chi.Mux, m *Mosdns) {
	router.Get("/api/v1/lists/{tag}", handleGetListContent(m))
	router.Put("/api/v1/lists/{tag}", handleReplaceListContent(m))
}

func handleGetListContent(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := m.GetPlugin(chi.URLParam(r, "tag")).(ListContentController)
		if !ok || controller == nil {
			writeAPIError(w, http.StatusNotFound, "list_not_found", "list plugin not found")
			return
		}
		if err := controller.WriteListContent(w, r.URL.Query().Get("q"), 0, 0); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "list_read_failed", err.Error())
			return
		}
	}
}

func handleReplaceListContent(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := m.GetPlugin(chi.URLParam(r, "tag")).(ListContentController)
		if !ok || controller == nil {
			writeAPIError(w, http.StatusNotFound, "list_not_found", "list plugin not found")
			return
		}

		var body listPayload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request_body", "invalid request body")
			return
		}

		replaced, err := controller.ReplaceListRuntime(r.Context(), body.Values)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "list_replace_failed", err.Error())
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"message":  "列表已保存。",
			"replaced": replaced,
			"tag":      chi.URLParam(r, "tag"),
		})
	}
}

