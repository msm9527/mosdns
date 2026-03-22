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
	"strings"
	"time"

	coremain "github.com/IrineSistiana/mosdns/v5/coremain"
	_ "github.com/IrineSistiana/mosdns/v5/plugin"
	"github.com/miekg/dns"
)

const serviceE2EFixtureStartAttempts = 5

type serviceE2EPorts struct {
	api            int
	dns            int
	requery        int
	clashmi        int
	google         int
	googleECS      int
	local          int
	requeryRefresh int
	sbnode         int
	singbox        int
}

type serviceE2EUpstreams struct {
	domestic   string
	foreign    string
	foreignecs string
	cnfake     string
	nocnfake   string
}

type serviceE2EFixture struct {
	savedEnv      coremain.RuntimeEnv
	baseDir       string
	configDir     string
	httpBase      string
	dnsAddr       string
	googleAddr    string
	googleECSAddr string
	localAddr     string
	sbnodeAddr    string
	singboxAddr   string
	server        *coremain.Mosdns
	client        *http.Client
	stopUpstream  func()
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
	var lastErr error
	for attempt := 0; attempt < serviceE2EFixtureStartAttempts; attempt++ {
		fx, err := startServiceE2EFixture(savedEnv)
		if err == nil {
			return fx, nil
		}
		lastErr = err
		if !isServiceE2EPortConflict(err) {
			coremain.ApplyRuntimeEnvForTesting(savedEnv)
			return nil, err
		}
		time.Sleep(50 * time.Millisecond)
	}
	coremain.ApplyRuntimeEnvForTesting(savedEnv)
	return nil, lastErr
}

func startServiceE2EFixture(savedEnv coremain.RuntimeEnv) (*serviceE2EFixture, error) {
	baseDir, err := os.MkdirTemp("", "mosdns-e2e-*")
	if err != nil {
		return nil, err
	}

	ports, err := reserveServiceE2EPorts()
	if err != nil {
		cleanupServiceE2EAttempt(savedEnv, baseDir, nil)
		return nil, err
	}
	upstreams, stopUpstream, err := startServiceE2EUpstreams()
	if err != nil {
		cleanupServiceE2EAttempt(savedEnv, baseDir, nil)
		return nil, err
	}

	configPath, err := writeServiceE2EFiles(baseDir, ports, upstreams)
	if err != nil {
		cleanupServiceE2EAttempt(savedEnv, baseDir, stopUpstream)
		return nil, err
	}
	server, err := coremain.NewServerFromConfigPath(configPath)
	if err != nil {
		cleanupServiceE2EAttempt(savedEnv, baseDir, stopUpstream)
		return nil, err
	}

	fx := &serviceE2EFixture{
		savedEnv:      savedEnv,
		baseDir:       baseDir,
		configDir:     filepath.Dir(configPath),
		httpBase:      fmt.Sprintf("http://127.0.0.1:%d", ports.api),
		dnsAddr:       fmt.Sprintf("127.0.0.1:%d", ports.dns),
		googleAddr:    fmt.Sprintf("127.0.0.1:%d", ports.google),
		googleECSAddr: fmt.Sprintf("127.0.0.1:%d", ports.googleECS),
		localAddr:     fmt.Sprintf("127.0.0.1:%d", ports.local),
		sbnodeAddr:    fmt.Sprintf("127.0.0.1:%d", ports.sbnode),
		singboxAddr:   fmt.Sprintf("127.0.0.1:%d", ports.singbox),
		server:        server,
		client:        &http.Client{Timeout: 3 * time.Second},
		stopUpstream:  stopUpstream,
	}
	if err := fx.waitReady(); err != nil {
		fx.Close()
		return nil, err
	}
	return fx, nil
}

func cleanupServiceE2EAttempt(savedEnv coremain.RuntimeEnv, baseDir string, stopUpstream func()) {
	if stopUpstream != nil {
		stopUpstream()
	}
	coremain.ApplyRuntimeEnvForTesting(savedEnv)
	if baseDir != "" {
		_ = os.RemoveAll(baseDir)
	}
}

func isServiceE2EPortConflict(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "address already in use")
}

func reserveServiceE2EPorts() (serviceE2EPorts, error) {
	ports := make([]int, 0, 10)
	for i := 0; i < 10; i++ {
		port, err := reserveServiceE2EPort()
		if err != nil {
			return serviceE2EPorts{}, err
		}
		ports = append(ports, port)
	}
	return serviceE2EPorts{
		api:            ports[0],
		dns:            ports[1],
		requery:        ports[2],
		clashmi:        ports[3],
		google:         ports[4],
		googleECS:      ports[5],
		local:          ports[6],
		requeryRefresh: ports[7],
		sbnode:         ports[8],
		singbox:        ports[9],
	}, nil
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
	if err := waitServiceE2EEventually(10*time.Second, fx.httpReady, "http api not ready"); err != nil {
		return err
	}
	return waitServiceE2EEventually(10*time.Second, fx.dnsReady, "dns listener not ready")
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
