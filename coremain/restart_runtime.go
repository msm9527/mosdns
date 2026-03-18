package coremain

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	defaultRestartDelayMs = 300
	maxRestartDelayMs     = 60_000
)

var (
	ErrRestartAlreadyScheduled     = errors.New("restart already scheduled")
	ErrRestartSchedulerUnavailable = errors.New("restart scheduler unavailable")

	internalRestartSchedulerMu sync.RWMutex
	internalRestartScheduler   func(int) error
	execSelfRestartFn          = ExecSelfRestart
)

type RestartDelayError struct {
	Max int
}

func (e *RestartDelayError) Error() string {
	return fmt.Sprintf("delay_ms must be <= %d", e.Max)
}

func normalizeRestartDelay(delayMs int) (int, error) {
	if delayMs <= 0 {
		return defaultRestartDelayMs, nil
	}
	if delayMs > maxRestartDelayMs {
		return 0, &RestartDelayError{Max: maxRestartDelayMs}
	}
	return delayMs, nil
}

func registerInternalRestartScheduler(fn func(int) error) func() {
	internalRestartSchedulerMu.Lock()
	internalRestartScheduler = fn
	internalRestartSchedulerMu.Unlock()

	return func() {
		internalRestartSchedulerMu.Lock()
		if internalRestartScheduler != nil {
			internalRestartScheduler = nil
		}
		internalRestartSchedulerMu.Unlock()
	}
}

func requestInternalRestart(delayMs int) error {
	internalRestartSchedulerMu.RLock()
	fn := internalRestartScheduler
	internalRestartSchedulerMu.RUnlock()
	if fn == nil {
		return ErrRestartSchedulerUnavailable
	}
	return fn(delayMs)
}

func (m *Mosdns) ScheduleSelfRestart(delayMs int) (int, error) {
	if m == nil {
		return 0, errors.New("service unavailable")
	}
	delayMs, err := normalizeRestartDelay(delayMs)
	if err != nil {
		return 0, err
	}
	if !SelfRestartSupported() {
		return 0, ErrSelfRestartNotSupported
	}
	if !m.tryScheduleRestart() {
		return 0, ErrRestartAlreadyScheduled
	}
	go m.runScheduledRestart(delayMs)
	return delayMs, nil
}

func (m *Mosdns) runScheduledRestart(delayMs int) {
	logger := m.Logger()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.Error("panic during self restart flow", zap.Any("panic", recovered))
			m.clearScheduledRestart()
		}
	}()

	timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
	defer timer.Stop()
	<-timer.C

	if err := m.preparePluginsForRestart(logger); err != nil {
		logger.Error("restart aborted due to plugin preparation failure", zap.Error(err))
		m.clearScheduledRestart()
		return
	}

	logger.Info("executing self restart")
	_ = logger.Sync()
	if err := execSelfRestartFn(); err != nil {
		logger.Error("self-restart exec failed", zap.Error(err))
		m.clearScheduledRestart()
	}
}

func (m *Mosdns) preparePluginsForRestart(logger *zap.Logger) error {
	logger.Info("preparing plugins for restart")
	var errs []error

	for tag, p := range m.plugins {
		if p == nil {
			continue
		}
		preparer, ok := p.(RestartPreparer)
		if !ok {
			continue
		}
		logger.Info("running plugin restart preparer", zap.String("tag", tag))
		if err := preparer.PrepareForRestart(); err != nil {
			logger.Error("plugin restart preparation failed", zap.String("tag", tag), zap.Error(err))
			errs = append(errs, fmt.Errorf("%s: %w", tag, err))
		}
	}

	if len(errs) == 0 {
		logger.Info("plugin restart preparation completed")
		return nil
	}
	return errors.Join(errs...)
}
