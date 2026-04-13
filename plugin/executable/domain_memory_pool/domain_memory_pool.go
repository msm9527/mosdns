package domain_memory_pool

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
	"github.com/IrineSistiana/mosdns/v5/pkg/stringintern"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"go.uber.org/zap"
)

const (
	PluginType        = "domain_memory_pool"
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
	Count                int
	LastSeenAtUnixMS     int64
	LastDirtyAtUnixMS    int64
	LastVerifiedAtUnixMS int64
	CooldownUntilUnixMS  int64
	DirtyReason          string
	RefreshState         string
	QTypeMask            uint8
	Score                int
	Promoted             bool
	ConflictCount        int
	LastSource           string
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
	Domain               string
	Count                int
	DateUnixMS           int64
	Score                int
	QMask                uint8
	Prom                 bool
	LastSeenAtUnixMS     int64
	LastDirtyAtUnixMS    int64
	LastVerifiedAtUnixMS int64
	DirtyReason          string
	RefreshState         string
	CooldownUntilUnixMS  int64
	ConflictCount        int
}

type aggregateEntry struct {
	Domain               string
	Count                int
	DateUnixMS           int64
	Score                int
	QMask                uint8
	FlagsMask            uint8
	VariantCount         int
	DirtyVariantCount    int
	Promoted             bool
	LastSource           string
	LastSeenAtUnixMS     int64
	LastDirtyAtUnixMS    int64
	LastVerifiedAtUnixMS int64
	CooldownUntilUnixMS  int64
	DirtyReason          string
	RefreshState         string
	ConflictCount        int
}

type writeSnapshot struct {
	state         coremain.DomainPoolState
	promotedCount int
	dirtyCount    int
}

type entryKey struct {
	domain string
	flags  uint8
}

type domainMemoryPool struct {
	pluginTag   string
	plugin      func(string) any
	snapshotter coremain.PluginSnapshotter
	logger      *zap.Logger
	dbPath      string
	policy      writePolicy
	memoryID    string
	enableFlags bool

	stats              map[entryKey]*statEntry
	domainVariantCount map[string]uint8
	strings            *stringintern.Pool
	domainCount        int
	statsPeak          int
	domainVariantPeak  int
	subscribers        []func()
	hotActiveRules     []string

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
	hotCmdChan         chan hotPublishRequest
	hotDoneChan        chan struct{}
	workerStarted      atomic.Bool

	dirtyPending         atomic.Bool
	hotNeedsReplace      atomic.Bool
	lastRulesHash        uint64
	hasRulesHash         bool
	closeOnce            sync.Once
	hotPendingCount      int64
	hotAddTotal          int64
	hotReplaceTotal      int64
	hotDispatchFailTotal int64
	lastHotSyncAtUnixMS  int64
	lastEnqueueWarnAtMS  int64
}

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
	sequence.MustRegExecQuickSetup(PluginType, QuickSetup)
}

func Init(bp *coremain.BP, _ any) (any, error) {
	pool, err := newDomainMemoryPoolFromBP(bp)
	if err != nil {
		return nil, err
	}
	if err := pool.loadFromStore(); err != nil {
		return nil, err
	}
	pool.workerStarted.Store(true)
	go pool.startWorker()
	return pool, nil
}

func QuickSetup(_ sequence.BQ, _ string) (any, error) {
	return nil, errors.New("domain_memory_pool quick setup is not supported in v2")
}

func newDomainMemoryPoolFromBP(bp *coremain.BP) (*domainMemoryPool, error) {
	return newDomainMemoryPoolWithDeps(bp.Tag(), bp.Plugin, pluginSnapshotterFunc(bp.SnapshotPlugins), bp.L(), bp.ControlDBPath())
}

func newDomainMemoryPool(pluginTag string, manager *coremain.Mosdns, logger *zap.Logger) (*domainMemoryPool, error) {
	var plugin func(string) any
	var snapshotter coremain.PluginSnapshotter
	var dbPath string
	if manager != nil {
		plugin = manager.GetPlugin
		snapshotter = manager
		dbPath = manager.ControlDBPath()
	}
	return newDomainMemoryPoolWithDeps(pluginTag, plugin, snapshotter, logger, dbPath)
}

