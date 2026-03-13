package coremain

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"

	"github.com/go-chi/chi/v5"
)

const (
	runtimeNamespaceSwitch  = "switch"
	runtimeNamespaceWebinfo = "webinfo"
	runtimeNamespaceRequery = "requery"
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

type runtimeResourcesResponse struct {
	StorageEngine string                         `json:"storage_engine"`
	DBPath        string                         `json:"db_path"`
	Overrides     *GlobalOverrides               `json:"overrides,omitempty"`
	Upstreams     GlobalUpstreamOverrides        `json:"upstreams,omitempty"`
	Switches      map[string]string              `json:"switches,omitempty"`
	Webinfo       map[string]json.RawMessage     `json:"webinfo,omitempty"`
	Requery       map[string]json.RawMessage     `json:"requery,omitempty"`
	Namespaces    map[string][]RuntimeStateEntry `json:"namespaces,omitempty"`
}

func RegisterRuntimeAPI(router *chi.Mux) {
	router.Route("/api/v1/runtime", func(r chi.Router) {
		r.Get("/summary", handleRuntimeSummary)
		r.Get("/resources", handleRuntimeResources)
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

func handleRuntimeResources(w http.ResponseWriter, _ *http.Request) {
	dbPath := defaultRuntimeStateDBPath()
	resp := runtimeResourcesResponse{
		StorageEngine: "sqlite",
		DBPath:        dbPath,
		Upstreams:     make(GlobalUpstreamOverrides),
		Switches:      make(map[string]string),
		Webinfo:       make(map[string]json.RawMessage),
		Requery:       make(map[string]json.RawMessage),
		Namespaces:    make(map[string][]RuntimeStateEntry),
	}

	if overrides, ok, err := loadGlobalOverridesFromRuntimeStore(); err == nil && ok {
		resp.Overrides = overrides
	} else if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "RUNTIME_LOAD_OVERRIDES_FAILED", err.Error())
		return
	}

	if upstreams, ok, err := loadUpstreamOverridesFromRuntimeStore(); err == nil && ok {
		resp.Upstreams = upstreams
	} else if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "RUNTIME_LOAD_UPSTREAMS_FAILED", err.Error())
		return
	}

	for _, namespace := range []string{runtimeNamespaceSwitch, runtimeNamespaceWebinfo, runtimeNamespaceRequery} {
		entries, err := ListRuntimeStateByNamespace(dbPath, namespace)
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, "RUNTIME_LIST_NAMESPACE_FAILED", err.Error())
			return
		}
		resp.Namespaces[namespace] = entries
	}

	for _, entry := range resp.Namespaces[runtimeNamespaceSwitch] {
		var values map[string]string
		if err := json.Unmarshal(entry.Value, &values); err != nil {
			writeAPIError(w, http.StatusInternalServerError, "RUNTIME_DECODE_SWITCH_FAILED", err.Error())
			return
		}
		for k, v := range values {
			resp.Switches[k] = v
		}
	}
	for _, entry := range resp.Namespaces[runtimeNamespaceWebinfo] {
		resp.Webinfo[filepath.Base(entry.Key)] = append(json.RawMessage(nil), entry.Value...)
	}
	for _, entry := range resp.Namespaces[runtimeNamespaceRequery] {
		resp.Requery[filepath.Base(entry.Key)] = append(json.RawMessage(nil), entry.Value...)
	}

	keys := make([]string, 0, len(resp.Switches))
	for key := range resp.Switches {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(keys))
	for _, key := range keys {
		ordered[key] = resp.Switches[key]
	}
	resp.Switches = ordered

	writeJSON(w, http.StatusOK, resp)
}
