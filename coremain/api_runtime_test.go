package coremain

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeUpstreamHealthProvider struct{}

func (fakeUpstreamHealthProvider) SnapshotUpstreamHealth() []UpstreamHealthSnapshot {
	return []UpstreamHealthSnapshot{{
		PluginTag:           "fake",
		PluginType:          "forward",
		UpstreamTag:         "u1",
		Address:             "udp://1.1.1.1:53",
		Score:               123,
		AverageLatencyMs:    12.3,
		ObservedAverageMs:   8.8,
		QueryTotal:          90,
		AttemptTotal:        120,
		ErrorTotal:          6,
		WinnerTotal:         90,
		Inflight:            1,
		ConsecutiveFailures: 0,
		Healthy:             true,
	}}
}

type fakeGroupedUpstreamHealthProvider struct{}

func (fakeGroupedUpstreamHealthProvider) SnapshotUpstreamHealth() []UpstreamHealthSnapshot {
	return []UpstreamHealthSnapshot{
		{
			PluginTag:   "fake",
			PluginType:  "forward",
			UpstreamTag: "u1",
			WinnerTotal: 30,
		},
		{
			PluginTag:   "fake",
			PluginType:  "forward",
			UpstreamTag: "u2",
			WinnerTotal: 90,
		},
	}
}

type fakeUpstreamStatsResetter struct {
	calledWith []string
	returned   int
}

func (f *fakeUpstreamStatsResetter) ResetUpstreamStats(_ context.Context, upstreamTag string) (int, error) {
	f.calledWith = append(f.calledWith, upstreamTag)
	return f.returned, nil
}

func TestHandleRuntimeSummary(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, NewTestMosdnsWithPlugins(map[string]any{"fake": fakeUpstreamHealthProvider{}}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}

	var resp runtimeSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if resp.StorageEngine != "sqlite" {
		t.Fatalf("unexpected storage engine: %+v", resp)
	}
	if len(resp.Namespaces) == 0 {
		t.Fatalf("expected namespace summary, got %+v", resp)
	}
	for _, item := range resp.Namespaces {
		if item.Namespace == "audit" {
			t.Fatalf("unexpected audit namespace in runtime summary: %+v", resp.Namespaces)
		}
	}
	if resp.Upstreams.Total != 1 || len(resp.Upstreams.Items) != 1 {
		t.Fatalf("expected upstream summary in response, got %+v", resp.Upstreams)
	}
}

func TestHandleRuntimeHealth(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, NewTestMosdnsWithPlugins(map[string]any{"fake": fakeUpstreamHealthProvider{}}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}

	var resp runtimeHealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if resp.StorageEngine != "sqlite" || len(resp.Checks) == 0 {
		t.Fatalf("unexpected health payload: %+v", resp)
	}
	if len(resp.SuggestedActions) == 0 {
		t.Fatalf("expected suggested actions, got %+v", resp)
	}
}

