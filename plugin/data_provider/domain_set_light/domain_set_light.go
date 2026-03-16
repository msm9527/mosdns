package domain_set_light

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	scdomain "github.com/sagernet/sing/common/domain"
	"github.com/sagernet/sing/common/varbin"
	"go.uber.org/zap"
)

// [修改] 插件类型名称
const PluginType = "domain_set_light"

var fileWatchInterval = 2 * time.Second

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
}

type Args struct {
	Exps          []string `yaml:"exps"`
	Sets          []string `yaml:"sets"` // 保留字段以防配置文件报错，但内部不再加载
	Files         []string `yaml:"files"`
	GeneratedFrom string   `yaml:"generated_from"`
}

// 接口实现检查
var _ data_provider.DomainMatcherProvider = (*DomainSetLight)(nil)
var _ domain.Matcher[struct{}] = (*DomainSetLight)(nil)
var _ data_provider.RuleExporter = (*DomainSetLight)(nil)

// 定义一个简单的接口，用于复用 SRS 解析逻辑（解耦 Trie 树依赖）
type ruleAdder interface {
	Add(string, struct{}) error
}

// 字符串收集器，用于替代 MixMatcher 接收解析出来的规则
type stringCollector struct {
	rules *[]string
}

func (c *stringCollector) Add(s string, _ struct{}) error {
	*c.rules = append(*c.rules, s)
	return nil
}

type watchedFileState struct {
	exists  bool
	size    int64
	modTime time.Time
}

type DomainSetLight struct {
	bp        *coremain.BP
	pluginTag string
	baseArgs  *Args
	curArgs   *Args

	mu sync.RWMutex
	// [优化] 移除了 heavy 的 mixM 和 otherM
	// mixM   *domain.MixMatcher[struct{}]
	// otherM []domain.Matcher[struct{}]

	ruleFile      string
	generatedFrom string
	rules         []string // 仅维护字符串列表，内存占用极低

	// 新增：订阅者列表
	subscribers []func()
	fileStates  map[string]watchedFileState
}

var _ coremain.ControlConfigReloader = (*DomainSetLight)(nil)

// GetRules 实现 RuleExporter 接口
func (d *DomainSetLight) GetRules() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	// 返回规则的副本
	rulesCopy := make([]string, len(d.rules))
	copy(rulesCopy, d.rules)
	return rulesCopy, nil
}

// Subscribe 实现 RuleExporter 接口
func (d *DomainSetLight) Subscribe(cb func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.subscribers = append(d.subscribers, cb)
}

// notifySubscribers 通知所有订阅者
func (d *DomainSetLight) notifySubscribers() {
	d.mu.RLock()
	subs := make([]func(), len(d.subscribers))
	copy(subs, d.subscribers)
	d.mu.RUnlock()

	for _, cb := range subs {
		go cb()
	}
}

// initAndLoadRules 加载规则到字符串切片
func (d *DomainSetLight) initAndLoadRules(exps, files []string, generatedFrom string) ([]string, error) {
	allRules := make([]string, 0, len(exps)+len(files)*100)

	// Load from expressions
	allRules = append(allRules, exps...)

	if generatedFrom != "" {
		rules, err := d.loadGeneratedRuntimeRules(generatedFrom)
		if err != nil {
			return nil, err
		}
		allRules = append(allRules, rules...)
	}

	// Load from files
	for i, f := range files {
		rules, err := d.loadFileInternal(f)
		if err != nil {
			return nil, fmt.Errorf("failed to load file %d %s: %w", i, f, err)
		}
		allRules = append(allRules, rules...)
	}

	return allRules, nil
}

// loadFileInternal 读取文件内容并解析为规则字符串
func (d *DomainSetLight) loadFileInternal(f string) ([]string, error) {
	if f == "" {
		return nil, nil
	}
	b, source, err := readDomainRulesSource(f)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return nil, nil
	}

	// 1. 尝试作为 SRS 解析
	var srsRules []string
	collector := &stringCollector{rules: &srsRules}
	if ok, count, last := tryLoadSRS(b, collector); ok {
		fmt.Printf("[%s] loaded %d rules from srs file: %s (last rule: %s)\n", PluginType, count, f, last)
		return srsRules, nil
	}

	// 2. 作为普通文本解析
	var rules []string
	var lastTxt string
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// [优化] 直接存入字符串，无需 MixMatcher.Add 的开销
		rules = append(rules, line)
		lastTxt = line
	}

	if len(rules) > 0 {
		fmt.Printf("[%s] loaded %d rules from %s: %s (last rule: %s)\n", PluginType, len(rules), source, f, lastTxt)
	}
	return rules, scanner.Err()
}

