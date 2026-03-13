package requeryruntime

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	runtimesqlite "github.com/IrineSistiana/mosdns/v5/internal/store/sqlite"
)

type Job struct {
	JobID           string          `json:"job_id"`
	ConfigKey       string          `json:"config_key"`
	Mode            string          `json:"mode"`
	TriggerSource   string          `json:"trigger_source"`
	Enabled         bool            `json:"enabled"`
	Definition      json.RawMessage `json:"definition"`
	UpdatedAtUnixMS int64           `json:"updated_at_unix_ms"`
}

type Run struct {
	RunID           string          `json:"run_id"`
	ConfigKey       string          `json:"config_key"`
	JobID           string          `json:"job_id,omitempty"`
	Mode            string          `json:"mode"`
	TriggerSource   string          `json:"trigger_source"`
	State           string          `json:"state"`
	Stage           string          `json:"stage,omitempty"`
	StageLabel      string          `json:"stage_label,omitempty"`
	Total           int             `json:"total"`
	Completed       int             `json:"completed"`
	ErrorText       string          `json:"error_text,omitempty"`
	Metadata        json.RawMessage `json:"metadata,omitempty"`
	StartedAtUnixMS int64           `json:"started_at_unix_ms"`
	EndedAtUnixMS   int64           `json:"ended_at_unix_ms,omitempty"`
	UpdatedAtUnixMS int64           `json:"updated_at_unix_ms"`
}

type Checkpoint struct {
	ID              int64           `json:"id"`
	ConfigKey       string          `json:"config_key"`
	RunID           string          `json:"run_id"`
	Stage           string          `json:"stage"`
	Completed       int             `json:"completed"`
	Total           int             `json:"total"`
	Snapshot        json.RawMessage `json:"snapshot"`
	CreatedAtUnixMS int64           `json:"created_at_unix_ms"`
}

var globalStore struct {
	mu    sync.Mutex
	paths map[string]*runtimesqlite.RuntimeDB
}

func dbForPath(path string) (*runtimesqlite.RuntimeDB, error) {
	if path == "" {
		return nil, fmt.Errorf("runtime db path is required")
	}

	globalStore.mu.Lock()
	defer globalStore.mu.Unlock()

	if globalStore.paths == nil {
		globalStore.paths = make(map[string]*runtimesqlite.RuntimeDB)
	}
	if db := globalStore.paths[path]; db != nil {
		return db, nil
	}

	db, err := runtimesqlite.Open(path, nil)
	if err != nil {
		return nil, err
	}
	globalStore.paths[path] = db
	return db, nil
}

