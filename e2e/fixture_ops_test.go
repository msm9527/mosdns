package e2e_test

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	coremain "github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
	"github.com/miekg/dns"
)

func (fx *serviceE2EFixture) setSwitch(t TestingT, name, value string) {
	t.Helper()
	var state serviceE2ESwitchState
	fx.putJSON(t, "/api/v1/control/switches/"+name, map[string]string{"value": value}, &state)
	if state.Value != value {
		t.Fatalf("switch %s not updated: %+v", name, state)
	}
}

func (fx *serviceE2EFixture) queryUDP(t TestingT, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return fx.mustExchange(t, "udp", fx.dnsAddr, name, qtype)
}

func (fx *serviceE2EFixture) queryTCP(t TestingT, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return fx.mustExchange(t, "tcp", fx.dnsAddr, name, qtype)
}

func (fx *serviceE2EFixture) queryGoogle(t TestingT, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return fx.mustExchange(t, "udp", fx.googleAddr, name, qtype)
}

func (fx *serviceE2EFixture) queryGoogleECS(t TestingT, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return fx.mustExchange(t, "udp", fx.googleECSAddr, name, qtype)
}

func (fx *serviceE2EFixture) queryLocal(t TestingT, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return fx.mustExchange(t, "udp", fx.localAddr, name, qtype)
}

func (fx *serviceE2EFixture) queryProbe(t TestingT, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return fx.mustExchange(t, "udp", fx.sbnodeAddr, name, qtype)
}

func (fx *serviceE2EFixture) querySingbox(t TestingT, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return fx.mustExchange(t, "udp", fx.singboxAddr, name, qtype)
}

func (fx *serviceE2EFixture) mustExchange(t TestingT, network, addr, name string, qtype uint16) *dns.Msg {
	t.Helper()
	resp, err := fx.exchange(network, addr, name, qtype)
	if err != nil {
		t.Fatalf("dns exchange %s %s %s: %v", network, addr, name, err)
	}
	return resp
}

func (fx *serviceE2EFixture) exchange(network, addr, name string, qtype uint16) (*dns.Msg, error) {
	client := &dns.Client{Net: network, Timeout: 2 * time.Second}
	req := new(dns.Msg)
	req.SetQuestion(dns.Fqdn(name), qtype)
	resp, _, err := client.Exchange(req, addr)
	return resp, err
}

func (fx *serviceE2EFixture) waitForDNS(t TestingT, network, addr, name string, qtype uint16, check func(*dns.Msg) bool) {
	t.Helper()
	if err := waitServiceE2EEventually(10*time.Second, func() bool {
		resp, err := fx.exchange(network, addr, name, qtype)
		return err == nil && check(resp)
	}, "condition not satisfied before deadline"); err != nil {
		t.Fatalf("%v", err)
	}
}

func (fx *serviceE2EFixture) cacheStats(t TestingT) map[string]coremain.CacheStatsSnapshot {
	t.Helper()
	var payload serviceE2ECacheStats
	fx.getJSON(t, "/api/v1/cache/stats", &payload)
	items := make(map[string]coremain.CacheStatsSnapshot, len(payload.Items))
	for _, item := range payload.Items {
		items[item.Tag] = item
	}
	return items
}

func (fx *serviceE2EFixture) addAdguardRuleFile(t TestingT, name, content string) string {
	t.Helper()
	path := filepath.Join(fx.configDir, "adguard", name)
	if err := writeServiceE2EFile(path, content); err != nil {
		t.Fatalf("write adguard rule file: %v", err)
	}
	return path
}

func (fx *serviceE2EFixture) addDiversionRuleFile(t TestingT, name, content string) string {
	t.Helper()
	path := filepath.Join(fx.configDir, "diversion", name)
	if err := writeServiceE2EFile(path, content); err != nil {
		t.Fatalf("write diversion rule file: %v", err)
	}
	return path
}

func (fx *serviceE2EFixture) requireDeleted(t TestingT, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be deleted, err=%v", path, err)
	}
}

func newLocalAdguardRule(id, name, path string) coremain.RuleSourceItem {
	return coremain.RuleSourceItem{
		ID:         id,
		Name:       name,
		Enabled:    true,
		MatchMode:  rulesource.MatchModeAdguardNative,
		Format:     rulesource.FormatRules,
		SourceKind: rulesource.SourceKindLocal,
		Path:       path,
	}
}

