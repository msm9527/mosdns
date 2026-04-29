package rewrite

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
)

func TestExecPassesThroughUnsupportedQueryType(t *testing.T) {
	r := mustNewTestRewrite(t, "example.com 1.1.1.1")
	qCtx := newTestContext("example.com.", dns.TypeTXT)

	if err := r.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if qCtx.R() != nil {
		t.Fatal("expected rewrite to skip unsupported query type")
	}
}

func TestExecSupportsMultipleStaticTargets(t *testing.T) {
	r := mustNewTestRewrite(t, "example.com 1.1.1.1 # first\nexample.com 2.2.2.2\nexample.com 2001:db8::1")

	aCtx := newTestContext("example.com.", dns.TypeA)
	if err := r.Exec(context.Background(), aCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec() A error = %v", err)
	}
	assertAnswerIPs(t, aCtx.R(), "example.com.", dns.TypeA, "1.1.1.1", "2.2.2.2")
	if !aCtx.HasFastFlag(rewriteFastMark) {
		t.Fatal("expected rewrite fast mark on A response")
	}

	aaaaCtx := newTestContext("example.com.", dns.TypeAAAA)
	if err := r.Exec(context.Background(), aaaaCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec() AAAA error = %v", err)
	}
	assertAnswerIPs(t, aaaaCtx.R(), "example.com.", dns.TypeAAAA, "2001:db8::1")
	if !aaaaCtx.HasFastFlag(rewriteFastMark) {
		t.Fatal("expected rewrite fast mark on AAAA response")
	}
}

func TestLoadRulesFromFilesMergesDuplicatePatternsAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "rewrite-a.txt")
	file2 := filepath.Join(dir, "rewrite-b.txt")
	if err := os.WriteFile(file1, []byte("example.com 1.1.1.1\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(file1) error = %v", err)
	}
	if err := os.WriteFile(file2, []byte("example.com 2.2.2.2\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(file2) error = %v", err)
	}

	matcher, _, err := loadRulesFromFiles([]string{file1, file2})
	if err != nil {
		t.Fatalf("loadRulesFromFiles() error = %v", err)
	}
	rule, ok := matcher.Match("example.com.")
	if !ok {
		t.Fatal("expected merged rewrite rule")
	}
	if got := len(rule.targets); got != 2 {
		t.Fatalf("unexpected target count %d", got)
	}
}

func TestReplaceListRuntimeSupportsMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "rewrite-a.txt")
	file2 := filepath.Join(dir, "rewrite-b.txt")
	r := &Rewrite{
		ruleFiles: []string{file1, file2},
	}

	if _, err := r.ReplaceListRuntime(context.Background(), []string{"example.com 1.1.1.1"}); err != nil {
		t.Fatalf("ReplaceListRuntime() error = %v", err)
	}

	b1, err := os.ReadFile(file1)
	if err != nil {
		t.Fatalf("ReadFile(file1) error = %v", err)
	}
	if string(b1) != "example.com 1.1.1.1\n" {
		t.Fatalf("unexpected file1 content %q", string(b1))
	}

	b2, err := os.ReadFile(file2)
	if err != nil {
		t.Fatalf("ReadFile(file2) error = %v", err)
	}
	if string(b2) != "" {
		t.Fatalf("unexpected file2 content %q", string(b2))
	}
}

func TestExecFlattensDomainTargets(t *testing.T) {
	r := mustNewTestRewrite(t, "example.com upstream.test")
	r.exchange = func(_ context.Context, req *dns.Msg, _ string) (*dns.Msg, error) {
		resp := new(dns.Msg).SetReply(req)
		resp.RecursionAvailable = true
		resp.Answer = []dns.RR{
			&dns.CNAME{
				Hdr:    dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 30},
				Target: "alias.target.",
			},
			&dns.A{
				Hdr: dns.RR_Header{Name: "alias.target.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30},
				A:   net.ParseIP("9.9.9.9").To4(),
			},
		}
		return resp, nil
	}

	qCtx := newTestContext("example.com.", dns.TypeA)
	if err := r.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	resp := qCtx.R()
	assertAnswerIPs(t, resp, "example.com.", dns.TypeA, "9.9.9.9")
	if !resp.RecursionAvailable {
		t.Fatal("expected recursion available flag to be preserved")
	}
}

