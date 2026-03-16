package requery

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IrineSistiana/mosdns/v5/internal/requeryruntime"
)

const defaultRequeryRunHistoryLimit = 20

func generateRunID() string {
	return strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
}

func (p *Requery) runtimeJobKey(mode, trigger string) string {
	return filepath.Clean(p.filePath) + "::" + strings.ToLower(mode) + "::" + strings.ToLower(trigger)
}

func (p *Requery) syncRuntimeJobsLocked() error {
	if p.config == nil {
		return nil
	}

	manualJobs := []requeryruntime.Job{
		{
			JobID:         p.runtimeJobKey("quick_prewarm", "manual"),
			ConfigKey:     p.runtimeConfigKey(),
			Mode:          "quick_prewarm",
			TriggerSource: "manual",
			Enabled:       true,
			Definition:    marshalRuntimeJSON(p.runtimeJobDefinitionLocked("quick_prewarm", "manual")),
		},
		{
			JobID:         p.runtimeJobKey("quick_rebuild", "manual"),
			ConfigKey:     p.runtimeConfigKey(),
			Mode:          "quick_rebuild",
			TriggerSource: "manual",
			Enabled:       true,
			Definition:    marshalRuntimeJSON(p.runtimeJobDefinitionLocked("quick_rebuild", "manual")),
		},
		{
			JobID:         p.runtimeJobKey("full_rebuild", "manual"),
			ConfigKey:     p.runtimeConfigKey(),
			Mode:          "full_rebuild",
			TriggerSource: "manual",
			Enabled:       true,
			Definition:    marshalRuntimeJSON(p.runtimeJobDefinitionLocked("full_rebuild", "manual")),
		},
	}
	manualJobs = append(manualJobs, requeryruntime.Job{
		JobID:         p.runtimeJobKey("quick_rebuild", "scheduler"),
		ConfigKey:     p.runtimeConfigKey(),
		Mode:          "quick_rebuild",
		TriggerSource: "scheduler",
		Enabled:       p.allowsSweep(),
		Definition:    marshalRuntimeJSON(p.runtimeJobDefinitionLocked("quick_rebuild", "scheduler")),
	})

	return requeryruntime.ReplaceJobs(p.runtimeDBPath(), p.runtimeConfigKey(), manualJobs)
}

func (p *Requery) runtimeJobDefinitionLocked(mode, trigger string) map[string]any {
	profile := p.profileForMode(mode, 0)
	return map[string]any{
		"mode":                  mode,
		"trigger_source":        trigger,
		"enabled":               mode != "quick_rebuild" || trigger != "scheduler" || p.allowsSweep(),
		"display_name":          profile.DisplayName,
		"progress_mode":         profile.ProgressMode,
		"qps":                   profile.QPS,
		"limit":                 profile.Limit,
		"scheduler_enabled":     p.config.Scheduler.Enabled,
		"scheduler_start":       p.config.Scheduler.StartDatetime,
		"interval_minutes":      p.config.Scheduler.IntervalMinutes,
		"query_mode":            p.config.ExecutionSettings.QueryMode,
		"date_range_days":       p.config.ExecutionSettings.DateRangeDays,
		"refresh_resolver_pool": len(p.config.ExecutionSettings.RefreshResolverPool),
	}
}

func (p *Requery) persistRunSnapshot(state string, endedAt time.Time) error {
	return p.persistRunSnapshotWithID("", state, endedAt)
}

func (p *Requery) persistRunSnapshotWithID(runID, state string, endedAt time.Time) error {
	p.mu.RLock()
	run := p.activeRunRecordLocked(runID, state, endedAt)
	p.mu.RUnlock()
	if run == nil {
		return nil
	}
	return requeryruntime.SaveRun(p.runtimeDBPath(), *run)
}

