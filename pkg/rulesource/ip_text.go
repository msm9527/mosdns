package rulesource

import (
	"fmt"
	"net/netip"
	"strings"
)

func parseIPTextLines(lines []string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(lines))
	for _, line := range lines {
		prefix, err := normalizeIPLine(line)
		if err != nil {
			return nil, err
		}
		prefixes = append(prefixes, prefix)
	}
	return uniquePrefixes(prefixes), nil
}

func normalizeIPLine(line string) (netip.Prefix, error) {
	text := line
	if strings.Contains(line, ",") {
		text = afterComma(line)
	}
	if prefix, err := netip.ParsePrefix(text); err == nil {
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(text)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("invalid ip rule %q", line)
	}
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	return netip.PrefixFrom(addr.Unmap(), bits), nil
}
