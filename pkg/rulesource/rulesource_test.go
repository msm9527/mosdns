package rulesource

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"net/netip"
	"reflect"
	"slices"
	"testing"

	"github.com/klauspost/compress/zstd"
	mcidr "github.com/metacubex/mihomo/component/cidr"
	mtrie "github.com/metacubex/mihomo/component/trie"
	scdomain "github.com/sagernet/sing/common/domain"
	"github.com/sagernet/sing/common/varbin"
)

func TestParseDomainFormats(t *testing.T) {
	textRules, err := ParseDomainBytes(FormatList, []byte("+.example.com\nDOMAIN,exact.example\n"))
	if err != nil {
		t.Fatalf("ParseDomainBytes list: %v", err)
	}
	expectDomainRules(t, textRules, []string{"domain:example.com", "full:exact.example"})

	jsonData := []byte(`{"rules":[{"domain_suffix":["json.example"],"domain_keyword":"keyword"}]}`)
	jsonRules, err := ParseDomainBytes(FormatJSON, jsonData)
	if err != nil {
		t.Fatalf("ParseDomainBytes json: %v", err)
	}
	expectDomainRules(t, jsonRules, []string{"domain:json.example", "keyword:keyword"})

	yamlData := []byte("payload:\n  - +.yaml.example\n  - DOMAIN,full.yaml\n")
	yamlRules, err := ParseDomainBytes(FormatYAML, yamlData)
	if err != nil {
		t.Fatalf("ParseDomainBytes yaml: %v", err)
	}
	expectDomainRules(t, yamlRules, []string{"domain:yaml.example", "full:full.yaml"})

	srsRules, err := ParseDomainBytes(FormatSRS, buildDomainSRS(t))
	if err != nil {
		t.Fatalf("ParseDomainBytes srs: %v", err)
	}
	expectDomainRules(t, srsRules, []string{"domain:suffix.example", "full:full.example", "keyword:key", "regexp:.*regexp.*"})

	mrsRules, err := ParseDomainBytes(FormatMRS, buildDomainMRS(t))
	if err != nil {
		t.Fatalf("ParseDomainBytes mrs: %v", err)
	}
	expectDomainRules(t, mrsRules, []string{"domain:mrs.example", "full:exact.mrs"})
}

func TestParseIPFormats(t *testing.T) {
	textRules, err := ParseIPCIDRBytes(FormatList, []byte("1.1.1.1\nIP-CIDR,2.2.2.0/24\n"))
	if err != nil {
		t.Fatalf("ParseIPCIDRBytes list: %v", err)
	}
	expectPrefixes(t, textRules, []string{"1.1.1.1/32", "2.2.2.0/24"})

	yamlRules, err := ParseIPCIDRBytes(FormatYAML, []byte("payload:\n  - 3.3.3.0/24\n"))
	if err != nil {
		t.Fatalf("ParseIPCIDRBytes yaml: %v", err)
	}
	expectPrefixes(t, yamlRules, []string{"3.3.3.0/24"})

	srsRules, err := ParseIPCIDRBytes(FormatSRS, buildIPSRS(t))
	if err != nil {
		t.Fatalf("ParseIPCIDRBytes srs: %v", err)
	}
	expectPrefixes(t, srsRules, []string{"4.4.4.0/24", "2001:db8::/32"})

	srsRangeRules, err := ParseIPCIDRBytes(FormatSRS, buildIPRangeSRS(t))
	if err != nil {
		t.Fatalf("ParseIPCIDRBytes range srs: %v", err)
	}
	expectPrefixes(t, srsRangeRules, []string{"6.6.6.0/24"})

	mrsRules, err := ParseIPCIDRBytes(FormatMRS, buildIPMRS(t))
	if err != nil {
		t.Fatalf("ParseIPCIDRBytes mrs: %v", err)
	}
	expectPrefixes(t, mrsRules, []string{"5.5.5.0/24"})
}

func TestParseAdguardFormats(t *testing.T) {
	textResult, err := ParseAdguardBytes(FormatRules, []byte("||block.example^\n@@||allow.example^\n/full.*/\n"))
	if err != nil {
		t.Fatalf("ParseAdguardBytes rules: %v", err)
	}
	expectDomainRules(t, textResult.Allow, []string{"domain:allow.example"})
	expectDomainRules(t, textResult.Deny, []string{"domain:block.example", "regexp:full.*"})

	yamlResult, err := ParseAdguardBytes(FormatYAML, []byte("payload:\n  - '||yaml-block.example^'\n"))
	if err != nil {
		t.Fatalf("ParseAdguardBytes yaml: %v", err)
	}
	expectDomainRules(t, yamlResult.Deny, []string{"domain:yaml-block.example"})
}

