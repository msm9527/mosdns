package requery

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultURLCallDelayMS           = 50
	defaultURLCallConcurrency       = 4
	defaultFullQPS                  = 100
	defaultQuickQPS                 = 300
	defaultPrewarmQPS               = 500
	defaultDateRangeDays            = 30
	defaultMaxQueueSize             = 2048
	defaultOnDemandBatchSize        = 32
	defaultQuickRebuildLimit        = 2000
	defaultPrewarmLimit             = 1000
	defaultFullRebuildPriorityLimit = 4000
	defaultCheckpointBatchSize      = 256
	defaultResumeDelayMS            = 1500
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

func applyConfigDefaults(cfg *Config) bool {
	if cfg == nil {
		return false
	}

	configChanged := false

	if cfg.Status.TaskState == "" {
		cfg.Status.TaskState = "idle"
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

func stateFilePath(configPath string) string {
	ext := filepath.Ext(configPath)
	if ext == "" {
		return configPath + ".state.json"
	}
	return strings.TrimSuffix(configPath, ext) + ".state" + ext
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

	dataBytes, err := os.ReadFile(p.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[requery] config file %s not found, initializing with default empty config.", p.filePath)
			p.config = newDefaultConfig()
			p.status = Status{TaskState: "idle"}
			p.fullTask = nil
			if err := p.saveConfigUnlocked(); err != nil {
				return err
			}
			return p.saveStateUnlocked()
		}
		return err
	}

	var cfg Config
	if err := json.Unmarshal(dataBytes, &cfg); err != nil {
		return fmt.Errorf("failed to parse json from config file %s: %w", p.filePath, err)
	}
	legacyStatus := cfg.Status
	legacyTask := cloneFullRebuildTask(cfg.FullRebuildTask)
	legacyEmbeddedState := legacyStatus.TaskState != "" || legacyTask != nil
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

	stateChanged, err := p.loadStateUnlocked(legacyStatus, legacyTask)
	if err != nil {
		return err
	}

	if configChanged || legacyEmbeddedState {
		if err := p.saveConfigUnlocked(); err != nil {
			return fmt.Errorf("failed to save config after applying defaults: %w", err)
		}
	}
	if stateChanged {
		if err := p.saveStateUnlocked(); err != nil {
			return fmt.Errorf("failed to save requery state: %w", err)
		}
	}
	return nil
}

func (p *Requery) saveConfigUnlocked() error {
	if p.config == nil {
		return errors.New("requery config is nil")
	}
	dataBytes, err := json.MarshalIndent(configFromPersisted(p.config), "", "  ")
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

func (p *Requery) loadStateUnlocked(legacyStatus Status, legacyTask *FullRebuildTask) (bool, error) {
	statePath := stateFilePath(p.filePath)
	dataBytes, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			p.status = legacyStatus
			if p.status.TaskState == "" {
				p.status.TaskState = "idle"
			}
			p.fullTask = cloneFullRebuildTask(legacyTask)
			return true, nil
		}
		return false, fmt.Errorf("failed to read state file %s: %w", statePath, err)
	}

	var state persistedState
	if err := json.Unmarshal(dataBytes, &state); err != nil {
		return false, fmt.Errorf("failed to parse json from state file %s: %w", statePath, err)
	}
	p.status = state.Status
	if p.status.TaskState == "" {
		p.status.TaskState = "idle"
	}
	p.fullTask = cloneFullRebuildTask(state.FullRebuildTask)
	return false, nil
}

func (p *Requery) saveStateUnlocked() error {
	dataBytes, err := json.MarshalIndent(cloneState(p.status, p.fullTask), "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state to json: %w", err)
	}

	statePath := stateFilePath(p.filePath)
	tmpFile := statePath + ".tmp"
	if err := os.WriteFile(tmpFile, dataBytes, 0644); err != nil {
		return fmt.Errorf("failed to write temporary state file: %w", err)
	}
	if err := os.Rename(tmpFile, statePath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("failed to rename temporary state file: %w", err)
	}

	return nil
}
