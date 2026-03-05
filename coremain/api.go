// /root/mosdns/coremain/api.go
package coremain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// RegisterCaptureAPI registers the log capture APIs to the given router.
func RegisterCaptureAPI(router *chi.Mux) {
	router.Post("/api/v1/capture/start", handleStartCapture())
	router.Get("/api/v1/capture/logs", WithAsyncGC(handleGetLogs()))
}

func handleStartCapture() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DurationSeconds int `json:"duration_seconds"`
		}

		// Set default duration
		req.DurationSeconds = 120

		// Decode request body if provided
		if r.Body != http.NoBody {
			if err := decodeJSONBodyStrict(w, r, &req, true); err != nil {
				if errors.Is(err, errJSONBodyTooLarge) {
					writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
					return
				}
				writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body: "+err.Error())
				return
			}
		}

		if req.DurationSeconds <= 0 || req.DurationSeconds > 600 {
			writeAPIError(w, http.StatusBadRequest, "INVALID_DURATION_SECONDS", "Duration must be between 1 and 600 seconds")
			return
		}

		duration := time.Duration(req.DurationSeconds) * time.Second
		// Use the exported mlog.Lvl
		GlobalLogCollector.StartCapture(duration, mlog.Lvl)

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Log capture started for %d seconds. Log level set to DEBUG.", req.DurationSeconds)
	}
}

func handleGetLogs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		logs := GlobalLogCollector.GetLogs()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(logs); err != nil {
			mlog.L().Error("failed to encode logs to client", zap.Error(err))
		}
	}
}
