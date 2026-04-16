package fastforward

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"go.uber.org/zap"
)

func cloneArgs(src *Args) *Args {
	if src == nil {
		return &Args{}
	}
	dst := *src
	if src.Upstreams != nil {
		dst.Upstreams = append([]UpstreamConfig(nil), src.Upstreams...)
	}
	return &dst
}

func buildEffectiveArgs(base *Args) *Args {
	return cloneArgs(base)
}

func (f *Forward) snapshotRuntime() (*Args, []*upstreamWrapper) {
	f.runtimeMu.RLock()
	defer f.runtimeMu.RUnlock()
	return f.args, append([]*upstreamWrapper(nil), f.us...)
}

func (f *Forward) snapshotRuntimeByTags(tags []string) (*Args, []*upstreamWrapper, error) {
	f.runtimeMu.RLock()
	defer f.runtimeMu.RUnlock()

	us := make([]*upstreamWrapper, 0, len(tags))
	for _, tag := range tags {
		u := f.tag2Upstream[tag]
		if u == nil {
			return nil, nil, fmt.Errorf("cannot find upstream by tag %s", tag)
		}
		us = append(us, u)
	}
	return f.args, us, nil
}

func (f *Forward) ReloadControlConfig(_ *coremain.GlobalOverrides, _ []coremain.UpstreamOverrideConfig) error {
	f.runtimeMu.RLock()
	base := cloneArgs(f.baseArgs)
	metricsTag := f.metricsTag
	controlDBPath := f.controlDBPath
	f.runtimeMu.RUnlock()

	if err := f.flushPersistentStats(); err != nil {
		return err
	}
	rebuilt, err := NewForward(buildEffectiveArgs(base), Opts{Logger: f.logger, MetricsTag: metricsTag})
	if err != nil {
		return err
	}
	f.bindStatsCallbacks(rebuilt.us)
	if err := f.restorePersistentStats(rebuilt.us); err != nil {
		_ = rebuilt.Close()
		return err
	}

	f.runtimeMu.Lock()
	oldUs := f.us
	f.args = rebuilt.args
	f.us = rebuilt.us
	f.tag2Upstream = rebuilt.tag2Upstream
	f.controlDBPath = controlDBPath
	f.runtimeMu.Unlock()

	go closeUpstreamsLater(oldUs, 2*time.Second)
	return nil
}

func closeUpstreamsLater(us []*upstreamWrapper, delay time.Duration) {
	time.Sleep(delay)
	for _, u := range us {
		if err := u.Close(); err != nil {
			mlog.L().Warn("failed to close old upstream", zap.Error(err))
		}
	}
}

func (f *Forward) Exec(ctx context.Context, qCtx *query_context.Context) error {
	args, us := f.snapshotRuntime()
	resp, err := f.exchange(ctx, qCtx, args, us)
	if err != nil {
		return err
	}
	qCtx.SetResponse(resp)
	return nil
}

func (f *Forward) QuickConfigureExec(args string) (any, error) {
	selectedTags := strings.Fields(args)
	return sequence.ExecutableFunc(func(ctx context.Context, qCtx *query_context.Context) error {
		runtimeArgs, us, err := f.runtimeSelection(selectedTags)
		if err != nil {
			return err
		}
		resp, err := f.exchange(ctx, qCtx, runtimeArgs, us)
		if err != nil {
			return err
		}
		qCtx.SetResponse(resp)
		return nil
	}), nil
}

func (f *Forward) runtimeSelection(tags []string) (*Args, []*upstreamWrapper, error) {
	if len(tags) == 0 {
		args, us := f.snapshotRuntime()
		return args, us, nil
	}
	return f.snapshotRuntimeByTags(tags)
}

func (f *Forward) Close() error {
	var firstErr error
	if err := f.closeStatsFlusher(); err != nil {
		firstErr = err
	}
	f.runtimeMu.RLock()
	us := append([]*upstreamWrapper(nil), f.us...)
	f.runtimeMu.RUnlock()
	for _, u := range us {
		_ = u.Close()
	}
	return firstErr
}

func (f *Forward) SnapshotUpstreamHealth() []coremain.UpstreamHealthSnapshot {
	_, us := f.snapshotRuntime()
	now := time.Now()
	items := make([]coremain.UpstreamHealthSnapshot, 0, len(us))
	for _, u := range us {
		items = append(items, u.snapshotHealth(f.pluginTag, now))
	}
	return items
}

func (f *Forward) SnapshotControlUpstreams() (string, []coremain.UpstreamOverrideConfig) {
	args, _ := f.snapshotRuntime()
	items := make([]coremain.UpstreamOverrideConfig, 0, len(args.Upstreams))
	for _, upstream := range args.Upstreams {
		items = append(items, coremain.UpstreamOverrideConfig{
			Tag:                  upstream.Tag,
			Enabled:              true,
			Protocol:             coremain.UpstreamProtocolFromAddr(upstream.Addr),
			Addr:                 upstream.Addr,
			DialAddr:             upstream.DialAddr,
			IdleTimeout:          upstream.IdleTimeout,
			UpstreamQueryTimeout: upstream.UpstreamQueryTimeout,
			EnablePipeline:       upstream.EnablePipeline,
			EnableHTTP3:          upstream.EnableHTTP3,
			InsecureSkipVerify:   upstream.InsecureSkipVerify,
			Socks5:               upstream.Socks5,
			SoMark:               upstream.SoMark,
			BindToDevice:         upstream.BindToDevice,
			Bootstrap:            upstream.Bootstrap,
			BootstrapVer:         upstream.BootstrapVer,
		})
	}
	return f.pluginTag, items
}

func quickSetup(bq sequence.BQ, raw string) (any, error) {
	args := &Args{Concurrent: maxConcurrentQueries}
	for _, endpoint := range strings.Fields(raw) {
		args.Upstreams = append(args.Upstreams, UpstreamConfig{Addr: endpoint})
	}
	return NewForward(args, Opts{Logger: bq.L()})
}
