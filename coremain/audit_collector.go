package coremain

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
	"go.uber.org/zap"
)

const auditChannelCapacity = 16384

type AuditCollector struct {
	mu            sync.RWMutex
	settings      AuditSettings
	configBaseDir string
	storage       *SQLiteAuditStorage
	realtime      *auditRealtimeStore
	queue         chan AuditLog
	workerDone    chan struct{}
	maintDone     chan struct{}
	closed        atomic.Bool
	degraded      atomic.Bool
}

var GlobalAuditCollector = NewAuditCollector(defaultAuditSettings(), "")

func InitializeAuditCollector(configBaseDir string, base *AuditSettings) {
	settings := loadAuditSettings(configBaseDir, base)
	GlobalAuditCollector = NewAuditCollector(settings, configBaseDir)
}

func NewAuditCollector(settings AuditSettings, configBaseDir string) *AuditCollector {
	settings = normalizeAuditSettings(settings)
	collector := &AuditCollector{
		settings:      settings,
		configBaseDir: configBaseDir,
		realtime:      newAuditRealtimeStore(auditRealtimeBucketCount),
		queue:         make(chan AuditLog, auditChannelCapacity),
		workerDone:    make(chan struct{}),
		maintDone:     make(chan struct{}),
	}
	if err := collector.reopenStorage(settings, configBaseDir); err != nil {
		collector.degraded.Store(true)
		mlog.L().Warn("failed to open audit storage", zap.Error(err))
	}
	return collector
}

func (c *AuditCollector) StartWorker() {
	go c.runWriter()
	go c.runMaintenance()
}

func (c *AuditCollector) StopWorker() {
	if c.closed.CompareAndSwap(false, true) {
		close(c.queue)
	}
	<-c.workerDone
	<-c.maintDone
	c.closeStorage()
}

func (c *AuditCollector) Collect(qCtx *query_context.Context) {
	if !c.IsCapturing() || qCtx == nil {
		return
	}
	log := buildAuditLog(qCtx, time.Since(qCtx.StartTime()))
	c.realtime.Record(log)
	select {
	case c.queue <- log:
	default:
		c.degraded.Store(true)
		c.realtime.RecordDrop(log.QueryTime)
	}
}

func (c *AuditCollector) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.Enabled = true
}

func (c *AuditCollector) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.settings.Enabled = false
}

func (c *AuditCollector) IsCapturing() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.settings.Enabled
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
	if err := c.reopenStorage(next, configBaseDir); err != nil {
		return err
	}
	c.mu.Lock()
	c.settings = next
	c.configBaseDir = configBaseDir
	c.mu.Unlock()
	return nil
}

func (c *AuditCollector) reopenStorage(settings AuditSettings, configBaseDir string) error {
	path := resolveAuditSQLitePath(configBaseDir, settings.SQLitePath)
	storage := newSQLiteAuditStorage(path)
	if err := storage.Open(); err != nil {
		return fmt.Errorf("open sqlite audit storage: %w", err)
	}
	c.mu.Lock()
	oldStorage := c.storage
	c.storage = storage
	c.mu.Unlock()
	if oldStorage != nil {
		_ = oldStorage.Close()
	}
	return nil
}

func (c *AuditCollector) closeStorage() {
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
		DurationMs:   float64(duration.Microseconds()) / 1000.0,
		TraceID:      qCtx.TraceID,
		DomainSetRaw: getAuditDomainSet(qCtx),
		UpstreamTag:  getAuditUpstreamTag(qCtx),
		Transport:    auditTransport(qCtx),
		ServerName:   qCtx.ServerMeta.ServerName,
		URLPath:      qCtx.ServerMeta.UrlPath,
		CacheStatus:  getAuditCacheStatus(qCtx),
	}
	log.DomainSetNorm = normalizeAuditDomainSet(log.DomainSetRaw, log.QueryType)
	populateAuditResponse(&log, qCtx.R())
	return log
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
