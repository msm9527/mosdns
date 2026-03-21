package e2e_test

import (
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
	report.RunCase(t, "block and ad switches", fx.testBlockAndAdSwitches)
	report.RunCase(t, "query type and ipv6 switches", fx.testQueryTypeAndIPv6Switches)
	report.RunCase(t, "routing mode switches", fx.testRoutingModeSwitches)
	report.RunCase(t, "rule apis", fx.testRuleAPIs)
	report.RunCase(t, "cache stats and stability", fx.testCacheAndStability)
}

func (fx *serviceE2EFixture) testControlAPI(t *testing.T) {
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
}

func (fx *serviceE2EFixture) testUDPTCPDNS(t *testing.T) {
	fx.setSwitch(t, "cn_answer_mode", "realip")
	requireServiceE2EARecord(t, fx.queryUDP(t, "cn.example", dns.TypeA), "1.1.1.1")
	requireServiceE2EARecord(t, fx.queryTCP(t, "cn.example", dns.TypeA), "1.1.1.1")
}

func (fx *serviceE2EFixture) testBlockAndAdSwitches(t *testing.T) {
	fx.setSwitch(t, "block_response", "on")
	requireServiceE2ERcode(t, fx.queryUDP(t, "blocked.example", dns.TypeA), dns.RcodeNameError)
	fx.setSwitch(t, "block_response", "off")
	requireServiceE2EARecord(t, fx.queryUDP(t, "blocked.example", dns.TypeA), "8.8.8.8")
	fx.setSwitch(t, "ad_block", "on")
	requireServiceE2ERcode(t, fx.queryUDP(t, "ad.example", dns.TypeA), dns.RcodeNameError)
	fx.setSwitch(t, "ad_block", "off")
	requireServiceE2EARecord(t, fx.queryUDP(t, "ad.example", dns.TypeA), "8.8.8.8")
}

func (fx *serviceE2EFixture) testQueryTypeAndIPv6Switches(t *testing.T) {
	fx.setSwitch(t, "block_query_type", "on")
	requireServiceE2EEmptyAnswer(t, fx.queryUDP(t, "cn.example", dns.TypeSOA))
	fx.setSwitch(t, "block_query_type", "off")
	requireServiceE2ESOARecord(t, fx.queryUDP(t, "cn.example", dns.TypeSOA))
	fx.setSwitch(t, "block_ipv6", "on")
	requireServiceE2EEmptyAnswer(t, fx.queryUDP(t, "cn.example", dns.TypeAAAA))
	fx.setSwitch(t, "block_ipv6", "off")
	requireServiceE2EAAAARecord(t, fx.queryUDP(t, "cn.example", dns.TypeAAAA), "2001:db8::1")
}

func (fx *serviceE2EFixture) testRoutingModeSwitches(t *testing.T) {
	fx.setSwitch(t, "client_proxy_mode", "all")
	fx.setSwitch(t, "cn_answer_mode", "realip")
	requireServiceE2EARecord(t, fx.queryUDP(t, "cn.example", dns.TypeA), "1.1.1.1")
	fx.setSwitch(t, "cn_answer_mode", "fakeip")
	requireServiceE2EARecord(t, fx.queryUDP(t, "cn.example", dns.TypeA), "30.0.0.2")
	fx.setSwitch(t, "client_proxy_mode", "all")
	requireServiceE2EARecord(t, fx.queryUDP(t, "proxy.example", dns.TypeA), "28.0.0.2")
	fx.setSwitch(t, "client_proxy_mode", "whitelist")
	requireServiceE2EARecord(t, fx.queryUDP(t, "proxy.example", dns.TypeA), "1.1.1.1")
	fx.setSwitch(t, "client_proxy_mode", "all")
}

func (fx *serviceE2EFixture) testRuleAPIs(t *testing.T) {
	fx.testAdguardRuleAPI(t)
	fx.testDiversionRuleAPI(t)
}

func (fx *serviceE2EFixture) testAdguardRuleAPI(t *testing.T) {
	fx.setSwitch(t, "ad_block", "on")
	filePath := fx.addAdguardRuleFile(t, "adhoc.rules", "||newad.example^\n")
	body := newLocalAdguardRule("adhoc-ad", "Adhoc Ad", "adguard/adhoc.rules")
	var created coremain.RuleSourceItem
	fx.postJSON(t, "/api/v1/rules/adguard", body, &created, http.StatusCreated)
	fx.waitForDNS(t, "udp", fx.dnsAddr, "newad.example", dns.TypeA, func(resp *dns.Msg) bool {
		return resp.Rcode == dns.RcodeNameError
	})

	var deleted coremain.RuleSourceDeleteResponse
	fx.deleteJSON(t, "/api/v1/rules/adguard/adhoc-ad", &deleted)
	fx.requireDeleted(t, filePath)
	fx.waitForDNS(t, "udp", fx.dnsAddr, "newad.example", dns.TypeA, func(resp *dns.Msg) bool {
		return resp.Rcode == dns.RcodeSuccess && len(resp.Answer) == 1
	})
}

