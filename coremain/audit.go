package coremain

import (
	"container/heap"
	"container/list"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

// --- Optimized String Interning with Constant Fast-Path ---
const lruCacheSize = 16384

type lruEntry struct {
	key   string
	value string
}

type lruCache struct {
	mu       sync.Mutex
	capacity int
	cache    map[string]*list.Element
	ll       *list.List
}

func newLRUCache(capacity int) *lruCache {
	if capacity <= 0 {
		capacity = lruCacheSize
	}
	return &lruCache{
		capacity: capacity,
		cache:    make(map[string]*list.Element, capacity),
		ll:       list.New(),
	}
}

func (l *lruCache) Get(key string) (value string, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if elem, hit := l.cache[key]; hit {
		l.ll.MoveToFront(elem)
		return elem.Value.(*lruEntry).value, true
	}
	return "", false
}

func (l *lruCache) Put(key, value string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if elem, hit := l.cache[key]; hit {
		l.ll.MoveToFront(elem)
		elem.Value.(*lruEntry).value = value
		return
	}

	if l.ll.Len() >= l.capacity {
		oldest := l.ll.Back()
		if oldest != nil {
			l.ll.Remove(oldest)
			delete(l.cache, oldest.Value.(*lruEntry).key)
		}
	}

	elem := l.ll.PushFront(&lruEntry{key: key, value: value})
	l.cache[key] = elem
}

var globalStringLRU = newLRUCache(lruCacheSize)

func internString(s string) string {
	// OPTIMIZATION: Fast-path for common DNS constants to bypass LRU lock entirely.
	switch s {
	case "A", "AAAA", "CNAME", "TXT", "NS", "MX", "PTR", "SOA", "SRV", "HTTPS", "SVCB",
		"NOERROR", "FORMERR", "SERVFAIL", "NXDOMAIN", "NOTIMP", "REFUSED", "IN",
		"NO_RESPONSE", "unmatched_rule":
		return s
	}

	if val, ok := globalStringLRU.Get(s); ok {
		return val
	}
	globalStringLRU.Put(s, s)
	return s
}

type auditContext struct {
	Ctx                *query_context.Context
	ProcessingDuration time.Duration
}

// Pool for auditContext to minimize GC overhead during high-load periods.
var auditCtxPool = sync.Pool{
	New: func() any { return new(auditContext) },
}

type AnswerDetail struct {
	Type string `json:"type"`
	TTL  uint32 `json:"ttl"`
	Data string `json:"data"`
}

type AuditLog struct {
	ClientIP      string         `json:"client_ip"`
	QueryType     string         `json:"query_type"`
	QueryName     string         `json:"query_name"`
	QueryClass    string         `json:"query_class"`
	QueryTime     time.Time      `json:"query_time"`
	DurationMs    float64        `json:"duration_ms"`
	TraceID       string         `json:"trace_id"`
	ResponseCode  string         `json:"response_code"`
	ResponseFlags ResponseFlags  `json:"response_flags"`
	Answers       []AnswerDetail `json:"answers"`
	DomainSet     string         `json:"domain_set,omitempty"`
}

type ResponseFlags struct {
	AA bool `json:"aa"`
	TC bool `json:"tc"`
	RA bool `json:"ra"`
}

const (
	defaultAuditMemoryEntries = 100000
	maxAuditMemoryEntries     = 400000
	defaultAuditRetentionDays = 30
	maxAuditRetentionDays     = 365
	defaultAuditMaxDiskSizeMB = 10
	maxAuditMaxDiskSizeMB     = 10240
	slowestQueriesCapacity    = 300
	auditChannelCapacity      = 10240
	auditSettingsFilename     = "audit_settings.json"
	auditLogsDirname          = "audit_logs"
)

type AuditSettings struct {
	MemoryEntries int    `json:"memory_entries"`
	RetentionDays int    `json:"retention_days"`
	MaxDiskSizeMB int    `json:"max_disk_size_mb"`
	MaxDBSizeMB   int    `json:"max_db_size_mb,omitempty"`
	StorageEngine string `json:"storage_engine,omitempty"`
	SQLitePath    string `json:"sqlite_path,omitempty"`
	DualWrite     bool   `json:"dual_write,omitempty"`
	Capacity      int    `json:"capacity,omitempty"`
}

type slowestQueryHeap []AuditLog

func (h slowestQueryHeap) Len() int           { return len(h) }
func (h slowestQueryHeap) Less(i, j int) bool { return h[i].DurationMs < h[j].DurationMs }
func (h slowestQueryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *slowestQueryHeap) Push(x any) {
	*h = append(*h, x.(AuditLog))
}

func (h *slowestQueryHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type AuditCollector struct {
	mu                 sync.RWMutex
	capturing          bool
	capacity           int
	logs               []AuditLog
	head               int
	slowestQueries     slowestQueryHeap
	domainCounts       map[string]int
	clientCounts       map[string]int
	domainSetCounts    map[string]int
	totalQueryCount    uint64
	totalQueryDuration float64
	ctxChan            chan *auditContext
	workerDone         chan struct{}
	configBaseDir      string
	logDir             string
	settings           AuditSettings
	ndjsonStorage      *NdjsonAuditStorage
	sqliteStorage      *SQLiteAuditStorage

	lastDiskCleanup time.Time

	// Global Statistics for monitoring without mutex pressure
	totalQueryCountGlobal    atomic.Uint64
	totalQueryDurationGlobal atomic.Uint64 // Stored in microseconds
}

var GlobalAuditCollector = NewAuditCollector(AuditSettings{
	MemoryEntries: defaultAuditMemoryEntries,
	RetentionDays: defaultAuditRetentionDays,
	MaxDiskSizeMB: defaultAuditMaxDiskSizeMB,
	MaxDBSizeMB:   defaultAuditMaxDBSizeMB,
	StorageEngine: defaultAuditStorageEngine,
}, "")

func InitializeAuditCollector(configBaseDir string, base *AuditSettings) {
	settings := loadAuditSettings(configBaseDir, base)
	GlobalAuditCollector = NewAuditCollector(settings, configBaseDir)
	if err := GlobalAuditCollector.restoreFromDisk(); err != nil {
		mlog.L().Warn("failed to restore audit logs from disk", zap.Error(err))
	}
}

func NewAuditCollector(settings AuditSettings, configBaseDir string) *AuditCollector {
	settings = normalizeAuditSettings(settings)
	c := &AuditCollector{
		capturing:          true,
		capacity:           settings.MemoryEntries,
		logs:               make([]AuditLog, 0, settings.MemoryEntries),
		slowestQueries:     make(slowestQueryHeap, 0, slowestQueriesCapacity),
		domainCounts:       make(map[string]int),
		clientCounts:       make(map[string]int),
		domainSetCounts:    make(map[string]int),
		totalQueryCount:    0,
		totalQueryDuration: 0.0,
		ctxChan:            make(chan *auditContext, auditChannelCapacity),
		workerDone:         make(chan struct{}),
		configBaseDir:      configBaseDir,
		logDir:             filepath.Join(configBaseDir, auditLogsDirname),
		settings:           settings,
	}
	heap.Init(&c.slowestQueries)
	if err := c.configureStorages(); err != nil {
		mlog.L().Warn("failed to configure audit storages", zap.Error(err))
	}
	return c
}

func (c *AuditCollector) StartWorker() {
	go c.worker()
}

func (c *AuditCollector) StopWorker() {
	close(c.ctxChan)
	<-c.workerDone
}

func (c *AuditCollector) worker() {
	defer close(c.workerDone)

	// Batch processing slice to reduce lock contention frequency
	batch := make([]*auditContext, 0, 256)

	for {
		batch = batch[:0]

		wrappedCtx, ok := <-c.ctxChan
		if !ok {
			return
		}
		batch = append(batch, wrappedCtx)

		// Non-blocking drain to fill the batch
	drainLoop:
		for len(batch) < cap(batch) {
			select {
			case nextItem, ok := <-c.ctxChan:
				if !ok {
					break drainLoop
				}
				batch = append(batch, nextItem)
			default:
				break drainLoop
			}
		}

		c.processBatch(batch)

		for _, item := range batch {
			auditCtxPool.Put(item)
		}
	}
}

func (c *AuditCollector) processBatch(batch []*auditContext) {
	c.mu.Lock()
	persistedLogs := make([]AuditLog, 0, len(batch))

	for _, wrappedCtx := range batch {
		if wrappedCtx == nil || wrappedCtx.Ctx == nil {
			continue
		}

		qCtx := wrappedCtx.Ctx
		qQuestion := qCtx.QQuestion()
		duration := wrappedCtx.ProcessingDuration
		durationMs := float64(duration.Microseconds()) / 1000.0

		// Instant update of global atomic statistics
		c.totalQueryCountGlobal.Add(1)
		c.totalQueryDurationGlobal.Add(uint64(duration.Microseconds()))

		if !c.capturing || c.capacity == 0 {
			continue
		}

		// Optimized IP parsing: Strip port before interning to maximize LRU cache utility
		clientAddr := qCtx.ServerMeta.ClientAddr.String()
		if host, _, err := net.SplitHostPort(clientAddr); err == nil {
			clientAddr = host
		}

		// Optimized domain trim without allocating new string unless necessary
		qName := qQuestion.Name
		if len(qName) > 1 && qName[len(qName)-1] == '.' {
			qName = qName[:len(qName)-1]
		}

		log := AuditLog{
			ClientIP:   internString(clientAddr),
			QueryType:  internString(dns.TypeToString[qQuestion.Qtype]),
			QueryName:  internString(qName),
			QueryClass: internString(dns.ClassToString[qQuestion.Qclass]),
			QueryTime:  qCtx.StartTime(),
			DurationMs: durationMs,
			TraceID:    qCtx.TraceID, // OPTIMIZATION: Do not intern unique TraceIDs.
		}

		if val, ok := qCtx.GetValue(query_context.KeyDomainSet); ok {
			if name, isString := val.(string); isString {
				log.DomainSet = name
			}
		}

		if log.DomainSet == "" {
			log.DomainSet = "unmatched_rule"
		}

		if resp := qCtx.R(); resp != nil {
			log.ResponseCode = internString(dns.RcodeToString[resp.Rcode])
			log.ResponseFlags = ResponseFlags{
				AA: resp.Authoritative,
				TC: resp.Truncated,
				RA: resp.RecursionAvailable,
			}

			if len(resp.Answer) > 0 {
				log.Answers = make([]AnswerDetail, 0, len(resp.Answer))
				for _, ans := range resp.Answer {
					header := ans.Header()
					detail := AnswerDetail{
						Type: internString(dns.TypeToString[header.Rrtype]),
						TTL:  header.Ttl,
					}
					switch record := ans.(type) {
					case *dns.A:
						detail.Data = internString(record.A.String())
					case *dns.AAAA:
						detail.Data = internString(record.AAAA.String())
					case *dns.CNAME:
						detail.Data = internString(record.Target)
					case *dns.PTR:
						detail.Data = internString(record.Ptr)
					case *dns.NS:
						detail.Data = internString(record.Ns)
					case *dns.MX:
						detail.Data = internString(record.Mx)
					case *dns.TXT:
						detail.Data = internString(strings.Join(record.Txt, " "))
					default:
						detail.Data = internString(ans.String())
					}
					log.Answers = append(log.Answers, detail)
				}
			}
		} else {
			log.ResponseCode = "NO_RESPONSE"
		}

		c.appendLogLocked(log)
		persistedLogs = append(persistedLogs, log)
	}
	c.mu.Unlock()

	if len(persistedLogs) > 0 {
		if err := c.appendBatchToDisk(persistedLogs); err != nil {
			mlog.L().Warn("failed to persist audit logs", zap.Error(err))
		}
		if err := c.maybeEnforceDiskRetention(); err != nil {
			mlog.L().Warn("failed to enforce audit log retention", zap.Error(err))
		}
	}
}

func (c *AuditCollector) appendLogLocked(log AuditLog) {
	if c.capacity <= 0 {
		return
	}
	if len(c.logs) < c.capacity {
		c.logs = append(c.logs, log)
	} else {
		c.removeLogStatsLocked(c.logs[c.head])
		c.logs[c.head] = log
		c.head = (c.head + 1) % c.capacity
	}

	c.addLogStatsLocked(log)
	if c.slowestQueries.Len() < slowestQueriesCapacity {
		heap.Push(&c.slowestQueries, log)
	} else if log.DurationMs > c.slowestQueries[0].DurationMs {
		c.slowestQueries[0] = log
		heap.Fix(&c.slowestQueries, 0)
	}
}

func (c *AuditCollector) addLogStatsLocked(log AuditLog) {
	c.domainCounts[log.QueryName]++
	c.clientCounts[log.ClientIP]++
	c.domainSetCounts[normalizeAuditDomainSet(log.DomainSet, log.QueryType)]++
	c.totalQueryCount++
	c.totalQueryDuration += log.DurationMs
}

func (c *AuditCollector) removeLogStatsLocked(log AuditLog) {
	decrementCountMap(c.domainCounts, log.QueryName)
	decrementCountMap(c.clientCounts, log.ClientIP)
	decrementCountMap(c.domainSetCounts, normalizeAuditDomainSet(log.DomainSet, log.QueryType))
	if c.totalQueryCount > 0 {
		c.totalQueryCount--
	}
	c.totalQueryDuration -= log.DurationMs
	if c.totalQueryDuration < 0 {
		c.totalQueryDuration = 0
	}
}

func (c *AuditCollector) syncGlobalTotalsLocked() {
	c.totalQueryCountGlobal.Store(c.totalQueryCount)
	c.totalQueryDurationGlobal.Store(durationMicrosFromMilliseconds(c.totalQueryDuration))
}

func (c *AuditCollector) Collect(qCtx *query_context.Context) {
	if !c.IsCapturing() {
		return
	}

	duration := time.Since(qCtx.StartTime())

	// Retrieve object from pool to reduce heap pressure
	wrappedCtx := auditCtxPool.Get().(*auditContext)
	wrappedCtx.Ctx = qCtx
	wrappedCtx.ProcessingDuration = duration

	select {
	case c.ctxChan <- wrappedCtx:
	default:
		// Non-blocking drop during system overload
		auditCtxPool.Put(wrappedCtx)
	}
}

func (c *AuditCollector) Start() { c.mu.Lock(); c.capturing = true; c.mu.Unlock() }
func (c *AuditCollector) Stop()  { c.mu.Lock(); c.capturing = false; c.mu.Unlock() }
func (c *AuditCollector) IsCapturing() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capturing
}

func (c *AuditCollector) snapshotChronologicalLocked() []AuditLog {
	if c.capacity == 0 || len(c.logs) == 0 {
		return []AuditLog{}
	}
	if len(c.logs) < c.capacity {
		logsCopy := make([]AuditLog, len(c.logs))
		copy(logsCopy, c.logs)
		return logsCopy
	}
	logsCopy := make([]AuditLog, c.capacity)
	copy(logsCopy, c.logs[c.head:])
	copy(logsCopy[c.capacity-c.head:], c.logs[:c.head])
	return logsCopy
}

func (c *AuditCollector) rebuildDerivedLocked() {
	c.slowestQueries = make(slowestQueryHeap, 0, slowestQueriesCapacity)
	heap.Init(&c.slowestQueries)
	c.domainCounts = make(map[string]int)
	c.clientCounts = make(map[string]int)
	c.domainSetCounts = make(map[string]int)
	c.totalQueryCount = 0
	c.totalQueryDuration = 0.0
	for _, log := range c.snapshotChronologicalLocked() {
		c.addLogStatsLocked(log)
		if c.slowestQueries.Len() < slowestQueriesCapacity {
			heap.Push(&c.slowestQueries, log)
		} else if log.DurationMs > c.slowestQueries[0].DurationMs {
			c.slowestQueries[0] = log
			heap.Fix(&c.slowestQueries, 0)
		}
	}
	c.syncGlobalTotalsLocked()
}

func (c *AuditCollector) GetLogs() []AuditLog {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshotChronologicalLocked()
}

func (c *AuditCollector) ClearLogs(clearDisk bool) {
	c.mu.Lock()
	if c.logs != nil {
		c.logs = c.logs[:0]
	}

	c.head = 0
	c.slowestQueries = make(slowestQueryHeap, 0, slowestQueriesCapacity)
	heap.Init(&c.slowestQueries)
	c.domainCounts = make(map[string]int)
	c.clientCounts = make(map[string]int)
	c.domainSetCounts = make(map[string]int)
	c.totalQueryCount = 0
	c.totalQueryDuration = 0.0
	c.syncGlobalTotalsLocked()
	c.mu.Unlock()

	if clearDisk {
		for _, storage := range c.writeStorages() {
			if err := storage.Clear(); err != nil {
				mlog.L().Warn("failed to clear persisted audit logs", zap.String("storage", storage.Name()), zap.Error(err))
			}
		}
	}
}

func (c *AuditCollector) GetCapacity() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.capacity
}

