package e2e_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	coremain "github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/miekg/dns"
)

func TestServiceE2E(t *testing.T) {
	report := newE2EReport()
	startedAt := time.Now()
	defer func() {
		report.Finalize(time.Since(startedAt), t.Failed())
		if err := report.Write(); err != nil {
			t.Logf("write e2e report: %v", err)
		}
	}()

	fx, err := newServiceE2EFixture()
	if err != nil {
		report.AddSetupFailure(err)
		t.Fatalf("setup e2e fixture: %v", err)
	}
	defer fx.Close()

	report.RunCase(t, "control api", fx.testControlAPI)
	report.RunCase(t, "udp and tcp dns", fx.testUDPTCPDNS)
	report.RunCase(t, "specialized listeners", fx.testSpecializedListeners)
	report.RunCase(t, "block and ad switches", fx.testBlockAndAdSwitches)
	report.RunCase(t, "query type and ipv6 switches", fx.testQueryTypeAndIPv6Switches)
	report.RunCase(t, "routing mode switches", fx.testRoutingModeSwitches)
	report.RunCase(t, "rule apis", fx.testRuleAPIs)
	report.RunCase(t, "cache stats and stability", fx.testCacheAndStability)
}

func (fx *serviceE2EFixture) testControlAPI(t *testing.T, rec *e2eCaseRecorder) {
	var health serviceE2EHealth
	fx.getJSON(t, "/api/v1/control/health", &health)
	if health.StorageEngine != "sqlite" || len(health.Checks) == 0 {
		t.Fatalf("unexpected health payload: %+v", health)
	}

	var switches []serviceE2ESwitchState
	fx.getJSON(t, "/api/v1/control/switches/", &switches)
	if len(switches) == 0 {
		t.Fatal("expected switches list to be non-empty")
	}
	rec.SetDetail("control health endpoint and switch inventory both responded")
	rec.AddCheck("health", fmt.Sprintf("storage_engine=%s checks=%d", health.StorageEngine, len(health.Checks)))
	rec.AddMetric("switch_count", fmt.Sprintf("%d", len(switches)), "registered control switches")
}

func (fx *serviceE2EFixture) testUDPTCPDNS(t *testing.T, rec *e2eCaseRecorder) {
	fx.setSwitch(t, "cn_answer_mode", "realip")
	udpResp := fx.queryUDP(t, "cn.example", dns.TypeA)
	tcpResp := fx.queryTCP(t, "cn.example", dns.TypeA)
	requireServiceE2EARecord(t, udpResp, "1.1.1.1")
	requireServiceE2EARecord(t, tcpResp, "1.1.1.1")
	rec.SetDetail("main listener returned expected domestic realip over UDP and TCP")
	recordDNSCheck(rec, "udp main cn.example", "udp", fx.dnsAddr, "cn.example", dns.TypeA, udpResp)
	recordDNSCheck(rec, "tcp main cn.example", "tcp", fx.dnsAddr, "cn.example", dns.TypeA, tcpResp)
}

func (fx *serviceE2EFixture) testSpecializedListeners(t *testing.T, rec *e2eCaseRecorder) {
	googleResp := fx.queryGoogle(t, "google-listener.example", dns.TypeA)
	googleECSResp := fx.queryGoogleECS(t, "google-ecs-listener.example", dns.TypeA)
	localResp := fx.queryLocal(t, "local-listener.example", dns.TypeA)
	probeResp := fx.queryProbe(t, "probe-listener.example", dns.TypeA)
	singboxResp := fx.querySingbox(t, "singbox-listener.example", dns.TypeA)
	requireServiceE2EARecord(t, googleResp, "8.8.8.8")
	requireServiceE2EARecord(t, googleECSResp, "8.8.8.8")
	requireServiceE2EARecord(t, localResp, "1.1.1.1")
	requireServiceE2EARecord(t, probeResp, "8.8.8.8")
	requireServiceE2EARecord(t, singboxResp, "1.1.1.1")
	rec.SetDetail("dedicated listeners routed to their expected upstream chains")
	recordDNSCheck(rec, "google listener", "udp", fx.googleAddr, "google-listener.example", dns.TypeA, googleResp)
	recordDNSCheck(rec, "google ecs listener", "udp", fx.googleECSAddr, "google-ecs-listener.example", dns.TypeA, googleECSResp)
	recordDNSCheck(rec, "local listener", "udp", fx.localAddr, "local-listener.example", dns.TypeA, localResp)
	recordDNSCheck(rec, "probe listener", "udp", fx.sbnodeAddr, "probe-listener.example", dns.TypeA, probeResp)
	recordDNSCheck(rec, "singbox listener", "udp", fx.singboxAddr, "singbox-listener.example", dns.TypeA, singboxResp)
}

