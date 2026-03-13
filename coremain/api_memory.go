package coremain

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

type MemoryRuntimeController interface {
	DomainStatsProvider
	WriteEntries(w http.ResponseWriter, query string, offset, limit int) error
	SaveToDisk(ctx context.Context) error
	FlushRuntime(ctx context.Context) error
	MarkDomainVerified(ctx context.Context, domain, verifiedAt string) (int, error)
}

type verifyMemoryRequest struct {
	Domain     string `json:"domain"`
	VerifiedAt string `json:"verified_at,omitempty"`
}

func RegisterMemoryAPI(router *chi.Mux, m *Mosdns) {
	router.Get("/api/v1/memory/{tag}/stats", handleMemoryStatsByTag(m))
	router.Get("/api/v1/memory/{tag}/entries", handleMemoryEntriesByTag(m))
	router.Post("/api/v1/memory/{tag}/save", handleMemorySaveByTag(m))
	router.Post("/api/v1/memory/{tag}/flush", handleMemoryFlushByTag(m))
	router.Post("/api/v1/memory/{tag}/verify", handleMemoryVerifyByTag(m))
}

func handleMemoryStatsByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := memoryControllerByTag(m, chi.URLParam(r, "tag"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "memory_not_found", "memory plugin not found")
			return
		}
		writeJSON(w, http.StatusOK, controller.SnapshotDomainStats())
	}
}

func handleMemoryEntriesByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := memoryControllerByTag(m, chi.URLParam(r, "tag"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "memory_not_found", "memory plugin not found")
			return
		}

		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if err := controller.WriteEntries(w, r.URL.Query().Get("q"), offset, limit); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "memory_entries_failed", err)
			return
		}
	}
}

func handleMemorySaveByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := memoryControllerByTag(m, chi.URLParam(r, "tag"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "memory_not_found", "memory plugin not found")
			return
		}
		if err := controller.SaveToDisk(r.Context()); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "memory_save_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": "记忆库已保存。", "tag": chi.URLParam(r, "tag")})
	}
}

func handleMemoryFlushByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := memoryControllerByTag(m, chi.URLParam(r, "tag"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "memory_not_found", "memory plugin not found")
			return
		}
		if err := controller.FlushRuntime(r.Context()); err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "memory_flush_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": "记忆库已清空。", "tag": chi.URLParam(r, "tag")})
	}
}

func handleMemoryVerifyByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := memoryControllerByTag(m, chi.URLParam(r, "tag"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "memory_not_found", "memory plugin not found")
			return
		}

		var body verifyMemoryRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Domain == "" {
			writeAPIError(w, http.StatusBadRequest, "invalid_request_body", "invalid request body")
			return
		}

		updated, err := controller.MarkDomainVerified(r.Context(), body.Domain, body.VerifiedAt)
		if err != nil {
			status := http.StatusBadRequest
			if err.Error() == "domain not found" {
				status = http.StatusNotFound
			}
			writeAPIErrorFromErr(w, status, "memory_verify_failed", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "success", "updated": updated})
	}
}

func memoryControllerByTag(m *Mosdns, tag string) (MemoryRuntimeController, bool) {
	controller, ok := m.GetPlugin(tag).(MemoryRuntimeController)
	return controller, ok && controller != nil
}
