package coremain

import (
	"encoding/json"
	"fmt"
)

type SystemEventEntry struct {
	ID              int64           `json:"id"`
	Component       string          `json:"component"`
	Level           string          `json:"level"`
	Message         string          `json:"message"`
	Details         json.RawMessage `json:"details"`
	CreatedAtUnixMS int64           `json:"created_at_unix_ms"`
}

func RecordSystemEvent(component, level, message string, details any) error {
	return RecordSystemEventToPath("", component, level, message, details)
}

func RecordSystemEventToPath(path, component, level, message string, details any) error {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return err
	}
	data, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal system event details: %w", err)
	}
	if _, err := store.db.DB().Exec(`
		INSERT INTO system_event (component, level, message, details_json, created_at_unix_ms)
		VALUES (?, ?, ?, ?, unixepoch('subsec') * 1000)
	`, component, level, message, string(data)); err != nil {
		return fmt.Errorf("insert system event: %w", err)
	}
	return nil
}

func ListSystemEvents(path, component string, limit int) ([]SystemEventEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT id, component, level, message, details_json, created_at_unix_ms
		FROM system_event
	`
	args := []any{}
	if component != "" {
		query += ` WHERE component = ?`
		args = append(args, component)
	}
	query += ` ORDER BY created_at_unix_ms DESC LIMIT ?`
	args = append(args, limit)

	rows, err := store.db.DB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query system events: %w", err)
	}
	defer rows.Close()

	events := make([]SystemEventEntry, 0, limit)
	for rows.Next() {
		var event SystemEventEntry
		var raw string
		if err := rows.Scan(&event.ID, &event.Component, &event.Level, &event.Message, &raw, &event.CreatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scan system event: %w", err)
		}
		event.Details = json.RawMessage(raw)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate system events: %w", err)
	}
	return events, nil
}
