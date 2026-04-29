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