func TestValidateConfig(t *testing.T) {
	cfg := Config{
		Sources: []Source{{
			ID:         "geo",
			Name:       "geo",
			BindTo:     "geosite_cn",
			Behavior:   BehaviorDomain,
			MatchMode:  MatchModeDomainSet,
			Format:     FormatSRS,
			SourceKind: SourceKindRemote,
			URL:        "https://example.com/a.srs",
			Path:       "diversion/a.srs",
		}},
	}
	if err := ValidateConfig(ScopeDiversion, cfg); err != nil {
		t.Fatalf("ValidateConfig: %v", err)
	}
}

func TestValidateConfigRequiresBindToForDiversion(t *testing.T) {
	cfg := Config{
		Sources: []Source{{
			ID:         "geo",
			Name:       "geo",
			Behavior:   BehaviorDomain,
			MatchMode:  MatchModeDomainSet,
			Format:     FormatList,
			SourceKind: SourceKindLocal,
			Path:       "diversion/geo.list",
		}},
	}
	if err := ValidateConfig(ScopeDiversion, cfg); err == nil {
		t.Fatal("expected bind_to validation error")
	}
}

func buildDomainSRS(t *testing.T) []byte {
	var raw bytes.Buffer
	raw.Write(srsMagic[:])
	if err := binary.Write(&raw, binary.BigEndian, uint8(3)); err != nil {
		t.Fatalf("binary.Write version: %v", err)
	}
	zw := zlib.NewWriter(&raw)
	bw := bufio.NewWriter(zw)
	if _, err := varbin.WriteUvarint(bw, 1); err != nil {
		t.Fatalf("WriteUvarint rules: %v", err)
	}
	if err := bw.WriteByte(0); err != nil {
		t.Fatalf("WriteByte mode: %v", err)
	}
	if err := bw.WriteByte(srsRuleItemDomain); err != nil {
		t.Fatalf("WriteByte domain item: %v", err)
	}
	matcher := scdomain.NewMatcher([]string{"full.example"}, []string{"suffix.example"}, false)
	if err := matcher.Write(bw); err != nil {
		t.Fatalf("matcher.Write: %v", err)
	}
	if err := bw.WriteByte(srsRuleItemDomainKeyword); err != nil {
		t.Fatalf("WriteByte keyword item: %v", err)
	}
	if err := varbin.Write(bw, binary.BigEndian, []string{"key"}); err != nil {
		t.Fatalf("varbin.Write keyword: %v", err)
	}
	if err := bw.WriteByte(srsRuleItemDomainRegex); err != nil {
		t.Fatalf("WriteByte regex item: %v", err)
	}
	if err := varbin.Write(bw, binary.BigEndian, []string{".*regexp.*"}); err != nil {
		t.Fatalf("varbin.Write regex: %v", err)
	}
	if err := bw.WriteByte(srsRuleItemFinal); err != nil {
		t.Fatalf("WriteByte final: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("bw.Flush: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zw.Close: %v", err)
	}
	return raw.Bytes()
}

func buildIPSRS(t *testing.T) []byte {
	var raw bytes.Buffer
	raw.Write(srsMagic[:])
	if err := binary.Write(&raw, binary.BigEndian, uint8(3)); err != nil {
		t.Fatalf("binary.Write version: %v", err)
	}
	zw := zlib.NewWriter(&raw)
	bw := bufio.NewWriter(zw)
	if _, err := varbin.WriteUvarint(bw, 1); err != nil {
		t.Fatalf("WriteUvarint rules: %v", err)
	}
	if err := bw.WriteByte(0); err != nil {
		t.Fatalf("WriteByte mode: %v", err)
	}
	if err := bw.WriteByte(srsRuleItemIPCIDR); err != nil {
		t.Fatalf("WriteByte ip item: %v", err)
	}
	if _, err := varbin.WriteUvarint(bw, 2); err != nil {
		t.Fatalf("WriteUvarint prefixes: %v", err)
	}
	writeSRSPrefix(t, bw, "4.4.4.0/24")
	writeSRSPrefix(t, bw, "2001:db8::/32")
	if err := bw.WriteByte(srsRuleItemFinal); err != nil {
		t.Fatalf("WriteByte final: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("bw.Flush: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zw.Close: %v", err)
	}
	return raw.Bytes()
}

func buildIPRangeSRS(t *testing.T) []byte {
	var raw bytes.Buffer
	raw.Write(srsMagic[:])
	if err := binary.Write(&raw, binary.BigEndian, uint8(2)); err != nil {
		t.Fatalf("binary.Write version: %v", err)
	}
	zw := zlib.NewWriter(&raw)
	bw := bufio.NewWriter(zw)
	if _, err := varbin.WriteUvarint(bw, 1); err != nil {
		t.Fatalf("WriteUvarint rules: %v", err)
	}
	if err := bw.WriteByte(0); err != nil {
		t.Fatalf("WriteByte mode: %v", err)
	}
	if err := bw.WriteByte(srsRuleItemIPCIDR); err != nil {
		t.Fatalf("WriteByte ip item: %v", err)
	}
	if err := bw.WriteByte(1); err != nil {
		t.Fatalf("WriteByte ipset version: %v", err)
	}
	if err := binary.Write(bw, binary.BigEndian, uint64(1)); err != nil {
		t.Fatalf("binary.Write range count: %v", err)
	}
	if err := varbin.Write(bw, binary.BigEndian, []byte{6, 6, 6, 0}); err != nil {
		t.Fatalf("varbin.Write range from: %v", err)
	}
	if err := varbin.Write(bw, binary.BigEndian, []byte{6, 6, 6, 255}); err != nil {
		t.Fatalf("varbin.Write range to: %v", err)
	}
	if err := bw.WriteByte(srsRuleItemFinal); err != nil {
		t.Fatalf("WriteByte final: %v", err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatalf("bw.Flush: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zw.Close: %v", err)
	}
	return raw.Bytes()
}

func writeSRSPrefix(t *testing.T, writer *bufio.Writer, value string) {
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		t.Fatalf("ParsePrefix %s: %v", value, err)
	}
	addr := prefix.Addr()
	kind := uint8(4)
	if addr.Is6() {
		kind = 6
	}
	if err := binary.Write(writer, binary.BigEndian, kind); err != nil {
		t.Fatalf("binary.Write kind: %v", err)
	}
	if addr.Is4() {
		raw := addr.As4()
		if err := binary.Write(writer, binary.BigEndian, raw); err != nil {
			t.Fatalf("binary.Write addr4: %v", err)
		}
	} else {
		raw := addr.As16()
		if err := binary.Write(writer, binary.BigEndian, raw); err != nil {
			t.Fatalf("binary.Write addr6: %v", err)
		}
	}
	if err := binary.Write(writer, binary.BigEndian, uint8(prefix.Bits())); err != nil {
		t.Fatalf("binary.Write bits: %v", err)
	}
}

func buildDomainMRS(t *testing.T) []byte {
	trie := mtrie.New[struct{}]()
	if err := trie.Insert("+.mrs.example", struct{}{}); err != nil {
		t.Fatalf("Insert mrs domain suffix: %v", err)
	}
	if err := trie.Insert("exact.mrs", struct{}{}); err != nil {
		t.Fatalf("Insert mrs full: %v", err)
	}
	set := trie.NewDomainSet()
	return buildMRS(t, mrsBehaviorDomain, 2, func(writer *zstd.Encoder) error {
		return set.WriteBin(writer)
	})
}

func buildIPMRS(t *testing.T) []byte {
	set := mcidr.NewIpCidrSet()
	if err := set.AddIpCidrForString("5.5.5.0/24"); err != nil {
		t.Fatalf("AddIpCidrForString: %v", err)
	}
	return buildMRS(t, mrsBehaviorIPCIDR, 1, func(writer *zstd.Encoder) error {
		return set.WriteBin(writer)
	})
}

func buildMRS(t *testing.T, behavior byte, count int64, writeBody func(*zstd.Encoder) error) []byte {
	var raw bytes.Buffer
	writer, err := zstd.NewWriter(&raw)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, err := writer.Write(mrsMagic[:]); err != nil {
		t.Fatalf("writer.Write magic: %v", err)
	}
	if _, err := writer.Write([]byte{behavior}); err != nil {
		t.Fatalf("writer.Write behavior: %v", err)
	}
	if err := binary.Write(writer, binary.BigEndian, count); err != nil {
		t.Fatalf("binary.Write count: %v", err)
	}
	if err := binary.Write(writer, binary.BigEndian, int64(0)); err != nil {
		t.Fatalf("binary.Write extra length: %v", err)
	}
	if err := writeBody(writer); err != nil {
		t.Fatalf("writeBody: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close: %v", err)
	}
	return raw.Bytes()
}

func expectDomainRules(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected rules: got=%v want=%v", got, want)
	}
}

func expectPrefixes(t *testing.T, got []netip.Prefix, want []string) {
	t.Helper()
	actual := make([]string, 0, len(got))
	for _, prefix := range got {
		actual = append(actual, prefix.String())
	}
	slices.Sort(actual)
	slices.Sort(want)
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("unexpected prefixes: got=%v want=%v", actual, want)
	}
}
