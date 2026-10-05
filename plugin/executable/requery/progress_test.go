package requery

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// The server receives real UDP queries and holds each response until released.
func startBlockedProgressDNS(t *testing.T) (string, <-chan uint16, func(uint16)) {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan uint16, 32)
	gates := map[uint16]chan struct{}{dns.TypeA: make(chan struct{}), dns.TypeAAAA: make(chan struct{})}
	var mu sync.Mutex
	release := func(qtype uint16) {
		mu.Lock()
		defer mu.Unlock()
		select {
		case <-gates[qtype]:
		default:
			close(gates[qtype])
		}
	}
	ready := make(chan struct{})
	server := &dns.Server{PacketConn: pc, NotifyStartedFunc: func() { close(ready) }, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
		qtype := req.Question[0].Qtype
		received <- qtype
		<-gates[qtype]
		resp := new(dns.Msg)
		resp.SetReply(req)
		_ = w.WriteMsg(resp)
	})}
	go func() { _ = server.ActivateAndServe() }()
	<-ready
	t.Cleanup(func() {
		release(dns.TypeA)
		release(dns.TypeAAAA)
		_ = server.Shutdown()
		_ = pc.Close()
	})
	return pc.LocalAddr().String(), received, release
}

func receiveProgressQueries(t *testing.T, received <-chan uint16, n int) {
	t.Helper()
	for range n {
		select {
		case <-received:
		case <-time.After(3 * time.Second):
			t.Fatal("DNS query did not reach the local server")
		}
	}
}

func progressStatus(p *Requery) Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status
}

func TestProgressWaitsForBothDNSAttempts(t *testing.T) {
	addr, received, release := startBlockedProgressDNS(t)
	p := &Requery{config: &Config{ExecutionSettings: ExecutionSettings{QueryMode: "observed"}}, status: Status{TaskState: "running", Progress: Progress{Total: 1}}}
	done := make(chan error, 1)
	go func() {
		done <- p.resendDNSQueries(context.Background(), []domainCandidate{{Name: "held.example", QTypeMask: qtypeMaskA | qtypeMaskAAAA}}, true, taskProfile{QPS: 1000, ResolverAddr: addr})
	}()
	receiveProgressQueries(t, received, 2)
	if got := progressStatus(p); got.Progress.Processed != 0 || got.TaskStageProcessed != 0 {
		t.Errorf("queued DNS attempts must not count as processed domains: %+v", got)
	}
	release(dns.TypeA)
	time.Sleep(30 * time.Millisecond)
	if got := progressStatus(p); got.Progress.Processed != 0 {
		t.Errorf("one completed qtype must not count as a processed dual-stack domain: %+v", got)
	}
	release(dns.TypeAAAA)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := progressStatus(p); got.Progress.Processed != 1 || got.TaskStageProcessed != 1 {
		t.Fatalf("both completed attempts should count once: %+v", got)
	}
}

func TestProgressCancellationAfterDispatch(t *testing.T) {
	addr, received, _ := startBlockedProgressDNS(t)
	runtimeKey, dbPath := newTestRequeryStore(t.TempDir())
	p := &Requery{runtimeKey: runtimeKey, dbPath: dbPath, config: &Config{ExecutionSettings: ExecutionSettings{QueryMode: "observed"}}, status: Status{TaskState: "running", Progress: Progress{Total: 1}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- p.resendDNSQueries(ctx, []domainCandidate{{Name: "cancel.example", QTypeMask: qtypeMaskA | qtypeMaskAAAA}}, true, taskProfile{QPS: 1000, ResolverAddr: addr})
	}()
	receiveProgressQueries(t, received, 2)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("cancel after all jobs were dispatched must still return cancellation, got %v", err)
	}
	if got := progressStatus(p); got.Progress.Processed != 0 || got.TaskState != "cancelled" {
		t.Fatalf("cancelled DNS attempts must not complete the domain: %+v", got)
	}
}

