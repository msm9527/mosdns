package coremain

import (
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

const (
	auditQueueCapacityFactor = 32
	auditMinQueueCapacity    = 512
	auditMaxQueueCapacity    = 32768
	auditQueueMaxShards      = 8
	auditQueueMinShardCap    = 64
)

type AuditCollector struct {
	mu            sync.RWMutex
	clearMu       sync.RWMutex
	storageMu     sync.RWMutex
	settings      AuditSettings
	configBaseDir string
	storage       *SQLiteAuditStorage
	realtime      *auditRealtimeStore
	queues        []chan auditQueuedLog
	workerDone    []chan struct{}
	maintDone     chan struct{}
	generation    atomic.Uint64
	enabled       atomic.Bool
	closed        atomic.Bool
	degraded      atomic.Bool
	opening       atomic.Bool
}

var GlobalAuditCollector = NewAuditCollector(defaultAuditSettings(), "")

func InitializeAuditCollector(configBaseDir string, base *AuditSettings) {
	settings := loadAuditSettings(configBaseDir, base)
	GlobalAuditCollector = NewAuditCollector(settings, configBaseDir)
}

func NewAuditCollector(settings AuditSettings, configBaseDir string) *AuditCollector {
	settings = normalizeAuditSettings(settings)
	queues := newAuditQueues(settings)
	collector := &AuditCollector{
		settings:      settings,
		configBaseDir: configBaseDir,
		realtime:      newAuditRealtimeStore(auditRealtimeBucketCount),
		queues:        queues,
		workerDone:    make([]chan struct{}, len(queues)),
		maintDone:     make(chan struct{}),
	}
	for i := range collector.workerDone {
		collector.workerDone[i] = make(chan struct{})
	}
	collector.enabled.Store(settings.Enabled)
	return collector
}

func (c *AuditCollector) StartWorker() {
	c.OpenStorageAsync()
	for i := range c.queues {
		go c.runWriter(c.queues[i], c.workerDone[i])
	}
	go c.runMaintenance()
}

func (c *AuditCollector) StopWorker() {
	if c.closed.CompareAndSwap(false, true) {
		for _, queue := range c.queues {
			close(queue)
		}
	}
	for _, done := range c.workerDone {
		<-done
	}
	<-c.maintDone
	c.closeStorage()
}

func (c *AuditCollector) Collect(qCtx *query_context.Context) {
	if !c.IsCapturing() || qCtx == nil {
		return
	}
	log := buildAuditLog(qCtx, time.Since(qCtx.StartTime()))
	c.CollectLog(log)
}

// CollectLog records an already-built audit log into realtime and persistent audit stores.
func (c *AuditCollector) CollectLog(log AuditLog) {
	c.CollectLogWithShard(log, auditLogShardKey(log))
}

// CollectLogWithShard records an already-built audit log and uses shardKey to
// distribute hot UDP cache-hit audit events across collector queues.
func (c *AuditCollector) CollectLogWithShard(log AuditLog, shardKey uint64) {
	if c == nil || c.closed.Load() || !c.enabled.Load() {
		return
	}
	generation := c.generation.Load()
	queue := c.queueForShard(shardKey)
	if queue == nil {
		return
	}
	select {
	case queue <- auditQueuedLog{generation: generation, log: log}:
	default:
		c.degraded.Store(true)
		at := log.QueryTime
		if at.IsZero() {
			at = nowTime()
		}
		select {
		case queue <- auditQueuedLog{generation: generation, dropped: true, at: at}:
		default:
		}
	}
}

func normalizeAuditLog(log *AuditLog) {
	if log == nil {
		return
	}
	if log.QueryTime.IsZero() {
		log.QueryTime = nowTime()
	}
	if log.DomainSetRaw == "" {
		log.DomainSetRaw = "unmatched_rule"
	}
	if log.DomainSetNorm == "" {
		log.DomainSetNorm = normalizeAuditDomainSet(log.DomainSetRaw, log.QueryType)
	}
}

func (c *AuditCollector) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.Enabled = true
	c.enabled.Store(true)
}

func (c *AuditCollector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.Enabled = false
	c.enabled.Store(false)
}

func (c *AuditCollector) IsCapturing() bool {
	return c != nil && c.enabled.Load()
}

func (c *AuditCollector) GetSettings() AuditSettings {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings
}

