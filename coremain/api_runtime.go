package coremain

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

const (
	runtimeNamespaceWebinfo    = "webinfo"
	runtimeNamespaceRequery    = "requery"
	runtimeStateNamespaceAudit = "audit"
	runtimeNamespaceAdguard    = "adguard_rule"
	runtimeNamespaceDiversion  = "diversion_rule"
)

type runtimeNamespaceSummary struct {
	Namespace string `json:"namespace"`
	Keys      int    `json:"keys"`
}

type runtimeSummaryResponse struct {
	StorageEngine string                    `json:"storage_engine"`
	DBPath        string                    `json:"db_path"`
	Namespaces    []runtimeNamespaceSummary `json:"namespaces"`
	Upstreams     upstreamHealthOverview    `json:"upstreams,omitempty"`
}

func RegisterRuntimeAPI(router *chi.Mux, m *Mosdns) {
	router.Route("/api/v1/control", func(r chi.Router) {
		r.Get("/health", handleRuntimeHealth(m))
		r.Get("/summary", handleRuntimeSummary(m))
		r.Get("/datasets", handleRuntimeDatasets)
		r.Post("/datasets/export", handleRuntimeDatasetsExport)
		r.Post("/datasets/verify", handleRuntimeDatasetsVerify)
		r.Get("/events", handleRuntimeEvents)
		r.Get("/overrides", func(w http.ResponseWriter, r *http.Request) {
			handleGetOverrides(w, r, m)
		})
		r.Post("/overrides", func(w http.ResponseWriter, r *http.Request) {
			handleSetOverridesWithMosdns(w, r, m)
		})
		r.Get("/clientname", handleGetClientname(m))
		r.Put("/clientname", handlePutClientname(m))
		r.Get("/upstreams", handleGetControlUpstreamConfig(m))
		r.Put("/upstreams", func(w http.ResponseWriter, r *http.Request) {
			handleReplaceUpstreamConfigWithMosdns(w, r, m)
		})
		r.Post("/upstreams", func(w http.ResponseWriter, r *http.Request) {
			handleSetUpstreamConfigWithMosdns(w, r, m)
		})
		r.Get("/upstreams/health", handleControlUpstreamHealth(m))
		r.Get("/upstreams/tags", handleGetControlUpstreamTags(m))
	})
}

func handleRuntimeSummary(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		dbPath := defaultRuntimeStateDBPath()
		namespaces := []string{
			runtimeStateNamespaceAudit,
			runtimeNamespaceWebinfo,
			runtimeNamespaceRequery,
			runtimeNamespaceAdguard,
			runtimeNamespaceDiversion,
			runtimeStateNamespaceGeneratedDataset,
		}

		summary := runtimeSummaryResponse{
			StorageEngine: "sqlite",
			DBPath:        dbPath,
			Namespaces:    make([]runtimeNamespaceSummary, 0, len(namespaces)),
			Upstreams:     collectUpstreamHealth(m),
		}

		for _, namespace := range namespaces {
			entries, err := ListRuntimeStateByNamespace(dbPath, namespace)
			if err != nil {
				writeAPIError(w, http.StatusInternalServerError, "RUNTIME_SUMMARY_FAILED", err.Error())
				return
			}
			summary.Namespaces = append(summary.Namespaces, runtimeNamespaceSummary{
				Namespace: namespace,
				Keys:      len(entries),
			})
		}

		writeJSON(w, http.StatusOK, summary)
	}
}

func handleRuntimeHealth(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		resp, err := runtimeHealthReport(defaultRuntimeStateDBPath(), m)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "RUNTIME_HEALTH_FAILED", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleRuntimeDatasets(w http.ResponseWriter, _ *http.Request) {
	datasets, err := ListGeneratedDatasetsFromPath(defaultRuntimeStateDBPath())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "RUNTIME_DATASETS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, datasets)
}

func handleRuntimeDatasetsExport(w http.ResponseWriter, _ *http.Request) {
	exported, err := ExportGeneratedDatasetsToFiles(defaultRuntimeStateDBPath())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "RUNTIME_DATASETS_EXPORT_FAILED", err.Error())
		return
	}
	_ = RecordSystemEvent("control.datasets", "info", "exported generated datasets to files", map[string]any{
		"exported_files": exported,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "success",
		"exported_files": exported,
	})
}

func handleRuntimeDatasetsVerify(w http.ResponseWriter, _ *http.Request) {
	summary, err := VerifyGeneratedDatasetsOnFiles(defaultRuntimeStateDBPath())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "RUNTIME_DATASETS_VERIFY_FAILED", err.Error())
		return
	}
	_ = RecordSystemEvent("control.datasets", "info", "verified generated datasets against files", map[string]any{
		"checked":  summary.Checked,
		"matched":  summary.Matched,
		"missing":  summary.Missing,
		"mismatch": summary.Mismatch,
	})
	writeJSON(w, http.StatusOK, summary)
}

func handleRuntimeEvents(w http.ResponseWriter, r *http.Request) {
	component := strings.TrimSpace(r.URL.Query().Get("component"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := ListSystemEvents(defaultRuntimeStateDBPath(), component, limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "RUNTIME_EVENTS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func handleControlUpstreamHealth(m *Mosdns) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, collectUpstreamHealth(m))
	}
}
