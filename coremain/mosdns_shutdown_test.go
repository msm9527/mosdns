package coremain

import "testing"

func TestPluginsForShutdownOrdersByPriorityAndTag(t *testing.T) {
	m := &Mosdns{
		plugins: map[string]any{
			"cache":      struct{}{},
			"stats":      struct{}{},
			"forwarder":  struct{}{},
			"b_listener": struct{}{},
			"a_listener": struct{}{},
		},
		pluginTypes: map[string]string{
			"cache":      "cache",
			"stats":      "domain_stats_pool",
			"forwarder":  "forward",
			"b_listener": "udp_server",
			"a_listener": "tcp_server",
		},
	}

	entries := m.pluginsForShutdown()
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.tag)
	}

	want := []string{"a_listener", "b_listener", "forwarder", "cache", "stats"}
	if len(got) != len(want) {
		t.Fatalf("unexpected shutdown order length: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected shutdown order: got=%v want=%v", got, want)
		}
	}
}
