package coremain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

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

type purgeDomainsRequest struct {
	Tags           []string `json:"tags,omitempty"`
	Domains        []string `json:"domains"`
	QTypes         []uint16 `json:"qtypes,omitempty"`
	IncludeUDPFast bool     `json:"include_udp_fast,omitempty"`
}

type flushAllRequest struct {
	Tags           []string `json:"tags,omitempty"`
	IncludeUDPFast bool     `json:"include_udp_fast,omitempty"`
}

type runtimeCacheActionItem struct {
	Tag    string `json:"tag"`
	Kind   string `json:"kind"`
	OK     bool   `json:"ok"`
	Purged int    `json:"purged,omitempty"`
	Error  string `json:"error,omitempty"`
}

type runtimeCacheActionResponse struct {
	Success int                      `json:"success"`
	Failed  int                      `json:"failed"`
	Total   int                      `json:"total"`
	Items   []runtimeCacheActionItem `json:"items"`
}

func RegisterCacheAPI(router *chi.Mux, m *Mosdns) {
	router.Get("/api/v1/cache/{tag}/stats", handleCacheStatsByTag(m))
	router.Get("/api/v1/cache/{tag}/entries", handleCacheEntriesByTag(m))
	router.Post("/api/v1/cache/{tag}/save", handleCacheSaveByTag(m))
	router.Post("/api/v1/cache/{tag}/flush", handleCacheFlushByTag(m))
	router.Post("/api/v1/cache/{tag}/purge_domain", handleCachePurgeDomainByTag(m))
	router.Post("/api/v1/cache/purge_domains", handleCachePurgeDomains(m))
	router.Post("/api/v1/cache/flush_all", handleCacheFlushAll(m))
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

type runtimeCacheTarget struct {
	tag        string
	controller RuntimeCacheController
}

func runtimeCacheTargets(m *Mosdns, tags []string, includeUDPFast bool) ([]runtimeCacheTarget, error) {
	targetMap := make(map[string]RuntimeCacheController)
	if len(tags) > 0 {
		for _, rawTag := range tags {
			tag := strings.TrimSpace(rawTag)
			if tag == "" {
				continue
			}
			controller, ok := m.GetPlugin(tag).(RuntimeCacheController)
			if !ok || controller == nil {
				return nil, errors.New("runtime cache plugin not found: " + tag)
			}
			targetMap[tag] = controller
		}
	} else {
		for tag, plugin := range m.SnapshotPlugins() {
			controller, ok := plugin.(RuntimeCacheController)
			if !ok || controller == nil {
				continue
			}
			if controller.RuntimeCacheKind() == "udp_fast" && !includeUDPFast {
				continue
			}
			if controller.RuntimeCacheKind() != "response" && controller.RuntimeCacheKind() != "udp_fast" {
				continue
			}
			targetMap[tag] = controller
		}
	}

	items := make([]runtimeCacheTarget, 0, len(targetMap))
	for tag, controller := range targetMap {
		items = append(items, runtimeCacheTarget{tag: tag, controller: controller})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].tag < items[j].tag })
	return items, nil
}

func decodeOptionalJSONBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(r.Body).Decode(dst)
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func handleCacheFlushAll(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body flushAllRequest
		if err := decodeOptionalJSONBody(r, &body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request_body", "invalid request body")
			return
		}

		targets, err := runtimeCacheTargets(m, body.Tags, body.IncludeUDPFast)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "cache_not_found", err.Error())
			return
		}

		resp := runtimeCacheActionResponse{
			Total: len(targets),
			Items: make([]runtimeCacheActionItem, 0, len(targets)),
		}
		statusCode := http.StatusOK
		for _, target := range targets {
			item := runtimeCacheActionItem{
				Tag:  target.tag,
				Kind: target.controller.RuntimeCacheKind(),
			}
			if err := target.controller.FlushRuntimeCache(r.Context()); err != nil {
				item.Error = err.Error()
				resp.Failed++
				statusCode = http.StatusBadGateway
			} else {
				item.OK = true
				resp.Success++
			}
			resp.Items = append(resp.Items, item)
		}
		writeJSON(w, statusCode, resp)
	}
}

func handleCachePurgeDomains(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body purgeDomainsRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_request_body", "invalid request body")
			return
		}

		targets, err := runtimeCacheTargets(m, body.Tags, body.IncludeUDPFast)
		if err != nil {
			writeAPIError(w, http.StatusNotFound, "cache_not_found", err.Error())
			return
		}

		resp := runtimeCacheActionResponse{
			Total: len(targets),
			Items: make([]runtimeCacheActionItem, 0, len(targets)),
		}
		statusCode := http.StatusOK
		for _, target := range targets {
			item := runtimeCacheActionItem{
				Tag:  target.tag,
				Kind: target.controller.RuntimeCacheKind(),
			}
			purged, err := target.controller.PurgeDomainsRuntimeCache(r.Context(), body.Domains, body.QTypes)
			if err != nil {
				item.Error = err.Error()
				resp.Failed++
				statusCode = http.StatusBadGateway
			} else {
				item.OK = true
				item.Purged = purged
				resp.Success++
			}
			resp.Items = append(resp.Items, item)
		}
		writeJSON(w, statusCode, resp)
	}
}
