package requery

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
)

const (
	defaultURLCallDelayMS           = 50
	defaultURLCallConcurrency       = 4
	defaultSchedulerIntervalMinutes = 8 * 60
	defaultFullQPS                  = 100
	defaultQuickQPS                 = 200
	defaultPrewarmQPS               = 300
	defaultDateRangeDays            = 30
	defaultMaxQueueSize             = 2048
	defaultOnDemandBatchSize        = 32
	defaultQuickRebuildLimit        = 3500
	defaultPrewarmLimit             = 2000
	defaultFullRebuildPriorityLimit = 6000
	defaultCheckpointBatchSize      = 256
	defaultResumeDelayMS            = 1500
	runtimeStateNamespaceRequery    = "requery"
)

type persistedConfig struct {
	DomainProcessing  DomainProcessing  `json:"domain_processing"`
	URLActions        URLActions        `json:"url_actions"`
	Workflow          WorkflowSettings  `json:"workflow"`
	Scheduler         SchedulerConfig   `json:"scheduler"`
	Recovery          RecoverySettings  `json:"recovery,omitempty"`
	ExecutionSettings ExecutionSettings `json:"execution_settings"`
}

type persistedState struct {
	Status          Status           `json:"status"`
	FullRebuildTask *FullRebuildTask `json:"full_rebuild_task,omitempty"`
}

func newDefaultConfig() *Config {
	cfg := &Config{
		Status: Status{TaskState: "idle"},
		Workflow: WorkflowSettings{
			FlushMode:         "none",
			Mode:              "hybrid",
			SaveBeforeRefresh: boolPtr(true),
			SaveAfterRefresh:  boolPtr(true),
		},
		Recovery: RecoverySettings{
			AutoResume:          boolPtr(true),
			CheckpointBatchSize: defaultCheckpointBatchSize,
			ResumeDelayMS:       defaultResumeDelayMS,
		},
		Scheduler: SchedulerConfig{
			Enabled:         true,
			IntervalMinutes: defaultSchedulerIntervalMinutes,
		},
	}
	cfg.ExecutionSettings.DateRangeDays = defaultDateRangeDays
	cfg.ExecutionSettings.QueryMode = "observed"
	cfg.ExecutionSettings.QueriesPerSecond = defaultFullQPS
	cfg.ExecutionSettings.QuickQueriesPerSecond = defaultQuickQPS
	cfg.ExecutionSettings.PrewarmQueriesPerSecond = defaultPrewarmQPS
	cfg.ExecutionSettings.URLCallDelayMS = defaultURLCallDelayMS
	cfg.ExecutionSettings.URLCallConcurrency = defaultURLCallConcurrency
	cfg.ExecutionSettings.MaxQueueSize = defaultMaxQueueSize
	cfg.ExecutionSettings.OnDemandBatchSize = defaultOnDemandBatchSize
	cfg.ExecutionSettings.QuickRebuildLimit = defaultQuickRebuildLimit
	cfg.ExecutionSettings.PrewarmLimit = defaultPrewarmLimit
	cfg.ExecutionSettings.FullRebuildPriorityLimit = defaultFullRebuildPriorityLimit
	return cfg
}

func normalizeSchedulerDefaults(cfg *SchedulerConfig) bool {
	if cfg == nil || cfg.IntervalMinutes > 0 {
		return false
	}
	cfg.IntervalMinutes = defaultSchedulerIntervalMinutes
	return true
}

