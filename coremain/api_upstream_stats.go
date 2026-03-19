package coremain

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type upstreamStatsResetRequest struct {
	PluginTag   string `json:"plugin_tag"`
	UpstreamTag string `json:"upstream_tag"`
}

type upstreamStatsResetResponse struct {
	Message                string `json:"message"`
	PluginTag              string `json:"plugin_tag,omitempty"`
	UpstreamTag            string `json:"upstream_tag,omitempty"`
	ClearedRuntimeItems    int    `json:"cleared_runtime_items"`
	DeletedPersistentItems int    `json:"deleted_persistent_items"`
}

func handleResetUpstreamStats(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req upstreamStatsResetRequest
		if err := decodeJSONBodyStrict(w, r, &req, true); err != nil {
			if errors.Is(err, errJSONBodyTooLarge) {
				writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
				return
			}
			writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body")
			return
		}
		if err := validateUpstreamStatsResetRequest(req); err != nil {
			writeAPIError(w, http.StatusBadRequest, "INVALID_UPSTREAM_STATS_RESET_SCOPE", err.Error())
			return
		}

		resp, err := resetUpstreamStats(m, runtimeControlDBPath(m), r, req)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "UPSTREAM_STATS_RESET_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func validateUpstreamStatsResetRequest(req upstreamStatsResetRequest) error {
	if strings.TrimSpace(req.UpstreamTag) != "" && strings.TrimSpace(req.PluginTag) == "" {
		return fmt.Errorf("plugin_tag is required when upstream_tag is set")
	}
	return nil
}

func resetUpstreamStats(m *Mosdns, dbPath string, r *http.Request, req upstreamStatsResetRequest) (upstreamStatsResetResponse, error) {
	req.PluginTag = strings.TrimSpace(req.PluginTag)
	req.UpstreamTag = strings.TrimSpace(req.UpstreamTag)

	clearedRuntimeItems, err := resetUpstreamStatsInMemory(m, r, req)
	if err != nil {
		return upstreamStatsResetResponse{}, err
	}
	deletedPersistentItems, err := ResetUpstreamRuntimeStats(dbPath, req.PluginTag, req.UpstreamTag)
	if err != nil {
		return upstreamStatsResetResponse{}, err
	}
	_ = RecordSystemEvent("control.upstreams", "info", "reset upstream runtime stats", map[string]any{
		"plugin_tag":               req.PluginTag,
		"upstream_tag":             req.UpstreamTag,
		"cleared_runtime_items":    clearedRuntimeItems,
		"deleted_persistent_items": deletedPersistentItems,
	})
	return upstreamStatsResetResponse{
		Message:                "上游统计已清空",
		PluginTag:              req.PluginTag,
		UpstreamTag:            req.UpstreamTag,
		ClearedRuntimeItems:    clearedRuntimeItems,
		DeletedPersistentItems: deletedPersistentItems,
	}, nil
}

func resetUpstreamStatsInMemory(m *Mosdns, r *http.Request, req upstreamStatsResetRequest) (int, error) {
	if m == nil {
		return 0, nil
	}
	if req.PluginTag != "" {
		return resetUpstreamStatsForPlugin(r, m.GetPlugin(req.PluginTag), req.UpstreamTag)
	}

	total := 0
	for _, plugin := range m.SnapshotPlugins() {
		count, err := resetUpstreamStatsForPlugin(r, plugin, "")
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func resetUpstreamStatsForPlugin(r *http.Request, plugin any, upstreamTag string) (int, error) {
	resetter, ok := plugin.(UpstreamStatsResetter)
	if !ok || resetter == nil {
		return 0, nil
	}
	return resetter.ResetUpstreamStats(r.Context(), upstreamTag)
}
