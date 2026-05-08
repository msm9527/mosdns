package coremain

import "testing"

func TestNormalizeAuditDomainSetPrefersCusnocnOverGeositeNoCn(t *testing.T) {
	got := normalizeAuditDomainSet("国外分流|代理补充", "A")
	if got != "代理补充" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}

func TestNormalizeAuditDomainSetPrefersSubscriptionDirectOverMemoryProxy(t *testing.T) {
	got := normalizeAuditDomainSet("国内分流|记忆代理", "A")
	if got != "国内分流" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}

func TestNormalizeAuditDomainSetPrefersGeositeNoCnOverMemoryDirect(t *testing.T) {
	got := normalizeAuditDomainSet("国外分流|记忆直连", "A")
	if got != "国外分流" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}

func TestNormalizeAuditDomainSetPrefersHighChurnOverWhitelist(t *testing.T) {
	got := normalizeAuditDomainSet("白名单|高变化域名", "A")
	if got != "高变化域名" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}

func TestNormalizeAuditDomainSetMapsLegacySubscriptionLabels(t *testing.T) {
	got := normalizeAuditDomainSet("订阅代理|订阅直连补充", "A")
	if got != "直连补充" {
		t.Fatalf("unexpected normalized domain set: %q", got)
	}
}
