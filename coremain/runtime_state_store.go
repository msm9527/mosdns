package coremain

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"

	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
)

const runtimeStateDBFilename = "runtime.db"

type runtimeStateStore struct {
	db *runtimesqlite.RuntimeDB
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
	if _, err := s.db.DB().Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, namespace, key); err != nil {
		return fmt.Errorf("delete runtime state %s/%s: %w", namespace, key, err)
	}
	return nil
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
