package adguard_rule

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
)

const (
	PluginType      = "adguard_rule"
	syncTimeout     = 60 * time.Second
	syncCheckPeriod = time.Minute
	scope           = rulesource.ScopeAdguard
)

func init() {
	coremain.RegNewPluginFunc(PluginType, newAdguardRule, func() any { return new(Args) })
}

type Args struct {
	Socks5     string `yaml:"socks5,omitempty"`
	ConfigFile string `yaml:"config_file"`
}

type AdguardRule struct {
	pluginTag string
	baseArgs  *Args
	baseDir   string

	mu                    sync.RWMutex
	configFile            string
	sources               []rulesource.Source
	importantAllowMatcher *domain.MixMatcher[struct{}]
	importantDenyMatcher  *domain.MixMatcher[struct{}]
	allowMatcher          *domain.MixMatcher[struct{}]
	denyMatcher           *domain.MixMatcher[struct{}]
	denyRules             []string
	httpClient            *http.Client
	ctx                   context.Context
	cancel                context.CancelFunc

	subsMu      sync.RWMutex
	subscribers []func()
}

var _ data_provider.DomainMatcherProvider = (*AdguardRule)(nil)
var _ data_provider.RuleExporter = (*AdguardRule)(nil)
var _ coremain.ControlConfigReloader = (*AdguardRule)(nil)
var _ io.Closer = (*AdguardRule)(nil)

func newAdguardRule(bp *coremain.BP, args any) (any, error) {
	cfg := cloneArgs(args.(*Args))
	client, err := newHTTPClient(cfg.Socks5)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &AdguardRule{
		pluginTag:             bp.Tag(),
		baseArgs:              cfg,
		baseDir:               bp.BaseDir(),
		configFile:            cfg.ConfigFile,
		importantAllowMatcher: domain.NewDomainMixMatcher(),
		importantDenyMatcher:  domain.NewDomainMixMatcher(),
		allowMatcher:          domain.NewDomainMixMatcher(),
		denyMatcher:           domain.NewDomainMixMatcher(),
		httpClient:            client,
		ctx:                   ctx,
		cancel:                cancel,
		subscribers:           make([]func(), 0),
	}
	if err := p.loadSources(); err != nil {
		return nil, err
	}
	if err := p.reloadAllRules(coremain.RuleSourceSyncOptions{PreferCache: true}); err != nil {
		return nil, err
	}
	go p.backgroundSync()
	return p, nil
}

func (p *AdguardRule) Close() error {
	p.cancel()
	return nil
}

func (p *AdguardRule) GetDomainMatcher() domain.Matcher[struct{}] {
	return p
}

func (p *AdguardRule) Match(domainStr string) (struct{}, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if _, matched := p.importantAllowMatcher.Match(domainStr); matched {
		return struct{}{}, false
	}
	if _, matched := p.importantDenyMatcher.Match(domainStr); matched {
		return struct{}{}, true
	}
	if _, matched := p.allowMatcher.Match(domainStr); matched {
		return struct{}{}, false
	}
	if _, matched := p.denyMatcher.Match(domainStr); matched {
		return struct{}{}, true
	}
	return struct{}{}, false
}

func (p *AdguardRule) Subscribe(callback func()) {
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	p.subscribers = append(p.subscribers, callback)
}

func (p *AdguardRule) GetRules() ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.denyRules...), nil
}

func (p *AdguardRule) ReloadControlConfig(global *coremain.GlobalOverrides, _ []coremain.UpstreamOverrideConfig) error {
	effective := new(Args)
	if err := coremain.DecodeRawArgsWithGlobalOverrides(p.pluginTag, p.baseArgs, effective, global); err != nil {
		return err
	}
	client, err := newHTTPClient(effective.Socks5)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.baseArgs = cloneArgs(effective)
	p.configFile = effective.ConfigFile
	p.httpClient = client
	p.mu.Unlock()
	if err := p.loadSources(); err != nil {
		return err
	}
	return p.reloadAllRules(coremain.RuleSourceSyncOptions{})
}