func TestProgressTimeoutCountsCompletedAttempts(t *testing.T) {
	addr, received, _ := startBlockedProgressDNS(t)
	p := &Requery{config: &Config{}, status: Status{Progress: Progress{Total: 1}}}
	done := make(chan error, 1)
	go func() {
		done <- p.resendDNSQueries(context.Background(), []domainCandidate{{Name: "timeout.example", QTypeMask: qtypeMaskA | qtypeMaskAAAA}}, true, taskProfile{QPS: 1000, ResolverAddr: addr})
	}()
	receiveProgressQueries(t, received, 2)
	if got := progressStatus(p); got.Progress.Processed != 0 {
		t.Errorf("in-flight attempts must not count as completed: %+v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := progressStatus(p); got.Progress.Processed != 1 || got.TaskStageProcessed != 1 {
		t.Fatalf("timed-out attempts should count as processed once, without asserting DNS success: %+v", got)
	}
}

type blockedProgressCache struct {
	entered chan struct{}
	release chan struct{}
}

func (*blockedProgressCache) RuntimeCacheKind() string    { return "response" }
func (*blockedProgressCache) RuntimeCacheEntryCount() int { return 10 }
func (c *blockedProgressCache) FlushRuntimeCache(ctx context.Context) error {
	close(c.entered)
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (c *blockedProgressCache) PurgeDomainsRuntimeCache(ctx context.Context, domains []string, _ []uint16) (int, error) {
	return len(domains), c.FlushRuntimeCache(ctx)
}

func TestRunTaskReportsPublicationInvalidationAndPostwarm(t *testing.T) {
	refreshAddr, _, shutdownRefresh := startTestDNSServer(t)
	defer shutdownRefresh()
	warmAddr, warmReceived, releaseWarm := startBlockedProgressDNS(t)
	publishEntered, releasePublish := make(chan struct{}), make(chan struct{})
	var publishOnce sync.Once
	cache := &blockedProgressCache{entered: make(chan struct{}), release: make(chan struct{})}
	var cacheOnce sync.Once
	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(publishEntered)
		<-releasePublish
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(httpSrv.Close)
	dir := t.TempDir()
	runtimeKey, dbPath := newTestRequeryStore(dir)
	source := filepath.Join(dir, "domains.txt")
	if err := os.WriteFile(source, []byte(fmt.Sprintf("0000000002 %s phased.example qmask=1 score=2 promoted=1\n", recentObservedDate())), 0600); err != nil {
		t.Fatal(err)
	}
	p := &Requery{
		runtimeKey: runtimeKey, dbPath: dbPath,
		httpClient:  &http.Client{Timeout: 5 * time.Second},
		snapshotter: mockSnapshotter{plugins: map[string]any{"cache_main": cache}},
		config: &Config{
			DomainProcessing: DomainProcessing{SourceFiles: []SourceFile{{Alias: "top", Path: source}}},
			URLActions:       URLActions{SaveRules: []string{httpSrv.URL}},
			Workflow:         WorkflowSettings{SaveAfterRefresh: boolPtr(true)},
			ExecutionSettings: ExecutionSettings{
				ResolverAddress: warmAddr, RefreshResolverAddress: refreshAddr,
				QueriesPerSecond: 1000, QuickQueriesPerSecond: 1000, PrewarmQueriesPerSecond: 1000,
				QueryMode: "observed", DateRangeDays: 30, PrewarmLimit: 1,
			},
		},
		status: Status{TaskState: "idle"},
	}
	done := make(chan struct{})
	go func() { p.runTask(context.Background(), p.profileForMode("quick_rebuild", 1), nil); close(done) }()
	t.Cleanup(func() {
		publishOnce.Do(func() { close(releasePublish) })
		cacheOnce.Do(func() { close(cache.release) })
		releaseWarm(dns.TypeA)
		releaseWarm(dns.TypeAAAA)
		<-done
	})
	waitSignal := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatal("task did not reach the expected phase")
		}
	}
	assertStage := func(stage string) {
		t.Helper()
		if got := progressStatus(p); got.TaskState != "running" || got.TaskStage != stage || got.TaskStageLabel != stageLabel(stage) || got.Progress.Processed != 1 || got.Progress.Total != 1 {
			t.Fatalf("expected active %s after completed refresh plan, got %+v", stage, got)
		}
	}
	waitSignal(publishEntered)
	assertStage("publish")
	publishOnce.Do(func() { close(releasePublish) })
	waitSignal(cache.entered)
	assertStage("cache_invalidation")
	cacheOnce.Do(func() { close(cache.release) })
	receiveProgressQueries(t, warmReceived, 2)
	assertStage("postwarm")
	if got := progressStatus(p); got.TaskStageProcessed != 0 || got.TaskStageTotal != 1 {
		t.Fatalf("postwarm must expose independent incomplete domain progress: %+v", got)
	}
	releaseWarm(dns.TypeA)
	time.Sleep(30 * time.Millisecond)
	if got := progressStatus(p); got.TaskStageProcessed != 0 {
		t.Fatalf("postwarm must wait for both required attempts: %+v", got)
	}
	releaseWarm(dns.TypeAAAA)
	waitSignal(done)
	if got := progressStatus(p); got.TaskState != "idle" || got.TaskStage != "" || got.Progress.Processed != 1 || got.Progress.Total != 1 {
		t.Fatalf("unexpected completed task status: %+v", got)
	}
}

