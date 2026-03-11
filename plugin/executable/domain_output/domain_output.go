package domain_output

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

const (
	PluginType        = "domain_output"
	RecordBufferLimit = 10240

	qtypeMaskA    uint8 = 1 << 0
	qtypeMaskAAAA uint8 = 1 << 1

	defaultDirtyNotifyPath = "/plugins/requery/enqueue"
)

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
	sequence.MustRegExecQuickSetup(PluginType, QuickSetup)
}

type Args struct {
	FileStat       string      `yaml:"file_stat"`
	FileRule       string      `yaml:"file_rule"`
	GenRule        string      `yaml:"gen_rule"`
	Pattern        string      `yaml:"pattern"`
	AppendedString string      `yaml:"appended_string"`
	MaxEntries     int         `yaml:"max_entries"`
	DumpInterval   int         `yaml:"dump_interval"`
	DomainSetURL   string      `yaml:"domain_set_url"`
	EnableFlags    bool        `yaml:"enable_flags"`
	Policy         *PolicyArgs `yaml:"policy"`
}

type PolicyArgs struct {
	Kind                   string `yaml:"kind"`
	PromoteAfter           int    `yaml:"promote_after"`
	DecayDays              int    `yaml:"decay_days"`
	TrackQType             bool   `yaml:"track_qtype"`
	PublishMode            string `yaml:"publish_mode"`
	StaleAfterMinutes      int    `yaml:"stale_after_minutes"`
	RefreshCooldownMinutes int    `yaml:"refresh_cooldown_minutes"`
	OnDirtyURL             string `yaml:"on_dirty_url"`
	VerifyURL              string `yaml:"verify_url"`
}

