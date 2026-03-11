package coremain

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// RegisterSystemAPI 提供系统级操作，如自重启。
func RegisterSystemAPI(router *chi.Mux, m *Mosdns) {
	router.Route("/api/v1/system", func(r chi.Router) {
		r.Post("/restart", handleSelfRestart(m))
	})
}

func handleSelfRestart(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if m == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "service unavailable")
			return
		}

		type reqBody struct {
			DelayMs int `json:"delay_ms"`
		}
		var body reqBody
		if r.Body != nil && r.Body != http.NoBody {
			if err := decodeJSONBodyStrict(w, r, &body, true); err != nil {
				if errors.Is(err, errJSONBodyTooLarge) {
					writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "request body too large")
					return
				}
				writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "invalid request body")
				return
			}
		}
		if body.DelayMs <= 0 {
			body.DelayMs = 300
		}

		if !SelfRestartSupported() {
			// 复用 api_update.go 中的 writeJSON
			writeAPIError(w, http.StatusNotImplemented, "RESTART_NOT_SUPPORTED_ON_WINDOWS", "self-restart is not supported on Windows")
			return
		}
		if !m.tryScheduleRestart() {
			writeAPIError(w, http.StatusConflict, "RESTART_ALREADY_SCHEDULED", "restart already scheduled")
			return
		}

		// 1. 立即响应
		writeJSON(w, http.StatusOK, map[string]any{"status": "scheduled", "delay_ms": body.DelayMs})

		go func(delay int) {
			logger := m.Logger()
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.Error("panic during self restart flow", zap.Any("panic", recovered))
					m.clearScheduledRestart()
				}
			}()

			// 2. 等待延迟
			time.Sleep(time.Duration(delay) * time.Millisecond)

			// 3. 只对显式声明了重启准备能力的插件执行落盘逻辑
			logger.Info("preparing plugins for restart")

			for tag, p := range m.plugins {
				if p == nil {
					continue
				}
				preparer, ok := p.(RestartPreparer)
				if !ok {
					continue
				}
				logger.Info("running plugin restart preparer", zap.String("tag", tag))
				if err := preparer.PrepareForRestart(); err != nil {
					logger.Warn("plugin restart preparation failed", zap.String("tag", tag), zap.Error(err))
				}
			}
			logger.Info("plugin restart preparation completed")

			// 4. 执行重启 (进程替换)
			logger.Info("executing self restart")
			_ = logger.Sync()
			if err := ExecSelfRestart(); err != nil {
				logger.Error("self-restart exec failed", zap.Error(err))
				m.clearScheduledRestart()
			}
		}(body.DelayMs)
	}
}
