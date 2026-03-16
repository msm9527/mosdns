package domain_output

import (
	"context"
	"errors"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"go.uber.org/zap"
)

const (
	PluginType        = "domain_output"
	RecordBufferLimit = 10240

	qtypeMaskA    uint8 = 1 << 0
	qtypeMaskAAAA uint8 = 1 << 1
)

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
	sequence.MustRegExecQuickSetup(PluginType, QuickSetup)
}

type statEntry struct {
	Count          int
	LastDate       string
	LastSeenAt     string
	LastDirtyAt    string
	LastVerifiedAt string
	CooldownUntil  string
	DirtyReason    string
	RefreshState   string
	QTypeMask      uint8
	Score          int
	Promoted       bool
	ConflictCount  int
	LastSource     string
}

type logItem struct {
	name   string
	qtype  uint16
	source string
	ad     bool
	cd     bool
	do     bool
}

type domainOutput struct {
	pluginTag      string
	publishTo      string
	manager        *coremain.Mosdns
	logger         *zap.Logger
	dbPath         string
	statDatasetKey string
	ruleDatasetKey string
	maxEntries     int
	persistEvery   time.Duration
	enableFlags    bool
	policy         writePolicy
	memoryID       string

	stats   map[string]*statEntry
	mu      sync.Mutex
	writeMu sync.Mutex

	totalCount         int64
	entryCounter       int64
	droppedCount       int64
	droppedBufferCount int64
	droppedByCapCount  int64
	promotedCount      int64
	publishedCount     int64
	recordChan         chan *logItem
	writeSignalChan    chan struct{}
	stopChan           chan struct{}
	workerDoneChan     chan struct{}

	dirtyPending  atomic.Bool
	lastRulesHash uint64
	hasRulesHash  bool
	closeOnce     sync.Once
}

type WriteMode int

const (
	WriteModePeriodic WriteMode = iota
	WriteModeFlush
	WriteModeSave
	WriteModeShutdown
)

type writeSnapshot struct {
	items         []outputRankItem
	rules         []string
	promotedCount int
	dirtyCount    int
}

type outputRankItem struct {
	Domain         string
	Count          int
	Date           string
	Score          int
	QMask          uint8
	Prom           bool
	LastSeenAt     string
	LastDirtyAt    string
	LastVerifiedAt string
	DirtyReason    string
	RefreshState   string
	CooldownUntil  string
	ConflictCount  int
}

func Init(bp *coremain.BP, args any) (any, error) {
	d := newDomainOutput(bp.Tag(), bp.M(), bp.L(), args.(*Args))
	if err := d.loadFromDataset(); err != nil {
		return nil, err
	}
	go d.startWorker()
	return d, nil
}

func QuickSetup(_ sequence.BQ, _ string) (any, error) {
	return nil, errors.New("domain_output quick setup is not supported in v2")
}

func newDomainOutput(pluginTag string, manager *coremain.Mosdns, logger *zap.Logger, cfg *Args) *domainOutput {
	persistInterval := cfg.PersistInterval
	if persistInterval <= 0 {
		persistInterval = defaultPersistIntervalSeconds
	}
	policy := normalizePolicy(pluginTag, cfg)

	d := &domainOutput{
		pluginTag:       strings.TrimSpace(pluginTag),
		publishTo:       strings.TrimSpace(cfg.PublishTo),
		manager:         manager,
		logger:          logger,
		dbPath:          coremain.RuntimeStateDBPath(),
		statDatasetKey:  coremain.DomainOutputStatDatasetKey(pluginTag),
		maxEntries:      cfg.MaxEntries,
		persistEvery:    time.Duration(persistInterval) * time.Second,
		enableFlags:     cfg.EnableFlags,
		policy:          policy,
		memoryID:        inferMemoryID(pluginTag, cfg),
		stats:           make(map[string]*statEntry),
		recordChan:      make(chan *logItem, RecordBufferLimit),
		writeSignalChan: make(chan struct{}, 1),
		stopChan:        make(chan struct{}),
		workerDoneChan:  make(chan struct{}),
	}
	if d.publishTo != "" {
		d.ruleDatasetKey = coremain.DomainOutputRuleDatasetKey(pluginTag)
	}
	return d
}