func (d *DomainSetLight) loadGeneratedRuntimeRules(generatedFrom string) ([]string, error) {
	key := coremain.DomainOutputRuleDatasetKey(generatedFrom)
	if key == "" {
		return nil, nil
	}
	dataset, ok, err := coremain.LoadGeneratedDatasetFromPath(coremain.RuntimeStateDBPath(), key)
	if err != nil {
		return nil, err
	}
	if !ok || !isGeneratedRuleDatasetFormat(dataset.Format) || dataset.Content == "" {
		return nil, nil
	}

	rules := make([]string, 0, 64)
	scanner := bufio.NewScanner(strings.NewReader(dataset.Content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules = append(rules, line)
	}
	return rules, scanner.Err()
}

func Init(bp *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)
	baseArgs := cloneArgs(cfg)
	if rawArgs, ok := bp.RawArgs().(*Args); ok && rawArgs != nil {
		baseArgs = cloneArgs(rawArgs)
	}
	ds := &DomainSetLight{
		bp:          bp,
		pluginTag:   bp.Tag(),
		baseArgs:    baseArgs,
		curArgs:     cloneArgs(cfg),
		subscribers: make([]func(), 0),
		fileStates:  make(map[string]watchedFileState),
	}

	if len(cfg.Files) > 0 {
		ds.ruleFile = cfg.Files[0]
	}
	ds.generatedFrom = strings.TrimSpace(cfg.GeneratedFrom)

	// 使用新的加载逻辑
	loadedRules, err := ds.initAndLoadRules(cfg.Exps, cfg.Files, cfg.GeneratedFrom)
	if err != nil {
		return nil, fmt.Errorf("failed to load rules: %w", err)
	}
	ds.rules = loadedRules
	ds.updateWatchedFilesLocked(cfg.Files)

	// [注意] 这里故意忽略了 cfg.Sets 的处理
	// 因为本插件不负责匹配，不需要持有其他插件的引用

	ds.startFileWatcher()
	return ds, nil
}

func cloneArgs(src *Args) *Args {
	if src == nil {
		return new(Args)
	}
	return &Args{
		Exps:          append([]string(nil), src.Exps...),
		Sets:          append([]string(nil), src.Sets...),
		Files:         append([]string(nil), src.Files...),
		GeneratedFrom: src.GeneratedFrom,
	}
}

func (d *DomainSetLight) GetDomainMatcher() domain.Matcher[struct{}] {
	return d
}

// Match [重要修改] 恒定返回 false，不占用 CPU，不查找 Trie
func (d *DomainSetLight) Match(domainStr string) (value struct{}, ok bool) {
	return struct{}{}, false
}

func (d *DomainSetLight) ReloadControlConfig(global *coremain.GlobalOverrides, _ []coremain.UpstreamOverrideConfig) error {
	effective := new(Args)
	if err := coremain.DecodeRawArgsWithGlobalOverrides(d.pluginTag, d.baseArgs, effective, global); err != nil {
		return err
	}

	tmp := &DomainSetLight{}
	loadedRules, err := tmp.initAndLoadRules(effective.Exps, effective.Files, effective.GeneratedFrom)
	if err != nil {
		return fmt.Errorf("failed to load rules: %w", err)
	}

	ruleFile := ""
	if len(effective.Files) > 0 {
		ruleFile = effective.Files[0]
	}

	d.mu.Lock()
	d.curArgs = cloneArgs(effective)
	d.ruleFile = ruleFile
	d.generatedFrom = strings.TrimSpace(effective.GeneratedFrom)
	d.rules = loadedRules
	d.updateWatchedFilesLocked(effective.Files)
	d.mu.Unlock()

	d.notifySubscribers()
	go func() {
		time.Sleep(1 * time.Second)
		coremain.ManualGC()
	}()
	return nil
}

func (d *DomainSetLight) startFileWatcher() {
	go func() {
		ticker := time.NewTicker(fileWatchInterval)
		defer ticker.Stop()
		for range ticker.C {
			if err := d.pollWatchedFiles(); err != nil {
				d.bp.L().Warn("domain_set_light file watcher reload failed",
					zap.String("plugin", d.pluginTag),
					zap.Error(err))
			}
		}
	}()
}

func (d *DomainSetLight) pollWatchedFiles() error {
	d.mu.RLock()
	files := append([]string(nil), d.curArgs.Files...)
	prevStates := make(map[string]watchedFileState, len(d.fileStates))
	for k, v := range d.fileStates {
		prevStates[k] = v
	}
	d.mu.RUnlock()

	changed := false
	newStates := make(map[string]watchedFileState, len(files))
	for _, file := range files {
		state, err := statWatchedFile(file)
		if err != nil {
			return err
		}
		newStates[file] = state
		if prev, ok := prevStates[file]; !ok || prev != state {
			changed = true
		}
	}
	if !changed && len(prevStates) != len(newStates) {
		changed = true
	}
	if !changed {
		return nil
	}
	return d.reloadCurrentArgs(newStates)
}

