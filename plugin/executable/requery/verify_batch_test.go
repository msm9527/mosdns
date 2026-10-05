package requery

import (
	"context"
	"fmt"
	"testing"
)

type batchVerifier struct {
	batches [][]string
	singles []string
	fail    bool
}

func (v *batchVerifier) MarkDomainVerified(_ context.Context, domain, _ string) (int, error) {
	v.singles = append(v.singles, domain)
	return 1, nil
}
func (v *batchVerifier) MarkDomainsVerified(_ context.Context, domains []string, _ string) (int, error) {
	v.batches = append(v.batches, append([]string(nil), domains...))
	if v.fail {
		return 0, fmt.Errorf("missing domain")
	}
	return len(domains), nil
}

type singleVerifier struct{ domains []string }

func (v *singleVerifier) MarkDomainVerified(_ context.Context, domain, _ string) (int, error) {
	v.domains = append(v.domains, domain)
	if domain == "missing.example" {
		return 0, fmt.Errorf("missing domain")
	}
	return 1, nil
}

func TestMarkDomainsVerifiedGroupsPoolsAndKeepsFallback(t *testing.T) {
	a, b := &batchVerifier{fail: true}, &batchVerifier{}
	legacy := &singleVerifier{}
	plugins := map[string]any{"a": a, "b": b, "legacy": legacy}
	p := &Requery{plugin: func(tag string) any { return plugins[tag] }}
	jobs := make([]refreshJob, 0, 38)
	for i := 0; i < 32; i++ {
		jobs = append(jobs, refreshJob{VerifyTag: "a", Domain: fmt.Sprintf("a-%d.example", i)})
	}
	jobs = append(jobs, refreshJob{VerifyTag: "legacy", Domain: "missing.example"}, refreshJob{VerifyTag: "b", Domain: "b.example"}, refreshJob{VerifyTag: "legacy", Domain: "exists.example"}, refreshJob{Domain: "unverified.example"})
	if err := p.markDomainsVerified(context.Background(), jobs); err == nil {
		t.Fatal("expected partial failure")
	}
	if len(a.batches) != 1 || len(a.batches[0]) != 32 || len(a.singles) != 0 {
		t.Fatalf("unexpected first pool calls %+v", a)
	}
	if len(b.batches) != 1 || len(b.batches[0]) != 1 {
		t.Fatalf("second pool was blocked %+v", b)
	}
	if len(legacy.domains) != 2 {
		t.Fatalf("fallback stopped after missing domain %+v", legacy)
	}
}
