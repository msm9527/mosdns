package coremain

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	Key                  string `json:"key"`
	Format               string `json:"format"`
	Content              string `json:"content"`
	Version              int64  `json:"version"`
	ContentSHA256        string `json:"content_sha256,omitempty"`
	UpdatedAtUnixMS      int64  `json:"updated_at_unix_ms"`
	LastExportedAtUnixMS int64  `json:"last_exported_at_unix_ms,omitempty"`
	LastExportStatus     string `json:"last_export_status,omitempty"`
	LastExportError      string `json:"last_export_error,omitempty"`
	LastVerifiedAtUnixMS int64  `json:"last_verified_at_unix_ms,omitempty"`
	LastVerifiedStatus   string `json:"last_verified_status,omitempty"`
	LastVerifiedError    string `json:"last_verified_error,omitempty"`
	LastFileSHA256       string `json:"last_file_sha256,omitempty"`
}

func LoadGeneratedDatasetFromPath(path, key string) (*GeneratedDataset, bool, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return nil, false, err
	}

	if entry, ok, err := loadStructuredGeneratedDataset(store.db.DB(), key); err != nil {
		return nil, false, err
	} else if ok {
		return &GeneratedDataset{
			Format:  entry.Format,
			Content: entry.Content,
		}, true, nil
	}
	return nil, false, nil
}

func SaveGeneratedDatasetToPath(path, key, format, content string) error {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return err
	}

	tx, err := store.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin generated_dataset tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`
		INSERT INTO generated_dataset (
			dataset_key, output_path, format, content, version, content_sha256, updated_at_unix_ms,
			last_exported_at_unix_ms, last_export_status, last_export_error,
			last_verified_at_unix_ms, last_verified_status, last_verified_error, last_file_sha256
		)
		VALUES (?, ?, ?, ?, 1, ?, unixepoch('subsec') * 1000, 0, '', '', 0, '', '', '')
		ON CONFLICT(dataset_key) DO UPDATE SET
			output_path = excluded.output_path,
			format = excluded.format,
			content = excluded.content,
			content_sha256 = excluded.content_sha256,
			updated_at_unix_ms = excluded.updated_at_unix_ms,
			version = CASE
				WHEN generated_dataset.content <> excluded.content OR generated_dataset.format <> excluded.format THEN generated_dataset.version + 1
				ELSE generated_dataset.version
			END,
			last_export_status = CASE
				WHEN generated_dataset.content <> excluded.content OR generated_dataset.format <> excluded.format THEN ''
				ELSE generated_dataset.last_export_status
			END,
			last_export_error = CASE
				WHEN generated_dataset.content <> excluded.content OR generated_dataset.format <> excluded.format THEN ''
				ELSE generated_dataset.last_export_error
			END,
			last_exported_at_unix_ms = CASE
				WHEN generated_dataset.content <> excluded.content OR generated_dataset.format <> excluded.format THEN 0
				ELSE generated_dataset.last_exported_at_unix_ms
			END,
			last_verified_status = CASE
				WHEN generated_dataset.content <> excluded.content OR generated_dataset.format <> excluded.format THEN ''
				ELSE generated_dataset.last_verified_status
			END,
			last_verified_error = CASE
				WHEN generated_dataset.content <> excluded.content OR generated_dataset.format <> excluded.format THEN ''
				ELSE generated_dataset.last_verified_error
			END,
			last_verified_at_unix_ms = CASE
				WHEN generated_dataset.content <> excluded.content OR generated_dataset.format <> excluded.format THEN 0
				ELSE generated_dataset.last_verified_at_unix_ms
			END,
			last_file_sha256 = CASE
				WHEN generated_dataset.content <> excluded.content OR generated_dataset.format <> excluded.format THEN ''
				ELSE generated_dataset.last_file_sha256
			END
	`, key, key, format, content, sha256Text(content)); err != nil {
		return fmt.Errorf("save generated_dataset %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit generated_dataset %s: %w", key, err)
	}
	return nil
}

func ListGeneratedDatasetsFromPath(path string) ([]GeneratedDatasetEntry, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return nil, err
	}

	datasets, err := listStructuredGeneratedDatasets(store.db.DB())
	if err != nil {
		return nil, err
	}
	return datasets, nil
}

