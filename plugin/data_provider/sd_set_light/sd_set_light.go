package sd_set_light

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	scdomain "github.com/sagernet/sing/common/domain"
	"github.com/sagernet/sing/common/varbin"
	"golang.org/x/net/proxy"
)

// [修改] 插件类型名称
const (
	PluginType       = "sd_set_light"
	downloadTimeout  = 60 * time.Second
	runtimeNamespace = "diversion_rule"
)

func init() {
	coremain.RegNewPluginFunc(PluginType, newSdSetLight, func() any { return new(Args) })
}

type Args struct {
	Socks5      string `yaml:"socks5,omitempty"`
	LocalConfig string `yaml:"local_config"`
}

type RuleSource struct {
	Name                string    `json:"name"`
	Type                string    `json:"type"`
	Files               string    `json:"files"`
	URL                 string    `json:"url"`
	Enabled             bool      `json:"enabled"`
	EnableRegexp        bool      `json:"enable_regexp,omitempty"`
	AutoUpdate          bool      `json:"auto_update"`
	UpdateIntervalHours int       `json:"update_interval_hours"`
	RuleCount           int       `json:"rule_count"`
	LastUpdated         time.Time `json:"last_updated"`
}

type SdSetLight struct {
	pluginTag string
	baseArgs  *Args

	// [优化] 移除 matcher atomic.Value

	mu      sync.RWMutex
	sources map[string]*RuleSource

	localConfigFile string
	httpClient      *http.Client
	ctx             context.Context
	cancel          context.CancelFunc

	subscribers []func()
	subsMu      sync.RWMutex
}

var _ data_provider.DomainMatcherProvider = (*SdSetLight)(nil)
var _ io.Closer = (*SdSetLight)(nil)
var _ data_provider.RuleExporter = (*SdSetLight)(nil)
var _ coremain.ControlConfigReloader = (*SdSetLight)(nil)

// 接口定义，用于解耦
type RuleReceiver interface {
	Add(string, struct{}) error
}

// 字符串收集器 (用于 GetRules)
type ruleCollector struct {
	rules []string
}

func (c *ruleCollector) Add(s string, _ struct{}) error {
	c.rules = append(c.rules, s)
	return nil
}

// [新增] 计数器收集器 (用于 reloadAllRules，只计数不存数据，极度省内存)
type counterCollector struct {
	count int
}

func (c *counterCollector) Add(_ string, _ struct{}) error {
	c.count++
	return nil
}

// Subscribe 实现 RuleExporter
func (p *SdSetLight) Subscribe(cb func()) {
	p.subsMu.Lock()
	defer p.subsMu.Unlock()
	p.subscribers = append(p.subscribers, cb)
}

// 2. 修改 GetRules：改为实时流式读取，不留驻留内存
func (p *SdSetLight) GetRules() ([]string, error) {
	p.mu.RLock()
	type srcInfo struct {
		path   string
		regexp bool
		count  int
	}
	var tasks []srcInfo
	totalExpected := 0
	for _, src := range p.sources {
		if src.Enabled && src.Files != "" {
			tasks = append(tasks, srcInfo{src.Files, src.EnableRegexp, src.RuleCount})
			totalExpected += src.RuleCount
		}
	}
	p.mu.RUnlock()

	// 这里的 collector.rules 是临时的，会被 domain_mapper 使用后由其触发 GC 释放
	collector := &ruleCollector{rules: make([]string, 0, totalExpected)}
	for _, task := range tasks {
		f, err := os.Open(task.path)
		if err != nil {
			continue
		}
		tryLoadSRS(f, collector, task.regexp)
		f.Close()
	}
	return collector.rules, nil
}

func (p *SdSetLight) notifySubscribers() {
	p.subsMu.RLock()
	subs := make([]func(), len(p.subscribers))
	copy(subs, p.subscribers)
	p.subsMu.RUnlock()

	for _, cb := range subs {
		go cb()
	}
}

