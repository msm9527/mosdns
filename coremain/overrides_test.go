package coremain

import "testing"

func TestApplyOverrideStringSupportsSplitECSValues(t *testing.T) {
	overrides := &GlobalOverrides{
		ECS:         "223.5.5.5",
		DomesticECS: "auto",
		ForeignECS:  "1.1.1.1",
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "domestic marker", in: "ecs __domestic_ecs__", want: "ecs auto"},
		{name: "foreign marker", in: "ecs __foreign_ecs__", want: "ecs 1.1.1.1"},
		{name: "legacy ecs", in: "ecs auto", want: "ecs 223.5.5.5"},
		{name: "unrelated", in: "$domestic", want: "$domestic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ApplyOverrideString("test", tt.in, overrides); got != tt.want {
				t.Fatalf("ApplyOverrideString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyOverrideStringUsesSafeECSDefaults(t *testing.T) {
	tests := []struct {
		name      string
		overrides *GlobalOverrides
		in        string
		want      string
	}{
		{name: "nil domestic", in: "ecs __domestic_ecs__", want: "ecs auto"},
		{name: "nil foreign", in: "ecs __foreign_ecs__", want: "ecs auto"},
		{
			name:      "domestic falls back to legacy",
			overrides: &GlobalOverrides{ECS: "2408:8888::8"},
			in:        "ecs __domestic_ecs__",
			want:      "ecs 2408:8888::8",
		},
		{
			name:      "foreign defaults to auto",
			overrides: &GlobalOverrides{ECS: "2408:8888::8"},
			in:        "ecs __foreign_ecs__",
			want:      "ecs auto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ApplyOverrideString("test", tt.in, tt.overrides); got != tt.want {
				t.Fatalf("ApplyOverrideString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
