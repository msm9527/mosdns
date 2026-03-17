package rulesource

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"

	scdomain "github.com/sagernet/sing/common/domain"
	"github.com/sagernet/sing/common/varbin"
)

var srsMagic = [3]byte{'S', 'R', 'S'}

const (
	srsRuleItemDomain        = uint8(2)
	srsRuleItemDomainKeyword = uint8(3)
	srsRuleItemDomainRegex   = uint8(4)
	srsRuleItemIPCIDR        = uint8(6)
	srsRuleItemFinal         = uint8(0xFF)
	srsRuleSetVersionCurrent = 3
)

func parseDomainSRS(data []byte) ([]string, error) {
	reader, count, err := newSRSReader(data)
	if err != nil {
		return nil, err
	}
	rules := make([]string, 0, count)
	if err := collectDomainSRSRules(reader, count, &rules); err != nil {
		return nil, err
	}
	return uniqueStrings(rules), nil
}

func newSRSReader(data []byte) (*bufio.Reader, uint64, error) {
	raw := bytes.NewReader(data)
	var magic [3]byte
	if _, err := io.ReadFull(raw, magic[:]); err != nil || magic != srsMagic {
		return nil, 0, fmt.Errorf("invalid srs header")
	}
	var version uint8
	if err := binary.Read(raw, binary.BigEndian, &version); err != nil || version > srsRuleSetVersionCurrent {
		return nil, 0, fmt.Errorf("unsupported srs version")
	}
	zr, err := zlib.NewReader(raw)
	if err != nil {
		return nil, 0, err
	}
	reader := bufio.NewReader(zr)
	count, err := binary.ReadUvarint(reader)
	if err != nil {
		return nil, 0, err
	}
	return reader, count, nil
}

func collectDomainSRSRules(reader *bufio.Reader, count uint64, rules *[]string) error {
	for i := uint64(0); i < count; i++ {
		mode, err := reader.ReadByte()
		if err != nil {
			return err
		}
		if err := readSRSRuleCompat(reader, mode, rules); err != nil {
			return err
		}
	}
	return nil
}

func readSRSRuleCompat(reader *bufio.Reader, mode byte, rules *[]string) error {
	switch mode {
	case 0:
		return readSRSDefaultRule(reader, rules)
	case 1:
		if _, err := reader.ReadByte(); err != nil {
			return err
		}
		count, err := binary.ReadUvarint(reader)
		if err != nil {
			return err
		}
		for i := uint64(0); i < count; i++ {
			mode, err := reader.ReadByte()
			if err != nil {
				return err
			}
			if err := readSRSRuleCompat(reader, mode, rules); err != nil {
				return err
			}
		}
		_, err = reader.ReadByte()
		return err
	default:
		return fmt.Errorf("unsupported srs rule mode %d", mode)
	}
}

func readSRSDefaultRule(reader *bufio.Reader, rules *[]string) error {
	for {
		item, err := reader.ReadByte()
		if err != nil {
			return err
		}
		switch item {
		case srsRuleItemDomain:
			if err := appendSRSDomains(reader, rules); err != nil {
				return err
			}
		case srsRuleItemDomainKeyword:
			appendStringValues(reader, "keyword:", rules)
		case srsRuleItemDomainRegex:
			appendStringValues(reader, "regexp:", rules)
		case srsRuleItemIPCIDR:
			if err := skipSRSIPSet(reader); err != nil {
				return err
			}
		case srsRuleItemFinal:
			return nil
		default:
			return nil
		}
	}
}

func appendSRSDomains(reader *bufio.Reader, rules *[]string) error {
	matcher, err := scdomain.ReadMatcher(reader)
	if err != nil {
		return err
	}
	full, suffix := matcher.Dump()
	for _, item := range full {
		*rules = append(*rules, "full:"+item)
	}
	for _, item := range suffix {
		*rules = append(*rules, "domain:"+item)
	}
	return nil
}

func appendStringValues(reader *bufio.Reader, prefix string, rules *[]string) {
	values, _ := varbin.ReadValue[[]string](reader, binary.BigEndian)
	for _, value := range values {
		*rules = append(*rules, prefix+value)
	}
}
