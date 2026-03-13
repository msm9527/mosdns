package coremain

import (
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
}