func (fx *serviceE2EFixture) testDiversionRuleAPI(t *testing.T) {
	fx.setSwitch(t, "cn_answer_mode", "realip")
	filePath := fx.addDiversionRuleFile(t, "extra-cn.list", "full:newcn.example\n")
	body := newLocalDiversionRule("extra-cn", "Extra CN", "geosite_cn", "diversion/extra-cn.list")
	var created coremain.RuleSourceItem
	fx.postJSON(t, "/api/v1/rules/diversion", body, &created, http.StatusCreated)
	fx.waitForDNS(t, "udp", fx.dnsAddr, "newcn.example", dns.TypeA, func(resp *dns.Msg) bool {
		return resp.Rcode == dns.RcodeSuccess && len(resp.Answer) == 1 && resp.Answer[0].(*dns.A).A.String() == "1.1.1.1"
	})

	var deleted coremain.RuleSourceDeleteResponse
	fx.deleteJSON(t, "/api/v1/rules/diversion/extra-cn", &deleted)
	fx.requireDeleted(t, filePath)
	fx.waitForDNS(t, "udp", fx.dnsAddr, "newcn.example", dns.TypeA, func(resp *dns.Msg) bool {
		return resp.Rcode == dns.RcodeSuccess && len(resp.Answer) == 1 && resp.Answer[0].(*dns.A).A.String() == "8.8.8.8"
	})
}

func (fx *serviceE2EFixture) testCacheAndStability(t *testing.T) {
	fx.setSwitch(t, "cn_answer_mode", "realip")
	fx.setSwitch(t, "client_proxy_mode", "all")
	fx.setSwitch(t, "main_cache", "on")
	fx.setSwitch(t, "branch_cache", "on")
	requireServiceE2EARecord(t, fx.queryUDP(t, "default.example", dns.TypeA), "8.8.8.8")
	requireServiceE2EARecord(t, fx.queryUDP(t, "default.example", dns.TypeA), "8.8.8.8")
	fx.setSwitch(t, "main_cache", "off")
	requireServiceE2EARecord(t, fx.queryUDP(t, "branch.example", dns.TypeA), "8.8.8.8")
	requireServiceE2EARecord(t, fx.queryUDP(t, "branch.example", dns.TypeA), "8.8.8.8")
	fx.setSwitch(t, "main_cache", "on")
	fx.setSwitch(t, "cn_answer_mode", "fakeip")
	requireServiceE2EARecord(t, fx.queryUDP(t, "cn.example", dns.TypeA), "30.0.0.2")
	requireServiceE2EARecord(t, fx.queryUDP(t, "cn.example", dns.TypeA), "30.0.0.2")
	requireServiceE2EARecord(t, fx.queryUDP(t, "proxy.example", dns.TypeA), "28.0.0.2")
	requireServiceE2EARecord(t, fx.queryUDP(t, "proxy.example", dns.TypeA), "28.0.0.2")
	requireServiceE2EARecord(t, fx.queryProbe(t, "probe.example", dns.TypeA), "8.8.8.8")
	requireServiceE2EARecord(t, fx.queryProbe(t, "probe.example", dns.TypeA), "8.8.8.8")

	stats := fx.cacheStats(t)
	requireServiceE2ECacheHits(t, stats["cache_main"])
	requireServiceE2ECacheHits(t, stats["cache_branch_foreign"])
	requireServiceE2ECacheHits(t, stats["cache_fakeip_domestic"])
	requireServiceE2ECacheHits(t, stats["cache_fakeip_proxy"])
	requireServiceE2ECacheHits(t, stats["cache_probe"])
	fx.runConcurrentQueries(t)
}

func requireServiceE2ECacheHits(t *testing.T, item coremain.CacheStatsSnapshot) {
	t.Helper()
	if item.Counters["query_total"] < 2 || item.Counters["hit_total"] < 1 {
		t.Fatalf("unexpected cache counters for %s: %+v", item.Tag, item.Counters)
	}
}

func (fx *serviceE2EFixture) runConcurrentQueries(t *testing.T) {
	t.Helper()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := fx.exchange("udp", fx.dnsAddr, "cn.example", dns.TypeA)
			if err != nil || resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 1 {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent query failed: %v", err)
	}
}
