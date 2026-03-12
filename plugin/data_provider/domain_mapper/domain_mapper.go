package domain_mapper

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"go.uber.org/zap"
)

const PluginType = "domain_mapper"

var reservedFastMarks = map[uint8]string{
	39: "指定客户端直连标记 / designated-client bypass flag",
	48: "UDP 快路径 client_ip 已匹配标记 / UDP fast-path client_ip matched flag",
}

func init() {
	coremain.RegNewPluginFunc(PluginType, NewMapper, func() any { return new(Args) })
}

type RuleConfig struct {
	Tag       string `yaml:"tag"`
	Mark      uint8  `yaml:"mark"`
	OutputTag string `yaml:"output_tag"`
}

type Args struct {
	Rules       []RuleConfig `yaml:"rules"`
	DefaultMark uint8        `yaml:"default_mark"`
	DefaultTag  string       `yaml:"default_tag"`
}

type MatchResult struct {
	Marks      []uint8
	JoinedTags string
}

type DomainMapper struct {
	bp          *coremain.BP
	pluginTag   string
	baseArgs    *Args
	logger      *zap.Logger
	matcher     atomic.Value
	updateMu    sync.Mutex
	updateTimer *time.Timer
	ruleConfigs []RuleConfig
	defaultMark uint8
	defaultTag  string
	providers   map[string]data_provider.RuleExporter
	subscribed  map[string]bool
}

var _ sequence.Executable = (*DomainMapper)(nil)
var _ coremain.RuntimeConfigReloader = (*DomainMapper)(nil)

func validateDomainMapperMark(scope string, mark uint8) error {
	if mark > 63 {
		return fmt.Errorf("%s must be between 0 and 63, got %d", scope, mark)
	}
	if mark == 0 {
		return nil
	}
	if reason, ok := reservedFastMarks[mark]; ok {
		return fmt.Errorf(
			"%s uses reserved fast_mark %d (%s); migrate to a non-reserved business mark, recommended range: 50-63 / 该配置使用了保留位 fast_mark %d（%s），请迁移到非保留业务位，推荐范围：50-63",
			scope, mark, reason, mark, reason,
		)
	}
	return nil
}

func NewMapper(bp *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)

	if err := validateArgs(cfg); err != nil {
		return nil, err
	}

	baseArgs := cloneArgs(cfg)
	if rawArgs, ok := bp.RawArgs().(*Args); ok && rawArgs != nil {
		baseArgs = cloneArgs(rawArgs)
	}

	dm := &DomainMapper{
		bp:          bp,
		pluginTag:   bp.Tag(),
		baseArgs:    baseArgs,
		logger:      bp.L(),
		ruleConfigs: cfg.Rules,
		defaultMark: cfg.DefaultMark,
		defaultTag:  cfg.DefaultTag,
		providers:   make(map[string]data_provider.RuleExporter),
		subscribed:  make(map[string]bool),
	}
	dm.matcher.Store(domain.NewMixMatcher[*MatchResult]())

	if err := dm.reloadFromConfig(cfg); err != nil {
		return nil, err
	}
	return dm, nil
}

func cloneArgs(src *Args) *Args {
	if src == nil {
		return new(Args)
	}
	dst := &Args{
		Rules:       append([]RuleConfig(nil), src.Rules...),
		DefaultMark: src.DefaultMark,
		DefaultTag:  src.DefaultTag,
	}
	return dst
}

func validateArgs(cfg *Args) error {
	if err := validateDomainMapperMark("default_mark", cfg.DefaultMark); err != nil {
		return err
	}
	for _, r := range cfg.Rules {
		if err := validateDomainMapperMark(fmt.Sprintf("rule mark for tag '%s'", r.Tag), r.Mark); err != nil {
			return err
		}
	}
	return nil
}

func (dm *DomainMapper) resolveProviders(ruleConfigs []RuleConfig) (map[string]data_provider.RuleExporter, error) {
	providers := make(map[string]data_provider.RuleExporter)
	for _, r := range ruleConfigs {
		if _, loaded := providers[r.Tag]; loaded {
			continue
		}
		pluginInterface := dm.bp.M().GetPlugin(r.Tag)
		if pluginInterface == nil {
			return nil, fmt.Errorf("plugin %s not found", r.Tag)
		}
		exporter, ok := pluginInterface.(data_provider.RuleExporter)
		if !ok {
			return nil, fmt.Errorf("plugin %s does not support rule export", r.Tag)
		}
		providers[r.Tag] = exporter
	}
	return providers, nil
}

