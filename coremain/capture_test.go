package coremain

import (
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestInMemoryLogCollectorCapsCapturedLogs(t *testing.T) {
	collector := &InMemoryLogCollector{}
	level := zap.NewAtomicLevelAt(zap.InfoLevel)
	collector.StartCapture(time.Minute, level)
	t.Cleanup(func() {
		collector.StopCapture(level)
	})

	entry := zapcore.Entry{
		Level:   zap.DebugLevel,
		Time:    time.Now(),
		Message: "debug message",
	}
	for i := 0; i < maxCapturedLogs*2; i++ {
		collector.AddLog(entry, nil)
	}

	logs := collector.GetLogs()
	if len(logs) != maxCapturedLogs {
		t.Fatalf("len(logs) = %d, want %d", len(logs), maxCapturedLogs)
	}
	if collector.dropped != 0 {
		t.Fatalf("collector.dropped = %d after GetLogs, want 0", collector.dropped)
	}
}
