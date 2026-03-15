package coremain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/internal/requeryruntime"
)

func TestResolveRuntimeCommandBaseDir(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveRuntimeCommandBaseDir("", dir)
	if err != nil {
		t.Fatalf("resolveRuntimeCommandBaseDir() error = %v", err)
	}
	if got != dir {
		t.Fatalf("unexpected dir: got %q want %q", got, dir)
	}
}

func TestRuntimeCommandHelpers(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	dbPath := filepath.Join(MainConfigBaseDir, runtimeStateDBFilename)
	target := filepath.Join(MainConfigBaseDir, "gen", "realip.rule")

	if err := SaveGeneratedDatasetToPath(dbPath, target, "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}
	if err := RecordSystemEvent("control.test", "info", "hello", map[string]any{"ok": true}); err != nil {
		t.Fatalf("RecordSystemEvent: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceAdguard, "config.json", []map[string]any{{"id": "rule-1"}}); err != nil {
		t.Fatalf("SaveRuntimeStateJSONToPath adguard: %v", err)
	}

	summaryJSON, err := runtimeSummaryJSON(dbPath)
	if err != nil {
		t.Fatalf("runtimeSummaryJSON: %v", err)
	}
	var summary runtimeSummaryResponse
	if err := json.Unmarshal(summaryJSON, &summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.DBPath != dbPath || len(summary.Namespaces) == 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}

	healthJSON, err := runtimeHealthJSON(dbPath)
	if err != nil {
		t.Fatalf("runtimeHealthJSON: %v", err)
	}
	var health runtimeHealthResponse
	if err := json.Unmarshal(healthJSON, &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if health.StorageEngine != "sqlite" || len(health.Checks) == 0 {
		t.Fatalf("unexpected health: %+v", health)
	}

	datasetsJSON, err := runtimeDatasetsJSON(dbPath)
	if err != nil {
		t.Fatalf("runtimeDatasetsJSON: %v", err)
	}
	var datasets []GeneratedDatasetEntry
	if err := json.Unmarshal(datasetsJSON, &datasets); err != nil {
		t.Fatalf("decode datasets: %v", err)
	}
	if len(datasets) != 1 || datasets[0].Key != target {
		t.Fatalf("unexpected datasets: %+v", datasets)
	}
	if datasets[0].Version != 1 || datasets[0].ContentSHA256 == "" {
		t.Fatalf("expected dataset integrity metadata: %+v", datasets)
	}

	verifyJSON, err := runtimeDatasetsVerifyJSON(dbPath)
	if err != nil {
		t.Fatalf("runtimeDatasetsVerifyJSON: %v", err)
	}
	var verifySummary GeneratedDatasetVerifySummary
	if err := json.Unmarshal(verifyJSON, &verifySummary); err != nil {
		t.Fatalf("decode verify summary: %v", err)
	}
	if verifySummary.Checked != 1 || verifySummary.Missing != 1 {
		t.Fatalf("unexpected verify summary before export: %+v", verifySummary)
	}

	eventsJSON, err := runtimeEventsJSON(dbPath, 20)
	if err != nil {
		t.Fatalf("runtimeEventsJSON: %v", err)
	}
	var events []SystemEventEntry
	if err := json.Unmarshal(eventsJSON, &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(events) == 0 || events[0].Component != "control.test" {
		t.Fatalf("unexpected events: %+v", events)
	}

	if err := requeryruntime.ReplaceJobs(dbPath, "cfg-a", []requeryruntime.Job{{
		JobID:         "cfg-a/full_rebuild/manual",
		ConfigKey:     "cfg-a",
		Mode:          "full_rebuild",
		TriggerSource: "manual",
		Enabled:       true,
		Definition:    json.RawMessage(`{"limit":0}`),
	}}); err != nil {
		t.Fatalf("ReplaceJobs: %v", err)
	}
	if err := requeryruntime.SaveRun(dbPath, requeryruntime.Run{
		RunID:         "run-1",
		ConfigKey:     "cfg-a",
		JobID:         "cfg-a/full_rebuild/manual",
		Mode:          "full_rebuild",
		TriggerSource: "manual",
		State:         "completed",
	}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if err := requeryruntime.SaveCheckpoint(dbPath, requeryruntime.Checkpoint{
		ConfigKey: "cfg-a",
		RunID:     "run-1",
		Stage:     "priority",
		Completed: 10,
		Total:     10,
		Snapshot:  json.RawMessage(`{"ok":true}`),
	}); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}

	requeryJobsJSON, err := runtimeRequeryJobsJSON(dbPath)
	if err != nil {
		t.Fatalf("runtimeRequeryJobsJSON: %v", err)
	}
	var jobs []requeryruntime.Job
	if err := json.Unmarshal(requeryJobsJSON, &jobs); err != nil {
		t.Fatalf("decode jobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].JobID != "cfg-a/full_rebuild/manual" {
		t.Fatalf("unexpected jobs: %+v", jobs)
	}

	requeryRunsJSON, err := runtimeRequeryRunsJSON(dbPath, 20)
	if err != nil {
		t.Fatalf("runtimeRequeryRunsJSON: %v", err)
	}
	var runs []requeryruntime.Run
	if err := json.Unmarshal(requeryRunsJSON, &runs); err != nil {
		t.Fatalf("decode runs: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "run-1" {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	requeryCheckpointsJSON, err := runtimeRequeryCheckpointsJSON(dbPath, "run-1", 20)
	if err != nil {
		t.Fatalf("runtimeRequeryCheckpointsJSON: %v", err)
	}
	var checkpoints []requeryruntime.Checkpoint
	if err := json.Unmarshal(requeryCheckpointsJSON, &checkpoints); err != nil {
		t.Fatalf("decode checkpoints: %v", err)
	}
	if len(checkpoints) != 1 || checkpoints[0].RunID != "run-1" {
		t.Fatalf("unexpected checkpoints: %+v", checkpoints)
	}

	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected target file to be absent before export, err=%v", err)
	}
	exported, err := ExportGeneratedDatasetsToFiles(dbPath)
	if err != nil {
		t.Fatalf("ExportGeneratedDatasetsToFiles: %v", err)
	}
	if exported != 1 {
		t.Fatalf("unexpected exported count: %d", exported)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read exported file: %v", err)
	}
	if string(raw) != "full:example.com\n" {
		t.Fatalf("unexpected exported content: %q", string(raw))
	}
}

func TestParseExportedFilesOutput(t *testing.T) {
	n, err := parseExportedFilesOutput("exported_files=3\n")
	if err != nil {
		t.Fatalf("parseExportedFilesOutput() error = %v", err)
	}
	if n != 3 {
		t.Fatalf("unexpected parsed value: %d", n)
	}
	if _, err := parseExportedFilesOutput("bad"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestRuntimeCmdDatasetsExportOutput(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	dbPath := filepath.Join(MainConfigBaseDir, runtimeStateDBFilename)
	target := filepath.Join(MainConfigBaseDir, "gen", "realip.rule")
	if err := SaveGeneratedDatasetToPath(dbPath, target, "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	cmd := newControlCmd()
	buf := new(strings.Builder)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"-d", MainConfigBaseDir, "datasets", "export"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}
	exported, err := parseExportedFilesOutput(buf.String())
	if err != nil {
		t.Fatalf("parseExportedFilesOutput() error = %v", err)
	}
	if exported != 1 {
		t.Fatalf("unexpected exported count: %d output=%q", exported, buf.String())
	}
}

func TestRuntimeCmdDatasetsVerifyOutput(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	dbPath := filepath.Join(MainConfigBaseDir, runtimeStateDBFilename)
	target := filepath.Join(MainConfigBaseDir, "gen", "realip.rule")
	if err := SaveGeneratedDatasetToPath(dbPath, target, "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	cmd := newControlCmd()
	buf := new(strings.Builder)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"-d", MainConfigBaseDir, "datasets", "verify"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}
	var summary GeneratedDatasetVerifySummary
	if err := json.Unmarshal([]byte(buf.String()), &summary); err != nil {
		t.Fatalf("decode verify output: %v output=%q", err, buf.String())
	}
	if summary.Checked != 1 || summary.Missing != 1 {
		t.Fatalf("unexpected verify output: %+v", summary)
	}
}

func TestRuntimeCmdHealthOutput(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	dbPath := filepath.Join(MainConfigBaseDir, runtimeStateDBFilename)
	target := filepath.Join(MainConfigBaseDir, "gen", "realip.rule")
	if err := SaveGeneratedDatasetToPath(dbPath, target, "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	cmd := newControlCmd()
	buf := new(strings.Builder)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"-d", MainConfigBaseDir, "health"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}
	var health runtimeHealthResponse
	if err := json.Unmarshal([]byte(buf.String()), &health); err != nil {
		t.Fatalf("decode health output: %v output=%q", err, buf.String())
	}
	if health.StorageEngine != "sqlite" || len(health.Checks) == 0 {
		t.Fatalf("unexpected health output: %+v", health)
	}
}

func TestRuntimeCmdRequeryRunsOutput(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	dbPath := filepath.Join(MainConfigBaseDir, runtimeStateDBFilename)
	if err := requeryruntime.SaveRun(dbPath, requeryruntime.Run{
		RunID:         "run-1",
		ConfigKey:     "cfg-a",
		Mode:          "full_rebuild",
		TriggerSource: "manual",
		State:         "completed",
	}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}

	cmd := newControlCmd()
	buf := new(strings.Builder)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"-d", MainConfigBaseDir, "requery", "runs", "--limit", "5"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}

	var runs []requeryruntime.Run
	if err := json.Unmarshal([]byte(buf.String()), &runs); err != nil {
		t.Fatalf("decode output: %v output=%q", err, buf.String())
	}
	if len(runs) != 1 || runs[0].RunID != "run-1" {
		t.Fatalf("unexpected runs output: %+v", runs)
	}
}

func TestRuntimeCmdRequeryPruneOutput(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	dbPath := filepath.Join(MainConfigBaseDir, runtimeStateDBFilename)
	for i := 0; i < 3; i++ {
		runID := fmt.Sprintf("run-%d", i+1)
		now := time.Now().UTC().UnixMilli() - int64(i*1000)
		if err := requeryruntime.SaveRun(dbPath, requeryruntime.Run{
			RunID:           runID,
			ConfigKey:       "cfg-a",
			Mode:            "full_rebuild",
			TriggerSource:   "manual",
			State:           "completed",
			StartedAtUnixMS: now - 500,
			EndedAtUnixMS:   now,
			UpdatedAtUnixMS: now,
		}); err != nil {
			t.Fatalf("SaveRun: %v", err)
		}
		if err := requeryruntime.SaveCheckpoint(dbPath, requeryruntime.Checkpoint{
			ConfigKey: "cfg-a",
			RunID:     runID,
			Stage:     "priority",
			Completed: 1,
			Total:     1,
			Snapshot:  json.RawMessage(`{"ok":true}`),
		}); err != nil {
			t.Fatalf("SaveCheckpoint: %v", err)
		}
	}

	cmd := newControlCmd()
	buf := new(strings.Builder)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"-d", MainConfigBaseDir, "requery", "prune", "--keep-runs", "2", "--keep-checkpoints", "1"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}
	var summary requeryruntime.PruneSummary
	if err := json.Unmarshal([]byte(buf.String()), &summary); err != nil {
		t.Fatalf("decode prune output: %v output=%q", err, buf.String())
	}
	if summary.DeletedRuns != 1 || summary.RemainingRuns != 2 {
		t.Fatalf("unexpected prune output: %+v", summary)
	}
}
