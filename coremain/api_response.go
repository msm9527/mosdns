package coremain

import (
	"encoding/json"
	"net/http"
)

type APIErrorResponse struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeAPIError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, APIErrorResponse{
		Code:  code,
		Error: msg,
	})
}

func writeAPIErrorFromErr(w http.ResponseWriter, status int, code string, err error) {
	if err == nil {
		writeAPIError(w, status, code, "")
		return
	}
	writeAPIError(w, status, code, err.Error())
}
