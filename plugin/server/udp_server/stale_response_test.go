package udp_server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/server"
	"github.com/IrineSistiana/mosdns/v5/pkg/server_handler"
	responsecache "github.com/IrineSistiana/mosdns/v5/plugin/executable/cache"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/miekg/dns"
	"google.golang.org/protobuf/proto"
)

func TestSharedStaleResponseCannotRenewFastCache(t *testing.T) {
	for _, fromUDP := range []bool{true, false} {
		name := "parsed"
		if fromUDP {
			name = "wire"
		}
		t.Run(name, func(t *testing.T) {
			c := responsecache.NewCache(&responsecache.Args{Size: 256, LazyCacheTTL: 43200, LazyStaleTTL: 43200, ClientTTLMin: 120, ClientTTLMax: 900}, responsecache.Opts{})
			defer c.Close()
			upstream := &stalePolicyUpstream{ready: make(chan struct{}), release: make(chan struct{})}
			defer upstream.releaseOnce()
			manager := coremain.NewTestMosdnsWithPlugins(map[string]any{"cache": c, "upstream": upstream})
			s, err := sequence.NewSequence(sequence.NewBQFromBP(coremain.NewBP("stale-path", manager)), []sequence.RuleArgs{{Exec: "$cache"}, {Exec: "$upstream"}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			entry := server_handler.NewEntryHandler(server_handler.EntryHandlerOpts{Entry: s})
			q := new(dns.Msg)
			q.SetQuestion("stale-path.example.", dns.TypeA)
			meta := server.QueryMeta{FromUDP: fromUDP}
			seed := entry.Handle(context.Background(), q.Copy(), meta, pool.PackBuffer)
			pool.ReleaseBuf(seed)
			ageSharedPolicyCache(t, c)
			fc := newNegativePolicyTestFastCache()
			h := &fastHandler{next: entry, fc: fc, sw: newFastSwitchValue(testSwitchPlugin{value: "on"})}
			stale := readStalePolicyResponse(t, h.Handle(context.Background(), q.Copy(), meta, pool.PackBuffer))
			if got := stale.Answer[0].Header().Ttl; got > 5 || got == 0 {
				t.Errorf("shared stale TTL = %d, want 1..5", got)
			}
			if stale.Answer[0].(*dns.A).A.String() != "1.1.1.1" || fc.Len() != 0 {
				t.Errorf("stale result lost or promoted into fast cache: %v, fast size=%d", stale.Answer, fc.Len())
			}
			select {
			case <-upstream.ready:
			case <-time.After(time.Second):
				t.Fatal("shared cache did not start refreshing")
			}
			// The fast cache's own refresh must also reject a shared stale result.
			h.refreshExpiredCache(context.Background(), q.Copy(), meta, pool.PackBuffer)
			if fc.Len() != 0 {
				t.Error("refresh promoted the old IP into a new fast cache lifetime")
			}
			upstream.releaseOnce()
			deadline := time.Now().Add(time.Second)
			for {
				r := readStalePolicyResponse(t, h.Handle(context.Background(), q.Copy(), meta, pool.PackBuffer))
				if r.Answer[0].(*dns.A).A.String() == "2.2.2.2" {
					if r.Answer[0].Header().Ttl != 120 || fc.Len() != 1 {
						t.Fatal("fresh response lost client TTL governance or fast-cache admission")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("old address survived successful shared-cache refresh")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestFastStaleRepliesUseShortTTLWithoutChangingFreshPayload(t *testing.T) {
	fc := newNegativePolicyTestFastCache()
	name := "fast-stale.example."
	payload := makeAnswer(t, name, dns.TypeA, 42, 30)
	fc.Store(name, dns.TypeA, payload, "", false)
	item := mustFastCacheItem(t, fc, name, dns.TypeA)
	atomic.StoreInt64(&item.expire, fastNowUnix()-1)
	atomic.StoreUint32(&item.updating, 1)
	buf := make([]byte, len(payload))
	copy(buf, makeQuery(t, name, dns.TypeA, 84))
	action, length, _, _, _ := fc.GetOrUpdating(fastQNameHashString(name, dns.TypeA), buf, name, dns.TypeA, true)
	if action != server.FastActionReply {
		t.Fatal("fast stale response was unavailable during refresh")
	}
	var direct dns.Msg
	if err := direct.Unpack(buf[:length]); err != nil {
		t.Fatal(err)
	}
	copyResponse := readStalePolicyResponse(t, fc.CopyResponse(84, name, dns.TypeA, true))
	for _, r := range []*dns.Msg{&direct, copyResponse} {
		if ttl := r.Answer[0].Header().Ttl; ttl == 0 || ttl > 5 {
			t.Errorf("fast stale TTL = %d, want 1..5", ttl)
		}
	}
	atomic.StoreInt64(&item.expire, time.Now().Add(time.Hour).Unix())
	fresh := readStalePolicyResponse(t, fc.CopyResponse(84, name, dns.TypeA, true))
	if fresh.Answer[0].Header().Ttl != 120 {
		t.Fatal("stale reply modified the stored fresh payload")
	}
}

type stalePolicyUpstream struct {
	calls   atomic.Uint32
	ready   chan struct{}
	release chan struct{}
	closed  atomic.Bool
}

func (u *stalePolicyUpstream) releaseOnce() {
	if u.closed.CompareAndSwap(false, true) {
		close(u.release)
	}
}

func (u *stalePolicyUpstream) Exec(ctx context.Context, q *query_context.Context) error {
	ip := "1.1.1.1"
	if call := u.calls.Add(1); call > 1 {
		if call == 2 {
			close(u.ready)
		}
		select {
		case <-u.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		ip = "2.2.2.2"
	}
	var r dns.Msg
	if err := r.Unpack(makeAnswerWithIPNoTest(q.Q().Question[0].Name, dns.TypeA, q.Q().Id, 30, ip)); err != nil {
		return err
	}
	q.SetResponse(&r)
	return nil
}

func readStalePolicyResponse(t *testing.T, payload *[]byte) *dns.Msg {
	t.Helper()
	if payload == nil {
		t.Fatal("missing DNS response")
	}
	defer pool.ReleaseBuf(payload)
	var r dns.Msg
	if err := r.Unpack(*payload); err != nil || len(r.Answer) == 0 {
		t.Fatalf("invalid DNS response: %v", err)
	}
	return &r
}

func ageSharedPolicyCache(t *testing.T, c *responsecache.Cache) {
	t.Helper()
	recorder := httptest.NewRecorder()
	c.Api().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/dump", nil))
	reader, err := gzip.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var length [8]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		t.Fatal(err)
	}
	blockBytes := make([]byte, binary.BigEndian.Uint64(length[:]))
	if _, err := io.ReadFull(reader, blockBytes); err != nil {
		t.Fatal(err)
	}
	var block responsecache.CacheDumpBlock
	if err := proto.Unmarshal(blockBytes, &block); err != nil || len(block.Entries) != 1 {
		t.Fatalf("invalid seed dump: %v", err)
	}
	now := time.Now().Unix()
	block.Entries[0].MsgStoredTime = now - 180
	block.Entries[0].MsgExpirationTime = now - 1
	block.Entries[0].CacheExpirationTime = now + 43200
	blockBytes, err = proto.Marshal(&block)
	if err != nil {
		t.Fatal(err)
	}
	var dump bytes.Buffer
	writer := gzip.NewWriter(&dump)
	writer.Name = reader.Name
	binary.BigEndian.PutUint64(length[:], uint64(len(blockBytes)))
	if _, err := writer.Write(append(length[:], blockBytes...)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.FlushRuntimeCache(context.Background()); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	c.Api().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/load_dump", &dump))
	if recorder.Code != http.StatusOK {
		t.Fatalf("restore stale fixture: %d %s", recorder.Code, recorder.Body.String())
	}
}
