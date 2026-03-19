package coremain

import (
	"database/sql"
	"fmt"
	"strings"
)

type UpstreamRuntimeStats struct {
	PluginTag       string `json:"plugin_tag"`
	UpstreamTag     string `json:"upstream_tag"`
	QueryTotal      uint64 `json:"query_total"`
	ErrorTotal      uint64 `json:"error_total"`
	WinnerTotal     uint64 `json:"winner_total"`
	LatencyTotalUs  uint64 `json:"latency_total_us"`
	LatencyCount    uint64 `json:"latency_count"`
	UpdatedAtUnixMS int64  `json:"updated_at_unix_ms"`
}

func LoadUpstreamRuntimeStatsByPlugin(path, pluginTag string) (map[string]UpstreamRuntimeStats, error) {
	pluginTag = strings.TrimSpace(pluginTag)
	if pluginTag == "" {
		return map[string]UpstreamRuntimeStats{}, nil
	}
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return nil, err
	}
	rows, err := store.db.DB().Query(`
		SELECT plugin_tag, upstream_tag, query_total, error_total, winner_total,
		       latency_total_us, latency_count, updated_at_unix_ms
		FROM upstream_runtime_stats
		WHERE plugin_tag = ?
		ORDER BY upstream_tag ASC
	`, pluginTag)
	if err != nil {
		return nil, fmt.Errorf("query upstream_runtime_stats for %s: %w", pluginTag, err)
	}
	defer rows.Close()

	values := make(map[string]UpstreamRuntimeStats)
	for rows.Next() {
		item, err := scanUpstreamRuntimeStatsRow(rows)
		if err != nil {
			return nil, err
		}
		values[item.UpstreamTag] = item
	}
	return values, rows.Err()
}

func SaveUpstreamRuntimeStats(path string, stats []UpstreamRuntimeStats) error {
	filtered := filterUpstreamRuntimeStats(stats)
	if len(filtered) == 0 {
		return nil
	}
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return err
	}
	tx, err := store.db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin upstream_runtime_stats tx: %w", err)
	}
	if err := saveUpstreamRuntimeStatsTx(tx, filtered); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit upstream_runtime_stats tx: %w", err)
	}
	return nil
}

func ResetUpstreamRuntimeStats(path, pluginTag, upstreamTag string) (int, error) {
	store, err := getRuntimeStateStoreByPath(path)
	if err != nil {
		return 0, err
	}
	query, args, err := buildResetUpstreamRuntimeStatsQuery(pluginTag, upstreamTag)
	if err != nil {
		return 0, err
	}
	result, err := store.db.DB().Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("reset upstream_runtime_stats: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read upstream_runtime_stats rows affected: %w", err)
	}
	return int(rowsAffected), nil
}

func filterUpstreamRuntimeStats(stats []UpstreamRuntimeStats) []UpstreamRuntimeStats {
	filtered := make([]UpstreamRuntimeStats, 0, len(stats))
	for _, item := range stats {
		item.PluginTag = strings.TrimSpace(item.PluginTag)
		item.UpstreamTag = strings.TrimSpace(item.UpstreamTag)
		if item.PluginTag == "" || item.UpstreamTag == "" {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func saveUpstreamRuntimeStatsTx(tx *sql.Tx, stats []UpstreamRuntimeStats) error {
	stmt, err := tx.Prepare(`
		INSERT INTO upstream_runtime_stats (
			plugin_tag, upstream_tag, query_total, error_total, winner_total,
			latency_total_us, latency_count, updated_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, unixepoch('subsec') * 1000)
		ON CONFLICT(plugin_tag, upstream_tag) DO UPDATE SET
			query_total = excluded.query_total,
			error_total = excluded.error_total,
			winner_total = excluded.winner_total,
			latency_total_us = excluded.latency_total_us,
			latency_count = excluded.latency_count,
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`)
	if err != nil {
		return fmt.Errorf("prepare upstream_runtime_stats upsert: %w", err)
	}
	defer stmt.Close()

	for _, item := range stats {
		if _, err := stmt.Exec(
			item.PluginTag,
			item.UpstreamTag,
			item.QueryTotal,
			item.ErrorTotal,
			item.WinnerTotal,
			item.LatencyTotalUs,
			item.LatencyCount,
		); err != nil {
			return fmt.Errorf("save upstream_runtime_stats %s/%s: %w", item.PluginTag, item.UpstreamTag, err)
		}
	}
	return nil
}

func buildResetUpstreamRuntimeStatsQuery(pluginTag, upstreamTag string) (string, []any, error) {
	pluginTag = strings.TrimSpace(pluginTag)
	upstreamTag = strings.TrimSpace(upstreamTag)

	switch {
	case pluginTag == "" && upstreamTag == "":
		return `DELETE FROM upstream_runtime_stats`, nil, nil
	case pluginTag == "":
		return "", nil, fmt.Errorf("plugin_tag is required when upstream_tag is set")
	case upstreamTag == "":
		return `DELETE FROM upstream_runtime_stats WHERE plugin_tag = ?`, []any{pluginTag}, nil
	default:
		return `DELETE FROM upstream_runtime_stats WHERE plugin_tag = ? AND upstream_tag = ?`, []any{pluginTag, upstreamTag}, nil
	}
}

func scanUpstreamRuntimeStatsRow(rows *sql.Rows) (UpstreamRuntimeStats, error) {
	var item UpstreamRuntimeStats
	if err := rows.Scan(
		&item.PluginTag,
		&item.UpstreamTag,
		&item.QueryTotal,
		&item.ErrorTotal,
		&item.WinnerTotal,
		&item.LatencyTotalUs,
		&item.LatencyCount,
		&item.UpdatedAtUnixMS,
	); err != nil {
		return UpstreamRuntimeStats{}, fmt.Errorf("scan upstream_runtime_stats: %w", err)
	}
	return item, nil
}
