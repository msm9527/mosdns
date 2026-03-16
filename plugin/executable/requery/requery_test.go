package requery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/miekg/dns"
	_ "modernc.org/sqlite"
)

func newTestRequeryStore(dir string) (string, string) {
	return "state/requery", filepath.Join(dir, "control.db")
}

func TestRequeryAPI_GetConfigAndStatus(t *testing.T) {
	t.Parallel()

	p := &Requery{
		config: &Config{
			Workflow: WorkflowSettings{Mode: "hybrid"},
			Status:   Status{TaskState: "idle"},
		},
		status: Status{TaskState: "idle"},
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	p.api().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status for config: %d, body=%s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/status", nil)
	w = httptest.NewRecorder()
	p.api().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status for status: %d, body=%s", w.Code, w.Body.String())
	}
}

func TestRequeryAPI_Enqueue(t *testing.T) {
	t.Parallel()

	p := &Requery{
		config: &Config{
			ExecutionSettings: ExecutionSettings{MaxQueueSize: 8},
			Workflow:          WorkflowSettings{Mode: "hybrid"},
		},
		status:     Status{TaskState: "idle"},
		queue:      make(refreshJobHeap, 0),
		queueIndex: make(map[string]struct{}),
		queueKick:  make(chan struct{}, 1),
	}

	req := httptest.NewRequest(http.MethodPost, "/enqueue", strings.NewReader(`{"domain":"example.com","reason":"observed"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	p.api().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("unexpected status for enqueue: %d, body=%s", w.Code, w.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode enqueue response: %v", err)
	}
	if payload["status"] != "queued" {
		t.Fatalf("unexpected enqueue payload: %+v", payload)
	}
}

func TestMergeAndFilterDomainsParsesQTypeMask(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "top.txt")
	content := "0000000002 2026-03-06 example.com qmask=1 score=2 promoted=1\n"
	if err := os.WriteFile(source, []byte(content), 0644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	p := &Requery{config: &Config{DomainProcessing: DomainProcessing{SourceFiles: []SourceFile{{Alias: "top", Path: source}}}, ExecutionSettings: ExecutionSettings{DateRangeDays: 30}}}
	got, err := p.mergeAndFilterDomains(context.Background(), taskProfile{Mode: "full_rebuild"})
	if err != nil {
		t.Fatalf("mergeAndFilterDomains: %v", err)
	}
	if len(got) != 1 || got[0].Name != "example.com" || got[0].QTypeMask != qtypeMaskA {
		t.Fatalf("unexpected candidates: %#v", got)
	}
}

func TestMergeAndFilterDomainsPrefersRuntimeCandidatesForQuickMode(t *testing.T) {
	t.Parallel()

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"my_realiplist": mockRefreshCandidateProvider{
			candidates: []coremain.DomainRefreshCandidate{
				{Domain: "dirty.example", QTypeMask: qtypeMaskA, Weight: 9000, Reason: "stale"},
				{Domain: "hot.example", QTypeMask: qtypeMaskAAAA, Weight: 1000},
			},
		},
	})

	p := &Requery{
		m: m,
		config: &Config{
			URLActions: URLActions{SaveRules: []string{"/api/v1/memory/my_realiplist/save"}},
		},
	}

	got, err := p.mergeAndFilterDomains(context.Background(), taskProfile{Mode: "quick_rebuild", Limit: 1})
	if err != nil {
		t.Fatalf("mergeAndFilterDomains: %v", err)
	}
	if len(got) != 1 || got[0].Name != "dirty.example" || got[0].QTypeMask != qtypeMaskA {
		t.Fatalf("unexpected runtime candidates: %#v", got)
	}
}

func TestMergeAndFilterDomainsMergesRuntimeCandidatesForFullMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "top.txt")
	content := "0000000002 2026-03-06 file.example qmask=1 score=2 promoted=1\n"
	if err := os.WriteFile(source, []byte(content), 0644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"my_realiplist": mockRefreshCandidateProvider{
			candidates: []coremain.DomainRefreshCandidate{
				{Domain: "runtime.example", QTypeMask: qtypeMaskAAAA, Weight: 9000, Reason: "stale"},
			},
		},
	})

	p := &Requery{
		m: m,
		config: &Config{
			DomainProcessing:  DomainProcessing{SourceFiles: []SourceFile{{Alias: "top", Path: source}}},
			ExecutionSettings: ExecutionSettings{DateRangeDays: 30},
			URLActions:        URLActions{SaveRules: []string{"/api/v1/memory/my_realiplist/save"}},
		},
	}

	got, err := p.mergeAndFilterDomains(context.Background(), taskProfile{Mode: "full_rebuild"})
	if err != nil {
		t.Fatalf("mergeAndFilterDomains: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected merged candidates, got %#v", got)
	}
	if got[0].Name != "runtime.example" || got[1].Name != "file.example" {
		t.Fatalf("unexpected candidate order: %#v", got)
	}
}

func TestBuildTaskCandidatePlanSplitsFullRebuildStages(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "top.txt")
	content := "" +
		"0000000002 2026-03-06 file-a.example qmask=1 score=2 promoted=1\n" +
		"0000000001 2026-03-06 file-b.example qmask=1 score=1 promoted=1\n"
	if err := os.WriteFile(source, []byte(content), 0644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	m := coremain.NewTestMosdnsWithPlugins(map[string]any{
		"my_realiplist": mockRefreshCandidateProvider{
			candidates: []coremain.DomainRefreshCandidate{
				{Domain: "runtime-a.example", QTypeMask: qtypeMaskA, Weight: 9000, Reason: "stale"},
				{Domain: "runtime-b.example", QTypeMask: qtypeMaskAAAA, Weight: 8000, Reason: "refresh_due"},
			},
		},
	})

	p := &Requery{
		m: m,
		config: &Config{
			DomainProcessing: DomainProcessing{SourceFiles: []SourceFile{{Alias: "top", Path: source}}},
			ExecutionSettings: ExecutionSettings{
				DateRangeDays:            30,
				FullRebuildPriorityLimit: 1,
			},
			URLActions: URLActions{SaveRules: []string{"/api/v1/memory/my_realiplist/save"}},
		},
	}

	plan, err := p.buildTaskCandidatePlan(context.Background(), taskProfile{Mode: "full_rebuild"})
	if err != nil {
		t.Fatalf("buildTaskCandidatePlan: %v", err)
	}
	if len(plan.Primary) != 1 || plan.Primary[0].Name != "runtime-a.example" {
		t.Fatalf("unexpected primary stage: %#v", plan.Primary)
	}
	if len(plan.Secondary) != 3 || plan.Secondary[0].Name != "runtime-b.example" {
		t.Fatalf("unexpected secondary stage: %#v", plan.Secondary)
	}
}

func TestRunTaskUsesRefreshResolverAndSkipsLegacyFlush(t *testing.T) {
	t.Parallel()

	dnsAddr, queries, shutdownDNS := startTestDNSServer(t)
	defer shutdownDNS()

	var mu sync.Mutex
	var hits []string
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer httpSrv.Close()

	dir := t.TempDir()
	runtimeKey, dbPath := newTestRequeryStore(dir)
	source := filepath.Join(dir, "top.txt")
	if err := os.WriteFile(source, []byte("0000000002 2026-03-06 example.com qmask=1 score=2 promoted=1\n"), 0644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	p := &Requery{
		runtimeKey: runtimeKey,
		dbPath:     dbPath,
		httpClient: &http.Client{Timeout: 2 * time.Second},
		config: &Config{
			DomainProcessing: DomainProcessing{SourceFiles: []SourceFile{{Alias: "top", Path: source}}},
			URLActions: URLActions{
				SaveRules:  []string{httpSrv.URL + "/save"},
				FlushRules: []string{httpSrv.URL + "/flush"},
			},
			Workflow: WorkflowSettings{
				FlushMode:         "none",
				SaveBeforeRefresh: boolPtr(true),
				SaveAfterRefresh:  boolPtr(true),
			},
			ExecutionSettings: ExecutionSettings{
				QueriesPerSecond:       50,
				ResolverAddress:        "127.0.0.1:7766",
				RefreshResolverAddress: dnsAddr,
				QueryMode:              "observed",
				DateRangeDays:          30,
			},
			Status: Status{TaskState: "idle"},
		},
		status: Status{TaskState: "idle"},
	}

	p.runTask(context.Background(), p.profileForMode("full_rebuild", 0), nil)

	mu.Lock()
	gotHits := append([]string(nil), hits...)
	mu.Unlock()
	if len(gotHits) != 2 || gotHits[0] != "/save" || gotHits[1] != "/save" {
		t.Fatalf("unexpected url hits: %#v", gotHits)
	}
	if count := len(queries()); count != 1 {
		t.Fatalf("expected one A query via refresh resolver, got %d", count)
	}
	if p.status.Progress.Total != 1 || p.status.TaskState != "idle" {
		t.Fatalf("unexpected status after run: %+v", p.status)
	}
}

func TestOnDemandQueueRefreshesAndVerifies(t *testing.T) {
	t.Parallel()

	dnsAddr, queries, shutdownDNS := startTestDNSServer(t)
	defer shutdownDNS()

	var (
		mu        sync.Mutex
		saveHits  int
		verifyHit []string
	)
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		switch r.URL.Path {
		case "/save":
			mu.Lock()
			saveHits++
			mu.Unlock()
		case "/verify":
			var payload map[string]string
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode verify payload: %v", err)
			}
			mu.Lock()
			verifyHit = append(verifyHit, payload["domain"])
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer httpSrv.Close()

	p := &Requery{
		httpClient: &http.Client{Timeout: 2 * time.Second},
		queueIndex: make(map[string]struct{}),
		queueKick:  make(chan struct{}, 1),
		config: &Config{
			URLActions: URLActions{SaveRules: []string{httpSrv.URL + "/save"}},
			Workflow: WorkflowSettings{
				Mode:              "hybrid",
				SaveAfterRefresh:  boolPtr(true),
				SaveBeforeRefresh: boolPtr(true),
			},
			ExecutionSettings: ExecutionSettings{
				QueriesPerSecond:       50,
				RefreshResolverAddress: dnsAddr,
				QueryMode:              "observed",
				MaxQueueSize:           16,
			},
			Status: Status{TaskState: "idle"},
		},
		status: Status{TaskState: "idle"},
	}

	if ok := p.enqueueRefreshJob(refreshJob{
		Domain:    "example.com",
		MemoryID:  "realip",
		QTypeMask: qtypeMaskA,
		Reason:    "stale",
		VerifyURL: httpSrv.URL + "/verify",
	}); !ok {
		t.Fatal("expected enqueue to succeed")
	}

	jobs := p.dequeueRefreshBatch(8)
	if len(jobs) != 1 {
		t.Fatalf("expected one queued job, got %d", len(jobs))
	}
	p.processOnDemandBatch(jobs)

	if count := len(queries()); count != 1 {
		t.Fatalf("expected one on-demand refresh query, got %d", count)
	}
	mu.Lock()
	defer mu.Unlock()
	if saveHits != 1 {
		t.Fatalf("expected one save hit, got %d", saveHits)
	}
	if len(verifyHit) != 1 || verifyHit[0] != "example.com" {
		t.Fatalf("unexpected verify hits: %#v", verifyHit)
	}
	if p.status.OnDemandProcessed != 1 || p.status.PendingQueue != 0 {
		t.Fatalf("unexpected on-demand status: %+v", p.status)
	}
}

func TestResolverAddressesForProfileUsesPool(t *testing.T) {
	t.Parallel()

	p := &Requery{
		config: &Config{
			ExecutionSettings: ExecutionSettings{
				ResolverAddress:        "127.0.0.1:5300",
				RefreshResolverAddress: "127.0.0.1:5301",
				RefreshResolverPool:    []string{"127.0.0.1:5302", "127.0.0.1:5303", "127.0.0.1:5302"},
			},
		},
	}

	got := p.resolverAddressesForProfile(taskProfile{Mode: "full_rebuild", ResolverAddr: p.refreshResolverAddress()})
	if len(got) != 3 {
		t.Fatalf("expected 3 unique resolvers, got %#v", got)
	}
	if got[0] != "127.0.0.1:5301" || got[1] != "127.0.0.1:5302" || got[2] != "127.0.0.1:5303" {
		t.Fatalf("unexpected resolver list: %#v", got)
	}
}

func TestPrepareRecoveryOnStartupMarksInterruptedFullRebuildRecoverable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runtimeKey, dbPath := newTestRequeryStore(dir)

	persist := &Requery{
		runtimeKey: runtimeKey,
		dbPath:     dbPath,
		config: &Config{
			Workflow: WorkflowSettings{
				Mode:              "hybrid",
				SaveBeforeRefresh: boolPtr(true),
				SaveAfterRefresh:  boolPtr(true),
			},
			Recovery: RecoverySettings{
				AutoResume:          boolPtr(true),
				CheckpointBatchSize: 2,
				ResumeDelayMS:       1,
			},
			Status: Status{TaskState: "running"},
		},
		status: Status{TaskState: "running"},
	}

	task := &FullRebuildTask{
		TaskID:     "task-1",
		Mode:       "full_rebuild",
		Stage:      "tail",
		StageLabel: "长尾补全阶段",
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		Total:      3,
		Completed:  1,
		Secondary: []domainCandidate{
			{Name: "resume.example", QTypeMask: qtypeMaskA},
		},
	}
	if err := persist.saveConfigUnlocked(); err != nil {
		t.Fatalf("saveConfigUnlocked: %v", err)
	}
	if err := persist.persistFullRebuildTask(task); err != nil {
		t.Fatalf("persistFullRebuildTask: %v", err)
	}

	reloaded := &Requery{runtimeKey: runtimeKey, dbPath: dbPath}
	if err := reloaded.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	reloaded.prepareRecoveryOnStartup()

	if reloaded.fullTask == nil {
		t.Fatal("expected persisted full rebuild task")
	}
	if reloaded.status.TaskState != "failed" {
		t.Fatalf("expected failed state for interrupted task, got %q", reloaded.status.TaskState)
	}
	if reloaded.status.TaskStage != "tail" || reloaded.status.TaskStageLabel != "长尾补全阶段" {
		t.Fatalf("unexpected recovered stage: %+v", reloaded.status)
	}
	if reloaded.status.Progress.Total != 3 || reloaded.status.Progress.Processed != 1 {
		t.Fatalf("unexpected recovered progress: %+v", reloaded.status.Progress)
	}
}

func TestApplyConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		URLActions: URLActions{
			FlushRules: []string{"/api/v1/cache/cache_cn/flush"},
		},
		Scheduler: SchedulerConfig{Enabled: true},
		ExecutionSettings: ExecutionSettings{
			RefreshResolverPool: []string{"127.0.0.1:5302", " 127.0.0.1:5303 ", "127.0.0.1:5302"},
		},
	}

	if !applyConfigDefaults(cfg) {
		t.Fatal("expected defaults to be applied")
	}
	if cfg.Status.TaskState != "idle" {
		t.Fatalf("unexpected task state: %q", cfg.Status.TaskState)
	}
	if cfg.Workflow.Mode != "hybrid" {
		t.Fatalf("unexpected workflow mode: %q", cfg.Workflow.Mode)
	}
	if cfg.Workflow.FlushMode != "legacy" {
		t.Fatalf("unexpected flush mode: %q", cfg.Workflow.FlushMode)
	}
	if cfg.ExecutionSettings.URLCallDelayMS != defaultURLCallDelayMS {
		t.Fatalf("unexpected url call delay: %d", cfg.ExecutionSettings.URLCallDelayMS)
	}
	if len(cfg.ExecutionSettings.RefreshResolverPool) != 2 || cfg.ExecutionSettings.RefreshResolverPool[0] != "127.0.0.1:5302" || cfg.ExecutionSettings.RefreshResolverPool[1] != "127.0.0.1:5303" {
		t.Fatalf("unexpected resolver pool: %#v", cfg.ExecutionSettings.RefreshResolverPool)
	}
	if cfg.Recovery.AutoResume == nil || !*cfg.Recovery.AutoResume {
		t.Fatalf("unexpected auto resume: %#v", cfg.Recovery.AutoResume)
	}
}

func TestLoadConfigInitializesRuntimeStateWithoutFiles(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	runtimeKey, dbPath := newTestRequeryStore(dir)

	p := &Requery{runtimeKey: runtimeKey, dbPath: dbPath}
	if err := p.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if p.fullTask != nil {
		t.Fatalf("unexpected task after init: %#v", p.fullTask)
	}
	if p.status.TaskState != "idle" {
		t.Fatalf("unexpected status after init: %+v", p.status)
	}
	if _, err := os.Stat(filepath.Join(dir, "state", "requeryconfig.json")); !os.IsNotExist(err) {
		t.Fatalf("expected pseudo config file to stay absent, got err=%v", err)
	}
	if ok, err := coremain.LoadRuntimeStateJSONFromPath(p.runtimeDBPath(), runtimeStateNamespaceRequery, p.runtimeConfigKey(), &persistedConfig{}); err != nil || !ok {
		t.Fatalf("expected runtime config in DB, ok=%v err=%v", ok, err)
	}
	if ok, err := coremain.LoadRuntimeStateJSONFromPath(p.runtimeDBPath(), runtimeStateNamespaceRequery, p.runtimeStateKey(), &persistedState{}); err != nil || !ok {
		t.Fatalf("expected runtime state in DB, ok=%v err=%v", ok, err)
	}
}

func TestBeginTaskExecutionRollsBackOnPersistFailure(t *testing.T) {
	oldBaseDir := coremain.MainConfigBaseDir
	coremain.MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		coremain.MainConfigBaseDir = oldBaseDir
	})

	p := &Requery{
		runtimeKey: "state/requery",
		dbPath:     filepath.Join(coremain.MainConfigBaseDir, "control.db"),
		config:     newDefaultConfig(),
		status:     Status{TaskState: "idle"},
	}
	if err := p.saveStateUnlocked(); err != nil {
		t.Fatalf("seed runtime state: %v", err)
	}

	lockDB, err := sql.Open("sqlite", p.runtimeDBPath())
	if err != nil {
		t.Fatalf("open lock db: %v", err)
	}
	defer lockDB.Close()
	tx, err := lockDB.Begin()
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	if _, err := tx.Exec(`UPDATE requery_state SET payload_json = payload_json WHERE file_path = ? AND state_kind = ?`, p.normalizedRuntimeKey(), "state"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("lock requery state row: %v", err)
	}
	defer tx.Rollback()

	p.taskCtx, p.taskCancel = context.WithCancel(context.Background())
	p.activeTriggerSource = "manual"
	if ok := p.beginTaskExecution(taskProfile{Mode: "quick_prewarm"}, nil); ok {
		t.Fatal("expected beginTaskExecution to fail while sqlite row is locked")
	}
	if p.status.TaskState != "idle" {
		t.Fatalf("expected task state to roll back to idle, got %q", p.status.TaskState)
	}
	if p.activeRunID != "" || p.status.ActiveRunID != "" {
		t.Fatalf("expected active run id to be cleared, got %q / %q", p.activeRunID, p.status.ActiveRunID)
	}
	if p.taskCtx != nil || p.taskCancel != nil {
		t.Fatal("expected task context to be cleared after failed start")
	}
	if p.lastError == "" {
		t.Fatal("expected lastError to capture persist failure")
	}
}

func startTestDNSServer(t *testing.T) (string, func() []uint16, func()) {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}

	var mu sync.Mutex
	var qtypes []uint16
	server := &dns.Server{PacketConn: pc, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		if len(r.Question) > 0 {
			mu.Lock()
			qtypes = append(qtypes, r.Question[0].Qtype)
			mu.Unlock()
		}
		resp := new(dns.Msg)
		resp.SetReply(r)
		if len(r.Question) > 0 {
			switch r.Question[0].Qtype {
			case dns.TypeA:
				rr, _ := dns.NewRR(fmt.Sprintf("%s 60 IN A 1.1.1.1", r.Question[0].Name))
				resp.Answer = append(resp.Answer, rr)
			case dns.TypeAAAA:
				rr, _ := dns.NewRR(fmt.Sprintf("%s 60 IN AAAA 240c::1", r.Question[0].Name))
				resp.Answer = append(resp.Answer, rr)
			}
		}
		_ = w.WriteMsg(resp)
	})}
	go func() { _ = server.ActivateAndServe() }()
	time.Sleep(50 * time.Millisecond)

	return pc.LocalAddr().String(), func() []uint16 {
			mu.Lock()
			defer mu.Unlock()
			out := make([]uint16, len(qtypes))
			copy(out, qtypes)
			return out
		}, func() {
			_ = server.Shutdown()
			_ = pc.Close()
		}
}

type mockRefreshCandidateProvider struct {
	candidates []coremain.DomainRefreshCandidate
}

func (m mockRefreshCandidateProvider) SnapshotRefreshCandidates(req coremain.DomainRefreshCandidateRequest) []coremain.DomainRefreshCandidate {
	if req.Limit > 0 && len(m.candidates) > req.Limit {
		return append([]coremain.DomainRefreshCandidate(nil), m.candidates[:req.Limit]...)
	}
	return append([]coremain.DomainRefreshCandidate(nil), m.candidates...)
}
