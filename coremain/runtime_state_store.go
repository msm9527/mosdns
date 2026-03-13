package coremain

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
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
	switch namespace {
	case runtimeNamespaceSwitch:
		return s.getStructuredSwitchState(key, dst)
	case runtimeStateNamespaceOverrides:
		return s.getStructuredGlobalOverrides(key, dst)
	case runtimeStateNamespaceUpstreams:
		return s.getStructuredUpstreamOverrides(key, dst)
	case runtimeNamespaceAdguard:
		return s.getStructuredAdguardState(key, dst)
	case runtimeNamespaceDiversion:
		return s.getStructuredDiversionState(key, dst)
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
	switch namespace {
	case runtimeNamespaceSwitch:
		return s.putStructuredSwitchState(key, value)
	case runtimeStateNamespaceOverrides:
		return s.putStructuredGlobalOverrides(key, value)
	case runtimeStateNamespaceUpstreams:
		return s.putStructuredUpstreamOverrides(key, value)
	case runtimeNamespaceAdguard:
		return s.putStructuredAdguardState(key, value)
	case runtimeNamespaceDiversion:
		return s.putStructuredDiversionState(key, value)
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
	switch namespace {
	case runtimeNamespaceSwitch:
		return s.removeStructuredSwitchState(key)
	case runtimeStateNamespaceOverrides:
		return s.removeStructuredGlobalOverrides(key)
	case runtimeStateNamespaceUpstreams:
		return s.removeStructuredUpstreamOverrides(key)
	case runtimeNamespaceAdguard:
		return s.removeStructuredAdguardState(key)
	case runtimeNamespaceDiversion:
		return s.removeStructuredDiversionState(key)
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
	if namespace == runtimeStateNamespaceOverrides {
		entries, err := s.listStructuredGlobalOverrides()
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return entries, nil
		}
	}
	if namespace == runtimeStateNamespaceUpstreams {
		entries, err := s.listStructuredUpstreamOverrides()
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return entries, nil
		}
	}
	if namespace == runtimeNamespaceAdguard {
		entries, err := s.listStructuredAdguardState()
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return entries, nil
		}
	}
	if namespace == runtimeNamespaceDiversion {
		entries, err := s.listStructuredDiversionState()
		if err != nil {
			return nil, err
		}
		if len(entries) > 0 {
			return entries, nil
		}
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

func (s *runtimeStateStore) getStructuredGlobalOverrides(key string, dst any) (bool, error) {
	row := s.db.DB().QueryRow(`
		SELECT socks5, ecs, replacements_json
		FROM global_override_state
		WHERE scope_key = ?
	`, key)

	var payload GlobalOverrides
	var replacementsJSON string
	err := row.Scan(&payload.Socks5, &payload.ECS, &replacementsJSON)
	switch err {
	case nil:
		if strings.TrimSpace(replacementsJSON) != "" {
			if err := json.Unmarshal([]byte(replacementsJSON), &payload.Replacements); err != nil {
				return false, fmt.Errorf("decode global_override_state %s replacements: %w", key, err)
			}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return false, fmt.Errorf("marshal global_override_state %s: %w", key, err)
		}
		if err := json.Unmarshal(data, dst); err != nil {
			return false, fmt.Errorf("decode global_override_state %s: %w", key, err)
		}
		return true, nil
	case sql.ErrNoRows:
	default:
		return false, fmt.Errorf("query global_override_state %s: %w", key, err)
	}

	row = s.db.DB().QueryRow(`SELECT value_json FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeStateNamespaceOverrides, key)
	var raw string
	err = row.Scan(&raw)
	switch err {
	case nil:
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, fmt.Errorf("query runtime state %s/%s: %w", runtimeStateNamespaceOverrides, key, err)
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("decode runtime state %s/%s: %w", runtimeStateNamespaceOverrides, key, err)
	}
	return true, nil
}

func (s *runtimeStateStore) putStructuredGlobalOverrides(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal global_override_state %s: %w", key, err)
	}
	var payload GlobalOverrides
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("decode global_override_state payload %s: %w", key, err)
	}
	replacementsJSON, err := json.Marshal(payload.Replacements)
	if err != nil {
		return fmt.Errorf("marshal global_override_state replacements %s: %w", key, err)
	}

	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin global_override_state tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`
		INSERT INTO global_override_state (scope_key, socks5, ecs, replacements_json, updated_at_unix_ms)
		VALUES (?, ?, ?, ?, unixepoch('subsec') * 1000)
		ON CONFLICT(scope_key) DO UPDATE SET
			socks5 = excluded.socks5,
			ecs = excluded.ecs,
			replacements_json = excluded.replacements_json,
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`, key, payload.Socks5, payload.ECS, string(replacementsJSON)); err != nil {
		return fmt.Errorf("save global_override_state %s: %w", key, err)
	}
	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeStateNamespaceOverrides, key); err != nil {
		return fmt.Errorf("cleanup legacy runtime overrides %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit global_override_state %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) removeStructuredGlobalOverrides(key string) error {
	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin delete global_override_state tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM global_override_state WHERE scope_key = ?`, key); err != nil {
		return fmt.Errorf("delete global_override_state %s: %w", key, err)
	}
	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeStateNamespaceOverrides, key); err != nil {
		return fmt.Errorf("delete legacy global_override_state %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit delete global_override_state %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) listStructuredGlobalOverrides() ([]RuntimeStateEntry, error) {
	rows, err := s.db.DB().Query(`
		SELECT scope_key, socks5, ecs, replacements_json, updated_at_unix_ms
		FROM global_override_state
		ORDER BY scope_key ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list global_override_state: %w", err)
	}
	defer rows.Close()

	var entries []RuntimeStateEntry
	for rows.Next() {
		var key, socks5, ecs, replacementsJSON string
		var updatedAt int64
		if err := rows.Scan(&key, &socks5, &ecs, &replacementsJSON, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan global_override_state: %w", err)
		}
		payload := GlobalOverrides{Socks5: socks5, ECS: ecs}
		if strings.TrimSpace(replacementsJSON) != "" {
			if err := json.Unmarshal([]byte(replacementsJSON), &payload.Replacements); err != nil {
				return nil, fmt.Errorf("decode global_override_state replacements %s: %w", key, err)
			}
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal global_override_state %s: %w", key, err)
		}
		entries = append(entries, RuntimeStateEntry{
			Namespace:       runtimeStateNamespaceOverrides,
			Key:             key,
			Value:           json.RawMessage(raw),
			UpdatedAtUnixMS: updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate global_override_state: %w", err)
	}
	return entries, nil
}

func (s *runtimeStateStore) getStructuredUpstreamOverrides(key string, dst any) (bool, error) {
	rowset, err := s.db.DB().Query(`
		SELECT plugin_tag, payload_json
		FROM upstream_override_item
		ORDER BY plugin_tag ASC, upstream_tag ASC
	`)
	if err != nil {
		return false, fmt.Errorf("query upstream_override_item: %w", err)
	}
	defer rowset.Close()

	cfg := make(GlobalUpstreamOverrides)
	for rowset.Next() {
		var pluginTag string
		var payloadJSON string
		var item UpstreamOverrideConfig
		if err := rowset.Scan(&pluginTag, &payloadJSON); err != nil {
			return false, fmt.Errorf("scan upstream_override_item: %w", err)
		}
		if err := json.Unmarshal([]byte(payloadJSON), &item); err != nil {
			return false, fmt.Errorf("decode upstream_override_item %s: %w", pluginTag, err)
		}
		cfg[pluginTag] = append(cfg[pluginTag], item)
	}
	if err := rowset.Err(); err != nil {
		return false, fmt.Errorf("iterate upstream_override_item: %w", err)
	}
	if len(cfg) > 0 {
		data, err := json.Marshal(cfg)
		if err != nil {
			return false, fmt.Errorf("marshal upstream_override_item payload: %w", err)
		}
		if err := json.Unmarshal(data, dst); err != nil {
			return false, fmt.Errorf("decode upstream_override_item payload: %w", err)
		}
		return true, nil
	}

	row := s.db.DB().QueryRow(`SELECT value_json FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeStateNamespaceUpstreams, key)
	var raw string
	err = row.Scan(&raw)
	switch err {
	case nil:
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, fmt.Errorf("query runtime state %s/%s: %w", runtimeStateNamespaceUpstreams, key, err)
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("decode runtime state %s/%s: %w", runtimeStateNamespaceUpstreams, key, err)
	}
	return true, nil
}

func (s *runtimeStateStore) putStructuredUpstreamOverrides(key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal upstream_override_item %s: %w", key, err)
	}
	var cfg GlobalUpstreamOverrides
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("decode upstream_override_item payload %s: %w", key, err)
	}

	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin upstream_override_item tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM upstream_override_item`); err != nil {
		return fmt.Errorf("clear upstream_override_item: %w", err)
	}

	pluginTags := make([]string, 0, len(cfg))
	for pluginTag := range cfg {
		pluginTags = append(pluginTags, pluginTag)
	}
	sort.Strings(pluginTags)
	for _, pluginTag := range pluginTags {
		items := cfg[pluginTag]
		sort.Slice(items, func(i, j int) bool { return items[i].Tag < items[j].Tag })
		for _, item := range items {
			payloadJSON, err := json.Marshal(item)
			if err != nil {
				return fmt.Errorf("marshal upstream_override_item %s/%s: %w", pluginTag, item.Tag, err)
			}
			if _, err = tx.Exec(`
				INSERT INTO upstream_override_item (plugin_tag, upstream_tag, enabled, protocol, payload_json, updated_at_unix_ms)
				VALUES (?, ?, ?, ?, ?, unixepoch('subsec') * 1000)
			`, pluginTag, item.Tag, runtimeBoolToInt(item.Enabled), item.Protocol, string(payloadJSON)); err != nil {
				return fmt.Errorf("insert upstream_override_item %s/%s: %w", pluginTag, item.Tag, err)
			}
		}
	}

	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeStateNamespaceUpstreams, key); err != nil {
		return fmt.Errorf("cleanup legacy runtime upstreams %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit upstream_override_item %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) removeStructuredUpstreamOverrides(key string) error {
	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin delete upstream_override_item tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM upstream_override_item`); err != nil {
		return fmt.Errorf("delete upstream_override_item: %w", err)
	}
	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeStateNamespaceUpstreams, key); err != nil {
		return fmt.Errorf("delete legacy upstream_override_item %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit delete upstream_override_item %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) listStructuredUpstreamOverrides() ([]RuntimeStateEntry, error) {
	rows, err := s.db.DB().Query(`
		SELECT plugin_tag, payload_json, updated_at_unix_ms
		FROM upstream_override_item
		ORDER BY plugin_tag ASC, upstream_tag ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list upstream_override_item: %w", err)
	}
	defer rows.Close()

	type grouped struct {
		items     []UpstreamOverrideConfig
		updatedAt int64
	}
	groupedByPlugin := make(map[string]*grouped)
	order := make([]string, 0)
	for rows.Next() {
		var pluginTag string
		var payloadJSON string
		var updatedAt int64
		var item UpstreamOverrideConfig
		if err := rows.Scan(&pluginTag, &payloadJSON, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan upstream_override_item: %w", err)
		}
		if err := json.Unmarshal([]byte(payloadJSON), &item); err != nil {
			return nil, fmt.Errorf("decode upstream_override_item %s: %w", pluginTag, err)
		}
		g := groupedByPlugin[pluginTag]
		if g == nil {
			g = &grouped{}
			groupedByPlugin[pluginTag] = g
			order = append(order, pluginTag)
		}
		g.items = append(g.items, item)
		if updatedAt > g.updatedAt {
			g.updatedAt = updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate upstream_override_item: %w", err)
	}

	entries := make([]RuntimeStateEntry, 0, len(order))
	for _, pluginTag := range order {
		raw, err := json.Marshal(groupedByPlugin[pluginTag].items)
		if err != nil {
			return nil, fmt.Errorf("marshal upstream_override_item %s: %w", pluginTag, err)
		}
		entries = append(entries, RuntimeStateEntry{
			Namespace:       runtimeStateNamespaceUpstreams,
			Key:             pluginTag,
			Value:           json.RawMessage(raw),
			UpdatedAtUnixMS: groupedByPlugin[pluginTag].updatedAt,
		})
	}
	return entries, nil
}

func (s *runtimeStateStore) getStructuredAdguardState(key string, dst any) (bool, error) {
	rows, err := s.db.DB().Query(`
		SELECT payload_json
		FROM adguard_rule_item
		WHERE config_key = ?
		ORDER BY rule_id ASC
	`, key)
	if err != nil {
		return false, fmt.Errorf("query adguard_rule_item %s: %w", key, err)
	}
	defer rows.Close()

	items, err := collectJSONArrayFromRows(rows)
	if err != nil {
		return false, fmt.Errorf("collect adguard_rule_item %s: %w", key, err)
	}
	if len(items) > 0 {
		if err := json.Unmarshal(items, dst); err != nil {
			return false, fmt.Errorf("decode adguard_rule_item %s: %w", key, err)
		}
		return true, nil
	}

	return s.getFromLegacyKV(runtimeNamespaceAdguard, key, dst)
}

func (s *runtimeStateStore) putStructuredAdguardState(key string, value any) error {
	items, err := normalizeJSONArrayObjects(value)
	if err != nil {
		return fmt.Errorf("normalize adguard_rule_item %s: %w", key, err)
	}

	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin adguard_rule_item tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM adguard_rule_item WHERE config_key = ?`, key); err != nil {
		return fmt.Errorf("clear adguard_rule_item %s: %w", key, err)
	}
	for _, item := range items {
		ruleID := runtimeStringField(item, "id")
		if ruleID == "" {
			ruleID = runtimeStringField(item, "name")
		}
		payloadJSON, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal adguard_rule_item %s/%s: %w", key, ruleID, err)
		}
		if _, err = tx.Exec(`
			INSERT INTO adguard_rule_item (
				config_key, rule_id, name, url, enabled, auto_update, update_interval_hours, payload_json, updated_at_unix_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, unixepoch('subsec') * 1000)
		`, key, ruleID, runtimeStringField(item, "name"), runtimeStringField(item, "url"),
			runtimeBoolToInt(runtimeBoolField(item, "enabled")),
			runtimeBoolToInt(runtimeBoolField(item, "auto_update")),
			runtimeIntField(item, "update_interval_hours"),
			string(payloadJSON),
		); err != nil {
			return fmt.Errorf("insert adguard_rule_item %s/%s: %w", key, ruleID, err)
		}
	}
	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeNamespaceAdguard, key); err != nil {
		return fmt.Errorf("cleanup legacy adguard_rule_item %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit adguard_rule_item %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) removeStructuredAdguardState(key string) error {
	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin delete adguard_rule_item tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM adguard_rule_item WHERE config_key = ?`, key); err != nil {
		return fmt.Errorf("delete adguard_rule_item %s: %w", key, err)
	}
	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeNamespaceAdguard, key); err != nil {
		return fmt.Errorf("delete legacy adguard_rule_item %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit delete adguard_rule_item %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) listStructuredAdguardState() ([]RuntimeStateEntry, error) {
	rows, err := s.db.DB().Query(`
		SELECT config_key, payload_json, updated_at_unix_ms
		FROM adguard_rule_item
		ORDER BY config_key ASC, rule_id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list adguard_rule_item: %w", err)
	}
	defer rows.Close()
	return collectGroupedJSONArrayEntries(rows, runtimeNamespaceAdguard)
}