type writePolicy struct {
	kind                   string
	promoteAfter           int
	decayDays              int
	trackQType             bool
	publishMode            string
	staleAfterMinutes      int
	refreshCooldownMinutes int
	onDirtyURL             string
	verifyURL              string
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
	fileStat       string
	fileRule       string
	genRule        string
	pattern        string
	appendedString string
	maxEntries     int
	dumpInterval   time.Duration
	policy         writePolicy

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
	currentDate        atomic.Value
	recordChan         chan *logItem
	writeSignalChan    chan struct{}
	stopChan           chan struct{}
	workerDoneChan     chan struct{}

	domainSetURL  string
	enableFlags   bool
	memoryID      string
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

type dirtyEvent struct {
	Domain     string `json:"domain"`
	MemoryID   string `json:"memory_id"`
	QTypeMask  uint8  `json:"qtype_mask"`
	Reason     string `json:"reason"`
	VerifyURL  string `json:"verify_url,omitempty"`
	ObservedAt string `json:"observed_at"`
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
	cfg := args.(*Args)
	d := newDomainOutput(cfg)
	d.loadFromFile()
	go d.startWorker()
	bp.RegAPI(d.Api())
	return d, nil
}

func QuickSetup(_ sequence.BQ, s string) (any, error) {
	params := strings.Split(s, ",")
	if len(params) < 6 || len(params) > 7 {
		return nil, errors.New("invalid quick setup arguments: need 6 or 7 fields")
	}
	maxEntries, err := strconv.Atoi(params[4])
	if err != nil {
		return nil, err
	}
	dumpInterval, err := strconv.Atoi(params[5])
	if err != nil || dumpInterval <= 0 {
		dumpInterval = 60
	}
	args := &Args{
		FileStat:     params[0],
		FileRule:     params[1],
		GenRule:      params[2],
		Pattern:      params[3],
		MaxEntries:   maxEntries,
		DumpInterval: dumpInterval,
		Policy:       &PolicyArgs{},
	}
	if len(params) == 7 {
		args.DomainSetURL = params[6]
	}
	d := newDomainOutput(args)
	d.loadFromFile()
	go d.startWorker()
	return d, nil
}

func newDomainOutput(cfg *Args) *domainOutput {
	dumpInterval := cfg.DumpInterval
	if dumpInterval <= 0 {
		dumpInterval = 60
	}
	policy := normalizePolicy(cfg)
	d := &domainOutput{
		fileStat:        cfg.FileStat,
		fileRule:        cfg.FileRule,
		genRule:         cfg.GenRule,
		pattern:         cfg.Pattern,
		appendedString:  cfg.AppendedString,
		maxEntries:      cfg.MaxEntries,
		dumpInterval:    time.Duration(dumpInterval) * time.Second,
		policy:          policy,
		stats:           make(map[string]*statEntry),
		recordChan:      make(chan *logItem, RecordBufferLimit),
		writeSignalChan: make(chan struct{}, 1),
		stopChan:        make(chan struct{}),
		workerDoneChan:  make(chan struct{}),
		domainSetURL:    cfg.DomainSetURL,
		enableFlags:     cfg.EnableFlags,
		memoryID:        inferMemoryID(cfg),
	}
	d.currentDate.Store(time.Now().Format("2006-01-02"))
	return d
}

func normalizePolicy(cfg *Args) writePolicy {
	apiBase := inferAPIBaseFromConfig(cfg)
	kind := "generic"
	promoteAfter := 1
	decayDays := 30
	publishMode := "all"
	trackQType := false
	staleAfterMinutes := 0
	refreshCooldownMinutes := 120
	onDirtyURL := ""
	verifyURL := ""

	infer := strings.ToLower(strings.Join([]string{cfg.FileStat, cfg.FileRule, cfg.GenRule, cfg.DomainSetURL}, " "))
	switch {
	case strings.Contains(infer, "realip"):
		kind = "realip"
		promoteAfter = 2
		decayDays = 21
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 360
		onDirtyURL = buildAPIURL(apiBase, defaultDirtyNotifyPath)
		verifyURL = buildAPIURL(apiBase, "/plugins/my_realiplist/verify")
	case strings.Contains(infer, "fakeip"):
		kind = "fakeip"
		promoteAfter = 2
		decayDays = 21
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 240
		onDirtyURL = buildAPIURL(apiBase, defaultDirtyNotifyPath)
		verifyURL = buildAPIURL(apiBase, "/plugins/my_fakeiplist/verify")
	case strings.Contains(infer, "nodenov4"):
		kind = "nov4"
		promoteAfter = 2
		decayDays = 14
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 180
		onDirtyURL = buildAPIURL(apiBase, defaultDirtyNotifyPath)
		verifyURL = buildAPIURL(apiBase, "/plugins/my_nodenov4list/verify")
	case strings.Contains(infer, "nodenov6"):
		kind = "nov6"
		promoteAfter = 2
		decayDays = 14
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 180
		onDirtyURL = buildAPIURL(apiBase, defaultDirtyNotifyPath)
		verifyURL = buildAPIURL(apiBase, "/plugins/my_nodenov6list/verify")
	case strings.Contains(infer, "nov4"):
		kind = "nov4"
		promoteAfter = 2
		decayDays = 14
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 180
		onDirtyURL = buildAPIURL(apiBase, defaultDirtyNotifyPath)
		verifyURL = buildAPIURL(apiBase, "/plugins/my_nov4list/verify")
	case strings.Contains(infer, "nov6"):
		kind = "nov6"
		promoteAfter = 2
		decayDays = 14
		publishMode = "promoted_only"
		trackQType = true
		staleAfterMinutes = 180
		onDirtyURL = buildAPIURL(apiBase, defaultDirtyNotifyPath)
		verifyURL = buildAPIURL(apiBase, "/plugins/my_nov6list/verify")
	}

	if cfg.Policy != nil {
		if cfg.Policy.Kind != "" {
			kind = strings.ToLower(cfg.Policy.Kind)
		}
		if cfg.Policy.PromoteAfter > 0 {
			promoteAfter = cfg.Policy.PromoteAfter
		}
		if cfg.Policy.DecayDays > 0 {
			decayDays = cfg.Policy.DecayDays
		}
		if cfg.Policy.PublishMode != "" {
			publishMode = strings.ToLower(cfg.Policy.PublishMode)
		}
		if cfg.Policy.TrackQType {
			trackQType = true
		}
		if cfg.Policy.StaleAfterMinutes > 0 {
			staleAfterMinutes = cfg.Policy.StaleAfterMinutes
		}
		if cfg.Policy.RefreshCooldownMinutes > 0 {
			refreshCooldownMinutes = cfg.Policy.RefreshCooldownMinutes
		}
		if cfg.Policy.OnDirtyURL != "" {
			onDirtyURL = cfg.Policy.OnDirtyURL
		}
		if cfg.Policy.VerifyURL != "" {
			verifyURL = cfg.Policy.VerifyURL
		}
	}

	if kind == "generic" && cfg.FileRule == "" && cfg.DomainSetURL == "" {
		publishMode = "all"
	}
	return writePolicy{
		kind:                   kind,
		promoteAfter:           promoteAfter,
		decayDays:              decayDays,
		trackQType:             trackQType,
		publishMode:            publishMode,
		staleAfterMinutes:      staleAfterMinutes,
		refreshCooldownMinutes: refreshCooldownMinutes,
		onDirtyURL:             onDirtyURL,
		verifyURL:              verifyURL,
	}
}

func inferAPIBaseFromConfig(cfg *Args) string {
	if cfg == nil || cfg.DomainSetURL == "" {
		return ""
	}
	u, err := url.Parse(cfg.DomainSetURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func buildAPIURL(base, path string) string {
	if base == "" || path == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + path
}

func (d *domainOutput) Exec(ctx context.Context, qCtx *query_context.Context) error {
	d.enqueueFromContext(qCtx, "live")
	return nil
}

func (d *domainOutput) GetFastExec() func(ctx context.Context, qCtx *query_context.Context) error {
	rChan := d.recordChan
	enableFlags := d.enableFlags
	trackQType := d.policy.trackQType
	return func(ctx context.Context, qCtx *query_context.Context) error {
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
	ticker := time.NewTicker(d.dumpInterval)
	defer ticker.Stop()
	defer close(d.workerDoneChan)

	for {
		select {
		case item := <-d.recordChan:
			d.processRecord(item)
		case <-ticker.C:
			d.performWrite(WriteModePeriodic)
		case <-d.writeSignalChan:
			d.performWrite(WriteModePeriodic)
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
	var notify *dirtyEvent

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
			if d.policy.onDirtyURL != "" && entry.Promoted {
				notify = &dirtyEvent{
					Domain:     strings.TrimSuffix(item.name, "."),
					MemoryID:   d.memoryID,
					QTypeMask:  entry.QTypeMask,
					Reason:     reason,
					VerifyURL:  d.policy.verifyURL,
					ObservedAt: nowStamp,
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
	storageKey := rawDomain
	if !enableFlags {
		return storageKey
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
		return storageKey
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

func (d *domainOutput) performWrite(mode WriteMode) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	if mode == WriteModePeriodic && !d.dirtyPending.Load() {
		return
	}

	d.currentDate.Store(time.Now().Format("2006-01-02"))
	snapshot := d.buildSnapshot(mode)
	writeOK, rulesChanged := d.writeSnapshot(snapshot, mode)
	if mode != WriteModeShutdown && rulesChanged {
		d.pushToDomainSet(snapshot.rules)
	}
	if writeOK {
		d.dirtyPending.Store(false)
	}
	atomic.StoreInt64(&d.promotedCount, int64(snapshot.promotedCount))
	atomic.StoreInt64(&d.publishedCount, int64(len(snapshot.rules)))
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
	type agg struct {
		count    int
		lastDate string
		promoted bool
	}
	aggregated := make(map[string]agg)
	promotedCount := 0
	for _, entry := range items {
		key := entry.Domain
		domainOnly := strings.Split(key, "|")[0]
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
	if d.isStale(entry.LastDate) {
		return false
	}
	if entry.Count < d.policy.promoteAfter {
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

func (d *domainOutput) writeSnapshot(snapshot writeSnapshot, mode WriteMode) (bool, bool) {
	writeFile := func(filePath string, writeContent func(io.Writer) error) bool {
		if filePath == "" {
			return true
		}
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return false
		}
		tmpFile := filePath + ".tmp"
		f, err := os.OpenFile(tmpFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return false
		}
		writer := bufio.NewWriter(f)
		writeErr := writeContent(writer)
		flushErr := writer.Flush()
		closeErr := f.Close()
		if writeErr != nil || flushErr != nil || closeErr != nil {
			_ = os.Remove(tmpFile)
			return false
		}
		if err := os.Rename(tmpFile, filePath); err != nil {
			_ = os.Remove(tmpFile)
			return false
		}
		return true
	}

	rulesHash := hashRules(snapshot.rules)
	rulesChanged := !d.hasRulesHash || d.lastRulesHash != rulesHash
	if mode == WriteModeFlush {
		// flush 需要强制同步空规则到下游 domain_set
		rulesChanged = true
	}

	sort.Slice(snapshot.items, func(i, j int) bool {
		if snapshot.items[i].Count == snapshot.items[j].Count {
			return snapshot.items[i].Domain < snapshot.items[j].Domain
		}
		return snapshot.items[i].Count > snapshot.items[j].Count
	})

	ok := writeFile(d.fileStat, func(w io.Writer) error {
		for _, item := range snapshot.items {
			_, err := fmt.Fprintf(
				w,
				"%010d %s %s qmask=%d score=%d promoted=%d last_seen=%s last_dirty=%s last_verified=%s dirty_reason=%s refresh_state=%s cooldown_until=%s conflicts=%d\n",
				item.Count,
				item.Date,
				item.Domain,
				item.QMask,
				item.Score,
				boolToInt(item.Prom),
				sanitizeStatToken(item.LastSeenAt),
				sanitizeStatToken(item.LastDirtyAt),
				sanitizeStatToken(item.LastVerifiedAt),
				sanitizeStatToken(item.DirtyReason),
				sanitizeStatToken(item.RefreshState),
				sanitizeStatToken(item.CooldownUntil),
				item.ConflictCount,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})

	needRuleWrite := mode != WriteModePeriodic || rulesChanged
	if needRuleWrite {
		if !writeFile(d.fileRule, func(w io.Writer) error {
			for _, domain := range snapshot.rules {
				if _, err := fmt.Fprintf(w, "full:%s\n", domain); err != nil {
					return err
				}
			}
			return nil
		}) {
			ok = false
		}

		if !writeFile(d.genRule, func(w io.Writer) error {
			if d.pattern == "" {
				return nil
			}
			if d.appendedString != "" {
				if _, err := fmt.Fprintln(w, d.appendedString); err != nil {
					return err
				}
			}
			for _, domain := range snapshot.rules {
				line := strings.ReplaceAll(d.pattern, "DOMAIN", domain)
				if _, err := fmt.Fprintln(w, line); err != nil {
					return err
				}
			}
			return nil
		}) {
			ok = false
		}
	}

	if ok {
		d.lastRulesHash = rulesHash
		d.hasRulesHash = true
	}
	return ok, rulesChanged
}

func hashRules(rules []string) uint64 {
	h := fnv.New64a()
	for _, domain := range rules {
		_, _ = h.Write([]byte(domain))
		_, _ = h.Write([]byte{'\n'})
	}
	return h.Sum64()
}

func (d *domainOutput) loadFromFile() {
	if d.fileStat == "" {
		return
	}
	f, err := os.Open(d.fileStat)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	today := time.Now().Format("2006-01-02")

	d.mu.Lock()
	defer d.mu.Unlock()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		count, _ := strconv.Atoi(fields[0])
		lastDate := today
		domain := ""
		startExtras := 2
		if len(fields) >= 3 && strings.Count(fields[1], "-") == 2 {
			lastDate = fields[1]
			domain = fields[2]
			startExtras = 3
		} else {
			domain = fields[1]
		}
		entry := &statEntry{Count: count, Score: count, LastDate: lastDate}
		for _, field := range fields[startExtras:] {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch k {
			case "qmask":
				if parsed, err := strconv.Atoi(v); err == nil {
					entry.QTypeMask = uint8(parsed)
				}
			case "score":
				if parsed, err := strconv.Atoi(v); err == nil {
					entry.Score = parsed
				}
			case "promoted":
				entry.Promoted = v == "1"
			case "last_seen":
				entry.LastSeenAt = restoreStatToken(v)
			case "last_dirty":
				entry.LastDirtyAt = restoreStatToken(v)
			case "last_verified":
				entry.LastVerifiedAt = restoreStatToken(v)
			case "dirty_reason":
				entry.DirtyReason = restoreStatToken(v)
			case "refresh_state":
				entry.RefreshState = restoreStatToken(v)
			case "cooldown_until":
				entry.CooldownUntil = restoreStatToken(v)
			case "conflicts":
				if parsed, err := strconv.Atoi(v); err == nil {
					entry.ConflictCount = parsed
				}
			}
		}
		entry.Promoted = d.shouldPromote(entry)
		if d.maxEntries > 0 && len(d.stats) >= d.maxEntries {
			continue
		}
		d.stats[domain] = entry
		atomic.AddInt64(&d.totalCount, int64(count))
	}
}

func (d *domainOutput) pushToDomainSet(rules []string) {
	if d.domainSetURL == "" {
		return
	}
	payload := struct {
		Values []string `json:"values"`
	}{Values: make([]string, len(rules))}
	copy(payload.Values, rules)
	body, _ := json.Marshal(payload)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.domainSetURL, bytes.NewReader(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
}

func (d *domainOutput) Close() error {
	d.closeOnce.Do(func() {
		close(d.stopChan)
		<-d.workerDoneChan
		d.performWrite(WriteModeShutdown)
	})
	return nil
}

func restartSelf() {
	time.Sleep(100 * time.Millisecond)
	bin, err := os.Executable()
	if err != nil {
		os.Exit(0)
	}
	_ = syscall.Exec(bin, os.Args, os.Environ())
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func sanitizeStatToken(v string) string {
	if v == "" {
		return "-"
	}
	return strings.ReplaceAll(v, " ", "_")
}

func restoreStatToken(v string) string {
	if v == "-" {
		return ""
	}
	return strings.ReplaceAll(v, "_", " ")
}

func inferMemoryID(cfg *Args) string {
	infer := strings.ToLower(strings.Join([]string{cfg.FileStat, cfg.FileRule, cfg.GenRule, cfg.DomainSetURL}, " "))
	switch {
	case strings.Contains(infer, "realip"):
		return "realip"
	case strings.Contains(infer, "fakeip"):
		return "fakeip"
	case strings.Contains(infer, "nodenov4"):
		return "nodenov4"
	case strings.Contains(infer, "nodenov6"):
		return "nodenov6"
	case strings.Contains(infer, "nov4"):
		return "nov4"
	case strings.Contains(infer, "nov6"):
		return "nov6"
	case strings.Contains(infer, "top_domains"):
		return "top"
	default:
		return "generic"
	}
}

func (d *domainOutput) nextDirtyReason(entry *statEntry, now time.Time) string {
	if !entry.Promoted || d.policy.onDirtyURL == "" {
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
		if ts, err := time.Parse(time.RFC3339, entry.LastDirtyAt); err == nil && now.Sub(ts) >= time.Duration(d.policy.staleAfterMinutes)*time.Minute {
			return "stale"
		}
	}
	return ""
}

func (d *domainOutput) notifyDirty(event dirtyEvent) {
	body, err := json.Marshal(event)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.policy.onDirtyURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var _ sequence.Executable = (*domainOutput)(nil)
