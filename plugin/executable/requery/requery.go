package requery

import (
	"bufio"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/internal/requeryruntime"
	"github.com/go-chi/chi/v5"
	"github.com/miekg/dns"
	"github.com/robfig/cron/v3"
)

const (
	PluginType = "requery"
)

const (
	qtypeMaskA uint8 = 1 << iota
	qtypeMaskAAAA
)

// ----------------------------------------------------------------------------
// 1. Plugin Registration and Initialization
// ----------------------------------------------------------------------------

func init() {
	coremain.RegNewPluginFunc(PluginType, newRequery, func() any { return new(Args) })
}

// Args is the plugin's configuration arguments from the main YAML config.
type Args struct {
	Key string `yaml:"key"` // Logical runtime key stored in control.db
}

// newRequery is the plugin's initialization function.
func newRequery(bp *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)
	if strings.TrimSpace(cfg.Key) == "" {
		return nil, errors.New("requery: 'key' must be specified")
	}

	p := &Requery{
		plugin:     bp.Plugin,
		pluginTag:  bp.Tag(),
		baseDir:    bp.BaseDir(),
		runtimeKey: cfg.Key,
		dbPath:     bp.ControlDBPath(),
		scheduler:  cron.New(),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		queue:      make(refreshJobHeap, 0),
		queueIndex: make(map[string]struct{}),
		queueKick:  make(chan struct{}, 1),
	}
	heap.Init(&p.queue)

	if err := p.loadConfig(); err != nil {
		return nil, fmt.Errorf("requery: failed to load initial config for %s: %w", p.runtimeKey, err)
	}

	p.prepareRecoveryOnStartup()

	// Start the scheduler's goroutine once. It will run forever.
	p.scheduler.Start()
	log.Println("[requery] Scheduler started.")
	go p.runOnDemandLoop()

	// Now, add the initial job based on the loaded config.
	if err := p.setupScheduler(); err != nil {
		log.Printf("[requery] WARN: Failed to setup initial scheduler job, it will be disabled: %v", err)
	}
	p.scheduleRecoveryIfNeeded()

	bp.MountAPI("/api/v1/control/requery", p.api())

	log.Printf("[requery] plugin instance created for runtime key: %s", p.runtimeKey)
	return p, nil
}

// ----------------------------------------------------------------------------
// 2. Main Plugin Struct and Configuration Structs
// ----------------------------------------------------------------------------

// Requery is the main struct for the plugin.
type Requery struct {
	mu                  sync.RWMutex
	plugin              func(string) any
	pluginTag           string
	baseDir             string
	runtimeKey          string
	dbPath              string
	config              *Config
	status              Status
	fullTask            *FullRebuildTask
	lastError           string
	scheduler           *cron.Cron
	taskCtx             context.Context
	taskCancel          context.CancelFunc
	activeRunID         string
	activeTriggerSource string
	httpClient          *http.Client
	queue               refreshJobHeap
	queueIndex          map[string]struct{}
	queueKick           chan struct{}
	resumeOnce          sync.Once
}

// Config is the persisted requery runtime config stored in control.db.
type Config struct {
	DomainProcessing  DomainProcessing  `json:"domain_processing"`
	URLActions        URLActions        `json:"url_actions"`
	Workflow          WorkflowSettings  `json:"workflow"`
	Scheduler         SchedulerConfig   `json:"scheduler"`
	Recovery          RecoverySettings  `json:"recovery,omitempty"`
	ExecutionSettings ExecutionSettings `json:"execution_settings"`
	Status            Status            `json:"status"`
	FullRebuildTask   *FullRebuildTask  `json:"full_rebuild_task,omitempty"`
}

type DomainProcessing struct {
	SourceFiles []SourceFile `json:"source_files"`
	// OutputFile 已删除
}

type SourceFile struct {
	Alias string `json:"alias"`
	Path  string `json:"path"`
}

type URLActions struct {
	SaveRules  []string `json:"save_rules"`
	FlushRules []string `json:"flush_rules"`
}

type WorkflowSettings struct {
	FlushMode         string `json:"flush_mode"`
	Mode              string `json:"mode,omitempty"`
	SaveBeforeRefresh *bool  `json:"save_before_refresh,omitempty"`
	SaveAfterRefresh  *bool  `json:"save_after_refresh,omitempty"`
}

type SchedulerConfig struct {
	Enabled         bool   `json:"enabled"`
	StartDatetime   string `json:"start_datetime"` // ISO 8601 format
	IntervalMinutes int    `json:"interval_minutes"`
}

type RecoverySettings struct {
	AutoResume          *bool `json:"auto_resume,omitempty"`
	CheckpointBatchSize int   `json:"checkpoint_batch_size,omitempty"`
	ResumeDelayMS       int   `json:"resume_delay_ms,omitempty"`
}

type ExecutionSettings struct {
	QueriesPerSecond         int      `json:"queries_per_second"`
	QuickQueriesPerSecond    int      `json:"quick_queries_per_second,omitempty"`
	PrewarmQueriesPerSecond  int      `json:"prewarm_queries_per_second,omitempty"`
	ResolverAddress          string   `json:"resolver_address"`
	RefreshResolverAddress   string   `json:"refresh_resolver_address,omitempty"`
	RefreshResolverPool      []string `json:"refresh_resolver_pool,omitempty"`
	QueryMode                string   `json:"query_mode,omitempty"`
	URLCallDelayMS           int      `json:"url_call_delay_ms"`
	URLCallConcurrency       int      `json:"url_call_concurrency,omitempty"`
	DateRangeDays            int      `json:"date_range_days"` // 新增配置项：日期范围
	MaxQueueSize             int      `json:"max_queue_size,omitempty"`
	OnDemandBatchSize        int      `json:"on_demand_batch_size,omitempty"`
	QuickRebuildLimit        int      `json:"quick_rebuild_limit,omitempty"`
	PrewarmLimit             int      `json:"prewarm_limit,omitempty"`
	FullRebuildPriorityLimit int      `json:"full_rebuild_priority_limit,omitempty"`
}

type Status struct {
	TaskState          string    `json:"task_state"` // "idle", "running", "failed", "cancelled"
	ActiveRunID        string    `json:"active_run_id,omitempty"`
	TaskMode           string    `json:"task_mode,omitempty"`
	TaskStage          string    `json:"task_stage,omitempty"`
	TaskStageLabel     string    `json:"task_stage_label,omitempty"`
	TaskStageProcessed int64     `json:"task_stage_processed,omitempty"`
	TaskStageTotal     int64     `json:"task_stage_total,omitempty"`
	LastRunStartTime   time.Time `json:"last_run_start_time,omitempty"`
	LastRunEndTime     time.Time `json:"last_run_end_time,omitempty"`
	LastRunDomainCount int       `json:"last_run_domain_count"`
	LastRunMode        string    `json:"last_run_mode,omitempty"`
	Progress           Progress  `json:"progress"`
	PendingQueue       int       `json:"pending_queue"`
	OnDemandTriggered  int64     `json:"on_demand_triggered"`
	OnDemandProcessed  int64     `json:"on_demand_processed"`
	OnDemandSkipped    int64     `json:"on_demand_skipped"`
	LastOnDemandAt     time.Time `json:"last_on_demand_at,omitempty"`
	LastOnDemandDomain string    `json:"last_on_demand_domain,omitempty"`
}

type Progress struct {
	Processed int64 `json:"processed"`
	Total     int64 `json:"total"`
}

type FullRebuildTask struct {
	TaskID      string            `json:"task_id"`
	Mode        string            `json:"mode"`
	Stage       string            `json:"stage"`
	StageLabel  string            `json:"stage_label,omitempty"`
	StartedAt   time.Time         `json:"started_at,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at,omitempty"`
	Total       int               `json:"total"`
	Completed   int               `json:"completed"`
	Primary     []domainCandidate `json:"primary,omitempty"`
	Secondary   []domainCandidate `json:"secondary,omitempty"`
	LastError   string            `json:"last_error,omitempty"`
	ResumeCount int               `json:"resume_count,omitempty"`
}

