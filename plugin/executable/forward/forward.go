/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package fastforward

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"sync"
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

const PluginType = "forward"

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
	sequence.MustRegExecQuickSetup(PluginType, quickSetup)
}

const (
	maxConcurrentQueries = 3
	queryTimeout         = time.Second * 5
)

type Args struct {
	Upstreams  []UpstreamConfig `yaml:"upstreams"`
	Concurrent int              `yaml:"concurrent"`

	// Global options.
	Socks5       string `yaml:"socks5"`
	SoMark       int    `yaml:"so_mark"`
	BindToDevice string `yaml:"bind_to_device"`
	Bootstrap    string `yaml:"bootstrap"`
	BootstrapVer int    `yaml:"bootstrap_version"`
}

type UpstreamConfig struct {
	Tag                  string `yaml:"tag"`
	Addr                 string `yaml:"addr"` // Required.
	DialAddr             string `yaml:"dial_addr"`
	IdleTimeout          int    `yaml:"idle_timeout"`
	UpstreamQueryTimeout int    `yaml:"upstream_query_timeout"` // New option for upstream timeout.

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
	effectiveArgs := buildEffectiveArgs(baseArgs, bp.M().GetGlobalOverrides())

	f, err := NewForward(effectiveArgs, Opts{Logger: bp.L(), MetricsTag: bp.Tag()})
	if err != nil {
		return nil, err
	}
	f.baseArgs = baseArgs
	f.pluginTag = bp.Tag()
	f.metricsTag = bp.Tag()

	if err := f.RegisterMetricsTo(prometheus.WrapRegistererWithPrefix(PluginType+"_", bp.M().GetMetricsReg())); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

var _ sequence.Executable = (*Forward)(nil)
var _ sequence.QuickConfigurableExec = (*Forward)(nil)
var _ coremain.ControlConfigReloader = (*Forward)(nil)

type Forward struct {
	runtimeMu  sync.RWMutex
	args       *Args
	baseArgs   *Args
	pluginTag  string
	metricsTag string

	logger       *zap.Logger
	us           []*upstreamWrapper
	tag2Upstream map[string]*upstreamWrapper // for fast tag lookup only.
}

type Opts struct {
	Logger     *zap.Logger
	MetricsTag string
}

// NewForward inits a Forward from given args.
// args must contain at least one upstream.
func NewForward(args *Args, opt Opts) (*Forward, error) {
	if len(args.Upstreams) == 0 {
		return nil, errors.New("no upstream is configured")
	}
	if opt.Logger == nil {
		opt.Logger = zap.NewNop()
	}

	f := &Forward{
		args:         args,
		logger:       opt.Logger,
		tag2Upstream: make(map[string]*upstreamWrapper),
	}

	applyGlobal := func(c *UpstreamConfig) {
		utils.SetDefaultString(&c.Socks5, args.Socks5)
		utils.SetDefaultUnsignNum(&c.SoMark, args.SoMark)
		utils.SetDefaultString(&c.BindToDevice, args.BindToDevice)
		utils.SetDefaultString(&c.Bootstrap, args.Bootstrap)
		utils.SetDefaultUnsignNum(&c.BootstrapVer, args.BootstrapVer)
	}

	for i, c := range args.Upstreams {
		if len(c.Addr) == 0 {
			return nil, fmt.Errorf("#%d upstream invalid args, addr is required", i)
		}
		applyGlobal(&c)

		uw := newWrapper(i, c, opt.MetricsTag)
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

		u, err := upstream.NewUpstream(c.Addr, uOpt)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("failed to init upstream #%d: %w", i, err)
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

func (f *Forward) RegisterMetricsTo(r prometheus.Registerer) error {
	for _, wu := range f.us {
		// Only register metrics for upstream that has a tag.
		if len(wu.cfg.Tag) == 0 {
			continue
		}
		if err := wu.registerMetricsTo(r); err != nil {
			return err
		}
	}
	return nil
}

func (f *Forward) exchange(ctx context.Context, qCtx *query_context.Context, runtimeArgs *Args, us []*upstreamWrapper) (*dns.Msg, error) {
	if len(us) == 0 {
		return nil, errors.New("no upstream to exchange")
	}
	queryPayload, err := pool.PackBuffer(qCtx.Q())
	if err != nil {
		return nil, err
	}
	defer pool.ReleaseBuf(queryPayload)

	selected := pickUpstreams(us, normalizeConcurrent(runtimeArgs.Concurrent, len(us)), time.Now())
	results := make(chan exchangeResult, len(selected))
	done := make(chan struct{})
	defer close(done)
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for i, u := range selected {
		f.startExchangeWorker(queryCtx, done, results, u, copyPayload(queryPayload), hedgeDelayAt(i))
	}

	picker := new(responsePicker)
	for range selected {
		select {
		case result := <-results:
			if winner, done := picker.add(result.resp, result.err); done {
				cancel()
				return winner, nil
			}
		case <-ctx.Done():
			cancel()
			if fallback, err := picker.final(); err == nil && fallback != nil {
				return fallback, nil
			}
			return nil, context.Cause(ctx)
		}
	}
	return picker.final()
}
