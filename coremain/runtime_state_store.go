package coremain

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
)

const runtimeStateDBFilename = "runtime.db"

type runtimeStateStore struct {
	db *runtimesqlite.RuntimeDB
}

type RuntimeStateEntry struct {
	Namespace       string          `json:"namespace"`
	Key             string          `json:"key"`
	Value           json.RawMessage `json:"value"`
	UpdatedAtUnixMS int64           `json:"updated_at_unix_ms"`
}

var globalRuntimeStateStore struct {
	mu    sync.Mutex
	paths map[string]*runtimesqlite.RuntimeDB
}

func defaultRuntimeStateDBPath() string {
	baseDir := MainConfigBaseDir
	if baseDir == "" {
		baseDir = "."
	}
	return filepath.Join(baseDir, runtimeStateDBFilename)
}

func getRuntimeStateStore() (*runtimeStateStore, error) {
	return getRuntimeStateStoreByPath(defaultRuntimeStateDBPath())
}

func getRuntimeStateStoreByPath(path string) (*runtimeStateStore, error) {
	if path == "" {
		path = defaultRuntimeStateDBPath()
	}

	globalRuntimeStateStore.mu.Lock()
	defer globalRuntimeStateStore.mu.Unlock()

	if globalRuntimeStateStore.paths == nil {
		globalRuntimeStateStore.paths = make(map[string]*runtimesqlite.RuntimeDB)
	}
	if db := globalRuntimeStateStore.paths[path]; db != nil {
		return &runtimeStateStore{db: db}, nil
	}

	db, err := runtimesqlite.Open(path, nil)
	if err != nil {
		return nil, fmt.Errorf("open runtime state db: %w", err)
	}
	globalRuntimeStateStore.paths[path] = db
	return &runtimeStateStore{db: db}, nil
}

func (s *runtimeStateStore) get(namespace, key string, dst any) (bool, error) {
	if namespace == runtimeNamespaceSwitch {
		return s.getStructuredSwitchState(key, dst)
	}
	row := s.db.DB().QueryRow(`SELECT value_json FROM runtime_kv WHERE namespace = ? AND key = ?`, namespace, key)

	var raw string
	err := row.Scan(&raw)
	switch err {
	case nil:
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, fmt.Errorf("query runtime state %s/%s: %w", namespace, key, err)
	}

	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("decode runtime state %s/%s: %w", namespace, key, err)
	}
	return true, nil
}

func (s *runtimeStateStore) put(namespace, key string, value any) error {
	if namespace == runtimeNamespaceSwitch {
		return s.putStructuredSwitchState(key, value)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal runtime state %s/%s: %w", namespace, key, err)
	}
	if _, err := s.db.DB().Exec(`
		INSERT INTO runtime_kv (namespace, key, value_json, updated_at_unix_ms)
		VALUES (?, ?, ?, unixepoch('subsec') * 1000)
		ON CONFLICT(namespace, key) DO UPDATE SET
			value_json = excluded.value_json,
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`, namespace, key, string(data)); err != nil {
		return fmt.Errorf("save runtime state %s/%s: %w", namespace, key, err)
	}
	return nil
}

func (s *runtimeStateStore) remove(namespace, key string) error {
	if namespace == runtimeNamespaceSwitch {
		return s.removeStructuredSwitchState(key)
	}
	if _, err := s.db.DB().Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, namespace, key); err != nil {
		return fmt.Errorf("delete runtime state %s/%s: %w", namespace, key, err)
	}
	return nil
}

func (s *runtimeStateStore) list(namespace string) ([]RuntimeStateEntry, error) {
	if namespace == runtimeNamespaceSwitch {
		return s.listStructuredSwitchState()
	}
	if namespace == runtimeStateNamespaceGeneratedDataset {
		entries, err := listStructuredGeneratedDatasetEntries(s.db.DB())
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return entries, nil
		}
	}
	rows, err := s.db.DB().Query(`
		SELECT namespace, key, value_json, updated_at_unix_ms
		FROM runtime_kv
		WHERE namespace = ?
		ORDER BY key ASC
	`, namespace)
	if err != nil {
		return nil, fmt.Errorf("list runtime state namespace %s: %w", namespace, err)
	}
	defer rows.Close()

	var entries []RuntimeStateEntry
	for rows.Next() {
		var entry RuntimeStateEntry
		var raw string
		if err := rows.Scan(&entry.Namespace, &entry.Key, &raw, &entry.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scan runtime state namespace %s: %w", namespace, err)
		}
		entry.Value = json.RawMessage(raw)
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate runtime state namespace %s: %w", namespace, err)
	}
	return entries, nil
}

