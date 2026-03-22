package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type e2eReport struct {
	mu         sync.Mutex
	outputDir  string
	startedAt  time.Time
	finishedAt time.Time
	duration   time.Duration
	status     string
	cases      []e2eCaseReport
}

type e2eCaseReport struct {
	Name     string            `json:"name"`
	Status   string            `json:"status"`
	Detail   string            `json:"detail,omitempty"`
	Duration string            `json:"duration"`
	Artifact string            `json:"artifact,omitempty"`
	Checks   []e2eCheckReport  `json:"checks,omitempty"`
	Metrics  []e2eMetricReport `json:"metrics,omitempty"`
}

type e2eCheckReport struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

type e2eMetricReport struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Detail string `json:"detail,omitempty"`
}

type e2eReportView struct {
	GeneratedAt  string              `json:"generated_at"`
	StartedAt    string              `json:"started_at"`
	FinishedAt   string              `json:"finished_at"`
	Duration     string              `json:"duration"`
	Status       string              `json:"status"`
	SuccessRate  string              `json:"success_rate"`
	Total        int                 `json:"total"`
	Passed       int                 `json:"passed"`
	Failed       int                 `json:"failed"`
	ChecksTotal  int                 `json:"checks_total"`
	MetricsTotal int                 `json:"metrics_total"`
	Environment  e2eEnvironmentInfo  `json:"environment"`
	Artifacts    []e2eArtifactReport `json:"artifacts"`
	Coverage     e2eCoverageView     `json:"coverage"`
	Suites       []e2eSuiteView      `json:"suites"`
	DNSChecks    []e2eDNSCheckView   `json:"dns_checks"`
	APIChecks    []e2eAPICheckView   `json:"api_checks"`
	Performance  e2ePerformanceView  `json:"performance"`
	Cases        []e2eCaseReport     `json:"cases"`
}

type e2eEnvironmentInfo struct {
	FixtureMode string `json:"fixture_mode"`
	GoVersion   string `json:"go_version"`
	GOOS        string `json:"goos"`
	GOARCH      string `json:"goarch"`
	CI          string `json:"ci"`
	Commit      string `json:"commit,omitempty"`
	Ref         string `json:"ref,omitempty"`
	ReportDir   string `json:"report_dir"`
}

type e2eArtifactReport struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
}

type e2eCaseRecorder struct {
	mu      sync.Mutex
	detail  string
	checks  []e2eCheckReport
	metrics []e2eMetricReport
}

func newE2EReport() *e2eReport {
	outputDir := os.Getenv("MOSDNS_E2E_REPORT_DIR")
	if outputDir == "" {
		outputDir = filepath.Join("reports", "latest")
	}
	return &e2eReport{
		outputDir: outputDir,
		startedAt: time.Now(),
		status:    "passed",
		cases:     make([]e2eCaseReport, 0, 8),
	}
}

func (r *e2eReport) AddSetupFailure(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = "failed"
	r.cases = append(r.cases, e2eCaseReport{
		Name:     "setup fixture",
		Status:   "failed",
		Detail:   err.Error(),
		Duration: "0s",
	})
}

func (r *e2eReport) RunCase(t *testing.T, name string, fn func(*testing.T, *e2eCaseRecorder)) {
	startedAt := time.Now()
	recorder := newE2ECaseRecorder()
	ok := t.Run(name, func(t *testing.T) {
		fn(t, recorder)
	})
	item := recorder.build(name, ok, time.Since(startedAt))

	r.mu.Lock()
	defer r.mu.Unlock()
	if item.Status == "failed" {
		r.status = "failed"
	}
	r.cases = append(r.cases, item)
}

func (r *e2eReport) Finalize(duration time.Duration, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finishedAt = time.Now()
	r.duration = duration
	if failed {
		r.status = "failed"
	}
}

func (r *e2eReport) Write() error {
	r.mu.Lock()
	view := r.viewLocked()
	r.mu.Unlock()

	if err := os.RemoveAll(r.outputDir); err != nil {
		return err
	}
	if err := os.MkdirAll(r.outputDir, 0o755); err != nil {
		return err
	}
	prepared, err := prepareE2EReportView(r.outputDir, view)
	if err != nil {
		return err
	}
	if err := r.writeJUnit(prepared); err != nil {
		return err
	}
	if err := r.writeJSON(prepared); err != nil {
		return err
	}
	return r.writeHTML(prepared)
}

func (r *e2eReport) viewLocked() e2eReportView {
	passed := 0
	failed := 0
	checksTotal := 0
	metricsTotal := 0
	for _, item := range r.cases {
		if item.Status == "passed" {
			passed++
		} else {
			failed++
		}
		checksTotal += len(item.Checks)
		metricsTotal += len(item.Metrics)
	}
	return e2eReportView{
		GeneratedAt:  r.finishedAt.Format(time.RFC3339),
		StartedAt:    r.startedAt.Format(time.RFC3339),
		FinishedAt:   r.finishedAt.Format(time.RFC3339),
		Duration:     r.duration.Round(time.Millisecond).String(),
		Status:       r.status,
		Total:        len(r.cases),
		Passed:       passed,
		Failed:       failed,
		ChecksTotal:  checksTotal,
		MetricsTotal: metricsTotal,
		Cases:        append([]e2eCaseReport(nil), r.cases...),
	}
}

func newE2ECaseRecorder() *e2eCaseRecorder {
	return &e2eCaseRecorder{
		checks:  make([]e2eCheckReport, 0, 8),
		metrics: make([]e2eMetricReport, 0, 8),
	}
}

func (c *e2eCaseRecorder) SetDetail(detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.detail = detail
}

func (c *e2eCaseRecorder) AddCheck(name, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks = append(c.checks, e2eCheckReport{Name: name, Detail: detail})
}

func (c *e2eCaseRecorder) AddMetric(name, value, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.metrics = append(c.metrics, e2eMetricReport{Name: name, Value: value, Detail: detail})
}

func (c *e2eCaseRecorder) build(name string, ok bool, duration time.Duration) e2eCaseReport {
	c.mu.Lock()
	defer c.mu.Unlock()

	item := e2eCaseReport{
		Name:     name,
		Status:   "passed",
		Detail:   c.detail,
		Duration: duration.Round(time.Millisecond).String(),
		Checks:   append([]e2eCheckReport(nil), c.checks...),
		Metrics:  append([]e2eMetricReport(nil), c.metrics...),
	}
	if item.Detail == "" {
		item.Detail = "all assertions passed"
	}
	if ok {
		return item
	}
	item.Status = "failed"
	if c.detail == "" {
		item.Detail = "see go test output for assertion details"
	}
	return item
}

func (r *e2eReport) writeJSON(view e2eReportView) error {
	data, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.outputDir, "report.json"), data, 0o644)
}

func (r *e2eReport) writeHTML(view e2eReportView) error {
	return writeE2EHTMLReport(r.outputDir, view)
}
