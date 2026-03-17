package fastforward

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/miekg/dns"
)

const (
	defaultHealthLatencyUs   = int64(50 * time.Millisecond / time.Microsecond)
	inflightPenaltyUs        = int64(15 * time.Millisecond / time.Microsecond)
	failurePenaltyUs         = int64(200 * time.Millisecond / time.Microsecond)
	unhealthyPenaltyUs       = int64(5 * time.Second / time.Microsecond)
	healthFailureThreshold   = uint32(2)
	healthFailureBackoffBase = 3 * time.Second
	healthFailureBackoffMax  = 30 * time.Second
	healthLatencyWeightOld   = int64(7)
	healthLatencyWeightNew   = int64(3)
	hedgedQueryDelay         = 40 * time.Millisecond
)

type exchangeResult struct {
	resp *dns.Msg
	err  error
	tag  string
}

type responsePicker struct {
	successOrNX    *dns.Msg
	successOrNXTag string
	other          *dns.Msg
	otherTag       string
	firstErr       error
}

func normalizeConcurrent(requested, total int) int {
	if total <= 0 {
		return 0
	}
	if requested <= 0 {
		requested = 1
	}
	if requested > maxConcurrentQueries {
		requested = maxConcurrentQueries
	}
	if requested > total {
		return total
	}
	return requested
}

func pickUpstreams(us []*upstreamWrapper, concurrent int, now time.Time) []*upstreamWrapper {
	candidates := append([]*upstreamWrapper(nil), us...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].healthScore(now) < candidates[j].healthScore(now)
	})
	if concurrent >= len(candidates) {
		return candidates
	}
	return append([]*upstreamWrapper(nil), candidates[:concurrent]...)
}

func (p *responsePicker) add(resp *dns.Msg, err error, tag string) (*dns.Msg, string, bool) {
	if err != nil {
		if p.firstErr == nil {
			p.firstErr = err
		}
		return nil, "", false
	}
	if hasUsableAnswer(resp) {
		return resp, tag, true
	}
	if resp != nil && (resp.Rcode == dns.RcodeSuccess || resp.Rcode == dns.RcodeNameError) {
		if p.successOrNX == nil {
			p.successOrNX = resp
			p.successOrNXTag = tag
		}
		return nil, "", false
	}
	if resp != nil && p.other == nil {
		p.other = resp
		p.otherTag = tag
	}
	return nil, "", false
}

func (p *responsePicker) final() (*dns.Msg, string, error) {
	if p.successOrNX != nil {
		return p.successOrNX, p.successOrNXTag, nil
	}
	if p.other != nil {
		return p.other, p.otherTag, nil
	}
	if p.firstErr != nil {
		return nil, "", p.firstErr
	}
	return nil, "", errors.New("all upstreams failed or returned no usable response")
}

func hasUsableAnswer(resp *dns.Msg) bool {
	if resp == nil || len(resp.Answer) == 0 {
		return false
	}
	for _, ans := range resp.Answer {
		if a, ok := ans.(*dns.A); ok && len(a.A) > 0 {
			return true
		}
		if aaaa, ok := ans.(*dns.AAAA); ok && len(aaaa.AAAA) > 0 {
			return true
		}
	}
	return false
}

func decodeResponse(payload *[]byte) (*dns.Msg, error) {
	if payload == nil {
		return nil, errors.New("nil upstream response")
	}
	defer pool.ReleaseBuf(payload)
	resp := new(dns.Msg)
	if err := resp.Unpack(*payload); err != nil {
		return nil, err
	}
	return resp, nil
}

func setAuditWinnerTag(qCtx *query_context.Context, tag string) {
	if qCtx == nil || tag == "" {
		return
	}
	coremain.SetAuditUpstreamTag(qCtx, tag)
}

func (f *Forward) queryUpstream(
	parent context.Context,
	queryPayload *[]byte,
	u *upstreamWrapper,
) (*dns.Msg, error) {
	timeout := time.Duration(u.cfg.UpstreamQueryTimeout) * time.Millisecond
	if timeout == 0 {
		timeout = queryTimeout
	}
	upstreamCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	respPayload, err := u.ExchangeContext(upstreamCtx, *queryPayload)
	if err != nil {
		return nil, err
	}
	resp, err := decodeResponse(respPayload)
	if err != nil {
		u.recordDecodeFailure(time.Now())
		return nil, err
	}
	return resp, nil
}

func hedgeDelayAt(index int) time.Duration {
	if index <= 0 {
		return 0
	}
	return time.Duration(index) * hedgedQueryDelay
}

func (f *Forward) startExchangeWorker(
	ctx context.Context,
	done <-chan struct{},
	results chan<- exchangeResult,
	u *upstreamWrapper,
	queryPayload *[]byte,
	delay time.Duration,
) {
	go func() {
		defer pool.ReleaseBuf(queryPayload)
		if delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return
			case <-done:
				return
			}
		}

		resp, err := f.queryUpstream(ctx, queryPayload, u)
		select {
		case results <- exchangeResult{resp: resp, err: err, tag: u.name()}:
		case <-ctx.Done():
		case <-done:
		}
	}()
}
