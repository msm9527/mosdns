package domain_memory_pool

import (
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"go.uber.org/zap"
)

type hotPublishMode uint8

const (
	hotPublishAdd hotPublishMode = iota
	hotPublishReplace
	hotPublishStop
)

type hotPublishRequest struct {
	mode  hotPublishMode
	rules []string
	resp  chan error
}

func (d *domainMemoryPool) initHotPublisher() {
	if d.snapshotter == nil || d.publishTarget() == "" {
		return
	}
	d.hotCmdChan = make(chan hotPublishRequest, 16)
	d.hotDoneChan = make(chan struct{})
	go d.startHotPublisher()
}

func (d *domainMemoryPool) startHotPublisher() {
	var timer *time.Timer
	var timerC <-chan time.Time
	pending := make(map[string]struct{})
	defer close(d.hotDoneChan)

	for {
		select {
		case req := <-d.hotCmdChan:
			switch req.mode {
			case hotPublishAdd:
				timer, timerC = d.handleHotAddRequest(timer, req.rules, pending)
			case hotPublishReplace:
				timer, timerC = stopHotPublishTimer(timer)
				clearHotRules(pending)
				atomicStoreInt64(&d.hotPendingCount, 0)
				req.resp <- d.dispatchHotReplace(req.rules)
			case hotPublishStop:
				timer, timerC = stopHotPublishTimer(timer)
				clearHotRules(pending)
				atomicStoreInt64(&d.hotPendingCount, 0)
				req.resp <- nil
				return
			}
		case <-timerC:
			timer, timerC = stopHotPublishTimer(timer)
			d.flushPendingHotAdds(pending)
		}
	}
}

func (d *domainMemoryPool) handleHotAddRequest(
	timer *time.Timer,
	rules []string,
	pending map[string]struct{},
) (*time.Timer, <-chan time.Time) {
	for _, rule := range rules {
		pending[rule] = struct{}{}
	}
	atomicStoreInt64(&d.hotPendingCount, int64(len(pending)))
	if len(pending) == 0 {
		return timer, timerChannel(timer)
	}
	if d.policy.publishDebounce <= 0 {
		d.flushPendingHotAdds(pending)
		return stopHotPublishTimer(timer)
	}
	return resetHotPublishTimer(timer, d.policy.publishDebounce)
}

func (d *domainMemoryPool) flushPendingHotAdds(pending map[string]struct{}) {
	rules := hotRuleSetToSlice(pending)
	if len(rules) == 0 {
		atomicStoreInt64(&d.hotPendingCount, 0)
		return
	}
	target := d.publishTarget()
	if err := coremain.DispatchHotRulesAdd(d.snapshotter, target, rules); err != nil {
		d.recordHotDispatchFailure("add", target, err)
		d.hotNeedsReplace.Store(true)
		return
	}
	clearHotRules(pending)
	atomicStoreInt64(&d.hotPendingCount, 0)
	atomic.AddInt64(&d.hotAddTotal, int64(len(rules)))
	atomic.StoreInt64(&d.lastHotSyncAtUnixMS, time.Now().UTC().UnixMilli())
	atomic.StoreInt64(&d.publishedCount, int64(d.addActiveHotRules(rules)))
}

func (d *domainMemoryPool) dispatchHotReplace(rules []string) error {
	target := d.publishTarget()
	if err := coremain.DispatchHotRulesReplace(d.snapshotter, target, rules); err != nil {
		d.recordHotDispatchFailure("replace", target, err)
		d.hotNeedsReplace.Store(true)
		return err
	}
	d.hotNeedsReplace.Store(false)
	atomic.AddInt64(&d.hotReplaceTotal, int64(len(rules)))
	atomic.StoreInt64(&d.lastHotSyncAtUnixMS, time.Now().UTC().UnixMilli())
	atomic.StoreInt64(&d.publishedCount, int64(d.replaceActiveHotRules(rules)))
	return nil
}

func (d *domainMemoryPool) recordHotDispatchFailure(action, target string, err error) {
	atomic.AddInt64(&d.hotDispatchFailTotal, 1)
	if d.logger == nil {
		return
	}
	d.logger.Warn(
		"domain_memory_pool hot publish failed",
		zap.String("plugin", d.pluginTag),
		zap.String("action", action),
		zap.String("publish_to", target),
		zap.Error(err),
	)
}

func (d *domainMemoryPool) stopHotPublisher() error {
	if d.hotCmdChan == nil {
		return nil
	}
	resp := make(chan error, 1)
	d.hotCmdChan <- hotPublishRequest{mode: hotPublishStop, resp: resp}
	err := <-resp
	<-d.hotDoneChan
	d.hotCmdChan = nil
	d.hotDoneChan = nil
	return err
}

func resetHotPublishTimer(timer *time.Timer, wait time.Duration) (*time.Timer, <-chan time.Time) {
	timer, _ = stopHotPublishTimer(timer)
	timer = time.NewTimer(wait)
	return timer, timer.C
}

func stopHotPublishTimer(timer *time.Timer) (*time.Timer, <-chan time.Time) {
	if timer == nil {
		return nil, nil
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	return nil, nil
}

func timerChannel(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

func clearHotRules(rules map[string]struct{}) {
	for rule := range rules {
		delete(rules, rule)
	}
}

func hotRuleSetToSlice(rules map[string]struct{}) []string {
	items := make([]string, 0, len(rules))
	for rule := range rules {
		items = append(items, rule)
	}
	return normalizePoolHotRules(items)
}

func atomicStoreInt64(target *int64, value int64) {
	atomic.StoreInt64(target, value)
}
