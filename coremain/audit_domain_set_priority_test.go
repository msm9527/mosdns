package coremain

import "testing"

func TestNormalizeAuditDomainSetPrefersCusnocnOverGeositeNoCn(t *testing.T) {
	got := normalizeAuditDomainSet("订阅代理|订阅代理补充", "A")
	if got != "订阅代理补充" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}

func TestNormalizeAuditDomainSetPrefersSubscriptionDirectOverMemoryProxy(t *testing.T) {
	got := normalizeAuditDomainSet("订阅直连|记忆代理", "A")
	if got != "订阅直连" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}

func TestNormalizeAuditDomainSetPrefersGeositeNoCnOverMemoryDirect(t *testing.T) {
	got := normalizeAuditDomainSet("订阅代理|记忆直连", "A")
	if got != "订阅代理" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}

func TestNormalizeAuditDomainSetPrefersHighChurnOverWhitelist(t *testing.T) {
	got := normalizeAuditDomainSet("白名单|高变化域名", "A")
	if got != "高变化域名" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}
