package sd_set_light

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
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
	PluginType      = "sd_set_light"
	syncTimeout     = 60 * time.Second
	syncCheckPeriod = 10 * time.Minute
	scope           = rulesource.ScopeDiversion
)

func init() {
	coremain.RegNewPluginFunc(PluginType, newSdSetLight, func() any { return new(Args) })
}

type Args struct {
	Socks5     string `yaml:"socks5,omitempty"`
	ConfigFile string `yaml:"config_file"`
	BindTo     string `yaml:"bind_to"`
}

type SdSetLight struct {
	pluginTag string
	baseArgs  *Args
	baseDir   string

	mu         sync.RWMutex
	sources    []rulesource.Source
	configFile string
	bindTo     string
	rules      []string
	syncState  []coremain.RuleSourceVersion
	httpClient *http.Client
	ctx        context.Context
	cancel     context.CancelFunc

	subsMu      sync.RWMutex
	subscribers []func()
}

var _ data_provider.DomainMatcherProvider = (*SdSetLight)(nil)
var _ data_provider.RuleExporter = (*SdSetLight)(nil)
var _ coremain.ControlConfigReloader = (*SdSetLight)(nil)
var _ coremain.ListContentController = (*SdSetLight)(nil)
var _ io.Closer = (*SdSetLight)(nil)

func newSdSetLight(bp *coremain.BP, args any) (any, error) {
	cfg := cloneArgs(args.(*Args))
	client, err := newHTTPClient(cfg.Socks5)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &SdSetLight{
		pluginTag:   bp.Tag(),
		baseArgs:    cfg,
		baseDir:     bp.BaseDir(),
		configFile:  cfg.ConfigFile,
		bindTo:      cfg.BindTo,
		httpClient:  client,
		ctx:         ctx,
		cancel:      cancel,
		subscribers: make([]func(), 0),
	}
	if err := p.loadSources(); err != nil {
		return nil, err
	}
	if err := p.reloadAllRules(coremain.StartupRuleSourceSyncOptions()); err != nil {
		return nil, err
	}
	go p.backgroundSync()
	return p, nil
}

func cloneArgs(src *Args) *Args {
	if src == nil {
		return &Args{}
	}
	return &Args{Socks5: src.Socks5, ConfigFile: src.ConfigFile, BindTo: src.BindTo}
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

func (p *SdSetLight) Close() error {
	p.cancel()
	return nil
}

func (p *SdSetLight) GetDomainMatcher() domain.Matcher[struct{}] {
	return p
}

func (p *SdSetLight) Match(string) (struct{}, bool) {
	return struct{}{}, false
}

func (p *SdSetLight) Subscribe(callback func()) {
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	p.subscribers = append(p.subscribers, callback)
}

func (p *SdSetLight) GetRules() ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.rules...), nil
}

func (p *SdSetLight) GetRulesShared() ([]string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.rules, nil
}

func (p *SdSetLight) ReloadControlConfig(global *coremain.GlobalOverrides, _ []coremain.UpstreamOverrideConfig) error {
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
	p.bindTo = effective.BindTo
	p.httpClient = client
	p.mu.Unlock()
	if err := p.loadSources(); err != nil {
		return err
	}
	return p.reloadAllRules(ruleSourceReloadOptions(global))
}

func (p *SdSetLight) ListEntries(query string, offset, limit int) ([]coremain.ListEntry, int, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	query = strings.ToLower(strings.TrimSpace(query))
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = len(p.rules)
		if limit <= 0 {
			limit = 1
		}
	}

	items := make([]coremain.ListEntry, 0, min(limit, len(p.rules)))
	matchedCount := 0
	for _, rule := range p.rules {
		if query != "" && !strings.Contains(strings.ToLower(rule), query) {
			continue
		}
		matchedCount++
		if matchedCount <= offset {
			continue
		}
		items = append(items, coremain.ListEntry{Value: rule})
		if len(items) >= limit {
			break
		}
	}
	return items, matchedCount, nil
}

func (p *SdSetLight) ReplaceListRuntime(_ context.Context, values []string) (int, error) {
	source, localPath, err := p.writableLocalSource()
	if err != nil {
		return 0, err
	}
	normalized, err := normalizeWritableDomainRules(source, values)
	if err != nil {
		return 0, err
	}
	if err := writeSdSetLightRulesFile(localPath, normalized); err != nil {
		return 0, err
	}
	p.resetSyncState()
	if err := p.reloadAllRules(coremain.RuleSourceSyncOptions{}); err != nil {
		return 0, err
	}
	return len(normalized), nil
}

func (p *SdSetLight) loadSources() error {
	configFile, bindTo := p.currentBinding()
	if strings.TrimSpace(configFile) == "" || strings.TrimSpace(bindTo) == "" {
		return fmt.Errorf("%s: config_file and bind_to are required", PluginType)
	}
	sources, err := coremain.LoadRuleSourcesByBindingForBaseDir(p.currentBaseDir(), configFile, scope, bindTo)
	if err != nil {
		return err
	}
	for _, source := range sources {
		if source.Behavior != rulesource.BehaviorDomain || source.MatchMode != rulesource.MatchModeDomainSet {
			return fmt.Errorf("%s: source %s is not a domain_set source", PluginType, source.ID)
		}
	}
	p.mu.Lock()
	p.sources = append([]rulesource.Source(nil), sources...)
	p.mu.Unlock()
	return nil
}