func (d *DomainSetLight) reloadCurrentArgs(fileStates map[string]watchedFileState) error {
	d.mu.RLock()
	effective := cloneArgs(d.curArgs)
	d.mu.RUnlock()

	tmp := &DomainSetLight{}
	loadedRules, err := tmp.initAndLoadRules(effective.Exps, effective.Files, effective.GeneratedFrom)
	if err != nil {
		return fmt.Errorf("failed to reload rules from files: %w", err)
	}

	ruleFile := ""
	if len(effective.Files) > 0 {
		ruleFile = effective.Files[0]
	}

	d.mu.Lock()
	d.ruleFile = ruleFile
	d.generatedFrom = strings.TrimSpace(effective.GeneratedFrom)
	d.rules = loadedRules
	d.fileStates = fileStates
	d.mu.Unlock()

	d.notifySubscribers()
	return nil
}

func (d *DomainSetLight) updateWatchedFilesLocked(files []string) {
	d.fileStates = make(map[string]watchedFileState, len(files))
	for _, file := range files {
		state, err := statWatchedFile(file)
		if err != nil {
			continue
		}
		d.fileStates[file] = state
	}
}

func statWatchedFile(path string) (watchedFileState, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return watchedFileState{}, nil
		}
		return watchedFileState{}, err
	}
	return watchedFileState{
		exists:  true,
		size:    info.Size(),
		modTime: info.ModTime(),
	}, nil
}

func (d *DomainSetLight) ListEntries(query string, offset, limit int) ([]coremain.ListEntry, int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	query = strings.ToLower(query)
	if limit <= 0 {
		limit = len(d.rules)
		if limit <= 0 {
			limit = 1
		}
	}
	if offset < 0 {
		offset = 0
	}

	items := make([]coremain.ListEntry, 0)
	matchedCount := 0
	sentCount := 0
	for _, rule := range d.rules {
		found := query == "" || strings.Contains(strings.ToLower(rule), query)
		if !found {
			continue
		}
		matchedCount++
		if matchedCount <= offset {
			continue
		}
		items = append(items, coremain.ListEntry{Value: rule})
		sentCount++
		if sentCount >= limit {
			break
		}
	}
	return items, matchedCount, nil
}

func (d *DomainSetLight) ReplaceListRuntime(ctx context.Context, values []string) (int, error) {
	if d.generatedFrom == "" && (d.ruleFile == "" || !strings.EqualFold(filepath.Ext(d.ruleFile), ".txt")) {
		return 0, fmt.Errorf("no txt file configured, cannot post")
	}

	d.mu.Lock()
	d.rules = append([]string(nil), values...)
	d.mu.Unlock()

	if d.generatedFrom != "" {
		content := strings.Join(values, "\n")
		if content != "" {
			content += "\n"
		}
		if err := coremain.SaveGeneratedDatasetEntryToPath(
			coremain.RuntimeStateDBPath(),
			coremain.DomainOutputRuleDatasetKey(d.generatedFrom),
			coremain.GeneratedDatasetFormatDomainOutputRule,
			content,
			"",
		); err != nil {
			return 0, err
		}
	} else {
		if err := writeRulesToFile(d.ruleFile, values); err != nil {
			return 0, err
		}
	}

	d.notifySubscribers()
	return len(values), nil
}

// ==============================================================

func writeRulesToFile(path string, rules []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	writer := bufio.NewWriter(f)
	for _, r := range rules {
		if _, err := writer.WriteString(r + "\n"); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func readDomainRulesSource(path string) ([]byte, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "text file", nil
		}
		return nil, "", err
	}
	return b, "text file", nil
}

func isGeneratedRuleDatasetFormat(format string) bool {
	switch format {
	case coremain.GeneratedDatasetFormatDomainOutputRule, coremain.GeneratedDatasetFormatDomainOutputGeneratedRule:
		return true
	default:
		return false
	}
}

// --- SRS 解析函数 (修改为适配 ruleAdder 接口) ---

func tryLoadSRS(b []byte, m ruleAdder) (bool, int, string) {
	r := bytes.NewReader(b)
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
	count := 0
	var lastRule string
	for i := uint64(0); i < length; i++ {
		count += readRuleCompat(br, m, &lastRule)
	}
	return true, count, lastRule
}

var (
	magicBytes            = [3]byte{0x53, 0x52, 0x53}
	ruleItemDomain        = uint8(2)
	ruleItemDomainKeyword = uint8(3)
	ruleItemDomainRegex   = uint8(4)
	ruleItemFinal         = uint8(0xFF)
)

const ruleSetVersionCurrent = 3

// 修改签名接收 ruleAdder
func readRuleCompat(r *bufio.Reader, m ruleAdder, last *string) int {
	ct := 0
	mode, err := r.ReadByte()
	if err != nil {
		return 0
	}
	switch mode {
	case 0:
		ct += readDefaultRuleCompat(r, m, last)
	case 1:
		r.ReadByte()
		n, _ := binary.ReadUvarint(r)
		for i := uint64(0); i < n; i++ {
			ct += readRuleCompat(r, m, last)
		}
		r.ReadByte()
	}
	return ct
}

// 修改签名接收 ruleAdder
func readDefaultRuleCompat(r *bufio.Reader, m ruleAdder, last *string) int {
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
			for _, d := range sl {
				*last = "regexp:" + d
				if m.Add(*last, struct{}{}) == nil {
					count++
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