func (c *AuditCollector) GetSettings() AuditSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings
}

func (c *AuditCollector) GetCurrentMemoryEntries() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.logs)
}

func (c *AuditCollector) SetSettings(next AuditSettings, configBaseDir string) error {
	next = normalizeAuditSettings(next)
	if configBaseDir == "" {
		configBaseDir = c.configBaseDir
	}

	c.mu.Lock()
	logs := c.snapshotChronologicalLocked()
	if len(logs) > next.MemoryEntries {
		logs = append([]AuditLog(nil), logs[len(logs)-next.MemoryEntries:]...)
	}

	c.capacity = next.MemoryEntries
	c.logs = make([]AuditLog, len(logs), next.MemoryEntries)
	copy(c.logs, logs)
	c.head = 0
	c.settings = next
	c.configBaseDir = configBaseDir
	c.logDir = filepath.Join(configBaseDir, auditLogsDirname)
	c.rebuildDerivedLocked()
	c.mu.Unlock()

	if err := saveAuditSettings(configBaseDir, next); err != nil {
		return err
	}
	if err := c.configureStorages(); err != nil {
		return err
	}
	if err := c.maybeEnforceDiskRetention(); err != nil {
		return err
	}
	return nil
}

type V2GetLogsParams struct {
	Page        int
	Limit       int
	Domain      string
	AnswerIP    string
	AnswerCNAME string
	ClientIP    string
	Q           string
	Exact       bool
}

