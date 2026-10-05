package coremain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditJSONBudgetIncludesEscapingWithoutAllocations(t *testing.T) {
	for _, value := range []string{"plain", "\x00\n\t\r", "<>&\"\\", "中文\u2028\u2029", string([]byte{0xff})} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if got := auditJSONStringBudget(value); got < int64(len(encoded)) {
			t.Fatalf("JSON budget %d below encoded length %d", got, len(encoded))
		}
		if n := testing.AllocsPerRun(100, func() { auditJSONStringBudget(value) }); n != 0 {
			t.Fatalf("budget allocated %v", n)
		}
	}
}

func TestAuditCollectorEscapedAnswersFlushBeforeQueryExpansion(t *testing.T) {
	c := newAuditWorkerTestCollector(t, 300000)
	log := auditWorkerTestLog(1)
	log.Answers = []AnswerDetail{{Type: "TXT", Data: strings.Repeat("\x00", 32*1024)}}
	cost := estimateAuditBufferBytes(log)
	if cost > auditCollectorBufferBytes || cost*2 <= auditCollectorBufferBytes {
		t.Fatalf("invalid escaped-answer fixture budget %d", cost)
	}
	c.CollectLog(log)
	waitAuditWorker(t, func() bool { return c.GetOverview(60).TotalQueryCount == 1 })
	c.CollectLog(log)
	waitAuditWorker(t, func() bool { return auditWorkerRowCount(t, c) == 1 })
	if n, budget := c.getStorage().BufferedUsage(); n != 1 || budget > auditCollectorBufferBytes {
		t.Fatalf("escaped tail count=%d budget=%d", n, budget)
	}
}
