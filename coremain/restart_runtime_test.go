package coremain

import (
	"errors"
	"testing"
	"time"
)

type testRestartPreparer struct {
	err    error
	called chan struct{}
}

func (p testRestartPreparer) PrepareForRestart() error {
	select {
	case p.called <- struct{}{}:
	default:
	}
	return p.err
}

func TestNormalizeRestartDelay(t *testing.T) {
	tests := []struct {
		name    string
		delayMs int
		want    int
		wantErr bool
	}{
		{name: "default", delayMs: 0, want: defaultRestartDelayMs},
		{name: "custom", delayMs: 1200, want: 1200},
		{name: "too large", delayMs: maxRestartDelayMs + 1, wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeRestartDelay(tc.delayMs)
			if tc.wantErr {
				var delayErr *RestartDelayError
				if !errors.As(err, &delayErr) {
					t.Fatalf("expected RestartDelayError, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeRestartDelay() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("normalizeRestartDelay() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestScheduleSelfRestart_PrepareFailureClearsState(t *testing.T) {
	if !SelfRestartSupported() {
		t.Skip("self restart is unsupported on this platform")
	}

	oldExec := execSelfRestartFn
	execSelfRestartFn = func() error {
		t.Fatal("execSelfRestartFn should not be called when preparation fails")
		return nil
	}
	defer func() {
		execSelfRestartFn = oldExec
	}()

	preparer := testRestartPreparer{
		err:    errors.New("flush failed"),
		called: make(chan struct{}, 1),
	}
	m := NewTestMosdnsWithPlugins(map[string]any{"bad": preparer})

	if _, err := m.ScheduleSelfRestart(1); err != nil {
		t.Fatalf("ScheduleSelfRestart() error = %v", err)
	}

	waitForSignal(t, preparer.called, time.Second, "restart preparer was not called")
	waitForCondition(t, time.Second, func() bool {
		return !m.restartScheduled.Load()
	}, "restartScheduled should be cleared after preparation failure")
}

func waitForSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatal(msg)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}
