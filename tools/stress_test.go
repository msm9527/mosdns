package tools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestBuildDomainCorpusExpandsUniquely(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "domains.txt")
	content := "1 example.com\n2 example.com\n3 cloudflare.com\n# comment\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	domains, preview, source, err := buildDomainCorpus(context.Background(), &http.Client{Timeout: time.Second}, path, nil, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 5 {
		t.Fatalf("expected 5 domains, got %d", len(domains))
	}
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		if _, ok := seen[domain]; ok {
			t.Fatalf("duplicate domain generated: %s", domain)
		}
		seen[domain] = struct{}{}
	}
	if source["unique_loaded"].(int) != 2 {
		t.Fatalf("expected 2 unique loaded domains, got %v", source["unique_loaded"])
	}
	if source["generated"].(int) != 3 {
		t.Fatalf("expected 3 generated domains, got %v", source["generated"])
	}
	if len(preview) == 0 || preview[0] != dns.Fqdn("example.com") {
		t.Fatalf("unexpected preview: %#v", preview)
	}
}

func TestBuildDomainCorpusRealOnlyDoesNotGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("+.google.com\n+.netflix.com\n"))
	}))
	defer server.Close()

	domains, _, source, err := buildDomainCorpus(context.Background(), server.Client(), "", []string{server.URL}, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 2 {
		t.Fatalf("expected 2 real domains, got %d", len(domains))
	}
	if source["generated"].(int) != 0 {
		t.Fatalf("expected no generated domains, got %#v", source)
	}
	if source["insufficient_real_domains"] != true {
		t.Fatalf("expected insufficient_real_domains=true, got %#v", source)
	}
}

func TestParseDomainLineUsesLastDomainToken(t *testing.T) {
	domain, ok := parseDomainLine("123  example.org")
	if !ok {
		t.Fatal("expected valid domain")
	}
	if domain != "example.org." {
		t.Fatalf("unexpected domain: %s", domain)
	}

	if _, ok := parseDomainLine("127.0.0.1"); ok {
		t.Fatal("ip address should not be treated as domain")
	}

	domain, ok = parseDomainLine("+.google.com")
	if !ok || domain != "google.com." {
		t.Fatalf("unexpected geosite domain parse: %q %v", domain, ok)
	}

	if _, ok := parseDomainLine("keyword:google"); ok {
		t.Fatal("keyword rule should not be treated as concrete domain")
	}

	domain, ok = parseDomainLine("ha-000001.iana.org.")
	if !ok || domain != "ha-000001.iana.org." {
		t.Fatalf("unexpected fqdn parse: %q %v", domain, ok)
	}
}

func TestSummarizeLatencies(t *testing.T) {
	summary := summarizeLatencies([]float64{4, 1, 3, 2, 10})
	if summary.P50Ms != 3 {
		t.Fatalf("expected p50=3, got %.3f", summary.P50Ms)
	}
	if summary.P90Ms != 10 {
		t.Fatalf("expected p90=10, got %.3f", summary.P90Ms)
	}
	if summary.MaxMs != 10 {
		t.Fatalf("expected max=10, got %.3f", summary.MaxMs)
	}
}

func TestBuildOtherRCodes(t *testing.T) {
	other := buildOtherRCodes(map[string]int{
		dns.RcodeToString[dns.RcodeSuccess]:       10,
		dns.RcodeToString[dns.RcodeNameError]:     2,
		dns.RcodeToString[dns.RcodeServerFailure]: 1,
		dns.RcodeToString[dns.RcodeRefused]:       3,
	})
	if len(other) != 1 || other[dns.RcodeToString[dns.RcodeRefused]] != 3 {
		t.Fatalf("unexpected other rcodes: %#v", other)
	}
}

func TestBuildPhaseBreakdown(t *testing.T) {
	breakdown := buildPhaseBreakdown(10, map[string]int{
		dns.RcodeToString[dns.RcodeSuccess]:       6,
		dns.RcodeToString[dns.RcodeNameError]:     2,
		dns.RcodeToString[dns.RcodeServerFailure]: 1,
		dns.RcodeToString[dns.RcodeRefused]:       1,
	}, 1, 2)

	if breakdown.PositiveResponses != 6 || breakdown.PositiveRate != 0.6 {
		t.Fatalf("unexpected positive breakdown: %#v", breakdown)
	}
	if breakdown.NXDomainResponses != 2 || breakdown.NXDomainRate != 0.2 {
		t.Fatalf("unexpected nxdomain breakdown: %#v", breakdown)
	}
	if breakdown.ServFailResponses != 1 || breakdown.ServFailRate != 0.1 {
		t.Fatalf("unexpected servfail breakdown: %#v", breakdown)
	}
	if breakdown.OtherRCodeResponses != 1 || breakdown.OtherRCodeRate != 0.1 {
		t.Fatalf("unexpected other rcode breakdown: %#v", breakdown)
	}
	if breakdown.TimeoutErrors != 1 || breakdown.TransportErrors != 2 {
		t.Fatalf("unexpected error breakdown: %#v", breakdown)
	}
}

