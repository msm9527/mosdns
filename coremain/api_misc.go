package coremain

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

type ReverseLookupController interface {
	LookupIPString(ip string) (string, error)
}

func RegisterMiscAPI(router *chi.Mux, m *Mosdns) {
	router.Get("/api/v1/reverse_lookup", handleReverseLookup(m))
}

func handleGetClientname(_ *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		values, _, err := LoadClientNamesFromCustomConfig()
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "clientname_load_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, values)
	}
}

func handlePutClientname(_ *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request_body", "invalid request body")
			return
		}

		if err := SaveClientNamesToCustomConfig(body); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "clientname_update_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, body)
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