func (fx *serviceE2EFixture) testBlockAndAdSwitches(t *testing.T, rec *e2eCaseRecorder) {
	fx.setSwitch(t, "block_response", "on")
	blockOnResp := fx.queryUDP(t, "blocked.example", dns.TypeA)
	requireServiceE2ERcode(t, blockOnResp, dns.RcodeNameError)
	fx.setSwitch(t, "block_response", "off")
	blockOffResp := fx.queryUDP(t, "blocked.example", dns.TypeA)
	requireServiceE2EAnyARecord(t, blockOffResp)
	fx.setSwitch(t, "ad_block", "on")
	adOnResp := fx.queryUDP(t, "ad.example", dns.TypeA)
	requireServiceE2ERcode(t, adOnResp, dns.RcodeNameError)
	fx.setSwitch(t, "ad_block", "off")
	adOffResp := fx.queryUDP(t, "ad.example", dns.TypeA)
	requireServiceE2EAnyARecord(t, adOffResp)
	rec.SetDetail("block_response and ad_block switches toggled live DNS behavior")
	recordDNSCheck(rec, "block_response on", "udp", fx.dnsAddr, "blocked.example", dns.TypeA, blockOnResp)
	recordDNSCheck(rec, "block_response off", "udp", fx.dnsAddr, "blocked.example", dns.TypeA, blockOffResp)
	recordDNSCheck(rec, "ad_block on", "udp", fx.dnsAddr, "ad.example", dns.TypeA, adOnResp)
	recordDNSCheck(rec, "ad_block off", "udp", fx.dnsAddr, "ad.example", dns.TypeA, adOffResp)
}

func (fx *serviceE2EFixture) testQueryTypeAndIPv6Switches(t *testing.T, rec *e2eCaseRecorder) {
	fx.setSwitch(t, "block_query_type", "on")
	soaBlocked := fx.queryUDP(t, "cn.example", dns.TypeSOA)
	requireServiceE2EEmptyAnswer(t, soaBlocked)
	fx.setSwitch(t, "block_query_type", "off")
	soaAllowed := fx.queryUDP(t, "cn.example", dns.TypeSOA)
	requireServiceE2ESOARecord(t, soaAllowed)
	fx.setSwitch(t, "block_ipv6", "on")
	aaaaBlocked := fx.queryUDP(t, "cn.example", dns.TypeAAAA)
	requireServiceE2EEmptyAnswer(t, aaaaBlocked)
	fx.setSwitch(t, "block_ipv6", "off")
	aaaaAllowed := fx.queryUDP(t, "cn.example", dns.TypeAAAA)
	requireServiceE2EAAAARecord(t, aaaaAllowed, "2001:db8::1")
	rec.SetDetail("special qtype and IPv6 switches correctly changed reply behavior")
	recordDNSCheck(rec, "SOA blocked", "udp", fx.dnsAddr, "cn.example", dns.TypeSOA, soaBlocked)
	recordDNSCheck(rec, "SOA allowed", "udp", fx.dnsAddr, "cn.example", dns.TypeSOA, soaAllowed)
	recordDNSCheck(rec, "AAAA blocked", "udp", fx.dnsAddr, "cn.example", dns.TypeAAAA, aaaaBlocked)
	recordDNSCheck(rec, "AAAA allowed", "udp", fx.dnsAddr, "cn.example", dns.TypeAAAA, aaaaAllowed)
}

