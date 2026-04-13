package coremain

import "testing"

func TestRecommendAutoMemoryLimit(t *testing.T) {
	tests := []struct {
		name      string
		heapAlloc uint64
		want      int64
	}{
		{
			name:      "floor",
			heapAlloc: 20 << 20,
			want:      80 << 20,
		},
		{
			name:      "scaled",
			heapAlloc: 48 << 20,
			want:      112 << 20,
		},
		{
			name:      "align up",
			heapAlloc: 49 << 20,
			want:      120 << 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recommendAutoMemoryLimit(tt.heapAlloc); got != tt.want {
				t.Fatalf("recommendAutoMemoryLimit(%d) = %d, want %d", tt.heapAlloc, got, tt.want)
			}
		})
	}
}

func TestAlignUpInt64(t *testing.T) {
	if got := alignUpInt64(97, 8); got != 104 {
		t.Fatalf("alignUpInt64 returned %d, want 104", got)
	}
	if got := alignUpInt64(96, 8); got != 96 {
		t.Fatalf("alignUpInt64 returned %d, want 96", got)
	}
}