func newSdSetLight(bp *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)
	if cfg.LocalConfig == "" {
		return nil, fmt.Errorf("%s: 'local_config' must be specified", PluginType)
	}

	httpClient, err := newHTTPClient(cfg.Socks5)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	baseArgs := cloneArgs(cfg)
	if rawArgs, ok := bp.RawArgs().(*Args); ok && rawArgs != nil {
		baseArgs = cloneArgs(rawArgs)
	}

	p := &SdSetLight{
		pluginTag:       bp.Tag(),
		baseArgs:        baseArgs,
		sources:         make(map[string]*RuleSource),
		localConfigFile: cfg.LocalConfig,
		httpClient:      httpClient,
		ctx:             ctx,
		cancel:          cancel,
		subscribers:     make([]func(), 0),
	}
	// [优化] 不再初始化 matcher

	if err := p.loadConfig(); err != nil {
		log.Printf("[%s] failed to load config file: %v. Starting with empty config.", PluginType, err)
	}

	if err := p.reloadAllRules(); err != nil {
		log.Printf("[%s] failed to perform initial rule load: %v", PluginType, err)
	}

	go p.backgroundUpdater()

	return p, nil
}

func cloneArgs(src *Args) *Args {
	if src == nil {
		return new(Args)
	}
	return &Args{
		Socks5:      src.Socks5,
		LocalConfig: src.LocalConfig,
	}
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
	if socks5 != "" {
		log.Printf("[%s] using SOCKS5 proxy: %s", PluginType, socks5)
		dialer, err := proxy.SOCKS5("tcp", socks5, nil, proxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("%s: failed to create SOCKS5 dialer: %w", PluginType, err)
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("%s: created dialer does not support context", PluginType)
		}
		transport.DialContext = contextDialer.DialContext
		transport.Proxy = nil
	}
	return &http.Client{
		Timeout:   downloadTimeout,
		Transport: transport,
	}, nil
}

func (p *SdSetLight) Close() error {
	log.Printf("[%s] closing...", PluginType)
	p.cancel()
	return nil
}

func (p *SdSetLight) GetDomainMatcher() domain.Matcher[struct{}] {
	return p
}

// Match [重要修改] 恒定返回 false
func (p *SdSetLight) Match(domainStr string) (value struct{}, ok bool) {
	return struct{}{}, false
}