func (fx *serviceE2EFixture) testRoutingModeSwitches(t *testing.T, rec *e2eCaseRecorder) {
	fx.setSwitch(t, "client_proxy_mode", "all")
	fx.setSwitch(t, "cn_answer_mode", "realip")
	cnRealipResp := fx.queryUDP(t, "cn.example", dns.TypeA)
	requireServiceE2EARecord(t, cnRealipResp, "1.1.1.1")
	fx.setSwitch(t, "cn_answer_mode", "fakeip")
	cnFakeResp := fx.queryUDP(t, "cn.example", dns.TypeA)
	requireServiceE2EARecord(t, cnFakeResp, "30.0.0.2")
	fx.setSwitch(t, "client_proxy_mode", "all")
	proxyAllResp := fx.queryUDP(t, "proxy.example", dns.TypeA)
	requireServiceE2EARecord(t, proxyAllResp, "28.0.0.2")
	fx.setSwitch(t, "client_proxy_mode", "whitelist")
	proxyWhitelistResp := fx.queryUDP(t, "proxy.example", dns.TypeA)
	requireServiceE2EARecord(t, proxyWhitelistResp, "1.1.1.1")
	fx.setSwitch(t, "client_proxy_mode", "all")
	rec.SetDetail("routing switches changed domestic realip/fakeip and proxy path selection")
	recordDNSCheck(rec, "cn realip", "udp", fx.dnsAddr, "cn.example", dns.TypeA, cnRealipResp)
	recordDNSCheck(rec, "cn fakeip", "udp", fx.dnsAddr, "cn.example", dns.TypeA, cnFakeResp)
	recordDNSCheck(rec, "proxy all", "udp", fx.dnsAddr, "proxy.example", dns.TypeA, proxyAllResp)
	recordDNSCheck(rec, "proxy whitelist", "udp", fx.dnsAddr, "proxy.example", dns.TypeA, proxyWhitelistResp)
}

func (fx *serviceE2EFixture) testRuleAPIs(t *testing.T, rec *e2eCaseRecorder) {
	fx.testAdguardRuleAPI(t, rec)
	fx.testDiversionRuleAPI(t, rec)
	rec.SetDetail("rule source create/delete APIs updated live DNS behavior and cleaned managed files")
}

func (fx *serviceE2EFixture) testAdguardRuleAPI(t *testing.T, rec *e2eCaseRecorder) {
	fx.setSwitch(t, "ad_block", "on")
	filePath := fx.addAdguardRuleFile(t, "adhoc.rules", "||newad.example^\n")
	body := newLocalAdguardRule("adhoc-ad", "Adhoc Ad", "adguard/adhoc.rules")
	var created coremain.RuleSourceItem
	fx.postJSON(t, "/api/v1/rules/adguard", body, &created, http.StatusCreated)
	fx.waitForDNS(t, "udp", fx.dnsAddr, "newad.example", dns.TypeA, func(resp *dns.Msg) bool {
		return resp.Rcode == dns.RcodeNameError
	})
	adBlockedResp := fx.queryUDP(t, "newad.example", dns.TypeA)

	var deleted coremain.RuleSourceDeleteResponse
	fx.deleteJSON(t, "/api/v1/rules/adguard/adhoc-ad", &deleted)
	fx.requireDeleted(t, filePath)
	fx.waitForDNS(t, "udp", fx.dnsAddr, "newad.example", dns.TypeA, func(resp *dns.Msg) bool {
		return resp.Rcode == dns.RcodeSuccess && len(resp.Answer) == 1
	})
	adReleasedResp := fx.queryUDP(t, "newad.example", dns.TypeA)
	rec.AddCheck("POST /api/v1/rules/adguard", fmt.Sprintf("created id=%s path=%s", created.ID, created.Path))
	recordDNSCheck(rec, "adguard source active", "udp", fx.dnsAddr, "newad.example", dns.TypeA, adBlockedResp)
	rec.AddCheck("DELETE /api/v1/rules/adguard/adhoc-ad", fmt.Sprintf("file_cleanup=%s deleted=%t", deleted.FileCleanup.Status, deleted.FileCleanup.Deleted))
	recordDNSCheck(rec, "adguard source removed", "udp", fx.dnsAddr, "newad.example", dns.TypeA, adReleasedResp)
}

