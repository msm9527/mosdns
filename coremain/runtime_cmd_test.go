package coremain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if err := RecordSystemEvent("runtime.test", "info", "hello", map[string]any{"ok": true}); err != nil {
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

	eventsJSON, err := runtimeEventsJSON(dbPath, 20)
	if err != nil {
		t.Fatalf("runtimeEventsJSON: %v", err)
	}
	var events []SystemEventEntry
	if err := json.Unmarshal(eventsJSON, &events); err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(events) == 0 || events[0].Component != "runtime.test" {
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

	cmd := newRuntimeCmd()
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

	cmd := newRuntimeCmd()
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

func TestImportLegacyRuntimeState(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, runtimeStateDBFilename)

	if err := os.WriteFile(filepath.Join(baseDir, overridesFilename), []byte(`{"socks5":"127.0.0.1:1080"}`), 0o644); err != nil {
		t.Fatalf("write overrides: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, upstreamOverridesFilename), []byte(`{"test":[{"tag":"u1","protocol":"udp","addr":"8.8.8.8"}]}`), 0o644); err != nil {
		t.Fatalf("write upstreams: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(baseDir, "webinfo"), 0o755); err != nil {
		t.Fatalf("mkdir webinfo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "webinfo", "clientname.json"), []byte(`{"1.1.1.1":"cloudflare"}`), 0o644); err != nil {
		t.Fatalf("write clientname: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "webinfo", "requeryconfig.json"), []byte(`{"workflow":{"mode":"hybrid"},"status":{"task_state":"idle"}}`), 0o644); err != nil {
		t.Fatalf("write requery config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "switches.json"), []byte(`{"core_mode":"secure"}`), 0o644); err != nil {
		t.Fatalf("write switches: %v", err)
	}

	summary, err := ImportLegacyRuntimeState(baseDir)
	if err != nil {
		t.Fatalf("ImportLegacyRuntimeState: %v", err)
	}
	if summary.Overrides != 1 || summary.Upstreams != 1 || summary.Switches != 1 || summary.Webinfo != 1 || summary.Requery != 1 {
		t.Fatalf("unexpected import summary: %+v", summary)
	}

	var overrides GlobalOverrides
	if ok, err := LoadRuntimeStateJSONFromPath(dbPath, runtimeStateNamespaceOverrides, runtimeStateKeyGlobalOverrides, &overrides); err != nil || !ok || overrides.Socks5 != "127.0.0.1:1080" {
		t.Fatalf("unexpected imported overrides: ok=%v err=%v payload=%+v", ok, err, overrides)
	}
	var webinfo map[string]string
	if ok, err := LoadRuntimeStateJSONFromPath(filepath.Join(baseDir, "webinfo", runtimeStateDBFilename), runtimeNamespaceWebinfo, filepath.Join(baseDir, "webinfo", "clientname.json"), &webinfo); err != nil || !ok || webinfo["1.1.1.1"] != "cloudflare" {
		t.Fatalf("unexpected imported webinfo: ok=%v err=%v payload=%+v", ok, err, webinfo)
	}
	var requeryConfig map[string]any
	if ok, err := LoadRuntimeStateJSONFromPath(filepath.Join(baseDir, "webinfo", runtimeStateDBFilename), runtimeNamespaceRequery, filepath.Join(baseDir, "webinfo", "requeryconfig.json")+":config", &requeryConfig); err != nil || !ok {
		t.Fatalf("unexpected imported requery config: ok=%v err=%v payload=%+v", ok, err, requeryConfig)
	}
	if _, exists := requeryConfig["status"]; exists {
		t.Fatalf("expected status to be stripped from config payload: %+v", requeryConfig)
	}
}

func TestRuntimeCmdLegacyImportOutput(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	if err := os.WriteFile(filepath.Join(MainConfigBaseDir, overridesFilename), []byte(`{"socks5":"127.0.0.1:1080"}`), 0o644); err != nil {
		t.Fatalf("write overrides: %v", err)
	}

	cmd := newRuntimeCmd()
	buf := new(strings.Builder)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"-d", MainConfigBaseDir, "legacy", "import"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}

	var summary LegacyRuntimeImportSummary
	if err := json.Unmarshal([]byte(buf.String()), &summary); err != nil {
		t.Fatalf("decode output: %v output=%q", err, buf.String())
	}
	if summary.Overrides != 1 {
		t.Fatalf("unexpected import summary: %+v", summary)
	}
}

func TestExportLegacyRuntimeState(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, runtimeStateDBFilename)

	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeStateNamespaceOverrides, runtimeStateKeyGlobalOverrides, GlobalOverrides{Socks5: "127.0.0.1:1080"}); err != nil {
		t.Fatalf("save runtime overrides: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeStateNamespaceUpstreams, runtimeStateKeyUpstreamConfig, GlobalUpstreamOverrides{
		"test": {{Tag: "u1", Protocol: "udp", Addr: "8.8.8.8"}},
	}); err != nil {
		t.Fatalf("save runtime upstreams: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceSwitch, filepath.Join(baseDir, "switches.json"), map[string]string{"core_mode": "secure"}); err != nil {
		t.Fatalf("save runtime switch: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceWebinfo, filepath.Join(baseDir, "webinfo", "clientname.json"), map[string]string{"1.1.1.1": "cloudflare"}); err != nil {
		t.Fatalf("save runtime webinfo: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceRequery, filepath.Join(baseDir, "webinfo", "requeryconfig.state.json")+":state", map[string]any{"status": map[string]any{"task_state": "idle"}}); err != nil {
		t.Fatalf("save runtime requery: %v", err)
	}
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeNamespaceAdguard, filepath.Join(baseDir, "adguard", "config.json"), []map[string]any{{"id": "rule-1"}}); err != nil {
		t.Fatalf("save runtime adguard: %v", err)
	}

	summary, err := ExportLegacyRuntimeState(baseDir)
	if err != nil {
		t.Fatalf("ExportLegacyRuntimeState: %v", err)
	}
	if summary.Overrides != 1 || summary.Upstreams != 1 || summary.Switches != 1 || summary.Webinfo != 1 || summary.Requery != 1 || summary.Adguard != 1 {
		t.Fatalf("unexpected export summary: %+v", summary)
	}
	if _, err := os.Stat(filepath.Join(baseDir, overridesFilename)); err != nil {
		t.Fatalf("expected overrides export: %v", err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "adguard", "config.json")); err != nil {
		t.Fatalf("expected adguard export: %v", err)
	}
}

func TestRuntimeCmdLegacyExportOutput(t *testing.T) {
	oldBaseDir := MainConfigBaseDir
	MainConfigBaseDir = t.TempDir()
	t.Cleanup(func() {
		MainConfigBaseDir = oldBaseDir
	})

	dbPath := filepath.Join(MainConfigBaseDir, runtimeStateDBFilename)
	if err := SaveRuntimeStateJSONToPath(dbPath, runtimeStateNamespaceOverrides, runtimeStateKeyGlobalOverrides, GlobalOverrides{Socks5: "127.0.0.1:1080"}); err != nil {
		t.Fatalf("save runtime overrides: %v", err)
	}

	cmd := newRuntimeCmd()
	buf := new(strings.Builder)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"-d", MainConfigBaseDir, "legacy", "export"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("cmd.Execute() error = %v", err)
	}

	var summary LegacyRuntimeImportSummary
	if err := json.Unmarshal([]byte(buf.String()), &summary); err != nil {
		t.Fatalf("decode output: %v output=%q", err, buf.String())
	}
	if summary.Overrides != 1 {
		t.Fatalf("unexpected export summary: %+v", summary)
	}
}