func TestBuildCacheEffectSummary(t *testing.T) {
	cold := phaseResult{
		Phase:     "udp-cold",
		Latency:   latencySummary{P50Ms: 20, P95Ms: 120},
		Breakdown: phaseBreakdown{PositiveRate: 0.7, TimeoutRate: 0.1},
	}
	repeat := phaseResult{
		Phase:     "udp-cache-hit",
		Latency:   latencySummary{P50Ms: 1.2, P95Ms: 5},
		Breakdown: phaseBreakdown{PositiveRate: 0.9, TimeoutRate: 0.01},
	}

	summary := buildCacheEffectSummary(cold, repeat)
	if summary.PositiveRateDelta != 0.2 {
		t.Fatalf("unexpected positive rate delta: %#v", summary)
	}
	if summary.TimeoutRateDelta != -0.09 {
		t.Fatalf("unexpected timeout rate delta: %#v", summary)
	}
	if summary.P50DeltaMs != -18.8 || summary.P95DeltaMs != -115 {
		t.Fatalf("unexpected latency delta: %#v", summary)
	}
}

func TestRunDNSStressSmoke(t *testing.T) {
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpLn.Close()
	addr := tcpLn.Addr().String()

	udpPc, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer udpPc.Close()

	handler := dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		if len(r.Question) > 0 && r.Question[0].Qtype == dns.TypeA {
			rr, _ := dns.NewRR(r.Question[0].Name + " 60 IN A 127.0.0.1")
			m.Answer = append(m.Answer, rr)
		}
		_ = w.WriteMsg(m)
	})

	udpServer := &dns.Server{PacketConn: udpPc, Handler: handler}
	tcpServer := &dns.Server{Listener: tcpLn, Handler: handler}
	go func() { _ = udpServer.ActivateAndServe() }()
	go func() { _ = tcpServer.ActivateAndServe() }()
	defer udpServer.Shutdown()
	defer tcpServer.Shutdown()
	time.Sleep(100 * time.Millisecond)

	dir := t.TempDir()
	reportPath := filepath.Join(dir, "report.json")
	failuresPath := filepath.Join(dir, "failures.ndjson")
	opts := &dnsStressOptions{
		Server:         addr,
		ReportFile:     reportPath,
		FailuresFile:   failuresPath,
		TotalQueries:   8,
		TargetUnique:   8,
		Concurrency:    4,
		Timeout:        time.Second,
		QType:          dns.TypeA,
		QTypeName:      "A",
		TCPSample:      4,
		HotsetRatio:    0.2,
		AllowGenerated: true,
	}

	if err := runDNSStress(context.Background(), opts); err != nil {
		t.Fatal(err)
	}

	reportRaw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report dnsStressReport
	if err := json.Unmarshal(reportRaw, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Phases) != 2 {
		t.Fatalf("expected 2 phases, got %d", len(report.Phases))
	}
	if report.Phases[0].NoError != 8 {
		t.Fatalf("unexpected udp success count: %#v", report.Phases[0])
	}
	if report.Phases[1].NoError != 4 {
		t.Fatalf("unexpected tcp success count: %#v", report.Phases[1])
	}
	if report.RequestedTotalQueries != 8 || report.RequestedUnique != 8 || report.RepeatedQueries != 0 {
		t.Fatalf("unexpected report traffic summary: %#v", report)
	}
	if report.Phases[0].Breakdown.PositiveResponses != 8 || report.Phases[1].Breakdown.PositiveResponses != 4 {
		t.Fatalf("unexpected phase breakdown: %#v", report.Phases)
	}
	if report.CacheEffect != nil {
		t.Fatalf("expected nil cache effect when there is no repeat phase, got %#v", report.CacheEffect)
	}
}

func TestBuildDomainCorpusWithRemoteSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("+.google.com\n+.netflix.com\nkeyword:skipme\n"))
	}))
	defer server.Close()

	domains, _, source, err := buildDomainCorpus(context.Background(), server.Client(), "", []string{server.URL}, 3, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 3 {
		t.Fatalf("expected 3 domains, got %d", len(domains))
	}
	if domains[0] != "google.com." || domains[1] != "netflix.com." {
		t.Fatalf("unexpected remote domains: %#v", domains)
	}
	if source["remote_loaded"].(int) != 2 {
		t.Fatalf("unexpected remote_loaded: %#v", source)
	}
	if source["generated"].(int) != 1 {
		t.Fatalf("unexpected generated count: %#v", source)
	}
}

func TestBuildTrafficPlanUsesHotsetRepeats(t *testing.T) {
	domains := []string{"a.com.", "b.com.", "c.com.", "d.com.", "e.com."}
	plan := buildTrafficPlan(domains, 20, dns.TypeA, 0.4)
	if len(plan.ColdQuestions) != 5 {
		t.Fatalf("expected 5 cold questions, got %d", len(plan.ColdQuestions))
	}
	if len(plan.RepeatQuestions) != 15 {
		t.Fatalf("expected 15 repeat questions, got %d", len(plan.RepeatQuestions))
	}
	if plan.HotsetSize != 2 {
		t.Fatalf("expected hotset size 2, got %d", plan.HotsetSize)
	}
	for _, q := range plan.RepeatQuestions {
		if q.Name != "a.com." && q.Name != "b.com." {
			t.Fatalf("repeat question escaped hotset: %#v", q)
		}
	}
}
