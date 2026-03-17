package requery

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

func (p *Requery) beginTaskExecution(profile taskProfile, recovery *FullRebuildTask) bool {
	var runID string
	startedAt := time.Now().UTC()
	p.mu.Lock()
	prevStatus := p.status
	prevRunID := p.activeRunID
	prevTriggerSource := p.activeTriggerSource

	if p.status.TaskState == "running" {
		p.mu.Unlock()
		log.Println("[requery] Task trigger ignored: a task is already running.")
		return false
	}

	if recovery != nil && recovery.TaskID != "" {
		runID = recovery.TaskID
		if !recovery.StartedAt.IsZero() {
			startedAt = recovery.StartedAt.UTC()
		}
	} else {
		runID = generateRunID()
	}

	p.activeRunID = runID
	p.status.TaskState = "running"
	p.status.ActiveRunID = runID
	p.status.TaskMode = profile.Mode
	p.status.TaskStage = ""
	p.status.TaskStageLabel = ""
	p.status.TaskStageProcessed = 0
	p.status.TaskStageTotal = 0
	p.status.LastRunStartTime = startedAt
	p.status.LastRunEndTime = time.Time{}
	p.status.Progress.Processed = 0
	p.status.Progress.Total = 0
	p.lastError = ""
	if err := p.saveStateUnlocked(); err != nil {
		if p.taskCancel != nil {
			p.taskCancel()
		}
		p.status = prevStatus
		p.activeRunID = prevRunID
		p.activeTriggerSource = prevTriggerSource
		p.status.ActiveRunID = prevRunID
		p.taskCtx = nil
		p.taskCancel = nil
		p.lastError = fmt.Sprintf("failed to persist task start state: %v", err)
		p.mu.Unlock()
		log.Printf("[requery] WARN: failed to persist task start state: %v", err)
		return false
	}
	p.mu.Unlock()

	if err := p.persistRunSnapshot("running", time.Time{}); err != nil {
		log.Printf("[requery] WARN: failed to persist run snapshot on start: %v", err)
	}
	return true
}

func (p *Requery) finishTaskExecution() {
	endedAt := time.Now().UTC()
	var finalState string
	var activeRunID string
	p.mu.Lock()

	if p.status.TaskState == "running" {
		p.status.TaskState = "idle"
	}

	if r := recover(); r != nil {
		log.Printf("[requery] FATAL: Task panicked: %v", r)
		p.status.TaskState = "failed"
		p.lastError = fmt.Sprintf("task panicked: %v", r)
	}

	p.status.LastRunEndTime = time.Now().UTC()
	if p.status.TaskMode != "" {
		p.status.LastRunMode = p.status.TaskMode
	}
	p.status.TaskStage = ""
	p.status.TaskStageLabel = ""
	p.status.TaskStageProcessed = 0
	p.status.TaskStageTotal = 0
	activeRunID = p.activeRunID
	finalState = p.status.TaskState
	p.activeRunID = ""
	p.activeTriggerSource = ""
	p.status.ActiveRunID = ""
	if err := p.saveStateUnlocked(); err != nil {
		log.Printf("[requery] WARN: failed to persist final task state: %v", err)
	}
	p.taskCancel = nil
	p.mu.Unlock()

	if activeRunID != "" {
		if err := p.persistRunSnapshotWithID(activeRunID, finalState, endedAt); err != nil {
			log.Printf("[requery] WARN: failed to persist run snapshot on finish: %v", err)
		}
	}

	log.Println("[requery] Task finished, triggering background memory release...")
	coremain.ManualGC()
}

func (p *Requery) logTaskStart(profile taskProfile, recovery *FullRebuildTask) {
	if recovery != nil {
		log.Printf("[requery] Resuming task: %s.", profile.Mode)
		return
	}
	log.Printf("[requery] Starting a new task: %s.", profile.Mode)
}