func (p *SdSetLight) ListDiversionRules() ([]coremain.DiversionRuleItem, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	items := make([]coremain.DiversionRuleItem, 0, len(p.sources))
	for _, src := range p.sources {
		items = append(items, coremain.DiversionRuleItem{
			Name:                src.Name,
			Type:                src.Type,
			Files:               src.Files,
			URL:                 src.URL,
			Enabled:             src.Enabled,
			EnableRegexp:        src.EnableRegexp,
			AutoUpdate:          src.AutoUpdate,
			UpdateIntervalHours: src.UpdateIntervalHours,
			RuleCount:           src.RuleCount,
			LastUpdated:         src.LastUpdated,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}

func (p *SdSetLight) UpsertDiversionRule(name string, item coremain.DiversionRuleItem) (coremain.DiversionRuleItem, bool, error) {
	if strings.TrimSpace(item.Files) == "" || strings.TrimSpace(item.URL) == "" {
		return coremain.DiversionRuleItem{}, false, coremain.NewRuleAPIError(http.StatusBadRequest, "RULES_DIVERSION_INVALID", "'files' and 'url' fields are required")
	}

	reqData := RuleSource{
		Name:                name,
		Type:                item.Type,
		Files:               item.Files,
		URL:                 item.URL,
		Enabled:             item.Enabled,
		EnableRegexp:        item.EnableRegexp,
		AutoUpdate:          item.AutoUpdate,
		UpdateIntervalHours: item.UpdateIntervalHours,
	}

	var created bool
	var updatedSource *RuleSource
	p.mu.Lock()
	existing, isUpdate := p.sources[name]
	if isUpdate {
		existing.Type = reqData.Type
		existing.Files = reqData.Files
		existing.URL = reqData.URL
		existing.Enabled = reqData.Enabled
		existing.EnableRegexp = reqData.EnableRegexp
		existing.AutoUpdate = reqData.AutoUpdate
		existing.UpdateIntervalHours = reqData.UpdateIntervalHours
		updatedSource = existing
	} else {
		reqData.RuleCount = 0
		reqData.LastUpdated = time.Time{}
		p.sources[name] = &reqData
		updatedSource = &reqData
		created = true
	}
	p.mu.Unlock()

	if err := p.saveConfig(); err != nil {
		return coremain.DiversionRuleItem{}, false, coremain.NewRuleAPIError(http.StatusInternalServerError, "RULES_DIVERSION_SAVE_FAILED", "failed to save config")
	}

	go p.reloadAllRules()

	return coremain.DiversionRuleItem{
		Name:                updatedSource.Name,
		Type:                updatedSource.Type,
		Files:               updatedSource.Files,
		URL:                 updatedSource.URL,
		Enabled:             updatedSource.Enabled,
		EnableRegexp:        updatedSource.EnableRegexp,
		AutoUpdate:          updatedSource.AutoUpdate,
		UpdateIntervalHours: updatedSource.UpdateIntervalHours,
		RuleCount:           updatedSource.RuleCount,
		LastUpdated:         updatedSource.LastUpdated,
	}, created, nil
}

func (p *SdSetLight) DeleteDiversionRule(name string) error {
	var srcToDelete *RuleSource
	p.mu.Lock()
	src, ok := p.sources[name]
	if ok {
		srcToDelete = src
		delete(p.sources, name)
	}
	p.mu.Unlock()
	if !ok {
		return coremain.NewRuleAPIError(http.StatusNotFound, "RULES_DIVERSION_NOT_FOUND", "source not found")
	}
	if srcToDelete.Files != "" {
		if err := os.Remove(srcToDelete.Files); err != nil && !os.IsNotExist(err) {
			log.Printf("[%s] WARN: failed to delete srs file %s: %v", PluginType, srcToDelete.Files, err)
		}
	}
	if err := p.saveConfig(); err != nil {
		return coremain.NewRuleAPIError(http.StatusInternalServerError, "RULES_DIVERSION_SAVE_FAILED", "failed to save config")
	}
	go p.reloadAllRules()
	return nil
}

func (p *SdSetLight) TriggerDiversionRuleUpdate(name string) error {
	p.mu.RLock()
	_, ok := p.sources[name]
	p.mu.RUnlock()
	if !ok {
		return coremain.NewRuleAPIError(http.StatusNotFound, "RULES_DIVERSION_NOT_FOUND", "source not found")
	}

	go func() {
		log.Printf("[%s] manual update triggered for source '%s'.", PluginType, name)
		updateCtx, cancel := context.WithTimeout(p.ctx, downloadTimeout)
		defer cancel()
		if err := p.downloadAndUpdateLocalFile(updateCtx, name); err != nil {
			log.Printf("[%s] ERROR: failed to manually update source '%s': %v", PluginType, name, err)
			return
		}
		log.Printf("[%s] manual update for '%s' successful, triggering reload.", PluginType, name)
		p.reloadAllRules()
		coremain.ManualGC()
	}()
	return nil
}

func (p *SdSetLight) ReloadControlConfig(global *coremain.GlobalOverrides, _ []coremain.UpstreamOverrideConfig) error {
	effective := new(Args)
	if err := coremain.DecodeRawArgsWithGlobalOverrides(p.pluginTag, p.baseArgs, effective, global); err != nil {
		return err
	}
	if effective.LocalConfig == "" {
		return fmt.Errorf("%s: 'local_config' must be specified", PluginType)
	}

	httpClient, err := newHTTPClient(effective.Socks5)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.localConfigFile = effective.LocalConfig
	p.httpClient = httpClient
	p.mu.Unlock()

	if err := p.loadConfig(); err != nil {
		return err
	}
	return p.reloadAllRules()
}

func (p *SdSetLight) loadConfig() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if key := p.runtimeConfigKey(); key != "" {
		var sources []*RuleSource
		ok, err := coremain.LoadRuntimeStateJSONFromPath(p.runtimeDBPath(), runtimeNamespace, key, &sources)
		if err == nil && ok {
			p.sources = make(map[string]*RuleSource, len(sources))
			for _, src := range sources {
				if src == nil || src.Name == "" {
					continue
				}
				p.sources[src.Name] = src
			}
			log.Printf("[%s] loaded %d rule sources from runtime store", PluginType, len(p.sources))
			return nil
		}
		if err != nil {
			return err
		}
	}

	data, err := os.ReadFile(p.localConfigFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[%s] config file not found at %s, will create a new one.", PluginType, p.localConfigFile)
			return nil
		}
		return err
	}
	if len(data) == 0 {
		p.sources = make(map[string]*RuleSource)
		return nil
	}

	var sources []*RuleSource
	if err := json.Unmarshal(data, &sources); err != nil {
		return fmt.Errorf("failed to parse config json: %w", err)
	}

	if len(sources) == 0 {
		log.Printf("[%s] WARN: config file %s is not empty, but parsed 0 rules. Treating as empty config.", PluginType, p.localConfigFile)
	}

	p.sources = make(map[string]*RuleSource, len(sources))
	for _, src := range sources {
		if src.Name == "" {
			log.Printf("[%s] WARN: found a rule source with empty name, skipping.", PluginType)
			continue
		}
		p.sources[src.Name] = src
	}
	log.Printf("[%s] loaded %d rule sources from %s", PluginType, len(p.sources), p.localConfigFile)
	return nil
}

func (p *SdSetLight) saveConfig() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	sourcesSnapshot := make([]*RuleSource, 0, len(p.sources))
	for _, src := range p.sources {
		s := *src
		sourcesSnapshot = append(sourcesSnapshot, &s)
	}

	sort.Slice(sourcesSnapshot, func(i, j int) bool {
		return sourcesSnapshot[i].Name < sourcesSnapshot[j].Name
	})

	data, err := json.MarshalIndent(sourcesSnapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config to json: %w", err)
	}
	if key := p.runtimeConfigKey(); key != "" {
		if err := coremain.SaveRuntimeStateJSONToPath(p.runtimeDBPath(), runtimeNamespace, key, sourcesSnapshot); err != nil {
			return fmt.Errorf("failed to save config to runtime store: %w", err)
		}
	}

	tmpFile := p.localConfigFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("failed to write to temporary config file: %w", err)
	}
	if err := os.Rename(tmpFile, p.localConfigFile); err != nil {
		return fmt.Errorf("failed to rename temporary config to final: %w", err)
	}
	return nil
}

