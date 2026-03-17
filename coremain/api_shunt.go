package coremain

import (
	"net/http"
	"strconv"
	"strings"
)

func registerControlShuntAPI(r chiRouter, m *Mosdns) {
	r.Get("/shunt/explain", handleControlShuntExplain(m))
	r.Get("/shunt/conflicts", handleControlShuntConflicts(m))
}

type chiRouter interface {
	Get(pattern string, handlerFn http.HandlerFunc)
}

func handleControlShuntExplain(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		domainName := strings.TrimSpace(r.URL.Query().Get("domain"))
		if domainName == "" {
			writeAPIError(w, http.StatusBadRequest, "SHUNT_DOMAIN_REQUIRED", "domain is required")
			return
		}
		qtype := strings.TrimSpace(r.URL.Query().Get("qtype"))
		analyzer, err := loadControlShuntAnalyzer(m, parseBoolDefaultTrue(r.URL.Query().Get("live")))
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "SHUNT_ANALYZER_INIT_FAILED", err)
			return
		}
		result, err := analyzer.Explain(domainName, qtype)
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "SHUNT_EXPLAIN_FAILED", err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func handleControlShuntConflicts(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		analyzer, err := loadControlShuntAnalyzer(m, parseBoolDefaultTrue(r.URL.Query().Get("live")))
		if err != nil {
			writeAPIErrorFromErr(w, http.StatusInternalServerError, "SHUNT_ANALYZER_INIT_FAILED", err)
			return
		}
		conflicts := analyzer.Conflicts()
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit > 0 && len(conflicts) > limit {
			conflicts = conflicts[:limit]
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"base_dir":  MainConfigBaseDir,
			"count":     len(conflicts),
			"conflicts": conflicts,
			"warnings":  analyzer.warnings,
		})
	}
}

func loadControlShuntAnalyzer(m *Mosdns, live bool) (*shuntAnalyzer, error) {
	if live {
		return newShuntAnalyzerWithManager(MainConfigBaseDir, m)
	}
	return newShuntAnalyzer(MainConfigBaseDir)
}

func parseBoolDefaultTrue(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	switch value {
	case "", "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