type statusSnapshot struct {
	Status
	MaxQueueSize      int               `json:"max_queue_size"`
	LastError         string            `json:"last_error,omitempty"`
	QueuePreview      []refreshJobView  `json:"queue_preview,omitempty"`
	MemoryStats       []memoryStatView  `json:"memory_stats,omitempty"`
	BatchCapabilities ruleActionTargets `json:"batch_capabilities"`
}

type refreshJobView struct {
	Domain     string    `json:"domain"`
	MemoryID   string    `json:"memory_id,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	QTypeMask  uint8     `json:"qtype_mask,omitempty"`
	Priority   int       `json:"priority"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
}

type ruleActionTargets struct {
	SaveTargets  []string `json:"save_targets"`
	FlushTargets []string `json:"flush_targets"`
}

type memoryStatView struct {
	Key                 string `json:"key"`
	Name                string `json:"name"`
	Tag                 string `json:"tag"`
	MemoryID            string `json:"memory_id,omitempty"`
	Kind                string `json:"kind,omitempty"`
	TotalEntries        int    `json:"total_entries"`
	DirtyEntries        int    `json:"dirty_entries"`
	PromotedEntries     int64  `json:"promoted_entries"`
	PublishedRules      int64  `json:"published_rules"`
	TotalObservations   int64  `json:"total_observations"`
	DroppedObservations int64  `json:"dropped_observations"`
	DroppedByBuffer     int64  `json:"dropped_by_buffer"`
	DroppedByCap        int64  `json:"dropped_by_cap"`
	Error               string `json:"error,omitempty"`
}

type summaryResponse struct {
	Config      *Config              `json:"config"`
	Status      statusSnapshot       `json:"status"`
	MemoryStats []memoryStatView     `json:"memory_stats"`
	RuleTargets ruleActionTargets    `json:"rule_targets"`
	RecentRuns  []requeryruntime.Run `json:"recent_runs,omitempty"`
}

type batchActionItem struct {
	URL        string `json:"url"`
	Tag        string `json:"tag,omitempty"`
	OK         bool   `json:"ok"`
	StatusCode int    `json:"status_code,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type batchActionResult struct {
	Action     string            `json:"action"`
	Total      int               `json:"total"`
	Success    int               `json:"success"`
	Failed     int               `json:"failed"`
	DurationMS int64             `json:"duration_ms"`
	Items      []batchActionItem `json:"items"`
}

type triggerPayload struct {
	Mode  string `json:"mode,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type taskProfile struct {
	Mode           string
	DisplayName    string
	Limit          int
	ResolverAddr   string
	QPS            int
	SaveBefore     bool
	SaveAfter      bool
	FlushBefore    bool
	VerifyOnDemand bool
	ProgressMode   string
}

type taskCandidatePlan struct {
	Primary   []domainCandidate
	Secondary []domainCandidate
}

type taskExecutionState struct {
	plan     taskCandidatePlan
	recovery *FullRebuildTask
}

type domainCandidate struct {
	Name      string
	QTypeMask uint8
	Weight    int
}