func (c *AuditCollector) SetSettings(next AuditSettings, configBaseDir string) error {
	next = normalizeAuditSettings(next)
	if configBaseDir == "" {
		configBaseDir = c.configBaseDir
	}
	storage, err := openAuditStorage(next, configBaseDir)
	if err != nil {
		return err
	}
	c.storageMu.Lock()
	defer c.storageMu.Unlock()
	c.mu.Lock()
	oldStorage := c.storage
	c.settings = next
	c.configBaseDir = configBaseDir
	c.storage = storage
	c.enabled.Store(next.Enabled)
	c.mu.Unlock()
	c.degraded.Store(false)
	closeAuditStorageAfterSwap(oldStorage)
	return nil
}

func (c *AuditCollector) reopenStorage(settings AuditSettings, configBaseDir string) error {
	storage, err := openAuditStorage(settings, configBaseDir)
	if err != nil {
		return err
	}
	c.storageMu.Lock()
	defer c.storageMu.Unlock()
	c.mu.Lock()
	oldStorage := c.storage
	c.storage = storage
	c.enabled.Store(settings.Enabled)
	c.mu.Unlock()
	c.degraded.Store(false)
	closeAuditStorageAfterSwap(oldStorage)
	return nil
}

func (c *AuditCollector) OpenStorageAsync() {
	if c == nil || c.closed.Load() || !c.opening.CompareAndSwap(false, true) {
		return
	}
	c.degraded.Store(true)
	go func() {
		defer c.opening.Store(false)
		settings := c.GetSettings()
		c.mu.RLock()
		configBaseDir := c.configBaseDir
		c.mu.RUnlock()
		storage, err := openAuditStorage(settings, configBaseDir)
		if err != nil {
			c.degraded.Store(true)
			mlog.L().Warn("failed to open audit storage", zap.Error(err))
			return
		}
		if !c.installStorageIfCurrent(settings, configBaseDir, storage) {
			_ = storage.Close()
			return
		}
		mlog.L().Info("audit storage opened")
	}()
}

var openAuditStorage = func(settings AuditSettings, configBaseDir string) (*SQLiteAuditStorage, error) {
	path := resolveAuditSQLitePath(configBaseDir, settings.SQLitePath)
	storage := newSQLiteAuditStorage(path)
	if err := storage.Open(); err != nil {
		return nil, fmt.Errorf("open sqlite audit storage: %w", err)
	}
	return storage, nil
}

func (c *AuditCollector) installStorageIfCurrent(settings AuditSettings, configBaseDir string, storage *SQLiteAuditStorage) bool {
	c.storageMu.Lock()
	defer c.storageMu.Unlock()
	c.mu.Lock()
	if c.closed.Load() || c.configBaseDir != configBaseDir || c.settings != settings {
		c.mu.Unlock()
		return false
	}
	oldStorage := c.storage
	c.storage = storage
	c.mu.Unlock()
	c.degraded.Store(false)
	closeAuditStorageAfterSwap(oldStorage)
	return true
}

func closeAuditStorageAfterSwap(storage *SQLiteAuditStorage) {
	if storage == nil {
		return
	}
	// Grace period: allow in-flight workers that captured oldStorage
	// before the swap to finish their current operation.
	time.Sleep(200 * time.Millisecond)
	_ = storage.Close()
}

func (c *AuditCollector) closeStorage() {
	c.storageMu.Lock()
	defer c.storageMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.storage != nil {
		_ = c.storage.Close()
		c.storage = nil
	}
}

func buildAuditLog(qCtx *query_context.Context, duration time.Duration) AuditLog {
	question := qCtx.QQuestion()
	queryName := strings.TrimSuffix(question.Name, ".")
	clientAddr := qCtx.ServerMeta.ClientAddr.String()
	if host, _, err := net.SplitHostPort(clientAddr); err == nil {
		clientAddr = host
	}
	log := AuditLog{
		QueryTime:    qCtx.StartTime(),
		ClientIP:     clientAddr,
		QueryType:    dns.TypeToString[question.Qtype],
		QueryName:    queryName,
		QueryClass:   dns.ClassToString[question.Qclass],
		DurationMs:   auditDurationMs(duration),
		TraceID:      qCtx.TraceID,
		DomainSetRaw: getAuditDomainSet(qCtx),
		UpstreamTag:  getAuditUpstreamTag(qCtx),
		Transport:    auditTransport(qCtx),
		ServerName:   qCtx.ServerMeta.ServerName,
		URLPath:      qCtx.ServerMeta.UrlPath,
		CacheStatus:  getAuditCacheStatus(qCtx),
	}
	log.DomainSetNorm = normalizeAuditDomainSet(log.DomainSetRaw, log.QueryType)
	populateAuditResponse(&log, finalAuditResponse(qCtx))
	return log
}

