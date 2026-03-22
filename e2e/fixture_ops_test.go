package e2e_test

import (
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