func (p *SdSetLight) runtimeDBPath() string {
	return coremain.RuntimeStateDBPathForPath(p.localConfigFile)
}

func (p *SdSetLight) runtimeConfigKey() string {
	if p.localConfigFile == "" {
		return ""
	}
	return filepath.Clean(p.localConfigFile)
}

func (p *SdSetLight) reloadAllRules() error {
	log.Printf("[%s] starting lightweight rule scan (zero-cache mode)...", PluginType)

	p.mu.RLock()
	sourcesSnapshot := make([]*RuleSource, 0, len(p.sources))
	for _, src := range p.sources {
		if src.Enabled {
			sourcesSnapshot = append(sourcesSnapshot, src)
		}
	}
	p.mu.RUnlock()

	rulesCountUpdated := false

	for _, src := range sourcesSnapshot {
		if src.Files == "" {
			continue
		}

		// 流式打开文件，不读取内容到内存
		f, err := os.Open(src.Files)
		if err != nil {
			log.Printf("[%s] WARN: cannot open source file %s: %v", PluginType, src.Files, err)
			continue
		}

		// 使用计数器收集器：只计数，不产生任何字符串对象，不产生任何内存压力
		counter := &counterCollector{}
		ok, count, _ := tryLoadSRS(f, counter, src.EnableRegexp) // 确保 tryLoadSRS 已改为 io.Reader 版本
		f.Close()

		if !ok {
			log.Printf("[%s] ERROR: failed to scan SRS file for source '%s'", PluginType, src.Name)
			continue
		}

		p.mu.Lock()
		if s, ok := p.sources[src.Name]; ok && s.RuleCount != count {
			s.RuleCount = count
			rulesCountUpdated = true
		}
		p.mu.Unlock()
	}

	if rulesCountUpdated {
		if err := p.saveConfig(); err != nil {
			log.Printf("[%s] ERROR: failed to save config after scan: %v", PluginType, err)
		}
	}

	// 通知订阅者（domain_mapper）开始重建
	// 此时 domain_mapper 会调用 GetRules()，那里的 rules 是临时产生的
	p.notifySubscribers()

	// 异步清理扫描期间产生的极其微量的临时对象
	go func() {
		time.Sleep(1 * time.Second)
		coremain.ManualGC()
	}()

	return nil
}