func (dm *DomainMapper) subscribeProvider(tag string, exporter data_provider.RuleExporter) {
	if dm.subscribed[tag] {
		return
	}
	dm.subscribed[tag] = true
	exporter.Subscribe(func() {
		dm.logger.Info("upstream rule provider updated", zap.String("plugin", tag))
		dm.triggerUpdate()
	})
}

func (dm *DomainMapper) rebuild() {
	dm.logger.Info("rebuilding domain_mapper with logic inheritance...")
	start := time.Now()

	markMap := make(map[string]uint64)
	tagMap := make(map[string]string)
	totalRules := 0

	for _, ruleCfg := range dm.ruleConfigs {
		provider, ok := dm.providers[ruleCfg.Tag]
		if !ok {
			continue
		}
		rules, err := provider.GetRules()
		if err != nil {
			continue
		}

		targetTag := ruleCfg.OutputTag
		if targetTag == "" {
			targetTag = ruleCfg.Tag
		}

		for _, ruleStr := range rules {
			ruleKey := normalizeRuleKey(ruleStr)
			if ruleKey == "" {
				continue
			}
			if ruleCfg.Mark > 0 && ruleCfg.Mark <= 63 {
				markMap[ruleKey] |= (1 << (ruleCfg.Mark - 1))
			}
			oldTags := tagMap[ruleKey]
			if oldTags == "" {
				tagMap[ruleKey] = targetTag
			} else if !strings.Contains(oldTags, targetTag) {
				tagMap[ruleKey] = oldTags + "|" + targetTag
			}
		}
		totalRules += len(rules)
	}

	for ruleStr := range markMap {
		dotPos := strings.Index(ruleStr, ":")
		if dotPos == -1 {
			continue
		}
		dName := ruleStr[dotPos+1:]
		if strings.HasPrefix(ruleStr, "full:") {
			directDomainKey := "domain:" + dName
			if aMask, ok := markMap[directDomainKey]; ok {
				markMap[ruleStr] |= aMask
				tagMap[ruleStr] = mergeTagStrings(tagMap[ruleStr], tagMap[directDomainKey])
			}
		}

		for {
			nextDot := strings.Index(dName, ".")
			if nextDot == -1 {
				break
			}
			dName = dName[nextDot+1:]
			ancestorKey := "domain:" + dName

			if aMask, ok := markMap[ancestorKey]; ok {
				markMap[ruleStr] |= aMask
				tagMap[ruleStr] = mergeTagStrings(tagMap[ruleStr], tagMap[ancestorKey])
			}
		}
	}

	pool := make(map[string]*MatchResult)
	newMatcher := domain.NewMixMatcher[*MatchResult]()

	for ruleStr, mask := range markMap {
		tagsStr := tagMap[ruleStr]
		sig := fmt.Sprintf("%d-%s", mask, tagsStr)

		res, exists := pool[sig]
		if !exists {
			res = &MatchResult{
				JoinedTags: tagsStr,
			}
			for i := uint8(0); i < 64; i++ {
				if mask&(1<<i) != 0 {
					res.Marks = append(res.Marks, i+1)
				}
			}
			pool[sig] = res
		}
		newMatcher.Add(ruleStr, res)
	}

	dm.matcher.Store(newMatcher)

	dm.logger.Info("rebuild finished",
		zap.Int("rules", totalRules),
		zap.Int("pooled_results", len(pool)),
		zap.Duration("duration", time.Since(start)))

	go func() {
		time.Sleep(3 * time.Second)
		coremain.ManualGC()
	}()
}

func normalizeRuleKey(rule string) string {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return ""
	}
	if strings.Contains(rule, ":") {
		return rule
	}
	return "domain:" + rule
}

func mergeTagStrings(current, next string) string {
	if next == "" {
		return current
	}
	if current == "" {
		return next
	}
	if strings.Contains(current, next) {
		return current
	}
	return current + "|" + next
}

func (dm *DomainMapper) triggerUpdate() {
	dm.updateMu.Lock()
	defer dm.updateMu.Unlock()
	if dm.updateTimer != nil {
		dm.updateTimer.Stop()
	}
	dm.updateTimer = time.AfterFunc(1*time.Second, dm.rebuild)
}