func (c *AuditCollector) getLogsSnapshot() []AuditLog {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snapshot := c.snapshotChronologicalLocked()

	for i, j := 0, len(snapshot)-1; i < j; i, j = i+1, j-1 {
		snapshot[i], snapshot[j] = snapshot[j], snapshot[i]
	}
	return snapshot
}

func (c *AuditCollector) CalculateV2Stats() V2StatsResponse {
	if storage := c.readSQLiteStorage(); storage != nil {
		stats, err := storage.QueryStats()
		if err == nil {
			return stats
		}
		mlog.L().Warn("failed to query audit stats from sqlite, falling back to in-memory counters", zap.Error(err))
	}
	totalQueries := c.totalQueryCountGlobal.Load()
	totalDurationMicros := c.totalQueryDurationGlobal.Load()
	if totalQueries == 0 {
		return V2StatsResponse{}
	}
	return V2StatsResponse{
		TotalQueries:      totalQueries,
		AverageDurationMs: float64(totalDurationMicros) / float64(totalQueries) / 1000.0,
	}
}

type rankHeap []V2RankItem

func (h rankHeap) Len() int           { return len(h) }
func (h rankHeap) Less(i, j int) bool { return h[i].Count < h[j].Count }
func (h rankHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *rankHeap) Push(x any)        { *h = append(*h, x.(V2RankItem)) }
func (h *rankHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

func (c *AuditCollector) getRankFromMap(sourceMap map[string]int, limit int) []V2RankItem {
	if len(sourceMap) == 0 {
		return []V2RankItem{}
	}

	if len(sourceMap) <= limit {
		res := make([]V2RankItem, 0, len(sourceMap))
		for k, v := range sourceMap {
			res = append(res, V2RankItem{Key: k, Count: v})
		}
		sort.Slice(res, func(i, j int) bool {
			return res[i].Count > res[j].Count
		})
		return res
	}

	h := &rankHeap{}
	heap.Init(h)

	for key, count := range sourceMap {
		if h.Len() < limit {
			heap.Push(h, V2RankItem{Key: key, Count: count})
		} else if count > (*h)[0].Count {
			heap.Pop(h)
			heap.Push(h, V2RankItem{Key: key, Count: count})
		}
	}

	result := make([]V2RankItem, h.Len())
	for i := h.Len() - 1; i >= 0; i-- {
		result[i] = heap.Pop(h).(V2RankItem)
	}

	return result
}

type RankType string

const (
	RankByDomain    RankType = "domain"
	RankByClient    RankType = "client"
	RankByDomainSet RankType = "domain_set"
)

func (c *AuditCollector) CalculateRank(rankType RankType, limit int) []V2RankItem {
	if storage := c.readSQLiteStorage(); storage != nil {
		rank, err := storage.QueryRank(rankType, limit)
		if err == nil {
			return rank
		}
		mlog.L().Warn("failed to query audit rank from sqlite, falling back to in-memory counters", zap.String("rank_type", string(rankType)), zap.Error(err))
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	switch rankType {
	case RankByDomain:
		return c.getRankFromMap(c.domainCounts, limit)
	case RankByClient:
		return c.getRankFromMap(c.clientCounts, limit)
	case RankByDomainSet:
		return c.getRankFromMap(c.domainSetCounts, limit)
	default:
		return []V2RankItem{}
	}
}

func (c *AuditCollector) GetSlowestQueries(limit int) []AuditLog {
	if storage := c.readSQLiteStorage(); storage != nil {
		logs, err := storage.QuerySlowest(limit)
		if err == nil {
			return logs
		}
		mlog.L().Warn("failed to query slowest audit logs from sqlite, falling back to in-memory heap", zap.Error(err))
	}
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.slowestQueries.Len() == 0 {
		return []AuditLog{}
	}

	snapshot := make([]AuditLog, c.slowestQueries.Len())
	copy(snapshot, c.slowestQueries)

	sort.Slice(snapshot, func(i, j int) bool {
		return snapshot[i].DurationMs > snapshot[j].DurationMs
	})

	if len(snapshot) > limit {
		return snapshot[:limit]
	}
	return snapshot
}

func (c *AuditCollector) GetV2Logs(params V2GetLogsParams) V2PaginatedLogsResponse {
	if c.shouldUseSQLiteReads(params) {
		if resp, err := c.sqliteStorage.QueryLogs(params); err == nil {
			return resp
		} else {
			mlog.L().Warn("failed to query audit logs from sqlite, falling back to memory", zap.Error(err))
		}
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	totalLogs := len(c.logs)
	if totalLogs == 0 || c.capacity == 0 {
		return V2PaginatedLogsResponse{
			Pagination: V2PaginationInfo{CurrentPage: params.Page, ItemsPerPage: params.Limit},
			Logs:       []AuditLog{},
		}
	}

	if params.Page < 1 {
		params.Page = 1
	}
	if params.Limit <= 0 {
		params.Limit = 50
	}

	searchTerm := params.Q
	if params.Q != "" && !params.Exact {
		searchTerm = strings.ToLower(searchTerm)
	}

	matchCount := 0
	offset := (params.Page - 1) * params.Limit
	filteredLogs := make([]AuditLog, 0, params.Limit)

	curr := (c.head - 1 + totalLogs) % totalLogs

	for i := 0; i < totalLogs; i++ {
		log := c.logs[curr]
		isMatched := true

		if params.Q != "" {
			foundInQ := false
			matchFunc := strings.Contains
			if params.Exact {
				matchFunc = func(s, substr string) bool { return s == substr }
			}

			// 1. Check QueryName
			haystack := log.QueryName
			if !params.Exact {
				haystack = strings.ToLower(haystack)
			}
			if matchFunc(haystack, searchTerm) {
				foundInQ = true
			}

			// 2. Check ClientIP
			if !foundInQ {
				haystack = log.ClientIP
				if !params.Exact {
					haystack = strings.ToLower(haystack)
				}
				if matchFunc(haystack, searchTerm) {
					foundInQ = true
				}
			}

			// 3. Check TraceID
			if !foundInQ {
				haystack = log.TraceID
				if !params.Exact {
					haystack = strings.ToLower(haystack)
				}
				if matchFunc(haystack, searchTerm) {
					foundInQ = true
				}
			}

			// 4. Check DomainSet
			if !foundInQ && log.DomainSet != "" {
				haystack = log.DomainSet
				if !params.Exact {
					haystack = strings.ToLower(haystack)
				}
				if matchFunc(haystack, searchTerm) {
					foundInQ = true
				}
			}

			// 5. Check Answers
			if !foundInQ {
				for _, answer := range log.Answers {
					haystack = answer.Data
					if !params.Exact {
						haystack = strings.ToLower(haystack)
					}
					if matchFunc(haystack, searchTerm) {
						foundInQ = true
						break
					}
				}
			}
			if !foundInQ {
				isMatched = false
			}
		}

		if isMatched && params.ClientIP != "" && log.ClientIP != params.ClientIP {
			isMatched = false
		}
		if isMatched && params.Domain != "" && !strings.Contains(log.QueryName, params.Domain) {
			isMatched = false
		}
		if isMatched && params.AnswerIP != "" {
			found := false
			for _, answer := range log.Answers {
				if (answer.Type == "A" || answer.Type == "AAAA") && answer.Data == params.AnswerIP {
					found = true
					break
				}
			}
			if !found {
				isMatched = false
			}
		}
		if isMatched && params.AnswerCNAME != "" {
			found := false
			for _, answer := range log.Answers {
				if answer.Type == "CNAME" && strings.Contains(answer.Data, params.AnswerCNAME) {
					found = true
					break
				}
			}
			if !found {
				isMatched = false
			}
		}

		if isMatched {
			if matchCount >= offset && len(filteredLogs) < params.Limit {
				filteredLogs = append(filteredLogs, log)
			}
			matchCount++
		}

		curr = (curr - 1 + totalLogs) % totalLogs
	}

	totalPages := int(math.Ceil(float64(matchCount) / float64(params.Limit)))
	return V2PaginatedLogsResponse{
		Pagination: V2PaginationInfo{
			TotalItems:   matchCount,
			TotalPages:   totalPages,
			CurrentPage:  params.Page,
			ItemsPerPage: params.Limit,
		},
		Logs: filteredLogs,
	}
}

func (c *AuditCollector) configureStorages() error {
	c.mu.RLock()
	settings := c.settings
	configBaseDir := c.configBaseDir
	logDir := c.logDir
	oldNDJSON := c.ndjsonStorage
	oldSQLite := c.sqliteStorage
	c.mu.RUnlock()

	if configBaseDir == "" && settings.SQLitePath == "" {
		c.mu.Lock()
		c.ndjsonStorage = nil
		c.sqliteStorage = nil
		c.mu.Unlock()
		if oldNDJSON != nil {
			_ = oldNDJSON.Close()
		}
		if oldSQLite != nil {
			_ = oldSQLite.Close()
		}
		return nil
	}

	var newNDJSON *NdjsonAuditStorage
	if settings.StorageEngine != "sqlite" || settings.DualWrite {
		newNDJSON = newNdjsonAuditStorage(logDir)
		if err := newNDJSON.Open(); err != nil {
			return fmt.Errorf("open ndjson audit storage: %w", err)
		}
	}

	var newSQLite *SQLiteAuditStorage
	if settings.StorageEngine == "sqlite" || settings.DualWrite {
		sqlitePath := resolveAuditSQLitePath(configBaseDir, settings.SQLitePath)
		newSQLite = newSQLiteAuditStorage(sqlitePath, settings.MaxDBSizeMB)
		if err := newSQLite.Open(); err != nil {
			if newNDJSON != nil {
				_ = newNDJSON.Close()
			}
			return fmt.Errorf("open sqlite audit storage: %w", err)
		}
	}

	c.mu.Lock()
	c.ndjsonStorage = newNDJSON
	c.sqliteStorage = newSQLite
	if settings.SQLitePath == "" {
		c.settings.SQLitePath = defaultAuditSQLitePath(configBaseDir)
	}
	c.mu.Unlock()

	if oldNDJSON != nil {
		_ = oldNDJSON.Close()
	}
	if oldSQLite != nil {
		_ = oldSQLite.Close()
	}
	return nil
}

func (c *AuditCollector) primaryStorage() auditStorage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.settings.StorageEngine == "sqlite" && c.sqliteStorage != nil {
		return c.sqliteStorage
	}
	if c.ndjsonStorage != nil {
		return c.ndjsonStorage
	}
	return c.sqliteStorage
}

func (c *AuditCollector) writeStorages() []auditStorage {
	c.mu.RLock()
	defer c.mu.RUnlock()

	storages := make([]auditStorage, 0, 2)
	addStorage := func(s auditStorage) {
		if s == nil {
			return
		}
		for _, existing := range storages {
			if existing == s {
				return
			}
		}
		storages = append(storages, s)
	}

	switch c.settings.StorageEngine {
	case "sqlite":
		addStorage(c.sqliteStorage)
	default:
		addStorage(c.ndjsonStorage)
	}
	if c.settings.DualWrite {
		addStorage(c.ndjsonStorage)
		addStorage(c.sqliteStorage)
	}
	return storages
}

func (c *AuditCollector) shouldUseSQLiteReads(params V2GetLogsParams) bool {
	c.mu.RLock()
	sqliteEnabled := c.sqliteStorage != nil && (c.settings.StorageEngine == "sqlite" || c.settings.DualWrite)
	c.mu.RUnlock()
	return sqliteEnabled
}

func (c *AuditCollector) readSQLiteStorage() *SQLiteAuditStorage {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.sqliteStorage == nil {
		return nil
	}
	if c.settings.StorageEngine != "sqlite" && !c.settings.DualWrite {
		return nil
	}
	return c.sqliteStorage
}

func decrementCountMap(counter map[string]int, key string) {
	if counter[key] <= 1 {
		delete(counter, key)
		return
	}
	counter[key]--
}

func durationMicrosFromMilliseconds(durationMs float64) uint64 {
	if durationMs <= 0 {
		return 0
	}
	return uint64(durationMs * 1000)
}

func (c *AuditCollector) appendBatchToDisk(logs []AuditLog) error {
	for _, storage := range c.writeStorages() {
		if err := storage.WriteBatch(logs); err != nil {
			return fmt.Errorf("write audit batch to %s: %w", storage.Name(), err)
		}
	}
	return nil
}

func (c *AuditCollector) enforceDiskRetention() error {
	for _, storage := range c.writeStorages() {
		if err := storage.EnforceRetention(c.GetSettings()); err != nil {
			return err
		}
	}
	return nil
}

func (c *AuditCollector) listAuditLogFiles() ([]auditLogFileInfo, error) {
	c.mu.RLock()
	ndjsonStorage := c.ndjsonStorage
	c.mu.RUnlock()
	if ndjsonStorage == nil {
		return nil, nil
	}
	return ndjsonStorage.listAuditLogFiles()
}
