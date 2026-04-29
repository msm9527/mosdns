package coremain

import "testing"

func TestDecideShuntActionPrefersCusnocnOverGeositeNoCn(t *testing.T) {
	decision, path := decideShuntAction("A", map[uint8]bool{
		14: true,
		15: true,
	}, map[string]string{}, nil)
	if decision.Matched != 15 || decision.Action != "sequence_fakeip_addlist" {
		t.Fatalf("unexpected decision: %+v path=%+v", decision, path)
	}
}

func TestDecideShuntActionPrefersCusnocnOverGeositeCn(t *testing.T) {
	decision, path := decideShuntAction("A", map[uint8]bool{
		15: true,
		16: true,
	}, map[string]string{}, nil)
	if decision.Matched != 15 || decision.Action != "sequence_fakeip_addlist" {
		t.Fatalf("unexpected decision: %+v path=%+v", decision, path)
	}
}

func TestDecideShuntActionPrefersMemoryProxyOverMemoryDirect(t *testing.T) {
	decision, path := decideShuntAction("A", map[uint8]bool{
		11: true,
		12: true,
	}, map[string]string{}, nil)
	if decision.Matched != 12 || decision.Action != "sequence_fakeip" {
		t.Fatalf("unexpected decision: %+v path=%+v", decision, path)
	}
}

func TestDecideShuntActionPrefersGeositeNoCnOverMemoryDirect(t *testing.T) {
	decision, path := decideShuntAction("A", map[uint8]bool{
		11: true,
		14: true,
	}, map[string]string{}, nil)
	if decision.Matched != 14 || decision.Action != "sequence_fakeip_addlist" {
		t.Fatalf("unexpected decision: %+v path=%+v", decision, path)
	}
}