func ReplaceJobs(path, configKey string, jobs []Job) error {
	if configKey == "" {
		return fmt.Errorf("config key is required")
	}
	db, err := dbForPath(path)
	if err != nil {
		return err
	}
	tx, err := db.DB().Begin()
	if err != nil {
		return fmt.Errorf("begin replace jobs tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM requery_job WHERE config_key = ?`, configKey); err != nil {
		return fmt.Errorf("delete existing requery jobs: %w", err)
	}

	now := time.Now().UTC().UnixMilli()
	for _, job := range jobs {
		if job.JobID == "" {
			return fmt.Errorf("requery job id is required")
		}
		raw := string(normalizeJSON(job.Definition))
		if _, err = tx.Exec(`
			INSERT INTO requery_job (job_id, config_key, mode, trigger_source, enabled, definition_json, updated_at_unix_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, job.JobID, configKey, job.Mode, job.TriggerSource, boolToInt(job.Enabled), raw, now); err != nil {
			return fmt.Errorf("insert requery job %s: %w", job.JobID, err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit replace jobs tx: %w", err)
	}
	return nil
}

func ListJobs(path, configKey string) ([]Job, error) {
	db, err := dbForPath(path)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT job_id, config_key, mode, trigger_source, enabled, definition_json, updated_at_unix_ms
		FROM requery_job
	`
	args := []any{}
	if configKey != "" {
		query += ` WHERE config_key = ?`
		args = append(args, configKey)
	}
	query += ` ORDER BY updated_at_unix_ms DESC, job_id ASC`

	rows, err := db.DB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query requery jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var job Job
		var enabled int
		var definition string
		if err := rows.Scan(&job.JobID, &job.ConfigKey, &job.Mode, &job.TriggerSource, &enabled, &definition, &job.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scan requery job: %w", err)
		}
		job.Enabled = enabled != 0
		job.Definition = normalizeJSON(json.RawMessage(definition))
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate requery jobs: %w", err)
	}
	return jobs, nil
}

func SaveRun(path string, run Run) error {
	if run.RunID == "" {
		return fmt.Errorf("run id is required")
	}
	if run.ConfigKey == "" {
		return fmt.Errorf("config key is required")
	}
	db, err := dbForPath(path)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if run.StartedAtUnixMS == 0 {
		run.StartedAtUnixMS = now
	}
	if run.UpdatedAtUnixMS == 0 {
		run.UpdatedAtUnixMS = now
	}
	if run.Metadata == nil {
		run.Metadata = json.RawMessage(`{}`)
	}
	if _, err := db.DB().Exec(`
		INSERT INTO requery_run (
			run_id, config_key, job_id, mode, trigger_source, state, stage, stage_label,
			total, completed, error_text, metadata_json, started_at_unix_ms, ended_at_unix_ms, updated_at_unix_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET
			config_key = excluded.config_key,
			job_id = excluded.job_id,
			mode = excluded.mode,
			trigger_source = excluded.trigger_source,
			state = excluded.state,
			stage = excluded.stage,
			stage_label = excluded.stage_label,
			total = excluded.total,
			completed = excluded.completed,
			error_text = excluded.error_text,
			metadata_json = excluded.metadata_json,
			started_at_unix_ms = excluded.started_at_unix_ms,
			ended_at_unix_ms = excluded.ended_at_unix_ms,
			updated_at_unix_ms = excluded.updated_at_unix_ms
	`, run.RunID, run.ConfigKey, run.JobID, run.Mode, run.TriggerSource, run.State, run.Stage, run.StageLabel, run.Total, run.Completed, run.ErrorText, string(normalizeJSON(run.Metadata)), run.StartedAtUnixMS, run.EndedAtUnixMS, run.UpdatedAtUnixMS); err != nil {
		return fmt.Errorf("save requery run %s: %w", run.RunID, err)
	}
	return nil
}

func ListRuns(path, configKey string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	db, err := dbForPath(path)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT run_id, config_key, job_id, mode, trigger_source, state, stage, stage_label,
			total, completed, error_text, metadata_json, started_at_unix_ms, ended_at_unix_ms, updated_at_unix_ms
		FROM requery_run
	`
	args := []any{}
	if configKey != "" {
		query += ` WHERE config_key = ?`
		args = append(args, configKey)
	}
	query += ` ORDER BY updated_at_unix_ms DESC, run_id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.DB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query requery runs: %w", err)
	}
	defer rows.Close()

	runs := make([]Run, 0, limit)
	for rows.Next() {
		var run Run
		var metadata string
		if err := rows.Scan(&run.RunID, &run.ConfigKey, &run.JobID, &run.Mode, &run.TriggerSource, &run.State, &run.Stage, &run.StageLabel, &run.Total, &run.Completed, &run.ErrorText, &metadata, &run.StartedAtUnixMS, &run.EndedAtUnixMS, &run.UpdatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scan requery run: %w", err)
		}
		run.Metadata = normalizeJSON(json.RawMessage(metadata))
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate requery runs: %w", err)
	}
	return runs, nil
}

func SaveCheckpoint(path string, checkpoint Checkpoint) error {
	if checkpoint.ConfigKey == "" {
		return fmt.Errorf("config key is required")
	}
	if checkpoint.RunID == "" {
		return fmt.Errorf("run id is required")
	}
	db, err := dbForPath(path)
	if err != nil {
		return err
	}
	now := time.Now().UTC().UnixMilli()
	if checkpoint.CreatedAtUnixMS == 0 {
		checkpoint.CreatedAtUnixMS = now
	}
	result, err := db.DB().Exec(`
		INSERT INTO requery_checkpoint (config_key, run_id, stage, completed, total, snapshot_json, created_at_unix_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, checkpoint.ConfigKey, checkpoint.RunID, checkpoint.Stage, checkpoint.Completed, checkpoint.Total, string(normalizeJSON(checkpoint.Snapshot)), checkpoint.CreatedAtUnixMS)
	if err != nil {
		return fmt.Errorf("insert requery checkpoint for %s: %w", checkpoint.RunID, err)
	}
	if id, err := result.LastInsertId(); err == nil {
		checkpoint.ID = id
	}
	return nil
}

func ListCheckpoints(path, configKey, runID string, limit int) ([]Checkpoint, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	db, err := dbForPath(path)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT id, config_key, run_id, stage, completed, total, snapshot_json, created_at_unix_ms
		FROM requery_checkpoint
	`
	var conditions []string
	args := make([]any, 0, 3)
	if configKey != "" {
		conditions = append(conditions, "config_key = ?")
		args = append(args, configKey)
	}
	if runID != "" {
		conditions = append(conditions, "run_id = ?")
		args = append(args, runID)
	}
	if len(conditions) > 0 {
		query += " WHERE " + joinConditions(conditions)
	}
	query += ` ORDER BY created_at_unix_ms DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.DB().Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query requery checkpoints: %w", err)
	}
	defer rows.Close()

	checkpoints := make([]Checkpoint, 0, limit)
	for rows.Next() {
		var checkpoint Checkpoint
		var snapshot string
		if err := rows.Scan(&checkpoint.ID, &checkpoint.ConfigKey, &checkpoint.RunID, &checkpoint.Stage, &checkpoint.Completed, &checkpoint.Total, &snapshot, &checkpoint.CreatedAtUnixMS); err != nil {
			return nil, fmt.Errorf("scan requery checkpoint: %w", err)
		}
		checkpoint.Snapshot = normalizeJSON(json.RawMessage(snapshot))
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate requery checkpoints: %w", err)
	}
	return checkpoints, nil
}

func GetLatestCheckpoint(path, configKey, runID string) (*Checkpoint, error) {
	items, err := ListCheckpoints(path, configKey, runID, 1)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

func normalizeJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), raw...)
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func joinConditions(conditions []string) string {
	switch len(conditions) {
	case 0:
		return ""
	case 1:
		return conditions[0]
	}
	out := conditions[0]
	for i := 1; i < len(conditions); i++ {
		out += " AND " + conditions[i]
	}
	return out
}

func ResetForTesting(path string) error {
	globalStore.mu.Lock()
	db := globalStore.paths[path]
	delete(globalStore.paths, path)
	globalStore.mu.Unlock()

	if db != nil {
		if err := db.Close(); err != nil {
			return err
		}
	}
	return nil
}

func LoadRun(path, runID string) (*Run, error) {
	db, err := dbForPath(path)
	if err != nil {
		return nil, err
	}
	row := db.DB().QueryRow(`
		SELECT run_id, config_key, job_id, mode, trigger_source, state, stage, stage_label,
			total, completed, error_text, metadata_json, started_at_unix_ms, ended_at_unix_ms, updated_at_unix_ms
		FROM requery_run WHERE run_id = ?
	`, runID)

	var run Run
	var metadata string
	err = row.Scan(&run.RunID, &run.ConfigKey, &run.JobID, &run.Mode, &run.TriggerSource, &run.State, &run.Stage, &run.StageLabel, &run.Total, &run.Completed, &run.ErrorText, &metadata, &run.StartedAtUnixMS, &run.EndedAtUnixMS, &run.UpdatedAtUnixMS)
	switch err {
	case nil:
		run.Metadata = normalizeJSON(json.RawMessage(metadata))
		return &run, nil
	case sql.ErrNoRows:
		return nil, nil
	default:
		return nil, fmt.Errorf("load requery run %s: %w", runID, err)
	}
}