func (fx *serviceE2EFixture) testDiversionRuleAPI(t *testing.T, rec *e2eCaseRecorder) {
	fx.setSwitch(t, "main_cache", "off")
	fx.setSwitch(t, "branch_cache", "off")
	fx.setSwitch(t, "cn_answer_mode", "realip")
	filePath := fx.addDiversionRuleFile(t, "extra-cn.list", "domain:e2e-cn.example\n")
	body := newLocalDiversionRule("extra-cn", "Extra CN", "geosite_cn", "diversion/extra-cn.list")
	var created coremain.RuleSourceItem
	fx.postJSON(t, "/api/v1/rules/diversion", body, &created, http.StatusCreated)
	fx.waitForDNS(t, "udp", fx.dnsAddr, "create.e2e-cn.example", dns.TypeA, func(resp *dns.Msg) bool {
		return resp.Rcode == dns.RcodeSuccess && len(resp.Answer) == 1 && resp.Answer[0].(*dns.A).A.String() == "1.1.1.1"
	})
	diversionCreatedResp := fx.queryUDP(t, "create.e2e-cn.example", dns.TypeA)

	var deleted coremain.RuleSourceDeleteResponse
	fx.deleteJSON(t, "/api/v1/rules/diversion/extra-cn", &deleted)
	fx.requireDeleted(t, filePath)
	fx.waitForDNS(t, "udp", fx.dnsAddr, "delete.e2e-cn.example", dns.TypeA, func(resp *dns.Msg) bool {
		if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
			return false
		}
		a, ok := resp.Answer[0].(*dns.A)
		return ok && a.A.String() != "1.1.1.1"
	})
	diversionDeletedResp := fx.queryUDP(t, "delete.e2e-cn.example", dns.TypeA)
	rec.AddCheck("POST /api/v1/rules/diversion", fmt.Sprintf("created id=%s bind_to=%s path=%s", created.ID, created.BindTo, created.Path))
	recordDNSCheck(rec, "diversion source active", "udp", fx.dnsAddr, "create.e2e-cn.example", dns.TypeA, diversionCreatedResp)
	rec.AddCheck("DELETE /api/v1/rules/diversion/extra-cn", fmt.Sprintf("file_cleanup=%s deleted=%t", deleted.FileCleanup.Status, deleted.FileCleanup.Deleted))
	recordDNSCheck(rec, "diversion source removed", "udp", fx.dnsAddr, "delete.e2e-cn.example", dns.TypeA, diversionDeletedResp)
}

