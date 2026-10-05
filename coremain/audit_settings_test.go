package coremain

import "testing"

func TestAuditQueueCapacityUsesConservativeBound(t *testing.T) {
	settings := defaultAuditSettings()
	if got := auditQueueCapacity(settings); got != 8192 {
		t.Fatalf("auditQueueCapacity(default) = %d, want 8192", got)
	}

	if got := auditQueueCapacity(AuditSettings{FlushBatchSize: 1}); got != auditMinQueueCapacity {
		t.Fatalf("auditQueueCapacity(min) = %d, want %d", got, auditMinQueueCapacity)
	}

	if got := auditQueueCapacity(AuditSettings{FlushBatchSize: 4096}); got != auditMaxQueueCapacity {
		t.Fatalf("auditQueueCapacity(max) = %d, want %d", got, auditMaxQueueCapacity)
	}
}

func TestAuditLargerBatchesKeepHistoricalIngressAllocation(t *testing.T) {
	old := newAuditQueues(AuditSettings{FlushBatchSize: 256})
	candidate := newAuditQueues(defaultAuditSettings())
	if len(old) != len(candidate) {
		t.Fatal("batch size changed shard count")
	}
	for i := range old {
		if cap(old[i]) != cap(candidate[i]) {
			t.Fatalf("shard %d grew from %d to %d", i, cap(old[i]), cap(candidate[i]))
		}
	}
}
