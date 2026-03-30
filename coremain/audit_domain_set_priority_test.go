package coremain

import "testing"

func TestNormalizeAuditDomainSetPrefersCusnocnOverGeositeNoCn(t *testing.T) {
	got := normalizeAuditDomainSet("订阅代理|订阅代理补充", "A")
	if got != "订阅代理补充" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}
