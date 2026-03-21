package coremain

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
	"go.uber.org/zap"
	"golang.org/x/net/proxy"
)

const ruleSourceManualSyncTimeout = 60 * time.Second

type ruleSourceService struct {
	manager *Mosdns
	scope   rulesource.Scope
}

func newRuleSourceService(m *Mosdns, scope rulesource.Scope) *ruleSourceService {
	return &ruleSourceService{manager: m, scope: scope}
}

func (s *ruleSourceService) List() ([]RuleSourceItem, error) {
	cfg, err := s.loadActiveConfig()
	if err != nil {
		return nil, err
	}
	statuses, err := ListRuleSourceStatusByScope(s.controlDBPath(), s.scope)
	if err != nil {
		return nil, err
	}
	bindings, err := listRuleSourceBindings(s.baseDir(), s.scope)
	if err != nil {
		return nil, err
	}
	items := make([]RuleSourceItem, 0, len(cfg.Sources))
	for _, source := range cfg.Sources {
		items = append(items, s.itemFromSource(source, statuses[source.ID], bindings))
	}
	return items, nil
}

func (s *ruleSourceService) Create(item RuleSourceItem) (RuleSourceItem, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return RuleSourceItem{}, err
	}
	source, err := s.sourceFromItem("", item)
	if err != nil {
		return RuleSourceItem{}, err
	}
	for _, current := range cfg.Sources {
		if current.ID == source.ID {
			return RuleSourceItem{}, NewRuleAPIError(http.StatusConflict, "RULE_SOURCE_ID_EXISTS", "规则源 ID 已存在")
		}
	}
	cfg.Sources = append(cfg.Sources, source)
	if err := s.saveConfig(cfg); err != nil {
		return RuleSourceItem{}, err
	}
	if err := s.reload(); err != nil {
		return RuleSourceItem{}, err
	}
	return s.Get(source.ID)
}

func (s *ruleSourceService) Update(id string, item RuleSourceItem) (RuleSourceItem, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return RuleSourceItem{}, err
	}
	index := indexRuleSource(cfg, id)
	if index < 0 {
		return RuleSourceItem{}, NewRuleAPIError(http.StatusNotFound, "RULE_SOURCE_NOT_FOUND", "规则源不存在")
	}
	previous := cfg.Sources[index]
	source, err := s.sourceFromItem(id, item)
	if err != nil {
		return RuleSourceItem{}, err
	}
	if source.ID != id && indexRuleSource(cfg, source.ID) >= 0 {
		return RuleSourceItem{}, NewRuleAPIError(http.StatusConflict, "RULE_SOURCE_ID_EXISTS", "规则源 ID 已存在")
	}
	cfg.Sources[index] = source
	if err := s.saveConfig(cfg); err != nil {
		return RuleSourceItem{}, err
	}
	if source.ID != id {
		if err := DeleteRuleSourceStatus(s.controlDBPath(), s.scope, id); err != nil {
			return RuleSourceItem{}, err
		}
	}
	cleanup := s.cleanupUpdatedSourceFile(previous, source, cfg)
	logRuleSourceFileCleanup("update", s.scope, source.ID, cleanup)
	if err := s.reload(); err != nil {
		return RuleSourceItem{}, err
	}
	return s.Get(source.ID)
}

func (s *ruleSourceService) Delete(id string) (RuleSourceDeleteResponse, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return RuleSourceDeleteResponse{}, err
	}
	index := indexRuleSource(cfg, id)
	if index < 0 {
		return RuleSourceDeleteResponse{}, NewRuleAPIError(http.StatusNotFound, "RULE_SOURCE_NOT_FOUND", "规则源不存在")
	}
	removed := cfg.Sources[index]
	cfg.Sources = append(cfg.Sources[:index], cfg.Sources[index+1:]...)
	if err := s.saveConfig(cfg); err != nil {
		return RuleSourceDeleteResponse{}, err
	}
	cleanup := s.cleanupRemovedSourceFile(removed, cfg)
	logRuleSourceFileCleanup("delete", s.scope, id, cleanup)
	if err := DeleteRuleSourceStatus(s.controlDBPath(), s.scope, id); err != nil {
		return RuleSourceDeleteResponse{}, err
	}
	if err := s.reload(); err != nil {
		return RuleSourceDeleteResponse{}, err
	}
	return RuleSourceDeleteResponse{
		Message:     "规则源已删除。",
		ID:          id,
		Scope:       string(s.scope),
		FileCleanup: cleanup,
	}, nil
}

