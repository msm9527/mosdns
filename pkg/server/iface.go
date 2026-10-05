package server

import (
	"context"
	"net/netip"

	"github.com/miekg/dns"
)

type Handler interface {
	Handle(ctx context.Context, q *dns.Msg, meta QueryMeta, packMsgPayload func(m *dns.Msg) (*[]byte, error)) (respPayload *[]byte)
}

// FastRuleMatchMeta is the original mapper result used by UDP fast routing.
// Known distinguishes a checked negative match from an unknown result. It does
// not include client-policy marks or response classification discovered later.
type FastRuleMatchMeta struct {
	Known     bool
	Flags     uint64
	DomainSet string
	Matched   bool
}

type QueryMeta struct {
	FromUDP bool

	// RequestSource describes server-owned provenance, not a client-supplied value.
	// Unspecified is resolved by the entry handler using its listener's policy.
	RequestSource RequestSource

	ClientAddr   netip.Addr
	ServerName   string
	UrlPath      string
	PreFastFlags uint64
	// PreFastRuleMatch belongs to the original request and is cleared for replay.
	PreFastRuleMatch FastRuleMatchMeta
	PreFastDomainSet string
	// PreFastDomainMatched reports whether domain_mapper already matched in UDP fast path.
	PreFastDomainMatched bool
	// PreFastStaleRefresh reports that UDP fast path should serve stale cache and
	// refresh the answer in background.
	PreFastStaleRefresh bool
}