func (p *SdSetLight) downloadAndUpdateLocalFile(ctx context.Context, sourceName string) error {
	p.mu.RLock()
	source, ok := p.sources[sourceName]
	if !ok {
		p.mu.RUnlock()
		return fmt.Errorf("source '%s' not found", sourceName)
	}
	sourceURL := source.URL
	localFile := source.Files
	enableRegexp := source.EnableRegexp
	httpClient := p.httpClient
	p.mu.RUnlock()

	log.Printf("[%s] downloading %s", PluginType, sourceName)
	req, err := http.NewRequestWithContext(ctx, "GET", sourceURL, nil)
	if err != nil {
		return err
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	// 这里依然需要读入内存进行校验，使用 bytes.NewReader 适配新接口
	srsData, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// 校验逻辑
	counter := &counterCollector{}
	ok, count, _ := tryLoadSRS(bytes.NewReader(srsData), counter, enableRegexp) // 修复调用点
	if !ok {
		return fmt.Errorf("invalid SRS data")
	}

	if err := os.MkdirAll(filepath.Dir(localFile), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(localFile, srsData, 0644); err != nil {
		return err
	}

	p.mu.Lock()
	if source, ok := p.sources[sourceName]; ok {
		source.RuleCount = count
		source.LastUpdated = time.Now()
	}
	p.mu.Unlock()

	return p.saveConfig()
}

func (p *SdSetLight) backgroundUpdater() {
	select {
	case <-time.After(1 * time.Minute):
	case <-p.ctx.Done():
		return
	}
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.mu.RLock()
			var sourcesToUpdate []string
			for name, src := range p.sources {
				if src.Enabled && src.AutoUpdate && src.UpdateIntervalHours > 0 {
					if time.Since(src.LastUpdated).Hours() >= float64(src.UpdateIntervalHours) {
						sourcesToUpdate = append(sourcesToUpdate, name)
					}
				}
			}
			p.mu.RUnlock()
			if len(sourcesToUpdate) == 0 {
				continue
			}
			log.Printf("[%s] auto-update: found %d source(s) that need updating.", PluginType, len(sourcesToUpdate))
			var wg sync.WaitGroup
			for _, name := range sourcesToUpdate {
				wg.Add(1)
				go func(sourceName string) {
					defer wg.Done()
					updateCtx, cancel := context.WithTimeout(p.ctx, downloadTimeout)
					defer cancel()
					if err := p.downloadAndUpdateLocalFile(updateCtx, sourceName); err != nil {
						log.Printf("[%s] ERROR: failed to auto-update source '%s': %v", PluginType, sourceName, err)
					}
				}(name)
			}
			wg.Wait()
			log.Printf("[%s] auto-update: downloads finished, triggering reload.", PluginType)
			p.reloadAllRules()
			coremain.ManualGC()
		case <-p.ctx.Done():
			log.Printf("[%s] background updater is shutting down.", PluginType)
			return
		}
	}
}

