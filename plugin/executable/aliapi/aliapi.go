/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 */

package aliapi

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/upstream"
	"github.com/IrineSistiana/mosdns/v5/pkg/utils"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

const PluginType = "aliapi"

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
	sequence.MustRegExecQuickSetup(PluginType, quickSetup)
}

const (
	maxConcurrentQueries = 20
	queryTimeout         = time.Second * 5
	defaultAliAPIServer  = "223.5.5.5"
)

var aliAPIFakeIPPrefixes = mustParseAliAPIFakeIPPrefixes(
	"28.0.0.0/8",
	"30.0.0.0/8",
	"f2b0::/18",
	"2400::1/64",
)

// Args defines the configuration for the aliapi plugin.
type Args struct {
	Upstreams                   []UpstreamConfig `yaml:"upstreams"`
	Concurrent                  int              `yaml:"concurrent"`
	FailureSuppressTTL          int              `yaml:"failure_suppress_ttl"`
	PersistentServfailThreshold int              `yaml:"persistent_servfail_threshold"`
	PersistentServfailTTL       int              `yaml:"persistent_servfail_ttl"`
	UpstreamFailureThreshold    int              `yaml:"upstream_failure_threshold"`
	UpstreamCircuitBreakSeconds int              `yaml:"upstream_circuit_break_seconds"`

	// AliDNS API legacy fallback options. New configs should prefer per-upstream
	// credentials in UpstreamConfig. These globals are only used to expand old
	// plugin-level configs into each aliapi upstream at runtime.
	AccountID       string `yaml:"account_id"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	ServerAddr      string `yaml:"server_addr"`
	EcsClientIP     string `yaml:"ecs_client_ip"`
	EcsClientMask   uint8  `yaml:"ecs_client_mask"`

	// Global options for standard DNS upstreams.
	Socks5       string `yaml:"socks5"`
	SoMark       int    `yaml:"so_mark"`
	BindToDevice string `yaml:"bind_to_device"`
	Bootstrap    string `yaml:"bootstrap"`
	BootstrapVer int    `yaml:"bootstrap_version"`
}

// UpstreamConfig defines a single upstream server configuration.
type UpstreamConfig struct {
	Tag                  string `yaml:"tag"`
	Addr                 string `yaml:"addr"`
	DialAddr             string `yaml:"dial_addr"`
	IdleTimeout          int    `yaml:"idle_timeout"`
	UpstreamQueryTimeout int    `yaml:"upstream_query_timeout"`

	Type string `yaml:"type"` // "dns" (default) or "aliapi"

	AccountID       string `yaml:"account_id"`
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	ServerAddr      string `yaml:"server_addr"`
	EcsClientIP     string `yaml:"ecs_client_ip"`
	EcsClientMask   uint8  `yaml:"ecs_client_mask"`

	// Deprecated: This option has no affect.
	// TODO: (v6) Remove this option.
	MaxConns           int  `yaml:"max_conns"`
	EnablePipeline     bool `yaml:"enable_pipeline"`
	EnableHTTP3        bool `yaml:"enable_http3"`
	InsecureSkipVerify bool `yaml:"insecure_skip_verify"`

	Socks5       string `yaml:"socks5"`
	SoMark       int    `yaml:"so_mark"`
	BindToDevice string `yaml:"bind_to_device"`
	Bootstrap    string `yaml:"bootstrap"`
	BootstrapVer int    `yaml:"bootstrap_version"`
}

func Init(bp *coremain.BP, args any) (any, error) {
	baseArgs := cloneArgs(args.(*Args))
	if rawArgs, ok := bp.RawArgs().(*Args); ok && rawArgs != nil {
		baseArgs = cloneArgs(rawArgs)
	}
	effectiveArgs := buildEffectiveArgs(bp.Tag(), baseArgs, coremain.GetUpstreamOverrides(bp.Tag()), bp.L())

	f, err := NewAliAPI(effectiveArgs, Opts{Logger: bp.L(), MetricsTag: bp.Tag()})
	if err != nil {
		return nil, err
	}
	f.baseArgs = baseArgs
	f.pluginTag = bp.Tag()
	f.metricsTag = bp.Tag()
	if err := f.configureStatsPersistence(bp.ControlDBPath()); err != nil {
		_ = f.Close()
		return nil, err
	}

	if err := f.RegisterMetricsTo(prometheus.WrapRegistererWithPrefix(PluginType+"_", bp.MetricsRegisterer())); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

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

func buildEffectiveArgs(pluginTag string, base *Args, overrides []coremain.UpstreamOverrideConfig, logger *zap.Logger) *Args {
	a := cloneArgs(base)

	enabledUpstreams := make([]UpstreamConfig, 0, len(overrides))
	enabledCount := 0

	for _, o := range overrides {
		if !o.Enabled {
			continue
		}
		u := UpstreamConfig{
			Tag: o.Tag, Addr: o.Addr, DialAddr: o.DialAddr,
			IdleTimeout: o.IdleTimeout, UpstreamQueryTimeout: o.UpstreamQueryTimeout,
			EnablePipeline: o.EnablePipeline, EnableHTTP3: o.EnableHTTP3,
			InsecureSkipVerify: o.InsecureSkipVerify, Socks5: o.Socks5,
			SoMark: o.SoMark, BindToDevice: o.BindToDevice,
			Bootstrap: o.Bootstrap, BootstrapVer: o.BootstrapVer,
		}

		if o.Protocol == "aliapi" {
			u.Type = "aliapi"
			u.AccountID = o.AccountID
			u.AccessKeyID = o.AccessKeyID
			u.AccessKeySecret = o.AccessKeySecret
			u.ServerAddr = o.ServerAddr
			u.EcsClientIP = o.EcsClientIP
			u.EcsClientMask = o.EcsClientMask
		} else {
			u.Type = "dns"
		}

		enabledUpstreams = append(enabledUpstreams, u)
		enabledCount++
	}

	if len(enabledUpstreams) > 0 {
		a.Upstreams = enabledUpstreams
		conc := enabledCount
		if conc > maxConcurrentQueries {
			conc = maxConcurrentQueries
		}
		if conc < 1 {
			conc = 1
		}
		a.Concurrent = conc
		if logger != nil {
			logger.Info("[Debug AliAPI] Configuration replaced by runtime overrides",
				zap.String("tag", pluginTag),
				zap.Int("active_upstreams", enabledCount),
				zap.Int("new_concurrent", a.Concurrent))
		}
		return a
	}

	if logger != nil {
		logger.Info("[Debug AliAPI] No enabled upstream overrides, using base config",
			zap.String("tag", pluginTag))
	}
	return a
}

var _ sequence.Executable = (*AliAPI)(nil)
var _ sequence.QuickConfigurableExec = (*AliAPI)(nil)
var _ coremain.ControlConfigReloader = (*AliAPI)(nil)
var _ coremain.UpstreamStatsResetter = (*AliAPI)(nil)

// AliAPI represents the aliapi plugin instance.
type AliAPI struct {
	runtimeMu     sync.RWMutex
	args          *Args
	baseArgs      *Args
	pluginTag     string
	metricsTag    string
	controlDBPath string

	logger       *zap.Logger
	statsFlusher *coremain.UpstreamRuntimeStatsFlusher
	us           []*upstreamWrapper
	tag2Upstream map[string]*upstreamWrapper
	failureMu    sync.Mutex
	failures     map[string]failureRecord
}

type failureRecord struct {
	rcode     int
	expiresAt time.Time
	hits      uint32
	lastSeen  time.Time
}

type Opts struct {
	Logger     *zap.Logger
	MetricsTag string
}

// NewAliAPI inits a AliAPI from given args.
// args must contain at least one upstream.
func NewAliAPI(args *Args, opt Opts) (*AliAPI, error) {
	args = materializeRuntimeArgs(args, opt.Logger)
	if len(args.Upstreams) == 0 {
		return nil, errors.New("no upstream is configured")
	}
	if opt.Logger == nil {
		opt.Logger = zap.NewNop()
	}

	if args.ServerAddr == "" {
		args.ServerAddr = defaultAliAPIServer
	}

	f := &AliAPI{
		args:         args,
		logger:       opt.Logger,
		tag2Upstream: make(map[string]*upstreamWrapper),
		failures:     make(map[string]failureRecord),
	}
	if args.FailureSuppressTTL <= 0 {
		args.FailureSuppressTTL = 10
	}
	if args.PersistentServfailThreshold <= 0 {
		args.PersistentServfailThreshold = 3
	}
	if args.PersistentServfailTTL <= 0 {
		args.PersistentServfailTTL = 60
	}
	if args.UpstreamFailureThreshold <= 0 {
		args.UpstreamFailureThreshold = 3
	}
	if args.UpstreamCircuitBreakSeconds <= 0 {
		args.UpstreamCircuitBreakSeconds = 60
	}

	applyGlobal := func(c *UpstreamConfig) {
		utils.SetDefaultString(&c.Socks5, args.Socks5)
		utils.SetDefaultUnsignNum(&c.SoMark, args.SoMark)
		utils.SetDefaultString(&c.BindToDevice, args.BindToDevice)
		utils.SetDefaultString(&c.Bootstrap, args.Bootstrap)
		utils.SetDefaultUnsignNum(&c.BootstrapVer, args.BootstrapVer)
		utils.SetDefaultString(&c.Type, "dns")
	}

	for i, c := range args.Upstreams {
		applyGlobal(&c)

		uw := newWrapper(i, c, opt.MetricsTag)
		var u upstream.Upstream
		var err error

		if c.Type == "aliapi" {
			if c.AccountID == "" || c.AccessKeyID == "" || c.AccessKeySecret == "" {
				return nil, fmt.Errorf("aliapi upstream %q requires account_id, access_key_id, and access_key_secret", c.Tag)
			}
			aliAPIArgs := AliAPIUpstreamArgs{
				AccountID:       c.AccountID,
				AccessKeyID:     c.AccessKeyID,
				AccessKeySecret: c.AccessKeySecret,
				ServerAddr:      c.ServerAddr,
				EcsClientIP:     c.EcsClientIP,
				EcsClientMask:   c.EcsClientMask,
			}
			u = NewAliAPIUpstream(aliAPIArgs, opt.Logger)
		} else {
			if len(c.Addr) == 0 {
				return nil, fmt.Errorf("#%d upstream invalid args, addr is required for type 'dns'", i)
			}
			uOpt := upstream.Opt{
				DialAddr:       c.DialAddr,
				Socks5:         c.Socks5,
				SoMark:         c.SoMark,
				BindToDevice:   c.BindToDevice,
				IdleTimeout:    time.Duration(c.IdleTimeout) * time.Second,
				EnablePipeline: c.EnablePipeline,
				EnableHTTP3:    c.EnableHTTP3,
				Bootstrap:      c.Bootstrap,
				BootstrapVer:   c.BootstrapVer,
				TLSConfig: &tls.Config{
					InsecureSkipVerify: c.InsecureSkipVerify,
					ClientSessionCache: tls.NewLRUClientSessionCache(4),
				},
				Logger:        opt.Logger,
				EventObserver: uw,
			}
			u, err = upstream.NewUpstream(c.Addr, uOpt)
			if err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("failed to init upstream #%d: %w", i, err)
			}
		}

		uw.u = u
		f.us = append(f.us, uw)

		if len(c.Tag) > 0 {
			if _, dup := f.tag2Upstream[c.Tag]; dup {
				_ = f.Close()
				return nil, fmt.Errorf("duplicated upstream tag %s", c.Tag)
			}
			f.tag2Upstream[c.Tag] = uw
		}
	}

	return f, nil
}

func (f *AliAPI) RegisterMetricsTo(r prometheus.Registerer) error {
	for _, wu := range f.us {
		if len(wu.cfg.Tag) == 0 {
			continue
		}
		if err := wu.registerMetricsTo(r); err != nil {
			return err
		}
	}
	return nil
}

func (f *AliAPI) snapshotRuntime() (*Args, []*upstreamWrapper) {
	f.runtimeMu.RLock()
	defer f.runtimeMu.RUnlock()
	return f.args, append([]*upstreamWrapper(nil), f.us...)
}

func (f *AliAPI) snapshotRuntimeByTags(tags []string) (*Args, []*upstreamWrapper, error) {
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

func (f *AliAPI) ReloadControlConfig(_ *coremain.GlobalOverrides, upstreams []coremain.UpstreamOverrideConfig) error {
	f.runtimeMu.RLock()
	base := cloneArgs(f.baseArgs)
	pluginTag := f.pluginTag
	metricsTag := f.metricsTag
	f.runtimeMu.RUnlock()

	if err := f.flushPersistentStats(); err != nil {
		return err
	}
	effective := buildEffectiveArgs(pluginTag, base, upstreams, f.logger)
	rebuilt, err := NewAliAPI(effective, Opts{Logger: f.logger, MetricsTag: metricsTag})
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
	f.runtimeMu.Unlock()

	f.failureMu.Lock()
	f.failures = make(map[string]failureRecord)
	f.failureMu.Unlock()

	go func(old []*upstreamWrapper) {
		time.Sleep(2 * time.Second)
		for _, u := range old {
			_ = u.Close()
		}
	}(oldUs)

	return nil
}

func (f *AliAPI) Exec(ctx context.Context, qCtx *query_context.Context) (err error) {
	args, us := f.snapshotRuntime()
	r, err := f.exchange(ctx, qCtx, args, us)
	if err != nil {
		return err
	}
	qCtx.SetResponse(r)
	return nil
}

func (f *AliAPI) QuickConfigureExec(args string) (any, error) {
	selectedTags := strings.Fields(args)
	var execFunc sequence.ExecutableFunc = func(ctx context.Context, qCtx *query_context.Context) error {
		var (
			runtimeArgs *Args
			us          []*upstreamWrapper
			err         error
		)
		if len(selectedTags) == 0 {
			runtimeArgs, us = f.snapshotRuntime()
		} else {
			runtimeArgs, us, err = f.snapshotRuntimeByTags(selectedTags)
			if err != nil {
				return err
			}
		}
		r, err := f.exchange(ctx, qCtx, runtimeArgs, us)
		if err != nil {
			return err
		}
		qCtx.SetResponse(r)
		return nil
	}
	return execFunc, nil
}

func (f *AliAPI) Close() error {
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

func (f *AliAPI) SnapshotUpstreamHealth() []coremain.UpstreamHealthSnapshot {
	_, us := f.snapshotRuntime()
	now := time.Now()
	items := make([]coremain.UpstreamHealthSnapshot, 0, len(us))
	for _, u := range us {
		items = append(items, u.snapshotHealth(f.pluginTag, now))
	}
	return items
}

func (f *AliAPI) SnapshotControlUpstreams() (string, []coremain.UpstreamOverrideConfig) {
	f.runtimeMu.RLock()
	pluginTag := f.pluginTag
	us := append([]*upstreamWrapper(nil), f.us...)
	f.runtimeMu.RUnlock()

	items := make([]coremain.UpstreamOverrideConfig, 0, len(us))
	for _, wrapper := range us {
		upstream := wrapper.cfg
		protocol := coremain.UpstreamProtocolFromAddr(upstream.Addr)
		item := coremain.UpstreamOverrideConfig{
			Tag:                  upstream.Tag,
			Enabled:              true,
			Protocol:             protocol,
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
		}
		if upstream.Type == "aliapi" {
			item.Protocol = "aliapi"
			item.AccountID = upstream.AccountID
			item.AccessKeyID = upstream.AccessKeyID
			item.AccessKeySecret = upstream.AccessKeySecret
			item.ServerAddr = upstream.ServerAddr
			item.EcsClientIP = upstream.EcsClientIP
			item.EcsClientMask = upstream.EcsClientMask
		}
		items = append(items, item)
	}
	return pluginTag, items
}

func (f *AliAPI) exchange(ctx context.Context, qCtx *query_context.Context, runtimeArgs *Args, us []*upstreamWrapper) (*dns.Msg, error) {
	if len(us) == 0 {
		return nil, errors.New("no upstream to exchange")
	}

	question := qCtx.QQuestion()
	failureKey := buildFailureKey(question)
	if cachedFailure, ok := f.getFailure(failureKey, runtimeArgs); ok {
		return newSyntheticFailureResponse(qCtx.Q(), cachedFailure.rcode), nil
	}

	queryPayload, err := pool.PackBuffer(qCtx.Q())
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseBuf(queryPayload)

	concurrent := runtimeArgs.Concurrent
	if concurrent <= 0 {
		concurrent = 1
	}
	if concurrent > maxConcurrentQueries {
		concurrent = maxConcurrentQueries
	}
	if concurrent > len(us) {
		concurrent = len(us)
	}

	type res struct {
		r   *dns.Msg
		err error
		u   *upstreamWrapper
	}

	resChan := make(chan res, concurrent)

	var lastSuccessOrNXRes *dns.Msg
	var lastSuccessOrNXResUpstream *upstreamWrapper
	var lastOtherRes *dns.Msg
	var lastOtherResUpstream *upstreamWrapper
	var lastError error

	availableUpstreams := filterAvailableUpstreams(us)
	if len(availableUpstreams) == 0 {
		availableUpstreams = us
	}
	if concurrent > len(availableUpstreams) {
		concurrent = len(availableUpstreams)
	}
	randIndex := rand.Intn(len(availableUpstreams))

	usToQuery := make([]*upstreamWrapper, 0, concurrent)
	for i := 0; i < concurrent; i++ {
		usToQuery = append(usToQuery, availableUpstreams[(randIndex+i)%len(availableUpstreams)])
	}

	if len(usToQuery) == 1 {
		u := usToQuery[0]
		r, err := f.exchangeOne(ctx, queryPayload, u)
		if err != nil {
			u.recordFailure(time.Now(), runtimeArgs.UpstreamFailureThreshold, time.Duration(runtimeArgs.UpstreamCircuitBreakSeconds)*time.Second)
			f.putFailure(failureKey, dns.RcodeServerFailure, runtimeArgs)
			return newSyntheticFailureResponse(qCtx.Q(), dns.RcodeServerFailure), nil
		}

		u.recordSuccess()
		sanitizeFakeIPAuthenticatedData(r)
		switch {
		case hasUsableAnswer(r):
			f.clearFailure(failureKey)
			u.recordWinner()
			coremain.SetAuditUpstreamTag(qCtx, u.name())
			return r, nil
		case r.Rcode == dns.RcodeSuccess || r.Rcode == dns.RcodeNameError:
			f.clearFailure(failureKey)
			u.recordWinner()
			coremain.SetAuditUpstreamTag(qCtx, u.name())
			return r, nil
		default:
			if r.Rcode == dns.RcodeServerFailure {
				f.putFailure(failureKey, dns.RcodeServerFailure, runtimeArgs)
			}
			u.recordWinner()
			coremain.SetAuditUpstreamTag(qCtx, u.name())
			return r, nil
		}
	}

	processResult := func(r *dns.Msg, err error, u *upstreamWrapper) (*dns.Msg, bool) {
		if err != nil {
			u.recordFailure(time.Now(), runtimeArgs.UpstreamFailureThreshold, time.Duration(runtimeArgs.UpstreamCircuitBreakSeconds)*time.Second)
			if lastError == nil {
				lastError = err
			}
			return nil, false
		}
		u.recordSuccess()
		sanitizeFakeIPAuthenticatedData(r)

		if hasUsableAnswer(r) {
			f.clearFailure(failureKey)
			u.recordWinner()
			coremain.SetAuditUpstreamTag(qCtx, u.name())
			return r, true
		}

		if r.Rcode == dns.RcodeSuccess || r.Rcode == dns.RcodeNameError {
			f.clearFailure(failureKey)
			if lastSuccessOrNXRes == nil {
				lastSuccessOrNXRes = r
				lastSuccessOrNXResUpstream = u
			}
		} else {
			if r.Rcode == dns.RcodeServerFailure {
				f.putFailure(failureKey, dns.RcodeServerFailure, runtimeArgs)
			}
			if lastOtherRes == nil {
				lastOtherRes = r
				lastOtherResUpstream = u
			}
		}
		return nil, false
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	defer workerCancel()

	startWorker := func(currentUpstream *upstreamWrapper) {
		qc := copyPayload(queryPayload)
		go func() {
			defer pool.ReleaseBuf(qc)

			r, err := f.exchangeOne(workerCtx, qc, currentUpstream)
			select {
			case resChan <- res{r: r, err: err, u: currentUpstream}:
			case <-workerCtx.Done():
			}
		}()
	}

	for _, u := range usToQuery {
		startWorker(u)
	}

	for i := 0; i < len(usToQuery); i++ {
		select {
		case res := <-resChan:
			if r, done := processResult(res.r, res.err, res.u); done {
				workerCancel()
				return r, nil
			}

		case <-ctx.Done():
			workerCancel()
			if lastSuccessOrNXRes != nil {
				lastSuccessOrNXResUpstream.recordWinner()
				coremain.SetAuditUpstreamTag(qCtx, lastSuccessOrNXResUpstream.name())
				return lastSuccessOrNXRes, nil
			}
			if lastOtherRes != nil {
				lastOtherResUpstream.recordWinner()
				coremain.SetAuditUpstreamTag(qCtx, lastOtherResUpstream.name())
				return lastOtherRes, nil
			}
			if lastError != nil {
				return nil, lastError
			}
			return nil, context.Cause(ctx)
		}
	}

	if lastSuccessOrNXRes != nil {
		f.clearFailure(failureKey)
		lastSuccessOrNXResUpstream.recordWinner()
		coremain.SetAuditUpstreamTag(qCtx, lastSuccessOrNXResUpstream.name())
		return lastSuccessOrNXRes, nil
	}
	if lastOtherRes != nil {
		if lastOtherRes.Rcode == dns.RcodeServerFailure {
			f.putFailure(failureKey, dns.RcodeServerFailure, runtimeArgs)
		}
		lastOtherResUpstream.recordWinner()
		coremain.SetAuditUpstreamTag(qCtx, lastOtherResUpstream.name())
		return lastOtherRes, nil
	}
	if lastError != nil {
		f.putFailure(failureKey, dns.RcodeServerFailure, runtimeArgs)
		return newSyntheticFailureResponse(qCtx.Q(), dns.RcodeServerFailure), nil
	}

	return nil, errors.New("all upstreams failed or returned no usable response")
}

func hasUsableAnswer(r *dns.Msg) bool {
	if r == nil || len(r.Answer) == 0 {
		return false
	}
	for _, ans := range r.Answer {
		if a, ok := ans.(*dns.A); ok && len(a.A) > 0 {
			return true
		}
		if aaaa, ok := ans.(*dns.AAAA); ok && len(aaaa.AAAA) > 0 {
			return true
		}
	}
	return false
}

func mustParseAliAPIFakeIPPrefixes(raw ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(raw))
	for _, item := range raw {
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			panic(err)
		}
		out = append(out, prefix)
	}
	return out
}

func sanitizeFakeIPAuthenticatedData(r *dns.Msg) {
	if r == nil || !r.AuthenticatedData || len(r.Answer) == 0 {
		return
	}
	for _, answer := range r.Answer {
		switch rr := answer.(type) {
		case *dns.A:
			addr, ok := netip.AddrFromSlice(rr.A)
			if ok && isAliAPIFakeIPAddr(addr) {
				r.AuthenticatedData = false
				return
			}
		case *dns.AAAA:
			addr, ok := netip.AddrFromSlice(rr.AAAA)
			if ok && isAliAPIFakeIPAddr(addr) {
				r.AuthenticatedData = false
				return
			}
		}
	}
}

func isAliAPIFakeIPAddr(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range aliAPIFakeIPPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (f *AliAPI) exchangeOne(parent context.Context, queryPayload *[]byte, u *upstreamWrapper) (*dns.Msg, error) {
	upstreamTimeout := time.Duration(u.cfg.UpstreamQueryTimeout) * time.Millisecond
	if upstreamTimeout == 0 {
		upstreamTimeout = queryTimeout
	}

	upstreamCtx, upstreamCancel := context.WithTimeout(parent, upstreamTimeout)
	defer upstreamCancel()

	respPayload, err := u.ExchangeContext(upstreamCtx, *queryPayload)
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) &&
			!strings.Contains(err.Error(), "connection refused") &&
			!strings.Contains(err.Error(), "no such host") {
			f.logger.Debug("upstream query failed", zap.String("upstream", u.cfg.Addr), zap.Error(err))
		}
		return nil, err
	}
	defer pool.ReleaseBuf(respPayload)

	r := new(dns.Msg)
	if err := r.Unpack(*respPayload); err != nil {
		f.logger.Debug("failed to unpack DNS response", zap.String("upstream", u.cfg.Addr), zap.Error(err))
		return nil, err
	}
	return r, nil
}

func buildFailureKey(q dns.Question) string {
	return strings.ToLower(dns.Fqdn(q.Name)) + "|" + strconv.Itoa(int(q.Qclass)) + "|" + strconv.Itoa(int(q.Qtype))
}

func newSyntheticFailureResponse(q *dns.Msg, rcode int) *dns.Msg {
	r := new(dns.Msg)
	r.SetRcode(q, rcode)
	r.RecursionAvailable = true
	return r
}

func filterAvailableUpstreams(us []*upstreamWrapper) []*upstreamWrapper {
	now := time.Now()
	available := make([]*upstreamWrapper, 0, len(us))
	for _, u := range us {
		if !u.isCircuitOpen(now) {
			available = append(available, u)
			continue
		}
		u.mCircuitSkipTotal.Inc()
	}
	return available
}

func (f *AliAPI) getFailure(key string, runtimeArgs *Args) (failureRecord, bool) {
	f.failureMu.Lock()
	defer f.failureMu.Unlock()
	rec, ok := f.failures[key]
	if !ok {
		return failureRecord{}, false
	}
	now := time.Now()
	if now.After(rec.expiresAt) {
		retention := time.Duration(max(runtimeArgs.PersistentServfailTTL, runtimeArgs.FailureSuppressTTL*3)) * time.Second
		if rec.lastSeen.IsZero() || now.Sub(rec.lastSeen) > retention {
			delete(f.failures, key)
		}
		return failureRecord{}, false
	}
	return rec, true
}

func (f *AliAPI) putFailure(key string, rcode int, runtimeArgs *Args) {
	if runtimeArgs.FailureSuppressTTL <= 0 {
		return
	}
	now := time.Now()
	baseTTL := time.Duration(runtimeArgs.FailureSuppressTTL) * time.Second
	persistentTTL := time.Duration(runtimeArgs.PersistentServfailTTL) * time.Second
	if persistentTTL < baseTTL {
		persistentTTL = baseTTL
	}
	accumulationWindow := persistentTTL
	if accumulationWindow <= 0 {
		accumulationWindow = baseTTL
	}

	f.failureMu.Lock()
	rec := f.failures[key]
	if rec.rcode == rcode && !rec.lastSeen.IsZero() && now.Sub(rec.lastSeen) <= accumulationWindow {
		rec.hits++
	} else {
		rec.hits = 1
	}
	rec.rcode = rcode
	rec.lastSeen = now
	ttl := baseTTL
	if rcode == dns.RcodeServerFailure && rec.hits >= uint32(runtimeArgs.PersistentServfailThreshold) {
		ttl = persistentTTL
	}
	rec.expiresAt = now.Add(ttl)
	f.failures[key] = rec
	f.failureMu.Unlock()
}

func (f *AliAPI) clearFailure(key string) {
	f.failureMu.Lock()
	delete(f.failures, key)
	f.failureMu.Unlock()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func quickSetup(bq sequence.BQ, s string) (any, error) {
	args := new(Args)
	args.Concurrent = maxConcurrentQueries
	for _, u := range strings.Fields(s) {
		args.Upstreams = append(args.Upstreams, UpstreamConfig{Addr: u, Type: "dns"})
	}
	return NewAliAPI(args, Opts{Logger: bq.L()})
}

// DNSEntity represents the structure of the JSON response from AliDNS API
type DNSEntity struct {
	Status int      `json:"status"`
	TC     bool     `json:"TC"` // Truncated
	RD     bool     `json:"RD"` // Recursion Desired
	RA     bool     `json:"RA"` // Recursion Available
	AD     bool     `json:"AD"` // Authenticated Data
	CD     bool     `json:"CD"` // Checking Disabled
	Answer []Answer `json:"answer"`
	Remark string   `json:"remark"`
}

// Answer represents a single DNS record in the AliDNS API response
type Answer struct {
	Name string `json:"name"`
	Type uint16 `json:"type"`
	TTL  uint32 `json:"TTL"`
	Data string `json:"data"`
}

// getDNSRecord converts a JSON Answer object to a dns.RR
func getDNSRecord(ans Answer) dns.RR {
	header := dns.RR_Header{Name: ans.Name, Rrtype: ans.Type, Class: dns.ClassINET, Ttl: ans.TTL}
	switch ans.Type {
	case dns.TypeA:
		rr := new(dns.A)
		rr.Hdr = header
		rr.A = net.ParseIP(ans.Data)
		return rr
	case dns.TypeNS:
		rr := new(dns.NS)
		rr.Hdr = header
		rr.Ns = ans.Data
		return rr
	case dns.TypeCNAME:
		rr := new(dns.CNAME)
		rr.Hdr = header
		rr.Target = ans.Data
		return rr
	case dns.TypeSOA:
		rr := new(dns.SOA)
		rr.Hdr = header
		data := strings.Fields(ans.Data)
		if len(data) == 7 {
			rr.Ns = data[0]
			rr.Mbox = data[1]
			serial, err1 := strconv.ParseUint(data[2], 10, 32)
			refresh, err2 := strconv.ParseUint(data[3], 10, 32)
			retry, err3 := strconv.ParseUint(data[4], 10, 32)
			expire, err4 := strconv.ParseUint(data[5], 10, 32)
			minttl, err5 := strconv.ParseUint(data[6], 10, 32)
			if err1 != nil || err2 != nil || err3 != nil || err4 != nil || err5 != nil {
				return &dns.TXT{Hdr: header, Txt: []string{"Malformed SOA: invalid number format"}}
			}
			rr.Serial = uint32(serial)
			rr.Refresh = uint32(refresh)
			rr.Retry = uint32(retry)
			rr.Expire = uint32(expire)
			rr.Minttl = uint32(minttl)
		} else {
			return &dns.TXT{Hdr: header, Txt: []string{"Malformed SOA: wrong number of fields"}}
		}
		return rr
	case dns.TypeMX:
		rr := new(dns.MX)
		rr.Hdr = header
		data := strings.Fields(ans.Data)
		if len(data) == 2 {
			pref, err := strconv.ParseUint(data[0], 10, 16)
			if err != nil {
				return &dns.TXT{Hdr: header, Txt: []string{"Malformed MX: invalid preference"}}
			}
			rr.Preference = uint16(pref)
			rr.Mx = data[1]
		} else {
			return &dns.TXT{Hdr: header, Txt: []string{"Malformed MX: wrong number of fields"}}
		}
		return rr
	case dns.TypeTXT:
		rr := new(dns.TXT)
		rr.Hdr = header
		cleanedData := strings.Trim(ans.Data, "\"")
		rr.Txt = []string{cleanedData}
		return rr
	case dns.TypeAAAA:
		rr := new(dns.AAAA)
		rr.Hdr = header
		rr.AAAA = net.ParseIP(ans.Data)
		return rr
	case dns.TypeCAA:
		rr := new(dns.CAA)
		rr.Hdr = header
		data := strings.Fields(ans.Data)
		if len(data) == 3 {
			flag, err := strconv.ParseUint(data[0], 10, 8)
			if err != nil {
				return &dns.TXT{Hdr: header, Txt: []string{"Malformed CAA: invalid flag"}}
			}
			rr.Flag = uint8(flag)
			rr.Tag = data[1]
			rr.Value = strings.Trim(data[2], "\"")
		} else {
			return &dns.TXT{Hdr: header, Txt: []string{"Malformed CAA: wrong number of fields"}}
		}
		return rr
	default:
		rr := new(dns.TXT)
		rr.Hdr = header
		cleanedData := strings.Trim(ans.Data, "\"")
		rr.Txt = []string{fmt.Sprintf("Type %d: %s", ans.Type, cleanedData)}
		return rr
	}
}

// AliAPIUpstreamArgs holds configuration for an AliAPI upstream instance.
type AliAPIUpstreamArgs struct {
	AccountID       string
	AccessKeyID     string
	AccessKeySecret string
	ServerAddr      string
	EcsClientIP     string
	EcsClientMask   uint8
}

// AliAPIUpstream implements the upstream.Upstream interface for AliDNS JSON API.
type AliAPIUpstream struct {
	args   AliAPIUpstreamArgs
	logger *zap.Logger
	client *http.Client
}

// NewAliAPIUpstream creates a new AliAPIUpstream.
func NewAliAPIUpstream(args AliAPIUpstreamArgs, logger *zap.Logger) *AliAPIUpstream {
	httpClient := &http.Client{
		Timeout: queryTimeout,
	}
	return &AliAPIUpstream{
		args:   args,
		logger: logger,
		client: httpClient,
	}
}

// ExchangeContext performs a DNS query via AliDNS JSON API.
func (a *AliAPIUpstream) ExchangeContext(ctx context.Context, req []byte) (resp *[]byte, err error) {
	dnsMsg := new(dns.Msg)
	if err := dnsMsg.Unpack(req); err != nil {
		a.logger.Warn("failed to unpack DNS message for AliAPI", zap.Error(err))
		return nil, fmt.Errorf("failed to unpack DNS message: %w", err)
	}

	if len(dnsMsg.Question) == 0 {
		return nil, errors.New("DNS message has no questions")
	}

	q := dnsMsg.Question[0]
	qName := dns.Fqdn(q.Name)
	qType := dns.Type(q.Qtype).String()

	var ednsClientSubnet string
	if a.args.EcsClientIP != "" && a.args.EcsClientMask > 0 {
		ednsClientSubnet = fmt.Sprintf("%s/%d", a.args.EcsClientIP, a.args.EcsClientMask)
	} else {
		for _, opt := range dnsMsg.Extra {
			if edns0, ok := opt.(*dns.OPT); ok {
				for _, option := range edns0.Option {
					if ecs, ok := option.(*dns.EDNS0_SUBNET); ok {
						ednsClientSubnet = ecs.Address.String() + "/" + strconv.Itoa(int(ecs.SourceNetmask))
						break
					}
				}
			}
			if ednsClientSubnet != "" {
				break
			}
		}
	}

	ts := fmt.Sprintf("%d", time.Now().Unix())
	keyData := a.args.AccountID + a.args.AccessKeySecret + ts + qName + a.args.AccessKeyID
	keyHash := sha256.Sum256([]byte(keyData))
	keyStr := hex.EncodeToString(keyHash[:])

	url := fmt.Sprintf("http://%s/resolve?name=%s&type=%s&uid=%s&ak=%s&key=%s&ts=%s",
		a.args.ServerAddr, qName, qType, a.args.AccountID, a.args.AccessKeyID, keyStr, ts)
	if ednsClientSubnet != "" {
		url = fmt.Sprintf("%s&edns_client_subnet=%s", url, ednsClientSubnet)
	}

	a.logger.Debug("Requesting AliDNS JSON API", zap.String("url", url))
	httpReq, _ := http.NewRequestWithContext(ctx, "GET", url, nil)

	httpResp, httpErr := a.client.Do(httpReq)
	if httpErr != nil {
		a.logger.Debug("AliAPI HTTP request failed", zap.String("url", url), zap.Error(httpErr))
		return nil, fmt.Errorf("AliAPI HTTP request failed: %w", httpErr)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		a.logger.Warn("AliAPI returned non-200 status code",
			zap.Int("status_code", httpResp.StatusCode),
			zap.String("body", string(body)))
		return nil, fmt.Errorf("AliAPI request failed with HTTP status %d", httpResp.StatusCode)
	}

	body, readErr := io.ReadAll(httpResp.Body)
	if readErr != nil {
		a.logger.Warn("Failed to read AliAPI response body", zap.Error(readErr))
		return nil, fmt.Errorf("failed to read AliAPI response body: %w", readErr)
	}

	a.logger.Debug("AliAPI raw response", zap.String("body", string(body)))

	var aliDNSResult DNSEntity
	jsonErr := json.Unmarshal(body, &aliDNSResult)
	if jsonErr != nil {
		a.logger.Warn("Failed to unmarshal AliAPI JSON response", zap.Error(jsonErr), zap.String("body", string(body)))
		return nil, fmt.Errorf("failed to unmarshal AliAPI JSON response: %w", jsonErr)
	}

	// === Start of logic based on AliAPI documentation ===
	responseMsg := new(dns.Msg)
	responseMsg.SetReply(dnsMsg)
	responseMsg.Authoritative = true

	// Directly use the Rcode from the API's "Status" field.
	responseMsg.SetRcode(dnsMsg, aliDNSResult.Status)
	responseMsg.Truncated = aliDNSResult.TC
	responseMsg.RecursionAvailable = aliDNSResult.RA
	responseMsg.AuthenticatedData = aliDNSResult.AD
	responseMsg.CheckingDisabled = aliDNSResult.CD

	// Only populate the Answer section if the status is NOERROR (0).
	if aliDNSResult.Status == dns.RcodeSuccess {
		for _, ans := range aliDNSResult.Answer {
			// Keeping the original logic to only add records matching the question name.
			// if ans.Name == qName {
			record := getDNSRecord(ans)
			responseMsg.Answer = append(responseMsg.Answer, record)
			// }
		}
	} else {
		// Log DNS-level errors (like NXDOMAIN, SERVFAIL) for debugging.
		a.logger.Debug("AliAPI returned a DNS error status",
			zap.Int("status", aliDNSResult.Status),
			zap.String("remark", aliDNSResult.Remark))
	}

	// Pack the DNS message. Whether it's a success, NXDOMAIN, or SERVFAIL,
	// it's a valid DNS response that should be returned to the caller.
	packed, packErr := pool.PackBuffer(responseMsg)
	if packErr != nil {
		return nil, fmt.Errorf("failed to pack DNS response from AliAPI result: %w", packErr)
	}

	// Return the packed message and a nil error. This ensures the caller receives
	// and processes the DNS response, regardless of its Rcode.
	return packed, nil
}

// Close for AliAPIUpstream is a no-op as there are no persistent connections.
func (a *AliAPIUpstream) Close() error {
	return nil
}

// --- EventObserver placeholder for standard upstreams ---
// This is a minimal EventObserver that does nothing. It's used when we
// create standard `upstream.Upstream` instances, because the metrics
// will now be handled directly by `upstreamWrapper.ExchangeContext`.
type nopEO struct{}

func (nopEO) OnEvent(upstream.Event) {}

// upstreamWrapper wraps an upstream.Upstream and collects metrics.
type upstreamWrapper struct {
	u                   upstream.Upstream
	cfg                 UpstreamConfig
	metricsTag          string
	onStatsChanged      func()
	consecutiveFailures atomic.Uint32
	circuitOpenUntil    atomic.Int64
	mInflightValue      atomic.Int64
	ewmaLatencyUs       atomic.Int64
	queryCount          atomic.Uint64
	errorCount          atomic.Uint64
	winnerCount         atomic.Uint64
	latencyTotalUs      atomic.Uint64
	latencyCount        atomic.Uint64

	mQueryTotal       prometheus.Counter
	mErrorTotal       prometheus.Counter
	mWinnerTotal      prometheus.Counter
	mInflight         prometheus.Gauge
	mResponseLatency  prometheus.Histogram
	mConnOpened       prometheus.Counter
	mConnClosed       prometheus.Counter
	mCircuitOpenTotal prometheus.Counter
	mCircuitSkipTotal prometheus.Counter
}

func (w *upstreamWrapper) OnEvent(e upstream.Event) {
	switch e {
	case upstream.EventConnOpen:
		w.mConnOpened.Inc()
	case upstream.EventConnClose:
		w.mConnClosed.Inc()
	}
}

func (w *upstreamWrapper) name() string {
	if w == nil {
		return ""
	}
	if w.cfg.Tag != "" {
		return w.cfg.Tag
	}
	return w.cfg.Addr
}

func newWrapper(idx int, c UpstreamConfig, metricsTag string) *upstreamWrapper {
	return &upstreamWrapper{
		cfg:        c,
		metricsTag: fmt.Sprintf("%s_%d", metricsTag, idx),
		mQueryTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "query_total",
			Help: "Total number of queries.",
			ConstLabels: prometheus.Labels{
				"tag":         c.Tag,
				"addr":        c.Addr,
				"metrics_tag": metricsTag,
			},
		}),
		mErrorTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "error_total",
			Help: "Total number of query errors.",
			ConstLabels: prometheus.Labels{
				"tag":         c.Tag,
				"addr":        c.Addr,
				"metrics_tag": metricsTag,
			},
		}),
		mWinnerTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "upstream_winner_total",
			Help: "Total number of times this upstream result was selected as the final response.",
			ConstLabels: prometheus.Labels{
				"tag":         c.Tag,
				"addr":        c.Addr,
				"metrics_tag": metricsTag,
			},
		}),
		mInflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "inflight_queries",
			Help: "Number of inflight queries.",
			ConstLabels: prometheus.Labels{
				"tag":         c.Tag,
				"addr":        c.Addr,
				"metrics_tag": metricsTag,
			},
		}),
		mResponseLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "response_latency_millisecond",
			Help:    "Response latency in milliseconds.",
			Buckets: []float64{1, 5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000},
			ConstLabels: prometheus.Labels{
				"tag":         c.Tag,
				"addr":        c.Addr,
				"metrics_tag": metricsTag,
			},
		}),
		mConnOpened: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "conn_opened_total",
			Help: "Total number of connections opened.",
			ConstLabels: prometheus.Labels{
				"tag":         c.Tag,
				"addr":        c.Addr,
				"metrics_tag": metricsTag,
			},
		}),
		mConnClosed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "conn_closed_total",
			Help: "Total number of connections closed.",
			ConstLabels: prometheus.Labels{
				"tag":         c.Tag,
				"addr":        c.Addr,
				"metrics_tag": metricsTag,
			},
		}),
		mCircuitOpenTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "circuit_open_total",
			Help: "Total number of times the upstream circuit breaker opened.",
			ConstLabels: prometheus.Labels{
				"tag":         c.Tag,
				"addr":        c.Addr,
				"metrics_tag": metricsTag,
			},
		}),
		mCircuitSkipTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "circuit_skip_total",
			Help: "Total number of times the upstream was skipped because of an open circuit breaker.",
			ConstLabels: prometheus.Labels{
				"tag":         c.Tag,
				"addr":        c.Addr,
				"metrics_tag": metricsTag,
			},
		}),
	}
}

func (w *upstreamWrapper) recordSuccess() {
	w.consecutiveFailures.Store(0)
}

func (w *upstreamWrapper) recordFailure(now time.Time, threshold int, duration time.Duration) {
	failures := w.consecutiveFailures.Add(1)
	if threshold <= 0 || failures < uint32(threshold) {
		return
	}
	openUntil := now.Add(duration).UnixNano()
	prev := w.circuitOpenUntil.Load()
	if openUntil > prev {
		w.circuitOpenUntil.Store(openUntil)
		w.mCircuitOpenTotal.Inc()
	}
}

func (w *upstreamWrapper) isCircuitOpen(now time.Time) bool {
	return now.UnixNano() < w.circuitOpenUntil.Load()
}

// ExchangeContext handles the actual exchange and updates metrics directly.
// This method takes over the responsibility of calling metric updates,
// thus `u` (the wrapped upstream) no longer needs to call them via EventObserver.
func (w *upstreamWrapper) ExchangeContext(ctx context.Context, req []byte) (*[]byte, error) {
	w.mQueryTotal.Inc()
	w.queryCount.Add(1)
	w.mInflight.Inc()
	w.mInflightValue.Add(1)

	start := time.Now()

	resp, err := w.u.ExchangeContext(ctx, req) // Call the wrapped upstream's method

	w.mInflightValue.Add(-1)
	w.mInflight.Dec() // Always decrement inflight after the exchange completes

	if err != nil {
		if !upstream.IsContextCanceled(err) {
			w.mErrorTotal.Inc()
			w.errorCount.Add(1)
		}
	} else {
		latency := time.Since(start)
		w.mResponseLatency.Observe(float64(latency.Milliseconds()))
		w.latencyTotalUs.Add(uint64(latency.Microseconds()))
		w.latencyCount.Add(1)
		w.recordLatency(latency)
	}
	w.notifyStatsChanged()

	return resp, err
}

func (w *upstreamWrapper) healthScore(now time.Time) int64 {
	score := w.ewmaLatencyUs.Load()
	if score <= 0 {
		score = int64(50 * time.Millisecond / time.Microsecond)
	}
	score += w.mInflightValue.Load() * int64(15*time.Millisecond/time.Microsecond)
	score += int64(w.consecutiveFailures.Load()) * int64(200*time.Millisecond/time.Microsecond)
	if w.isCircuitOpen(now) {
		score += int64(5 * time.Second / time.Microsecond)
	}
	return score
}

func (w *upstreamWrapper) recordLatency(latency time.Duration) {
	current := latency.Microseconds()
	if current <= 0 {
		current = 1
	}
	previous := w.ewmaLatencyUs.Load()
	if previous <= 0 {
		w.ewmaLatencyUs.Store(current)
		return
	}
	w.ewmaLatencyUs.Store((previous*7 + current*3) / 10)
}

func (w *upstreamWrapper) Close() error {
	return w.u.Close()
}

func (w *upstreamWrapper) registerMetricsTo(r prometheus.Registerer) error {
	if err := r.Register(w.mQueryTotal); err != nil {
		return err
	}
	if err := r.Register(w.mErrorTotal); err != nil {
		return err
	}
	if err := r.Register(w.mWinnerTotal); err != nil {
		return err
	}
	if err := r.Register(w.mInflight); err != nil {
		return err
	}
	if err := r.Register(w.mResponseLatency); err != nil {
		return err
	}
	if err := r.Register(w.mConnOpened); err != nil {
		return err
	}
	if err := r.Register(w.mConnClosed); err != nil {
		return err
	}
	if err := r.Register(w.mCircuitOpenTotal); err != nil {
		return err
	}
	if err := r.Register(w.mCircuitSkipTotal); err != nil {
		return err
	}
	return nil
}

func copyPayload(p *[]byte) *[]byte {
	buf := pool.GetBuf(len(*p))
	copy(*buf, *p)
	return buf
}
