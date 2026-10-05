package coremain

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestUpstreamRuntimeStatsFlusherPreservesDirtyDuringFlush(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	f := &UpstreamRuntimeStatsFlusher{flush: func() error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}}
	f.MarkDirty()
	done := make(chan error, 1)
	go func() { done <- f.flushDirty() }()
	<-entered
	f.MarkDirty()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := f.flushDirty(); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("change during flush must require a second snapshot, got %d callbacks", got)
	}
	if f.dirty.Load() {
		t.Fatal("successful second snapshot left dirty state")
	}
}

func TestUpstreamRuntimeStatsFlusherRetriesFailure(t *testing.T) {
	wantErr := errors.New("storage unavailable")
	var calls int
	f := &UpstreamRuntimeStatsFlusher{flush: func() error {
		calls++
		if calls == 1 {
			return wantErr
		}
		return nil
	}}
	f.MarkDirty()
	if err := f.flushDirty(); !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped storage error, got %v", err)
	}
	if !f.dirty.Load() {
		t.Fatal("failed snapshot must remain dirty")
	}
	if err := f.flushDirty(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || f.dirty.Load() {
		t.Fatalf("unexpected retry state, calls=%d dirty=%v", calls, f.dirty.Load())
	}
}

func TestUpstreamRuntimeStatsFlusherSerializesConcurrentFlushes(t *testing.T) {
	for _, newChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_snapshot", true: "new_change"}[newChange], func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			var calls, active atomic.Int32
			f := &UpstreamRuntimeStatsFlusher{flush: func() error {
				if active.Add(1) != 1 {
					t.Error("snapshot callbacks overlapped")
				}
				defer active.Add(-1)
				if calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				return nil
			}}
			f.MarkDirty()
			done := make(chan error, 2)
			go func() { done <- f.flushDirty() }()
			<-entered
			if newChange {
				f.MarkDirty()
			}
			secondStarted := make(chan struct{})
			go func() {
				close(secondStarted)
				done <- f.flushDirty()
			}()
			<-secondStarted
			close(release)
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			wantCalls := int32(1)
			if newChange {
				wantCalls = 2
			}
			if got := calls.Load(); got != wantCalls {
				t.Fatalf("expected %d serialized snapshots, got %d", wantCalls, got)
			}
		})
	}
}

func TestUpstreamRuntimeStatsFlusherCloseSavesPendingStats(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "control.db")
	stats := []UpstreamRuntimeStats{{
		PluginTag: "forward", UpstreamTag: "primary", QueryTotal: 12,
		ErrorTotal: 2, WinnerTotal: 9, LatencyTotalUs: 48000, LatencyCount: 10,
	}}
	var calls atomic.Int32
	f := NewUpstreamRuntimeStatsFlusher(nil, func() error {
		calls.Add(1)
		return SaveUpstreamRuntimeStats(dbPath, stats)
	})
	t.Cleanup(func() { _ = f.Close() })
	f.MarkDirty()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	values, err := LoadUpstreamRuntimeStatsByPlugin(dbPath, "forward")
	if err != nil {
		t.Fatal(err)
	}
	got := values["primary"]
	got.UpdatedAtUnixMS = 0
	if got != stats[0] {
		t.Fatalf("close did not persist final snapshot, got %+v", got)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("clean close must not repeat persistence, got %d callbacks", got)
	}
}

func TestUpstreamRuntimeStatsFlusherCloseWaitsForFlushAndSavesNewChange(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	f := NewUpstreamRuntimeStatsFlusher(nil, func() error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	})
	f.MarkDirty()
	flushDone := make(chan error, 1)
	go func() { flushDone <- f.flushDirty() }()
	<-entered
	f.MarkDirty()
	closeDone := make(chan error, 1)
	go func() { closeDone <- f.Close() }()
	<-f.doneCh
	select {
	case err := <-closeDone:
		t.Errorf("close returned before snapshot completed, got %v", err)
	default:
	}
	close(release)
	if err := <-flushDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("close must save the change made during snapshot, got %d callbacks", got)
	}
}

func TestUpstreamRuntimeStatsFlusherCloseRetriesFailure(t *testing.T) {
	wantErr := errors.New("storage unavailable")
	var calls int
	f := NewUpstreamRuntimeStatsFlusher(nil, func() error {
		calls++
		if calls == 1 {
			return wantErr
		}
		return nil
	})
	f.MarkDirty()
	if err := f.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("expected close to report storage failure, got %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || f.dirty.Load() {
		t.Fatalf("unexpected close retry state, calls=%d dirty=%v", calls, f.dirty.Load())
	}
}

func TestUpstreamRuntimeStatsFlusherCleanAndNilCallbacks(t *testing.T) {
	var f *UpstreamRuntimeStatsFlusher
	f.MarkDirty()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	f = &UpstreamRuntimeStatsFlusher{flush: func() error {
		t.Fatal("clean stats triggered persistence")
		return nil
	}}
	if err := f.flushDirty(); err != nil {
		t.Fatal(err)
	}
	f = &UpstreamRuntimeStatsFlusher{}
	f.MarkDirty()
	if err := f.flushDirty(); err != nil {
		t.Fatal(err)
	}
}