func (s *ruleSourceService) Get(id string) (RuleSourceItem, error) {
	items, err := s.List()
	if err != nil {
		return RuleSourceItem{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return RuleSourceItem{}, NewRuleAPIError(http.StatusNotFound, "RULE_SOURCE_NOT_FOUND", "规则源不存在")
}

func (s *ruleSourceService) RefreshAll() ([]RuleSourceItem, error) {
	cfg, err := s.loadActiveConfig()
	if err != nil {
		return nil, err
	}
	for _, source := range cfg.Sources {
		if err := s.refreshSource(source); err != nil {
			return nil, err
		}
	}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s.List()
}

func (s *ruleSourceService) RefreshOne(id string) (RuleSourceItem, error) {
	source, err := LoadRuleSourceByIDForBaseDir(s.baseDir(), s.configFile(), s.scope, id)
	if err != nil {
		if isRuleSourceConfigEmptyError(err) {
			return RuleSourceItem{}, s.wrapConfigError(err)
		}
		return RuleSourceItem{}, NewRuleAPIError(http.StatusNotFound, "RULE_SOURCE_NOT_FOUND", "规则源不存在")
	}
	if err := s.refreshSource(source); err != nil {
		return RuleSourceItem{}, err
	}
	if err := s.reload(); err != nil {
		return RuleSourceItem{}, err
	}
	return s.Get(id)
}

func (s *ruleSourceService) loadConfig() (rulesource.Config, error) {
	switch s.scope {
	case rulesource.ScopeAdguard:
		cfg, _, err := LoadAdguardSourcesFromCustomConfigForBaseDir(s.baseDir())
		return cfg, err
	case rulesource.ScopeDiversion:
		cfg, _, err := LoadDiversionSourcesFromCustomConfigForBaseDir(s.baseDir())
		return cfg, err
	default:
		return rulesource.Config{}, fmt.Errorf("unsupported scope %q", s.scope)
	}
}

func (s *ruleSourceService) loadActiveConfig() (rulesource.Config, error) {
	configPath := resolvePolicyConfigPath(s.baseDir(), s.configFile())
	cfg, err := loadActiveRuleSourcesConfigAtPath(configPath, s.scope)
	if err != nil {
		return rulesource.Config{}, s.wrapConfigError(err)
	}
	return cfg, nil
}

func (s *ruleSourceService) saveConfig(cfg rulesource.Config) error {
	switch s.scope {
	case rulesource.ScopeAdguard:
		return SaveAdguardSourcesToCustomConfigForBaseDir(s.baseDir(), cfg)
	case rulesource.ScopeDiversion:
		return SaveDiversionSourcesToCustomConfigForBaseDir(s.baseDir(), cfg)
	default:
		return fmt.Errorf("unsupported scope %q", s.scope)
	}
}

func (s *ruleSourceService) configFile() string {
	switch s.scope {
	case rulesource.ScopeAdguard:
		return filepath.Join(customConfigDirname, adguardSourcesConfigFilename)
	case rulesource.ScopeDiversion:
		return filepath.Join(customConfigDirname, diversionSourcesConfigFilename)
	default:
		return ""
	}
}

func (s *ruleSourceService) wrapConfigError(err error) error {
	if !isRuleSourceConfigEmptyError(err) {
		return err
	}
	return NewRuleAPIError(
		http.StatusConflict,
		"RULE_SOURCE_CONFIG_EMPTY",
		fmt.Sprintf("规则源配置为空，请先编辑 %s", s.configFile()),
	)
}

func (s *ruleSourceService) itemFromSource(
	source rulesource.Source,
	status RuleSourceStatus,
	bindings map[string][]string,
) RuleSourceItem {
	key := ""
	if s.scope == rulesource.ScopeDiversion {
		key = source.BindTo
	}
	return RuleSourceItem{
		ID:                  source.ID,
		Name:                source.Name,
		BindTo:              source.BindTo,
		Bindings:            append([]string(nil), bindings[key]...),
		Enabled:             source.Enabled,
		Behavior:            source.Behavior,
		MatchMode:           source.MatchMode,
		Format:              source.Format,
		SourceKind:          source.SourceKind,
		Path:                effectiveSourcePath(s.scope, source),
		URL:                 source.URL,
		AutoUpdate:          source.AutoUpdate,
		UpdateIntervalHours: source.UpdateIntervalHours,
		RuleCount:           status.RuleCount,
		LastUpdated:         status.LastUpdated,
		LastError:           status.LastError,
	}
}

func (s *ruleSourceService) sourceFromItem(currentID string, item RuleSourceItem) (rulesource.Source, error) {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		id = strings.TrimSpace(currentID)
	}
	matchMode := rulesource.MatchMode(strings.ToLower(strings.TrimSpace(string(item.MatchMode))))
	sourceKind := rulesource.SourceKind(strings.ToLower(strings.TrimSpace(string(item.SourceKind))))
	source := rulesource.Source{
		ID:                  id,
		Name:                strings.TrimSpace(item.Name),
		BindTo:              strings.TrimSpace(item.BindTo),
		Enabled:             item.Enabled,
		Behavior:            behaviorFromMatchMode(matchMode),
		MatchMode:           matchMode,
		Format:              rulesource.Format(strings.ToLower(strings.TrimSpace(string(item.Format)))),
		SourceKind:          sourceKind,
		Path:                strings.TrimSpace(item.Path),
		URL:                 strings.TrimSpace(item.URL),
		AutoUpdate:          item.AutoUpdate,
		UpdateIntervalHours: item.UpdateIntervalHours,
	}
	if source.SourceKind == rulesource.SourceKindLocal {
		source.URL = ""
		source.AutoUpdate = false
		source.UpdateIntervalHours = 0
	}
	if source.Path == "" {
		source.Path = rulesource.DefaultRelativePath(s.scope, source)
	}
	if err := rulesource.ValidateSource(s.scope, source); err != nil {
		return rulesource.Source{}, NewRuleAPIError(http.StatusBadRequest, "RULE_SOURCE_INVALID", err.Error())
	}
	return source, nil
}

func (s *ruleSourceService) refreshSource(source rulesource.Source) error {
	socks5, err := resolveRuleSourceSocks5(s.manager, s.scope, source.BindTo)
	if err != nil {
		return NewRuleAPIError(http.StatusInternalServerError, "RULE_SOURCE_REFRESH_FAILED", err.Error())
	}
	client, err := newRuleSourceHTTPClient(socks5)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), ruleSourceManualSyncTimeout)
	defer cancel()
	_, err = SyncRuleSource(ctx, client, s.controlDBPath(), s.baseDir(), s.scope, source, true)
	if err != nil {
		return NewRuleAPIError(http.StatusBadGateway, "RULE_SOURCE_REFRESH_FAILED", err.Error())
	}
	return nil
}

