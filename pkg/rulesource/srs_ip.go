package rulesource

import (
	"bufio"
	"encoding/binary"
	"io"
	"net/netip"

	scdomain "github.com/sagernet/sing/common/domain"
	"github.com/sagernet/sing/common/varbin"
)

func parseIPSRS(data []byte) ([]netip.Prefix, error) {
	reader, count, err := newSRSReader(data)
	if err != nil {
		return nil, err
	}
	prefixes := make([]netip.Prefix, 0, count)
	if err := collectIPSRSRules(reader, count, &prefixes); err != nil {
		return nil, err
	}
	return uniquePrefixes(prefixes), nil
}

func collectIPSRSRules(reader *bufio.Reader, count uint64, prefixes *[]netip.Prefix) error {
	for i := uint64(0); i < count; i++ {
		mode, err := reader.ReadByte()
		if err != nil {
			return err
		}
		if err := readSRSIPRuleCompat(reader, mode, prefixes); err != nil {
			return err
		}
	}
	return nil
}

func readSRSIPRuleCompat(reader *bufio.Reader, mode byte, prefixes *[]netip.Prefix) error {
	if mode == 1 {
		if _, err := reader.ReadByte(); err != nil {
			return err
		}
		count, err := binary.ReadUvarint(reader)
		if err != nil {
			return err
		}
		for i := uint64(0); i < count; i++ {
			child, err := reader.ReadByte()
			if err != nil {
				return err
			}
			if err := readSRSIPRuleCompat(reader, child, prefixes); err != nil {
				return err
			}
		}
		_, err = reader.ReadByte()
		return err
	}
	return readSRSIPDefaultRule(reader, prefixes)
}

func readSRSIPDefaultRule(reader *bufio.Reader, prefixes *[]netip.Prefix) error {
	for {
		item, err := reader.ReadByte()
		if err != nil {
			return err
		}
		switch item {
		case srsRuleItemIPCIDR:
			if err := appendSRSIPSet(reader, prefixes); err != nil {
				return err
			}
		case srsRuleItemDomain:
			if err := skipSRSDomains(reader); err != nil {
				return err
			}
		case srsRuleItemDomainKeyword, srsRuleItemDomainRegex:
			if _, err := varbin.ReadValue[[]string](reader, binary.BigEndian); err != nil {
				return err
			}
		case srsRuleItemFinal:
			return nil
		default:
			return nil
		}
	}
}

func appendSRSIPSet(reader *bufio.Reader, prefixes *[]netip.Prefix) error {
	count, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	for i := uint64(0); i < count; i++ {
		prefix, err := readSRSPrefix(reader)
		if err != nil {
			return err
		}
		*prefixes = append(*prefixes, prefix)
	}
	return nil
}

func skipSRSDomains(reader *bufio.Reader) error {
	_, err := scdomain.ReadMatcher(reader)
	return err
}

func skipSRSIPSet(reader *bufio.Reader) error {
	count, err := binary.ReadUvarint(reader)
	if err != nil {
		return err
	}
	for i := uint64(0); i < count; i++ {
		if _, err := readSRSPrefix(reader); err != nil {
			return err
		}
	}
	return nil
}

func readSRSPrefix(reader io.Reader) (netip.Prefix, error) {
	addr, err := readSRSAddr(reader)
	if err != nil {
		return netip.Prefix{}, err
	}
	var bits uint8
	if err := binary.Read(reader, binary.BigEndian, &bits); err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr.Unmap(), int(bits)), nil
}

func readSRSAddr(reader io.Reader) (netip.Addr, error) {
	var kind uint8
	if err := binary.Read(reader, binary.BigEndian, &kind); err != nil {
		return netip.Addr{}, err
	}
	switch kind {
	case 4:
		var raw [4]byte
		if err := binary.Read(reader, binary.BigEndian, &raw); err != nil {
			return netip.Addr{}, err
		}
		return netip.AddrFrom4(raw), nil
	case 6:
		var raw [16]byte
		if err := binary.Read(reader, binary.BigEndian, &raw); err != nil {
			return netip.Addr{}, err
		}
		return netip.AddrFrom16(raw), nil
	default:
		return netip.Addr{}, io.ErrUnexpectedEOF
	}
}
