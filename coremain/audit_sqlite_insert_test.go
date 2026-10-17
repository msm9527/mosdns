package coremain

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func auditInsertFixture(count int) []AuditLog {
	fixture := auditBufferFixture()
	logs := make([]AuditLog, count)
	for i := range logs {
		logs[i] = fixture[i%len(fixture)]
		logs[i].QueryName = fmt.Sprintf("chunk-%03d.example", i)
		logs[i].QueryTime = fixture[0].QueryTime.Add(time.Duration(i) * time.Millisecond)
	}
	return logs
}

func TestSQLiteAuditInsertChunksMatchIndividualWrites(t *testing.T) {
	for _, count := range []int{1, 31, 32, 33, 65} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			batch := newAuditBufferTestStorage(t)
			single := newAuditBufferTestStorage(t)
			logs := auditInsertFixture(count)
			if err := batch.WriteBatch(logs); err != nil {
				t.Fatal(err)
			}
			for i := range logs {
				if err := single.WriteBatch(logs[i : i+1]); err != nil {
					t.Fatal(err)
				}
			}
			// Compare public history, search, ranks, and rollups as well as rows.
			if got, want := snapshotAuditBuffer(t, batch, logs), snapshotAuditBuffer(t, single, logs); !reflect.DeepEqual(got, want) {
				t.Fatal("chunked insert changed history or aggregates")
			}
		})
	}
}

func TestSQLiteAuditLaterInsertChunkFailureIsRetryable(t *testing.T) {
	s := newAuditBufferTestStorage(t)
	logs := auditInsertFixture(97)
	logs[33].QueryName = "failure.example"
	for _, log := range logs {
		if err := s.StageBufferedLog(log); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER fail_second_insert_chunk BEFORE INSERT ON audit_log
		WHEN NEW.query_name = 'failure.example' BEGIN SELECT RAISE(ABORT, 'chunk failure'); END`); err != nil {
		t.Fatal(err)
	}
	writeErr, maintenanceErr := s.FlushBufferedWithBudget(0)
	if writeErr == nil || !strings.Contains(writeErr.Error(), "insert sqlite audit row") || maintenanceErr != nil {
		t.Fatalf("write=%v maintenance=%v", writeErr, maintenanceErr)
	}
	assertAuditBudgetCounts(t, s, 0, 0)
	if count, _ := s.BufferedUsage(); count != len(logs) {
		t.Fatalf("failed chunk lost retryable records: %d", count)
	}
	if _, err := s.DB().Exec(`DROP TRIGGER fail_second_insert_chunk`); err != nil {
		t.Fatal(err)
	}
	if writeErr, maintenanceErr := s.FlushBufferedWithBudget(0); writeErr != nil || maintenanceErr != nil {
		t.Fatalf("retry write=%v maintenance=%v", writeErr, maintenanceErr)
	}
	assertAuditBudgetCounts(t, s, len(logs), len(logs))
}

func TestAuditCollectorTransfersOwnedHistoryThroughFlush(t *testing.T) {
	c := newAuditWorkerTestCollector(t, 300000)
	log := auditWorkerTestLog(1)
	log.Answers = []AnswerDetail{{Type: "TXT", Data: "original", TTL: 97}}
	log.AnswerCount = 1
	c.CollectLog(log)
	log.Answers[0].Data = "caller reuse"
	waitAuditWorker(t, func() bool { return c.GetOverview(60).TotalQueryCount == 1 })
	for _, flush := range []bool{false, true} {
		if flush {
			if err := c.flushBuffered(); err != nil {
				t.Fatal(err)
			}
		}
		page, err := c.GetLogs(AuditLogsQuery{From: log.QueryTime.Add(-time.Hour), To: log.QueryTime.Add(time.Hour)})
		if err != nil || len(page.Logs) != 1 || len(page.Logs[0].Answers) != 1 || page.Logs[0].Answers[0].Data != "original" {
			t.Fatalf("flush=%v changed owned log: %+v err=%v", flush, page, err)
		}
	}
}
