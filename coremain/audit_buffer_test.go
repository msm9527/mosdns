package coremain

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func newAuditBufferTestStorage(t *testing.T) *SQLiteAuditStorage {
	t.Helper()
	s := newSQLiteAuditStorage(filepath.Join(t.TempDir(), "audit.db"))
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func auditBufferFixture() []AuditLog {
	base := time.Now().Truncate(time.Hour).Add(-48 * time.Hour)
	logs := make([]AuditLog, 18)
	for i := range logs {
		at := base.Add(time.Duration(i/3) * 12 * time.Hour)
		logs[i] = testAuditLog(fmt.Sprintf("host-%d.example", i%4), at, float64(i%7)*1.25, []string{"NOERROR", "NXDOMAIN", "SERVFAIL", "NO_RESPONSE"}[i%4], []string{"foreign", "domestic"}[i%2], []string{AuditCacheMiss, AuditCacheHit}[i%2])
		logs[i].ClientIP = fmt.Sprintf("192.0.2.%d", i%3)
		logs[i].TraceID = fmt.Sprintf("trace-%d", i)
		logs[i].Answers = []AnswerDetail{{Type: "CNAME", TTL: 73, Data: fmt.Sprintf("target-%d.example", i%3)}, {Type: "TXT", TTL: 97, Data: "full %_ escaped 雪 answer"}, {Type: "AAAA", TTL: 121, Data: "2001:db8::1"}}
		logs[i].AnswerCount = len(logs[i].Answers)
		logs[i].ResponseFlags = ResponseFlags{AA: i%2 == 0, TC: i%3 == 0, RA: true}
		logs[i].QueryClass = "IN"
		logs[i].UpstreamTag = fmt.Sprintf("upstream-%d", i%2)
		logs[i].Transport = "udp"
		logs[i].ServerName = "dns.example"
		logs[i].URLPath = "/dns-query"
		normalizeAuditLog(&logs[i])
	}
	return logs
}

type auditBufferSnapshot struct {
	Pages          []AuditLogsResponse
	Rank           [][]AuditRankItem
	Slow           []AuditLog
	Minute, Hour   []AuditTimeseriesPoint
	Totals         auditOverviewTotals
	Windows        []AuditPeriodSummary
	Count          int64
	Oldest, Newest *time.Time
}

func snapshotAuditBuffer(t *testing.T, s *SQLiteAuditStorage, logs []AuditLog) auditBufferSnapshot {
	t.Helper()
	from, to := logs[0].QueryTime.Add(-time.Hour), logs[len(logs)-1].QueryTime.Add(time.Hour)
	queries := []AuditLogsQuery{
		{From: from, To: to, Limit: 500},
		{From: from, To: to, Limit: 4, Offset: 3},
		{From: from, To: to, Limit: 5, Sort: AuditLogSearchSort{Field: "duration", Order: "ASC"}},
		{From: from, To: to, Limit: 5, Sort: AuditLogSearchSort{Field: "time", Order: "ASC"}},
		{From: from, To: to, Filters: AuditLogSearchFilters{Domain: AuditTextFilter{Value: "host-2.example", Mode: AuditMatchExact}}},
		{From: from, To: to, Filters: AuditLogSearchFilters{Domain: AuditTextFilter{Value: "host-", Mode: AuditMatchFuzzy}, ClientIP: AuditTextFilter{Value: "192.0.2.1", Mode: AuditMatchExact}}},
		{From: from, To: to, Filters: AuditLogSearchFilters{Answer: AuditTextFilter{Value: "2001:db8::1", Mode: AuditMatchExact}}},
		{From: from, To: to, Filters: AuditLogSearchFilters{Answer: AuditTextFilter{Value: "%_", Mode: AuditMatchFuzzy}}},
		{From: from, To: to, Filters: AuditLogSearchFilters{Answer: AuditTextFilter{Value: "2001:db8", Mode: AuditMatchExact}}},
		{From: from, To: to, Keyword: AuditLogKeywordSearch{Value: "雪", Mode: AuditMatchFuzzy, Fields: []AuditSearchField{AuditSearchFieldAnswer, AuditSearchFieldQueryName}}},
	}
	var out auditBufferSnapshot
	for _, query := range queries {
		page, err := s.QueryLogs(query)
		if err != nil {
			t.Fatal(err)
		}
		out.Pages = append(out.Pages, page)
	}
	query := AuditLogsQuery{From: from, To: to, Limit: 3}
	for {
		page, err := s.QueryLogs(query)
		if err != nil {
			t.Fatal(err)
		}
		out.Pages = append(out.Pages, page)
		if page.NextCursor == "" {
			break
		}
		query.Cursor = page.NextCursor
		if len(out.Pages) > 40 {
			t.Fatal("cursor did not terminate")
		}
	}
	for _, rank := range []RankType{RankByDomain, RankByClient, RankByDomainSet} {
		items, err := s.QueryRank(rank, AuditRangeQuery{From: from, To: to, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		out.Rank = append(out.Rank, items)
	}
	var err error
	out.Slow, err = s.QuerySlowLogs(AuditRangeQuery{From: from, To: to, Limit: 7})
	if err != nil {
		t.Fatal(err)
	}
	out.Minute, err = s.QueryTimeseries(AuditTimeseriesQuery{From: from, To: to, Step: "minute"})
	if err != nil {
		t.Fatal(err)
	}
	out.Hour, err = s.QueryTimeseries(AuditTimeseriesQuery{From: from, To: to, Step: "hour"})
	if err != nil {
		t.Fatal(err)
	}
	out.Totals, err = s.QueryOverviewTotals()
	if err != nil {
		t.Fatal(err)
	}
	out.Windows, err = s.QueryOverviewWindowSummaries(to)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := s.QueryStorageStats()
	if err != nil {
		t.Fatal(err)
	}
	out.Count, out.Oldest, out.Newest = stats.RawLogCount, stats.OldestLogTime, stats.NewestLogTime
	return out
}

func auditBufferWALHash(t *testing.T, s *SQLiteAuditStorage) [32]byte {
	t.Helper()
	data, err := os.ReadFile(s.Path() + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(data)
}

func assertAuditBufferEmptyMirror(t *testing.T, s *SQLiteAuditStorage) {
	t.Helper()
	count, size := s.BufferedUsage()
	if count != 0 || size != 0 || s.pending != nil {
		t.Fatalf("buffer still owns count=%d bytes=%d slots=%d", count, size, len(s.pending))
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_temp_master WHERE name LIKE 'audit_buffer_%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("mirror still owns %d temp objects", n)
	}
}

func TestAuditBufferQueriesMatchDurableStorageBeforeAndAfterFlush(t *testing.T) {
	s, reference := newAuditBufferTestStorage(t), newAuditBufferTestStorage(t)
	logs := auditBufferFixture()
	if err := s.WriteBatch(logs[:6]); err != nil {
		t.Fatal(err)
	}
	if err := reference.WriteBatch(logs); err != nil {
		t.Fatal(err)
	}
	// Aggregates outlive raw rows, so overview must merge buckets rather than
	// recompute all durable history from the raw UNION relation.
	for _, store := range []*SQLiteAuditStorage{s, reference} {
		if _, err := store.DB().Exec(`DELETE FROM audit_log WHERE id=1`); err != nil {
			t.Fatal(err)
		}
	}
	before := auditBufferWALHash(t, s)
	for _, log := range logs[6:] {
		if err := s.StageBufferedLog(log); err != nil {
			t.Fatal(err)
		}
	}
	staged := snapshotAuditBuffer(t, s, logs)
	want := snapshotAuditBuffer(t, reference, logs)
	if !reflect.DeepEqual(staged, want) {
		t.Fatalf("staged query snapshot differs\nstaged=%+v\nwant=%+v", staged, want)
	}
	if got := auditBufferWALHash(t, s); got != before {
		t.Fatal("Stage or realtime queries changed main WAL")
	}
	var raw, tempStore int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM main.audit_log`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != 5 {
		t.Fatalf("stage persisted %d raw rows, want 5", raw)
	}
	if err := s.DB().QueryRow(`PRAGMA temp_store`).Scan(&tempStore); err != nil {
		t.Fatal(err)
	}
	if tempStore != 2 {
		t.Fatalf("temp_store=%d", tempStore)
	}
	if writeErr, maintenanceErr := s.FlushBufferedWithBudget(0); writeErr != nil || maintenanceErr != nil {
		t.Fatalf("flush=%v maintenance=%v", writeErr, maintenanceErr)
	}
	assertAuditBufferEmptyMirror(t, s)
	if got := snapshotAuditBuffer(t, s, logs); !reflect.DeepEqual(got, want) {
		t.Fatal("flushed query snapshot differs from original durable reference")
	}
}

func TestAuditBufferOwnsInputAndStagesWithoutConnection(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	conn, err := s.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	log := auditBufferFixture()[0]
	original := log.Answers[0].Data
	done := make(chan error, 1)
	go func() { done <- s.StageBufferedLog(log) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		_ = conn.Close()
		t.Fatal("Stage borrowed the only SQLite connection")
	}
	_ = conn.Close()
	log.Answers[0].Data = "mutated input"
	page, err := s.QueryLogs(auditBufferAllQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Logs) != 1 || page.Logs[0].Answers[0].Data != original {
		t.Fatalf("stage did not own complete answers: %+v", page.Logs)
	}
	if _, err := s.FlushBufferedWithBudget(0); err != nil {
		t.Fatal(err)
	}
}

func TestAuditBufferMirrorRebuildsAfterConnectionReplacementAndAppend(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	logs := auditBufferFixture()
	for _, log := range logs[:2] {
		if err := s.StageBufferedLog(log); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.QueryLogs(auditBufferAllQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Logs) != 2 {
		t.Fatalf("first mirror=%d", len(page.Logs))
	}
	if err := s.StageBufferedLog(logs[2]); err != nil {
		t.Fatal(err)
	}
	page, err = s.QueryLogs(auditBufferAllQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Logs) != 3 {
		t.Fatalf("incremental mirror=%d", len(page.Logs))
	}
	db := s.DB()
	db.SetMaxIdleConns(0)
	db.SetMaxIdleConns(1)
	page, err = s.QueryLogs(auditBufferAllQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Logs) != 3 || page.Summary.MatchedCount != 3 {
		t.Fatalf("replaced connection lost/repeated tail: %+v", page)
	}
	if writeErr, maintenanceErr := s.FlushBufferedWithBudget(0); writeErr != nil || maintenanceErr != nil {
		t.Fatalf("flush=%v maintenance=%v", writeErr, maintenanceErr)
	}
	var synchronous, foreignKeys int
	if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 || foreignKeys != 1 {
		t.Fatalf("replaced write connection contract synchronous=%d foreign_keys=%d", synchronous, foreignKeys)
	}
}

func TestAuditBufferWriteAndCloseFailuresKeepRetryableTail(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	logs := auditBufferFixture()
	for _, log := range logs[:3] {
		if err := s.StageBufferedLog(log); err != nil {
			t.Fatal(err)
		}
	}
	before := snapshotAuditBuffer(t, s, logs)
	if _, err := s.DB().Exec(`CREATE TRIGGER audit_buffer_fail_aggregate BEFORE INSERT ON audit_hour BEGIN SELECT RAISE(ABORT,'synthetic aggregate failure'); END`); err != nil {
		t.Fatal(err)
	}
	if writeErr, maintenanceErr := s.FlushBufferedWithBudget(0); writeErr == nil || maintenanceErr != nil {
		t.Fatalf("failure classification=%v,%v", writeErr, maintenanceErr)
	}
	if err := s.Close(); err == nil || s.DB() == nil {
		t.Fatal("failed Close discarded the open retryable store")
	}
	if count, _ := s.BufferedUsage(); count != 3 {
		t.Fatalf("failure consumed %d records", count)
	}
	assertAuditBudgetCounts(t, s, 0, 0)
	if got := snapshotAuditBuffer(t, s, logs); !reflect.DeepEqual(got, before) {
		t.Fatal("failed flush changed realtime query snapshot")
	}
	if _, err := s.DB().Exec(`DROP TRIGGER audit_buffer_fail_aggregate`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.DB() != nil {
		t.Fatal("successful Close remains open")
	}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	if got := snapshotAuditBuffer(t, s, logs); !reflect.DeepEqual(got, before) {
		t.Fatal("normal Close did not persist complete tail")
	}
	assertAuditBufferEmptyMirror(t, s)
}

func TestAuditBufferClearIsAtomicAndNeverReusesIDs(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	logs := auditBufferFixture()
	if err := s.WriteBatch(logs[:2]); err != nil {
		t.Fatal(err)
	}
	if err := s.StageBufferedLog(logs[2]); err != nil {
		t.Fatal(err)
	}
	page, err := s.QueryLogs(auditBufferAllQuery())
	if err != nil {
		t.Fatal(err)
	}
	high := int64(0)
	for _, log := range page.Logs {
		high = max(high, log.ID)
	}
	s.evictionTargetBytes = 123
	if _, err := s.DB().Exec(`CREATE TRIGGER audit_buffer_fail_clear BEFORE DELETE ON audit_hour BEGIN SELECT RAISE(ABORT,'synthetic clear failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err == nil {
		t.Fatal("Clear ignored a transaction failure")
	}
	if count, _ := s.BufferedUsage(); count != 1 || s.evictionTargetBytes != 123 {
		t.Fatal("failed Clear changed tail or watermark")
	}
	assertAuditBudgetCounts(t, s, 2, 2)
	if _, err := s.DB().Exec(`DROP TRIGGER audit_buffer_fail_clear`); err != nil {
		t.Fatal(err)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	assertAuditBufferEmptyMirror(t, s)
	assertAuditBudgetCounts(t, s, 0, 0)
	if err := s.StageBufferedLog(logs[3]); err != nil {
		t.Fatal(err)
	}
	page, err = s.QueryLogs(auditBufferAllQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Logs) != 1 || page.Logs[0].ID <= high {
		t.Fatalf("Clear reused old ID: %+v", page)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	if err := s.StageBufferedLog(logs[4]); err != nil {
		t.Fatal(err)
	}
	page, err = s.QueryLogs(auditBufferAllQuery())
	if err != nil {
		t.Fatal(err)
	}
	if page.Logs[0].ID <= high+1 {
		t.Fatal("reopen reused process ID")
	}
}

func TestAuditBufferBoundsRejectWithoutConsumingID(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	log := auditBufferFixture()[0]
	oversized := log
	oversized.Answers = []AnswerDetail{{Data: strings.Repeat("x", int(auditBufferMaxBytes))}}
	high := s.ids.next.Load()
	if err := s.StageBufferedLog(oversized); !errors.Is(err, ErrAuditBufferFull) {
		t.Fatalf("oversized error=%v", err)
	}
	if count, size := s.BufferedUsage(); count != 0 || size != 0 || s.ids.next.Load() != high {
		t.Fatal("rejected row changed buffer or allocator")
	}
	// Oversized rows retain the ordinary immediate durable fallback.
	if err := s.WriteBatch([]AuditLog{oversized}); err != nil {
		t.Fatal(err)
	}
	log = AuditLog{QueryTime: time.Now()}
	for range auditBufferMaxLogs {
		if err := s.StageBufferedLog(log); err != nil {
			t.Fatal(err)
		}
	}
	count, size := s.BufferedUsage()
	high = s.ids.next.Load()
	if err := s.StageBufferedLog(log); !errors.Is(err, ErrAuditBufferFull) {
		t.Fatalf("full error=%v", err)
	}
	if gotCount, gotSize := s.BufferedUsage(); gotCount != count || gotSize != size || s.ids.next.Load() != high {
		t.Fatal("full buffer consumed rejected row or ID")
	}
	if size > auditBufferMaxBytes {
		t.Fatalf("Go estimate exceeded cap: %d", size)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
}

func TestAuditBufferSharedPathAndDirectWritesReserveStableUniqueIDs(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	other := newSQLiteAuditStorage(s.Path())
	if err := other.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	logs := auditBufferFixture()
	if err := s.StageBufferedLog(logs[0]); err != nil {
		t.Fatal(err)
	}
	if err := other.StageBufferedLog(logs[1]); err != nil {
		t.Fatal(err)
	}
	direct := logs[2]
	direct.ID = 999
	if err := other.WriteBatch([]AuditLog{direct}); err != nil {
		t.Fatal(err)
	}
	// Alternating readers share a physical connection but not the mirror owner.
	for range 2 {
		for _, store := range []*SQLiteAuditStorage{s, other} {
			page, err := store.QueryLogs(auditBufferAllQuery())
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Logs) != 2 {
				t.Fatalf("owner mirror leaked or lost records: %+v", page.Logs)
			}
		}
	}
	for _, store := range []*SQLiteAuditStorage{s, other} {
		if err, _ := store.FlushBufferedWithBudget(0); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.QueryLogs(auditBufferAllQuery())
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Logs) != 3 {
		t.Fatalf("mixed writes=%d", len(page.Logs))
	}
	ids := map[int64]bool{}
	for _, log := range page.Logs {
		if ids[log.ID] || log.ID == 999 {
			t.Fatal("mixed path reused or honored input ID")
		}
		ids[log.ID] = true
	}
}

func TestAuditBufferConcurrentReadsStageFlushAndMaintenance(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	logs := auditBufferFixture()
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 150 {
			if err := s.StageBufferedLog(logs[i%len(logs)]); err != nil {
				errs <- err
				return
			}
			if i%17 == 0 {
				if err, maintenance := s.FlushBufferedWithBudget(0); err != nil || maintenance != nil {
					errs <- errors.Join(err, maintenance)
					return
				}
			}
		}
	}()
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				if _, err := s.QueryLogs(AuditLogsQuery{From: time.Unix(0, 0), To: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), Limit: 15}); err != nil {
					errs <- err
					return
				}
				if _, err := s.QueryStorageStats(); err != nil {
					errs <- err
					return
				}
				if _, err := s.QueryOverviewWindowSummaries(time.Now()); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 10 {
			if err := s.enforceStorageBudget(1<<30, 20); err != nil {
				errs <- err
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("concurrent storage operations deadlocked")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if err, maintenance := s.FlushBufferedWithBudget(0); err != nil || maintenance != nil {
		t.Fatalf("final flush=%v,%v", err, maintenance)
	}
	stats, err := s.QueryStorageStats()
	if err != nil {
		t.Fatal(err)
	}
	totals, err := s.QueryOverviewTotals()
	if err != nil {
		t.Fatal(err)
	}
	if stats.RawLogCount != 150 || totals.QueryCount != 150 {
		t.Fatalf("concurrency repeated/lost raw=%d total=%d", stats.RawLogCount, totals.QueryCount)
	}
	assertAuditBufferEmptyMirror(t, s)
}

func auditBufferAllQuery() AuditLogsQuery {
	return AuditLogsQuery{From: time.Unix(0, 0), To: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), Limit: 500}
}

func TestAuditBufferRangedMirrorQueriesUseMainIndexes(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	logs := auditBufferFixture()
	if err := s.WriteBatch(logs[:6]); err != nil {
		t.Fatal(err)
	}
	if err := s.StageBufferedLog(logs[6]); err != nil {
		t.Fatal(err)
	}
	session, err := s.beginAuditRead()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	minute, args := session.aggregateRangeSource("audit_minute", logs[0].QueryTime.Unix(), logs[len(logs)-1].QueryTime.Unix(), true)
	hour, _ := session.aggregateRangeSource("audit_hour", logs[0].QueryTime.Unix(), logs[len(logs)-1].QueryTime.Unix(), true)
	queries := []string{
		`SELECT bucket_start_unix,query_count FROM ` + minute + ` ORDER BY bucket_start_unix`,
		`SELECT bucket_start_unix,query_count FROM ` + hour + ` ORDER BY bucket_start_unix`,
	}
	for _, query := range queries {
		rows, err := session.Query(`EXPLAIN QUERY PLAN `+query, args...)
		if err != nil {
			t.Fatal(err)
		}
		var plans []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plans = append(plans, detail)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
		joined := strings.Join(plans, "\n")
		t.Logf("%s\n%s", query, joined)
		if !strings.Contains(joined, "SEARCH main.audit_") {
			t.Fatalf("ranged query scans whole durable table: %s", joined)
		}
	}
}

func TestAuditBufferCommitFailurePreservesTailAndCapacityWatermark(t *testing.T) {
	s, logs := newAuditBudgetTestStorage(t, 500)
	if err := s.StageBufferedLog(logs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TABLE audit_buffer_parent(id INTEGER PRIMARY KEY);
 CREATE TABLE audit_buffer_child(parent_id INTEGER REFERENCES audit_buffer_parent(id) DEFERRABLE INITIALLY DEFERRED);
 CREATE TRIGGER audit_buffer_fail_commit AFTER INSERT ON audit_log BEGIN INSERT INTO audit_buffer_child VALUES(1); END`); err != nil {
		t.Fatal(err)
	}
	before := s.evictionTargetBytes
	writeErr, maintenanceErr := s.FlushBufferedWithBudget(100)
	if writeErr == nil || !strings.Contains(writeErr.Error(), "commit sqlite audit tx") || maintenanceErr != nil {
		t.Fatalf("commit failure=%v,%v", writeErr, maintenanceErr)
	}
	if count, _ := s.BufferedUsage(); count != 1 || s.evictionTargetBytes != before {
		t.Fatal("commit failure consumed tail or changed watermark")
	}
	assertAuditBudgetCounts(t, s, 500, 500)
	if _, err := s.DB().Exec(`DROP TRIGGER audit_buffer_fail_commit`); err != nil {
		t.Fatal(err)
	}
	if writeErr, maintenanceErr := s.FlushBufferedWithBudget(100); writeErr != nil || maintenanceErr != nil {
		t.Fatalf("retry=%v,%v", writeErr, maintenanceErr)
	}
	assertAuditBudgetCounts(t, s, 499, 501)
}

func TestAuditBufferMaintenanceFailureConsumesCommittedTailOnlyOnce(t *testing.T) {
	s, logs := newAuditBudgetTestStorage(t, 500)
	if err := s.StageBufferedLog(logs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER audit_buffer_fail_eviction BEFORE DELETE ON audit_log BEGIN SELECT RAISE(ABORT,'synthetic eviction failure'); END`); err != nil {
		t.Fatal(err)
	}
	writeErr, maintenanceErr := s.FlushBufferedWithBudget(100)
	if writeErr != nil || maintenanceErr == nil {
		t.Fatalf("maintenance failure=%v,%v", writeErr, maintenanceErr)
	}
	assertAuditBufferEmptyMirror(t, s)
	assertAuditBudgetCounts(t, s, 501, 501)
	if err, maintenance := s.FlushBufferedWithBudget(100); err != nil || maintenance != nil {
		t.Fatalf("empty retry=%v,%v", err, maintenance)
	}
	assertAuditBudgetCounts(t, s, 501, 501)
	if _, err := s.DB().Exec(`DROP TRIGGER audit_buffer_fail_eviction`); err != nil {
		t.Fatal(err)
	}
}

func TestAuditBufferByteCapRejectsWithoutConsumingTail(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	log := auditBufferFixture()[0]
	log.Answers = []AnswerDetail{{Data: strings.Repeat("x", 512*1024)}}
	if err := s.StageBufferedLog(log); err != nil {
		t.Fatal(err)
	}
	count, size := s.BufferedUsage()
	high := s.ids.next.Load()
	if err := s.StageBufferedLog(log); !errors.Is(err, ErrAuditBufferFull) {
		t.Fatalf("byte-cap error=%v", err)
	}
	if gotCount, gotSize := s.BufferedUsage(); gotCount != count || gotSize != size || s.ids.next.Load() != high {
		t.Fatal("byte-cap rejection changed owned tail or ID")
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
}

func TestAuditBufferMaintenanceBeforeFlushRestoresReplacedConnectionPragmas(t *testing.T) {
	for _, operation := range []string{"retention", "capacity", "clear"} {
		t.Run(operation, func(t *testing.T) {
			s := newAuditBufferTestStorage(t)
			log := auditBufferFixture()[0]
			log.QueryTime = time.Now().Add(-30 * 24 * time.Hour)
			if err := s.WriteBatch([]AuditLog{log}); err != nil {
				t.Fatal(err)
			}
			if err := s.StageBufferedLog(log); err != nil {
				t.Fatal(err)
			}
			db := s.DB()
			db.SetMaxIdleConns(0)
			db.SetMaxIdleConns(1)
			var synchronous, foreignKeys int
			if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
				t.Fatal(err)
			}
			if synchronous != 2 || foreignKeys != 0 {
				t.Fatalf("replacement precondition synchronous=%d foreign_keys=%d", synchronous, foreignKeys)
			}
			var err error
			switch operation {
			case "retention":
				err = s.EnforceRetention(AuditSettings{RawRetentionDays: 1, AggregateRetentionDays: 1, MaxStorageMB: 128})
			case "capacity":
				err = s.enforceStorageBudget(100, 1)
			case "clear":
				err = s.Clear()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
				t.Fatal(err)
			}
			if synchronous != 1 || foreignKeys != 1 {
				t.Fatalf("maintenance before flush synchronous=%d foreign_keys=%d", synchronous, foreignKeys)
			}
			want := 1
			if operation == "clear" {
				want = 0
			}
			if count, _ := s.BufferedUsage(); count != want {
				t.Fatalf("maintenance consumed tail count=%d want=%d", count, want)
			}
		})
	}
}
