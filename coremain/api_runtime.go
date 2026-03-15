package coremain

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/internal/requeryruntime"
	"github.com/go-chi/chi/v5"
)

const (
	runtimeNamespaceSwitch    = "switch"
	runtimeNamespaceWebinfo   = "webinfo"
	runtimeNamespaceRequery   = "requery"
	runtimeNamespaceAdguard   = "adguard_rule"
	runtimeNamespaceDiversion = "diversion_rule"
)

type runtimeNamespaceSummary struct {
	Namespace string `json:"namespace"`
	Keys      int    `json:"keys"`
}

type runtimeSummaryResponse struct {
	StorageEngine string                    `json:"storage_engine"`
	DBPath        string                    `json:"db_path"`
	Namespaces    []runtimeNamespaceSummary `json:"namespaces"`
}

type runtimeHealthCheck struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

type runtimeHealthResponse struct {
	StorageEngine string               `json:"storage_engine"`
	DBPath        string               `json:"db_path"`
	DBExists      bool                 `json:"db_exists"`
	DBSizeBytes   int64                `json:"db_size_bytes"`
	Status        string               `json:"status"`
	Checks        []runtimeHealthCheck `json:"checks"`
}

func RegisterRuntimeAPI(router *chi.Mux, m *Mosdns) {
	router.Route("/api/v1/runtime", func(r chi.Router) {
		r.Get("/health", handleRuntimeHealth)
		r.Get("/summary", handleRuntimeSummary)
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
		r.Get("/upstreams", handleGetUpstreamConfig)
		r.Put("/upstreams", func(w http.ResponseWriter, r *http.Request) {
			handleReplaceUpstreamConfigWithMosdns(w, r, m)
		})
		r.Post("/upstreams", func(w http.ResponseWriter, r *http.Request) {
			handleSetUpstreamConfigWithMosdns(w, r, m)
		})
		r.Get("/upstreams/tags", handleGetAliAPITags)
	})
}