func auditDurationMs(duration time.Duration) float64 {
	return float64(duration.Nanoseconds()) / float64(time.Millisecond)
}

func finalAuditResponse(qCtx *query_context.Context) *dns.Msg {
	if qCtx == nil {
		return nil
	}
	if payload := qCtx.ResponsePayload(); payload != nil {
		if len(payload.Wire) > 0 {
			resp := new(dns.Msg)
			if err := resp.Unpack(payload.Wire); err == nil {
				return resp
			}
		}
		if payload.Msg != nil {
			return payload.Msg
		}
	}
	return qCtx.R()
}

func getAuditDomainSet(qCtx *query_context.Context) string {
	value, ok := qCtx.GetValue(query_context.KeyDomainSet)
	if !ok {
		return "unmatched_rule"
	}
	domainSet, _ := value.(string)
	if domainSet == "" {
		return "unmatched_rule"
	}
	return domainSet
}

func auditTransport(qCtx *query_context.Context) string {
	if qCtx == nil {
		return ""
	}
	if qCtx.ServerMeta.UrlPath != "" {
		return "http"
	}
	if qCtx.ServerMeta.FromUDP {
		return "udp"
	}
	return "stream"
}

func populateAuditResponse(log *AuditLog, resp *dns.Msg) {
	if log == nil {
		return
	}
	if resp == nil {
		log.ResponseCode = "NO_RESPONSE"
		return
	}
	log.ResponseCode = dns.RcodeToString[resp.Rcode]
	log.ResponseFlags = ResponseFlags{
		AA: resp.Authoritative,
		TC: resp.Truncated,
		RA: resp.RecursionAvailable,
	}
	if len(resp.Answer) == 0 {
		return
	}
	log.Answers = make([]AnswerDetail, 0, len(resp.Answer))
	for _, answer := range resp.Answer {
		detail := answerDetail(answer)
		log.Answers = append(log.Answers, detail)
	}
	log.AnswerCount = len(log.Answers)
}

func answerDetail(answer dns.RR) AnswerDetail {
	header := answer.Header()
	detail := AnswerDetail{Type: dns.TypeToString[header.Rrtype], TTL: header.Ttl}
	switch record := answer.(type) {
	case *dns.A:
		detail.Data = record.A.String()
	case *dns.AAAA:
		detail.Data = record.AAAA.String()
	case *dns.CNAME:
		detail.Data = record.Target
	case *dns.PTR:
		detail.Data = record.Ptr
	case *dns.NS:
		detail.Data = record.Ns
	case *dns.MX:
		detail.Data = record.Mx
	case *dns.TXT:
		detail.Data = strings.Join(record.Txt, " ")
	default:
		detail.Data = answer.String()
	}
	return detail
}

var nowTime = time.Now

func auditQueueCapacity(settings AuditSettings) int {
	size := settings.FlushBatchSize * auditQueueCapacityFactor
	if size < auditMinQueueCapacity {
		return auditMinQueueCapacity
	}
	if size > auditMaxQueueCapacity {
		return auditMaxQueueCapacity
	}
	return size
}

func newAuditQueues(settings AuditSettings) []chan auditQueuedLog {
	shards := runtime.GOMAXPROCS(0)
	if shards < 1 {
		shards = 1
	}
	if shards > auditQueueMaxShards {
		shards = auditQueueMaxShards
	}
	total := auditQueueCapacity(settings)
	perShard := (total + shards - 1) / shards
	if perShard < auditQueueMinShardCap {
		perShard = auditQueueMinShardCap
	}
	queues := make([]chan auditQueuedLog, shards)
	for i := range queues {
		queues[i] = make(chan auditQueuedLog, perShard)
	}
	return queues
}

func (c *AuditCollector) queueForShard(shardKey uint64) chan auditQueuedLog {
	if c == nil || len(c.queues) == 0 {
		return nil
	}
	return c.queues[shardKey%uint64(len(c.queues))]
}

func (c *AuditCollector) queueDepth() int {
	if c == nil {
		return 0
	}
	total := 0
	for _, queue := range c.queues {
		total += len(queue)
	}
	return total
}

func auditLogShardKey(log AuditLog) uint64 {
	hash := uint64(1469598103934665603)
	for i := 0; i < len(log.ClientIP); i++ {
		hash ^= uint64(log.ClientIP[i])
		hash *= 1099511628211
	}
	for i := 0; i < len(log.QueryName); i++ {
		hash ^= uint64(log.QueryName[i])
		hash *= 1099511628211
	}
	return hash
}
