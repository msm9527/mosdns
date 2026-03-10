package sequence

import (
	"context"
	"io"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type benchFastMatcher struct {
	match bool
}

func (m benchFastMatcher) Match(ctx context.Context, qCtx *query_context.Context) (bool, error) {
	return m.match, nil
}

func (m benchFastMatcher) GetFastCheck() func(qCtx *query_context.Context) bool {
	return func(qCtx *query_context.Context) bool { return m.match }
}

type benchFastExec struct{}

func (benchFastExec) Exec(ctx context.Context, qCtx *query_context.Context) error { return nil }

func (benchFastExec) GetFastExec() func(ctx context.Context, qCtx *query_context.Context) error {
	return func(ctx context.Context, qCtx *query_context.Context) error { return nil }
}

type benchSlowMatcher struct {
	match bool
}

func (m benchSlowMatcher) Match(ctx context.Context, qCtx *query_context.Context) (bool, error) {
	return m.match, nil
}

type benchSlowExec struct{}

func (benchSlowExec) Exec(ctx context.Context, qCtx *query_context.Context) error { return nil }

func newBenchQueryContext() *query_context.Context {
	q := new(dns.Msg)
	q.SetQuestion("bench.example.org.", dns.TypeA)
	return query_context.NewContext(q)
}

func newDiscardDebugLogger() *zap.Logger {
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(io.Discard),
		zap.DebugLevel,
	)
	return zap.New(core)
}

func BenchmarkChainWalkerFastPathNoDebug(b *testing.B) {
	node := &ChainNode{
		PluginName: "bench_fast",
		Matches: []NamedMatcher{
			{Name: "fast_1", Matcher: benchFastMatcher{match: true}},
			{Name: "fast_2", Matcher: benchFastMatcher{match: true}},
		},
		E: benchFastExec{},
	}
	ins := []instruction{{
		isSimple:   true,
		fastChecks: []func(qCtx *query_context.Context) bool{benchFastMatcher{match: true}.GetFastCheck(), benchFastMatcher{match: true}.GetFastCheck()},
		fastExec:   benchFastExec{}.GetFastExec(),
		node:       node,
	}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := NewChainWalker(ins, []*ChainNode{node}, nil, nil)
		qCtx := newBenchQueryContext()
		if err := w.ExecNext(context.Background(), qCtx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChainWalkerFastPathDebugLogger(b *testing.B) {
	logger := newDiscardDebugLogger()
	node := &ChainNode{
		PluginName: "bench_fast",
		Matches: []NamedMatcher{
			{Name: "fast_1", Matcher: benchFastMatcher{match: true}},
			{Name: "fast_2", Matcher: benchFastMatcher{match: true}},
		},
		E: benchFastExec{},
	}
	ins := []instruction{{
		isSimple:   true,
		fastChecks: []func(qCtx *query_context.Context) bool{benchFastMatcher{match: true}.GetFastCheck(), benchFastMatcher{match: true}.GetFastCheck()},
		fastExec:   benchFastExec{}.GetFastExec(),
		node:       node,
	}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := NewChainWalker(ins, []*ChainNode{node}, nil, logger)
		qCtx := newBenchQueryContext()
		if err := w.ExecNext(context.Background(), qCtx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChainWalkerFallbackPathNoDebug(b *testing.B) {
	node := &ChainNode{
		PluginName: "bench_slow",
		Matches: []NamedMatcher{
			{Name: "slow_1", Matcher: benchSlowMatcher{match: true}},
			{Name: "slow_2", Matcher: benchSlowMatcher{match: true}},
		},
		E: benchSlowExec{},
	}
	ins := []instruction{{node: node}}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := NewChainWalker(ins, []*ChainNode{node}, nil, nil)
		qCtx := newBenchQueryContext()
		if err := w.ExecNext(context.Background(), qCtx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChainWalkerFastPathReuseContext(b *testing.B) {
	node := &ChainNode{
		PluginName: "bench_fast",
		Matches: []NamedMatcher{
			{Name: "fast_1", Matcher: benchFastMatcher{match: true}},
			{Name: "fast_2", Matcher: benchFastMatcher{match: true}},
		},
		E: benchFastExec{},
	}
	ins := []instruction{{
		isSimple:   true,
		fastChecks: []func(qCtx *query_context.Context) bool{benchFastMatcher{match: true}.GetFastCheck(), benchFastMatcher{match: true}.GetFastCheck()},
		fastExec:   benchFastExec{}.GetFastExec(),
		node:       node,
	}}
	qCtx := newBenchQueryContext()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := NewChainWalker(ins, []*ChainNode{node}, nil, nil)
		if err := w.ExecNext(context.Background(), qCtx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkChainWalkerFallbackPathReuseContext(b *testing.B) {
	node := &ChainNode{
		PluginName: "bench_slow",
		Matches: []NamedMatcher{
			{Name: "slow_1", Matcher: benchSlowMatcher{match: true}},
			{Name: "slow_2", Matcher: benchSlowMatcher{match: true}},
		},
		E: benchSlowExec{},
	}
	ins := []instruction{{node: node}}
	qCtx := newBenchQueryContext()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := NewChainWalker(ins, []*ChainNode{node}, nil, nil)
		if err := w.ExecNext(context.Background(), qCtx); err != nil {
			b.Fatal(err)
		}
	}
}
