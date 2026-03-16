package coremain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
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
	router.Get("/api/v1/reverse_lookup", handleReverseLookup(m))
}

func handleGetClientname(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, err := resolveJSONStoreController(m, "clientname")
		if err != nil {
			writeAPIError(w, http.StatusConflict, "clientname_ambiguous", err.Error())
			return
		}
		if controller == nil {
			writeAPIError(w, http.StatusNotFound, "clientname_not_found", "clientname controller not found")
			return
		}
		writeJSON(w, http.StatusOK, controller.SnapshotJSONValue())
	}
}

func handlePutClientname(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, err := resolveJSONStoreController(m, "clientname")
		if err != nil {
			writeAPIError(w, http.StatusConflict, "clientname_ambiguous", err.Error())
			return
		}
		if controller == nil {
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

func resolveJSONStoreController(m *Mosdns, preferredTag string) (JSONStoreController, error) {
	if m == nil {
		return nil, nil
	}
	if preferredTag != "" {
		controller, ok := m.GetPlugin(preferredTag).(JSONStoreController)
		if ok && controller != nil {
			return controller, nil
		}
	}
	tags := make([]string, 0, len(m.plugins))
	for tag, plugin := range m.plugins {
		controller, ok := plugin.(JSONStoreController)
		if !ok || controller == nil {
			continue
		}
		tags = append(tags, tag)
	}
	if len(tags) == 0 {
		return nil, nil
	}
	sort.Strings(tags)
	if len(tags) > 1 {
		return nil, fmt.Errorf("multiple clientname controllers found: %s", strings.Join(tags, ", "))
	}
	controller, _ := m.GetPlugin(tags[0]).(JSONStoreController)
	return controller, nil
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