func (s *ruleSourceService) reload() error {
	if s.manager == nil {
		return nil
	}
	if err := s.manager.ReloadControlConfig(""); err != nil {
		return NewRuleAPIError(http.StatusInternalServerError, "RULE_SOURCE_RELOAD_FAILED", err.Error())
	}
	return nil
}

func (s *ruleSourceService) baseDir() string {
	return runtimeBaseDir(s.manager)
}

func (s *ruleSourceService) controlDBPath() string {
	return runtimeControlDBPath(s.manager)
}

func behaviorFromMatchMode(matchMode rulesource.MatchMode) rulesource.Behavior {
	switch matchMode {
	case rulesource.MatchModeAdguardNative:
		return rulesource.BehaviorAdguard
	case rulesource.MatchModeIPCIDRSet:
		return rulesource.BehaviorIPCIDR
	default:
		return rulesource.BehaviorDomain
	}
}

func effectiveSourcePath(scope rulesource.Scope, source rulesource.Source) string {
	if strings.TrimSpace(source.Path) != "" {
		return source.Path
	}
	return rulesource.DefaultRelativePath(scope, source)
}

func indexRuleSource(cfg rulesource.Config, id string) int {
	for i, source := range cfg.Sources {
		if source.ID == id {
			return i
		}
	}
	return -1
}

func newRuleSourceHTTPClient(socks5 string) (*http.Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if strings.TrimSpace(socks5) != "" {
		dialer, err := proxy.SOCKS5("tcp", socks5, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("create socks5 dialer: %w", err)
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("socks5 dialer does not support context")
		}
		transport.DialContext = contextDialer.DialContext
		transport.Proxy = nil
	}
	return &http.Client{Timeout: ruleSourceManualSyncTimeout, Transport: transport}, nil
}

func logRuleSourceFileCleanup(
	action string,
	scope rulesource.Scope,
	sourceID string,
	cleanup RuleSourceFileCleanup,
) {
	if cleanup.Status != ruleSourceFileCleanupError {
		return
	}
	mlog.L().Warn("rule source file cleanup failed",
		zap.String("action", action),
		zap.String("scope", string(scope)),
		zap.String("source_id", sourceID),
		zap.String("path", cleanup.Path),
		zap.String("message", cleanup.Message))
}
