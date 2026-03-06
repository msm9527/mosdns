package requery

import (
	"context"
	"encoding/json"
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

func TestMergeAndFilterDomainsParsesQTypeMask(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "top.txt")
	content := "0000000002 2026-03-06 example.com qmask=1 score=2 promoted=1\n"
	if err := os.WriteFile(source, []byte(content), 0644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	p := &Requery{config: &Config{DomainProcessing: DomainProcessing{SourceFiles: []SourceFile{{Alias: "top", Path: source}}}, ExecutionSettings: ExecutionSettings{DateRangeDays: 30}}}
	got, err := p.mergeAndFilterDomains(context.Background())
	if err != nil {
		t.Fatalf("mergeAndFilterDomains: %v", err)
	}
	if len(got) != 1 || got[0].Name != "example.com" || got[0].QTypeMask != qtypeMaskA {
		t.Fatalf("unexpected candidates: %#v", got)
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
	cfgFile := filepath.Join(dir, "requery.json")
	source := filepath.Join(dir, "top.txt")
	if err := os.WriteFile(source, []byte("0000000002 2026-03-06 example.com qmask=1 score=2 promoted=1\n"), 0644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	p := &Requery{
		filePath:   cfgFile,
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
	}

	p.runTask(context.Background())

	mu.Lock()
	gotHits := append([]string(nil), hits...)
	mu.Unlock()
	if len(gotHits) != 2 || gotHits[0] != "/save" || gotHits[1] != "/save" {
		t.Fatalf("unexpected url hits: %#v", gotHits)
	}
	if count := len(queries()); count != 1 {
		t.Fatalf("expected one A query via refresh resolver, got %d", count)
	}
	if p.config.Status.Progress.Total != 1 || p.config.Status.TaskState != "idle" {
		t.Fatalf("unexpected status after run: %+v", p.config.Status)
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

	job, ok := p.dequeueRefreshJob()
	if !ok {
		t.Fatal("expected a queued job")
	}
	p.processOnDemandJob(job)

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
	if p.config.Status.OnDemandProcessed != 1 || p.config.Status.PendingQueue != 0 {
		t.Fatalf("unexpected on-demand status: %+v", p.config.Status)
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
