package coremain

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSQLiteAuditStorageWriteLoadAndQuery(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"), 16)
	if err := storage.Open(); err != nil {
		t.Fatalf("open sqlite storage: %v", err)
	}
	defer func() { _ = storage.Close() }()

	now := time.Now().Truncate(time.Millisecond)
	logs := []AuditLog{
		{
			ClientIP:     "127.0.0.1",
			QueryType:    "A",
			QueryName:    "one.example",
			QueryClass:   "IN",
			QueryTime:    now.Add(-2 * time.Minute),
			DurationMs:   1.2,
			TraceID:      "trace-1",
			ResponseCode: "NOERROR",
			DomainSet:    "domestic",
			Answers: []AnswerDetail{
				{Type: "A", Data: "1.1.1.1"},
				{Type: "CNAME", Data: "alias.one.example"},
			},
		},
		{
			ClientIP:     "127.0.0.2",
			QueryType:    "AAAA",
			QueryName:    "two.example",
			QueryClass:   "IN",
			QueryTime:    now.Add(-1 * time.Minute),
			DurationMs:   2.4,
			TraceID:      "trace-2",
			ResponseCode: "NOERROR",
			DomainSet:    "foreign",
			Answers: []AnswerDetail{
				{Type: "AAAA", Data: "2400::1"},
			},
		},
	}

	if err := storage.WriteBatch(logs); err != nil {
		t.Fatalf("write sqlite batch: %v", err)
	}

	recent, err := storage.LoadRecent(2)
	if err != nil {
		t.Fatalf("load recent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("expected 2 recent logs, got %d", len(recent))
	}
	if recent[0].QueryName != "one.example" || recent[1].QueryName != "two.example" {
		t.Fatalf("unexpected recent logs: %#v", recent)
	}

	resp, err := storage.QueryLogs(V2GetLogsParams{
		Page:     1,
		Limit:    10,
		ClientIP: "127.0.0.2",
	})
	if err != nil {
		t.Fatalf("query by client ip: %v", err)
	}
	if resp.Pagination.TotalItems != 1 || len(resp.Logs) != 1 {
		t.Fatalf("unexpected query pagination: %#v", resp.Pagination)
	}
	if resp.Logs[0].QueryName != "two.example" {
		t.Fatalf("unexpected query result: %#v", resp.Logs[0])
	}

	resp, err = storage.QueryLogs(V2GetLogsParams{
		Page:     1,
		Limit:    10,
		AnswerIP: "1.1.1.1",
	})
	if err != nil {
		t.Fatalf("query by answer ip: %v", err)
	}
	if resp.Pagination.TotalItems != 1 || resp.Logs[0].QueryName != "one.example" {
		t.Fatalf("unexpected answer ip query result: %#v", resp)
	}

	resp, err = storage.QueryLogs(V2GetLogsParams{
		Page:  1,
		Limit: 10,
		Q:     "alias.one.example",
		Exact: true,
	})
	if err != nil {
		t.Fatalf("exact query by answer: %v", err)
	}
	if resp.Pagination.TotalItems != 1 || resp.Logs[0].QueryName != "one.example" {
		t.Fatalf("unexpected exact answer query result: %#v", resp)
	}

	stats, err := storage.QueryStats()
	if err != nil {
		t.Fatalf("query stats: %v", err)
	}
	if stats.TotalQueries != 2 {
		t.Fatalf("stats total queries = %d", stats.TotalQueries)
	}
	if diff := stats.AverageDurationMs - 1.8; diff < -0.0001 || diff > 0.0001 {
		t.Fatalf("stats average duration = %.2f", stats.AverageDurationMs)
	}

	rank, err := storage.QueryRank(RankByDomain, 2)
	if err != nil {
		t.Fatalf("query rank: %v", err)
	}
	if len(rank) != 2 || rank[0].Key != "one.example" || rank[1].Key != "two.example" {
		t.Fatalf("unexpected rank result: %#v", rank)
	}

	slowest, err := storage.QuerySlowest(1)
	if err != nil {
		t.Fatalf("query slowest: %v", err)
	}
	if len(slowest) != 1 || slowest[0].QueryName != "two.example" {
		t.Fatalf("unexpected slowest result: %#v", slowest)
	}
}

func TestSQLiteAuditStorageQueryRankByDomainSetNormalizes(t *testing.T) {
	storage := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"), 16)
	if err := storage.Open(); err != nil {
		t.Fatalf("open sqlite storage: %v", err)
	}
	defer func() { _ = storage.Close() }()

	now := time.Now().Truncate(time.Millisecond)
	logs := []AuditLog{
		{
			ClientIP:     "127.0.0.1",
			QueryType:    "A",
			QueryName:    "one.example",
			QueryClass:   "IN",
			QueryTime:    now.Add(-4 * time.Minute),
			DurationMs:   1,
			TraceID:      "trace-1",
			ResponseCode: "NOERROR",
			DomainSet:    "记忆代理|记忆无V6|订阅代理",
		},
		{
			ClientIP:     "127.0.0.2",
			QueryType:    "A",
			QueryName:    "two.example",
			QueryClass:   "IN",
			QueryTime:    now.Add(-3 * time.Minute),
			DurationMs:   1,
			TraceID:      "trace-2",
			ResponseCode: "NOERROR",
			DomainSet:    "记忆代理|订阅代理",
		},
		{
			ClientIP:     "127.0.0.3",
			QueryType:    "AAAA",
			QueryName:    "three.example",
			QueryClass:   "IN",
			QueryTime:    now.Add(-2 * time.Minute),
			DurationMs:   1,
			TraceID:      "trace-3",
			ResponseCode: "NOERROR",
			DomainSet:    "记忆无V6|记忆直连|订阅直连",
		},
		{
			ClientIP:     "127.0.0.4",
			QueryType:    "A",
			QueryName:    "four.example",
			QueryClass:   "IN",
			QueryTime:    now.Add(-1 * time.Minute),
			DurationMs:   1,
			TraceID:      "trace-4",
			ResponseCode: "NOERROR",
			DomainSet:    "白名单|订阅代理",
		},
	}

	if err := storage.WriteBatch(logs); err != nil {
		t.Fatalf("write sqlite batch: %v", err)
	}

	rank, err := storage.QueryRank(RankByDomainSet, 10)
	if err != nil {
		t.Fatalf("query normalized domain_set rank: %v", err)
	}

	got := make(map[string]int, len(rank))
	for _, item := range rank {
		got[item.Key] = item.Count
	}

	if got["记忆代理"] != 2 {
		t.Fatalf("记忆代理 count = %d, want 2", got["记忆代理"])
	}
	if got["记忆无V6"] != 1 {
		t.Fatalf("记忆无V6 count = %d, want 1", got["记忆无V6"])
	}
	if got["白名单"] != 1 {
		t.Fatalf("白名单 count = %d, want 1", got["白名单"])
	}
}
