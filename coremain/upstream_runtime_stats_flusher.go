package coremain

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

const upstreamRuntimeStatsFlushInterval = 5 * time.Second

type UpstreamRuntimeStatsFlusher struct {
	logger *zap.Logger
	flush  func() error
	stopCh chan struct{}
	doneCh chan struct{}
	dirty  atomic.Bool
	mu     sync.Mutex
	once   sync.Once
}

func NewUpstreamRuntimeStatsFlusher(logger *zap.Logger, flush func() error) *UpstreamRuntimeStatsFlusher {
	if logger == nil {
		logger = zap.NewNop()
	}
	f := &UpstreamRuntimeStatsFlusher{
		logger: logger,
		flush:  flush,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go f.run()
	return f
}

func (f *UpstreamRuntimeStatsFlusher) MarkDirty() {
	if f == nil {
		return
	}
	f.dirty.Store(true)
}

func (f *UpstreamRuntimeStatsFlusher) Close() error {
	if f == nil {
		return nil
	}
	f.once.Do(func() {
		close(f.stopCh)
	})
	<-f.doneCh
	return f.flushDirty()
}

func (f *UpstreamRuntimeStatsFlusher) run() {
	ticker := time.NewTicker(upstreamRuntimeStatsFlushInterval)
	defer func() {
		ticker.Stop()
		close(f.doneCh)
	}()

	for {
		select {
		case <-ticker.C:
			if err := f.flushDirty(); err != nil {
				f.logger.Warn("flush upstream runtime stats failed", zap.Error(err))
			}
		case <-f.stopCh:
			return
		}
	}
}

func (f *UpstreamRuntimeStatsFlusher) flushDirty() error {
	if f == nil || f.flush == nil || !f.dirty.Load() {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.dirty.Load() {
		return nil
	}
	if err := f.flush(); err != nil {
		return fmt.Errorf("flush upstream runtime stats: %w", err)
	}
	f.dirty.Store(false)
	return nil
}
