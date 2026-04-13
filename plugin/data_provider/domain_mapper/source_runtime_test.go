package domain_mapper

import (
	"reflect"
	"testing"
)

func TestProviderSetSupportsRefsBeyondInlineMask(t *testing.T) {
	var set providerSet
	for _, ref := range []uint16{0, 1, 63, 64, 65, 129} {
		if !set.add(ref) {
			t.Fatalf("expected ref %d to be added once", ref)
		}
		if set.add(ref) {
			t.Fatalf("expected duplicate ref %d to be ignored", ref)
		}
	}

	var got []uint16
	set.forEach(func(ref uint16) {
		got = append(got, ref)
	})

	want := []uint16{0, 1, 63, 64, 65, 129}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected refs: got %v want %v", got, want)
	}

	clone := set.clone()
	if clone.cacheKey() != set.cacheKey() {
		t.Fatal("expected clone to preserve provider set cache key")
	}
}
