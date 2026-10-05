package coremain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	auditBufferMaxBytes int64 = 4 * 1024 * 1024
	auditBufferMaxLogs        = 4096
)

// ErrAuditBufferFull means the caller must flush accepted records before retrying.
// Stage never evicts a record or writes to disk to make room.
var ErrAuditBufferFull = errors.New("sqlite audit memory buffer is full")
var auditBufferOwnerSequence atomic.Uint64

// StageBufferedLog accepts an owned, normalized copy with a stable process ID.
// The collector owns flush scheduling and retries after ErrAuditBufferFull.
func (s *SQLiteAuditStorage) StageBufferedLog(log AuditLog) error {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()
	if s.DB() == nil {
		return fmt.Errorf("sqlite audit storage is not open")
	}
	normalizeAuditLog(&log)
	size := estimateAuditBufferBytes(log)
	if len(s.pending) >= auditBufferMaxLogs || size > auditBufferMaxBytes-s.pendingBytes {
		return ErrAuditBufferFull
	}
	id, err := s.ids.allocate()
	if err != nil {
		return err
	}
	log.ID = id
	// Clone variable input so neither mutation nor a large caller backing array
	// can change or retain memory beyond the accepted record's accounting.
	cloneAuditLog(&log)
	s.pending = append(s.pending, log)
	s.pendingBytes += size
	s.bufferRevision++
	return nil
}

func cloneAuditLog(log *AuditLog) {
	log.ClientIP = strings.Clone(log.ClientIP)
	log.QueryType = strings.Clone(log.QueryType)
	log.QueryName = strings.Clone(log.QueryName)
	log.QueryClass = strings.Clone(log.QueryClass)
	log.TraceID = strings.Clone(log.TraceID)
	log.ResponseCode = strings.Clone(log.ResponseCode)
	log.DomainSetRaw = strings.Clone(log.DomainSetRaw)
	log.DomainSetNorm = strings.Clone(log.DomainSetNorm)
	log.UpstreamTag = strings.Clone(log.UpstreamTag)
	log.Transport = strings.Clone(log.Transport)
	log.ServerName = strings.Clone(log.ServerName)
	log.URLPath = strings.Clone(log.URLPath)
	log.CacheStatus = strings.Clone(log.CacheStatus)
	if log.Answers != nil {
		answers := make([]AnswerDetail, len(log.Answers))
		for i, answer := range log.Answers {
			answers[i] = answer
			answers[i].Type = strings.Clone(answer.Type)
			answers[i].Data = strings.Clone(answer.Data)
		}
		log.Answers = answers
	}
}

func estimateAuditLogBytes(log AuditLog) int64 {
	// Reserve twice the struct size for append's backing-array growth. This is
	// an estimate of owned Go memory, not a serialization or SQLite size budget.
	size := int64(2*unsafe.Sizeof(log)) + int64(len(log.Answers))*int64(unsafe.Sizeof(AnswerDetail{}))
	for _, value := range []string{log.ClientIP, log.QueryType, log.QueryName, log.QueryClass, log.TraceID, log.ResponseCode, log.DomainSetRaw, log.DomainSetNorm, log.UpstreamTag, log.Transport, log.ServerName, log.URLPath, log.CacheStatus} {
		size += int64(len(value))
	}
	for _, answer := range log.Answers {
		size += int64(len(answer.Type) + len(answer.Data))
	}
	return size
}

// BufferedUsage reports the conservative work budget for accepted records,
// including owned data and query/serialization reserves, not measured RSS.
func (s *SQLiteAuditStorage) BufferedUsage() (count int, estimatedBytes int64) {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()
	return len(s.pending), s.pendingBytes
}

// FlushBufferedWithBudget consumes the tail only after raw rows, aggregates and
// capacity work commit together. Write failures leave the complete tail retryable.
func (s *SQLiteAuditStorage) FlushBufferedWithBudget(maxBytes int64) (writeErr, maintenanceErr error) {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()
	return s.flushBufferedLocked(maxBytes)
}

func (s *SQLiteAuditStorage) flushBufferedLocked(maxBytes int64) (writeErr, maintenanceErr error) {
	if len(s.pending) == 0 {
		return nil, nil
	}
	writeErr, maintenanceErr = s.writeBatchWithBudgetLocked(s.pending, maxBytes, false)
	if writeErr == nil {
		s.clearBufferedLocked()
		maintenanceErr = errors.Join(maintenanceErr, s.releaseBufferedMirror())
	}
	return writeErr, maintenanceErr
}

func (s *SQLiteAuditStorage) clearBufferedLocked() {
	clear(s.pending)
	s.pending = nil
	s.pendingBytes = 0
	s.bufferRevision++
	s.bufferGeneration++
}

// An auditReadSession owns the operation lock and the physical SQLite connection.
// Its helpers must never reacquire s.DB while the single connection is borrowed.
type auditReadSession struct {
	conn     *sql.Conn
	storage  *SQLiteAuditStorage
	buffered bool
}

