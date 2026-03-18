package coremain

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
)

const runtimeStateDBFilename = "control.db"

type runtimeStateStore struct {
	db *runtimesqlite.RuntimeDB
}

type RuntimeStateEntry struct {
	Namespace       string          `json:"namespace"`
	Key             string          `json:"key"`
	Value           json.RawMessage `json:"value"`
	UpdatedAtUnixMS int64           `json:"updated_at_unix_ms"`
}

var runtimeStateDBPathOverride string

func defaultRuntimeStateDBPath() string {
	return runtimeStateDBPathForBaseDir(MainConfigBaseDir)
}

func RuntimeStateDBPath() string {
	return defaultRuntimeStateDBPath()
}

func setRuntimeStateDBPath(path string) {
	runtimeStateDBPathOverride = strings.TrimSpace(path)
}

func RuntimeStateDBPathForPath(referencePath string) string {
	if MainConfigBaseDir != "" {
		return defaultRuntimeStateDBPath()
	}
	cleanPath := filepath.Clean(strings.TrimSpace(referencePath))
	if cleanPath == "" || cleanPath == "." {
		return defaultRuntimeStateDBPath()
	}
	return filepath.Join(filepath.Dir(cleanPath), runtimeStateDBFilename)
}

func runtimeStateDBPathForBaseDir(baseDir string) string {
	if strings.TrimSpace(runtimeStateDBPathOverride) != "" {
		return runtimeStateDBPathOverride
	}
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
	db, err := runtimesqlite.OpenPersistent(path, nil)
	if err != nil {
		return nil, fmt.Errorf("open runtime state db: %w", err)
	}
	return &runtimeStateStore{db: db}, nil
}

func (s *runtimeStateStore) get(namespace, key string, dst any) (bool, error) {
	switch namespace {
	case runtimeNamespaceWebinfo:
		return s.getStructuredWebinfoState(key, dst)
	case runtimeNamespaceRequery:
		return s.getStructuredRequeryState(key, dst)
	case runtimeNamespaceAdguard:
		return s.getStructuredAdguardState(key, dst)
	case runtimeNamespaceDiversion:
		return s.getStructuredDiversionState(key, dst)
	}
	return false, fmt.Errorf("unsupported runtime namespace %q", namespace)
}

func (s *runtimeStateStore) put(namespace, key string, value any) error {
	switch namespace {
	case runtimeNamespaceWebinfo:
		return s.putStructuredWebinfoState(key, value)
	case runtimeNamespaceRequery:
		return s.putStructuredRequeryState(key, value)
	case runtimeNamespaceAdguard:
		return s.putStructuredAdguardState(key, value)
	case runtimeNamespaceDiversion:
		return s.putStructuredDiversionState(key, value)
	}
	return fmt.Errorf("unsupported runtime namespace %q", namespace)
}

func (s *runtimeStateStore) remove(namespace, key string) error {
	switch namespace {
	case runtimeNamespaceWebinfo:
		return s.removeStructuredWebinfoState(key)
	case runtimeNamespaceRequery:
		return s.removeStructuredRequeryState(key)
	case runtimeNamespaceAdguard:
		return s.removeStructuredAdguardState(key)
	case runtimeNamespaceDiversion:
		return s.removeStructuredDiversionState(key)
	}
	return fmt.Errorf("unsupported runtime namespace %q", namespace)
}

func (s *runtimeStateStore) list(namespace string) ([]RuntimeStateEntry, error) {
	if namespace == runtimeNamespaceWebinfo {
		return s.listStructuredWebinfoState()
	}
	if namespace == runtimeNamespaceRequery {
		return s.listStructuredRequeryState()
	}
	if namespace == runtimeNamespaceAdguard {
		return s.listStructuredAdguardState()
	}
	if namespace == runtimeNamespaceDiversion {
		return s.listStructuredDiversionState()
	}
	return nil, fmt.Errorf("unsupported runtime namespace %q", namespace)
}

func (s *runtimeStateStore) getStructuredWebinfoState(key string, dst any) (bool, error) {
	return getStructuredJSONStateByKey(s.db.DB(), "webinfo_state", "file_path", key, dst)
}

func (s *runtimeStateStore) putStructuredWebinfoState(key string, value any) error {
	return putStructuredJSONStateByKey(s.db.DB(), "webinfo_state", "file_path", key, value)
}

func (s *runtimeStateStore) removeStructuredWebinfoState(key string) error {
	return removeStructuredJSONStateByKey(s.db.DB(), "webinfo_state", "file_path", key)
}

func (s *runtimeStateStore) listStructuredWebinfoState() ([]RuntimeStateEntry, error) {
	return listStructuredJSONStateByKey(s.db.DB(), "webinfo_state", "file_path", runtimeNamespaceWebinfo)
}

func (s *runtimeStateStore) getStructuredRequeryState(key string, dst any) (bool, error) {
	configKey, stateKind, err := parseRequeryStateKey(key)
	if err != nil {
		return false, err
	}
	row := s.db.DB().QueryRow(`
		SELECT payload_json
		FROM requery_state
		WHERE file_path = ? AND state_kind = ?
	`, configKey, stateKind)
	return scanStructuredJSONRow(row, runtimeNamespaceRequery, key, dst)
}

