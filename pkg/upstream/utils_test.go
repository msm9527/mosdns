package upstream

import "testing"

func TestTryTrimIpv6Brackets(t *testing.T) {
	t.Run("trim bare ipv6 host", func(t *testing.T) {
		got := tryTrimIpv6Brackets("[2001:db8::1]")
		if got != "2001:db8::1" {
			t.Fatalf("unexpected host: got %q", got)
		}
	})

	t.Run("keep host with port", func(t *testing.T) {
		got := tryTrimIpv6Brackets("[2001:db8::1]:853")
		if got != "[2001:db8::1]:853" {
			t.Fatalf("unexpected host: got %q", got)
		}
	})
}
