package rulesource

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"

	mcidr "github.com/metacubex/mihomo/component/cidr"
	mtrie "github.com/metacubex/mihomo/component/trie"
	"github.com/klauspost/compress/zstd"
)

var mrsMagic = [4]byte{'M', 'R', 'S', 1}

const (
	mrsBehaviorDomain = byte(0)
	mrsBehaviorIPCIDR = byte(1)
)

func parseDomainMRS(data []byte) ([]string, error) {
	reader, err := newMRSReader(data, mrsBehaviorDomain)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	set, err := mtrie.ReadDomainSetBin(reader)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0)
	set.Foreach(func(key string) bool {
		keys = append(keys, key)
		return true
	})
	return normalizeMRSDomainKeys(keys), nil
}

func parseIPMRS(data []byte) ([]netip.Prefix, error) {
	reader, err := newMRSReader(data, mrsBehaviorIPCIDR)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	set, err := mcidr.ReadIpCidrSet(reader)
	if err != nil {
		return nil, err
	}
	prefixes := make([]netip.Prefix, 0)
	set.Foreach(func(prefix netip.Prefix) bool {
		prefixes = append(prefixes, prefix.Masked())
		return true
	})
	return uniquePrefixes(prefixes), nil
}

func newMRSReader(data []byte, behavior byte) (*zstd.Decoder, error) {
	decoder, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var magic [4]byte
	if _, err := decoder.Read(magic[:]); err != nil || magic != mrsMagic {
		decoder.Close()
		return nil, fmt.Errorf("invalid mrs header")
	}
	var rawBehavior [1]byte
	if _, err := decoder.Read(rawBehavior[:]); err != nil || rawBehavior[0] != behavior {
		decoder.Close()
		return nil, fmt.Errorf("invalid mrs behavior")
	}
	var count int64
	if err := binary.Read(decoder, binary.BigEndian, &count); err != nil {
		decoder.Close()
		return nil, err
	}
	var extraLength int64
	if err := binary.Read(decoder, binary.BigEndian, &extraLength); err != nil {
		decoder.Close()
		return nil, err
	}
	if extraLength > 0 {
		if _, err := io.ReadFull(decoder, make([]byte, extraLength)); err != nil {
			decoder.Close()
			return nil, err
		}
	}
	return decoder, nil
}

func normalizeMRSDomainKeys(keys []string) []string {
	sort.Strings(keys)
	seenDomain := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if strings.HasPrefix(key, "+.") || strings.HasPrefix(key, ".") {
			seenDomain[strings.TrimPrefix(strings.TrimPrefix(key, "+."), ".")] = struct{}{}
		}
	}
	rules := make([]string, 0, len(keys))
	seenRule := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		rule := convertMRSDomainKey(key)
		if rule == "" || skipRedundantMRSRule(rule, seenDomain) {
			continue
		}
		if _, ok := seenRule[rule]; ok {
			continue
		}
		seenRule[rule] = struct{}{}
		rules = append(rules, rule)
	}
	return rules
}

func skipRedundantMRSRule(rule string, seenDomain map[string]struct{}) bool {
	if !strings.HasPrefix(rule, "full:") {
		return false
	}
	_, ok := seenDomain[strings.TrimPrefix(rule, "full:")]
	return ok
}

func convertMRSDomainKey(key string) string {
	switch {
	case strings.HasPrefix(key, "+."):
		return "domain:" + strings.TrimPrefix(key, "+.")
	case strings.HasPrefix(key, "."):
		return "domain:" + strings.TrimPrefix(key, ".")
	case strings.Contains(key, "*"):
		replacer := strings.NewReplacer(".", `\.`, "*", ".*")
		return "regexp:" + replacer.Replace(key)
	case key == "":
		return ""
	default:
		return "full:" + key
	}
}
