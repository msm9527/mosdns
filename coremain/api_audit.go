// /root/mosdns/coremain/api_audit.go
package coremain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// RegisterAuditAPI registers the audit log APIs to the given router.
func RegisterAuditAPI(router *chi.Mux) {
	router.Route("/api/v1/audit", func(r chi.Router) {
		r.Post("/start", handleAuditStart)
		r.Post("/stop", handleAuditStop)
		r.Get("/status", handleAuditStatus)
		r.Get("/logs", WithAsyncGC(handleGetAuditLogs))
		r.Post("/clear", WithAsyncGC(handleClearAuditLogs))
		// ADDED: New routes for capacity management
		r.Get("/capacity", handleGetAuditCapacity)
		r.Post("/capacity", handleSetAuditCapacity)
	})
}

func handleAuditStart(w http.ResponseWriter, r *http.Request) {
	GlobalAuditCollector.Start()
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Audit log collection started.")
}

func handleAuditStop(w http.ResponseWriter, r *http.Request) {
	GlobalAuditCollector.Stop()
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "Audit log collection stopped.")
}

func handleAuditStatus(w http.ResponseWriter, r *http.Request) {
	status := struct {
		Capturing bool `json:"capturing"`
	}{
		Capturing: GlobalAuditCollector.IsCapturing(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func handleGetAuditLogs(w http.ResponseWriter, r *http.Request) {
	logs := GlobalAuditCollector.GetLogs()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(logs); err != nil {
		mlog.L().Error("failed to encode audit logs to client", zap.Error(err))
	}
}

func handleClearAuditLogs(w http.ResponseWriter, r *http.Request) {
	GlobalAuditCollector.ClearLogs(true)
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "审计日志已清空。")
}

type auditStorageResponse struct {
	MemoryEntries        int   `json:"memory_entries"`
	CurrentMemoryEntries int   `json:"current_memory_entries,omitempty"`
	RetentionDays        int   `json:"retention_days"`
	MaxDiskSizeMB        int   `json:"max_disk_size_mb"`
	Capacity             int   `json:"capacity,omitempty"`
	CurrentDiskSize      int64 `json:"current_disk_size_bytes"`
}

func handleGetAuditCapacity(w http.ResponseWriter, r *http.Request) {
	settings := GlobalAuditCollector.GetSettings()
	resp := auditStorageResponse{
		MemoryEntries:        settings.MemoryEntries,
		CurrentMemoryEntries: GlobalAuditCollector.GetCurrentMemoryEntries(),
		RetentionDays:        settings.RetentionDays,
		MaxDiskSizeMB:        settings.MaxDiskSizeMB,
		Capacity:             settings.MemoryEntries,
		CurrentDiskSize:      GlobalAuditCollector.GetDiskUsageBytes(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func handleSetAuditCapacity(w http.ResponseWriter, r *http.Request) {
	var req auditStorageResponse

	if err := decodeJSONBodyStrict(w, r, &req, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body: "+err.Error())
		return
	}

	settings := AuditSettings{
		MemoryEntries: req.MemoryEntries,
		RetentionDays: req.RetentionDays,
		MaxDiskSizeMB: req.MaxDiskSizeMB,
		Capacity:      req.Capacity,
	}
	if err := GlobalAuditCollector.SetSettings(settings, MainConfigBaseDir); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "SET_AUDIT_SETTINGS_FAILED", "Failed to save audit settings: "+err.Error())
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "审计存储设置已保存并生效。")
}