func newLocalDiversionRule(id, name, bindTo, path string) coremain.RuleSourceItem {
	return coremain.RuleSourceItem{
		ID:         id,
		Name:       name,
		BindTo:     bindTo,
		Enabled:    true,
		MatchMode:  rulesource.MatchModeDomainSet,
		Format:     rulesource.FormatList,
		SourceKind: rulesource.SourceKindLocal,
		Path:       path,
	}
}

type TestingT interface {
	Helper()
	Fatalf(string, ...any)
}

type serviceE2ERequeryStatus struct {
	PendingQueue       int    `json:"pending_queue"`
	OnDemandProcessed  int64  `json:"on_demand_processed"`
	LastOnDemandDomain string `json:"last_on_demand_domain"`
}

type serviceE2ERequeryEnqueueResponse struct {
	Status       string `json:"status"`
	Domain       string `json:"domain"`
	PendingQueue int    `json:"pending_queue"`
}

func (fx *serviceE2EFixture) enqueueRequery(t TestingT, domain string, qtypeMask uint8) serviceE2ERequeryEnqueueResponse {
	t.Helper()
	var payload serviceE2ERequeryEnqueueResponse
	fx.postJSON(t, "/api/v1/control/requery/enqueue", map[string]any{
		"domain":     domain,
		"qtype_mask": qtypeMask,
		"reason":     "e2e",
	}, &payload, http.StatusAccepted)
	return payload
}

func (fx *serviceE2EFixture) requeryStatus(t TestingT) serviceE2ERequeryStatus {
	t.Helper()
	status, err := fx.loadRequeryStatus()
	if err != nil {
		t.Fatalf("get requery status: %v", err)
	}
	return status
}

func (fx *serviceE2EFixture) waitForOnDemandProcessed(t TestingT, before int64, domain string) serviceE2ERequeryStatus {
	t.Helper()
	var last serviceE2ERequeryStatus
	if err := waitServiceE2EEventually(10*time.Second, func() bool {
		status, err := fx.loadRequeryStatus()
		if err != nil {
			return false
		}
		last = status
		return status.PendingQueue == 0 && status.OnDemandProcessed > before && status.LastOnDemandDomain == domain
	}, "requery on-demand batch not completed before deadline"); err != nil {
		t.Fatalf("%v; last_status=%+v", err, last)
	}
	return last
}

func (fx *serviceE2EFixture) cacheEntries(t TestingT, tag, query string) coremain.CacheEntriesResponse {
	t.Helper()
	entries, err := fx.loadCacheEntries(tag, query)
	if err != nil {
		t.Fatalf("get cache entries %s q=%q: %v", tag, query, err)
	}
	return entries
}

func (fx *serviceE2EFixture) waitForCacheEntryState(t TestingT, tag, query string, wantPresent bool) coremain.CacheEntriesResponse {
	t.Helper()
	var last coremain.CacheEntriesResponse
	if err := waitServiceE2EEventually(10*time.Second, func() bool {
		entries, err := fx.loadCacheEntries(tag, query)
		if err != nil {
			return false
		}
		last = entries
		return (entries.Total > 0) == wantPresent
	}, fmt.Sprintf("cache entry state not reached for %s q=%q want_present=%t", tag, query, wantPresent)); err != nil {
		t.Fatalf("%v; last_entries=%+v", err, last)
	}
	return last
}

func (fx *serviceE2EFixture) runtimeCacheEntryCount(t TestingT, tag string) int {
	t.Helper()
	controller, ok := fx.server.GetPlugin(tag).(coremain.RuntimeCacheController)
	if !ok || controller == nil {
		t.Fatalf("runtime cache controller %s not found", tag)
	}
	return controller.RuntimeCacheEntryCount()
}

func (fx *serviceE2EFixture) loadRequeryStatus() (serviceE2ERequeryStatus, error) {
	var status serviceE2ERequeryStatus
	err := fx.doJSON(http.MethodGet, "/api/v1/control/requery/status", nil, &status, http.StatusOK)
	return status, err
}

func (fx *serviceE2EFixture) loadCacheEntries(tag, query string) (coremain.CacheEntriesResponse, error) {
	var entries coremain.CacheEntriesResponse
	path := fmt.Sprintf("/api/v1/cache/%s/entries?q=%s", tag, url.QueryEscape(query))
	err := fx.doJSON(http.MethodGet, path, nil, &entries, http.StatusOK)
	return entries, err
}