func TestExecFollowsCNAMEChainWithoutInlineAddress(t *testing.T) {
	r := mustNewTestRewrite(t, "example.com upstream.test")
	r.exchange = func(_ context.Context, req *dns.Msg, _ string) (*dns.Msg, error) {
		switch req.Question[0].Name {
		case "upstream.test.":
			resp := new(dns.Msg).SetReply(req)
			resp.Answer = []dns.RR{
				&dns.CNAME{
					Hdr:    dns.RR_Header{Name: "upstream.test.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60},
					Target: "alias.target.",
				},
			}
			return resp, nil
		case "alias.target.":
			resp := new(dns.Msg).SetReply(req)
			resp.Answer = []dns.RR{
				&dns.A{
					Hdr: dns.RR_Header{Name: "alias.target.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP("7.7.7.7").To4(),
				},
			}
			return resp, nil
		default:
			t.Fatalf("unexpected upstream query %s", req.Question[0].Name)
			return nil, nil
		}
	}

	qCtx := newTestContext("example.com.", dns.TypeA)
	if err := r.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	assertAnswerIPs(t, qCtx.R(), "example.com.", dns.TypeA, "7.7.7.7")
}

func TestExecMergesDuplicateDomainRules(t *testing.T) {
	r := mustNewTestRewrite(t, "example.com miss.test\nexample.com hit.test")
	r.exchange = func(_ context.Context, req *dns.Msg, _ string) (*dns.Msg, error) {
		switch req.Question[0].Name {
		case "miss.test.":
			return new(dns.Msg).SetRcode(req, dns.RcodeNameError), nil
		case "hit.test.":
			resp := new(dns.Msg).SetReply(req)
			resp.Answer = []dns.RR{
				&dns.A{
					Hdr: dns.RR_Header{Name: req.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   net.ParseIP("8.8.8.8").To4(),
				},
			}
			return resp, nil
		default:
			t.Fatalf("unexpected upstream query %s", req.Question[0].Name)
			return nil, nil
		}
	}

	qCtx := newTestContext("example.com.", dns.TypeA)
	if err := r.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	assertAnswerIPs(t, qCtx.R(), "example.com.", dns.TypeA, "8.8.8.8")
}

func TestExecPreservesNXDomainWhenNoAnswer(t *testing.T) {
	r := mustNewTestRewrite(t, "example.com missing.test")
	r.exchange = func(_ context.Context, req *dns.Msg, _ string) (*dns.Msg, error) {
		resp := new(dns.Msg).SetRcode(req, dns.RcodeNameError)
		resp.Ns = []dns.RR{
			&dns.SOA{
				Hdr: dns.RR_Header{Name: "missing.test.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 123},
				Ns:  "ns1.missing.test.",
			},
		}
		resp.Extra = []dns.RR{
			&dns.TXT{
				Hdr: dns.RR_Header{Name: "trace.missing.test.", Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 77},
				Txt: []string{"meta"},
			},
		}
		return resp, nil
	}

	qCtx := newTestContext("example.com.", dns.TypeA)
	if err := r.Exec(context.Background(), qCtx, sequence.ChainWalker{}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	resp := qCtx.R()
	if resp.Rcode != dns.RcodeNameError {
		t.Fatalf("unexpected rcode %d", resp.Rcode)
	}
	if len(resp.Question) != 1 || resp.Question[0].Name != "example.com." {
		t.Fatal("expected original question to be preserved")
	}
	if len(resp.Ns) != 1 || resp.Ns[0].Header().Name != "missing.test." {
		t.Fatal("expected upstream authority section to be preserved")
	}
	if len(resp.Extra) != 1 || resp.Extra[0].Header().Name != "trace.missing.test." {
		t.Fatal("expected upstream extra section to be preserved")
	}
}

func TestNewUpstreamQueryCopiesEdns0Options(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeA)
	opt := &dns.OPT{
		Hdr: dns.RR_Header{Name: ".", Rrtype: dns.TypeOPT},
	}
	opt.SetUDPSize(1400)
	opt.SetDo()
	opt.Option = append(opt.Option, &dns.EDNS0_SUBNET{
		Code:          dns.EDNS0SUBNET,
		Family:        1,
		SourceNetmask: 24,
		Address:       net.ParseIP("1.2.3.0"),
	})
	query.Extra = append(query.Extra, opt)

	upstream := newUpstreamQuery(query, "target.test.")
	gotOpt := upstream.IsEdns0()
	if gotOpt == nil {
		t.Fatal("expected edns0 option")
	}
	if gotOpt.UDPSize() != 1400 || !gotOpt.Do() {
		t.Fatal("expected udp size and do bit to be copied")
	}
	if len(gotOpt.Option) != 1 {
		t.Fatalf("unexpected option count %d", len(gotOpt.Option))
	}
}

func TestParseUpstreamAddrRejectsInvalidPort(t *testing.T) {
	if _, err := parseUpstreamAddr("127.0.0.1:bad"); err == nil {
		t.Fatal("expected invalid port error")
	}
	addr, err := parseUpstreamAddr("2001:db8::1")
	if err != nil {
		t.Fatalf("parseUpstreamAddr() error = %v", err)
	}
	if addr != "[2001:db8::1]:53" {
		t.Fatalf("unexpected normalized addr %s", addr)
	}
}

func mustNewTestRewrite(t *testing.T, rules string) *Rewrite {
	t.Helper()
	matcher := newRewriteMatcher()
	loadedRules, err := loadRulesFromReader(strings.NewReader(rules), matcher)
	if err != nil {
		t.Fatalf("loadRulesFromReader() error = %v", err)
	}
	return &Rewrite{
		matcher:       matcher,
		dnsClient:     newDNSClient(),
		dnsServerAddr: "127.0.0.1:53",
		rules:         loadedRules,
	}
}

func newTestContext(name string, qtype uint16) *query_context.Context {
	msg := new(dns.Msg)
	msg.SetQuestion(name, qtype)
	return query_context.NewContext(msg)
}

func assertAnswerIPs(t *testing.T, resp *dns.Msg, wantName string, wantType uint16, wantIPs ...string) {
	t.Helper()
	if resp == nil {
		t.Fatal("expected response, got nil")
	}
	if len(resp.Answer) != len(wantIPs) {
		t.Fatalf("unexpected answer count %d", len(resp.Answer))
	}
	for i, answer := range resp.Answer {
		if answer.Header().Name != wantName {
			t.Fatalf("answer %d has unexpected name %s", i, answer.Header().Name)
		}
		if answer.Header().Rrtype != wantType {
			t.Fatalf("answer %d has unexpected type %d", i, answer.Header().Rrtype)
		}
		if answer.Header().Ttl != fixedTTL {
			t.Fatalf("answer %d has unexpected ttl %d", i, answer.Header().Ttl)
		}
		switch rr := answer.(type) {
		case *dns.A:
			if rr.A.String() != wantIPs[i] {
				t.Fatalf("answer %d has unexpected ip %s", i, rr.A.String())
			}
		case *dns.AAAA:
			if rr.AAAA.String() != wantIPs[i] {
				t.Fatalf("answer %d has unexpected ip %s", i, rr.AAAA.String())
			}
		default:
			t.Fatalf("answer %d has unexpected rr type %T", i, answer)
		}
	}
}
