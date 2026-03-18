package domain_stats_pool

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
	PluginType        = "domain_stats_pool"
	RecordBufferLimit = 10240

	qtypeMaskA    uint8 = 1 << 0
	qtypeMaskAAAA uint8 = 1 << 1

	flagMaskAD uint8 = 1 << 0
	flagMaskCD uint8 = 1 << 1
	flagMaskDO uint8 = 1 << 2
)

type WriteMode int

const (
	WriteModePeriodic WriteMode = iota
	WriteModeFlush
	WriteModeSave
	WriteModeShutdown
)

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

type aggregateEntry struct {
	Domain            string
	Count             int
	Date              string
	Score             int
	QMask             uint8
	FlagsMask         uint8
	VariantCount      int
	DirtyVariantCount int
	Promoted          bool
	LastSource        string
	LastSeenAt        string
	LastDirtyAt       string
	LastVerifiedAt    string
	CooldownUntil     string
	DirtyReason       string
	RefreshState      string
	ConflictCount     int
}

type writeSnapshot struct {
	items         []outputRankItem
	rules         []string
	state         coremain.DomainPoolState
	promotedCount int
	dirtyCount    int
}

type domainStatsPool struct {
	pluginTag   string
	logger      *zap.Logger
	dbPath      string
	policy      writePolicy
	memoryID    string
	enableFlags bool

	stats              map[string]*statEntry
	domainVariantCount map[string]int
	domainCount        int
	rules              []string
	subscribers        []func()

	mu      sync.Mutex
	writeMu sync.Mutex

	totalCount         int64
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

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
	sequence.MustRegExecQuickSetup(PluginType, QuickSetup)
}

func Init(bp *coremain.BP, _ any) (any, error) {
	pool, err := newDomainStatsPoolFromBP(bp)
	if err != nil {
		return nil, err
	}
	if err := pool.loadFromStore(); err != nil {
		return nil, err
	}
	go pool.startWorker()
	return pool, nil
}

func QuickSetup(_ sequence.BQ, _ string) (any, error) {
	return nil, errors.New("domain_stats_pool quick setup is not supported in v2")
}

func newDomainStatsPoolFromBP(bp *coremain.BP) (*domainStatsPool, error) {
	return newDomainStatsPoolWithDeps(bp.Tag(), bp.L(), bp.ControlDBPath())
}

func newDomainStatsPool(pluginTag string, _ *coremain.Mosdns, logger *zap.Logger) (*domainStatsPool, error) {
	return newDomainStatsPoolWithDeps(pluginTag, logger, "")
}

func newDomainStatsPoolWithDeps(pluginTag string, logger *zap.Logger, dbPath string) (*domainStatsPool, error) {
	policy, err := resolveWritePolicy(pluginTag)
	if err != nil {
		return nil, err
	}
	return &domainStatsPool{
		pluginTag:          strings.TrimSpace(pluginTag),
		logger:             logger,
		dbPath:             dbPath,
		policy:             policy,
		memoryID:           policy.raw.MemoryID,
		enableFlags:        policy.trackFlags,
		stats:              make(map[string]*statEntry),
		domainVariantCount: make(map[string]int),
		rules:              make([]string, 0),
		subscribers:        make([]func(), 0),
		recordChan:         make(chan *logItem, RecordBufferLimit),
		writeSignalChan:    make(chan struct{}, 1),
		stopChan:           make(chan struct{}),
		workerDoneChan:     make(chan struct{}),
	}, nil
}

func (d *domainStatsPool) Exec(_ context.Context, qCtx *query_context.Context) error {
	d.enqueueFromContext(qCtx, "live")
	return nil
}