func TestPostwarmCountsDomainsWithoutChangingRefreshProgress(t *testing.T) {
	addr, _, shutdown := startTestDNSServer(t)
	defer shutdown()
	runtimeKey, dbPath := newTestRequeryStore(t.TempDir())
	p := &Requery{runtimeKey: runtimeKey, dbPath: dbPath,
		config: &Config{ExecutionSettings: ExecutionSettings{ResolverAddress: addr, PrewarmQueriesPerSecond: 1000, PrewarmLimit: 1}},
		status: Status{TaskState: "running", Progress: Progress{Processed: 3, Total: 3}},
	}
	if !p.runPostPublishPrewarm(context.Background(), taskProfile{PostWarm: true}, []string{"warm.example", "limited.example"}) {
		t.Fatal("postwarm failed")
	}
	if got := progressStatus(p); got.Progress.Processed != 3 || got.Progress.Total != 3 || got.TaskStage != "postwarm" || got.TaskStageProcessed != 1 || got.TaskStageTotal != 1 {
		t.Fatalf("postwarm must count its own limited domain plan: %+v", got)
	}
}

func TestOnDemandPostwarmDoesNotChangeRunningTaskProgress(t *testing.T) {
	addr, _, shutdown := startTestDNSServer(t)
	defer shutdown()
	p := &Requery{config: &Config{ExecutionSettings: ExecutionSettings{ResolverAddress: addr, PrewarmQueriesPerSecond: 1000}},
		status: Status{TaskState: "running", TaskStage: "priority", TaskStageProcessed: 2, TaskStageTotal: 4, Progress: Progress{Processed: 2, Total: 4}},
	}
	before := progressStatus(p)
	if err := p.prewarmChangedDomainsAfterPublish(context.Background(), []domainCandidate{{Name: "ondemand.example", QTypeMask: qtypeMaskA | qtypeMaskAAAA}}); err != nil {
		t.Fatal(err)
	}
	if got := progressStatus(p); got != before {
		t.Fatalf("on-demand warming must leave running task progress alone: before=%+v after=%+v", before, got)
	}
}