func newDomainMemoryPoolWithDeps(
	pluginTag string,
	plugin func(string) any,
	snapshotter coremain.PluginSnapshotter,
	logger *zap.Logger,
	dbPath string,
) (*domainMemoryPool, error) {
	policy, err := resolveWritePolicy(pluginTag)
	if err != nil {
		return nil, err
	}
	pool := &domainMemoryPool{
		pluginTag:          strings.TrimSpace(pluginTag),
		plugin:             plugin,
		snapshotter:        snapshotter,
		logger:             logger,
		dbPath:             dbPath,
		policy:             policy,
		memoryID:           policy.raw.MemoryID,
		enableFlags:        policy.trackFlags,
		stats:              make(map[entryKey]*statEntry),
		domainVariantCount: make(map[string]uint8),
		strings:            stringintern.New(),
		subscribers:        make([]func(), 0),
		hotActiveRules:     make([]string, 0),
		recordChan:         make(chan *logItem, RecordBufferLimit),
		writeSignalChan:    make(chan struct{}, 1),
		stopChan:           make(chan struct{}),
		workerDoneChan:     make(chan struct{}),
	}
	pool.initHotPublisher()
	return pool, nil
}

func (d *domainMemoryPool) Exec(_ context.Context, qCtx *query_context.Context) error {
	query_context.AppendDependencyTag(qCtx, d.pluginTag)
	d.enqueueFromContext(qCtx, "live")
	return nil
}