func (d *domainStatsPool) GetFastExec() func(ctx context.Context, qCtx *query_context.Context) error {
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

func (d *domainStatsPool) enqueueFromContext(qCtx *query_context.Context, source string) {
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

func (d *domainStatsPool) startWorker() {
	flushTicker := time.NewTicker(d.policy.flushEvery)
	pruneTicker := time.NewTicker(d.policy.pruneEvery)
	defer flushTicker.Stop()
	defer pruneTicker.Stop()
	defer close(d.workerDoneChan)

	for {
		select {
		case item := <-d.recordChan:
			d.processRecord(item)
		case <-flushTicker.C:
			d.runWrite(WriteModePeriodic)
		case <-pruneTicker.C:
			d.runWrite(WriteModePeriodic)
		case <-d.writeSignalChan:
			d.runWrite(WriteModePeriodic)
		case <-d.stopChan:
			d.drainPendingRecords()
			return
		}
	}
}

func (d *domainStatsPool) drainPendingRecords() {
	for {
		select {
		case item := <-d.recordChan:
			d.processRecord(item)
		default:
			return
		}
	}
}

func (d *domainStatsPool) processRecord(item *logItem) {
	bareDomain := strings.TrimSpace(strings.TrimSuffix(item.name, "."))
	if bareDomain == "" {
		return
	}
	storageKey := buildStorageKey(bareDomain, item, d.enableFlags)
	qmask := qtypeToMask(item.qtype)
	now := time.Now().UTC()
	nowDate := now.Format("2006-01-02")
	nowStamp := now.Format(time.RFC3339)
	d.mu.Lock()
	entry, exists := d.stats[storageKey]
	if !exists {
		if !d.canCreateEntryLocked(bareDomain) {
			d.mu.Unlock()
			atomic.AddInt64(&d.droppedByCapCount, 1)
			atomic.AddInt64(&d.droppedCount, 1)
			return
		}
		entry = &statEntry{}
		d.stats[storageKey] = entry
		d.trackEntryCreatedLocked(bareDomain)
	}
	entry.Count++
	entry.Score++
	entry.LastDate = nowDate
	entry.LastSeenAt = nowStamp
	entry.LastSource = item.source
	if qmask != 0 {
		entry.QTypeMask |= qmask
	}
	d.mu.Unlock()

	d.dirtyPending.Store(true)
	atomic.AddInt64(&d.totalCount, 1)
}

func (d *domainStatsPool) canCreateEntryLocked(domain string) bool {
	variants := d.domainVariantCount[domain]
	if d.policy.maxVariantsPerDomain > 0 && variants >= d.policy.maxVariantsPerDomain {
		return false
	}
	if variants > 0 {
		return true
	}
	if d.policy.maxDomains > 0 && d.domainCount >= d.policy.maxDomains {
		return false
	}
	return true
}

func (d *domainStatsPool) trackEntryCreatedLocked(domain string) {
	if d.domainVariantCount[domain] == 0 {
		d.domainCount++
	}
	d.domainVariantCount[domain]++
}

func (d *domainStatsPool) deleteEntryLocked(storageKey string) {
	domain, _ := splitStorageKey(storageKey)
	delete(d.stats, storageKey)
	remaining := d.domainVariantCount[domain] - 1
	if remaining <= 0 {
		delete(d.domainVariantCount, domain)
		if d.domainCount > 0 {
			d.domainCount--
		}
		return
	}
	d.domainVariantCount[domain] = remaining
}

func buildStorageKey(domain string, item *logItem, enableFlags bool) string {
	if !enableFlags {
		return domain
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
		return domain
	}
	return domain + "|" + strings.Join(flags, "|")
}

func splitStorageKey(storageKey string) (string, uint8) {
	parts := strings.Split(storageKey, "|")
	if len(parts) == 1 {
		return storageKey, 0
	}
	var flags uint8
	for _, part := range parts[1:] {
		switch strings.TrimSpace(part) {
		case "AD":
			flags |= flagMaskAD
		case "CD":
			flags |= flagMaskCD
		case "DO":
			flags |= flagMaskDO
		}
	}
	return parts[0], flags
}

func buildStorageKeyFromFlags(domain string, flagsMask uint8) string {
	item := &logItem{name: domain}
	item.ad = flagsMask&flagMaskAD != 0
	item.cd = flagsMask&flagMaskCD != 0
	item.do = flagsMask&flagMaskDO != 0
	return buildStorageKey(domain, item, flagsMask != 0)
}

func buildVariantKey(flagsMask uint8) string {
	return "f:" + string('0'+flagsMask)
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

func (d *domainStatsPool) runWrite(mode WriteMode) {
	if err := d.performWrite(mode); err != nil {
		d.logWriteFailure(mode, err)
	}
}

func (d *domainStatsPool) performWrite(mode WriteMode) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if mode == WriteModePeriodic && !d.dirtyPending.Load() {
		return nil
	}

	snapshot := d.buildSnapshot(mode)
	rulesHash := hashRules(snapshot.rules)
	rulesChanged := mode == WriteModeFlush || !d.hasRulesHash || d.lastRulesHash != rulesHash
	if rulesChanged {
		snapshot.state.Meta.LastPublishAtUnixMS = time.Now().UTC().UnixMilli()
	}
	if err := d.saveState(snapshot.state); err != nil {
		return err
	}

	d.lastRulesHash = rulesHash
	d.hasRulesHash = true
	d.dirtyPending.Store(false)
	atomic.StoreInt64(&d.promotedCount, int64(snapshot.promotedCount))
	atomic.StoreInt64(&d.publishedCount, int64(len(snapshot.rules)))

	d.mu.Lock()
	d.rules = append([]string(nil), snapshot.rules...)
	d.mu.Unlock()

	if mode != WriteModeShutdown && rulesChanged {
		d.notifySubscribers()
	}
	return nil
}

func (d *domainStatsPool) buildSnapshot(mode WriteMode) writeSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.pruneExpiredLocked()
	if mode == WriteModeFlush {
		d.resetStateLocked()
		return d.emptySnapshot()
	}

	aggregated := make(map[string]*aggregateEntry, d.domainCount)
	variants := make([]coremain.DomainPoolVariant, 0, len(d.stats))
	for key, entry := range d.stats {
		domain, flagsMask := splitStorageKey(key)
		aggregate := aggregated[domain]
		if aggregate == nil {
			aggregate = &aggregateEntry{Domain: domain, Date: entry.LastDate}
			aggregated[domain] = aggregate
		}
		mergeAggregateEntry(aggregate, entry, flagsMask)
		variants = append(variants, buildVariantRecord(d.pluginTag, domain, flagsMask, entry))
	}

	items, rules, promotedCount, dirtyCount, domains := buildAggregatedOutputs(aggregated)
	state := coremain.DomainPoolState{
		Meta:     buildPoolMeta(d, len(domains), len(variants), promotedCount, dirtyCount, len(rules)),
		Domains:  domains,
		Variants: variants,
	}
	return writeSnapshot{
		items:         items,
		rules:         rules,
		state:         state,
		promotedCount: promotedCount,
		dirtyCount:    dirtyCount,
	}
}

func (d *domainStatsPool) pruneExpiredLocked() {
	evictBefore := time.Now().AddDate(0, 0, -d.policy.retentionDays)
	for key, entry := range d.stats {
		if entry.LastDate == "" {
			continue
		}
		ts, err := time.Parse("2006-01-02", entry.LastDate)
		if err == nil && ts.Before(evictBefore) {
			d.deleteEntryLocked(key)
		}
	}
}

func (d *domainStatsPool) resetStateLocked() {
	d.stats = make(map[string]*statEntry)
	d.domainVariantCount = make(map[string]int)
	d.domainCount = 0
	d.rules = nil
	atomic.StoreInt64(&d.totalCount, 0)
}

func (d *domainStatsPool) emptySnapshot() writeSnapshot {
	return writeSnapshot{
		items: []outputRankItem{},
		rules: []string{},
		state: coremain.DomainPoolState{
			Meta: buildPoolMeta(d, 0, 0, 0, 0, 0),
		},
	}
}

func mergeAggregateEntry(target *aggregateEntry, entry *statEntry, flagsMask uint8) {
	target.Count += entry.Count
	target.Score += entry.Score
	target.QMask |= entry.QTypeMask
	target.FlagsMask |= flagsMask
	target.VariantCount++
	target.ConflictCount += entry.ConflictCount
	target.Promoted = target.Promoted || entry.Promoted
	target.LastSource = maxStringByValue(target.LastSource, entry.LastSource)
	target.LastSeenAt = maxStringByValue(target.LastSeenAt, entry.LastSeenAt)
	target.LastDirtyAt = maxStringByValue(target.LastDirtyAt, entry.LastDirtyAt)
	target.LastVerifiedAt = maxStringByValue(target.LastVerifiedAt, entry.LastVerifiedAt)
	target.CooldownUntil = maxStringByValue(target.CooldownUntil, entry.CooldownUntil)
	target.Date = maxStringByValue(target.Date, entry.LastDate)
	if entry.RefreshState == "dirty" {
		target.DirtyVariantCount++
	}
	if entry.LastDirtyAt >= target.LastDirtyAt {
		target.DirtyReason = entry.DirtyReason
		target.RefreshState = entry.RefreshState
	}
}

func buildAggregatedOutputs(aggregated map[string]*aggregateEntry) ([]outputRankItem, []string, int, int, []coremain.DomainPoolDomain) {
	items := make([]outputRankItem, 0, len(aggregated))
	rules := make([]string, 0, len(aggregated))
	domains := make([]coremain.DomainPoolDomain, 0, len(aggregated))
	promotedCount := 0
	dirtyCount := 0

	keys := make([]string, 0, len(aggregated))
	for domain := range aggregated {
		keys = append(keys, domain)
	}
	sort.Strings(keys)

	for _, domain := range keys {
		entry := aggregated[domain]
		items = append(items, outputRankItem{
			Domain:         entry.Domain,
			Count:          entry.Count,
			Date:           entry.Date,
			Score:          entry.Score,
			QMask:          entry.QMask,
			Prom:           entry.Promoted,
			LastSeenAt:     entry.LastSeenAt,
			LastDirtyAt:    entry.LastDirtyAt,
			LastVerifiedAt: entry.LastVerifiedAt,
			DirtyReason:    entry.DirtyReason,
			RefreshState:   entry.RefreshState,
			CooldownUntil:  entry.CooldownUntil,
			ConflictCount:  entry.ConflictCount,
		})
		if entry.Promoted {
			rules = append(rules, "full:"+entry.Domain)
			promotedCount++
		}
		if entry.DirtyVariantCount > 0 {
			dirtyCount++
		}
		domains = append(domains, coremain.DomainPoolDomain{
			PoolTag:              "",
			Domain:               entry.Domain,
			TotalCount:           entry.Count,
			Score:                entry.Score,
			QTypeMask:            entry.QMask,
			FlagsMask:            entry.FlagsMask,
			VariantCount:         entry.VariantCount,
			DirtyVariantCount:    entry.DirtyVariantCount,
			Promoted:             entry.Promoted,
			LastSource:           entry.LastSource,
			LastSeenAtUnixMS:     parseUnixMS(entry.LastSeenAt),
			LastDirtyAtUnixMS:    parseUnixMS(entry.LastDirtyAt),
			LastVerifiedAtUnixMS: parseUnixMS(entry.LastVerifiedAt),
			CooldownUntilUnixMS:  parseUnixMS(entry.CooldownUntil),
			DirtyReason:          entry.DirtyReason,
			RefreshState:         entry.RefreshState,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Domain < items[j].Domain
		}
		return items[i].Count > items[j].Count
	})
	for i := range domains {
		domains[i].PoolTag = ""
	}
	return items, rules, promotedCount, dirtyCount, domains
}

func buildPoolMeta(d *domainStatsPool, domainCount, variantCount, promotedCount, dirtyCount, publishedCount int) coremain.DomainPoolMeta {
	return coremain.DomainPoolMeta{
		PoolTag:              d.pluginTag,
		PoolKind:             coremain.DomainPoolKindStats,
		MemoryID:             d.memoryID,
		Policy:               d.policy.raw,
		DomainCount:          domainCount,
		VariantCount:         variantCount,
		DirtyDomainCount:     dirtyCount,
		PromotedDomainCount:  promotedCount,
		PublishedDomainCount: publishedCount,
		TotalObservations:    atomic.LoadInt64(&d.totalCount),
		DroppedObservations:  atomic.LoadInt64(&d.droppedCount),
		DroppedByBuffer:      atomic.LoadInt64(&d.droppedBufferCount),
		DroppedByCap:         atomic.LoadInt64(&d.droppedByCapCount),
		LastFlushAtUnixMS:    time.Now().UTC().UnixMilli(),
	}
}

func buildVariantRecord(poolTag, domain string, flagsMask uint8, entry *statEntry) coremain.DomainPoolVariant {
	return coremain.DomainPoolVariant{
		PoolTag:              poolTag,
		Domain:               domain,
		VariantKey:           buildVariantKey(flagsMask),
		TotalCount:           entry.Count,
		Score:                entry.Score,
		QTypeMask:            entry.QTypeMask,
		FlagsMask:            flagsMask,
		Promoted:             entry.Promoted,
		LastSource:           entry.LastSource,
		LastSeenAtUnixMS:     parseUnixMS(entry.LastSeenAt),
		LastDirtyAtUnixMS:    parseUnixMS(entry.LastDirtyAt),
		LastVerifiedAtUnixMS: parseUnixMS(entry.LastVerifiedAt),
		CooldownUntilUnixMS:  parseUnixMS(entry.CooldownUntil),
		DirtyReason:          entry.DirtyReason,
		RefreshState:         entry.RefreshState,
		ConflictCount:        entry.ConflictCount,
	}
}

func (d *domainStatsPool) shouldPromote(entry *statEntry) bool {
	_ = entry
	return false
}

func (d *domainStatsPool) isStale(lastDate string) bool {
	_ = lastDate
	return false
}

func hashRules(rules []string) uint64 {
	h := fnv.New64a()
	for _, rule := range rules {
		_, _ = h.Write([]byte(rule))
		_, _ = h.Write([]byte{'\n'})
	}
	return h.Sum64()
}

func (d *domainStatsPool) nextDirtyReason(entry *statEntry, now time.Time) string {
	_ = entry
	_ = now
	return ""
}

func (d *domainStatsPool) Close() error {
	var closeErr error
	d.closeOnce.Do(func() {
		close(d.stopChan)
		<-d.workerDoneChan
		closeErr = d.performWrite(WriteModeShutdown)
	})
	return closeErr
}

func (d *domainStatsPool) PrepareForRestart() error {
	return d.Close()
}

func (d *domainStatsPool) logWriteFailure(mode WriteMode, err error) {
	if d.logger == nil || err == nil {
		return
	}
	d.logger.Warn(
		"domain_stats_pool write failed",
		zap.String("plugin", d.pluginTag),
		zap.Int("mode", int(mode)),
		zap.Error(err),
	)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxStringByValue(current, next string) string {
	if next > current {
		return next
	}
	return current
}

func parseUnixMS(value string) int64 {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0
	}
	return ts.UnixMilli()
}
