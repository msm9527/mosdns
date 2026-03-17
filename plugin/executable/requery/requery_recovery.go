package requery

import (
	"log"
	"strconv"
	"time"
)

func newFullRebuildTask(plan taskCandidatePlan) *FullRebuildTask {
	total := len(plan.Primary) + len(plan.Secondary)
	return &FullRebuildTask{
		TaskID:     strconv.FormatInt(time.Now().UTC().UnixNano(), 10),
		Mode:       "full_rebuild",
		Stage:      "priority",
		StageLabel: stageLabel("priority"),
		StartedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		Total:      total,
		Primary:    cloneDomainCandidates(plan.Primary),
		Secondary:  cloneDomainCandidates(plan.Secondary),
	}
}

func planFromRecovery(task *FullRebuildTask) taskCandidatePlan {
	if task == nil {
		return taskCandidatePlan{}
	}
	return taskCandidatePlan{
		Primary:   cloneDomainCandidates(task.Primary),
		Secondary: cloneDomainCandidates(task.Secondary),
	}
}

func cloneDomainCandidates(in []domainCandidate) []domainCandidate {
	if len(in) == 0 {
		return nil
	}
	out := make([]domainCandidate, len(in))
	copy(out, in)
	return out
}

func cloneFullRebuildTask(task *FullRebuildTask) *FullRebuildTask {
	if task == nil {
		return nil
	}
	cp := *task
	cp.Primary = cloneDomainCandidates(task.Primary)
	cp.Secondary = cloneDomainCandidates(task.Secondary)
	return &cp
}

func stageLabel(stage string) string {
	switch stage {
	case "priority":
		return "高优先级阶段"
	case "tail":
		return "长尾补全阶段"
	case "publish":
		return "发布阶段"
	default:
		return "恢复阶段"
	}
}

func stageTotal(task *FullRebuildTask) int64 {
	if task == nil {
		return 0
	}
	switch task.Stage {
	case "priority":
		return int64(task.Completed + len(task.Primary))
	case "tail", "publish":
		return int64(task.Completed + len(task.Secondary))
	default:
		return int64(task.Total)
	}
}

func stageProcessed(task *FullRebuildTask) int64 {
	if task == nil {
		return 0
	}
	switch task.Stage {
	case "priority":
		return 0
	case "tail", "publish":
		return int64(task.Total - len(task.Secondary))
	default:
		return int64(task.Completed)
	}
}

func (p *Requery) checkpointBatchSize() int {
	if p.config.Recovery.CheckpointBatchSize > 0 {
		return p.config.Recovery.CheckpointBatchSize
	}
	return 256
}

func (p *Requery) autoResumeEnabled() bool {
	if p.config.Recovery.AutoResume != nil {
		return *p.config.Recovery.AutoResume
	}
	return true
}

func (p *Requery) resumeDelay() time.Duration {
	if p.config.Recovery.ResumeDelayMS > 0 {
		return time.Duration(p.config.Recovery.ResumeDelayMS) * time.Millisecond
	}
	return 1500 * time.Millisecond
}

func (p *Requery) persistFullRebuildTask(task *FullRebuildTask) error {
	p.mu.Lock()
	p.fullTask = cloneFullRebuildTask(task)
	p.activeRunID = task.TaskID
	p.status.ActiveRunID = task.TaskID
	err := p.saveStateUnlocked()
	p.mu.Unlock()
	if err != nil {
		return err
	}
	if err := p.persistRunSnapshot("running", time.Time{}); err != nil {
		return err
	}
	if err := p.persistCheckpoint(task); err != nil {
		return err
	}
	return nil
}

func (p *Requery) clearFullRebuildTask() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fullTask == nil {
		return
	}
	p.fullTask = nil
	if err := p.saveStateUnlocked(); err != nil {
		log.Printf("[requery] WARN: failed to clear full rebuild snapshot: %v", err)
	}
}

func (p *Requery) prepareRecoveryOnStartup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.config == nil {
		return
	}

	if task := p.fullTask; task != nil {
		if task.Mode == "" {
			task.Mode = "full_rebuild"
		}
		if task.StageLabel == "" {
			task.StageLabel = stageLabel(task.Stage)
		}
		p.status.TaskState = "failed"
		p.status.TaskMode = task.Mode
		p.status.TaskStage = task.Stage
		p.status.TaskStageLabel = task.StageLabel
		p.status.TaskStageProcessed = stageProcessed(task)
		p.status.TaskStageTotal = stageTotal(task)
		p.status.Progress.Total = int64(task.Total)
		p.status.Progress.Processed = int64(task.Completed)
		p.status.LastRunDomainCount = task.Total
		p.status.LastRunEndTime = time.Now().UTC()
		p.activeRunID = task.TaskID
		p.status.ActiveRunID = task.TaskID
		p.lastError = "检测到中断的完整重建任务，等待恢复。"
		_ = p.saveStateUnlocked()
		return
	}

	if p.status.TaskState == "running" {
		log.Println("[requery] WARN: Found task in 'running' state on startup. Marking as 'failed'.")
		p.status.TaskState = "failed"
		p.status.LastRunEndTime = time.Now().UTC()
		_ = p.saveStateUnlocked()
	}
}

func (p *Requery) scheduleRecoveryIfNeeded() {
	p.mu.RLock()
	task := cloneFullRebuildTask(p.fullTask)
	autoResume := p.autoResumeEnabled()
	delay := p.resumeDelay()
	p.mu.RUnlock()

	if task == nil || !autoResume {
		return
	}

	p.resumeOnce.Do(func() {
		time.AfterFunc(delay, func() {
			profile := p.profileForMode("full_rebuild", 0)
			if ok := p.startTaskWithRecovery(profile, task); !ok {
				log.Println("[requery] Resume skipped: another task is already running.")
			}
		})
	})
}
