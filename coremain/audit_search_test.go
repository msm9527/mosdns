package coremain

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAuditSearchSQLiteKeywordAndFilters(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := storage.Open(); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() { _ = storage.Close() }()

	base, logs := buildAuditSearchFixtures()
	if err := storage.WriteBatch(logs); err != nil {
		t.Fatalf("WriteBatch() error = %v", err)
	}

	t.Run("keyword fuzzy all fields", func(t *testing.T) {
		resp := mustQueryAuditLogs(t, storage, AuditLogsQuery{
			From: base,
			To:   base.Add(3 * time.Minute),
			Keyword: AuditLogKeywordSearch{
				Value:  "dns.google",
				Mode:   AuditMatchFuzzy,
				Fields: allAuditSearchFields(),
			},
		})
		if resp.Summary.MatchedCount != 2 {
			t.Fatalf("MatchedCount = %d, want 2", resp.Summary.MatchedCount)
		}
	})

	t.Run("exact answer and date range", func(t *testing.T) {
		hasAnswer := true
		resp := mustQueryAuditLogs(t, storage, AuditLogsQuery{
			From: base.Add(75 * time.Second),
			To:   base.Add(95 * time.Second),
			Filters: AuditLogSearchFilters{
				Answer:    AuditTextFilter{Value: "8.8.8.8", Mode: AuditMatchExact},
				HasAnswer: &hasAnswer,
			},
		})
		if resp.Summary.MatchedCount != 1 {
			t.Fatalf("MatchedCount = %d, want 1", resp.Summary.MatchedCount)
		}
		if len(resp.Logs) != 1 || resp.Logs[0].QueryName != "alpha.example" {
			t.Fatalf("unexpected logs = %+v", resp.Logs)
		}
	})

	t.Run("combined filters", func(t *testing.T) {
		maxDuration := 4.0
		resp := mustQueryAuditLogs(t, storage, AuditLogsQuery{
			From: base,
			To:   base.Add(3 * time.Minute),
			Filters: AuditLogSearchFilters{
				ClientIP:      AuditTextFilter{Value: "127.0.0.1", Mode: AuditMatchExact},
				ResponseCode:  "NOERROR",
				Transport:     "udp",
				DurationMsMax: &maxDuration,
			},
		})
		if resp.Summary.MatchedCount != 1 {
			t.Fatalf("MatchedCount = %d, want 1", resp.Summary.MatchedCount)
		}
		if len(resp.Logs) != 1 || resp.Logs[0].QueryName != "logs.internal" {
			t.Fatalf("unexpected logs = %+v", resp.Logs)
		}
	})
}

func mustQueryAuditLogs(t *testing.T, storage *SQLiteAuditStorage, query AuditLogsQuery) AuditLogsResponse {
	t.Helper()
	query.Limit = 20
	resp, err := storage.QueryLogs(query)
	if err != nil {
		t.Fatalf("QueryLogs() error = %v", err)
	}
	return resp
}

type auditSearchLogSpec struct {
	Name        string
	At          time.Time
	ClientIP    string
	QueryType   string
	DurationMs  float64
	TraceID     string
	Response    string
	Answers     []AnswerDetail
	DomainSet   string
	UpstreamTag string
	Transport   string
	ServerName  string
	URLPath     string
	CacheStatus string
}

func buildAuditSearchFixtures() (time.Time, []AuditLog) {
	base := time.Now().Add(-2 * time.Minute).Truncate(time.Second)
	logs := []AuditLog{
		newAuditSearchLog(auditSearchLogSpec{
			Name:        "one.example",
			At:          base.Add(10 * time.Second),
			ClientIP:    "127.0.0.1",
			QueryType:   "A",
			DurationMs:  2,
			TraceID:     "trace-one",
			Response:    "NOERROR",
			Answers:     []AnswerDetail{{Type: "A", TTL: 60, Data: "1.1.1.1"}},
			DomainSet:   "domestic",
			UpstreamTag: "ali-doh",
			Transport:   "https",
			ServerName:  "dns.alidns.com",
			URLPath:     "/dns-query",
			CacheStatus: AuditCacheHit,
		}),
		newAuditSearchLog(auditSearchLogSpec{
			Name:        "two.example",
			At:          base.Add(70 * time.Second),
			ClientIP:    "10.0.0.2",
			QueryType:   "AAAA",
			DurationMs:  5,
			TraceID:     "trace-two",
			Response:    "SERVFAIL",
			DomainSet:   "foreign",
			UpstreamTag: "google",
			Transport:   "https",
			ServerName:  "dns.google",
			URLPath:     "/dns-query",
			CacheStatus: AuditCacheMiss,
		}),
		newAuditSearchLog(auditSearchLogSpec{
			Name:       "alpha.example",
			At:         base.Add(80 * time.Second),
			ClientIP:   "10.0.0.3",
			QueryType:  "A",
			DurationMs: 8,
			TraceID:    "trace-alpha",
			Response:   "NOERROR",
			Answers: []AnswerDetail{
				{Type: "A", TTL: 60, Data: "8.8.8.8"},
				{Type: "CNAME", TTL: 60, Data: "edge.alpha.example"},
			},
			DomainSet:   "foreign",
			UpstreamTag: "google",
			Transport:   "https",
			ServerName:  "dns.google",
			URLPath:     "/resolve",
			CacheStatus: AuditCacheLazy,
		}),
		newAuditSearchLog(auditSearchLogSpec{
			Name:        "logs.internal",
			At:          base.Add(90 * time.Second),
			ClientIP:    "127.0.0.1",
			QueryType:   "TXT",
			DurationMs:  3,
			TraceID:     "trace-four",
			Response:    "NOERROR",
			Answers:     []AnswerDetail{{Type: "TXT", TTL: 30, Data: "ok"}},
			DomainSet:   "whitelist",
			UpstreamTag: "internal",
			Transport:   "udp",
			CacheStatus: AuditCacheHit,
		}),
	}
	return base, logs
}

func newAuditSearchLog(spec auditSearchLogSpec) AuditLog {
	return AuditLog{
		QueryTime:     spec.At,
		ClientIP:      spec.ClientIP,
		QueryType:     spec.QueryType,
		QueryName:     spec.Name,
		QueryClass:    "IN",
		DurationMs:    spec.DurationMs,
		TraceID:       spec.TraceID,
		ResponseCode:  spec.Response,
		AnswerCount:   len(spec.Answers),
		Answers:       spec.Answers,
		DomainSetRaw:  spec.DomainSet,
		DomainSetNorm: normalizeAuditDomainSet(spec.DomainSet, spec.QueryType),
		UpstreamTag:   spec.UpstreamTag,
		Transport:     spec.Transport,
		ServerName:    spec.ServerName,
		URLPath:       spec.URLPath,
		CacheStatus:   spec.CacheStatus,
	}
}