func (d *domainMemoryPool) GetFastExec() func(ctx context.Context, qCtx *query_context.Context) error {
	rChan := d.recordChan
	enableFlags := d.enableFlags
	trackQType := d.policy.trackQType
	return func(_ context.Context, qCtx *query_context.Context) error {
		query_context.AppendDependencyTag(qCtx, d.pluginTag)
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

func (d *domainMemoryPool) enqueueFromContext(qCtx *query_context.Context, source string) {
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

func (d *domainMemoryPool) startWorker() {
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

func (d *domainMemoryPool) drainPendingRecords() {
	for {
		select {
		case item := <-d.recordChan:
			d.processRecord(item)
		default:
			return
		}
	}
}

func (d *domainMemoryPool) processRecord(item *logItem) {
	bareDomain := strings.TrimSpace(strings.TrimSuffix(item.name, "."))
	if bareDomain == "" {
		return
	}
	flagsMask := buildFlagsMask(item, d.enableFlags)
	lookupKey := buildEntryKey(bareDomain, flagsMask)
	qmask := qtypeToMask(item.qtype)
	now := time.Now().UTC()
	nowUnixMS := now.UnixMilli()
	var notify *coremain.DomainRefreshJob
	var hotRules []string

	d.mu.Lock()
	entry, exists := d.stats[lookupKey]
	if !exists {
		if !d.canCreateEntryLocked(bareDomain) {
			d.mu.Unlock()
			atomic.AddInt64(&d.droppedByCapCount, 1)
			atomic.AddInt64(&d.droppedCount, 1)
			return
		}
		canonicalDomain, canonicalKey := d.acquireEntryKey(bareDomain, flagsMask)
		entry = &statEntry{}
		d.stats[canonicalKey] = entry
		d.trackEntryCreatedLocked(canonicalDomain)
	}
	entry.Count++
	entry.Score++
	entry.LastSeenAtUnixMS = nowUnixMS
	entry.LastSource = item.source
	if qmask != 0 {
		entry.QTypeMask |= qmask
	}
	wasPromoted := entry.Promoted
	entry.Promoted = d.shouldPromote(entry)
	if !wasPromoted && entry.Promoted {
		hotRules = []string{"full:" + bareDomain}
	}
	if item.source == "live" {
		reason := d.nextDirtyReason(entry, now)
		if reason != "" {
			entry.RefreshState = "dirty"
			entry.DirtyReason = reason
			entry.LastDirtyAtUnixMS = nowUnixMS
			if d.policy.refreshCooldownMinute > 0 {
				entry.CooldownUntilUnixMS = now.Add(time.Duration(d.policy.refreshCooldownMinute) * time.Minute).UnixMilli()
			}
			if d.policy.requeryTag != "" && entry.Promoted {
				notify = &coremain.DomainRefreshJob{
					Domain:     bareDomain,
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
	if notify != nil {
		go d.notifyDirty(*notify)
	}
	if len(hotRules) > 0 {
		d.pushHotRulesAdd(hotRules)
	}
}

func (d *domainMemoryPool) canCreateEntryLocked(domain string) bool {
	variants := int(d.domainVariantCount[domain])
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

func (d *domainMemoryPool) trackEntryCreatedLocked(domain string) {
	if d.domainVariantCount[domain] == 0 {
		d.domainCount++
	}
	d.domainVariantCount[domain]++
	d.noteStatePeaksLocked()
}

func (d *domainMemoryPool) deleteEntryLocked(key entryKey) {
	domain := key.domain
	delete(d.stats, key)
	remaining := d.domainVariantCount[domain] - 1
	if remaining <= 0 {
		delete(d.domainVariantCount, domain)
		d.releaseDomain(domain)
		if d.domainCount > 0 {
			d.domainCount--
		}
		return
	}
	d.domainVariantCount[domain] = remaining
}

func buildEntryKey(domain string, flagsMask uint8) entryKey {
	return entryKey{domain: domain, flags: flagsMask}
}

func buildFlagsMask(item *logItem, enableFlags bool) uint8 {
	if !enableFlags || item == nil {
		return 0
	}
	var flags uint8
	if item.ad {
		flags |= flagMaskAD
	}
	if item.cd {
		flags |= flagMaskCD
	}
	if item.do {
		flags |= flagMaskDO
	}
	return flags
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

func (d *domainMemoryPool) runWrite(mode WriteMode) {
	if err := d.performWrite(mode); err != nil {
		d.logWriteFailure(mode, err)
	}
}

func (d *domainMemoryPool) performWrite(mode WriteMode) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()
	if !d.shouldWrite(mode) {
		return nil
	}

	if d.shouldOnlySyncHotRules(mode) {
		return d.syncHotRulesOnly()
	}
	snapshot := d.buildSnapshot(mode)
	return d.persistSnapshot(mode, snapshot)
}

func (d *domainMemoryPool) shouldWrite(mode WriteMode) bool {
	switch mode {
	case WriteModePeriodic:
		return d.dirtyPending.Load() || d.hotNeedsReplace.Load()
	case WriteModeShutdown:
		return d.shouldWriteOnShutdown()
	default:
		return true
	}
}

func (d *domainMemoryPool) shouldOnlySyncHotRules(mode WriteMode) bool {
	return mode == WriteModePeriodic && !d.dirtyPending.Load() && d.hotNeedsReplace.Load()
}

func (d *domainMemoryPool) shouldWriteOnShutdown() bool {
	return !d.hasRulesHash || d.dirtyPending.Load() || d.hotNeedsReplace.Load()
}

func (d *domainMemoryPool) syncHotRulesOnly() error {
	rules := d.snapshotPromotedRules()
	if err := d.pushHotRulesReplace(rules); err != nil {
		return err
	}
	atomic.StoreInt64(&d.publishedCount, int64(len(rules)))
	return nil
}

func (d *domainMemoryPool) persistSnapshot(mode WriteMode, snapshot writeSnapshot) error {
	rulesHash := hashPromotedDomains(snapshot.state.Domains)
	rulesChanged := mode == WriteModeFlush || !d.hasRulesHash || d.lastRulesHash != rulesHash
	needsHotReplace := rulesChanged || d.hotNeedsReplace.Load()
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
	if needsHotReplace {
		rules := buildRulesFromStoredDomains(snapshot.state.Domains)
		if err := d.pushHotRulesReplace(rules); err != nil {
			return err
		}
		atomic.StoreInt64(&d.publishedCount, int64(len(rules)))
	}

	if mode != WriteModeShutdown && rulesChanged {
		d.notifySubscribers()
	}
	return nil
}

func (d *domainMemoryPool) buildSnapshot(mode WriteMode) writeSnapshot {
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
		domain := key.domain
		flagsMask := key.flags
		aggregate := aggregated[domain]
		if aggregate == nil {
			aggregate = &aggregateEntry{Domain: domain, DateUnixMS: entry.LastSeenAtUnixMS}
			aggregated[domain] = aggregate
		}
		mergeAggregateEntry(aggregate, entry, flagsMask)
		variants = append(variants, buildVariantRecord(d.pluginTag, domain, flagsMask, entry))
	}

	promotedCount, dirtyCount, domains := buildAggregatedOutputs(aggregated)
	state := coremain.DomainPoolState{
		Meta:     buildPoolMeta(d, len(domains), len(variants), promotedCount, dirtyCount, promotedCount),
		Domains:  domains,
		Variants: variants,
	}
	return writeSnapshot{
		state:         state,
		promotedCount: promotedCount,
		dirtyCount:    dirtyCount,
	}
}

func (d *domainMemoryPool) pruneExpiredLocked() {
	evictBefore := time.Now().AddDate(0, 0, -maxInt(d.policy.decayDays*3, d.policy.decayDays+7))
	deleted := false
	for key, entry := range d.stats {
		if entry.LastSeenAtUnixMS <= 0 {
			continue
		}
		if time.UnixMilli(entry.LastSeenAtUnixMS).Before(evictBefore) {
			d.deleteEntryLocked(key)
			deleted = true
		}
	}
	if deleted {
		d.maybeCompactStateLocked()
	}
}

func (d *domainMemoryPool) resetStateLocked() {
	d.resetStateStorageLocked()
	d.domainCount = 0
	atomic.StoreInt64(&d.totalCount, 0)
}

func (d *domainMemoryPool) emptySnapshot() writeSnapshot {
	return writeSnapshot{
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
	target.LastSeenAtUnixMS = maxInt64(target.LastSeenAtUnixMS, entry.LastSeenAtUnixMS)
	target.LastDirtyAtUnixMS = maxInt64(target.LastDirtyAtUnixMS, entry.LastDirtyAtUnixMS)
	target.LastVerifiedAtUnixMS = maxInt64(target.LastVerifiedAtUnixMS, entry.LastVerifiedAtUnixMS)
	target.CooldownUntilUnixMS = maxInt64(target.CooldownUntilUnixMS, entry.CooldownUntilUnixMS)
	target.DateUnixMS = maxInt64(target.DateUnixMS, entry.LastSeenAtUnixMS)
	if entry.RefreshState == "dirty" {
		target.DirtyVariantCount++
	}
	if entry.LastDirtyAtUnixMS >= target.LastDirtyAtUnixMS {
		target.DirtyReason = entry.DirtyReason
		target.RefreshState = entry.RefreshState
	}
}

func buildAggregatedOutputs(aggregated map[string]*aggregateEntry) (int, int, []coremain.DomainPoolDomain) {
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
		if entry.Promoted {
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
			LastSeenAtUnixMS:     entry.LastSeenAtUnixMS,
			LastDirtyAtUnixMS:    entry.LastDirtyAtUnixMS,
			LastVerifiedAtUnixMS: entry.LastVerifiedAtUnixMS,
			CooldownUntilUnixMS:  entry.CooldownUntilUnixMS,
			DirtyReason:          entry.DirtyReason,
			RefreshState:         entry.RefreshState,
		})
	}
	for i := range domains {
		domains[i].PoolTag = ""
	}
	return promotedCount, dirtyCount, domains
}

func buildPoolMeta(d *domainMemoryPool, domainCount, variantCount, promotedCount, dirtyCount, publishedCount int) coremain.DomainPoolMeta {
	return coremain.DomainPoolMeta{
		PoolTag:              d.pluginTag,
		PoolKind:             coremain.DomainPoolKindMemory,
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

func (d *domainMemoryPool) snapshotPromotedRules() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.pruneExpiredLocked()
	promoted := make(map[string]struct{}, d.domainCount)
	for key, entry := range d.stats {
		if !entry.Promoted {
			continue
		}
		domain := key.domain
		if domain == "" {
			continue
		}
		promoted[domain] = struct{}{}
	}
	rules := make([]string, 0, len(promoted))
	for domain := range promoted {
		rules = append(rules, "full:"+domain)
	}
	return normalizePoolHotRules(rules)
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
		LastSeenAtUnixMS:     entry.LastSeenAtUnixMS,
		LastDirtyAtUnixMS:    entry.LastDirtyAtUnixMS,
		LastVerifiedAtUnixMS: entry.LastVerifiedAtUnixMS,
		CooldownUntilUnixMS:  entry.CooldownUntilUnixMS,
		DirtyReason:          entry.DirtyReason,
		RefreshState:         entry.RefreshState,
		ConflictCount:        entry.ConflictCount,
	}
}

func (d *domainMemoryPool) shouldPromote(entry *statEntry) bool {
	if entry.Count < d.policy.promoteAfter {
		return false
	}
	if d.isStale(entry.LastSeenAtUnixMS) {
		return false
	}
	switch d.policy.kind {
	case "nov4", "nodenov4":
		return entry.QTypeMask&qtypeMaskA != 0
	case "nov6", "nodenov6":
		return entry.QTypeMask&qtypeMaskAAAA != 0
	default:
		return true
	}
}

func (d *domainMemoryPool) isStale(lastSeenAtUnixMS int64) bool {
	if d.policy.decayDays <= 0 || lastSeenAtUnixMS <= 0 {
		return false
	}
	return time.Since(time.UnixMilli(lastSeenAtUnixMS)) > time.Duration(d.policy.decayDays)*24*time.Hour
}

func hashPromotedDomains(domains []coremain.DomainPoolDomain) uint64 {
	h := fnv.New64a()
	names := make([]string, 0, len(domains))
	for _, domain := range domains {
		if !domain.Promoted {
			continue
		}
		names = append(names, domain.Domain)
	}
	sort.Strings(names)
	for _, domain := range names {
		_, _ = h.Write([]byte("full:"))
		_, _ = h.Write([]byte(domain))
		_, _ = h.Write([]byte{'\n'})
	}
	return h.Sum64()
}

func (d *domainMemoryPool) nextDirtyReason(entry *statEntry, now time.Time) string {
	if !entry.Promoted || d.policy.requeryTag == "" {
		return ""
	}
	if entry.CooldownUntilUnixMS > 0 && now.UnixMilli() < entry.CooldownUntilUnixMS {
		if entry.RefreshState == "" {
			entry.RefreshState = "cooldown"
		}
		return ""
	}
	if entry.LastDirtyAtUnixMS <= 0 {
		return "observed"
	}
	if d.policy.staleAfterMinutes > 0 {
		if now.Sub(time.UnixMilli(entry.LastDirtyAtUnixMS)) >= time.Duration(d.policy.staleAfterMinutes)*time.Minute {
			return "stale"
		}
	}
	return ""
}

func (d *domainMemoryPool) Close() error {
	var closeErr error
	d.closeOnce.Do(func() {
		if d.workerStarted.Load() {
			close(d.stopChan)
			<-d.workerDoneChan
		}
		closeErr = d.performWrite(WriteModeShutdown)
		if err := d.stopHotPublisher(); err != nil && closeErr == nil {
			closeErr = err
		}
	})
	return closeErr
}

func (d *domainMemoryPool) PrepareForRestart() error {
	return d.Close()
}

func (d *domainMemoryPool) logWriteFailure(mode WriteMode, err error) {
	if d.logger == nil || err == nil {
		return
	}
	d.logger.Warn(
		"domain_memory_pool write failed",
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

func maxInt64(current, next int64) int64 {
	if next > current {
		return next
	}
	return current
}
