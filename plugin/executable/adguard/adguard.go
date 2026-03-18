package adguard_rule

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	"golang.org/x/net/proxy"
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

	mu           sync.RWMutex
	configFile   string
	sources      []rulesource.Source
	allowMatcher *domain.MixMatcher[struct{}]
	denyMatcher  *domain.MixMatcher[struct{}]
	denyRules    []string
	httpClient   *http.Client
	ctx          context.Context
	cancel       context.CancelFunc

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
		pluginTag:    bp.Tag(),
		baseArgs:     cfg,
		configFile:   cfg.ConfigFile,
		allowMatcher: domain.NewDomainMixMatcher(),
		denyMatcher:  domain.NewDomainMixMatcher(),
		httpClient:   client,
		ctx:          ctx,
		cancel:       cancel,
		subscribers:  make([]func(), 0),
	}
	if err := p.loadSources(); err != nil {
		return nil, err
	}
	if err := p.reloadAllRules(false); err != nil {
		return nil, err
	}
	go p.backgroundSync()
	return p, nil
}

func cloneArgs(src *Args) *Args {
	if src == nil {
		return &Args{}
	}
	return &Args{Socks5: src.Socks5, ConfigFile: src.ConfigFile}
}

func newHTTPClient(socks5 string) (*http.Client, error) {
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
			return nil, fmt.Errorf("%s: create socks5 dialer: %w", PluginType, err)
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("%s: socks5 dialer does not support context", PluginType)
		}
		transport.DialContext = contextDialer.DialContext
		transport.Proxy = nil
	}
	return &http.Client{Timeout: syncTimeout, Transport: transport}, nil
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
	return p.reloadAllRules(false)
}

func (p *AdguardRule) loadSources() error {
	configFile := coremain.ResolveMainConfigPath(p.currentConfigFile())
	if strings.TrimSpace(configFile) == "" {
		return fmt.Errorf("%s: config_file is required", PluginType)
	}
	cfg, _, err := coremain.LoadAdguardSourcesConfigAtPath(configFile)
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

func (p *AdguardRule) reloadAllRules(forceRemote bool) error {
	sources := p.sourceSnapshot()
	allowMatcher := domain.NewDomainMixMatcher()
	denyMatcher := domain.NewDomainMixMatcher()
	denyRules := make([]string, 0)

	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		ctx, cancel := context.WithTimeout(p.ctx, syncTimeout)
		result, err := coremain.SyncRuleSource(ctx, p.httpClient, p.runtimeDBPath(), coremain.MainConfigBaseDir, scope, source, forceRemote)
		cancel()
		if err != nil {
			return err
		}
		if err := mergeSourceRules(source, result.Data, allowMatcher, denyMatcher, &denyRules); err != nil {
			return err
		}
	}

	p.mu.Lock()
	p.allowMatcher = allowMatcher
	p.denyMatcher = denyMatcher
	p.denyRules = append([]string(nil), denyRules...)
	p.mu.Unlock()
	p.notifySubscribers()
	return nil
}

func mergeSourceRules(
	source rulesource.Source,
	data []byte,
	allowMatcher *domain.MixMatcher[struct{}],
	denyMatcher *domain.MixMatcher[struct{}],
	denyRules *[]string,
) error {
	if source.Behavior == rulesource.BehaviorAdguard {
		result, err := rulesource.ParseAdguardBytes(source.Format, data)
		if err != nil {
			return err
		}
		return mergeAdguardResult(result, allowMatcher, denyMatcher, denyRules)
	}
	rules, err := rulesource.ParseDomainBytes(source.Format, data)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if err := denyMatcher.Add(rule, struct{}{}); err != nil {
			return err
		}
		*denyRules = append(*denyRules, rule)
	}
	return nil
}

func mergeAdguardResult(
	result rulesource.AdguardResult,
	allowMatcher *domain.MixMatcher[struct{}],
	denyMatcher *domain.MixMatcher[struct{}],
	denyRules *[]string,
) error {
	for _, rule := range result.Allow {
		if err := allowMatcher.Add(rule, struct{}{}); err != nil {
			return err
		}
	}
	for _, rule := range result.Deny {
		if err := denyMatcher.Add(rule, struct{}{}); err != nil {
			return err
		}
		*denyRules = append(*denyRules, rule)
	}
	return nil
}

func (p *AdguardRule) backgroundSync() {
	ticker := time.NewTicker(syncCheckPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := p.loadSources(); err == nil {
				_ = p.reloadAllRules(false)
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
	return coremain.RuntimeStateDBPathForPath(p.currentConfigFile())
}