func applyConfigDefaults(cfg *Config) bool {
	if cfg == nil {
		return false
	}

	configChanged := false

	if cfg.Status.TaskState == "" {
		cfg.Status.TaskState = "idle"
		configChanged = true
	}
	if normalizeSchedulerDefaults(&cfg.Scheduler) {
		configChanged = true
	}
	if cfg.ExecutionSettings.URLCallDelayMS == 0 {
		cfg.ExecutionSettings.URLCallDelayMS = defaultURLCallDelayMS
		configChanged = true
	}
	if cfg.ExecutionSettings.URLCallConcurrency <= 0 {
		cfg.ExecutionSettings.URLCallConcurrency = defaultURLCallConcurrency
		configChanged = true
	}
	if cfg.ExecutionSettings.QueriesPerSecond == 0 {
		cfg.ExecutionSettings.QueriesPerSecond = defaultFullQPS
		configChanged = true
	}
	if cfg.ExecutionSettings.QuickQueriesPerSecond <= 0 {
		cfg.ExecutionSettings.QuickQueriesPerSecond = defaultQuickQPS
		configChanged = true
	}
	if cfg.ExecutionSettings.PrewarmQueriesPerSecond <= 0 {
		cfg.ExecutionSettings.PrewarmQueriesPerSecond = defaultPrewarmQPS
		configChanged = true
	}
	if cfg.ExecutionSettings.DateRangeDays <= 0 {
		cfg.ExecutionSettings.DateRangeDays = defaultDateRangeDays
		configChanged = true
	}
	if cfg.ExecutionSettings.QueryMode == "" {
		cfg.ExecutionSettings.QueryMode = "observed"
		configChanged = true
	}
	if cfg.ExecutionSettings.MaxQueueSize <= 0 {
		cfg.ExecutionSettings.MaxQueueSize = defaultMaxQueueSize
		configChanged = true
	}
	if cfg.ExecutionSettings.OnDemandBatchSize <= 0 {
		cfg.ExecutionSettings.OnDemandBatchSize = defaultOnDemandBatchSize
		configChanged = true
	}
	if cfg.ExecutionSettings.QuickRebuildLimit <= 0 {
		cfg.ExecutionSettings.QuickRebuildLimit = defaultQuickRebuildLimit
		configChanged = true
	}
	if cfg.ExecutionSettings.PrewarmLimit <= 0 {
		cfg.ExecutionSettings.PrewarmLimit = defaultPrewarmLimit
		configChanged = true
	}
	if cfg.ExecutionSettings.FullRebuildPriorityLimit <= 0 {
		cfg.ExecutionSettings.FullRebuildPriorityLimit = defaultFullRebuildPriorityLimit
		configChanged = true
	}
	if cfg.Recovery.AutoResume == nil {
		cfg.Recovery.AutoResume = boolPtr(true)
		configChanged = true
	}
	if cfg.Recovery.CheckpointBatchSize <= 0 {
		cfg.Recovery.CheckpointBatchSize = defaultCheckpointBatchSize
		configChanged = true
	}
	if cfg.Recovery.ResumeDelayMS <= 0 {
		cfg.Recovery.ResumeDelayMS = defaultResumeDelayMS
		configChanged = true
	}
	normalizedPool := uniqueResolverAddresses(splitResolverAddressesSlice(cfg.ExecutionSettings.RefreshResolverPool))
	if len(normalizedPool) != len(cfg.ExecutionSettings.RefreshResolverPool) {
		cfg.ExecutionSettings.RefreshResolverPool = normalizedPool
		configChanged = true
	}
	if cfg.Workflow.FlushMode == "" {
		if len(cfg.URLActions.FlushRules) > 0 && cfg.ExecutionSettings.RefreshResolverAddress == "" {
			cfg.Workflow.FlushMode = "legacy"
		} else {
			cfg.Workflow.FlushMode = "none"
		}
		configChanged = true
	}
	if cfg.Workflow.Mode == "" {
		if cfg.Scheduler.Enabled {
			cfg.Workflow.Mode = "hybrid"
		} else {
			cfg.Workflow.Mode = "manual"
		}
		configChanged = true
	}
	if cfg.Workflow.SaveBeforeRefresh == nil {
		cfg.Workflow.SaveBeforeRefresh = boolPtr(true)
		configChanged = true
	}
	if cfg.Workflow.SaveAfterRefresh == nil {
		cfg.Workflow.SaveAfterRefresh = boolPtr(true)
		configChanged = true
	}

	return configChanged
}

func configFromPersisted(cfg *Config) persistedConfig {
	return persistedConfig{
		DomainProcessing:  cfg.DomainProcessing,
		URLActions:        cfg.URLActions,
		Workflow:          cfg.Workflow,
		Scheduler:         cfg.Scheduler,
		Recovery:          cfg.Recovery,
		ExecutionSettings: cfg.ExecutionSettings,
	}
}

func cloneState(status Status, task *FullRebuildTask) persistedState {
	return persistedState{
		Status:          status,
		FullRebuildTask: cloneFullRebuildTask(task),
	}
}

func (p *Requery) loadConfig() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	cfg, configInitialized, err := p.loadPersistedConfigUnlocked()
	if err != nil {
		return err
	}
	p.config = &Config{
		DomainProcessing:  cfg.DomainProcessing,
		URLActions:        cfg.URLActions,
		Workflow:          cfg.Workflow,
		Scheduler:         cfg.Scheduler,
		Recovery:          cfg.Recovery,
		ExecutionSettings: cfg.ExecutionSettings,
	}
	configChanged := applyConfigDefaults(p.config)
	if configChanged {
		log.Println("[requery] Configuration defaults applied, saving updated config.")
	}

	stateChanged, err := p.loadStateUnlocked()
	if err != nil {
		return err
	}

	if configChanged || configInitialized {
		if err := p.saveConfigUnlocked(); err != nil {
			return fmt.Errorf("failed to save config after applying defaults: %w", err)
		}
	}
	if stateChanged {
		if err := p.saveStateUnlocked(); err != nil {
			return fmt.Errorf("failed to save requery state: %w", err)
		}
	}
	if err := p.syncRuntimeJobsLocked(); err != nil {
		return fmt.Errorf("failed to sync runtime requery jobs: %w", err)
	}
	return nil
}