func (dm *DomainMapper) reloadFromConfig(cfg *Args) error {
	if err := validateArgs(cfg); err != nil {
		return err
	}

	providers, err := dm.resolveProviders(cfg.Rules)
	if err != nil {
		return err
	}

	dm.ruleConfigs = append([]RuleConfig(nil), cfg.Rules...)
	dm.defaultMark = cfg.DefaultMark
	dm.defaultTag = cfg.DefaultTag
	dm.providers = providers

	for tag, exporter := range providers {
		dm.subscribeProvider(tag, exporter)
	}

	dm.rebuild()
	return nil
}

func (dm *DomainMapper) ReloadRuntimeConfig(global *coremain.GlobalOverrides, _ []coremain.UpstreamOverrideConfig) error {
	effective := new(Args)
	if err := coremain.DecodeRawArgsWithGlobalOverrides(dm.pluginTag, dm.baseArgs, effective, global); err != nil {
		return err
	}
	return dm.reloadFromConfig(effective)
}

func (dm *DomainMapper) FastMatch(qname string) ([]uint8, string, bool) {
	matcher := dm.matcher.Load().(*domain.MixMatcher[*MatchResult])
	result, ok := matcher.Match(qname)
	if ok && result != nil {
		return result.Marks, result.JoinedTags, true
	}
	return nil, "", false
}

func skipMapperForPreMatchedFastBypass(qCtx *query_context.Context) bool {
	return qCtx.ServerMeta.PreFastDomainMatched || len(qCtx.ServerMeta.PreFastDomainSet) > 0
}

func (dm *DomainMapper) Exec(ctx context.Context, qCtx *query_context.Context) error {
	if skipMapperForPreMatchedFastBypass(qCtx) {
		return nil
	}

	qname := qCtx.FastQName
	if qname == "" {
		q := qCtx.Q()
		if q == nil || len(q.Question) == 0 {
			return nil
		}
		qname = q.Question[0].Name
	}

	matcher := dm.matcher.Load().(*domain.MixMatcher[*MatchResult])

	result, ok := matcher.Match(qname)
	if ok && result != nil {
		for _, mark := range result.Marks {
			qCtx.SetFastFlag(mark)
		}
		if result.JoinedTags != "" && !sameDomainSetValue(qCtx, result.JoinedTags) {
			qCtx.StoreValue(query_context.KeyDomainSet, result.JoinedTags)
		}
	} else {
		if dm.defaultMark != 0 {
			qCtx.SetFastFlag(dm.defaultMark)
		}
		if dm.defaultTag != "" && !sameDomainSetValue(qCtx, dm.defaultTag) {
			qCtx.StoreValue(query_context.KeyDomainSet, dm.defaultTag)
		}
	}
	return nil
}

func (dm *DomainMapper) GetFastExec() func(ctx context.Context, qCtx *query_context.Context) error {
	defMark := dm.defaultMark
	defTag := dm.defaultTag
	return func(ctx context.Context, qCtx *query_context.Context) error {
		if skipMapperForPreMatchedFastBypass(qCtx) {
			return nil
		}

		qname := qCtx.FastQName
		if qname == "" {
			q := qCtx.Q()
			if q == nil || len(q.Question) == 0 {
				return nil
			}
			qname = q.Question[0].Name
		}

		matcher := dm.matcher.Load().(*domain.MixMatcher[*MatchResult])
		result, ok := matcher.Match(qname)
		if ok && result != nil {
			for _, mark := range result.Marks {
				qCtx.SetFastFlag(mark)
			}
			if result.JoinedTags != "" && !sameDomainSetValue(qCtx, result.JoinedTags) {
				qCtx.StoreValue(query_context.KeyDomainSet, result.JoinedTags)
			}
		} else {
			if defMark != 0 {
				qCtx.SetFastFlag(defMark)
			}
			if defTag != "" && !sameDomainSetValue(qCtx, defTag) {
				qCtx.StoreValue(query_context.KeyDomainSet, defTag)
			}
		}
		return nil
	}
}

func sameDomainSetValue(qCtx *query_context.Context, want string) bool {
	if v, ok := qCtx.GetValue(query_context.KeyDomainSet); ok {
		if s, ok := v.(string); ok {
			return s == want
		}
	}
	return false
}