func ExportGeneratedDatasetsToFiles(path string) (int, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return 0, err
	}
	datasets, err := ListGeneratedDatasetsFromPath(path)
	if err != nil {
		return 0, err
	}
	exported := 0
	for _, dataset := range datasets {
		if err := os.MkdirAll(filepath.Dir(dataset.Key), 0o755); err != nil {
			_ = recordGeneratedDatasetExport(store.db.DB(), dataset.Key, "error", err.Error(), "")
			return exported, fmt.Errorf("create dataset directory for %s: %w", dataset.Key, err)
		}
		tmpFile := dataset.Key + ".tmp"
		if err := os.WriteFile(tmpFile, []byte(dataset.Content), 0o644); err != nil {
			_ = recordGeneratedDatasetExport(store.db.DB(), dataset.Key, "error", err.Error(), "")
			return exported, fmt.Errorf("write generated dataset temp file %s: %w", dataset.Key, err)
		}
		if err := os.Rename(tmpFile, dataset.Key); err != nil {
			_ = os.Remove(tmpFile)
			_ = recordGeneratedDatasetExport(store.db.DB(), dataset.Key, "error", err.Error(), "")
			return exported, fmt.Errorf("rename generated dataset file %s: %w", dataset.Key, err)
		}
		if err := recordGeneratedDatasetExport(store.db.DB(), dataset.Key, "success", "", sha256Text(dataset.Content)); err != nil {
			return exported, err
		}
		exported++
	}
	return exported, nil
}

type GeneratedDatasetVerifySummary struct {
	Checked  int                     `json:"checked"`
	Matched  int                     `json:"matched"`
	Missing  int                     `json:"missing"`
	Mismatch int                     `json:"mismatch"`
	Entries  []GeneratedDatasetEntry `json:"entries"`
}

func VerifyGeneratedDatasetsOnFiles(path string) (*GeneratedDatasetVerifySummary, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return nil, err
	}
	datasets, err := ListGeneratedDatasetsFromPath(path)
	if err != nil {
		return nil, err
	}
	summary := &GeneratedDatasetVerifySummary{
		Entries: make([]GeneratedDatasetEntry, 0, len(datasets)),
	}
	for _, dataset := range datasets {
		summary.Checked++
		fileData, err := os.ReadFile(dataset.Key)
		switch {
		case os.IsNotExist(err):
			dataset.LastVerifiedStatus = "missing"
			dataset.LastVerifiedError = "file does not exist"
			dataset.LastFileSHA256 = ""
			summary.Missing++
		case err != nil:
			dataset.LastVerifiedStatus = "error"
			dataset.LastVerifiedError = err.Error()
			dataset.LastFileSHA256 = ""
		default:
			dataset.LastFileSHA256 = sha256Bytes(fileData)
			if string(fileData) == dataset.Content {
				dataset.LastVerifiedStatus = "matched"
				dataset.LastVerifiedError = ""
				summary.Matched++
			} else {
				dataset.LastVerifiedStatus = "mismatch"
				dataset.LastVerifiedError = "file content does not match runtime dataset"
				summary.Mismatch++
			}
		}
		if err := recordGeneratedDatasetVerify(store.db.DB(), dataset.Key, dataset.LastVerifiedStatus, dataset.LastVerifiedError, dataset.LastFileSHA256); err != nil {
			return nil, err
		}
		refreshed, ok, err := loadStructuredGeneratedDataset(store.db.DB(), dataset.Key)
		if err != nil {
			return nil, err
		}
		if ok {
			dataset = refreshed
		}
		summary.Entries = append(summary.Entries, dataset)
	}
	return summary, nil
}

