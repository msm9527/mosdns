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
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
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
	File string `yaml:"file"` // Path to the requeryconfig.json file
}

// newRequery is the plugin's initialization function.
func newRequery(bp *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)
	if cfg.File == "" {
		return nil, errors.New("requery: 'file' for config json must be specified")
	}

	dir := filepath.Dir(cfg.File)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("requery: failed to create directory %s: %w", dir, err)
	}

	p := &Requery{
		filePath:   cfg.File,
		scheduler:  cron.New(),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		queue:      make(refreshJobHeap, 0),
		queueIndex: make(map[string]struct{}),
		queueKick:  make(chan struct{}, 1),
	}
	heap.Init(&p.queue)

	if err := p.loadConfig(); err != nil {
		return nil, fmt.Errorf("requery: failed to load initial config from %s: %w", p.filePath, err)
	}

	// Resiliency check: If mosdns was stopped while a task was running, mark it as failed.
	p.mu.Lock()
	if p.config.Status.TaskState == "running" {
		log.Println("[requery] WARN: Found task in 'running' state on startup. Marking as 'failed'.")
		p.config.Status.TaskState = "failed"
		p.config.Status.LastRunEndTime = time.Now().UTC()
		_ = p.saveConfigUnlocked() // Attempt to save the updated state
	}
	p.mu.Unlock()

	// Start the scheduler's goroutine once. It will run forever.
	p.scheduler.Start()
	log.Println("[requery] Scheduler started.")
	go p.runOnDemandLoop()

	// Now, add the initial job based on the loaded config.
	if err := p.setupScheduler(); err != nil {
		log.Printf("[requery] WARN: Failed to setup initial scheduler job, it will be disabled: %v", err)
	}

	bp.RegAPI(p.api())

	log.Printf("[requery] plugin instance created for config file: %s", p.filePath)
	return p, nil
}

// ----------------------------------------------------------------------------
// 2. Main Plugin Struct and Configuration Structs
// ----------------------------------------------------------------------------

// Requery is the main struct for the plugin.
type Requery struct {
	mu         sync.RWMutex
	filePath   string
	config     *Config
	scheduler  *cron.Cron
	taskCtx    context.Context
	taskCancel context.CancelFunc
	httpClient *http.Client
	queue      refreshJobHeap
	queueIndex map[string]struct{}
	queueKick  chan struct{}
}

