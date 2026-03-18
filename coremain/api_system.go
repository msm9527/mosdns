package coremain

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
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
		delayMs, err := m.ScheduleSelfRestart(body.DelayMs)
		if err != nil {
			writeSelfRestartError(w, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"status": "scheduled", "delay_ms": delayMs})
	}
}

func writeSelfRestartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSelfRestartNotSupported):
		writeAPIError(w, http.StatusNotImplemented, "RESTART_NOT_SUPPORTED_ON_WINDOWS", err.Error())
	case errors.Is(err, ErrRestartAlreadyScheduled):
		writeAPIError(w, http.StatusConflict, "RESTART_ALREADY_SCHEDULED", err.Error())
	default:
		var delayErr *RestartDelayError
		if errors.As(err, &delayErr) {
			writeAPIError(w, http.StatusBadRequest, "INVALID_RESTART_DELAY", err.Error())
			return
		}
		writeAPIError(w, http.StatusInternalServerError, "RESTART_SCHEDULE_FAILED", err.Error())
	}
}
