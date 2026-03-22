package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnrichE2EReportViewBuildsStructuredSections(t *testing.T) {
	view := e2eReportView{
		Total:  1,
		Passed: 1,
		Cases: []e2eCaseReport{{
			Name:     "rule apis",
			Status:   "passed",
			Detail:   "ok",
			Duration: "2s",
			Checks: []e2eCheckReport{
				{Name: "POST /api/v1/rules/adguard", Detail: "created"},
				{Name: "adguard source active", Detail: "UDP 127.0.0.1:5353 ad.example A -> rcode=NXDOMAIN answers=[]"},
			},
			Metrics: []e2eMetricReport{
				{Name: "cache_main", Value: "hit=1/2", Detail: "entries=1 misses=1"},
				{Name: "load qps", Value: "123.45", Detail: "successful queries per second"},
			},
		}},
	}

	got := enrichE2EReportView(view)
	if got.SuccessRate != "100.0%" {
		t.Fatalf("unexpected success rate: %s", got.SuccessRate)
	}
	if got.Coverage.DNSChecks != 1 || got.Coverage.APIChecks != 1 {
		t.Fatalf("unexpected coverage: %+v", got.Coverage)
	}
	if len(got.Performance.LoadMetrics) != 1 || len(got.Performance.CacheMetrics) != 1 {
		t.Fatalf("unexpected performance split: %+v", got.Performance)
	}
	if len(got.Suites) != 1 || got.Suites[0].Category != "Rule Management" {
		t.Fatalf("unexpected suites: %+v", got.Suites)
	}
	if len(got.DNSChecks) != 1 || got.DNSChecks[0].Rcode != "NXDOMAIN" {
		t.Fatalf("unexpected dns checks: %+v", got.DNSChecks)
	}
	if len(got.APIChecks) != 1 || got.APIChecks[0].Path != "/api/v1/rules/adguard" {
		t.Fatalf("unexpected api checks: %+v", got.APIChecks)
	}
}

func TestE2EReportWriteRendersStructuredOutputs(t *testing.T) {
	report := &e2eReport{
		outputDir: filepath.Join(t.TempDir(), "report"),
		startedAt: time.Now().Add(-3 * time.Second),
		status:    "passed",
		cases: []e2eCaseReport{{
			Name:     "cache stats and stability",
			Status:   "passed",
			Detail:   "stable",
			Duration: "3s",
			Checks: []e2eCheckReport{
				{Name: "default first", Detail: "UDP 127.0.0.1:5353 cn.example A -> rcode=NOERROR answers=[cn.example.\t60\tIN\tA\t1.1.1.1]"},
			},
			Metrics: []e2eMetricReport{
				{Name: "cache_main", Value: "hit=2/3", Detail: "entries=1 misses=1"},
				{Name: "load qps", Value: "999.00", Detail: "successful queries per second"},
			},
		}},
	}
	report.Finalize(3*time.Second, false)

	if err := report.Write(); err != nil {
		t.Fatalf("write report: %v", err)
	}

	htmlData := readE2EReportFile(t, filepath.Join(report.outputDir, "index.html"))
	assertE2EContains(t, htmlData, "服务 E2E 中文报告")
	assertE2EContains(t, htmlData, "测试总览")
	assertE2EContains(t, htmlData, "接口检查")
	assertE2EContains(t, htmlData, "report.css")
	assertE2EContains(t, htmlData, "report_renderer.js")
	assertE2EContains(t, htmlData, "report.js")
	assertE2EFileExists(t, filepath.Join(report.outputDir, "report.css"))
	assertE2EFileExists(t, filepath.Join(report.outputDir, "report_renderer.js"))
	assertE2EFileExists(t, filepath.Join(report.outputDir, "report.js"))

	jsonData := readE2EReportFile(t, filepath.Join(report.outputDir, "report.json"))
	var payload map[string]any
	if err := json.Unmarshal([]byte(jsonData), &payload); err != nil {
		t.Fatalf("decode report.json: %v", err)
	}
	assertE2EJSONField(t, payload, "dns_checks")
	assertE2EJSONField(t, payload, "api_checks")
	assertE2EJSONField(t, payload, "performance")
	assertE2EJSONField(t, payload, "suites")
}

func readE2EReportFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertE2EContains(t *testing.T, body, needle string) {
	t.Helper()
	if !strings.Contains(body, needle) {
		t.Fatalf("expected %q in output", needle)
	}
}

func assertE2EJSONField(t *testing.T, payload map[string]any, key string) {
	t.Helper()
	if _, ok := payload[key]; !ok {
		t.Fatalf("missing key %q in payload", key)
	}
}

func assertE2EFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
}