func loadStructuredGeneratedDataset(db *sql.DB, key string) (GeneratedDatasetEntry, bool, error) {
	var entry GeneratedDatasetEntry
	err := db.QueryRow(`
		SELECT dataset_key, format, content, version, content_sha256, updated_at_unix_ms,
		       last_exported_at_unix_ms, last_export_status, last_export_error,
		       last_verified_at_unix_ms, last_verified_status, last_verified_error, last_file_sha256
		FROM generated_dataset
		WHERE dataset_key = ?
	`, key).Scan(
		&entry.Key,
		&entry.Format,
		&entry.Content,
		&entry.Version,
		&entry.ContentSHA256,
		&entry.UpdatedAtUnixMS,
		&entry.LastExportedAtUnixMS,
		&entry.LastExportStatus,
		&entry.LastExportError,
		&entry.LastVerifiedAtUnixMS,
		&entry.LastVerifiedStatus,
		&entry.LastVerifiedError,
		&entry.LastFileSHA256,
	)
	switch err {
	case nil:
		return entry, true, nil
	case sql.ErrNoRows:
		return GeneratedDatasetEntry{}, false, nil
	default:
		return GeneratedDatasetEntry{}, false, fmt.Errorf("query generated_dataset %s: %w", key, err)
	}
}

func listStructuredGeneratedDatasets(db *sql.DB) ([]GeneratedDatasetEntry, error) {
	rows, err := db.Query(`
		SELECT dataset_key, format, content, version, content_sha256, updated_at_unix_ms,
		       last_exported_at_unix_ms, last_export_status, last_export_error,
		       last_verified_at_unix_ms, last_verified_status, last_verified_error, last_file_sha256
		FROM generated_dataset
		ORDER BY dataset_key ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list generated_dataset: %w", err)
	}
	defer rows.Close()

	datasets := make([]GeneratedDatasetEntry, 0)
	for rows.Next() {
		var entry GeneratedDatasetEntry
		if err := rows.Scan(
			&entry.Key,
			&entry.Format,
			&entry.Content,
			&entry.Version,
			&entry.ContentSHA256,
			&entry.UpdatedAtUnixMS,
			&entry.LastExportedAtUnixMS,
			&entry.LastExportStatus,
			&entry.LastExportError,
			&entry.LastVerifiedAtUnixMS,
			&entry.LastVerifiedStatus,
			&entry.LastVerifiedError,
			&entry.LastFileSHA256,
		); err != nil {
			return nil, fmt.Errorf("scan generated_dataset: %w", err)
		}
		datasets = append(datasets, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate generated_dataset: %w", err)
	}
	return datasets, nil
}

func listStructuredGeneratedDatasetEntries(db *sql.DB) ([]RuntimeStateEntry, error) {
	datasets, err := listStructuredGeneratedDatasets(db)
	if err != nil {
		return nil, err
	}
	entries := make([]RuntimeStateEntry, 0, len(datasets))
	for _, dataset := range datasets {
		raw, err := json.Marshal(GeneratedDataset{
			Format:  dataset.Format,
			Content: dataset.Content,
		})
		if err != nil {
			return nil, fmt.Errorf("marshal generated_dataset %s: %w", dataset.Key, err)
		}
		entries = append(entries, RuntimeStateEntry{
			Namespace:       runtimeStateNamespaceGeneratedDataset,
			Key:             dataset.Key,
			Value:           json.RawMessage(raw),
			UpdatedAtUnixMS: dataset.UpdatedAtUnixMS,
		})
	}
	return entries, nil
}

func recordGeneratedDatasetExport(db *sql.DB, key, status, errText, fileSHA string) error {
	if _, err := db.Exec(`
		UPDATE generated_dataset
		SET last_exported_at_unix_ms = unixepoch('subsec') * 1000,
			last_export_status = ?,
			last_export_error = ?,
			last_file_sha256 = ?
		WHERE dataset_key = ?
	`, status, errText, fileSHA, key); err != nil {
		return fmt.Errorf("update generated_dataset export status %s: %w", key, err)
	}
	return nil
}

func recordGeneratedDatasetVerify(db *sql.DB, key, status, errText, fileSHA string) error {
	if _, err := db.Exec(`
		UPDATE generated_dataset
		SET last_verified_at_unix_ms = unixepoch('subsec') * 1000,
			last_verified_status = ?,
			last_verified_error = ?,
			last_file_sha256 = ?
		WHERE dataset_key = ?
	`, status, errText, fileSHA, key); err != nil {
		return fmt.Errorf("update generated_dataset verify status %s: %w", key, err)
	}
	return nil
}

func sha256Text(s string) string {
	return sha256Bytes([]byte(s))
}

func sha256Bytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