func (d *domainOutput) Exec(_ context.Context, qCtx *query_context.Context) error {
	d.enqueueFromContext(qCtx, "live")
	return nil
}

func (d *domainOutput) GetFastExec() func(ctx context.Context, qCtx *query_context.Context) error {
	rChan := d.recordChan
	enableFlags := d.enableFlags
	trackQType := d.policy.trackQType
	return func(_ context.Context, qCtx *query_context.Context) error {
		q := qCtx.Q()
		if q == nil || len(q.Question) == 0 {
			return nil
		}
		for _, question := range q.Question {
			item := &logItem{name: question.Name, source: "live"}
			if trackQType {
				item.qtype = question.Qtype
			}
			if enableFlags {
				item.ad = q.AuthenticatedData
				item.cd = q.CheckingDisabled
				if opt := q.IsEdns0(); opt != nil {
					item.do = opt.Do()
				}
			}
			select {
			case rChan <- item:
			default:
				atomic.AddInt64(&d.droppedBufferCount, 1)
				atomic.AddInt64(&d.droppedCount, 1)
			}
		}
		return nil
	}
}

func (d *domainOutput) enqueueFromContext(qCtx *query_context.Context, source string) {
	q := qCtx.Q()
	if q == nil || len(q.Question) == 0 {
		return
	}
	for _, question := range q.Question {
		item := &logItem{name: question.Name, source: source}
		if d.policy.trackQType {
			item.qtype = question.Qtype
		}
		if d.enableFlags {
			item.ad = q.AuthenticatedData
			item.cd = q.CheckingDisabled
			if opt := q.IsEdns0(); opt != nil {
				item.do = opt.Do()
			}
		}
		select {
		case d.recordChan <- item:
		default:
			atomic.AddInt64(&d.droppedBufferCount, 1)
			atomic.AddInt64(&d.droppedCount, 1)
		}
	}
}

func (d *domainOutput) startWorker() {
	ticker := time.NewTicker(d.persistEvery)
	defer ticker.Stop()
	defer close(d.workerDoneChan)

	for {
		select {
		case item := <-d.recordChan:
			d.processRecord(item)
		case <-ticker.C:
			d.runWrite(WriteModePeriodic)
		case <-d.writeSignalChan:
			d.runWrite(WriteModePeriodic)
		case <-d.stopChan:
			for {
				select {
				case item := <-d.recordChan:
					d.processRecord(item)
				default:
					return
				}
			}
		}
	}
}

func (d *domainOutput) processRecord(item *logItem) {
	storageKey := buildStorageKey(strings.TrimSuffix(item.name, "."), item, d.enableFlags)
	qmask := qtypeToMask(item.qtype)
	now := time.Now().UTC()
	nowDate := now.Format("2006-01-02")
	nowStamp := now.Format(time.RFC3339)
	var notify *coremain.DomainRefreshJob

	d.mu.Lock()
	entry, exists := d.stats[storageKey]
	if !exists {
		if d.maxEntries > 0 && len(d.stats) >= d.maxEntries {
			d.mu.Unlock()
			atomic.AddInt64(&d.droppedByCapCount, 1)
			atomic.AddInt64(&d.droppedCount, 1)
			return
		}
		entry = &statEntry{}
		d.stats[storageKey] = entry
	}
	entry.Count++
	entry.Score++
	entry.LastDate = nowDate
	entry.LastSeenAt = nowStamp
	entry.LastSource = item.source
	if qmask != 0 {
		entry.QTypeMask |= qmask
	}
	entry.Promoted = d.shouldPromote(entry)
	if item.source == "live" {
		reason := d.nextDirtyReason(entry, now)
		if reason != "" {
			entry.RefreshState = "dirty"
			entry.DirtyReason = reason
			entry.LastDirtyAt = nowStamp
			if d.policy.refreshCooldownMinutes > 0 {
				entry.CooldownUntil = now.Add(time.Duration(d.policy.refreshCooldownMinutes) * time.Minute).Format(time.RFC3339)
			}
			if d.policy.requeryTag != "" && entry.Promoted {
				notify = &coremain.DomainRefreshJob{
					Domain:     strings.TrimSuffix(item.name, "."),
					MemoryID:   d.memoryID,
					QTypeMask:  entry.QTypeMask,
					Reason:     reason,
					VerifyTag:  d.pluginTag,
					ObservedAt: now,
				}
			}
		}
	}
	d.mu.Unlock()
	d.dirtyPending.Store(true)

	atomic.AddInt64(&d.totalCount, 1)
	newCount := atomic.AddInt64(&d.entryCounter, 1)
	if d.maxEntries > 0 && newCount >= int64(d.maxEntries) {
		select {
		case d.writeSignalChan <- struct{}{}:
		default:
		}
	}
	if notify != nil {
		go d.notifyDirty(*notify)
	}
}

