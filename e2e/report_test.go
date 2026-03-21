package e2e_test

import (
	"encoding/json"
	"html/template"
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
	Name     string `json:"name"`
	Status   string `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Duration string `json:"duration"`
}

type e2eReportView struct {
	GeneratedAt string
	Duration    string
	Status      string
	Total       int
	Passed      int
	Failed      int
	Cases       []e2eCaseReport
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

func (r *e2eReport) RunCase(t *testing.T, name string, fn func(*testing.T)) {
	startedAt := time.Now()
	ok := t.Run(name, fn)
	item := e2eCaseReport{
		Name:     name,
		Status:   "passed",
		Detail:   "all assertions passed",
		Duration: time.Since(startedAt).Round(time.Millisecond).String(),
	}
	if !ok {
		item.Status = "failed"
		item.Detail = "see go test output for assertion details"
	}

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

	if err := os.MkdirAll(r.outputDir, 0o755); err != nil {
		return err
	}
	if err := r.writeJSON(view); err != nil {
		return err
	}
	return r.writeHTML(view)
}

func (r *e2eReport) viewLocked() e2eReportView {
	passed := 0
	failed := 0
	for _, item := range r.cases {
		if item.Status == "passed" {
			passed++
			continue
		}
		failed++
	}
	return e2eReportView{
		GeneratedAt: r.finishedAt.Format(time.RFC3339),
		Duration:    r.duration.Round(time.Millisecond).String(),
		Status:      r.status,
		Total:       len(r.cases),
		Passed:      passed,
		Failed:      failed,
		Cases:       append([]e2eCaseReport(nil), r.cases...),
	}
}

func (r *e2eReport) writeJSON(view e2eReportView) error {
	data, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.outputDir, "report.json"), data, 0o644)
}

func (r *e2eReport) writeHTML(view e2eReportView) error {
	tmpl, err := template.ParseFiles(filepath.Join("testdata", "report.html.tmpl"))
	if err != nil {
		return err
	}
	file, err := os.Create(filepath.Join(r.outputDir, "index.html"))
	if err != nil {
		return err
	}
	defer file.Close()
	return tmpl.Execute(file, view)
}