func (p *Requery) activeRunRecordLocked(runID, state string, endedAt time.Time) *requeryruntime.Run {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		runID = strings.TrimSpace(p.activeRunID)
	}
	if runID == "" && p.fullTask != nil {
		runID = strings.TrimSpace(p.fullTask.TaskID)
	}
	if runID == "" {
		runID = strings.TrimSpace(p.status.ActiveRunID)
	}
	if runID == "" {
		return nil
	}

	taskState := strings.TrimSpace(state)
	if taskState == "" {
		taskState = strings.TrimSpace(p.status.TaskState)
	}

	mode := strings.TrimSpace(p.status.TaskMode)
	if mode == "" && p.fullTask != nil {
		mode = strings.TrimSpace(p.fullTask.Mode)
	}
	if mode == "" {
		mode = "full_rebuild"
	}

	triggerSource := strings.TrimSpace(p.activeTriggerSource)
	if triggerSource == "" {
		triggerSource = "manual"
	}

	startedAtUnixMS := p.status.LastRunStartTime.UTC().UnixMilli()
	if startedAtUnixMS <= 0 {
		startedAtUnixMS = time.Now().UTC().UnixMilli()
	}

	metadata := map[string]any{
		"last_error":          p.lastError,
		"pending_queue":       p.status.PendingQueue,
		"on_demand_triggered": p.status.OnDemandTriggered,
		"on_demand_processed": p.status.OnDemandProcessed,
		"on_demand_skipped":   p.status.OnDemandSkipped,
		"max_queue_size":      p.maxQueueSize(),
	}
	if p.fullTask != nil {
		metadata["resume_count"] = p.fullTask.ResumeCount
	}

	record := &requeryruntime.Run{
		RunID:           runID,
		ConfigKey:       p.runtimeConfigKey(),
		JobID:           p.runtimeJobKey(mode, triggerSource),
		Mode:            mode,
		TriggerSource:   triggerSource,
		State:           taskState,
		Stage:           p.status.TaskStage,
		StageLabel:      p.status.TaskStageLabel,
		Total:           int(p.status.Progress.Total),
		Completed:       int(p.status.Progress.Processed),
		ErrorText:       p.lastError,
		Metadata:        marshalRuntimeJSON(metadata),
		StartedAtUnixMS: startedAtUnixMS,
		UpdatedAtUnixMS: time.Now().UTC().UnixMilli(),
	}
	if !endedAt.IsZero() {
		record.EndedAtUnixMS = endedAt.UTC().UnixMilli()
	}
	return record
}

func (p *Requery) persistCheckpoint(task *FullRebuildTask) error {
	if task == nil {
		return nil
	}
	snapshot := cloneFullRebuildTask(task)
	return requeryruntime.SaveCheckpoint(p.runtimeDBPath(), requeryruntime.Checkpoint{
		ConfigKey: p.runtimeConfigKey(),
		RunID:     snapshot.TaskID,
		Stage:     snapshot.Stage,
		Completed: snapshot.Completed,
		Total:     snapshot.Total,
		Snapshot:  marshalRuntimeJSON(snapshot),
	})
}

func (p *Requery) listRuntimeJobs() ([]requeryruntime.Job, error) {
	return requeryruntime.ListJobs(p.runtimeDBPath(), p.runtimeConfigKey())
}

func (p *Requery) listRuntimeRuns(limit int) ([]requeryruntime.Run, error) {
	if limit <= 0 {
		limit = defaultRequeryRunHistoryLimit
	}
	return requeryruntime.ListRuns(p.runtimeDBPath(), p.runtimeConfigKey(), limit)
}

func (p *Requery) listRuntimeCheckpoints(runID string, limit int) ([]requeryruntime.Checkpoint, error) {
	if limit <= 0 {
		limit = defaultRequeryRunHistoryLimit
	}
	return requeryruntime.ListCheckpoints(p.runtimeDBPath(), p.runtimeConfigKey(), runID, limit)
}

func marshalRuntimeJSON(v any) json.RawMessage {
	if v == nil {
		return json.RawMessage(`{}`)
	}
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(fmt.Sprintf(`{"error":%q}`, err.Error()))
	}
	return json.RawMessage(data)
}