func (s *runtimeStateStore) getStructuredSwitchState(key string, dst any) (bool, error) {
	rowset, err := s.db.DB().Query(`
		SELECT switch_name, value
		FROM switch_state
		WHERE file_path = ?
		ORDER BY switch_name ASC
	`, key)
	if err != nil {
		return false, fmt.Errorf("query switch_state %s: %w", key, err)
	}
	defer rowset.Close()

	values := make(map[string]string)
	for rowset.Next() {
		var name string
		var value string
		if err := rowset.Scan(&name, &value); err != nil {
			return false, fmt.Errorf("scan switch_state %s: %w", key, err)
		}
		values[name] = value
	}
	if err := rowset.Err(); err != nil {
		return false, fmt.Errorf("iterate switch_state %s: %w", key, err)
	}
	if len(values) > 0 {
		data, err := json.Marshal(values)
		if err != nil {
			return false, fmt.Errorf("marshal switch_state %s: %w", key, err)
		}
		if err := json.Unmarshal(data, dst); err != nil {
			return false, fmt.Errorf("decode switch_state %s: %w", key, err)
		}
		return true, nil
	}

	row := s.db.DB().QueryRow(`SELECT value_json FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeNamespaceSwitch, key)
	var raw string
	err = row.Scan(&raw)
	switch err {
	case nil:
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, fmt.Errorf("query runtime state %s/%s: %w", runtimeNamespaceSwitch, key, err)
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("decode runtime state %s/%s: %w", runtimeNamespaceSwitch, key, err)
	}
	return true, nil
}

func (s *runtimeStateStore) putStructuredSwitchState(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal switch_state %s: %w", key, err)
	}
	values := make(map[string]string)
	if err := json.Unmarshal(data, &values); err != nil {
		return fmt.Errorf("decode switch_state payload %s: %w", key, err)
	}

	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin switch_state tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM switch_state WHERE file_path = ?`, key); err != nil {
		return fmt.Errorf("clear switch_state %s: %w", key, err)
	}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err = tx.Exec(`
			INSERT INTO switch_state (file_path, switch_name, value, updated_at_unix_ms)
			VALUES (?, ?, ?, unixepoch('subsec') * 1000)
		`, key, name, values[name]); err != nil {
			return fmt.Errorf("insert switch_state %s/%s: %w", key, name, err)
		}
	}

	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeNamespaceSwitch, key); err != nil {
		return fmt.Errorf("cleanup legacy runtime switch state %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit switch_state %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) removeStructuredSwitchState(key string) error {
	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin delete switch_state tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM switch_state WHERE file_path = ?`, key); err != nil {
		return fmt.Errorf("delete switch_state %s: %w", key, err)
	}
	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeNamespaceSwitch, key); err != nil {
		return fmt.Errorf("delete legacy switch_state %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit delete switch_state %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) listStructuredSwitchState() ([]RuntimeStateEntry, error) {
	rows, err := s.db.DB().Query(`
		SELECT file_path, switch_name, value, updated_at_unix_ms
		FROM switch_state
		ORDER BY file_path ASC, switch_name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list switch_state: %w", err)
	}
	defer rows.Close()

	type grouped struct {
		values    map[string]string
		updatedAt int64
	}
	groupedByFile := make(map[string]*grouped)
	order := make([]string, 0)
	for rows.Next() {
		var filePath, name, value string
		var updatedAt int64
		if err := rows.Scan(&filePath, &name, &value, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan switch_state: %w", err)
		}
		g := groupedByFile[filePath]
		if g == nil {
			g = &grouped{values: make(map[string]string)}
			groupedByFile[filePath] = g
			order = append(order, filePath)
		}
		g.values[name] = value
		if updatedAt > g.updatedAt {
			g.updatedAt = updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate switch_state: %w", err)
	}
	if len(groupedByFile) == 0 {
		rows, err := s.db.DB().Query(`
			SELECT namespace, key, value_json, updated_at_unix_ms
			FROM runtime_kv
			WHERE namespace = ?
			ORDER BY key ASC
		`, runtimeNamespaceSwitch)
		if err != nil {
			return nil, fmt.Errorf("list legacy switch runtime state: %w", err)
		}
		defer rows.Close()
		var entries []RuntimeStateEntry
		for rows.Next() {
			var entry RuntimeStateEntry
			var raw string
			if err := rows.Scan(&entry.Namespace, &entry.Key, &raw, &entry.UpdatedAtUnixMS); err != nil {
				return nil, fmt.Errorf("scan legacy switch runtime state: %w", err)
			}
			entry.Value = json.RawMessage(raw)
			entries = append(entries, entry)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate legacy switch runtime state: %w", err)
		}
		return entries, nil
	}

	entries := make([]RuntimeStateEntry, 0, len(order))
	for _, filePath := range order {
		raw, err := json.Marshal(groupedByFile[filePath].values)
		if err != nil {
			return nil, fmt.Errorf("marshal grouped switch_state %s: %w", filePath, err)
		}
		entries = append(entries, RuntimeStateEntry{
			Namespace:       runtimeNamespaceSwitch,
			Key:             filePath,
			Value:           json.RawMessage(raw),
			UpdatedAtUnixMS: groupedByFile[filePath].updatedAt,
		})
	}
	return entries, nil
}

func LoadRuntimeStateJSON(namespace, key string, dst any) (bool, error) {
	return LoadRuntimeStateJSONFromPath("", namespace, key, dst)
}

func LoadRuntimeStateJSONFromPath(path, namespace, key string, dst any) (bool, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return false, err
	}
	return store.get(namespace, key, dst)
}

func SaveRuntimeStateJSON(namespace, key string, value any) error {
	return SaveRuntimeStateJSONToPath("", namespace, key, value)
}

func SaveRuntimeStateJSONToPath(path, namespace, key string, value any) error {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return err
	}
	return store.put(namespace, key, value)
}

func DeleteRuntimeStateJSON(namespace, key string) error {
	return DeleteRuntimeStateJSONFromPath("", namespace, key)
}

func DeleteRuntimeStateJSONFromPath(path, namespace, key string) error {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return err
	}
	return store.remove(namespace, key)
}

func ListRuntimeStateByNamespace(path, namespace string) ([]RuntimeStateEntry, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return nil, err
	}
	return store.list(namespace)
}