func TestHandleControlUpstreamHealth(t *testing.T) {
	router := chi.NewRouter()
	RegisterRuntimeAPI(router, NewTestMosdnsWithPlugins(map[string]any{"fake": fakeUpstreamHealthProvider{}}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/upstreams/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var resp upstreamHealthOverview
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode upstream health: %v", err)
	}
	if resp.Total != 1 || len(resp.Items) != 1 || resp.Items[0].UpstreamTag != "u1" {
		t.Fatalf("unexpected upstream health payload: %+v", resp)
	}
	if resp.Items[0].QueryTotal != 90 || resp.Items[0].AttemptTotal != 120 || resp.Items[0].WinnerTotal != 90 || resp.Items[0].ObservedAverageMs != 8.8 {
		t.Fatalf("unexpected upstream health stats payload: %+v", resp.Items[0])
	}
	if resp.Items[0].AcceptedRate != 100 {
		t.Fatalf("unexpected accepted rate payload: %+v", resp.Items[0])
	}
}

func TestHandleControlUpstreamHealthAcceptedRate(t *testing.T) {
	router := chi.NewRouter()
	RegisterRuntimeAPI(router, NewTestMosdnsWithPlugins(map[string]any{"fake": fakeGroupedUpstreamHealthProvider{}}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/upstreams/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	var resp upstreamHealthOverview
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode upstream health: %v", err)
	}
	if resp.Total != 2 || len(resp.Items) != 2 {
		t.Fatalf("unexpected upstream health payload: %+v", resp)
	}
	if resp.Items[0].UpstreamTag != "u1" || resp.Items[0].AcceptedRate != 25 {
		t.Fatalf("unexpected first upstream payload: %+v", resp.Items[0])
	}
	if resp.Items[1].UpstreamTag != "u2" || resp.Items[1].AcceptedRate != 75 {
		t.Fatalf("unexpected second upstream payload: %+v", resp.Items[1])
	}
}

func TestHandleControlUpstreamStatsReset(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	dbPath := RuntimeStateDBPath()
	if err := SaveUpstreamRuntimeStats(dbPath, []UpstreamRuntimeStats{
		{PluginTag: "fake", UpstreamTag: "u1", QueryTotal: 10},
		{PluginTag: "other", UpstreamTag: "keep", QueryTotal: 20},
	}); err != nil {
		t.Fatalf("SaveUpstreamRuntimeStats: %v", err)
	}

	resetter := &fakeUpstreamStatsResetter{returned: 1}
	router := chi.NewRouter()
	RegisterRuntimeAPI(router, NewTestMosdnsWithPlugins(map[string]any{"fake": resetter}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/control/upstreams/stats/reset", strings.NewReader(`{"plugin_tag":"fake","upstream_tag":"u1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", w.Code, w.Body.String())
	}
	if len(resetter.calledWith) != 1 || resetter.calledWith[0] != "u1" {
		t.Fatalf("unexpected resetter calls: %+v", resetter.calledWith)
	}

	var resp upstreamStatsResetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode reset response: %v", err)
	}
	if resp.ClearedRuntimeItems != 1 || resp.DeletedPersistentItems != 1 {
		t.Fatalf("unexpected reset response: %+v", resp)
	}

	values, err := LoadUpstreamRuntimeStatsByPlugin(dbPath, "fake")
	if err != nil {
		t.Fatalf("LoadUpstreamRuntimeStatsByPlugin fake: %v", err)
	}
	if len(values) != 0 {
		t.Fatalf("expected fake stats to be deleted, got %+v", values)
	}
	other, err := LoadUpstreamRuntimeStatsByPlugin(dbPath, "other")
	if err != nil {
		t.Fatalf("LoadUpstreamRuntimeStatsByPlugin other: %v", err)
	}
	if len(other) != 1 || other["keep"].QueryTotal != 20 {
		t.Fatalf("unexpected other stats: %+v", other)
	}
}

func TestHandleRuntimeEvents(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := RecordSystemEvent("control.test", "info", "hello", map[string]any{"ok": true}); err != nil {
		t.Fatalf("RecordSystemEvent: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/events?component=control.test&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected events status: %d body=%s", w.Code, w.Body.String())
	}
	var events []SystemEventEntry
	if err := json.Unmarshal(w.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(events) != 1 || events[0].Component != "control.test" || events[0].Message != "hello" {
		t.Fatalf("unexpected events payload: %+v", events)
	}
}

func TestRuntimeAliasesForOverridesAndUpstreams(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := saveGlobalOverridesToCustomConfig(&GlobalOverrides{Socks5: "127.0.0.1:1080"}); err != nil {
		t.Fatalf("saveGlobalOverridesToCustomConfig: %v", err)
	}
	if err := saveUpstreamOverridesToCustomConfig(GlobalUpstreamOverrides{
		"test": {{Tag: "u1", Protocol: "udp", Addr: "8.8.8.8"}},
	}); err != nil {
		t.Fatalf("saveUpstreamOverridesToCustomConfig: %v", err)
	}

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/overrides", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected overrides status: %d body=%s", w.Code, w.Body.String())
	}
	var overrides GlobalOverridesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &overrides); err != nil {
		t.Fatalf("decode overrides: %v", err)
	}
	if overrides.Socks5 != "127.0.0.1:1080" {
		t.Fatalf("unexpected overrides payload: %+v", overrides)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/control/upstreams", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected upstreams status: %d body=%s", w.Code, w.Body.String())
	}
	var upstreams GlobalUpstreamOverrides
	if err := json.Unmarshal(w.Body.Bytes(), &upstreams); err != nil {
		t.Fatalf("decode upstreams: %v", err)
	}
	if len(upstreams["test"]) != 1 {
		t.Fatalf("unexpected upstreams payload: %+v", upstreams)
	}
}

