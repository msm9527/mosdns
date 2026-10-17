package server

import (
	"context"

	"github.com/miekg/dns"
)

// ResponseHandler preserves response provenance for cache admission while
// retaining Handler compatibility for transports that only need wire bytes.
type ResponseHandler interface {
	Handler
	HandleResponse(ctx context.Context, q *dns.Msg, meta QueryMeta, pack func(*dns.Msg) (*[]byte, error)) (payload *[]byte, stale bool)
}

// HandleResponse uses response metadata when a handler provides it. The legacy
// Handler contract has no stale provenance and remains supported unchanged.
func HandleResponse(h Handler, ctx context.Context, q *dns.Msg, meta QueryMeta, pack func(*dns.Msg) (*[]byte, error)) (payload *[]byte, stale bool) {
	if h, ok := h.(ResponseHandler); ok {
		return h.HandleResponse(ctx, q, meta, pack)
	}
	return h.Handle(ctx, q, meta, pack), false
}
