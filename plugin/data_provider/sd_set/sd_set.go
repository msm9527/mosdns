package sd_set

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/pkg/rulesource"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	"golang.org/x/net/proxy"
)

const (
	PluginType      = "sd_set"
	syncTimeout     = 60 * time.Second
	syncCheckPeriod = 10 * time.Minute
	scope           = rulesource.ScopeDiversion
)

func init() {
	coremain.RegNewPluginFunc(PluginType, newSdSet, func() any { return new(Args) })
}

type Args struct {
	Socks5     string `yaml:"socks5,omitempty"`
	ConfigFile string `yaml:"config_file"`
	SourceID   string `yaml:"source_id"`
}

type SdSet struct {
	pluginTag string
	baseArgs  *Args

	matcher atomic.Value

	mu         sync.RWMutex
	source     rulesource.Source
	configFile string
	sourceID   string
	rules      []string
	httpClient *http.Client
	ctx        context.Context
	cancel     context.CancelFunc

	subsMu       sync.RWMutex
	subscribers  []func()
}

var _ data_provider.DomainMatcherProvider = (*SdSet)(nil)
var _ data_provider.RuleExporter = (*SdSet)(nil)
var _ coremain.ControlConfigReloader = (*SdSet)(nil)
var _ io.Closer = (*SdSet)(nil)

func newSdSet(bp *coremain.BP, args any) (any, error) {
	cfg := cloneArgs(args.(*Args))
	client, err := newHTTPClient(cfg.Socks5)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &SdSet{
		pluginTag:    bp.Tag(),
		baseArgs:     cfg,
		configFile:   cfg.ConfigFile,
		sourceID:     cfg.SourceID,
		httpClient:   client,
		ctx:          ctx,
		cancel:       cancel,
		subscribers:  make([]func(), 0),
	}
	p.matcher.Store(domain.NewDomainMixMatcher())
	if err := p.loadSource(); err != nil {
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
	return &Args{Socks5: src.Socks5, ConfigFile: src.ConfigFile, SourceID: src.SourceID}
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

func (p *SdSet) Close() error {
	p.cancel()
	return nil
}

func (p *SdSet) GetDomainMatcher() domain.Matcher[struct{}] {
	return p
}

func (p *SdSet) Match(domainStr string) (struct{}, bool) {
	return p.matcher.Load().(*domain.MixMatcher[struct{}]).Match(domainStr)
}

func (p *SdSet) Subscribe(callback func()) {
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	p.subscribers = append(p.subscribers, callback)
}

func (p *SdSet) GetRules() ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.rules...), nil
}

func (p *SdSet) ReloadControlConfig(global *coremain.GlobalOverrides, _ []coremain.UpstreamOverrideConfig) error {
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
	p.sourceID = effective.SourceID
	p.httpClient = client
	p.mu.Unlock()
	if err := p.loadSource(); err != nil {
		return err
	}
	return p.reloadAllRules(false)
}

func (p *SdSet) loadSource() error {
	configFile, sourceID := p.currentBinding()
	if strings.TrimSpace(configFile) == "" || strings.TrimSpace(sourceID) == "" {
		return fmt.Errorf("%s: config_file and source_id are required", PluginType)
	}
	source, err := coremain.LoadRuleSourceByID(configFile, scope, sourceID)
	if err != nil {
		return err
	}
	if source.Behavior != rulesource.BehaviorDomain || source.MatchMode != rulesource.MatchModeDomainSet {
		return fmt.Errorf("%s: source %s is not a domain_set source", PluginType, sourceID)
	}
	p.mu.Lock()
	p.source = source
	p.mu.Unlock()
	return nil
}

func (p *SdSet) reloadAllRules(forceRemote bool) error {
	source := p.sourceSnapshot()
	if !source.Enabled {
		p.setRules(nil)
		return nil
	}
	ctx, cancel := context.WithTimeout(p.ctx, syncTimeout)
	defer cancel()
	result, err := coremain.SyncRuleSource(ctx, p.httpClient, p.runtimeDBPath(), coremain.MainConfigBaseDir, scope, source, forceRemote)
	if err != nil {
		p.setRules(nil)
		return err
	}
	rules, err := rulesource.ParseDomainBytes(source.Format, result.Data)
	if err != nil {
		p.setRules(nil)
		return err
	}
	next := domain.NewDomainMixMatcher()
	for _, rule := range rules {
		if err := next.Add(rule, struct{}{}); err != nil {
			return err
		}
	}
	p.matcher.Store(next)
	p.setRules(rules)
	p.notifySubscribers()
	return nil
}

func (p *SdSet) setRules(rules []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = append([]string(nil), rules...)
}

func (p *SdSet) backgroundSync() {
	ticker := time.NewTicker(syncCheckPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := p.loadSource(); err == nil {
				_ = p.reloadAllRules(false)
			}
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *SdSet) notifySubscribers() {
	p.subsMu.RLock()
	subs := append([]func(){}, p.subscribers...)
	p.subsMu.RUnlock()
	for _, callback := range subs {
		go callback()
	}
}

func (p *SdSet) currentBinding() (string, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.configFile, p.sourceID
}

func (p *SdSet) sourceSnapshot() rulesource.Source {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.source
}

func (p *SdSet) runtimeDBPath() string {
	configFile, _ := p.currentBinding()
	return coremain.RuntimeStateDBPathForPath(configFile)
}