var (
	magicBytes            = [3]byte{0x53, 0x52, 0x53}
	ruleItemDomain        = uint8(2)
	ruleItemDomainKeyword = uint8(3)
	ruleItemDomainRegex   = uint8(4)
	ruleItemFinal         = uint8(0xFF)
)

const ruleSetVersionCurrent = 3

// 修改：参数 m 类型改为 RuleReceiver
// 修改：参数 b 改为 io.Reader，函数名保持 tryLoadSRS 避免其他地方报错
func tryLoadSRS(r io.Reader, m RuleReceiver, enableRegexp bool) (ok bool, count int, lastRule string) {
	var mb [3]byte
	if _, err := io.ReadFull(r, mb[:]); err != nil || mb != magicBytes {
		return false, 0, ""
	}
	var version uint8
	if err := binary.Read(r, binary.BigEndian, &version); err != nil || version > ruleSetVersionCurrent {
		return false, 0, ""
	}
	zr, err := zlib.NewReader(r)
	if err != nil {
		return false, 0, ""
	}
	defer zr.Close()
	br := bufio.NewReader(zr)
	length, err := binary.ReadUvarint(br)
	if err != nil {
		return false, 0, ""
	}
	for i := uint64(0); i < length; i++ {
		count += readRuleCompat(br, m, &lastRule, enableRegexp)
	}
	return true, count, lastRule
}

// 修改：参数 m 类型改为 RuleReceiver
func readRuleCompat(r *bufio.Reader, m RuleReceiver, last *string, enableRegexp bool) int {
	ct := 0
	mode, err := r.ReadByte()
	if err != nil {
		return 0
	}
	switch mode {
	case 0:
		ct += readDefaultRuleCompat(r, m, last, enableRegexp)
	case 1:
		r.ReadByte()
		n, _ := binary.ReadUvarint(r)
		for i := uint64(0); i < n; i++ {
			ct += readRuleCompat(r, m, last, enableRegexp)
		}
		r.ReadByte()
	}
	return ct
}

// 修改：参数 m 类型改为 RuleReceiver
func readDefaultRuleCompat(r *bufio.Reader, m RuleReceiver, last *string, enableRegexp bool) int {
	count := 0
	for {
		item, err := r.ReadByte()
		if err != nil {
			break
		}
		switch item {
		case ruleItemDomain:
			matcher, err := scdomain.ReadMatcher(r)
			if err != nil {
				return count
			}
			doms, suffix := matcher.Dump()
			for _, d := range doms {
				*last = "full:" + d
				if m.Add(*last, struct{}{}) == nil {
					count++
				}
			}
			for _, d := range suffix {
				*last = "domain:" + d
				if m.Add(*last, struct{}{}) == nil {
					count++
				}
			}
		case ruleItemDomainKeyword:
			sl, _ := varbin.ReadValue[[]string](r, binary.BigEndian)
			for _, d := range sl {
				*last = "keyword:" + d
				if m.Add(*last, struct{}{}) == nil {
					count++
				}
			}
		case ruleItemDomainRegex:
			sl, _ := varbin.ReadValue[[]string](r, binary.BigEndian)
			if enableRegexp {
				for _, d := range sl {
					*last = "regexp:" + d
					if m.Add(*last, struct{}{}) == nil {
						count++
					}
				}
			}
		case ruleItemFinal:
			return count
		default:
			return count
		}
	}
	return count
}