func TestPostwarmCancellationKeepsIncompleteStageAndCancelledState(t *testing.T) {
	addr, received, _ := startBlockedProgressDNS(t)
	runtimeKey, dbPath := newTestRequeryStore(t.TempDir())
	p := &Requery{runtimeKey: runtimeKey, dbPath: dbPath,
		config: &Config{ExecutionSettings: ExecutionSettings{ResolverAddress: addr, PrewarmQueriesPerSecond: 1000}},
		status: Status{TaskState: "running", Progress: Progress{Processed: 1, Total: 1}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan bool, 1)
	go func() {
		done <- p.runPostPublishPrewarm(ctx, taskProfile{PostWarm: true}, []string{"cancelwarm.example"})
	}()
	receiveProgressQueries(t, received, 2)
	cancel()
	if <-done {
		t.Fatal("cancelled postwarm must stop finalization")
	}
	if got := progressStatus(p); got.TaskState != "cancelled" || got.TaskStage != "postwarm" || got.TaskStageProcessed != 0 || got.TaskStageTotal != 1 || got.Progress.Processed != 1 {
		t.Fatalf("unexpected postwarm cancellation state: %+v", got)
	}
}

func TestCheckpointWaitsForDNSCompletion(t *testing.T) {
	addr, received, release := startBlockedProgressDNS(t)
	runtimeKey, dbPath := newTestRequeryStore(t.TempDir())
	domains := []domainCandidate{{Name: "checkpoint.example", QTypeMask: qtypeMaskA | qtypeMaskAAAA}}
	p := &Requery{runtimeKey: runtimeKey, dbPath: dbPath,
		config: &Config{Recovery: RecoverySettings{CheckpointBatchSize: 1}},
		status: Status{TaskState: "running", Progress: Progress{Total: 1}},
	}
	task := newFullRebuildTask(taskCandidatePlan{Primary: domains})
	if err := p.persistFullRebuildTask(task); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- p.runStageWithCheckpoint(context.Background(), taskProfile{QPS: 1000, ResolverAddr: addr}, task, "priority", domains, nil, nil)
	}()
	receiveProgressQueries(t, received, 2)
	p.mu.RLock()
	initial := cloneFullRebuildTask(p.fullTask)
	p.mu.RUnlock()
	if initial.Completed != 0 || len(initial.Primary) != 1 {
		t.Errorf("checkpoint must retain a domain while DNS attempts are in flight: %+v", initial)
	}
	release(dns.TypeA)
	release(dns.TypeAAAA)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	p.mu.RLock()
	completed := cloneFullRebuildTask(p.fullTask)
	p.mu.RUnlock()
	if completed.Completed != 1 || len(completed.Primary) != 0 {
		t.Fatalf("completed domain should advance the existing checkpoint: %+v", completed)
	}
}

func TestFullRebuildRecoveryProcessesOnlyRemainingDomains(t *testing.T) {
	addr, queries, shutdown := startTestDNSServer(t)
	defer shutdown()
	runtimeKey, dbPath := newTestRequeryStore(t.TempDir())
	p := &Requery{runtimeKey: runtimeKey, dbPath: dbPath,
		config: &Config{
			Workflow:          WorkflowSettings{SaveBeforeRefresh: boolPtr(false), SaveAfterRefresh: boolPtr(false)},
			ExecutionSettings: ExecutionSettings{ResolverAddress: addr, RefreshResolverAddress: addr, QueriesPerSecond: 1000, QueryMode: "observed"},
			Recovery:          RecoverySettings{CheckpointBatchSize: 1},
		},
		status: Status{TaskState: "idle"},
	}
	task := &FullRebuildTask{TaskID: "recover-domain-progress", Mode: "full_rebuild", Stage: "tail", StageLabel: stageLabel("tail"),
		Total: 2, Completed: 1, Secondary: []domainCandidate{{Name: "remaining.example", QTypeMask: qtypeMaskA}},
	}
	if err := p.saveConfigUnlocked(); err != nil {
		t.Fatal(err)
	}
	if err := p.persistFullRebuildTask(task); err != nil {
		t.Fatal(err)
	}
	reloaded := &Requery{runtimeKey: runtimeKey, dbPath: dbPath}
	if err := reloaded.loadConfig(); err != nil {
		t.Fatal(err)
	}
	reloaded.prepareRecoveryOnStartup()
	profile := reloaded.profileForMode("full_rebuild", 0)
	profile.PostWarm = false
	reloaded.runTask(context.Background(), profile, cloneFullRebuildTask(reloaded.fullTask))
	if got := queries(); len(got) != 1 || got[0] != dns.TypeA {
		t.Fatalf("recovery should issue only the remaining domain's observed qtype, got %#v", got)
	}
	if got := progressStatus(reloaded); got.TaskState != "idle" || got.Progress.Processed != 2 || got.Progress.Total != 2 {
		t.Fatalf("existing full-rebuild progress must survive recovery: %+v", got)
	}
	if reloaded.fullTask != nil {
		t.Fatal("completed recovery must clear the checkpoint")
	}
}