func (p *AdguardRule) loadSources() error {
	configFile := coremain.ResolveMainConfigPathForBaseDir(p.currentBaseDir(), p.currentConfigFile())
	if strings.TrimSpace(configFile) == "" {
		return fmt.Errorf("%s: config_file is required", PluginType)
	}
	cfg, err := coremain.LoadActiveAdguardSourcesConfigAtPath(configFile)
	if err != nil {
		return err
	}
	for _, source := range cfg.Sources {
		if source.Behavior == rulesource.BehaviorAdguard && source.MatchMode == rulesource.MatchModeAdguardNative {
			continue
		}
		if source.Behavior == rulesource.BehaviorDomain && source.MatchMode == rulesource.MatchModeDomainSet {
			continue
		}
		return fmt.Errorf("%s: invalid adguard source %s", PluginType, source.ID)
	}
	p.mu.Lock()
	p.sources = append([]rulesource.Source(nil), cfg.Sources...)
	p.mu.Unlock()
	keepIDs := make(map[string]struct{}, len(cfg.Sources))
	for _, source := range cfg.Sources {
		keepIDs[source.ID] = struct{}{}
	}
	return coremain.PruneRuleSourceStatus(p.runtimeDBPath(), scope, keepIDs)
}

func (p *AdguardRule) reloadAllRules(options coremain.RuleSourceSyncOptions) error {
	sources := p.sourceSnapshot()
	importantAllowMatcher := domain.NewDomainMixMatcher()
	importantDenyMatcher := domain.NewDomainMixMatcher()
	allowMatcher := domain.NewDomainMixMatcher()
	denyMatcher := domain.NewDomainMixMatcher()
	denyRules := make([]string, 0)

	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		ctx, cancel := context.WithTimeout(p.ctx, syncTimeout)
		result, err := coremain.SyncRuleSource(ctx, p.httpClient, p.runtimeDBPath(), p.currentBaseDir(), scope, source, options)
		cancel()
		if err != nil {
			return err
		}
		if err := mergeSourceRules(
			source,
			result.Data,
			importantAllowMatcher,
			importantDenyMatcher,
			allowMatcher,
			denyMatcher,
			&denyRules,
		); err != nil {
			return err
		}
	}

	p.mu.Lock()
	p.importantAllowMatcher = importantAllowMatcher
	p.importantDenyMatcher = importantDenyMatcher
	p.allowMatcher = allowMatcher
	p.denyMatcher = denyMatcher
	p.denyRules = append([]string(nil), denyRules...)
	p.mu.Unlock()
	p.notifySubscribers()
	return nil
}

func (p *AdguardRule) backgroundSync() {
	ticker := time.NewTicker(syncCheckPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := p.loadSources(); err == nil {
				_ = p.reloadAllRules(coremain.RuleSourceSyncOptions{})
			}
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *AdguardRule) notifySubscribers() {
	p.subsMu.RLock()
	subs := append([]func(){}, p.subscribers...)
	p.subsMu.RUnlock()
	for _, callback := range subs {
		go callback()
	}
}

func (p *AdguardRule) currentConfigFile() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.configFile
}

func (p *AdguardRule) sourceSnapshot() []rulesource.Source {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]rulesource.Source(nil), p.sources...)
}

func (p *AdguardRule) runtimeDBPath() string {
	baseDir := p.currentBaseDir()
	if baseDir != "" {
		return coremain.RuntimeStateDBPathForBaseDir(baseDir)
	}
	return coremain.RuntimeStateDBPathForPath(p.currentConfigFile())
}

func (p *AdguardRule) currentBaseDir() string {
	if strings.TrimSpace(p.baseDir) != "" {
		return p.baseDir
	}
	return strings.TrimSpace(coremain.MainConfigBaseDir)
}
