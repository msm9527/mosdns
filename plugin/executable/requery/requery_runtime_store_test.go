package requery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/internal/requeryruntime"
)

func TestSaveConfigUnlockedSyncsRuntimeJobs(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "requery.json")

	p := &Requery{
		filePath: cfgFile,
		config:   newDefaultConfig(),
		status:   Status{TaskState: "idle"},
	}
	p.config.Scheduler.Enabled = true
	p.config.Scheduler.IntervalMinutes = 30

	if err := p.saveConfigUnlocked(); err != nil {
		t.Fatalf("saveConfigUnlocked: %v", err)
	}

	jobs, err := requeryruntime.ListJobs(p.runtimeDBPath(), p.runtimeConfigKey())
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 4 {
		t.Fatalf("unexpected runtime jobs: %+v", jobs)
	}
}

func TestRequeryAPIListsRunsAndCheckpoints(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "requery.json")

	p := &Requery{
		filePath: cfgFile,
		config:   newDefaultConfig(),
		status:   Status{TaskState: "idle"},
	}
	if err := p.saveConfigUnlocked(); err != nil {
		t.Fatalf("saveConfigUnlocked: %v", err)
	}
	if err := requeryruntime.SaveRun(p.runtimeDBPath(), requeryruntime.Run{
		RunID:         "run-1",
		ConfigKey:     p.runtimeConfigKey(),
		Mode:          "full_rebuild",
		TriggerSource: "manual",
		State:         "completed",
	}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if err := requeryruntime.SaveCheckpoint(p.runtimeDBPath(), requeryruntime.Checkpoint{
		ConfigKey: p.runtimeConfigKey(),
		RunID:     "run-1",
		Stage:     "priority",
		Completed: 10,
		Total:     10,
		Snapshot:  json.RawMessage(`{"ok":true}`),
	}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/runs?limit=5", nil)
	w := httptest.NewRecorder()
	p.api().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status for runs: %d body=%s", w.Code, w.Body.String())
	}
	var runs []requeryruntime.Run
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "run-1" {
		t.Fatalf("unexpected runs payload: %+v", runs)
	}

	req = httptest.NewRequest(http.MethodGet, "/checkpoints?run_id=run-1&limit=5", nil)
	w = httptest.NewRecorder()
	p.api().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status for checkpoints: %d body=%s", w.Code, w.Body.String())
	}
	var checkpoints []requeryruntime.Checkpoint
	if err := json.Unmarshal(w.Body.Bytes(), &checkpoints); err != nil {
		t.Fatalf("decode checkpoints: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].RunID != "run-1" {
		t.Fatalf("unexpected checkpoints payload: %+v", checkpoints)
	}
}

func TestPersistRunSnapshotWithIDKeepsFinalStateAfterActiveRunCleared(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "requery.json")

	p := &Requery{
		filePath: cfgFile,
		config:   newDefaultConfig(),
		status: Status{
			TaskState:        "idle",
			TaskMode:         "quick_prewarm",
			LastRunStartTime: time.Now().UTC().Add(-time.Second),
		},
		activeTriggerSource: "manual",
	}
	if err := p.saveConfigUnlocked(); err != nil {
		t.Fatalf("saveConfigUnlocked: %v", err)
	}

	endedAt := time.Now().UTC()
	if err := p.persistRunSnapshotWithID("run-finish", "idle", endedAt); err != nil {
		t.Fatalf("persistRunSnapshotWithID: %v", err)
	}

	runs, err := requeryruntime.ListRuns(p.runtimeDBPath(), p.runtimeConfigKey(), 5)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("unexpected runs: %+v", runs)
	}
	if runs[0].RunID != "run-finish" || runs[0].State != "idle" || runs[0].EndedAtUnixMS == 0 {
		t.Fatalf("unexpected final run snapshot: %+v", runs[0])
	}
}
