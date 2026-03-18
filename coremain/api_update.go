package coremain

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"time"

	"github.com/go-chi/chi/v5"
)

// RegisterUpdateAPI 暴露版本检查与在线更新接口。
func RegisterUpdateAPI(router *chi.Mux, mosdns ...*Mosdns) {
	manager := resolveUpdateManager(firstMosdns(mosdns))
	router.Route("/api/v1/update", func(r chi.Router) {
		r.Get("/status", handleUpdateStatus(manager))
		r.Post("/check", handleForceUpdateStatus(manager))
		r.Post("/apply", handleApplyUpdate(manager))
	})
}

func handleUpdateStatus(manager *UpdateManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 限时查询，避免前端长时间转圈（正常 3 秒内返回）
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		status, err := manager.CheckForUpdate(ctx, false)
		if err != nil {
			// 失败时也返回 200 与降级信息，前端不再卡在“检测中…”。
			fallback := UpdateStatus{
				CurrentVersion:  GetBuildVersion(),
				LatestVersion:   "",
				Architecture:    runtime.GOOS + "/" + runtime.GOARCH,
				CheckedAt:       time.Now(),
				CacheExpiresAt:  time.Now(),
				UpdateAvailable: false,
				Cached:          false,
				Message:         "检查更新失败：" + err.Error(),
			}
			writeJSON(w, http.StatusOK, fallback)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func handleForceUpdateStatus(manager *UpdateManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 强制检查允许更长的时间窗口，但也设置上限以保证可用性
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()

		status, err := manager.CheckForUpdate(ctx, true)
		if err != nil {
			fallback := UpdateStatus{
				CurrentVersion:  GetBuildVersion(),
				LatestVersion:   "",
				Architecture:    runtime.GOOS + "/" + runtime.GOARCH,
				CheckedAt:       time.Now(),
				CacheExpiresAt:  time.Now(),
				UpdateAvailable: false,
				Cached:          false,
				Message:         "检查更新失败：" + err.Error(),
			}
			writeJSON(w, http.StatusOK, fallback)
			return
		}
		writeJSON(w, http.StatusOK, status)
	}
}

func handleApplyUpdate(manager *UpdateManager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		force := false
		preferV3 := false
		if r.Body != nil && r.Body != http.NoBody {
			var req struct {
				Force    bool `json:"force"`
				PreferV3 bool `json:"prefer_v3"`
			}
			if err := decodeJSONBodyStrict(w, r, &req, true); err != nil {
				if errors.Is(err, errJSONBodyTooLarge) {
					writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
					return
				}
				writeAPIErrorFromErr(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", err)
				return
			}
			force = req.Force
			preferV3 = req.PreferV3
		}

		result, err := manager.PerformUpdate(r.Context(), force, preferV3)
		if err != nil {
			if errors.Is(err, ErrNoUpdateAvailable) {
				writeJSON(w, http.StatusOK, result)
				return
			}
			writeAPIErrorFromErr(w, http.StatusBadGateway, "UPDATE_APPLY_FAILED", err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func resolveUpdateManager(m *Mosdns) *UpdateManager {
	if m != nil && m.GetUpdateManager() != nil {
		return m.GetUpdateManager()
	}
	return GlobalUpdateManager
}

func firstMosdns(ms []*Mosdns) *Mosdns {
	if len(ms) == 0 {
		return nil
	}
	return ms[0]
}
