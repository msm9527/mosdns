package requeryruntime

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestRequeryRuntimeStoreLifecycle(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control.db")
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

func TestPruneHistory(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control.db")
	t.Cleanup(func() {
		_ = ResetForTesting(dbPath)
	})

	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		runID := fmt.Sprintf("run-%d", i+1)
		updatedAt := now.Add(time.Duration(-i) * time.Hour).UnixMilli()
		if err := SaveRun(dbPath, Run{
			RunID:           runID,
			ConfigKey:       "cfg-a",
			Mode:            "full_rebuild",
			TriggerSource:   "manual",
			State:           "completed",
			StartedAtUnixMS: updatedAt - 1000,
			EndedAtUnixMS:   updatedAt,
			UpdatedAtUnixMS: updatedAt,
		}); err != nil {
			t.Fatalf("SaveRun %s: %v", runID, err)
		}
		for j := 0; j < 3; j++ {
			if err := SaveCheckpoint(dbPath, Checkpoint{
				ConfigKey:       "cfg-a",
				RunID:           runID,
				Stage:           fmt.Sprintf("stage-%d", j),
				Completed:       j + 1,
				Total:           3,
				Snapshot:        json.RawMessage(fmt.Sprintf(`{"idx":%d}`, j)),
				CreatedAtUnixMS: updatedAt + int64(j),
			}); err != nil {
				t.Fatalf("SaveCheckpoint %s/%d: %v", runID, j, err)
			}
		}
	}

	summary, err := PruneHistory(dbPath, PruneOptions{
		KeepRuns:              2,
		KeepCheckpointsPerRun: 1,
	})
	if err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if summary.DeletedRuns != 1 || summary.DeletedCheckpoints != 4 {
		t.Fatalf("unexpected prune summary: %+v", summary)
	}
	if summary.RemainingRuns != 2 || summary.RemainingChecks != 2 {
		t.Fatalf("unexpected remaining counts: %+v", summary)
	}

	runs, err := ListRuns(dbPath, "", 10)
	if err != nil {
		t.Fatalf("ListRuns after prune: %v", err)
	}
	if len(runs) != 2 || runs[0].RunID != "run-1" || runs[1].RunID != "run-2" {
		t.Fatalf("unexpected runs after prune: %+v", runs)
	}

	checkpoints, err := ListCheckpoints(dbPath, "", "", 10)
	if err != nil {
		t.Fatalf("ListCheckpoints after prune: %v", err)
	}
	if len(checkpoints) != 2 {
		t.Fatalf("unexpected checkpoints after prune: %+v", checkpoints)
	}
}
