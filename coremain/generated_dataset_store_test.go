package coremain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedDatasetStructuredStore(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, runtimeStateDBFilename)
	target := filepath.Join(baseDir, "gen", "realip.rule")

	if err := SaveGeneratedDatasetToPath(dbPath, target, "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	dataset, ok, err := LoadGeneratedDatasetFromPath(dbPath, target)
	if err != nil {
		t.Fatalf("LoadGeneratedDatasetFromPath: %v", err)
	}
	if !ok {
		t.Fatalf("expected generated dataset to exist")
	}
	if dataset.Format != "domain_output_rule" || dataset.Content != "full:example.com\n" {
		t.Fatalf("unexpected generated dataset: %+v", dataset)
	}

	datasets, err := ListGeneratedDatasetsFromPath(dbPath)
	if err != nil {
		t.Fatalf("ListGeneratedDatasetsFromPath: %v", err)
	}
	if len(datasets) != 1 {
		t.Fatalf("unexpected generated datasets: %+v", datasets)
	}
	if datasets[0].LastExportStatus != "" || datasets[0].LastExportedAtUnixMS != 0 {
		t.Fatalf("unexpected pre-export metadata: %+v", datasets[0])
	}

	exported, err := ExportGeneratedDatasetsToFiles(dbPath)
	if err != nil {
		t.Fatalf("ExportGeneratedDatasetsToFiles: %v", err)
	}
	if exported != 1 {
		t.Fatalf("unexpected exported count: %d", exported)
	}

	datasets, err = ListGeneratedDatasetsFromPath(dbPath)
	if err != nil {
		t.Fatalf("ListGeneratedDatasetsFromPath after export: %v", err)
	}
	if len(datasets) != 1 || datasets[0].LastExportStatus != "success" || datasets[0].LastExportedAtUnixMS == 0 {
		t.Fatalf("unexpected exported dataset metadata: %+v", datasets)
	}
	if datasets[0].Version != 1 || datasets[0].ContentSHA256 == "" || datasets[0].LastFileSHA256 == "" {
		t.Fatalf("expected integrity metadata after export: %+v", datasets[0])
	}
}

func TestGeneratedDatasetVerifyOnFiles(t *testing.T) {
	baseDir := t.TempDir()
	dbPath := filepath.Join(baseDir, runtimeStateDBFilename)
	target := filepath.Join(baseDir, "gen", "realip.rule")

	if err := SaveGeneratedDatasetToPath(dbPath, target, "domain_output_rule", "full:example.com\n"); err != nil {
		t.Fatalf("SaveGeneratedDatasetToPath: %v", err)
	}

	summary, err := VerifyGeneratedDatasetsOnFiles(dbPath)
	if err != nil {
		t.Fatalf("VerifyGeneratedDatasetsOnFiles missing: %v", err)
	}
	if summary.Checked != 1 || summary.Missing != 1 || summary.Matched != 0 || summary.Mismatch != 0 {
		t.Fatalf("unexpected missing summary: %+v", summary)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(target, []byte("full:example.com\n"), 0o644); err != nil {
		t.Fatalf("WriteFile match: %v", err)
	}
	summary, err = VerifyGeneratedDatasetsOnFiles(dbPath)
	if err != nil {
		t.Fatalf("VerifyGeneratedDatasetsOnFiles matched: %v", err)
	}
	if summary.Checked != 1 || summary.Matched != 1 || summary.Missing != 0 || summary.Mismatch != 0 {
		t.Fatalf("unexpected matched summary: %+v", summary)
	}

	if err := os.WriteFile(target, []byte("full:changed.example.com\n"), 0o644); err != nil {
		t.Fatalf("WriteFile mismatch: %v", err)
	}
	summary, err = VerifyGeneratedDatasetsOnFiles(dbPath)
	if err != nil {
		t.Fatalf("VerifyGeneratedDatasetsOnFiles mismatch: %v", err)
	}
	if summary.Checked != 1 || summary.Matched != 0 || summary.Missing != 0 || summary.Mismatch != 1 {
		t.Fatalf("unexpected mismatch summary: %+v", summary)
	}

	datasets, err := ListGeneratedDatasetsFromPath(dbPath)
	if err != nil {
		t.Fatalf("ListGeneratedDatasetsFromPath: %v", err)
	}
	if len(datasets) != 1 || datasets[0].LastVerifiedStatus != "mismatch" || datasets[0].LastVerifiedAtUnixMS == 0 || datasets[0].LastFileSHA256 == "" {
		t.Fatalf("unexpected verify metadata: %+v", datasets)
	}
}