func (p *Requery) prepareTaskExecutionState(ctx context.Context, profile taskProfile, recovery *FullRebuildTask) (taskExecutionState, bool) {
	p.logTaskStart(profile, recovery)
	if !p.saveRulesBeforeRun(ctx, profile, recovery) {
		return taskExecutionState{}, false
	}

	plan, ok := p.prepareTaskPlan(ctx, profile, recovery)
	if !ok {
		return taskExecutionState{}, false
	}
	if !p.flushRulesBeforeRun(ctx, profile, recovery) {
		return taskExecutionState{}, false
	}

	p.syncTaskProgress(plan, recovery)
	recovery, ok = p.prepareRecoveryTask(plan, recovery, profile)
	if !ok {
		return taskExecutionState{}, false
	}
	return taskExecutionState{
		plan:     plan,
		recovery: recovery,
	}, true
}

func (p *Requery) saveRulesBeforeRun(ctx context.Context, profile taskProfile, recovery *FullRebuildTask) bool {
	if recovery != nil || !profile.SaveBefore {
		return true
	}
	log.Println("[requery] Step 1: Saving current rule state...")
	if result := p.callURLs(ctx, "save_rules", p.config.URLActions.SaveRules); result.Failed > 0 {
		p.setFailedState("failed during save_rules step: %d/%d targets failed", result.Failed, result.Total)
		return false
	}
	return true
}

func (p *Requery) prepareTaskPlan(ctx context.Context, profile taskProfile, recovery *FullRebuildTask) (taskCandidatePlan, bool) {
	var (
		plan taskCandidatePlan
		err  error
	)
	if recovery != nil {
		plan = planFromRecovery(recovery)
	} else {
		log.Println("[requery] Step 2 & 3: Collecting candidate domains...")
		plan, err = p.buildTaskCandidatePlan(ctx, profile)
		if err != nil {
			p.setFailedState("failed during domain merge: %v", err)
			return taskCandidatePlan{}, false
		}
	}

	totalDomains := len(plan.Primary) + len(plan.Secondary)
	if totalDomains == 0 && !(recovery != nil && recovery.Stage == "publish") {
		log.Println("[requery] No domains found to process. Task finished.")
		p.clearFullRebuildTask()
		return taskCandidatePlan{}, false
	}
	return plan, true
}

func (p *Requery) flushRulesBeforeRun(ctx context.Context, profile taskProfile, recovery *FullRebuildTask) bool {
	if recovery != nil || !profile.FlushBefore {
		return true
	}
	log.Println("[requery] Step 4: Flushing old rules (legacy mode)...")
	if result := p.callURLs(ctx, "flush_rules", p.config.URLActions.FlushRules); result.Failed > 0 {
		p.setFailedState("failed during flush_rules step: %d/%d targets failed", result.Failed, result.Total)
		return false
	}
	return true
}

func (p *Requery) syncTaskProgress(plan taskCandidatePlan, recovery *FullRebuildTask) {
	if recovery != nil {
		p.mu.Lock()
		p.status.LastRunDomainCount = recovery.Total
		p.status.Progress.Total = int64(recovery.Total)
		p.status.Progress.Processed = int64(recovery.Completed)
		p.status.ActiveRunID = recovery.TaskID
		p.activeRunID = recovery.TaskID
		p.status.TaskStage = recovery.Stage
		p.status.TaskStageLabel = recovery.StageLabel
		p.status.TaskStageProcessed = stageProcessed(recovery)
		p.status.TaskStageTotal = stageTotal(recovery)
		p.lastError = ""
		if err := p.saveStateUnlocked(); err != nil {
			log.Printf("[requery] WARN: failed to persist recovery state: %v", err)
		}
		p.mu.Unlock()
		if err := p.persistRunSnapshot("running", time.Time{}); err != nil {
			log.Printf("[requery] WARN: failed to persist recovery run snapshot: %v", err)
		}
		return
	}

	totalDomains := len(plan.Primary) + len(plan.Secondary)
	p.mu.Lock()
	p.status.LastRunDomainCount = totalDomains
	p.status.Progress.Total = int64(totalDomains)
	if err := p.saveStateUnlocked(); err != nil {
		log.Printf("[requery] WARN: failed to persist task progress state: %v", err)
	}
	p.mu.Unlock()
}