func TestRuntimeOverridesPostSupportsConcurrentRequests(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	router := chi.NewRouter()
	RegisterRuntimeAPI(router, nil)

	start := make(chan struct{})
	errCh := make(chan error, 8)
	var wg sync.WaitGroup

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			body := fmt.Sprintf(`{"socks5":"127.0.0.1:%d","ecs":"2408::%d","replacements":[]}`, 7000+i, i)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/control/overrides", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				errCh <- fmt.Errorf("status=%d body=%s", w.Code, w.Body.String())
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent overrides POST failed: %v", err)
		}
	}

	values, ok, err := loadGlobalOverridesFromCustomConfig()
	if err != nil {
		t.Fatalf("loadGlobalOverridesFromCustomConfig: %v", err)
	}
	if !ok {
		t.Fatal("expected overrides config file to exist")
	}
	if values == nil || values.Socks5 == "" {
		t.Fatalf("unexpected overrides payload after concurrent writes: %+v", values)
	}
}

func TestHandleControlShuntExplainLiveFlag(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	mustWriteShuntFile(t, filepath.Join(MainConfigBaseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(MainConfigBaseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: my_fakeiprule
    type: domain_set_light
    args:
      generated_from: my_fakeiplist
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: my_fakeiprule
          mark: 12
          output_tag: 记忆代理
`)

	m := NewTestMosdnsWithPlugins(map[string]any{
		"my_fakeiplist": &mockHotRuleSnapshotProvider{rules: []string{"full:live-proxy.example"}},
	})
	router := chi.NewRouter()
	RegisterRuntimeAPI(router, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/shunt/explain?domain=live-proxy.example&qtype=A&live=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected live explain status: %d body=%s", w.Code, w.Body.String())
	}

	var liveResp shuntExplainResult
	if err := json.Unmarshal(w.Body.Bytes(), &liveResp); err != nil {
		t.Fatalf("decode live explain: %v", err)
	}
	if liveResp.Decision.Matched != 12 || len(liveResp.Matches) != 1 {
		t.Fatalf("unexpected live explain payload: %+v", liveResp)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/control/shunt/explain?domain=live-proxy.example&qtype=A&live=false", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected persisted explain status: %d body=%s", w.Code, w.Body.String())
	}

	var persistedResp shuntExplainResult
	if err := json.Unmarshal(w.Body.Bytes(), &persistedResp); err != nil {
		t.Fatalf("decode persisted explain: %v", err)
	}
	if len(persistedResp.Matches) != 0 || persistedResp.Decision.Matched == 12 {
		t.Fatalf("expected live=false to ignore runtime hot rule, got %+v", persistedResp)
	}
}

func TestHandleControlShuntConflictsLiveFlag(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	mustWriteShuntFile(t, filepath.Join(MainConfigBaseDir, "custom_config", "switches.yaml"), `{}`)
	mustWriteShuntFile(t, filepath.Join(MainConfigBaseDir, dataSourcePolicyConfigRelPath), `
policies:
  - name: static_hot
    type: domain_set_light
    args:
      files:
        - rule/hot.txt
  - name: my_fakeiprule
    type: domain_set_light
    args:
      generated_from: my_fakeiplist
  - name: unified_matcher1
    type: domain_mapper
    args:
      default_mark: 17
      default_tag: 未命中
      rules:
        - tag: static_hot
          mark: 8
          output_tag: 静态
        - tag: my_fakeiprule
          mark: 12
          output_tag: 记忆代理
`)
	mustWriteShuntFile(t, filepath.Join(MainConfigBaseDir, "rule", "hot.txt"), "full:live-proxy.example\n")

	m := NewTestMosdnsWithPlugins(map[string]any{
		"my_fakeiplist": &mockHotRuleSnapshotProvider{rules: []string{"full:live-proxy.example"}},
	})
	router := chi.NewRouter()
	RegisterRuntimeAPI(router, m)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/control/shunt/conflicts?live=true&limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected live conflicts status: %d body=%s", w.Code, w.Body.String())
	}

	var liveResp struct {
		Count     int                  `json:"count"`
		Conflicts []shuntConflictEntry `json:"conflicts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &liveResp); err != nil {
		t.Fatalf("decode live conflicts: %v", err)
	}
	if liveResp.Count != 1 || len(liveResp.Conflicts) != 1 {
		t.Fatalf("expected one live conflict, got %+v", liveResp)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/control/shunt/conflicts?live=false&limit=10", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected persisted conflicts status: %d body=%s", w.Code, w.Body.String())
	}

	var persistedResp struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &persistedResp); err != nil {
		t.Fatalf("decode persisted conflicts: %v", err)
	}
	if persistedResp.Count != 0 {
		t.Fatalf("expected no persisted conflicts without live hot rules, got %+v", persistedResp)
	}
}
