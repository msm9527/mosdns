package requeryruntime

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRequeryRuntimeStoreLifecycle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "runtime.db")
	t.Cleanup(func() {
		_ = ResetForTesting(dbPath)
	})

	if err := ReplaceJobs(dbPath, "cfg-a", []Job{
		{
			JobID:         "cfg-a/full_rebuild/manual",
			ConfigKey:     "cfg-a",
			Mode:          "full_rebuild",
			TriggerSource: "manual",
			Enabled:       true,
			Definition:    json.RawMessage(`{"limit":0}`),
		},
		{
			JobID:         "cfg-a/quick_rebuild/scheduler",
			ConfigKey:     "cfg-a",
			Mode:          "quick_rebuild",
			TriggerSource: "scheduler",
			Enabled:       true,
			Definition:    json.RawMessage(`{"interval_minutes":30}`),
		},
	}); err != nil {
		t.Fatalf("ReplaceJobs: %v", err)
	}

	jobs, err := ListJobs(dbPath, "cfg-a")
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}

	run := Run{
		RunID:         "run-1",
		ConfigKey:     "cfg-a",
		JobID:         "cfg-a/full_rebuild/manual",
		Mode:          "full_rebuild",
		TriggerSource: "manual",
		State:         "running",
		Stage:         "priority",
		StageLabel:    "高优先级阶段",
		Total:         100,
		Completed:     20,
		Metadata:      json.RawMessage(`{"source":"api"}`),
	}
	if err := SaveRun(dbPath, run); err != nil {
		t.Fatalf("SaveRun running: %v", err)
	}

	run.State = "completed"
	run.Completed = 100
	run.EndedAtUnixMS = run.StartedAtUnixMS + 1000
	run.UpdatedAtUnixMS = run.EndedAtUnixMS
	if err := SaveRun(dbPath, run); err != nil {
		t.Fatalf("SaveRun completed: %v", err)
	}

	if err := SaveCheckpoint(dbPath, Checkpoint{
		ConfigKey: "cfg-a",
		RunID:     "run-1",
		Stage:     "priority",
		Completed: 20,
		Total:     100,
		Snapshot:  json.RawMessage(`{"completed":20}`),
	}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	loadedRun, err := LoadRun(dbPath, "run-1")
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if loadedRun == nil || loadedRun.State != "completed" || loadedRun.Completed != 100 {
		t.Fatalf("unexpected loaded run: %+v", loadedRun)
	}

	runs, err := ListRuns(dbPath, "cfg-a", 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "run-1" {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	checkpoints, err := ListCheckpoints(dbPath, "cfg-a", "run-1", 10)
	if err != nil {
		t.Fatalf("ListCheckpoints: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].RunID != "run-1" {
		t.Fatalf("unexpected checkpoints: %+v", checkpoints)
	}
}
