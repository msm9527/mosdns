package coremain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type GeneratedDataset struct {
	Format  string `json:"format"`
	Content string `json:"content"`
}

const runtimeStateNamespaceGeneratedDataset = "generated_dataset"

type GeneratedDatasetEntry struct {
	Key             string `json:"key"`
	Format          string `json:"format"`
	Content         string `json:"content"`
	UpdatedAtUnixMS int64  `json:"updated_at_unix_ms"`
}

func LoadGeneratedDatasetFromPath(path, key string) (*GeneratedDataset, bool, error) {
	var dataset GeneratedDataset
	ok, err := LoadRuntimeStateJSONFromPath(path, runtimeStateNamespaceGeneratedDataset, key, &dataset)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &dataset, true, nil
}

func SaveGeneratedDatasetToPath(path, key, format, content string) error {
	return SaveRuntimeStateJSONToPath(path, runtimeStateNamespaceGeneratedDataset, key, GeneratedDataset{
		Format:  format,
		Content: content,
	})
}

func ListGeneratedDatasetsFromPath(path string) ([]GeneratedDatasetEntry, error) {
	entries, err := ListRuntimeStateByNamespace(path, runtimeStateNamespaceGeneratedDataset)
	if err != nil {
		return nil, err
	}
	datasets := make([]GeneratedDatasetEntry, 0, len(entries))
	for _, entry := range entries {
		var dataset GeneratedDataset
		if err := json.Unmarshal(entry.Value, &dataset); err != nil {
			return nil, fmt.Errorf("decode generated dataset %s: %w", entry.Key, err)
		}
		datasets = append(datasets, GeneratedDatasetEntry{
			Key:             entry.Key,
			Format:          dataset.Format,
			Content:         dataset.Content,
			UpdatedAtUnixMS: entry.UpdatedAtUnixMS,
		})
	}
	return datasets, nil
}

func ExportGeneratedDatasetsToFiles(path string) (int, error) {
	datasets, err := ListGeneratedDatasetsFromPath(path)
	if err != nil {
		return 0, err
	}
	exported := 0
	for _, dataset := range datasets {
		if err := os.MkdirAll(filepath.Dir(dataset.Key), 0o755); err != nil {
			return exported, fmt.Errorf("create dataset directory for %s: %w", dataset.Key, err)
		}
		tmpFile := dataset.Key + ".tmp"
		if err := os.WriteFile(tmpFile, []byte(dataset.Content), 0o644); err != nil {
			return exported, fmt.Errorf("write generated dataset temp file %s: %w", dataset.Key, err)
		}
		if err := os.Rename(tmpFile, dataset.Key); err != nil {
			_ = os.Remove(tmpFile)
			return exported, fmt.Errorf("rename generated dataset file %s: %w", dataset.Key, err)
		}
		exported++
	}
	return exported, nil
}
