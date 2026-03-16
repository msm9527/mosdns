package coremain

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/internal/requeryruntime"
)

const sqliteWALWarnBytes = 64 << 20

type runtimeHealthCheck struct {
	Name    string         `json:"name"`
	Status  string         `json:"status"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

type runtimeHealthResponse struct {
	StorageEngine    string               `json:"storage_engine"`
	DBPath           string               `json:"db_path"`
	DBExists         bool                 `json:"db_exists"`
	DBSizeBytes      int64                `json:"db_size_bytes"`
	WALSizeBytes     int64                `json:"wal_size_bytes,omitempty"`
	Status           string               `json:"status"`
	Checks           []runtimeHealthCheck `json:"checks"`
	SuggestedActions []string             `json:"suggested_actions,omitempty"`
}

func runtimeHealthReport(dbPath string, m *Mosdns) (*runtimeHealthResponse, error) {
	resp := &runtimeHealthResponse{
		StorageEngine: "sqlite",
		DBPath:        dbPath,
		Status:        "ok",
		Checks:        make([]runtimeHealthCheck, 0, 12),
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
			Message: "control db does not exist yet",
			Details: map[string]any{"exists": false},
		})
	} else {
		return nil, err
	}

	if walInfo, err := os.Stat(dbPath + "-wal"); err == nil {
		resp.WALSizeBytes = walInfo.Size()
		status := "ok"
		message := ""
		if walInfo.Size() >= sqliteWALWarnBytes {
			status = "warn"
			message = "sqlite wal file is large"
		}
		addCheck(runtimeHealthCheck{
			Name:    "sqlite_wal",
			Status:  status,
			Message: message,
			Details: map[string]any{"size_bytes": walInfo.Size()},
		})
	}

	store, err := getRuntimeStateStoreByPath(dbPath)
	if err != nil {
		addCheck(runtimeHealthCheck{Name: "sqlite_open", Status: "error", Message: err.Error()})
		resp.SuggestedActions = buildHealthSuggestedActions(resp.Checks)
		return resp, nil
	}
	if size, err := store.db.FileSizeBytes(); err == nil && size > resp.DBSizeBytes {
		resp.DBExists = true
		resp.DBSizeBytes = size
	}
	addCheck(runtimeHealthCheck{Name: "sqlite_open", Status: "ok"})

	if quickCheck, err := store.db.QuickCheck(); err != nil {
		addCheck(runtimeHealthCheck{Name: "sqlite_quick_check", Status: "error", Message: err.Error()})
	} else {
		addCheck(runtimeHealthCheck{Name: "sqlite_quick_check", Status: "ok", Details: map[string]any{"result": quickCheck}})
	}

	addNamespaceSummaryCheck(dbPath, addCheck)
	addSwitchesHealthCheck(addCheck)
	addOverridesHealthCheck(addCheck)
	addUpstreamOverrideHealthCheck(addCheck)
	addDatasetHealthCheck(dbPath, addCheck)
	addRequeryHealthCheck(dbPath, addCheck)
	addUpstreamHealthCheck(m, addCheck)

	resp.SuggestedActions = buildHealthSuggestedActions(resp.Checks)
	return resp, nil
}

func addNamespaceSummaryCheck(dbPath string, addCheck func(runtimeHealthCheck)) {
	namespaces := []string{
		runtimeNamespaceWebinfo,
		runtimeNamespaceRequery,
		runtimeNamespaceAdguard,
		runtimeNamespaceDiversion,
		runtimeStateNamespaceGeneratedDataset,
	}
	counts := make(map[string]int, len(namespaces))
	for _, namespace := range namespaces {
		entries, err := ListRuntimeStateByNamespace(dbPath, namespace)
		if err != nil {
			addCheck(runtimeHealthCheck{
				Name:    "namespace_summary",
				Status:  "error",
				Message: err.Error(),
				Details: map[string]any{"namespace": namespace},
			})
			return
		}
		counts[namespace] = len(entries)
	}
	addCheck(runtimeHealthCheck{Name: "namespace_summary", Status: "ok", Details: map[string]any{"counts": counts}})
}

func addSwitchesHealthCheck(addCheck func(runtimeHealthCheck)) {
	switches, ok, err := loadSwitchesFromCustomConfig()
	if err != nil {
		addCheck(runtimeHealthCheck{Name: "control_switches", Status: "error", Message: err.Error()})
		return
	}
	addCheck(runtimeHealthCheck{
		Name:   "control_switches",
		Status: "ok",
		Details: map[string]any{
			"present": ok,
			"count":   len(switches),
			"path":    switchesConfigPath(),
		},
	})
}

func addOverridesHealthCheck(addCheck func(runtimeHealthCheck)) {
	if overrides, ok, err := loadGlobalOverridesFromCustomConfig(); err != nil {
		addCheck(runtimeHealthCheck{Name: "control_overrides", Status: "error", Message: err.Error()})
		return
	} else {
		count := 0
		if overrides != nil {
			count = len(overrides.Replacements)
		}
		addCheck(runtimeHealthCheck{
			Name:   "control_overrides",
			Status: "ok",
			Details: map[string]any{
				"present": ok,
				"count":   count,
				"path":    globalOverridesConfigPath(),
			},
		})
	}
}

func addUpstreamOverrideHealthCheck(addCheck func(runtimeHealthCheck)) {
	if upstreams, ok, err := loadUpstreamOverridesFromCustomConfig(); err != nil {
		addCheck(runtimeHealthCheck{Name: "control_upstreams", Status: "error", Message: err.Error()})
		return
	} else {
		total := 0
		for _, items := range upstreams {
			total += len(items)
		}
		addCheck(runtimeHealthCheck{
			Name:   "control_upstreams",
			Status: "ok",
			Details: map[string]any{
				"present": ok,
				"groups":  len(upstreams),
				"items":   total,
				"path":    upstreamOverridesConfigPath(),
			},
		})
	}
}

func addDatasetHealthCheck(dbPath string, addCheck func(runtimeHealthCheck)) {
	datasets, err := ListGeneratedDatasetsFromPath(dbPath)
	if err != nil {
		addCheck(runtimeHealthCheck{Name: "generated_datasets", Status: "error", Message: err.Error()})
		return
	}
	exportable := 0
	for _, dataset := range datasets {
		if strings.TrimSpace(dataset.ExportPath) != "" {
			exportable++
		}
	}
	addCheck(runtimeHealthCheck{
		Name:    "generated_datasets",
		Status:  "ok",
		Message: "generated datasets are stored in sqlite",
		Details: map[string]any{
			"datasets":   len(datasets),
			"exportable": exportable,
		},
	})
}

func addRequeryHealthCheck(dbPath string, addCheck func(runtimeHealthCheck)) {
	runs, err := requeryruntime.ListRuns(dbPath, "", 20)
	if err != nil {
		addCheck(runtimeHealthCheck{Name: "requery_runs", Status: "error", Message: err.Error()})
		return
	}
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

func addUpstreamHealthCheck(m *Mosdns, addCheck func(runtimeHealthCheck)) {
	if m == nil {
		addCheck(runtimeHealthCheck{Name: "upstream_health", Status: "ok", Message: "live upstream health requires running process"})
		return
	}
	overview := collectUpstreamHealth(m)
	if overview.Total == 0 {
		addCheck(runtimeHealthCheck{Name: "upstream_health", Status: "warn", Message: "no runtime upstream health providers found"})
		return
	}
	status := "ok"
	message := ""
	if overview.Unhealthy > 0 {
		status = "warn"
		message = "some upstreams are unhealthy"
	} else if overview.Degraded > 0 {
		status = "warn"
		message = "some upstreams are degraded"
	}
	addCheck(runtimeHealthCheck{
		Name:    "upstream_health",
		Status:  status,
		Message: message,
		Details: map[string]any{
			"total":       overview.Total,
			"healthy":     overview.Healthy,
			"degraded":    overview.Degraded,
			"unhealthy":   overview.Unhealthy,
			"worst_score": overview.WorstScore,
			"items":       overview.Items,
		},
	})
}

func buildHealthSuggestedActions(checks []runtimeHealthCheck) []string {
	actions := make([]string, 0, 4)
	for _, check := range checks {
		switch check.Name {
		case "sqlite_wal":
			if check.Status == "warn" {
				actions = append(actions, "执行 SQLite checkpoint 或检查是否存在长事务，缩小 control.db-wal。")
			}
		case "requery_runs":
			if check.Status == "warn" {
				actions = append(actions, "检查最近失败的 requery 任务并在必要时执行 control requery prune 清理历史。")
			}
		case "upstream_health":
			if check.Status == "warn" {
				actions = append(actions, "检查 unhealthy/degraded upstream，优先处理高失败或高延迟的上游。")
			}
		case "sqlite_quick_check":
			if check.Status == "error" {
				actions = append(actions, "立即备份并重建 control.db，数据库自检失败。")
			}
		}
	}
	return dedupeStrings(actions)
}

func dedupeStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func runtimeWalPath(dbPath string) string {
	return filepath.Clean(dbPath) + "-wal"
}
