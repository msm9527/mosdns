package rewrite

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/domain"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

const PluginType = "rewrite"

const (
	defaultDNSTimeout = 5 * time.Second
	fixedTTL          = 5
	rewriteFastMark   = 29
)

type targetKind uint8

const (
	targetIP targetKind = iota + 1
	targetDomain
)

type rewriteTarget struct {
	kind   targetKind
	ip     netip.Addr
	domain string
}

type rewriteRule struct {
	targets []rewriteTarget
}

type Args struct {
	Files []string `yaml:"files"`
	Dns   string   `yaml:"dns"`
}

type dnsExchangeFunc func(context.Context, *dns.Msg, string) (*dns.Msg, error)

type Rewrite struct {
	pluginTag string
	baseArgs  *Args

	mu            sync.RWMutex
	matcher       *domain.MixMatcher[*rewriteRule]
	dnsClient     *dns.Client
	dnsServerAddr string

	ruleFiles []string
	rules     []string
	exchange  dnsExchangeFunc
	revision  atomic.Uint64
}

var (
	_ coremain.ControlConfigReloader = (*Rewrite)(nil)
	_ coremain.CacheRevisionProvider = (*Rewrite)(nil)
	_ sequence.RecursiveExecutable   = (*Rewrite)(nil)
)

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
}

func Init(bp *coremain.BP, args any) (any, error) {
	cfg := args.(*Args)
	baseArgs := cloneArgs(cfg)
	if rawArgs, ok := bp.RawArgs().(*Args); ok && rawArgs != nil {
		baseArgs = cloneArgs(rawArgs)
	}
	return newRewrite(bp.Tag(), baseArgs, cfg)
}

func newRewrite(pluginTag string, baseArgs, effective *Args) (*Rewrite, error) {
	if len(effective.Files) == 0 {
		return nil, fmt.Errorf("at least one rule file must be specified in `files`")
	}

	dnsServerAddr, err := parseUpstreamAddr(effective.Dns)
	if err != nil {
		return nil, fmt.Errorf("invalid upstream dns server: %w", err)
	}

	ruleFiles := resolveRuleFiles(effective.Files)
	matcher, rules, err := loadRulesFromFiles(ruleFiles)
	if err != nil {
		return nil, err
	}

	r := &Rewrite{
		pluginTag:     pluginTag,
		baseArgs:      cloneArgs(baseArgs),
		matcher:       matcher,
		dnsClient:     newDNSClient(),
		dnsServerAddr: dnsServerAddr,
		ruleFiles:     ruleFiles,
		rules:         rules,
	}
	r.revision.Store(1)
	return r, nil
}

func newDNSClient() *dns.Client {
	return &dns.Client{
		Net:     "udp",
		Timeout: defaultDNSTimeout,
	}
}

func newRewriteMatcher() *domain.MixMatcher[*rewriteRule] {
	matcher := domain.NewMixMatcher[*rewriteRule]()
	matcher.SetDefaultMatcher(domain.MatcherFull)
	return matcher
}

func cloneArgs(src *Args) *Args {
	if src == nil {
		return new(Args)
	}
	return &Args{
		Files: append([]string(nil), src.Files...),
		Dns:   src.Dns,
	}
}

func resolveRuleFiles(files []string) []string {
	resolved := make([]string, 0, len(files))
	for _, file := range files {
		resolved = append(resolved, coremain.ResolveMainConfigPath(file))
	}
	return resolved
}

func parseUpstreamAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", fmt.Errorf("upstream dns server address cannot be empty")
	}

	if ip, err := netip.ParseAddr(strings.Trim(addr, "[]")); err == nil {
		return net.JoinHostPort(ip.String(), "53"), nil
	}

	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "" {
			return "", fmt.Errorf("upstream host cannot be empty")
		}
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return "", fmt.Errorf("invalid upstream port %q", port)
		}
		return net.JoinHostPort(host, port), nil
	}

	if strings.Contains(addr, ":") {
		return "", fmt.Errorf("invalid upstream address: %s", addr)
	}
	return net.JoinHostPort(addr, "53"), nil
}

func (r *Rewrite) exchangeUpstream(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
	if r.exchange != nil {
		return r.exchange(ctx, req, r.dnsServerAddr)
	}

	resp, _, err := r.dnsClient.ExchangeContext(ctx, req, r.dnsServerAddr)
	if err != nil || resp == nil || !resp.Truncated {
		return resp, err
	}

	tcpResp, _, tcpErr := (&dns.Client{
		Net:     "tcp",
		Timeout: defaultDNSTimeout,
	}).ExchangeContext(ctx, req.Copy(), r.dnsServerAddr)
	if tcpErr != nil {
		return nil, tcpErr
	}
	return tcpResp, nil
}

func (r *Rewrite) ReloadControlConfig(global *coremain.GlobalOverrides, _ []coremain.UpstreamOverrideConfig) error {
	effective := new(Args)
	if err := coremain.DecodeRawArgsWithGlobalOverrides(r.pluginTag, r.baseArgs, effective, global); err != nil {
		return err
	}

	next, err := newRewrite(r.pluginTag, r.baseArgs, effective)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.matcher = next.matcher
	r.dnsClient = next.dnsClient
	r.dnsServerAddr = next.dnsServerAddr
	r.ruleFiles = next.ruleFiles
	r.rules = next.rules
	r.revision.Add(1)
	r.mu.Unlock()
	return nil
}

func (r *Rewrite) CacheRevision() string {
	return strconv.FormatUint(r.revision.Load(), 10)
}
