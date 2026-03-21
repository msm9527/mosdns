package e2e_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	coremain "github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
	_ "github.com/IrineSistiana/mosdns/v5/plugin"
	"github.com/miekg/dns"
)

type serviceE2EPorts struct {
	api   int
	dns   int
	probe int
}

type serviceE2EUpstreams struct {
	domestic string
	foreign  string
	cnfake   string
	nocnfake string
}

type serviceE2EFixture struct {
	savedEnv     coremain.RuntimeEnv
	baseDir      string
	httpBase     string
	dnsAddr      string
	probeAddr    string
	server       *coremain.Mosdns
	client       *http.Client
	stopUpstream func()
}

type serviceE2ESwitchState struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type serviceE2EHealth struct {
	StorageEngine string           `json:"storage_engine"`
	Checks        []map[string]any `json:"checks"`
}

type serviceE2ECacheStats struct {
	Items []coremain.CacheStatsSnapshot `json:"items"`
}

func newServiceE2EFixture() (*serviceE2EFixture, error) {
	savedEnv := coremain.SnapshotRuntimeEnvForTesting()
	baseDir, err := os.MkdirTemp("", "mosdns-e2e-*")
	if err != nil {
		return nil, err
	}

	ports, err := reserveServiceE2EPorts()
	if err != nil {
		_ = os.RemoveAll(baseDir)
		return nil, err
	}
	upstreams, stopUpstream, err := startServiceE2EUpstreams()
	if err != nil {
		_ = os.RemoveAll(baseDir)
		return nil, err
	}

	configPath, err := writeServiceE2EFiles(baseDir, ports, upstreams)
	if err != nil {
		stopUpstream()
		_ = os.RemoveAll(baseDir)
		return nil, err
	}
	server, err := coremain.NewServerFromConfigPath(configPath)
	if err != nil {
		stopUpstream()
		coremain.ApplyRuntimeEnvForTesting(savedEnv)
		_ = os.RemoveAll(baseDir)
		return nil, err
	}

	fx := &serviceE2EFixture{
		savedEnv:     savedEnv,
		baseDir:      baseDir,
		httpBase:     fmt.Sprintf("http://127.0.0.1:%d", ports.api),
		dnsAddr:      fmt.Sprintf("127.0.0.1:%d", ports.dns),
		probeAddr:    fmt.Sprintf("127.0.0.1:%d", ports.probe),
		server:       server,
		client:       &http.Client{Timeout: 3 * time.Second},
		stopUpstream: stopUpstream,
	}
	if err := fx.waitReady(); err != nil {
		fx.Close()
		return nil, err
	}
	return fx, nil
}

func reserveServiceE2EPorts() (serviceE2EPorts, error) {
	api, err := reserveServiceE2EPort()
	if err != nil {
		return serviceE2EPorts{}, err
	}
	dnsPort, err := reserveServiceE2EPort()
	if err != nil {
		return serviceE2EPorts{}, err
	}
	probe, err := reserveServiceE2EPort()
	if err != nil {
		return serviceE2EPorts{}, err
	}
	return serviceE2EPorts{api: api, dns: dnsPort, probe: probe}, nil
}

func reserveServiceE2EPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (fx *serviceE2EFixture) Close() {
	if fx.server != nil {
		fx.server.CloseWithErr(nil)
		_ = fx.server.GetSafeClose().WaitClosed()
	}
	if fx.stopUpstream != nil {
		fx.stopUpstream()
	}
	coremain.ApplyRuntimeEnvForTesting(fx.savedEnv)
	if fx.baseDir != "" {
		_ = os.RemoveAll(fx.baseDir)
	}
}

func (fx *serviceE2EFixture) waitReady() error {
	if err := waitServiceE2EEventually(5*time.Second, fx.httpReady, "http api not ready"); err != nil {
		return err
	}
	return waitServiceE2EEventually(5*time.Second, fx.dnsReady, "dns listener not ready")
}

func (fx *serviceE2EFixture) httpReady() bool {
	resp, err := fx.client.Get(fx.httpBase + "/api/v1/control/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (fx *serviceE2EFixture) dnsReady() bool {
	resp, err := fx.exchange("udp", fx.dnsAddr, "cn.example", dns.TypeA)
	return err == nil && resp.Rcode == dns.RcodeSuccess && len(resp.Answer) == 1
}

func (fx *serviceE2EFixture) doJSON(method, path string, body any, dst any, want int) error {
	req, err := newServiceE2ERequest(method, fx.httpBase+path, body)
	if err != nil {
		return err
	}
	resp, err := fx.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read %s %s body: %w", method, path, err)
	}
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s returned %d want %d body=%s", method, path, resp.StatusCode, want, string(raw))
	}
	if dst != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, dst); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

func newServiceE2ERequest(method, url string, body any) (*http.Request, error) {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (fx *serviceE2EFixture) getJSON(t TestingT, path string, dst any) {
	t.Helper()
	if err := fx.doJSON(http.MethodGet, path, nil, dst, http.StatusOK); err != nil {
		t.Fatalf("%v", err)
	}
}

func (fx *serviceE2EFixture) postJSON(t TestingT, path string, body any, dst any, status int) {
	t.Helper()
	if err := fx.doJSON(http.MethodPost, path, body, dst, status); err != nil {
		t.Fatalf("%v", err)
	}
}

func (fx *serviceE2EFixture) putJSON(t TestingT, path string, body any, dst any) {
	t.Helper()
	if err := fx.doJSON(http.MethodPut, path, body, dst, http.StatusOK); err != nil {
		t.Fatalf("%v", err)
	}
}

func (fx *serviceE2EFixture) deleteJSON(t TestingT, path string, dst any) {
	t.Helper()
	if err := fx.doJSON(http.MethodDelete, path, nil, dst, http.StatusOK); err != nil {
		t.Fatalf("%v", err)
	}
}

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

func (fx *serviceE2EFixture) queryProbe(t TestingT, name string, qtype uint16) *dns.Msg {
	t.Helper()
	return fx.mustExchange(t, "udp", fx.probeAddr, name, qtype)
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
	if err := waitServiceE2EEventually(5*time.Second, func() bool {
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
	path := filepath.Join(fx.baseDir, "adguard", name)
	if err := writeServiceE2EFile(path, content); err != nil {
		t.Fatalf("write adguard rule file: %v", err)
	}
	return path
}

func (fx *serviceE2EFixture) addDiversionRuleFile(t TestingT, name, content string) string {
	t.Helper()
	path := filepath.Join(fx.baseDir, "diversion", name)
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
