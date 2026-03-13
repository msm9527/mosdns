package coremain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