type refreshJob struct {
	Domain     string    `json:"domain"`
	MemoryID   string    `json:"memory_id,omitempty"`
	QTypeMask  uint8     `json:"qtype_mask,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	VerifyTag  string    `json:"verify_tag,omitempty"`
	ObservedAt time.Time `json:"observed_at,omitempty"`
	Priority   int       `json:"-"`
}

type refreshJobHeap []refreshJob

func (h refreshJobHeap) Len() int { return len(h) }

func (h refreshJobHeap) Less(i, j int) bool {
	if h[i].Priority == h[j].Priority {
		if h[i].ObservedAt.Equal(h[j].ObservedAt) {
			return h[i].Domain < h[j].Domain
		}
		return h[i].ObservedAt.Before(h[j].ObservedAt)
	}
	return h[i].Priority > h[j].Priority
}

func (h refreshJobHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *refreshJobHeap) Push(x any) {
	*h = append(*h, x.(refreshJob))
}

func (h *refreshJobHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

func (j refreshJob) key() string {
	return strings.ToLower(j.MemoryID + "|" + j.Domain)
}

func priorityForReason(reason string) int {
	switch strings.ToLower(reason) {
	case "conflict", "error":
		return 100
	case "stale":
		return 80
	case "observed":
		return 60
	default:
		return 40
	}
}

// ----------------------------------------------------------------------------
// 3. Core Task Workflow
// ----------------------------------------------------------------------------

func (p *Requery) startTask(profile taskProfile) bool {
	return p.startTaskWithSource(profile, nil, "manual")
}

func (p *Requery) startTaskWithRecovery(profile taskProfile, recovery *FullRebuildTask) bool {
	source := "manual"
	if recovery != nil {
		source = "recovery"
	}
	return p.startTaskWithSource(profile, recovery, source)
}

func (p *Requery) startTaskWithSource(profile taskProfile, recovery *FullRebuildTask, triggerSource string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.status.TaskState == "running" {
		return false
	}
	p.activeTriggerSource = triggerSource
	p.taskCtx, p.taskCancel = context.WithCancel(context.Background())
	go p.runTask(p.taskCtx, profile, recovery)
	return true
}

// runTask executes the entire requery workflow. It's designed to be run in a goroutine.
func (p *Requery) runTask(ctx context.Context, profile taskProfile, recovery *FullRebuildTask) {
	if !p.beginTaskExecution(profile, recovery) {
		return
	}
	defer p.finishTaskExecution()

	state, ok := p.prepareTaskExecutionState(ctx, profile, recovery)
	if !ok {
		return
	}
	if !p.executeTaskStages(ctx, profile, &state) {
		return
	}
	if !p.finalizeTaskExecution(ctx, profile) {
		return
	}

	log.Println("[requery] Task completed successfully.")
}

func (p *Requery) setTaskStage(stage, label string, total int64) {
	p.mu.Lock()
	p.status.TaskStage = stage
	p.status.TaskStageLabel = label
	p.status.TaskStageProcessed = 0
	p.status.TaskStageTotal = total
	if err := p.saveStateUnlocked(); err != nil {
		log.Printf("[requery] WARN: failed to persist stage state: %v", err)
	}
	p.mu.Unlock()
	if err := p.persistRunSnapshot("running", time.Time{}); err != nil {
		log.Printf("[requery] WARN: failed to persist stage snapshot: %v", err)
	}
}

// mergeAndFilterDomains builds the effective candidate set for the task.
func (p *Requery) mergeAndFilterDomains(ctx context.Context, profile taskProfile) ([]domainCandidate, error) {
	plan, err := p.buildTaskCandidatePlan(ctx, profile)
	if err != nil {
		return nil, err
	}
	return append(append([]domainCandidate{}, plan.Primary...), plan.Secondary...), nil
}

func (p *Requery) buildTaskCandidatePlan(ctx context.Context, profile taskProfile) (taskCandidatePlan, error) {
	runtimeDomains, err := p.collectRuntimeCandidates(profile)
	if err != nil {
		return taskCandidatePlan{}, err
	}
	if profile.Mode != "full_rebuild" {
		if len(runtimeDomains) > 0 {
			return taskCandidatePlan{Primary: applyCandidateLimit(runtimeDomains, profile.Limit)}, nil
		}
		fallback, err := p.scanDomainsFromSourceFiles(ctx, profile.Limit)
		if err != nil {
			return taskCandidatePlan{}, err
		}
		return taskCandidatePlan{Primary: fallback}, nil
	}

	fileDomains, err := p.scanDomainsFromSourceFiles(ctx, 0)
	if err != nil {
		return taskCandidatePlan{}, err
	}
	merged := mergeDomainCandidates(runtimeDomains, fileDomains)
	merged = applyCandidateLimit(merged, profile.Limit)
	return splitCandidatePlan(runtimeDomains, merged, p.fullRebuildPriorityLimit()), nil
}

// scanDomainsFromSourceFiles handles reading source files, parsing formats, and filtering by date.
func (p *Requery) scanDomainsFromSourceFiles(ctx context.Context, limit int) ([]domainCandidate, error) {
	domainSet := make(map[string]domainCandidate)

	// 准备正则：匹配 full: 开头
	domainPattern := regexp.MustCompile(`^full:(.+)`)

	// 获取日期过滤配置，默认为 30 天
	limitDays := p.config.ExecutionSettings.DateRangeDays
	if limitDays <= 0 {
		limitDays = 30
	}

	processedCount := 0

	for _, sourceFile := range p.config.DomainProcessing.SourceFiles {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		file, err := os.Open(sourceFile.Path)
		if err != nil {
			if os.IsNotExist(err) {
				log.Printf("[requery] Source file not found, skipping: %s", sourceFile.Path)
				continue
			}
			return nil, fmt.Errorf("failed to open source file %s: %w", sourceFile.Path, err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			// 判断格式
			if strings.HasPrefix(line, "full:") {
				matches := domainPattern.FindStringSubmatch(line)
				if len(matches) > 1 {
					domain := strings.TrimSpace(matches[1])
					candidate := domainSet[domain]
					candidate.Name = domain
					if candidate.QTypeMask == 0 {
						candidate.QTypeMask = qtypeMaskA | qtypeMaskAAAA
					}
					domainSet[domain] = candidate
				}
			} else if len(line) > 0 && line[0] >= '0' && line[0] <= '9' {
				fields := strings.Fields(line)
				if len(fields) >= 3 {
					dateStr := fields[1]
					domain := fields[2]
					weight, _ := strconv.Atoi(fields[0])
					parsedTime, err := time.Parse("2006-01-02", dateStr)
					if err == nil {
						daysDiff := time.Since(parsedTime).Hours() / 24
						if daysDiff <= float64(limitDays) {
							candidate := domainSet[domain]
							candidate.Name = domain
							if weight > candidate.Weight {
								candidate.Weight = weight
							}
							candidate.QTypeMask |= parseQTypeMaskFromFields(fields[3:])
							if candidate.QTypeMask == 0 {
								candidate.QTypeMask = qtypeMaskA | qtypeMaskAAAA
							}
							domainSet[domain] = candidate
						}
					}
				}
			}
			processedCount++
		}

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("error reading source file %s: %w", sourceFile.Path, err)
		}
	}

	log.Printf("[requery] Processed source files. Total unique domains loaded (within %d days): %d.", limitDays, len(domainSet))

	if len(domainSet) == 0 {
		return []domainCandidate{}, nil
	}

	domains := make([]domainCandidate, 0, len(domainSet))
	for _, candidate := range domainSet {
		if candidate.QTypeMask == 0 {
			candidate.QTypeMask = qtypeMaskA | qtypeMaskAAAA
		}
		domains = append(domains, candidate)
	}
	sort.Slice(domains, func(i, j int) bool {
		if domains[i].Weight == domains[j].Weight {
			return domains[i].Name < domains[j].Name
		}
		return domains[i].Weight > domains[j].Weight
	})
	domains = applyCandidateLimit(domains, limit)
	domainSet = nil
	// 此时不再写入 output_file (requery_backup.txt)

	return domains, nil
}

func (p *Requery) collectRuntimeCandidates(profile taskProfile) ([]domainCandidate, error) {
	if p.plugin == nil {
		return nil, nil
	}

	req := coremain.DomainRefreshCandidateRequest{
		Mode: profile.Mode,
	}
	switch profile.Mode {
	case "quick_prewarm":
		req.IncludeHot = true
		req.Limit = profile.Limit
	case "quick_rebuild":
		req.IncludeDirty = true
		req.IncludeStale = true
		req.IncludeHot = true
		req.Limit = profile.Limit
	default:
		req.IncludeDirty = true
		req.IncludeStale = true
		req.IncludeHot = true
	}

	tags, err := p.memoryTargetTags()
	if err != nil {
		return nil, err
	}

	candidateSet := make(map[string]domainCandidate)
	for _, tag := range tags {
		provider, ok := p.plugin(tag).(coremain.DomainRefreshCandidateProvider)
		if !ok || provider == nil {
			continue
		}
		for _, item := range provider.SnapshotRefreshCandidates(req) {
			if strings.TrimSpace(item.Domain) == "" {
				continue
			}
			current := candidateSet[item.Domain]
			current.Name = item.Domain
			if item.Weight > current.Weight {
				current.Weight = item.Weight
			}
			current.QTypeMask |= item.QTypeMask
			if current.QTypeMask == 0 {
				current.QTypeMask = qtypeMaskA | qtypeMaskAAAA
			}
			candidateSet[item.Domain] = current
		}
	}

	if len(candidateSet) == 0 {
		return nil, nil
	}

	domains := make([]domainCandidate, 0, len(candidateSet))
	for _, candidate := range candidateSet {
		domains = append(domains, candidate)
	}
	sort.Slice(domains, func(i, j int) bool {
		if domains[i].Weight == domains[j].Weight {
			return domains[i].Name < domains[j].Name
		}
		return domains[i].Weight > domains[j].Weight
	})
	return applyCandidateLimit(domains, req.Limit), nil
}

func mergeDomainCandidates(primary []domainCandidate, secondary []domainCandidate) []domainCandidate {
	if len(primary) == 0 {
		return secondary
	}
	if len(secondary) == 0 {
		return primary
	}

	merged := make(map[string]domainCandidate, len(primary)+len(secondary))
	for _, candidate := range primary {
		if candidate.QTypeMask == 0 {
			candidate.QTypeMask = qtypeMaskA | qtypeMaskAAAA
		}
		merged[candidate.Name] = candidate
	}
	for _, candidate := range secondary {
		if current, ok := merged[candidate.Name]; ok {
			if candidate.Weight > current.Weight {
				current.Weight = candidate.Weight
			}
			current.QTypeMask |= candidate.QTypeMask
			if current.QTypeMask == 0 {
				current.QTypeMask = qtypeMaskA | qtypeMaskAAAA
			}
			merged[candidate.Name] = current
			continue
		}
		if candidate.QTypeMask == 0 {
			candidate.QTypeMask = qtypeMaskA | qtypeMaskAAAA
		}
		merged[candidate.Name] = candidate
	}

	items := make([]domainCandidate, 0, len(merged))
	for _, candidate := range merged {
		items = append(items, candidate)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Weight == items[j].Weight {
			return items[i].Name < items[j].Name
		}
		return items[i].Weight > items[j].Weight
	})
	return items
}

func applyCandidateLimit(domains []domainCandidate, limit int) []domainCandidate {
	if limit > 0 && len(domains) > limit {
		return domains[:limit]
	}
	return domains
}

func splitCandidatePlan(runtimeDomains []domainCandidate, merged []domainCandidate, priorityLimit int) taskCandidatePlan {
	if len(merged) == 0 {
		return taskCandidatePlan{}
	}

	runtimeSet := make(map[string]struct{}, len(runtimeDomains))
	for _, item := range runtimeDomains {
		runtimeSet[item.Name] = struct{}{}
	}

	primary := make([]domainCandidate, 0, len(merged))
	secondary := make([]domainCandidate, 0, len(merged))
	for _, item := range merged {
		if _, ok := runtimeSet[item.Name]; ok {
			primary = append(primary, item)
			continue
		}
		secondary = append(secondary, item)
	}

	if priorityLimit > 0 && len(primary) > priorityLimit {
		secondary = append(primary[priorityLimit:], secondary...)
		primary = primary[:priorityLimit]
	}
	return taskCandidatePlan{
		Primary:   primary,
		Secondary: secondary,
	}
}

func (p *Requery) runStageWithCheckpoint(ctx context.Context, profile taskProfile, recovery *FullRebuildTask, stage string, domains []domainCandidate, primaryRef *[]domainCandidate, secondaryRef *[]domainCandidate) error {
	if len(domains) == 0 {
		return nil
	}
	if recovery == nil {
		return p.resendDNSQueries(ctx, domains, true, profile)
	}

	batchSize := p.checkpointBatchSize()
	for start := 0; start < len(domains); start += batchSize {
		end := start + batchSize
		if end > len(domains) {
			end = len(domains)
		}
		batch := domains[start:end]
		if err := p.resendDNSQueries(ctx, batch, true, profile); err != nil {
			return err
		}

		recovery.Completed += len(batch)
		recovery.UpdatedAt = time.Now().UTC()
		switch stage {
		case "priority":
			recovery.Primary = cloneDomainCandidates(domains[end:])
			if primaryRef != nil {
				*primaryRef = cloneDomainCandidates(domains[end:])
			}
		case "tail":
			recovery.Secondary = cloneDomainCandidates(domains[end:])
			if secondaryRef != nil {
				*secondaryRef = cloneDomainCandidates(domains[end:])
			}
		}
		if err := p.persistFullRebuildTask(recovery); err != nil {
			return err
		}
	}
	return nil
}

// resendDNSQueries handles step 6 of the workflow.
func (p *Requery) resendDNSQueries(ctx context.Context, domains []domainCandidate, updateProgress bool, profile taskProfile) error {
	// 确保 QueriesPerSecond 大于 0，防止除以零 panic
	qps := profile.QPS
	if qps <= 0 {
		qps = p.defaultFullQPS()
	}
	// time.Second / qps 必须大于 0，避免 ticker 间隔为 0 触发 panic。
	if qps > int(time.Second) {
		qps = int(time.Second)
	}
	ticker := time.NewTicker(time.Second / time.Duration(qps))
	defer ticker.Stop()

	type queryJob struct {
		domain string
		qtype  uint16
		useAD  bool
		useCD  bool
		useDO  bool
	}
	workerCount := requeryWorkerCount(qps)
	jobCh := make(chan queryJob, workerCount*4)
	var workerWG sync.WaitGroup
	resolverAddrs := p.resolverAddressesForProfile(profile)
	for i := 0; i < workerCount; i++ {
		resolverAddr := resolverAddrs[i%len(resolverAddrs)]
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			dnsClient := &dns.Client{Timeout: 2 * time.Second}
			for job := range jobCh {
				msg := new(dns.Msg)
				msg.SetQuestion(dns.Fqdn(job.domain), job.qtype)
				msg.AuthenticatedData = job.useAD
				msg.CheckingDisabled = job.useCD
				msg.RecursionDesired = true
				if job.useDO {
					msg.SetEdns0(4096, true)
				}
				_, _, _ = dnsClient.ExchangeContext(ctx, msg, resolverAddr)
			}
		}()
	}

	sendJob := func(job queryJob) bool {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return false
		}
		select {
		case jobCh <- job:
			return true
		case <-ctx.Done():
			return false
		}
	}

	for i := 0; i < len(domains); i++ {
		rawDomainStr := domains[i].Name

		// --- 新增逻辑：解析域名和 Flags ---
		// 1. 分割字符串
		parts := strings.Split(rawDomainStr, "|")
		realDomain := parts[0] // 始终是域名部分

		// 2. 解析 Flags
		var useAD, useCD, useDO bool
		if len(parts) > 1 {
			for _, flag := range parts[1:] {
				switch flag {
				case "AD":
					useAD = true
				case "CD":
					useCD = true
				case "DO":
					useDO = true
				}
			}
		}

		qmask := p.effectiveQueryMask(domains[i].QTypeMask)
		if qmask&qtypeMaskA != 0 {
			if !sendJob(queryJob{
				domain: realDomain,
				qtype:  dns.TypeA,
				useAD:  useAD,
				useCD:  useCD,
				useDO:  useDO,
			}) {
				close(jobCh)
				workerWG.Wait()
				if updateProgress {
					p.setCancelledState("task cancelled by user")
				}
				return ctx.Err()
			}
		}
		if qmask&qtypeMaskAAAA != 0 {
			if !sendJob(queryJob{
				domain: realDomain,
				qtype:  dns.TypeAAAA,
				useAD:  useAD,
				useCD:  useCD,
				useDO:  useDO,
			}) {
				close(jobCh)
				workerWG.Wait()
				if updateProgress {
					p.setCancelledState("task cancelled by user")
				}
				return ctx.Err()
			}
		}

		if updateProgress {
			p.mu.Lock()
			p.status.Progress.Processed++
			p.status.TaskStageProcessed++
			p.mu.Unlock()
		}
	}

	close(jobCh)
	workerWG.Wait()
	return nil
}

func (p *Requery) resolverAddressesForProfile(profile taskProfile) []string {
	primary := strings.TrimSpace(profile.ResolverAddr)
	var addresses []string
	switch profile.Mode {
	case "quick_prewarm":
		addresses = append(addresses, splitResolverAddresses(primary)...)
	default:
		addresses = append(addresses, splitResolverAddresses(primary)...)
		addresses = append(addresses, splitResolverAddressesSlice(p.config.ExecutionSettings.RefreshResolverPool)...)
		if len(addresses) == 0 {
			addresses = append(addresses, splitResolverAddresses(p.refreshResolverAddress())...)
		}
	}
	if len(addresses) == 0 {
		addresses = append(addresses, splitResolverAddresses(p.config.ExecutionSettings.ResolverAddress)...)
	}
	if len(addresses) == 0 {
		addresses = []string{"127.0.0.1:53"}
	}
	return uniqueResolverAddresses(addresses)
}

func splitResolverAddresses(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		out = append(out, field)
	}
	return out
}

func splitResolverAddressesSlice(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, splitResolverAddresses(item)...)
	}
	return out
}

func uniqueResolverAddresses(addresses []string) []string {
	seen := make(map[string]struct{}, len(addresses))
	out := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out
}

func requeryWorkerCount(qps int) int {
	workers := qps / 8
	if workers < 4 {
		workers = 4
	}
	if workers > 128 {
		workers = 128
	}
	return workers
}

func parseQTypeMaskFromFields(fields []string) uint8 {
	var mask uint8
	for _, field := range fields {
		k, v, ok := strings.Cut(field, "=")
		if !ok || k != "qmask" {
			continue
		}
		if parsed, err := strconv.Atoi(v); err == nil {
			mask |= uint8(parsed)
		}
	}
	return mask
}

func (p *Requery) refreshResolverAddress() string {
	if p.config.ExecutionSettings.RefreshResolverAddress != "" {
		return p.config.ExecutionSettings.RefreshResolverAddress
	}
	return p.config.ExecutionSettings.ResolverAddress
}

func (p *Requery) effectiveQueryMask(observed uint8) uint8 {
	switch strings.ToLower(p.config.ExecutionSettings.QueryMode) {
	case "a", "ipv4", "ipv4_only":
		return qtypeMaskA
	case "aaaa", "ipv6", "ipv6_only":
		return qtypeMaskAAAA
	case "dual", "all":
		return qtypeMaskA | qtypeMaskAAAA
	default:
		if observed != 0 {
			return observed
		}
		return qtypeMaskA | qtypeMaskAAAA
	}
}

func (p *Requery) shouldFlushBeforeRefresh() bool {
	switch strings.ToLower(p.config.Workflow.FlushMode) {
	case "", "none":
		return false
	case "legacy", "before_refresh":
		return len(p.config.URLActions.FlushRules) > 0
	default:
		return len(p.config.URLActions.FlushRules) > 0
	}
}

func (p *Requery) refreshMode() string {
	mode := strings.ToLower(strings.TrimSpace(p.config.Workflow.Mode))
	switch mode {
	case "manual", "scheduled", "hybrid":
		return mode
	default:
		if p.config.Scheduler.Enabled {
			return "hybrid"
		}
		return "manual"
	}
}

func (p *Requery) allowsOnDemand() bool {
	return p.refreshMode() != "scheduled"
}

func (p *Requery) allowsSweep() bool {
	if p.refreshMode() == "manual" {
		return false
	}
	return p.config.Scheduler.Enabled && p.config.Scheduler.IntervalMinutes > 0
}

func (p *Requery) maxQueueSize() int {
	if p.config.ExecutionSettings.MaxQueueSize > 0 {
		return p.config.ExecutionSettings.MaxQueueSize
	}
	return 2048
}

func (p *Requery) enqueueRefreshJob(job refreshJob) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.allowsOnDemand() {
		p.status.OnDemandSkipped++
		return false
	}
	if job.Domain == "" {
		p.status.OnDemandSkipped++
		return false
	}
	job.Domain = strings.TrimSuffix(strings.TrimSpace(job.Domain), ".")
	if job.Domain == "" {
		p.status.OnDemandSkipped++
		return false
	}
	if job.QTypeMask == 0 {
		job.QTypeMask = qtypeMaskA | qtypeMaskAAAA
	}
	job.Priority = priorityForReason(job.Reason)
	if job.ObservedAt.IsZero() {
		job.ObservedAt = time.Now().UTC()
	}
	key := job.key()
	if _, exists := p.queueIndex[key]; exists {
		p.status.OnDemandSkipped++
		return false
	}
	if len(p.queue) >= p.maxQueueSize() {
		p.status.OnDemandSkipped++
		return false
	}
	heap.Push(&p.queue, job)
	p.queueIndex[key] = struct{}{}
	p.status.PendingQueue = len(p.queue)
	p.status.OnDemandTriggered++
	select {
	case p.queueKick <- struct{}{}:
	default:
	}
	return true
}

func (p *Requery) EnqueueDomainRefresh(_ context.Context, job coremain.DomainRefreshJob) bool {
	return p.enqueueRefreshJob(refreshJob{
		Domain:     job.Domain,
		MemoryID:   job.MemoryID,
		QTypeMask:  job.QTypeMask,
		Reason:     job.Reason,
		VerifyTag:  job.VerifyTag,
		ObservedAt: job.ObservedAt,
	})
}

func (p *Requery) dequeueRefreshBatch(max int) []refreshJob {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.status.TaskState == "running" || len(p.queue) == 0 {
		return nil
	}
	if max <= 0 {
		max = 1
	}
	jobs := make([]refreshJob, 0, minInt(max, len(p.queue)))
	for len(p.queue) > 0 && len(jobs) < max {
		job := heap.Pop(&p.queue).(refreshJob)
		delete(p.queueIndex, job.key())
		jobs = append(jobs, job)
	}
	p.status.PendingQueue = len(p.queue)
	return jobs
}

func (p *Requery) runOnDemandLoop() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.queueKick:
		case <-ticker.C:
		}

		for {
			jobs := p.dequeueRefreshBatch(p.onDemandBatchSize())
			if len(jobs) == 0 {
				break
			}
			p.processOnDemandBatch(jobs)
		}
	}
}

func (p *Requery) onDemandBatchSize() int {
	if p.config.ExecutionSettings.OnDemandBatchSize > 0 {
		return p.config.ExecutionSettings.OnDemandBatchSize
	}
	return 32
}

func (p *Requery) processOnDemandBatch(jobs []refreshJob) {
	if len(jobs) == 0 {
		return
	}

	qps := p.config.ExecutionSettings.QueriesPerSecond
	if qps <= 0 {
		qps = 100
	}
	timeout := 5*time.Second + (time.Duration(len(jobs))*time.Second)/time.Duration(qps)
	if timeout < 10*time.Second {
		timeout = 10 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	candidates := make([]domainCandidate, 0, len(jobs))
	for _, job := range jobs {
		candidates = append(candidates, domainCandidate{
			Name:      job.Domain,
			QTypeMask: job.QTypeMask,
		})
	}

	profile := p.profileForMode("quick_rebuild", len(jobs))
	profile.VerifyOnDemand = true
	err := p.resendDNSQueries(ctx, candidates, false, profile)
	if err != nil {
		p.mu.Lock()
		p.status.OnDemandSkipped += int64(len(jobs))
		p.lastError = err.Error()
		p.mu.Unlock()
		return
	}

	for _, job := range jobs {
		if job.VerifyTag != "" {
			if err := p.markDomainVerified(ctx, job); err != nil {
				p.mu.Lock()
				p.lastError = err.Error()
				p.mu.Unlock()
			}
		}
	}

	if workflowBool(p.config.Workflow.SaveAfterRefresh, true) && len(p.config.URLActions.SaveRules) > 0 {
		if result := p.callURLs(ctx, "save_rules", p.config.URLActions.SaveRules); result.Failed > 0 {
			p.mu.Lock()
			p.lastError = fmt.Sprintf("save_rules failed: %d/%d", result.Failed, result.Total)
			p.mu.Unlock()
		}
	}

	p.mu.Lock()
	p.status.OnDemandProcessed += int64(len(jobs))
	p.status.LastOnDemandAt = time.Now().UTC()
	p.status.LastOnDemandDomain = jobs[len(jobs)-1].Domain
	p.mu.Unlock()
}

func (p *Requery) markDomainVerified(ctx context.Context, job refreshJob) error {
	if strings.TrimSpace(job.VerifyTag) == "" {
		return nil
	}
	verifier, ok := p.plugin(job.VerifyTag).(coremain.DomainVerifyPlugin)
	if !ok || verifier == nil {
		return fmt.Errorf("verify target %s is not a DomainVerifyPlugin", job.VerifyTag)
	}
	_, err := verifier.MarkDomainVerified(ctx, job.Domain, time.Now().UTC().Format(time.RFC3339))
	return err
}

func workflowBool(v *bool, defaultValue bool) bool {
	if v == nil {
		return defaultValue
	}
	return *v
}

func boolPtr(v bool) *bool {
	return &v
}

func (p *Requery) snapshotStatusLocked() statusSnapshot {
	status := p.status
	preview := make([]refreshJobView, 0, minInt(len(p.queue), 10))
	if len(p.queue) > 0 {
		items := make([]refreshJob, len(p.queue))
		copy(items, p.queue)
		sort.Slice(items, func(i, j int) bool {
			if items[i].Priority == items[j].Priority {
				if items[i].ObservedAt.Equal(items[j].ObservedAt) {
					return items[i].Domain < items[j].Domain
				}
				return items[i].ObservedAt.Before(items[j].ObservedAt)
			}
			return items[i].Priority > items[j].Priority
		})
		for i := 0; i < len(items) && i < 10; i++ {
			preview = append(preview, refreshJobView{
				Domain:     items[i].Domain,
				MemoryID:   items[i].MemoryID,
				Reason:     items[i].Reason,
				QTypeMask:  items[i].QTypeMask,
				Priority:   items[i].Priority,
				ObservedAt: items[i].ObservedAt,
			})
		}
	}

	return statusSnapshot{
		Status:       status,
		MaxQueueSize: p.maxQueueSize(),
		LastError:    p.lastError,
		QueuePreview: preview,
		BatchCapabilities: ruleActionTargets{
			SaveTargets:  p.actionTagsLocked(p.config.URLActions.SaveRules, "save"),
			FlushTargets: p.actionTagsLocked(p.config.URLActions.FlushRules, "flush"),
		},
	}
}

func (p *Requery) currentStatus() statusSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snapshotStatusLocked()
}

func (p *Requery) cloneConfigLocked() *Config {
	if p.config == nil {
		return nil
	}
	cfg := *p.config
	cfg.Status = p.status
	cfg.FullRebuildTask = cloneFullRebuildTask(p.fullTask)
	return &cfg
}

func (p *Requery) actionTagsLocked(urls []string, expectedAction string) []string {
	seen := make(map[string]struct{})
	tags := make([]string, 0, len(urls))
	for _, raw := range urls {
		tag := pluginTagFromActionURL(raw, expectedAction)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags
}

func pluginTagFromActionURL(raw, expectedAction string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(u.Path)
	if path == "" {
		path = strings.TrimSpace(raw)
	}
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	switch {
	case len(parts) >= 5 && parts[0] == "api" && parts[1] == "v1" && (parts[2] == "memory" || parts[2] == "cache"):
		if expectedAction != "" && parts[4] != expectedAction {
			return ""
		}
		return parts[3]
	default:
		return ""
	}
}

func pluginActionFromURL(raw string) (tag string, action string, ok bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	path := strings.TrimSpace(u.Path)
	if path == "" {
		path = strings.TrimSpace(raw)
	}
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	switch {
	case len(parts) >= 5 && parts[0] == "api" && parts[1] == "v1" && (parts[2] == "memory" || parts[2] == "cache"):
		return parts[3], parts[4], true
	default:
		return "", "", false
	}
}

func (p *Requery) collectMemoryStats() ([]memoryStatView, error) {
	tags, err := p.memoryTargetTags()
	if err != nil {
		return nil, err
	}

	views := make([]memoryStatView, 0, len(tags))
	for _, tag := range tags {
		view := memoryStatView{Tag: tag, Key: tag, Name: tag}
		provider, ok := p.plugin(tag).(coremain.DomainStatsProvider)
		if !ok || provider == nil {
			continue
		}
		s := provider.SnapshotDomainStats()
		view.Key = s.Key
		if view.Key == "" {
			view.Key = tag
		}
		view.Name = s.Name
		if view.Name == "" {
			view.Name = tag
		}
		view.MemoryID = s.MemoryID
		view.Kind = s.Kind
		view.TotalEntries = s.TotalEntries
		view.DirtyEntries = s.DirtyEntries
		view.PromotedEntries = s.PromotedEntries
		view.PublishedRules = s.PublishedRules
		view.TotalObservations = s.TotalObservations
		view.DroppedObservations = s.DroppedObservations
		view.DroppedByBuffer = s.DroppedByBuffer
		view.DroppedByCap = s.DroppedByCap
		view.Error = s.Error
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		return views[i].Name < views[j].Name
	})
	return views, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func normalizeTaskMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "full", "full_rebuild":
		return "full_rebuild"
	case "quick", "quick_rebuild":
		return "quick_rebuild"
	case "prewarm", "quick_prewarm":
		return "quick_prewarm"
	default:
		return ""
	}
}

func (p *Requery) defaultFullQPS() int {
	if p.config.ExecutionSettings.QueriesPerSecond > 0 {
		return p.config.ExecutionSettings.QueriesPerSecond
	}
	return 100
}

func (p *Requery) defaultQuickQPS() int {
	if p.config.ExecutionSettings.QuickQueriesPerSecond > 0 {
		return p.config.ExecutionSettings.QuickQueriesPerSecond
	}
	qps := p.defaultFullQPS()
	if qps < 200 {
		return 200
	}
	return qps
}

func (p *Requery) defaultPrewarmQPS() int {
	if p.config.ExecutionSettings.PrewarmQueriesPerSecond > 0 {
		return p.config.ExecutionSettings.PrewarmQueriesPerSecond
	}
	qps := p.defaultQuickQPS()
	if qps < 300 {
		return 300
	}
	return qps
}

func (p *Requery) defaultQuickLimit() int {
	if p.config.ExecutionSettings.QuickRebuildLimit > 0 {
		return p.config.ExecutionSettings.QuickRebuildLimit
	}
	return 2000
}

func (p *Requery) defaultPrewarmLimit() int {
	if p.config.ExecutionSettings.PrewarmLimit > 0 {
		return p.config.ExecutionSettings.PrewarmLimit
	}
	return 1000
}

func (p *Requery) fullRebuildPriorityLimit() int {
	if p.config.ExecutionSettings.FullRebuildPriorityLimit > 0 {
		return p.config.ExecutionSettings.FullRebuildPriorityLimit
	}
	limit := p.defaultQuickLimit() * 2
	if limit < 4000 {
		return 4000
	}
	return limit
}

func (p *Requery) profileForMode(mode string, limit int) taskProfile {
	switch normalizeTaskMode(mode) {
	case "quick_rebuild":
		if limit <= 0 {
			limit = p.defaultQuickLimit()
		}
		return taskProfile{
			Mode:           "quick_rebuild",
			DisplayName:    "快速重建",
			Limit:          limit,
			ResolverAddr:   p.refreshResolverAddress(),
			QPS:            p.defaultQuickQPS(),
			SaveBefore:     false,
			SaveAfter:      workflowBool(p.config.Workflow.SaveAfterRefresh, true),
			FlushBefore:    false,
			VerifyOnDemand: true,
			ProgressMode:   "批量重建",
		}
	case "quick_prewarm":
		if limit <= 0 {
			limit = p.defaultPrewarmLimit()
		}
		return taskProfile{
			Mode:           "quick_prewarm",
			DisplayName:    "快速预热",
			Limit:          limit,
			ResolverAddr:   p.config.ExecutionSettings.ResolverAddress,
			QPS:            p.defaultPrewarmQPS(),
			SaveBefore:     false,
			SaveAfter:      false,
			FlushBefore:    false,
			VerifyOnDemand: false,
			ProgressMode:   "缓存预热",
		}
	default:
		return taskProfile{
			Mode:           "full_rebuild",
			DisplayName:    "完整重建",
			Limit:          limit,
			ResolverAddr:   p.refreshResolverAddress(),
			QPS:            p.defaultFullQPS(),
			SaveBefore:     workflowBool(p.config.Workflow.SaveBeforeRefresh, true),
			SaveAfter:      workflowBool(p.config.Workflow.SaveAfterRefresh, true),
			FlushBefore:    p.shouldFlushBeforeRefresh(),
			VerifyOnDemand: true,
			ProgressMode:   "批量重建",
		}
	}
}

// ----------------------------------------------------------------------------
// 4. API Handlers
// ----------------------------------------------------------------------------

func (p *Requery) api() *chi.Mux {
	r := chi.NewRouter()

	r.Get("/", p.handleGetConfig)
	r.Get("/status", p.handleGetStatus)
	r.Get("/summary", p.handleGetSummary)
	r.Get("/jobs", p.handleGetJobs)
	r.Get("/runs", p.handleGetRuns)
	r.Get("/checkpoints", p.handleGetCheckpoints)
	r.Post("/trigger", p.handleTriggerTask)
	r.Post("/enqueue", p.handleEnqueueRefresh)
	r.Post("/cancel", p.handleCancelTask)
	r.Post("/scheduler/config", p.handleUpdateScheduler)
	r.Post("/rules/save", p.handleSaveRules)
	r.Post("/rules/flush", p.handleFlushRules)
	r.Get("/stats/source_file_counts", p.handleGetSourceFileCounts)
	return r
}

func (p *Requery) handleGetSummary(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	cfg := p.cloneConfigLocked()
	status := p.snapshotStatusLocked()
	p.mu.RUnlock()

	memoryStats, err := p.collectMemoryStats()
	if err != nil {
		p.jsonError(w, "Failed to resolve memory pool targets", http.StatusInternalServerError)
		return
	}
	status.MemoryStats = memoryStats
	recentRuns, err := p.listRuntimeRuns(10)
	if err != nil {
		p.jsonError(w, "Failed to load recent run history", http.StatusInternalServerError)
		return
	}
	summary := summaryResponse{
		Config:      cfg,
		Status:      status,
		MemoryStats: memoryStats,
		RuleTargets: status.BatchCapabilities,
		RecentRuns:  recentRuns,
	}
	p.jsonResponse(w, summary, http.StatusOK)
}

func (p *Requery) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	cfg := p.cloneConfigLocked()
	p.mu.RUnlock()
	p.jsonResponse(w, cfg, http.StatusOK)
}

func (p *Requery) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	p.jsonResponse(w, p.currentStatus(), http.StatusOK)
}

func (p *Requery) handleGetJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := p.listRuntimeJobs()
	if err != nil {
		p.jsonError(w, "Failed to load runtime jobs", http.StatusInternalServerError)
		return
	}
	p.jsonResponse(w, jobs, http.StatusOK)
}

func (p *Requery) handleGetRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	runs, err := p.listRuntimeRuns(limit)
	if err != nil {
		p.jsonError(w, "Failed to load runtime runs", http.StatusInternalServerError)
		return
	}
	p.jsonResponse(w, runs, http.StatusOK)
}

func (p *Requery) handleGetCheckpoints(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("limit")))
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	checkpoints, err := p.listRuntimeCheckpoints(runID, limit)
	if err != nil {
		p.jsonError(w, "Failed to load runtime checkpoints", http.StatusInternalServerError)
		return
	}
	p.jsonResponse(w, checkpoints, http.StatusOK)
}

func (p *Requery) handleTriggerTask(w http.ResponseWriter, r *http.Request) {
	var payload triggerPayload
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&payload)
	}
	mode := normalizeTaskMode(payload.Mode)
	if mode == "" {
		mode = "full_rebuild"
	}
	profile := p.profileForMode(mode, payload.Limit)
	if ok := p.startTask(profile); !ok {
		p.jsonError(w, "A task is already running.", http.StatusConflict)
		return
	}

	p.jsonResponse(w, map[string]string{
		"status":    "success",
		"message":   fmt.Sprintf("%s任务已开始。", profile.DisplayName),
		"task_mode": profile.Mode,
	}, http.StatusOK)
	_ = coremain.RecordSystemEventToPath(p.runtimeDBPath(), "control.requery", "info", "triggered requery task", map[string]any{
		"mode":  profile.Mode,
		"limit": payload.Limit,
	})
}

func (p *Requery) handleEnqueueRefresh(w http.ResponseWriter, r *http.Request) {
	var req refreshJob
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		p.jsonError(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	if ok := p.enqueueRefreshJob(req); !ok {
		p.jsonResponse(w, map[string]any{
			"status":  "skipped",
			"message": "Refresh request was skipped.",
		}, http.StatusAccepted)
		return
	}
	p.jsonResponse(w, map[string]any{
		"status":        "queued",
		"domain":        req.Domain,
		"pending_queue": p.currentStatus().PendingQueue,
	}, http.StatusAccepted)
}

func (p *Requery) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.status.TaskState != "running" || p.taskCancel == nil {
		p.jsonError(w, "No running task to cancel.", http.StatusNotFound)
		return
	}

	p.taskCancel()
	log.Println("[requery] Task cancellation requested via API.")
	_ = coremain.RecordSystemEventToPath(p.runtimeDBPath(), "control.requery", "warn", "cancelled running requery task", map[string]any{
		"task_state": p.status.TaskState,
		"task_mode":  p.status.TaskMode,
	})

	p.jsonResponse(w, map[string]string{"status": "success", "message": "Task cancellation initiated."}, http.StatusOK)
}

func (p *Requery) handleUpdateScheduler(w http.ResponseWriter, r *http.Request) {
	type SchedulerUpdatePayload struct {
		SchedulerConfig
		DateRangeDays            int      `json:"date_range_days"`
		Mode                     string   `json:"mode"`
		QueriesPerSecond         int      `json:"queries_per_second"`
		QuickQueriesPerSecond    int      `json:"quick_queries_per_second"`
		PrewarmQueriesPerSecond  int      `json:"prewarm_queries_per_second"`
		QuickRebuildLimit        int      `json:"quick_rebuild_limit"`
		PrewarmLimit             int      `json:"prewarm_limit"`
		FullRebuildPriorityLimit int      `json:"full_rebuild_priority_limit"`
		RefreshResolverAddress   string   `json:"refresh_resolver_address"`
		RefreshResolverPool      []string `json:"refresh_resolver_pool"`
	}

	var payload SchedulerUpdatePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		p.jsonError(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.config.Scheduler = payload.SchedulerConfig
	if payload.Mode != "" {
		p.config.Workflow.Mode = strings.ToLower(payload.Mode)
	}

	if payload.DateRangeDays > 0 {
		p.config.ExecutionSettings.DateRangeDays = payload.DateRangeDays
	}
	if payload.QueriesPerSecond > 0 {
		p.config.ExecutionSettings.QueriesPerSecond = payload.QueriesPerSecond
	}
	if payload.QuickQueriesPerSecond > 0 {
		p.config.ExecutionSettings.QuickQueriesPerSecond = payload.QuickQueriesPerSecond
	}
	if payload.PrewarmQueriesPerSecond > 0 {
		p.config.ExecutionSettings.PrewarmQueriesPerSecond = payload.PrewarmQueriesPerSecond
	}
	if payload.QuickRebuildLimit > 0 {
		p.config.ExecutionSettings.QuickRebuildLimit = payload.QuickRebuildLimit
	}
	if payload.PrewarmLimit > 0 {
		p.config.ExecutionSettings.PrewarmLimit = payload.PrewarmLimit
	}
	if payload.FullRebuildPriorityLimit > 0 {
		p.config.ExecutionSettings.FullRebuildPriorityLimit = payload.FullRebuildPriorityLimit
	}
	if strings.TrimSpace(payload.RefreshResolverAddress) != "" {
		p.config.ExecutionSettings.RefreshResolverAddress = strings.TrimSpace(payload.RefreshResolverAddress)
	}
	if len(payload.RefreshResolverPool) > 0 {
		p.config.ExecutionSettings.RefreshResolverPool = append([]string(nil), payload.RefreshResolverPool...)
	}

	if err := p.saveConfigUnlocked(); err != nil {
		p.jsonError(w, "Failed to save updated config", http.StatusInternalServerError)
		return
	}
	p.rescheduleTasks()
	_ = coremain.RecordSystemEventToPath(p.runtimeDBPath(), "control.requery", "info", "updated requery scheduler config", map[string]any{
		"enabled":          p.config.Scheduler.Enabled,
		"mode":             p.config.Workflow.Mode,
		"interval_minutes": p.config.Scheduler.IntervalMinutes,
	})
	p.jsonResponse(w, map[string]string{"status": "success", "message": "Scheduler configuration updated successfully."}, http.StatusOK)
}

func (p *Requery) handleSaveRules(w http.ResponseWriter, r *http.Request) {
	result := p.callURLs(r.Context(), "save_rules", p.config.URLActions.SaveRules)
	code := http.StatusOK
	if result.Failed > 0 {
		code = http.StatusBadGateway
	}
	p.jsonResponse(w, result, code)
}

func (p *Requery) handleFlushRules(w http.ResponseWriter, r *http.Request) {
	result := p.callURLs(r.Context(), "flush_rules", p.config.URLActions.FlushRules)
	code := http.StatusOK
	if result.Failed > 0 {
		code = http.StatusBadGateway
	}
	p.jsonResponse(w, result, code)
}

// [已删除] handleClearBackupFile
// [已删除] handleGetBackupFileCount

func (p *Requery) handleGetSourceFileCounts(w http.ResponseWriter, r *http.Request) {
	log.Println("[requery] API: Getting source file counts...")

	type fileCount struct {
		Alias string `json:"alias"`
		Count int    `json:"count"`
	}

	counts := make([]fileCount, 0, len(p.config.DomainProcessing.SourceFiles))
	domainPattern := regexp.MustCompile(`^full:(.+)`)

	for _, sourceFile := range p.config.DomainProcessing.SourceFiles {
		count := 0
		file, err := os.Open(sourceFile.Path)
		if err != nil {
			if os.IsNotExist(err) {
				counts = append(counts, fileCount{Alias: sourceFile.Alias, Count: 0})
				continue
			}
			p.jsonError(w, "Failed to read source file "+sourceFile.Path+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			if domainPattern.MatchString(scanner.Text()) {
				count++
			}
		}
		if err := scanner.Err(); err != nil {
			p.jsonError(w, "Error while scanning file "+sourceFile.Path+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		counts = append(counts, fileCount{Alias: sourceFile.Alias, Count: count})
	}

	p.jsonResponse(w, map[string]any{"status": "success", "data": counts}, http.StatusOK)
}

// ----------------------------------------------------------------------------
// 5. Helper and Utility Functions
// ----------------------------------------------------------------------------

// [FIX] Corrected rescheduleTasks logic
func (p *Requery) rescheduleTasks() {
	if err := p.setupScheduler(); err != nil {
		log.Printf("[requery] WARN: Failed to reschedule tasks: %v", err)
	}
}

// [Modified] Rewrite setupScheduler to implement precise periodic scheduling based on the start time
func (p *Requery) setupScheduler() error {
	// 1. Remove all old scheduled jobs. This logic remains unchanged.
	for _, entry := range p.scheduler.Entries() {
		p.scheduler.Remove(entry.ID)
	}

	// 2. Check if the scheduler is enabled in the config. This logic remains unchanged.
	if !p.allowsSweep() {
		log.Println("[requery] Sweep scheduler is disabled or interval is invalid in config.")
		return nil
	}

	// 3. Check and parse the start time (start_datetime).
	// If it's not set, precise scheduling is not possible, so we return directly.
	startTime := time.Now().UTC()
	if p.config.Scheduler.StartDatetime != "" {
		parsed, err := time.Parse(time.RFC3339, p.config.Scheduler.StartDatetime)
		if err != nil {
			log.Printf("[requery] WARN: Invalid 'start_datetime' format ('%s'), using interval from now: %v", p.config.Scheduler.StartDatetime, err)
		} else {
			startTime = parsed
		}
	}

	// 4. Define the job to be executed. This logic remains unchanged and already includes the check to prevent task overlap.
	jobFunc := func() {
		profile := p.profileForMode("quick_rebuild", 0)
		log.Printf("[requery] Scheduler is triggering a task: %s.", profile.Mode)
		if ok := p.startTaskWithSource(profile, nil, "scheduler"); !ok {
			log.Println("[requery] Scheduler skipped: previous task is still running.")
		}
	}

	// 5. [Core Modification] Calculate the next precise execution time point.
	now := time.Now().UTC()
	interval := time.Duration(p.config.Scheduler.IntervalMinutes) * time.Minute
	var nextRunTime time.Time

	if startTime.After(now) {
		// If the start time is in the future, the next run time is the start time itself.
		nextRunTime = startTime
	} else {
		// If the start time has passed, calculate the next period from that point.
		// a. Calculate the duration that has elapsed since the start time.
		elapsed := now.Sub(startTime)
		// b. Calculate how many full intervals have passed.
		cyclesPassed := elapsed / interval
		// c. The next run time = start time + (number of cycles passed + 1) * interval.
		nextRunTime = startTime.Add(time.Duration(cyclesPassed+1) * interval)
	}

	// 6. Use time.AfterFunc to create a one-off timer to schedule the next job.
	delay := nextRunTime.Sub(now)

	if delay > 0 {
		log.Printf("[requery] Next scheduled run will be at %v (in %v).", nextRunTime.Local(), delay.Round(time.Second))

		// When the timer fires, it will execute the job and then immediately call rescheduleTasks
		// to schedule the subsequent job, creating a chain.
		time.AfterFunc(delay, func() {
			jobFunc()
			// Immediately reschedule to calculate and arrange the next execution cycle.
			p.rescheduleTasks()
		})
	} else {
		// This is an edge case, which should rarely happen. If the calculated run time is in the past
		// (possibly due to system clock issues or a long-running task),
		// reschedule immediately to find the next valid time point.
		log.Printf("[requery] Calculated next run time (%v) is in the past. Attempting to reschedule immediately.", nextRunTime.Local())
		go p.rescheduleTasks()
	}

	return nil
}

func (p *Requery) callURLs(ctx context.Context, action string, urls []string) batchActionResult {
	start := time.Now()
	result := batchActionResult{
		Action: action,
		Total:  len(urls),
		Items:  make([]batchActionItem, len(urls)),
	}
	if len(urls) == 0 {
		return result
	}

	concurrency := p.config.ExecutionSettings.URLCallConcurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	if concurrency > len(urls) {
		concurrency = len(urls)
	}
	delay := time.Duration(p.config.ExecutionSettings.URLCallDelayMS) * time.Millisecond

	type task struct {
		index int
		url   string
	}

	taskCh := make(chan task)
	var wg sync.WaitGroup
	var mu sync.Mutex

	worker := func() {
		defer wg.Done()
		for task := range taskCh {
			item := batchActionItem{URL: task.url, Tag: pluginTagFromActionURL(task.url, strings.TrimSuffix(strings.TrimPrefix(action, "batch_"), "_rules"))}
			reqStart := time.Now()
			if tag, op, ok := pluginActionFromURL(task.url); ok {
				switch op {
				case "save":
					if saver, ok := p.plugin(tag).(coremain.SaveablePlugin); ok && saver != nil {
						err := saver.SaveToDisk(ctx)
						item.StatusCode = http.StatusOK
						if err == nil {
							item.OK = true
						} else {
							item.Error = err.Error()
							item.StatusCode = http.StatusInternalServerError
						}
					}
				case "flush":
					if flusher, ok := p.plugin(tag).(coremain.FlushablePlugin); ok && flusher != nil {
						err := flusher.FlushRuntime(ctx)
						item.StatusCode = http.StatusOK
						if err == nil {
							item.OK = true
						} else {
							item.Error = err.Error()
							item.StatusCode = http.StatusInternalServerError
						}
					}
				}
			}
			if !item.OK && item.Error == "" {
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, task.url, nil)
				if err != nil {
					item.Error = err.Error()
				} else {
					resp, err := p.httpClient.Do(req)
					if err != nil {
						item.Error = err.Error()
					} else {
						item.StatusCode = resp.StatusCode
						_, _ = io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
						if resp.StatusCode >= 200 && resp.StatusCode < 300 {
							item.OK = true
						} else {
							item.Error = fmt.Sprintf("bad response status %d", resp.StatusCode)
						}
					}
				}
			}
			item.DurationMS = time.Since(reqStart).Milliseconds()

			mu.Lock()
			result.Items[task.index] = item
			if item.OK {
				result.Success++
			} else {
				result.Failed++
			}
			mu.Unlock()
		}
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go worker()
	}

dispatchLoop:
	for i, raw := range urls {
		select {
		case <-ctx.Done():
			break dispatchLoop
		case taskCh <- task{index: i, url: raw}:
		}
		if delay > 0 && i < len(urls)-1 {
			select {
			case <-ctx.Done():
				break dispatchLoop
			case <-time.After(delay):
			}
		}
	}
	close(taskCh)
	wg.Wait()
	result.DurationMS = time.Since(start).Milliseconds()
	return result
}

func (p *Requery) setFailedState(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status.TaskState = "failed"
	p.lastError = fmt.Sprintf(format, args...)
	if p.fullTask != nil {
		p.fullTask.LastError = p.lastError
		p.fullTask = nil
	}
	_ = p.saveStateUnlocked()
	log.Printf("[requery] ERROR: Task failed: "+format, args...)
}

func (p *Requery) setCancelledState(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status.TaskState = "cancelled"
	p.lastError = reason
	if p.fullTask != nil {
		p.fullTask = nil
	}
	_ = p.saveStateUnlocked()
	log.Println("[requery] INFO: Task cancelled:", reason)
}

func (p *Requery) jsonResponse(w http.ResponseWriter, data any, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[requery] ERROR: failed to encode response: %v", err)
	}
}

func (p *Requery) jsonError(w http.ResponseWriter, message string, code int) {
	p.jsonResponse(w, map[string]string{"status": "error", "message": message}, code)
}