func (p *Requery) prepareRecoveryTask(plan taskCandidatePlan, recovery *FullRebuildTask, profile taskProfile) (*FullRebuildTask, bool) {
	if profile.Mode != "full_rebuild" {
		return recovery, true
	}
	if recovery == nil {
		recovery = newFullRebuildTask(plan)
		p.mu.Lock()
		if p.activeRunID != "" {
			recovery.TaskID = p.activeRunID
		} else {
			p.activeRunID = recovery.TaskID
		}
		p.activeRunID = recovery.TaskID
		p.status.ActiveRunID = recovery.TaskID
		p.mu.Unlock()
	} else {
		recovery = cloneFullRebuildTask(recovery)
		recovery.ResumeCount++
		recovery.UpdatedAt = time.Now().UTC()
	}
	if err := p.persistFullRebuildTask(recovery); err != nil {
		p.setFailedState("failed to persist full rebuild snapshot: %v", err)
		return nil, false
	}
	return recovery, true
}

func (p *Requery) executeTaskStages(ctx context.Context, profile taskProfile, state *taskExecutionState) bool {
	if !p.runTaskPlan(ctx, profile, &state.plan, state.recovery) {
		return false
	}
	return p.persistPublishStage(state.recovery)
}

func (p *Requery) runTaskPlan(ctx context.Context, profile taskProfile, plan *taskCandidatePlan, recovery *FullRebuildTask) bool {
	if !p.runTaskStage(ctx, profile, recovery, "priority", plan.Primary, &plan.Primary, &plan.Secondary) {
		return false
	}
	if !p.runTaskStage(ctx, profile, recovery, "tail", plan.Secondary, &plan.Primary, &plan.Secondary) {
		return false
	}
	return true
}

func (p *Requery) finalizeTaskExecution(ctx context.Context, profile taskProfile) bool {
	if !p.saveRulesAfterRun(ctx, profile) {
		return false
	}
	p.clearFullRebuildTask()
	return true
}

func (p *Requery) runTaskStage(ctx context.Context, profile taskProfile, recovery *FullRebuildTask, stage string, domains []domainCandidate, primaryRef *[]domainCandidate, secondaryRef *[]domainCandidate) bool {
	if len(domains) == 0 {
		return true
	}

	label := stageLabel(stage)
	p.setTaskStage(stage, label, int64(len(domains)))
	switch stage {
	case "priority":
		log.Printf("[requery] Step 6.1: %s %s，处理 %d 个域名...", profile.ProgressMode, label, len(domains))
	case "tail":
		log.Printf("[requery] Step 6.2: %s %s，处理 %d 个域名...", profile.ProgressMode, label, len(domains))
	default:
		log.Printf("[requery] Step 6: %s %s，处理 %d 个域名...", profile.ProgressMode, label, len(domains))
	}

	if recovery != nil {
		recovery.Stage = stage
		recovery.StageLabel = label
		recovery.UpdatedAt = time.Now().UTC()
		if err := p.persistFullRebuildTask(recovery); err != nil {
			p.setFailedState("failed to persist %s stage snapshot: %v", stage, err)
			return false
		}
	}

	if err := p.runStageWithCheckpoint(ctx, profile, recovery, stage, domains, primaryRef, secondaryRef); err != nil {
		switch stage {
		case "priority":
			log.Printf("[requery] Task stopped during high-priority query phase: %v", err)
		case "tail":
			log.Printf("[requery] Task stopped during long-tail query phase: %v", err)
		default:
			log.Printf("[requery] Task stopped during %s phase: %v", stage, err)
		}
		return false
	}
	return true
}

func (p *Requery) persistPublishStage(recovery *FullRebuildTask) bool {
	if recovery == nil {
		return true
	}
	recovery.Stage = "publish"
	recovery.StageLabel = stageLabel(recovery.Stage)
	recovery.Primary = nil
	recovery.Secondary = nil
	recovery.UpdatedAt = time.Now().UTC()
	if err := p.persistFullRebuildTask(recovery); err != nil {
		p.setFailedState("failed to persist publish stage snapshot: %v", err)
		return false
	}
	return true
}

func (p *Requery) saveRulesAfterRun(ctx context.Context, profile taskProfile) bool {
	if !profile.SaveAfter {
		return true
	}
	log.Println("[requery] Step 7: Publishing refreshed rule state...")
	if result := p.callURLs(ctx, "save_rules", p.config.URLActions.SaveRules); result.Failed > 0 {
		p.setFailedState("failed during final save_rules step: %d/%d targets failed", result.Failed, result.Total)
		return false
	}
	return true
}
