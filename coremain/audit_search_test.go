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

	t.Run("exact answer escapes like metacharacters", func(t *testing.T) {
		literal := logs[0]
		literal.QueryName = "literal-wildcard.example"
		literal.QueryTime = base.Add(4 * time.Minute)
		literal.Answers = []AnswerDetail{{Type: "TXT", TTL: 60, Data: `%_\\literal`}}
		literal.AnswerCount = 1
		if err := storage.WriteBatch([]AuditLog{literal}); err != nil {
			t.Fatal(err)
		}
		resp := mustQueryAuditLogs(t, storage, AuditLogsQuery{
			From: base,
			To:   base.Add(5 * time.Minute),
			Filters: AuditLogSearchFilters{
				Answer: AuditTextFilter{Value: `%_\\literal`, Mode: AuditMatchExact},
			},
		})
		if resp.Summary.MatchedCount != 1 || len(resp.Logs) != 1 || resp.Logs[0].QueryName != literal.QueryName {
			t.Fatalf("literal wildcard matched incorrectly: %+v", resp)
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

	t.Run("offset pagination", func(t *testing.T) {
		resp := mustQueryAuditLogs(t, storage, AuditLogsQuery{
			From:   base,
			To:     base.Add(3 * time.Minute),
			Limit:  1,
			Offset: 1,
		})
		if resp.Summary.MatchedCount != 4 {
			t.Fatalf("MatchedCount = %d, want 4", resp.Summary.MatchedCount)
		}
		if len(resp.Logs) != 1 || resp.Logs[0].QueryName != "alpha.example" {
			t.Fatalf("unexpected logs = %+v", resp.Logs)
		}
		if resp.NextCursor == "" {
			t.Fatal("NextCursor is empty")
		}
	})

	t.Run("duration sort", func(t *testing.T) {
		resp := mustQueryAuditLogs(t, storage, AuditLogsQuery{
			From:  base,
			To:    base.Add(3 * time.Minute),
			Limit: 2,
			Sort:  AuditLogSearchSort{Field: "duration", Order: "desc"},
		})
		if resp.Summary.MatchedCount != 4 {
			t.Fatalf("MatchedCount = %d, want 4", resp.Summary.MatchedCount)
		}
		if len(resp.Logs) != 2 {
			t.Fatalf("len(resp.Logs) = %d, want 2", len(resp.Logs))
		}
		if resp.Logs[0].QueryName != "alpha.example" || resp.Logs[1].QueryName != "two.example" {
			t.Fatalf("unexpected logs = %+v", resp.Logs)
		}
		if resp.NextCursor == "" {
			t.Fatal("duration sort did not return a cursor")
		}
		next := mustQueryAuditLogs(t, storage, AuditLogsQuery{
			From:   base,
			To:     base.Add(3 * time.Minute),
			Limit:  2,
			Cursor: resp.NextCursor,
			Sort:   AuditLogSearchSort{Field: "duration", Order: "desc"},
		})
		if len(next.Logs) != 2 || next.Logs[0].QueryName != "logs.internal" || next.Logs[1].QueryName != "one.example" || next.NextCursor != "" {
			t.Fatalf("duration cursor page = %+v", next)
		}
	})

	t.Run("duration ascending cursor", func(t *testing.T) {
		resp := mustQueryAuditLogs(t, storage, AuditLogsQuery{
			From:  base,
			To:    base.Add(3 * time.Minute),
			Limit: 2,
			Sort:  AuditLogSearchSort{Field: "duration", Order: "asc"},
		})
		if len(resp.Logs) != 2 || resp.Logs[0].QueryName != "one.example" || resp.Logs[1].QueryName != "logs.internal" || resp.NextCursor == "" {
			t.Fatalf("duration ascending first page = %+v", resp)
		}
		next := mustQueryAuditLogs(t, storage, AuditLogsQuery{
			From:   base,
			To:     base.Add(3 * time.Minute),
			Limit:  2,
			Cursor: resp.NextCursor,
			Sort:   AuditLogSearchSort{Field: "duration", Order: "asc"},
		})
		if len(next.Logs) != 2 || next.Logs[0].QueryName != "two.example" || next.Logs[1].QueryName != "alpha.example" || next.NextCursor != "" {
			t.Fatalf("duration ascending cursor page = %+v", next)
		}
	})
}

func mustQueryAuditLogs(t *testing.T, storage *SQLiteAuditStorage, query AuditLogsQuery) AuditLogsResponse {
	t.Helper()
	if query.Limit <= 0 {
		query.Limit = 20
	}
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
