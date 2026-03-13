package coremain

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

type CacheRuntimeController interface {
	CacheStatsProvider
	CacheEntries(query string, offset, limit int) ([]CacheEntry, int, error)
	SaveToDisk(ctx context.Context) error
	FlushRuntime(ctx context.Context) error
	PurgeDomainRuntime(ctx context.Context, qname string, qtype uint16) (int, error)
}

type CacheEntry struct {
	Key         string `json:"key"`
	DomainSet   string `json:"domain_set,omitempty"`
	StoredTime  string `json:"stored_time"`
	MsgExpire   string `json:"msg_expire"`
	CacheExpire string `json:"cache_expire"`
	DNSMessage  string `json:"dns_message"`
}

type CacheEntriesResponse struct {
	Tag    string       `json:"tag"`
	Total  int          `json:"total"`
	Offset int          `json:"offset"`
	Limit  int          `json:"limit"`
	Items  []CacheEntry `json:"items"`
}

type purgeDomainRequest struct {
	QName string `json:"qname"`
	QType uint16 `json:"qtype,omitempty"`
}

type purgeDomainResponse struct {
	QName  string `json:"qname"`
	QType  uint16 `json:"qtype,omitempty"`
	Purged int    `json:"purged"`
}

func RegisterCacheAPI(router *chi.Mux, m *Mosdns) {
	router.Get("/api/v1/cache/{tag}/stats", handleCacheStatsByTag(m))
	router.Get("/api/v1/cache/{tag}/entries", handleCacheEntriesByTag(m))
	router.Post("/api/v1/cache/{tag}/save", handleCacheSaveByTag(m))
	router.Post("/api/v1/cache/{tag}/flush", handleCacheFlushByTag(m))
	router.Post("/api/v1/cache/{tag}/purge_domain", handleCachePurgeDomainByTag(m))
}

func handleCacheStatsByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := cacheControllerByTag(m, chi.URLParam(r, "tag"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "cache_not_found", "cache plugin not found")
			return
		}

		writeJSON(w, http.StatusOK, controller.SnapshotCacheStats())
	}
}

func handleCacheEntriesByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tag := chi.URLParam(r, "tag")
		controller, ok := cacheControllerByTag(m, tag)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "cache_not_found", "cache plugin not found")
			return
		}

		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		items, total, err := controller.CacheEntries(r.URL.Query().Get("q"), offset, limit)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "cache_entries_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, CacheEntriesResponse{
			Tag:    tag,
			Total:  total,
			Offset: offset,
			Limit:  limit,
			Items:  items,
		})
	}
}

func handleCacheFlushByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := cacheControllerByTag(m, chi.URLParam(r, "tag"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "cache_not_found", "cache plugin not found")
			return
		}
		if err := controller.FlushRuntime(r.Context()); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "cache_flush_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": "缓存已清空并触发后台持久化。"})
	}
}

func handleCacheSaveByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tag := chi.URLParam(r, "tag")
		controller, ok := cacheControllerByTag(m, tag)
		if !ok {
			writeAPIError(w, http.StatusNotFound, "cache_not_found", "cache plugin not found")
			return
		}
		if err := controller.SaveToDisk(r.Context()); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "cache_save_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": "缓存已保存。", "tag": tag})
	}
}

func handleCachePurgeDomainByTag(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		controller, ok := cacheControllerByTag(m, chi.URLParam(r, "tag"))
		if !ok {
			writeAPIError(w, http.StatusNotFound, "cache_not_found", "cache plugin not found")
			return
		}

		var body purgeDomainRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request_body", "invalid request body")
			return
		}

		purged, err := controller.PurgeDomainRuntime(r.Context(), body.QName, body.QType)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "cache_purge_failed", err.Error())
			return
		}

		writeJSON(w, http.StatusOK, purgeDomainResponse{
			QName:  body.QName,
			QType:  body.QType,
			Purged: purged,
		})
	}
}

func cacheControllerByTag(m *Mosdns, tag string) (CacheRuntimeController, bool) {
	controller, ok := m.GetPlugin(tag).(CacheRuntimeController)
	return controller, ok && controller != nil
}