// Config maps directly to the requeryconfig.json file structure.
type Config struct {
	DomainProcessing  DomainProcessing  `json:"domain_processing"`
	URLActions        URLActions        `json:"url_actions"`
	Workflow          WorkflowSettings  `json:"workflow"`
	Scheduler         SchedulerConfig   `json:"scheduler"`
	ExecutionSettings ExecutionSettings `json:"execution_settings"`
	Status            Status            `json:"status"`
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

type ExecutionSettings struct {
	QueriesPerSecond       int    `json:"queries_per_second"`
	ResolverAddress        string `json:"resolver_address"`
	RefreshResolverAddress string `json:"refresh_resolver_address,omitempty"`
	QueryMode              string `json:"query_mode,omitempty"`
	URLCallDelayMS         int    `json:"url_call_delay_ms"`
	DateRangeDays          int    `json:"date_range_days"` // 新增配置项：日期范围
	MaxQueueSize           int    `json:"max_queue_size,omitempty"`
}

type Status struct {
	TaskState          string    `json:"task_state"` // "idle", "running", "failed", "cancelled"
	LastRunStartTime   time.Time `json:"last_run_start_time,omitempty"`
	LastRunEndTime     time.Time `json:"last_run_end_time,omitempty"`
	LastRunDomainCount int       `json:"last_run_domain_count"`
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

type domainCandidate struct {
	Name      string
	QTypeMask uint8
}

type refreshJob struct {
	Domain     string    `json:"domain"`
	MemoryID   string    `json:"memory_id,omitempty"`
	QTypeMask  uint8     `json:"qtype_mask,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	VerifyURL  string    `json:"verify_url,omitempty"`
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

// runTask executes the entire requery workflow. It's designed to be run in a goroutine.
func (p *Requery) runTask(ctx context.Context) {
	p.mu.Lock()
	if p.config.Status.TaskState == "running" {
		log.Println("[requery] Task trigger ignored: a task is already running.")
		p.mu.Unlock()
		return
	}

	p.config.Status.TaskState = "running"
	p.config.Status.LastRunStartTime = time.Now().UTC()
	p.config.Status.LastRunEndTime = time.Time{} // Clear end time
	p.config.Status.Progress.Processed = 0
	p.config.Status.Progress.Total = 0
	_ = p.saveConfigUnlocked()
	p.mu.Unlock()

	// Defer block to ensure state is cleaned up on any exit path (success, failure, cancellation).
	defer func() {
		p.mu.Lock()

		if p.config.Status.TaskState == "running" {
			p.config.Status.TaskState = "idle" // Assume success unless overridden
		}

		if r := recover(); r != nil {
			log.Printf("[requery] FATAL: Task panicked: %v", r)
			p.config.Status.TaskState = "failed"
		}

		p.config.Status.LastRunEndTime = time.Now().UTC()
		_ = p.saveConfigUnlocked()

		p.taskCancel = nil
		p.mu.Unlock()
		// [修改点] 调用核心包的通用内存清理函数
		// 因为 ManualGC 内部是异步(go func)的，所以这里调用不会阻塞锁，非常安全
		log.Println("[requery] Task finished, triggering background memory release...")
		coremain.ManualGC()
	}()

	log.Println("[requery] Starting a new task.")

	if workflowBool(p.config.Workflow.SaveBeforeRefresh, true) {
		log.Println("[requery] Step 1: Saving current rule state...")
		if err := p.callURLs(ctx, p.config.URLActions.SaveRules); err != nil {
			p.setFailedState("failed during save_rules step: %v", err)
			return
		}
	}

	// Step 2 & 3: Consolidate domains (Merge only, no backup read/write)
	log.Println("[requery] Step 2 & 3: Merging domains from source files...")
	domains, err := p.mergeAndFilterDomains(ctx)
	if err != nil {
		p.setFailedState("failed during domain merge: %v", err)
		return
	}
	if len(domains) == 0 {
		log.Println("[requery] No domains found to process. Task finished.")
		return
	}

	if p.shouldFlushBeforeRefresh() {
		log.Println("[requery] Step 4: Flushing old rules (legacy mode)...")
		if err := p.callURLs(ctx, p.config.URLActions.FlushRules); err != nil {
			p.setFailedState("failed during flush_rules step: %v", err)
			return
		}
	}

	// Update status with total domain count
	p.mu.Lock()
	p.config.Status.LastRunDomainCount = len(domains)
	p.config.Status.Progress.Total = int64(len(domains))
	p.mu.Unlock()

	// Step 6: Re-query domains
	log.Printf("[requery] Step 6: Re-querying %d domains...", len(domains))
	err = p.resendDNSQueries(ctx, domains)
	if err != nil {
		// The error (e.g., cancellation) is handled inside resendDNSQueries by setting the state.
		log.Printf("[requery] Task stopped during DNS query phase: %v", err)
		return
	}

	if workflowBool(p.config.Workflow.SaveAfterRefresh, true) {
		log.Println("[requery] Step 7: Publishing refreshed rule state...")
		if err := p.callURLs(ctx, p.config.URLActions.SaveRules); err != nil {
			p.setFailedState("failed during final save_rules step: %v", err)
			return
		}
	}

	log.Println("[requery] Task completed successfully.")
}

// mergeAndFilterDomains handles reading source files, parsing formats, and filtering by date.
// It no longer reads or writes the backup file.
func (p *Requery) mergeAndFilterDomains(ctx context.Context) ([]domainCandidate, error) {
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
					parsedTime, err := time.Parse("2006-01-02", dateStr)
					if err == nil {
						daysDiff := time.Since(parsedTime).Hours() / 24
						if daysDiff <= float64(limitDays) {
							candidate := domainSet[domain]
							candidate.Name = domain
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
	domainSet = nil
	// 此时不再写入 output_file (requery_backup.txt)

	return domains, nil
}

// resendDNSQueries handles step 6 of the workflow.
func (p *Requery) resendDNSQueries(ctx context.Context, domains []domainCandidate) error {
	// 确保 QueriesPerSecond 大于 0，防止除以零 panic
	qps := p.config.ExecutionSettings.QueriesPerSecond
	if qps <= 0 {
		qps = 100
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
	resolverAddr := p.refreshResolverAddress()
	for i := 0; i < workerCount; i++ {
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
				p.setCancelledState("task cancelled by user")
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
				p.setCancelledState("task cancelled by user")
				return ctx.Err()
			}
		}

		newProcessed := atomic.AddInt64(&p.config.Status.Progress.Processed, 1)
		// 减少保存频率，优化 IO
		if newProcessed%100 == 0 || int(newProcessed) == len(domains) {
			p.mu.Lock()
			_ = p.saveConfigUnlocked()
			p.mu.Unlock()
		}
	}

	close(jobCh)
	workerWG.Wait()
	return nil
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
		p.config.Status.OnDemandSkipped++
		return false
	}
	if job.Domain == "" {
		p.config.Status.OnDemandSkipped++
		return false
	}
	job.Domain = strings.TrimSuffix(strings.TrimSpace(job.Domain), ".")
	if job.Domain == "" {
		p.config.Status.OnDemandSkipped++
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
		p.config.Status.OnDemandSkipped++
		return false
	}
	if len(p.queue) >= p.maxQueueSize() {
		p.config.Status.OnDemandSkipped++
		return false
	}
	heap.Push(&p.queue, job)
	p.queueIndex[key] = struct{}{}
	p.config.Status.PendingQueue = len(p.queue)
	p.config.Status.OnDemandTriggered++
	select {
	case p.queueKick <- struct{}{}:
	default:
	}
	return true
}

func (p *Requery) dequeueRefreshJob() (refreshJob, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.config.Status.TaskState == "running" || len(p.queue) == 0 {
		return refreshJob{}, false
	}
	job := heap.Pop(&p.queue).(refreshJob)
	delete(p.queueIndex, job.key())
	p.config.Status.PendingQueue = len(p.queue)
	return job, true
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
			job, ok := p.dequeueRefreshJob()
			if !ok {
				break
			}
			p.processOnDemandJob(job)
		}
	}
}

func (p *Requery) processOnDemandJob(job refreshJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := p.resendDNSQueries(ctx, []domainCandidate{{
		Name:      job.Domain,
		QTypeMask: job.QTypeMask,
	}})
	if err != nil {
		p.mu.Lock()
		p.config.Status.OnDemandSkipped++
		p.mu.Unlock()
		return
	}

	if job.VerifyURL != "" {
		_ = p.markDomainVerified(ctx, job)
	}

	if workflowBool(p.config.Workflow.SaveAfterRefresh, true) && len(p.config.URLActions.SaveRules) > 0 {
		_ = p.callURLs(ctx, p.config.URLActions.SaveRules)
	}

	p.mu.Lock()
	p.config.Status.OnDemandProcessed++
	p.config.Status.LastOnDemandAt = time.Now().UTC()
	p.config.Status.LastOnDemandDomain = job.Domain
	_ = p.saveConfigUnlocked()
	p.mu.Unlock()
}

func (p *Requery) markDomainVerified(ctx context.Context, job refreshJob) error {
	body, err := json.Marshal(map[string]string{
		"domain":      job.Domain,
		"verified_at": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.VerifyURL, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("verify url returned %d", resp.StatusCode)
	}
	return nil
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

// ----------------------------------------------------------------------------
// 4. API Handlers
// ----------------------------------------------------------------------------

func (p *Requery) api() *chi.Mux {
	r := chi.NewRouter()

	r.Get("/", p.handleGetConfig)
	r.Get("/status", p.handleGetStatus)
	r.Post("/trigger", p.handleTriggerTask)
	r.Post("/enqueue", p.handleEnqueueRefresh)
	r.Post("/cancel", p.handleCancelTask)
	r.Post("/scheduler/config", p.handleUpdateScheduler)
	r.Get("/stats/source_file_counts", p.handleGetSourceFileCounts)
	return r
}

func (p *Requery) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	p.jsonResponse(w, p.config, http.StatusOK)
}

func (p *Requery) handleGetStatus(w http.ResponseWriter, r *http.Request) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	p.jsonResponse(w, p.config.Status, http.StatusOK)
}

func (p *Requery) handleTriggerTask(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.config.Status.TaskState == "running" {
		p.jsonError(w, "A task is already running.", http.StatusConflict)
		return
	}

	p.taskCtx, p.taskCancel = context.WithCancel(context.Background())
	go p.runTask(p.taskCtx)

	p.jsonResponse(w, map[string]string{"status": "success", "message": "A new task has been started."}, http.StatusOK)
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
		"pending_queue": p.config.Status.PendingQueue,
	}, http.StatusAccepted)
}

func (p *Requery) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.config.Status.TaskState != "running" || p.taskCancel == nil {
		p.jsonError(w, "No running task to cancel.", http.StatusNotFound)
		return
	}

	p.taskCancel()
	log.Println("[requery] Task cancellation requested via API.")

	p.jsonResponse(w, map[string]string{"status": "success", "message": "Task cancellation initiated."}, http.StatusOK)
}

func (p *Requery) handleUpdateScheduler(w http.ResponseWriter, r *http.Request) {
	// [修改] 定义一个扩展的结构体来接收包含 date_range_days 的 JSON
	type SchedulerUpdatePayload struct {
		SchedulerConfig        // 嵌入原有的 SchedulerConfig 字段 (Enabled, StartDatetime, IntervalMinutes)
		DateRangeDays   int    `json:"date_range_days"` // 新增字段
		Mode            string `json:"mode"`
	}

	var payload SchedulerUpdatePayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		p.jsonError(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// [修改] 分别更新 Scheduler 和 ExecutionSettings
	p.config.Scheduler = payload.SchedulerConfig
	if payload.Mode != "" {
		p.config.Workflow.Mode = strings.ToLower(payload.Mode)
	}

	// 只有当传入了有效天数时才更新 (防止意外归零)
	if payload.DateRangeDays > 0 {
		p.config.ExecutionSettings.DateRangeDays = payload.DateRangeDays
	}

	if err := p.saveConfigUnlocked(); err != nil {
		p.jsonError(w, "Failed to save updated config", http.StatusInternalServerError)
		return
	}
	p.rescheduleTasks()
	p.jsonResponse(w, map[string]string{"status": "success", "message": "Scheduler configuration updated successfully."}, http.StatusOK)
}

// [已删除] handleClearBackupFile
// [已删除] handleGetBackupFileCount

func (p *Requery) handleGetSourceFileCounts(w http.ResponseWriter, r *http.Request) {
	log.Println("[requery] API: Getting source file counts...")
	if err := p.callURLs(r.Context(), p.config.URLActions.SaveRules); err != nil {
		p.jsonError(w, "Failed to save rules before counting: "+err.Error(), http.StatusInternalServerError)
		return
	}

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

func (p *Requery) loadConfig() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	dataBytes, err := os.ReadFile(p.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[requery] config file %s not found, initializing with default empty config.", p.filePath)
			p.config = &Config{
				Status: Status{TaskState: "idle"},
				Workflow: WorkflowSettings{
					FlushMode:         "none",
					Mode:              "hybrid",
					SaveBeforeRefresh: boolPtr(true),
					SaveAfterRefresh:  boolPtr(true),
				},
			}
			p.config.ExecutionSettings.DateRangeDays = 30
			p.config.ExecutionSettings.QueryMode = "observed"
			p.config.ExecutionSettings.MaxQueueSize = 2048
			return p.saveConfigUnlocked()
		}
		return err
	}

	var cfg Config
	if err := json.Unmarshal(dataBytes, &cfg); err != nil {
		return fmt.Errorf("failed to parse json from config file %s: %w", p.filePath, err)
	}
	p.config = &cfg

	// 检查并设置默认值，如果有变更则需要回写配置
	configChanged := false

	if p.config.Status.TaskState == "" {
		p.config.Status.TaskState = "idle"
		configChanged = true // 严格来说这只是内存状态修正，但也可以保存
	}
	if p.config.ExecutionSettings.URLCallDelayMS == 0 {
		p.config.ExecutionSettings.URLCallDelayMS = 50 // Default value
		configChanged = true
	}
	if p.config.ExecutionSettings.QueriesPerSecond == 0 {
		p.config.ExecutionSettings.QueriesPerSecond = 100 // Default value
		configChanged = true
	}
	if p.config.ExecutionSettings.DateRangeDays <= 0 {
		p.config.ExecutionSettings.DateRangeDays = 30 // Default value (Requirement 4)
		configChanged = true
	}
	if p.config.ExecutionSettings.QueryMode == "" {
		p.config.ExecutionSettings.QueryMode = "observed"
		configChanged = true
	}
	if p.config.ExecutionSettings.MaxQueueSize <= 0 {
		p.config.ExecutionSettings.MaxQueueSize = 2048
		configChanged = true
	}
	if p.config.Workflow.FlushMode == "" {
		if len(p.config.URLActions.FlushRules) > 0 && p.config.ExecutionSettings.RefreshResolverAddress == "" {
			p.config.Workflow.FlushMode = "legacy"
		} else {
			p.config.Workflow.FlushMode = "none"
		}
		configChanged = true
	}
	if p.config.Workflow.Mode == "" {
		if p.config.Scheduler.Enabled {
			p.config.Workflow.Mode = "hybrid"
		} else {
			p.config.Workflow.Mode = "manual"
		}
		configChanged = true
	}
	if p.config.Workflow.SaveBeforeRefresh == nil {
		p.config.Workflow.SaveBeforeRefresh = boolPtr(true)
		configChanged = true
	}
	if p.config.Workflow.SaveAfterRefresh == nil {
		p.config.Workflow.SaveAfterRefresh = boolPtr(true)
		configChanged = true
	}

	if configChanged {
		log.Println("[requery] Configuration defaults applied, saving updated config.")
		if err := p.saveConfigUnlocked(); err != nil {
			return fmt.Errorf("failed to save config after applying defaults: %w", err)
		}
	}

	return nil
}

func (p *Requery) saveConfigUnlocked() error {
	dataBytes, err := json.MarshalIndent(p.config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config to json: %w", err)
	}

	tmpFile := p.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, dataBytes, 0644); err != nil {
		return fmt.Errorf("failed to write to temporary config file: %w", err)
	}
	if err := os.Rename(tmpFile, p.filePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to rename temporary config file: %w", err)
	}

	return nil
}

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
		log.Println("[requery] Scheduler is triggering a task.")
		p.mu.Lock()
		defer p.mu.Unlock()

		if p.config.Status.TaskState == "running" {
			log.Println("[requery] Scheduler skipped: previous task is still running.")
			return
		}

		p.taskCtx, p.taskCancel = context.WithCancel(context.Background())
		go p.runTask(p.taskCtx)
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

func (p *Requery) callURLs(ctx context.Context, urls []string) error {
	delay := time.Duration(p.config.ExecutionSettings.URLCallDelayMS) * time.Millisecond
	for i, url := range urls {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return fmt.Errorf("failed to create request for %s: %w", url, err)
		}

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to call URL %s: %w", url, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("bad response from URL %s: status %d, body: %s", url, resp.StatusCode, string(body))
		}

		_, _ = io.Copy(io.Discard, resp.Body)

		if i < len(urls)-1 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func (p *Requery) setFailedState(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config.Status.TaskState = "failed"
	log.Printf("[requery] ERROR: Task failed: "+format, args...)
}

func (p *Requery) setCancelledState(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config.Status.TaskState = "cancelled"
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