func (fx *serviceE2EFixture) testCacheAndStability(t *testing.T, rec *e2eCaseRecorder) {
	fx.setSwitch(t, "cn_answer_mode", "realip")
	fx.setSwitch(t, "client_proxy_mode", "all")
	fx.setSwitch(t, "main_cache", "on")
	fx.setSwitch(t, "branch_cache", "on")
	defaultFirst := fx.queryUDP(t, "default.example", dns.TypeA)
	defaultSecond := fx.queryUDP(t, "default.example", dns.TypeA)
	requireServiceE2EARecord(t, defaultFirst, "8.8.8.8")
	requireServiceE2EARecord(t, defaultSecond, "8.8.8.8")
	fx.setSwitch(t, "main_cache", "off")
	branchFirst := fx.queryUDP(t, "branch.example", dns.TypeA)
	branchSecond := fx.queryUDP(t, "branch.example", dns.TypeA)
	requireServiceE2EARecord(t, branchFirst, "8.8.8.8")
	requireServiceE2EARecord(t, branchSecond, "8.8.8.8")
	fx.setSwitch(t, "main_cache", "on")
	fx.setSwitch(t, "cn_answer_mode", "fakeip")
	cnFakeFirst := fx.queryUDP(t, "cn.example", dns.TypeA)
	cnFakeSecond := fx.queryUDP(t, "cn.example", dns.TypeA)
	requireServiceE2EARecord(t, cnFakeFirst, "30.0.0.2")
	requireServiceE2EARecord(t, cnFakeSecond, "30.0.0.2")
	proxyFirst := fx.queryUDP(t, "proxy.example", dns.TypeA)
	proxyAnswer := requireServiceE2EAnyARecord(t, proxyFirst).A.String()
	proxySecond := fx.queryUDP(t, "proxy.example", dns.TypeA)
	requireServiceE2EARecord(t, proxySecond, proxyAnswer)
	probeFirst := fx.queryProbe(t, "probe.example", dns.TypeA)
	probeSecond := fx.queryProbe(t, "probe.example", dns.TypeA)
	requireServiceE2EARecord(t, probeFirst, "8.8.8.8")
	requireServiceE2EARecord(t, probeSecond, "8.8.8.8")

	stats := fx.cacheStats(t)
	requireServiceE2ECacheHits(t, stats["cache_main"])
	requireServiceE2ECacheHits(t, stats["cache_branch_foreign"])
	requireServiceE2ECacheHits(t, stats["cache_fakeip_domestic"])
	requireServiceE2ECacheHits(t, stats["cache_fakeip_proxy"])
	requireServiceE2ECacheHits(t, stats["cache_probe"])
	loadStats := fx.runConcurrentQueries(t)
	rec.SetDetail("cache hit paths remained stable and UDP concurrency test completed")
	recordDNSCheck(rec, "default first", "udp", fx.dnsAddr, "default.example", dns.TypeA, defaultFirst)
	recordDNSCheck(rec, "default second", "udp", fx.dnsAddr, "default.example", dns.TypeA, defaultSecond)
	recordDNSCheck(rec, "branch first", "udp", fx.dnsAddr, "branch.example", dns.TypeA, branchFirst)
	recordDNSCheck(rec, "branch second", "udp", fx.dnsAddr, "branch.example", dns.TypeA, branchSecond)
	recordDNSCheck(rec, "cn fakeip first", "udp", fx.dnsAddr, "cn.example", dns.TypeA, cnFakeFirst)
	recordDNSCheck(rec, "cn fakeip second", "udp", fx.dnsAddr, "cn.example", dns.TypeA, cnFakeSecond)
	recordDNSCheck(rec, "proxy first", "udp", fx.dnsAddr, "proxy.example", dns.TypeA, proxyFirst)
	recordDNSCheck(rec, "proxy second", "udp", fx.dnsAddr, "proxy.example", dns.TypeA, proxySecond)
	recordDNSCheck(rec, "probe first", "udp", fx.sbnodeAddr, "probe.example", dns.TypeA, probeFirst)
	recordDNSCheck(rec, "probe second", "udp", fx.sbnodeAddr, "probe.example", dns.TypeA, probeSecond)
	recordCacheMetric(rec, stats["cache_main"])
	recordCacheMetric(rec, stats["cache_branch_foreign"])
	recordCacheMetric(rec, stats["cache_fakeip_domestic"])
	recordCacheMetric(rec, stats["cache_fakeip_proxy"])
	recordCacheMetric(rec, stats["cache_probe"])
	rec.AddMetric("load total", fmt.Sprintf("%d", loadStats.Total), "workers x iterations")
	rec.AddMetric("load success", fmt.Sprintf("%d", loadStats.Successes), "successful UDP answers")
	rec.AddMetric("load failure", fmt.Sprintf("%d", loadStats.Failures), "transport or response validation failures")
	rec.AddMetric("load duration", loadStats.Duration.Round(time.Millisecond).String(), "wall clock")
	rec.AddMetric("load avg latency", loadStats.AvgLatency.Round(time.Microsecond).String(), "mean per request")
	rec.AddMetric("load p95 latency", loadStats.P95Latency.Round(time.Microsecond).String(), "95th percentile")
	rec.AddMetric("load max latency", loadStats.MaxLatency.Round(time.Microsecond).String(), "slowest request")
	rec.AddMetric("load qps", fmt.Sprintf("%.2f", loadStats.QueriesPerS), "successful queries per second")
}

func requireServiceE2ECacheHits(t *testing.T, item coremain.CacheStatsSnapshot) {
	t.Helper()
	if item.Counters["query_total"] < 2 || item.Counters["hit_total"] < 1 {
		t.Fatalf("unexpected cache counters for %s: %+v", item.Tag, item.Counters)
	}
}

func (fx *serviceE2EFixture) runConcurrentQueries(t *testing.T) serviceE2ELoadStats {
	t.Helper()
	var wg sync.WaitGroup
	startedAt := time.Now()
	errs := make(chan error, serviceE2ELoadWorkers*serviceE2ELoadIterations)
	latencies := make(chan time.Duration, serviceE2ELoadWorkers*serviceE2ELoadIterations)
	for i := 0; i < serviceE2ELoadWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < serviceE2ELoadIterations; j++ {
				queryStarted := time.Now()
				resp, err := fx.exchange("udp", fx.dnsAddr, "cn.example", dns.TypeA)
				if err != nil || resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
					errs <- err
					continue
				}
				latencies <- time.Since(queryStarted)
			}
		}()
	}
	wg.Wait()
	close(errs)
	close(latencies)
	failures := len(errs)
	for err := range errs {
		t.Fatalf("concurrent query failed: %v", err)
	}
	values := make([]time.Duration, 0, len(latencies))
	for latency := range latencies {
		values = append(values, latency)
	}
	return summarizeServiceE2ELoadStats(
		serviceE2ELoadWorkers,
		serviceE2ELoadIterations,
		values,
		failures,
		time.Since(startedAt),
	)
}
