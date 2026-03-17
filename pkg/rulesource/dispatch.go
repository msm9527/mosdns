package rulesource

import (
	"fmt"
	"net/netip"
)

type AdguardResult struct {
	Allow []string
	Deny  []string
}

func (r AdguardResult) Count() int {
	return len(r.Allow) + len(r.Deny)
}

func ParseDomainBytes(format Format, data []byte) ([]string, error) {
	switch format {
	case FormatTXT, FormatList, FormatRules:
		return parseDomainTextLines(normalizeTextLines(data))
	case FormatJSON:
		return parseDomainStructured(parseJSONAny, data)
	case FormatYAML:
		return parseDomainStructured(parseYAMLAny, data)
	case FormatSRS:
		return parseDomainSRS(data)
	case FormatMRS:
		return parseDomainMRS(data)
	default:
		return nil, fmt.Errorf("unsupported domain format %q", format)
	}
}

func ParseAdguardBytes(format Format, data []byte) (AdguardResult, error) {
	switch format {
	case FormatTXT, FormatList, FormatRules:
		return parseAdguardLines(normalizeTextLines(data))
	case FormatJSON:
		return parseAdguardStructured(parseJSONAny, data)
	case FormatYAML:
		return parseAdguardStructured(parseYAMLAny, data)
	default:
		return AdguardResult{}, fmt.Errorf("unsupported adguard format %q", format)
	}
}

func ParseIPCIDRBytes(format Format, data []byte) ([]netip.Prefix, error) {
	switch format {
	case FormatTXT, FormatList:
		return parseIPTextLines(normalizeTextLines(data))
	case FormatJSON:
		return parseIPStructured(parseJSONAny, data)
	case FormatYAML:
		return parseIPStructured(parseYAMLAny, data)
	case FormatSRS:
		return parseIPSRS(data)
	case FormatMRS:
		return parseIPMRS(data)
	default:
		return nil, fmt.Errorf("unsupported ipcidr format %q", format)
	}
}