func (s *runtimeStateStore) getStructuredDiversionState(key string, dst any) (bool, error) {
	rows, err := s.db.DB().Query(`
		SELECT payload_json
		FROM diversion_rule_source
		WHERE config_key = ?
		ORDER BY source_name ASC
	`, key)
	if err != nil {
		return false, fmt.Errorf("query diversion_rule_source %s: %w", key, err)
	}
	defer rows.Close()

	items, err := collectJSONArrayFromRows(rows)
	if err != nil {
		return false, fmt.Errorf("collect diversion_rule_source %s: %w", key, err)
	}
	if len(items) > 0 {
		if err := json.Unmarshal(items, dst); err != nil {
			return false, fmt.Errorf("decode diversion_rule_source %s: %w", key, err)
		}
		return true, nil
	}

	return s.getFromLegacyKV(runtimeNamespaceDiversion, key, dst)
}

func (s *runtimeStateStore) putStructuredDiversionState(key string, value any) error {
	items, err := normalizeJSONArrayObjects(value)
	if err != nil {
		return fmt.Errorf("normalize diversion_rule_source %s: %w", key, err)
	}

	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin diversion_rule_source tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM diversion_rule_source WHERE config_key = ?`, key); err != nil {
		return fmt.Errorf("clear diversion_rule_source %s: %w", key, err)
	}
	for _, item := range items {
		sourceName := runtimeStringField(item, "name")
		payloadJSON, err := json.Marshal(item)
		if err != nil {
			return fmt.Errorf("marshal diversion_rule_source %s/%s: %w", key, sourceName, err)
		}
		if _, err = tx.Exec(`
			INSERT INTO diversion_rule_source (
				config_key, source_name, source_type, files, url, enabled, auto_update, update_interval_hours, payload_json, updated_at_unix_ms
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, unixepoch('subsec') * 1000)
		`, key, sourceName, runtimeStringField(item, "type"), runtimeStringField(item, "files"),
			runtimeStringField(item, "url"),
			runtimeBoolToInt(runtimeBoolField(item, "enabled")),
			runtimeBoolToInt(runtimeBoolField(item, "auto_update")),
			runtimeIntField(item, "update_interval_hours"),
			string(payloadJSON),
		); err != nil {
			return fmt.Errorf("insert diversion_rule_source %s/%s: %w", key, sourceName, err)
		}
	}
	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeNamespaceDiversion, key); err != nil {
		return fmt.Errorf("cleanup legacy diversion_rule_source %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit diversion_rule_source %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) removeStructuredDiversionState(key string) error {
	tx, err := s.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin delete diversion_rule_source tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM diversion_rule_source WHERE config_key = ?`, key); err != nil {
		return fmt.Errorf("delete diversion_rule_source %s: %w", key, err)
	}
	if _, err = tx.Exec(`DELETE FROM runtime_kv WHERE namespace = ? AND key = ?`, runtimeNamespaceDiversion, key); err != nil {
		return fmt.Errorf("delete legacy diversion_rule_source %s: %w", key, err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit delete diversion_rule_source %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) listStructuredDiversionState() ([]RuntimeStateEntry, error) {
	rows, err := s.db.DB().Query(`
		SELECT config_key, payload_json, updated_at_unix_ms
		FROM diversion_rule_source
		ORDER BY config_key ASC, source_name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list diversion_rule_source: %w", err)
	}
	defer rows.Close()
	return collectGroupedJSONArrayEntries(rows, runtimeNamespaceDiversion)
}

func (s *runtimeStateStore) getFromLegacyKV(namespace, key string, dst any) (bool, error) {
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

func collectJSONArrayFromRows(rows *sql.Rows) ([]byte, error) {
	items := make([]json.RawMessage, 0)
	for rows.Next() {
		var payloadJSON string
		if err := rows.Scan(&payloadJSON); err != nil {
			return nil, err
		}
		items = append(items, json.RawMessage(payloadJSON))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return json.Marshal(items)
}

func collectGroupedJSONArrayEntries(rows *sql.Rows, namespace string) ([]RuntimeStateEntry, error) {
	type grouped struct {
		items     []json.RawMessage
		updatedAt int64
	}
	groupedByKey := make(map[string]*grouped)
	order := make([]string, 0)
	for rows.Next() {
		var key string
		var payloadJSON string
		var updatedAt int64
		if err := rows.Scan(&key, &payloadJSON, &updatedAt); err != nil {
			return nil, err
		}
		g := groupedByKey[key]
		if g == nil {
			g = &grouped{}
			groupedByKey[key] = g
			order = append(order, key)
		}
		g.items = append(g.items, json.RawMessage(payloadJSON))
		if updatedAt > g.updatedAt {
			g.updatedAt = updatedAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	entries := make([]RuntimeStateEntry, 0, len(order))
	for _, key := range order {
		raw, err := json.Marshal(groupedByKey[key].items)
		if err != nil {
			return nil, err
		}
		entries = append(entries, RuntimeStateEntry{
			Namespace:       namespace,
			Key:             key,
			Value:           json.RawMessage(raw),
			UpdatedAtUnixMS: groupedByKey[key].updatedAt,
		})
	}
	return entries, nil
}

func normalizeJSONArrayObjects(value any) ([]map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var items []map[string]any
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func runtimeStringField(item map[string]any, key string) string {
	if item == nil {
		return ""
	}
	if v, ok := item[key].(string); ok {
		return v
	}
	return ""
}

func runtimeBoolField(item map[string]any, key string) bool {
	if item == nil {
		return false
	}
	if v, ok := item[key].(bool); ok {
		return v
	}
	return false
}

func runtimeIntField(item map[string]any, key string) int {
	if item == nil {
		return 0
	}
	switch v := item[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

func runtimeBoolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
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