func buildStorageKey(rawDomain string, item *logItem, enableFlags bool) string {
	if !enableFlags {
		return rawDomain
	}
	flags := make([]string, 0, 3)
	if item.ad {
		flags = append(flags, "AD")
	}
	if item.cd {
		flags = append(flags, "CD")
	}
	if item.do {
		flags = append(flags, "DO")
	}
	if len(flags) == 0 {
		return rawDomain
	}
	return rawDomain + "|" + strings.Join(flags, "|")
}

func qtypeToMask(qtype uint16) uint8 {
	switch qtype {
	case 1:
		return qtypeMaskA
	case 28:
		return qtypeMaskAAAA
	default:
		return 0
	}
}

func (d *domainOutput) runWrite(mode WriteMode) {
	if err := d.performWrite(mode); err != nil {
		d.logWriteFailure(mode, err)
	}
}

func (d *domainOutput) performWrite(mode WriteMode) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	if mode == WriteModePeriodic && !d.dirtyPending.Load() {
		return nil
	}

	snapshot := d.buildSnapshot(mode)
	rulesHash := hashRules(snapshot.rules)
	rulesChanged := mode == WriteModeFlush || !d.hasRulesHash || d.lastRulesHash != rulesHash

	if err := d.saveGeneratedDatasets(snapshot); err != nil {
		return err
	}
	if mode != WriteModeShutdown && rulesChanged {
		if err := d.publishRules(snapshot.rules); err != nil {
			return err
		}
	}

	d.lastRulesHash = rulesHash
	d.hasRulesHash = true
	d.dirtyPending.Store(false)
	atomic.StoreInt64(&d.promotedCount, int64(snapshot.promotedCount))
	atomic.StoreInt64(&d.publishedCount, int64(len(snapshot.rules)))
	return nil
}

func (d *domainOutput) buildSnapshot(mode WriteMode) writeSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	evictBefore := now.AddDate(0, 0, -maxInt(d.policy.decayDays*3, d.policy.decayDays+7))
	for k, v := range d.stats {
		if v.LastDate == "" {
			continue
		}
		ts, err := time.Parse("2006-01-02", v.LastDate)
		if err == nil && ts.Before(evictBefore) {
			delete(d.stats, k)
		}
	}

	switch mode {
	case WriteModeFlush:
		d.stats = make(map[string]*statEntry)
		atomic.StoreInt64(&d.totalCount, 0)
		atomic.StoreInt64(&d.entryCounter, 0)
		return writeSnapshot{}
	case WriteModePeriodic, WriteModeSave, WriteModeShutdown:
		atomic.StoreInt64(&d.entryCounter, 0)
	}

	items := make([]outputRankItem, 0, len(d.stats))
	dirtyCount := 0
	for key, entry := range d.stats {
		entry.Promoted = d.shouldPromote(entry)
		items = append(items, outputRankItem{
			Domain:         key,
			Count:          entry.Count,
			Date:           entry.LastDate,
			Score:          entry.Score,
			QMask:          entry.QTypeMask,
			Prom:           entry.Promoted,
			LastSeenAt:     entry.LastSeenAt,
			LastDirtyAt:    entry.LastDirtyAt,
			LastVerifiedAt: entry.LastVerifiedAt,
			DirtyReason:    entry.DirtyReason,
			RefreshState:   entry.RefreshState,
			CooldownUntil:  entry.CooldownUntil,
			ConflictCount:  entry.ConflictCount,
		})
		if entry.RefreshState == "dirty" {
			dirtyCount++
		}
	}
	rules, promoted := d.collectRules(items)
	return writeSnapshot{items: items, rules: rules, promotedCount: promoted, dirtyCount: dirtyCount}
}