func handleRuntimeSummary(w http.ResponseWriter, _ *http.Request) {
	dbPath := defaultRuntimeStateDBPath()
	namespaces := []string{
		runtimeStateNamespaceOverrides,
		runtimeStateNamespaceUpstreams,
		runtimeNamespaceSwitch,
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

func handleRuntimeHealth(w http.ResponseWriter, _ *http.Request) {
	resp, err := runtimeHealthReport(defaultRuntimeStateDBPath())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "RUNTIME_HEALTH_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
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
	_ = RecordSystemEvent("runtime.datasets", "info", "exported generated datasets to files", map[string]any{
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
	_ = RecordSystemEvent("runtime.datasets", "info", "verified generated datasets against files", map[string]any{
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

func runtimeHealthReport(dbPath string) (*runtimeHealthResponse, error) {
	resp := &runtimeHealthResponse{
		StorageEngine: "sqlite",
		DBPath:        dbPath,
		Status:        "ok",
		Checks:        make([]runtimeHealthCheck, 0, 8),
	}

	addCheck := func(check runtimeHealthCheck) {
		resp.Checks = append(resp.Checks, check)
		switch check.Status {
		case "error":
			resp.Status = "error"
		case "warn":
			if resp.Status != "error" {
				resp.Status = "warn"
			}
		}
	}

	if info, err := os.Stat(dbPath); err == nil {
		resp.DBExists = true
		resp.DBSizeBytes = info.Size()
		addCheck(runtimeHealthCheck{
			Name:   "sqlite_file",
			Status: "ok",
			Details: map[string]any{
				"exists":     true,
				"size_bytes": info.Size(),
			},
		})
	} else if os.IsNotExist(err) {
		addCheck(runtimeHealthCheck{
			Name:    "sqlite_file",
			Status:  "warn",
			Message: "runtime db does not exist yet",
			Details: map[string]any{"exists": false},
		})
	} else {
		return nil, err
	}

	store, err := getRuntimeStateStoreByPath(dbPath)
	if err != nil {
		addCheck(runtimeHealthCheck{Name: "sqlite_open", Status: "error", Message: err.Error()})
		return resp, nil
	}
	if size, err := store.db.FileSizeBytes(); err == nil && size > 0 {
		resp.DBExists = true
		if size > resp.DBSizeBytes {
			resp.DBSizeBytes = size
		}
	}
	addCheck(runtimeHealthCheck{Name: "sqlite_open", Status: "ok"})

	namespaces := []string{
		runtimeStateNamespaceOverrides,
		runtimeStateNamespaceUpstreams,
		runtimeNamespaceSwitch,
		runtimeNamespaceWebinfo,
		runtimeNamespaceRequery,
		runtimeNamespaceAdguard,
		runtimeNamespaceDiversion,
		runtimeStateNamespaceGeneratedDataset,
	}
	namespaceCounts := make(map[string]int, len(namespaces))
	for _, namespace := range namespaces {
		entries, err := ListRuntimeStateByNamespace(dbPath, namespace)
		if err != nil {
			addCheck(runtimeHealthCheck{
				Name:    "namespace_summary",
				Status:  "error",
				Message: err.Error(),
				Details: map[string]any{"namespace": namespace},
			})
			goto checksContinue
		}
		namespaceCounts[namespace] = len(entries)
	}
	addCheck(runtimeHealthCheck{
		Name:    "namespace_summary",
		Status:  "ok",
		Details: map[string]any{"counts": namespaceCounts},
	})

checksContinue:
	if overrides, ok, err := loadGlobalOverridesFromRuntimeStore(); err != nil {
		addCheck(runtimeHealthCheck{Name: "runtime_overrides", Status: "error", Message: err.Error()})
	} else {
		addCheck(runtimeHealthCheck{
			Name:   "runtime_overrides",
			Status: "ok",
			Details: map[string]any{
				"present": ok,
				"count": func() int {
					if overrides == nil {
						return 0
					}
					return len(overrides.Replacements)
				}(),
			},
		})
	}

	if upstreams, ok, err := loadUpstreamOverridesFromRuntimeStore(); err != nil {
		addCheck(runtimeHealthCheck{Name: "runtime_upstreams", Status: "error", Message: err.Error()})
	} else {
		total := 0
		for _, items := range upstreams {
			total += len(items)
		}
		addCheck(runtimeHealthCheck{
			Name:   "runtime_upstreams",
			Status: "ok",
			Details: map[string]any{
				"present": ok,
				"groups":  len(upstreams),
				"items":   total,
			},
		})
	}

	if verify, err := VerifyGeneratedDatasetsOnFiles(dbPath); err != nil {
		addCheck(runtimeHealthCheck{Name: "generated_datasets", Status: "error", Message: err.Error()})
	} else {
		status := "ok"
		message := ""
		if verify.Mismatch > 0 {
			status = "warn"
			message = "generated dataset files have mismatches"
		} else if verify.Missing > 0 {
			status = "warn"
			message = "some generated dataset files are missing"
		}
		addCheck(runtimeHealthCheck{
			Name:    "generated_datasets",
			Status:  status,
			Message: message,
			Details: map[string]any{
				"checked":  verify.Checked,
				"matched":  verify.Matched,
				"missing":  verify.Missing,
				"mismatch": verify.Mismatch,
			},
		})
	}

	if runs, err := requeryruntime.ListRuns(dbPath, "", 20); err != nil {
		addCheck(runtimeHealthCheck{Name: "requery_runs", Status: "error", Message: err.Error()})
	} else {
		failed := 0
		for _, run := range runs {
			state := strings.ToLower(strings.TrimSpace(run.State))
			if state == "failed" || state == "error" {
				failed++
			}
		}
		status := "ok"
		message := ""
		if failed > 0 {
			status = "warn"
			message = "recent requery runs contain failures"
		}
		addCheck(runtimeHealthCheck{
			Name:    "requery_runs",
			Status:  status,
			Message: message,
			Details: map[string]any{
				"recent_runs": len(runs),
				"failed":      failed,
			},
		})
	}

	return resp, nil
}
