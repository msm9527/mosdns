package has_resp

import (
	"context"
	"testing"

	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
)

func TestHaveRespMatchesPrePackedPayload(t *testing.T) {
	q := new(dns.Msg)
	q.SetQuestion("payload-hit.example.", dns.TypeA)
	qCtx := query_context.NewContext(q)
	qCtx.SetResponsePayload(&query_context.ResponsePayload{Wire: []byte{0, 1, 2}})

	matched, err := haveResp{}.Match(context.Background(), qCtx)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("expected has_resp to match pre-packed response payload")
	}
}