func (p *SdSetLight) writableLocalSource() (rulesource.Source, string, error) {
	sources := p.sourceSnapshot()
	enabled := make([]rulesource.Source, 0, 1)
	for _, source := range sources {
		if source.Enabled {
			enabled = append(enabled, source)
		}
	}
	if len(enabled) != 1 {
		return rulesource.Source{}, "", fmt.Errorf("list %s is backed by %d enabled sources and is read-only", p.pluginTag, len(enabled))
	}
	source := enabled[0]
	if source.SourceKind != rulesource.SourceKindLocal {
		return rulesource.Source{}, "", fmt.Errorf("list %s source %s is %s and is read-only", p.pluginTag, source.ID, source.SourceKind)
	}
	switch source.Format {
	case rulesource.FormatTXT, rulesource.FormatList, rulesource.FormatRules:
	default:
		return rulesource.Source{}, "", fmt.Errorf("list %s source %s format %s is read-only", p.pluginTag, source.ID, source.Format)
	}
	localPath, err := rulesource.ResolveLocalPath(p.currentBaseDir(), scope, source)
	if err != nil {
		return rulesource.Source{}, "", err
	}
	return source, localPath, nil
}

func normalizeWritableDomainRules(source rulesource.Source, values []string) ([]string, error) {
	var builder strings.Builder
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		builder.WriteString(value)
		builder.WriteByte('\n')
	}
	rules, err := rulesource.ParseDomainBytes(source.Format, []byte(builder.String()))
	if err != nil {
		return nil, err
	}
	return rules, nil
}

func writeSdSetLightRulesFile(path string, rules []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	writer := bufio.NewWriter(f)
	for _, rule := range rules {
		if _, err := writer.WriteString(rule + "\n"); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func (p *SdSetLight) reloadAllRules(options coremain.RuleSourceSyncOptions) error {
	type syncPlan struct {
		source rulesource.Source
		result *coremain.RuleSourceSyncResult
	}

	inspectOptions := options
	inspectOptions.MetadataOnly = true
	plans := make([]syncPlan, 0)
	nextSyncState := make([]coremain.RuleSourceVersion, 0)
	for _, source := range p.sourceSnapshot() {
		if !source.Enabled {
			continue
		}
		ctx, cancel := context.WithTimeout(p.ctx, syncTimeout)
		result, err := coremain.SyncRuleSource(ctx, p.httpClient, p.runtimeDBPath(), p.currentBaseDir(), scope, source, inspectOptions)
		cancel()
		if err != nil {
			p.setRules(nil, nil)
			return err
		}
		plans = append(plans, syncPlan{source: source, result: result})
		nextSyncState = append(nextSyncState, coremain.NewRuleSourceVersion(source.ID, result))
	}
	if coremain.RuleSourceVersionsEqual(p.currentSyncState(), nextSyncState) {
		return nil
	}

	rules := make([]string, 0)
	for _, plan := range plans {
		source := plan.source
		result := plan.result
		if result.MissingCache {
			continue
		}
		if result.Data == nil {
			ctx, cancel := context.WithTimeout(p.ctx, syncTimeout)
			loaded, err := coremain.SyncRuleSource(ctx, p.httpClient, p.runtimeDBPath(), p.currentBaseDir(), scope, source, options)
			cancel()
			if err != nil {
				p.setRules(nil, nil)
				return err
			}
			result = loaded
		}
		sourceRules, err := rulesource.ParseDomainBytes(source.Format, result.Data)
		if err != nil {
			p.setRules(nil, nil)
			return err
		}
		rules = append(rules, sourceRules...)
	}
	p.setRules(rules, nextSyncState)
	p.notifySubscribers()
	return nil
}

func ruleSourceReloadOptions(global *coremain.GlobalOverrides) coremain.RuleSourceSyncOptions {
	if coremain.RuleSourceReloadMode(global) == "definitions" {
		return coremain.StartupRuleSourceSyncOptions()
	}
	return coremain.BackgroundRuleSourceSyncOptions()
}

func (p *SdSetLight) setRules(rules []string, syncState []coremain.RuleSourceVersion) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = rules
	p.syncState = append([]coremain.RuleSourceVersion(nil), syncState...)
}

func (p *SdSetLight) backgroundSync() {
	ticker := time.NewTicker(syncCheckPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := p.loadSources(); err == nil {
				_ = p.reloadAllRules(coremain.BackgroundRuleSourceSyncOptions())
			}
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *SdSetLight) notifySubscribers() {
	p.subsMu.RLock()
	subs := append([]func(){}, p.subscribers...)
	p.subsMu.RUnlock()
	for _, callback := range subs {
		go callback()
	}
}

func (p *SdSetLight) currentBinding() (string, string) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.configFile, p.bindTo
}

func (p *SdSetLight) currentSyncState() []coremain.RuleSourceVersion {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]coremain.RuleSourceVersion(nil), p.syncState...)
}

func (p *SdSetLight) resetSyncState() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.syncState = nil
}

func (p *SdSetLight) sourceSnapshot() []rulesource.Source {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]rulesource.Source(nil), p.sources...)
}

func (p *SdSetLight) runtimeDBPath() string {
	baseDir := p.currentBaseDir()
	if baseDir != "" {
		return coremain.RuntimeStateDBPathForBaseDir(baseDir)
	}
	configFile, _ := p.currentBinding()
	return coremain.RuntimeStateDBPathForPath(configFile)
}

func (p *SdSetLight) currentBaseDir() string {
	if strings.TrimSpace(p.baseDir) != "" {
		return p.baseDir
	}
	return strings.TrimSpace(coremain.MainConfigBaseDir)
}
