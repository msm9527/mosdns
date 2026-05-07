package cache_route_tag

import (
	"context"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

const PluginType = "cache_route_tag"
const routeTagPrefix = "chain:"

func init() {
	coremain.RegNewPluginFunc(PluginType, New, func() any { return new(Args) })
	sequence.MustRegExecQuickSetup(PluginType, QuickSetup)
}

type Args struct {
	Tag string `yaml:"tag"`
}

type CacheRouteTag struct {
	tag string
}

var _ sequence.Executable = (*CacheRouteTag)(nil)

func New(_ *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)
	return &CacheRouteTag{tag: strings.TrimSpace(cfg.Tag)}, nil
}

func QuickSetup(_ sequence.BQ, args string) (any, error) {
	return &CacheRouteTag{tag: strings.TrimSpace(args)}, nil
}

func (c *CacheRouteTag) Exec(_ context.Context, qCtx *query_context.Context) error {
	if strings.HasPrefix(c.tag, routeTagPrefix) {
		query_context.ReplaceDependencyTagPrefix(qCtx, c.tag, routeTagPrefix)
		return nil
	}
	query_context.AppendDependencyTag(qCtx, c.tag)
	return nil
}

func (c *CacheRouteTag) GetFastExec() func(context.Context, *query_context.Context) error {
	return c.Exec
}