func (s *runtimeStateStore) putStructuredRequeryState(key string, value any) error {
	configKey, stateKind, err := parseRequeryStateKey(key)
	if err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal requery_state %s: %w", key, err)
	}
	if _, err := s.db.DB().Exec(`
		INSERT INTO requery_state (file_path, state_kind, payload_json, updated_at_unix_ms)
		VALUES (?, ?, ?, unixepoch('subsec') * 1000)
		ON CONFLICT(file_path, state_kind) DO UPDATE SET
			payload_json = excluded.payload_json,
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`, configKey, stateKind, string(data)); err != nil {
		return fmt.Errorf("save requery_state %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) removeStructuredRequeryState(key string) error {
	configKey, stateKind, err := parseRequeryStateKey(key)
	if err != nil {
		return err
	}
	if _, err := s.db.DB().Exec(`DELETE FROM requery_state WHERE file_path = ? AND state_kind = ?`, configKey, stateKind); err != nil {
		return fmt.Errorf("delete requery_state %s: %w", key, err)
	}
	return nil
}

func (s *runtimeStateStore) listStructuredRequeryState() ([]RuntimeStateEntry, error) {
	rows, err := s.db.DB().Query(`
		SELECT file_path, state_kind, payload_json, updated_at_unix_ms
		FROM requery_state
		ORDER BY file_path ASC, state_kind ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list requery_state: %w", err)
	}
	defer rows.Close()

	entries := make([]RuntimeStateEntry, 0)
	for rows.Next() {
		var configKey string
		var stateKind string
		var payloadJSON string
		var updatedAt int64
		if err := rows.Scan(&configKey, &stateKind, &payloadJSON, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan requery_state: %w", err)
		}
		entries = append(entries, RuntimeStateEntry{
			Namespace:       runtimeNamespaceRequery,
			Key:             composeRequeryStateKey(configKey, stateKind),
			Value:           json.RawMessage(payloadJSON),
			UpdatedAtUnixMS: updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate requery_state: %w", err)
	}
	return entries, nil
}

func getStructuredJSONStateByKey(db *sql.DB, table, keyColumn, key string, dst any) (bool, error) {
	query := fmt.Sprintf(`SELECT payload_json FROM %s WHERE %s = ?`, table, keyColumn)
	row := db.QueryRow(query, key)
	return scanStructuredJSONRow(row, table, key, dst)
}

func putStructuredJSONStateByKey(db *sql.DB, table, keyColumn, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal %s %s: %w", table, key, err)
	}
	stmt := fmt.Sprintf(`
		INSERT INTO %s (%s, payload_json, updated_at_unix_ms)
		VALUES (?, ?, unixepoch('subsec') * 1000)
		ON CONFLICT(%s) DO UPDATE SET
			payload_json = excluded.payload_json,
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`, table, keyColumn, keyColumn)
	if _, err := db.Exec(stmt, key, string(data)); err != nil {
		return fmt.Errorf("save %s %s: %w", table, key, err)
	}
	return nil
}

func removeStructuredJSONStateByKey(db *sql.DB, table, keyColumn, key string) error {
	stmt := fmt.Sprintf(`DELETE FROM %s WHERE %s = ?`, table, keyColumn)
	if _, err := db.Exec(stmt, key); err != nil {
		return fmt.Errorf("delete %s %s: %w", table, key, err)
	}
	return nil
}

func listStructuredJSONStateByKey(db *sql.DB, table, keyColumn, namespace string) ([]RuntimeStateEntry, error) {
	query := fmt.Sprintf(`
		SELECT %s, payload_json, updated_at_unix_ms
		FROM %s
		ORDER BY %s ASC
	`, keyColumn, table, keyColumn)
	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", table, err)
	}
	defer rows.Close()

	entries := make([]RuntimeStateEntry, 0)
	for rows.Next() {
		var key string
		var payloadJSON string
		var updatedAt int64
		if err := rows.Scan(&key, &payloadJSON, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan %s: %w", table, err)
		}
		entries = append(entries, RuntimeStateEntry{
			Namespace:       namespace,
			Key:             key,
			Value:           json.RawMessage(payloadJSON),
			UpdatedAtUnixMS: updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", table, err)
	}
	return entries, nil
}

func scanStructuredJSONRow(row *sql.Row, namespace, key string, dst any) (bool, error) {
	var payloadJSON string
	err := row.Scan(&payloadJSON)
	switch err {
	case nil:
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, fmt.Errorf("query %s %s: %w", namespace, key, err)
	}
	if err := json.Unmarshal([]byte(payloadJSON), dst); err != nil {
		return false, fmt.Errorf("decode %s %s: %w", namespace, key, err)
	}
	return true, nil
}

func parseRequeryStateKey(key string) (string, string, error) {
	idx := strings.LastIndex(key, ":")
	if idx <= 0 || idx == len(key)-1 {
		return "", "", fmt.Errorf("invalid requery runtime key %q", key)
	}
	configKey := key[:idx]
	stateKind := key[idx+1:]
	if stateKind != "config" && stateKind != "state" {
		return "", "", fmt.Errorf("unsupported requery runtime kind %q", stateKind)
	}
	return configKey, stateKind, nil
}

func composeRequeryStateKey(configKey, stateKind string) string {
	return configKey + ":" + stateKind
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
	return false, nil
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
	return false, nil
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
