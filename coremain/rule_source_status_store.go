package coremain

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
)

type RuleSourceStatus struct {
	Scope           string    `json:"scope"`
	SourceID        string    `json:"source_id"`
	RuleCount       int       `json:"rule_count"`
	LastUpdated     time.Time `json:"last_updated"`
	LastError       string    `json:"last_error"`
	UpdatedAtUnixMS int64     `json:"updated_at_unix_ms"`
}

func ListRuleSourceStatusByScope(dbPath string, scope rulesource.Scope) (map[string]RuleSourceStatus, error) {
	store, err := getRuntimeStateStoreByPath(dbPath)
	if err != nil {
		return nil, err
	}
	rows, err := store.db.DB().Query(`
		SELECT source_id, rule_count, last_updated_unix_ms, last_error, updated_at_unix_ms
		FROM rule_source_status
		WHERE scope = ?
	`, string(scope))
	if err != nil {
		return nil, fmt.Errorf("query rule_source_status: %w", err)
	}
	defer rows.Close()

	values := make(map[string]RuleSourceStatus)
	for rows.Next() {
		status, err := scanRuleSourceStatusRow(rows, scope)
		if err != nil {
			return nil, err
		}
		values[status.SourceID] = status
	}
	return values, rows.Err()
}

func SaveRuleSourceStatus(dbPath string, status RuleSourceStatus) error {
	store, err := getRuntimeStateStoreByPath(dbPath)
	if err != nil {
		return err
	}
	lastUpdated := int64(0)
	if !status.LastUpdated.IsZero() {
		lastUpdated = status.LastUpdated.UnixMilli()
	}
	_, err = store.db.DB().Exec(`
		INSERT INTO rule_source_status (
			scope, source_id, rule_count, last_updated_unix_ms, last_error, updated_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, unixepoch('subsec') * 1000)
		ON CONFLICT(scope, source_id) DO UPDATE SET
			rule_count = excluded.rule_count,
			last_updated_unix_ms = excluded.last_updated_unix_ms,
			last_error = excluded.last_error,
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`, status.Scope, status.SourceID, status.RuleCount, lastUpdated, status.LastError)
	if err != nil {
		return fmt.Errorf("save rule_source_status %s/%s: %w", status.Scope, status.SourceID, err)
	}
	return nil
}

func DeleteRuleSourceStatus(dbPath string, scope rulesource.Scope, sourceID string) error {
	store, err := getRuntimeStateStoreByPath(dbPath)
	if err != nil {
		return err
	}
	_, err = store.db.DB().Exec(`DELETE FROM rule_source_status WHERE scope = ? AND source_id = ?`, string(scope), sourceID)
	if err != nil {
		return fmt.Errorf("delete rule_source_status %s/%s: %w", scope, sourceID, err)
	}
	return nil
}

func PruneRuleSourceStatus(dbPath string, scope rulesource.Scope, keepIDs map[string]struct{}) error {
	store, err := getRuntimeStateStoreByPath(dbPath)
	if err != nil {
		return err
	}
	rows, err := store.db.DB().Query(`SELECT source_id FROM rule_source_status WHERE scope = ?`, string(scope))
	if err != nil {
		return fmt.Errorf("query stale rule_source_status: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var sourceID string
		if err := rows.Scan(&sourceID); err != nil {
			return err
		}
		if _, ok := keepIDs[sourceID]; ok {
			continue
		}
		if err := DeleteRuleSourceStatus(dbPath, scope, sourceID); err != nil {
			return err
		}
	}
	return rows.Err()
}

func scanRuleSourceStatusRow(rows *sql.Rows, scope rulesource.Scope) (RuleSourceStatus, error) {
	var sourceID string
	var ruleCount int
	var lastUpdatedUnixMS int64
	var lastError string
	var updatedAt int64
	if err := rows.Scan(&sourceID, &ruleCount, &lastUpdatedUnixMS, &lastError, &updatedAt); err != nil {
		return RuleSourceStatus{}, fmt.Errorf("scan rule_source_status: %w", err)
	}
	status := RuleSourceStatus{
		Scope:           string(scope),
		SourceID:        sourceID,
		RuleCount:       ruleCount,
		LastError:       lastError,
		UpdatedAtUnixMS: updatedAt,
	}
	if lastUpdatedUnixMS > 0 {
		status.LastUpdated = time.UnixMilli(lastUpdatedUnixMS)
	}
	return status, nil
}
