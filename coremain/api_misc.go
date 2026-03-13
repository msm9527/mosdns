package coremain

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

type JSONStoreController interface {
	SnapshotJSONValue() any
	ReplaceJSONValue(ctx context.Context, value any) error
}

type ReverseLookupController interface {
	LookupIPString(ip string) (string, error)
}

func RegisterMiscAPI(router *chi.Mux, m *Mosdns) {
	router.Get("/api/v1/clientname", handleGetClientname(m))
	router.Put("/api/v1/clientname", handlePutClientname(m))
	router.Get("/api/v1/reverse_lookup", handleReverseLookup(m))
}

func handleGetClientname(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := m.GetPlugin("clientname").(JSONStoreController)
		if !ok || controller == nil {
			writeAPIError(w, http.StatusNotFound, "clientname_not_found", "clientname controller not found")
			return
		}
		writeJSON(w, http.StatusOK, controller.SnapshotJSONValue())
	}
}

func handlePutClientname(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := m.GetPlugin("clientname").(JSONStoreController)
		if !ok || controller == nil {
			writeAPIError(w, http.StatusNotFound, "clientname_not_found", "clientname controller not found")
			return
		}

		var body any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request_body", "invalid request body")
			return
		}

		if err := controller.ReplaceJSONValue(r.Context(), body); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "clientname_update_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, controller.SnapshotJSONValue())
	}
}

func handleReverseLookup(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := m.GetPlugin("reverse_lookup").(ReverseLookupController)
		if !ok || controller == nil {
			writeAPIError(w, http.StatusNotFound, "reverse_lookup_not_found", "reverse lookup controller not found")
			return
		}

		ip := strings.TrimSpace(r.URL.Query().Get("ip"))
		if ip == "" {
			writeAPIError(w, http.StatusBadRequest, "missing_ip", "missing ip query parameter")
			return
		}

		domain, err := controller.LookupIPString(ip)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusBadRequest, "reverse_lookup_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ip":     ip,
			"domain": domain,
		})
	}
}
