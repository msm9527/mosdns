package coremain

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

// RegisterOverridesAPI registers the global overrides APIs.
func RegisterOverridesAPI(router *chi.Mux, m *Mosdns) {
	router.Route("/api/v1/overrides", func(r chi.Router) {
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			handleGetOverrides(w, r, m)
		})
		r.Post("/", func(w http.ResponseWriter, r *http.Request) {
			handleSetOverridesWithMosdns(w, r, m)
		})
	})
}

// ReplacementRuleAPIResponse includes the runtime result field.
type ReplacementRuleAPIResponse struct {
	Original string `json:"original"`
	New      string `json:"new"`
	Comment  string `json:"comment"`
	Result   string `json:"result"` // e.g., "Success (3)" or "Not Found"
}

// GlobalOverridesResponse defines the API response structure.
type GlobalOverridesResponse struct {
	Socks5       string                       `json:"socks5"`
	ECS          string                       `json:"ecs"`
	Replacements []ReplacementRuleAPIResponse `json:"replacements"`
}

func handleGetOverrides(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	resp := GlobalOverridesResponse{
		Replacements: make([]ReplacementRuleAPIResponse, 0),
	}

	// Helper to populate replacements list from a GlobalOverrides struct
	// If it's from memory (stats=true), we use GetCount.
	populateReplacements := func(src *GlobalOverrides, useStats bool) {
		if src == nil || src.Replacements == nil {
			return
		}
		for _, rule := range src.Replacements {
			res := "Unknown"
			if useStats {
				count := rule.GetCount()
				if count > 0 {
					res = fmt.Sprintf("Success (%d)", count)
				} else {
					res = "Not Found"
				}
			} else {
				res = "Unknown (Not Loaded)"
			}
			resp.Replacements = append(resp.Replacements, ReplacementRuleAPIResponse{
				Original: rule.Original,
				New:      rule.New,
				Comment:  rule.Comment,
				Result:   res,
			})
		}
	}

	loadedFromRuntime := false
	// Logic: Runtime memory -> File -> Discovery fallback
	if m != nil {
		if current := m.GetGlobalOverrides(); current != nil {
			resp.Socks5 = current.Socks5
			resp.ECS = current.ECS
			populateReplacements(current, true)
			loadedFromRuntime = true
		}
	}
	if !loadedFromRuntime {
		// Not in memory, try file.
		overridesPath := filepath.Join(MainConfigBaseDir, overridesFilename)
		data, err := os.ReadFile(overridesPath)
		var fileObj GlobalOverrides
		fileLoaded := false

		if err == nil && json.Unmarshal(data, &fileObj) == nil {
			resp.Socks5 = fileObj.Socks5
			resp.ECS = fileObj.ECS
			populateReplacements(&fileObj, false)
			fileLoaded = true
		}

		// Fallback for Socks5/ECS if file didn't exist or parsing failed (or fields empty? - preserving original logic)
		// Original logic: "falling back to discovered settings" if Parse failed or File not exist.
		// If file loaded but values are empty, we might keep them empty?
		// Let's stick to strict fallback: if file not loaded, use discovered.
		if !fileLoaded {
			resp.Socks5 = discoveredSocks5
			resp.ECS = discoveredECS
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func handleSetOverrides(w http.ResponseWriter, r *http.Request) {
	handleSetOverridesWithMosdns(w, r, nil)
}

func handleSetOverridesWithMosdns(w http.ResponseWriter, r *http.Request, m *Mosdns) {
	var payload GlobalOverrides
	if err := decodeJSONBodyStrict(w, r, &payload, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body: "+err.Error())
		return
	}

	overridesPath := filepath.Join(MainConfigBaseDir, overridesFilename)

	// We only save original/new/comment for replacements (via json tags in struct)
	updatedData, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "MARSHAL_SETTINGS_FAILED", "Failed to marshal settings: "+err.Error())
		return
	}

	if err := os.WriteFile(overridesPath, updatedData, 0644); err != nil {
		writeAPIError(w, http.StatusInternalServerError, "WRITE_SETTINGS_FILE_FAILED", "Failed to write settings file: "+err.Error())
		return
	}

	mlog.L().Info("global overrides saved via API",
		zap.String("socks5", payload.Socks5),
		zap.String("ecs", payload.ECS),
		zap.Int("replacements", len(payload.Replacements)))

	payload.Prepare()
	if m != nil {
		m.setGlobalOverrides(CloneGlobalOverrides(&payload))
		if err := m.ReloadRuntimeConfig(""); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "RUNTIME_RELOAD_FAILED", "Settings saved but runtime apply failed: "+err.Error())
			return
		}
	}

	message := "全局覆盖配置已保存。"
	if m != nil {
		message = "全局覆盖配置已保存并生效。"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"message": message,
	})
}