func (d *domainOutput) collectRules(items []outputRankItem) ([]string, int) {
	if len(items) == 0 {
		return nil, 0
	}
	type aggregate struct {
		count    int
		lastDate string
		promoted bool
	}

	aggregated := make(map[string]aggregate, len(items))
	promotedCount := 0
	for _, entry := range items {
		domainOnly := strings.Split(entry.Domain, "|")[0]
		current := aggregated[domainOnly]
		current.count += entry.Count
		if entry.Date > current.lastDate {
			current.lastDate = entry.Date
		}
		if entry.Prom {
			current.promoted = true
		}
		aggregated[domainOnly] = current
	}

	domains := make([]string, 0, len(aggregated))
	for domain, item := range aggregated {
		if d.policy.publishMode == "promoted_only" && !item.promoted {
			continue
		}
		if d.isStale(item.lastDate) {
			continue
		}
		domains = append(domains, domain)
		if item.promoted {
			promotedCount++
		}
	}
	sort.Strings(domains)
	return domains, promotedCount
}

func (d *domainOutput) shouldPromote(entry *statEntry) bool {
	if d.policy.publishMode == "all" {
		return true
	}
	if d.isStale(entry.LastDate) || entry.Count < d.policy.promoteAfter {
		return false
	}
	switch d.policy.kind {
	case "nov4":
		return entry.QTypeMask&qtypeMaskA != 0
	case "nov6":
		return entry.QTypeMask&qtypeMaskAAAA != 0
	default:
		return true
	}
}

func (d *domainOutput) isStale(lastDate string) bool {
	if d.policy.decayDays <= 0 || lastDate == "" {
		return false
	}
	ts, err := time.Parse("2006-01-02", lastDate)
	if err != nil {
		return false
	}
	return time.Since(ts) > time.Duration(d.policy.decayDays)*24*time.Hour
}

func hashRules(rules []string) uint64 {
	h := fnv.New64a()
	for _, domain := range rules {
		_, _ = h.Write([]byte(domain))
		_, _ = h.Write([]byte{'\n'})
	}
	return h.Sum64()
}

func (d *domainOutput) nextDirtyReason(entry *statEntry, now time.Time) string {
	if !entry.Promoted || d.policy.requeryTag == "" {
		return ""
	}
	if entry.CooldownUntil != "" {
		if ts, err := time.Parse(time.RFC3339, entry.CooldownUntil); err == nil && now.Before(ts) {
			if entry.RefreshState == "" {
				entry.RefreshState = "cooldown"
			}
			return ""
		}
	}
	if entry.LastDirtyAt == "" {
		return "observed"
	}
	if d.policy.staleAfterMinutes > 0 {
		if ts, err := time.Parse(time.RFC3339, entry.LastDirtyAt); err == nil &&
			now.Sub(ts) >= time.Duration(d.policy.staleAfterMinutes)*time.Minute {
			return "stale"
		}
	}
	return ""
}

func (d *domainOutput) Close() error {
	var closeErr error
	d.closeOnce.Do(func() {
		close(d.stopChan)
		<-d.workerDoneChan
		closeErr = d.performWrite(WriteModeShutdown)
	})
	return closeErr
}

func (d *domainOutput) PrepareForRestart() error {
	return d.Close()
}

func (d *domainOutput) logWriteFailure(mode WriteMode, err error) {
	if d.logger == nil || err == nil {
		return
	}
	d.logger.Warn(
		"domain_output write failed",
		zap.String("plugin", d.pluginTag),
		zap.Int("mode", int(mode)),
		zap.Error(err),
	)
}