func (s *SQLiteAuditStorage) beginAuditRead() (*auditReadSession, error) {
	s.bufferMu.Lock()
	db := s.DB()
	if db == nil {
		s.bufferMu.Unlock()
		return nil, nil
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		s.bufferMu.Unlock()
		return nil, fmt.Errorf("borrow sqlite audit read connection: %w", err)
	}
	session := &auditReadSession{conn: conn, storage: s, buffered: len(s.pending) > 0}
	if session.buffered {
		if err := s.ensureBufferedMirror(conn); err != nil {
			session.Close()
			return nil, err
		}
	}
	return session, nil
}

func (r *auditReadSession) Close() { _ = r.conn.Close(); r.storage.bufferMu.Unlock() }
func (r *auditReadSession) Query(query string, args ...any) (*sql.Rows, error) {
	return r.conn.QueryContext(context.Background(), query, args...)
}
func (r *auditReadSession) QueryRow(query string, args ...any) *sql.Row {
	return r.conn.QueryRowContext(context.Background(), query, args...)
}
func (r *auditReadSession) table(table string) string {
	if r.buffered {
		return "temp.audit_buffer_all_" + strings.TrimPrefix(table, "audit_")
	}
	return "main." + table
}

func (s *SQLiteAuditStorage) ensureBufferedMirror(conn *sql.Conn) error {
	ctx := context.Background()
	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_temp_master WHERE type='table' AND name='audit_buffer_marker'`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect sqlite audit mirror: %w", err)
	}
	if exists == 0 {
		// temp_store is connection scoped and RuntimeDB can replace connections.
		if _, err := conn.ExecContext(ctx, `PRAGMA temp_store = MEMORY`); err != nil {
			return fmt.Errorf("configure sqlite audit temp memory: %w", err)
		}
		if err := createAuditBufferMirror(conn); err != nil {
			return err
		}
	}
	var owner, generation, revision uint64
	var count int
	err := conn.QueryRowContext(ctx, `SELECT owner, generation, revision, row_count FROM temp.audit_buffer_marker`).Scan(&owner, &generation, &revision, &count)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read sqlite audit mirror marker: %w", err)
	}
	if err == nil && owner == s.bufferOwner && generation == s.bufferGeneration && revision == s.bufferRevision {
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sqlite audit mirror tx: %w", err)
	}
	defer tx.Rollback()
	if owner != s.bufferOwner || generation != s.bufferGeneration || count > len(s.pending) {
		for _, table := range []string{"audit_buffer_log", "audit_buffer_minute", "audit_buffer_hour"} {
			if _, err := tx.Exec(`DELETE FROM temp.` + table); err != nil {
				return fmt.Errorf("reset sqlite audit mirror: %w", err)
			}
		}
		count = 0
	}
	tail := s.pending[count:]
	if err := insertAuditLogsInto(tx, "temp.audit_buffer_log", tail); err != nil {
		return err
	}
	minute, hour := buildAggregateRows(tail)
	if err := upsertAggregateTable(tx, "audit_buffer_minute", minute); err != nil {
		return err
	}
	if err := upsertAggregateTable(tx, "audit_buffer_hour", hour); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM temp.audit_buffer_marker`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO temp.audit_buffer_marker(owner,generation,revision,row_count) VALUES(?,?,?,?)`, s.bufferOwner, s.bufferGeneration, s.bufferRevision, len(s.pending)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite audit mirror tx: %w", err)
	}
	return nil
}

func createAuditBufferMirror(conn *sql.Conn) error {
	ctx := context.Background()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sqlite audit mirror schema: %w", err)
	}
	defer tx.Rollback()
	for _, table := range []string{"audit_log", "audit_minute", "audit_hour"} {
		var schema string
		if err := tx.QueryRow(`SELECT sql FROM main.sqlite_master WHERE type='table' AND name=?`, table).Scan(&schema); err != nil {
			return fmt.Errorf("read sqlite audit table schema: %w", err)
		}
		schema = strings.Replace(schema, "CREATE TABLE "+table, "CREATE TEMP TABLE audit_buffer_"+strings.TrimPrefix(table, "audit_"), 1)
		if _, err := tx.Exec(schema); err != nil {
			return fmt.Errorf("create sqlite audit mirror table: %w", err)
		}
	}
	if _, err := tx.Exec(`CREATE TEMP TABLE audit_buffer_marker(owner INTEGER NOT NULL,generation INTEGER NOT NULL,revision INTEGER NOT NULL,row_count INTEGER NOT NULL);
 CREATE TEMP VIEW audit_buffer_all_log AS SELECT * FROM main.audit_log UNION ALL SELECT * FROM temp.audit_buffer_log;
 CREATE INDEX temp.idx_audit_buffer_time ON audit_buffer_log(query_time_unix_ms DESC,id DESC);
 CREATE INDEX temp.idx_audit_buffer_duration ON audit_buffer_log(duration_ms DESC,query_time_unix_ms DESC,id DESC);`); err != nil {
		return fmt.Errorf("create sqlite audit mirror relation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite audit mirror schema: %w", err)
	}
	return nil
}

// Stores sharing a RuntimeDB also share ID reservations for their uncommitted
// tails. The process high water survives Clear and reopen without ID reuse.
type auditIDAllocator struct{ next atomic.Int64 }

var auditIDs struct {
	sync.Mutex
	paths map[string]*auditIDAllocator
}

func auditIDsForPath(path string) *auditIDAllocator {
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	auditIDs.Lock()
	defer auditIDs.Unlock()
	if auditIDs.paths == nil {
		auditIDs.paths = make(map[string]*auditIDAllocator)
	}
	ids := auditIDs.paths[path]
	if ids == nil {
		ids = &auditIDAllocator{}
		auditIDs.paths[path] = ids
	}
	return ids
}
func (ids *auditIDAllocator) syncFrom(db auditPageQuerier) error {
	var high int64
	if err := db.QueryRow(`SELECT MAX(COALESCE((SELECT seq FROM main.sqlite_sequence WHERE name='audit_log'),0),COALESCE((SELECT MAX(id) FROM main.audit_log),0))`).Scan(&high); err != nil {
		return fmt.Errorf("read sqlite audit ID high water: %w", err)
	}
	for old := ids.next.Load(); high > old; old = ids.next.Load() {
		if ids.next.CompareAndSwap(old, high) {
			break
		}
	}
	return nil
}
func (ids *auditIDAllocator) allocate() (int64, error) {
	for {
		old := ids.next.Load()
		if old == math.MaxInt64 {
			return 0, fmt.Errorf("sqlite audit ID space exhausted")
		}
		if ids.next.CompareAndSwap(old, old+1) {
			return old + 1, nil
		}
	}
}

// Drop this owner's disposable mirror after consumption. Empty reads never
// create it, and another store sharing the connection owns its own mirror.
func (s *SQLiteAuditStorage) releaseBufferedMirror() error {
	db := s.DB()
	if db == nil {
		return nil
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("borrow sqlite audit mirror cleanup connection: %w", err)
	}
	defer conn.Close()
	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_temp_master WHERE type='table' AND name='audit_buffer_marker'`).Scan(&exists); err != nil {
		return fmt.Errorf("inspect sqlite audit mirror cleanup: %w", err)
	}
	if exists == 0 {
		return nil
	}
	var owner uint64
	if err := conn.QueryRowContext(ctx, `SELECT owner FROM temp.audit_buffer_marker`).Scan(&owner); err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read sqlite audit mirror cleanup owner: %w", err)
	}
	if owner != s.bufferOwner {
		return nil
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sqlite audit mirror cleanup: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP VIEW temp.audit_buffer_all_log`); err != nil {
		return fmt.Errorf("drop sqlite audit mirror view: %w", err)
	}
	for _, table := range []string{"log", "minute", "hour"} {
		if _, err := tx.Exec(`DROP TABLE temp.audit_buffer_` + table); err != nil {
			return fmt.Errorf("drop sqlite audit mirror table: %w", err)
		}
	}
	if _, err := tx.Exec(`DROP TABLE temp.audit_buffer_marker`); err != nil {
		return fmt.Errorf("drop sqlite audit mirror marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite audit mirror cleanup: %w", err)
	}
	return nil
}

// Filter both UNION branches before grouping. SQLite does not push an outer
// range through this aggregate relation, which would sort all retained history.
func (r *auditReadSession) aggregateRangeSource(table string, from, to int64, groupBuckets bool) (string, []any) {
	if !r.buffered {
		return "main." + table + " WHERE bucket_start_unix BETWEEN ? AND ?", []any{from, to}
	}
	union := `SELECT * FROM main.` + table + ` WHERE bucket_start_unix BETWEEN ? AND ? UNION ALL SELECT * FROM temp.audit_buffer_` + strings.TrimPrefix(table, "audit_") + ` WHERE bucket_start_unix BETWEEN ? AND ?`
	if !groupBuckets {
		return "(" + union + ")", []any{from, to, from, to}
	}
	return `(` + auditAggregateMergeSelect + ` FROM (` + union + `) GROUP BY bucket_start_unix)`, []any{from, to, from, to}
}
func (r *auditReadSession) aggregateTotalsSource(table string) string {
	if !r.buffered {
		return "main." + table
	}
	return `(SELECT * FROM main.` + table + ` UNION ALL SELECT * FROM temp.audit_buffer_` + strings.TrimPrefix(table, "audit_") + `)`
}

const auditAggregateMergeSelect = `SELECT bucket_start_unix,
 SUM(query_count) AS query_count,SUM(duration_sum_ms) AS duration_sum_ms,MAX(duration_max_ms) AS duration_max_ms,
 SUM(resolved_query_count) AS resolved_query_count,SUM(resolved_duration_sum_ms) AS resolved_duration_sum_ms,MAX(resolved_duration_max_ms) AS resolved_duration_max_ms,
 SUM(error_count) AS error_count,SUM(no_response_count) AS no_response_count,SUM(cache_hit_count) AS cache_hit_count`
