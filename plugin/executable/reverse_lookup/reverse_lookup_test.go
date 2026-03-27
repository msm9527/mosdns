package reverselookup

import "testing"

func TestArgsInitSetsDefaultReverseLookupSize(t *testing.T) {
	var args Args

	args.init()

	if args.Size != defaultReverseLookupSize {
		t.Fatalf("unexpected default size: got %d want %d", args.Size, defaultReverseLookupSize)
	}
	if args.TTL != 7200 {
		t.Fatalf("unexpected default ttl: got %d want %d", args.TTL, 7200)
	}
}