func (p *Requery) saveConfigUnlocked() error {
	if p.config == nil {
		return errors.New("requery config is nil")
	}
	payload := configFromPersisted(p.config)
	if err := coremain.SaveRuntimeStateJSONToPath(p.runtimeDBPath(), runtimeStateNamespaceRequery, p.runtimeConfigKey(), payload); err != nil {
		return fmt.Errorf("failed to save runtime config state: %w", err)
	}
	if err := p.syncRuntimeJobsLocked(); err != nil {
		return fmt.Errorf("failed to sync runtime requery jobs: %w", err)
	}
	return nil
}

func (p *Requery) loadStateUnlocked() (bool, error) {
	var runtimeState persistedState
	if ok, err := coremain.LoadRuntimeStateJSONFromPath(p.runtimeDBPath(), runtimeStateNamespaceRequery, p.runtimeStateKey(), &runtimeState); err == nil && ok {
		p.status = runtimeState.Status
		if p.status.TaskState == "" {
			p.status.TaskState = "idle"
		}
		p.fullTask = cloneFullRebuildTask(runtimeState.FullRebuildTask)
		p.activeRunID = p.status.ActiveRunID
		if p.activeRunID == "" && p.fullTask != nil {
			p.activeRunID = p.fullTask.TaskID
			p.status.ActiveRunID = p.activeRunID
			return true, nil
		}
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("failed to load runtime requery state: %w", err)
	}

	p.status = Status{TaskState: "idle"}
	p.fullTask = nil
	p.activeRunID = ""
	return true, nil
}

func (p *Requery) saveStateUnlocked() error {
	payload := cloneState(p.status, p.fullTask)
	if err := coremain.SaveRuntimeStateJSONToPath(p.runtimeDBPath(), runtimeStateNamespaceRequery, p.runtimeStateKey(), payload); err != nil {
		return fmt.Errorf("failed to save runtime requery state: %w", err)
	}
	return nil
}

func (p *Requery) runtimeConfigKey() string {
	key := p.normalizedRuntimeKey()
	if key == "" {
		return ""
	}
	return key + ":config"
}

func (p *Requery) runtimeDBPath() string {
	if strings.TrimSpace(p.dbPath) != "" {
		return filepath.Clean(strings.TrimSpace(p.dbPath))
	}
	return coremain.RuntimeStateDBPath()
}

func (p *Requery) runtimeStateKey() string {
	key := p.normalizedRuntimeKey()
	if key == "" {
		return ""
	}
	return key + ":state"
}

func (p *Requery) normalizedRuntimeKey() string {
	key := strings.TrimSpace(p.runtimeKey)
	if key == "" {
		return ""
	}
	return filepath.Clean(key)
}

func (p *Requery) loadPersistedConfigUnlocked() (Config, bool, error) {
	var persisted persistedConfig
	if ok, err := coremain.LoadRuntimeStateJSONFromPath(p.runtimeDBPath(), runtimeStateNamespaceRequery, p.runtimeConfigKey(), &persisted); err == nil && ok {
		return Config{
			DomainProcessing:  persisted.DomainProcessing,
			URLActions:        persisted.URLActions,
			Workflow:          persisted.Workflow,
			Scheduler:         persisted.Scheduler,
			Recovery:          persisted.Recovery,
			ExecutionSettings: persisted.ExecutionSettings,
		}, false, nil
	} else if err != nil {
		return Config{}, false, fmt.Errorf("failed to load runtime requery config: %w", err)
	}

	log.Printf("[requery] runtime config %s not found, initializing with default config.", p.normalizedRuntimeKey())
	cfg := newDefaultConfig()
	return Config{
		DomainProcessing:  cfg.DomainProcessing,
		URLActions:        cfg.URLActions,
		Workflow:          cfg.Workflow,
		Scheduler:         cfg.Scheduler,
		Recovery:          cfg.Recovery,
		ExecutionSettings: cfg.ExecutionSettings,
	}, true, nil
}
