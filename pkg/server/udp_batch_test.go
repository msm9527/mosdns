package server

import (
	"errors"
	"io"
	"net"
	"reflect"
	"testing"

	"golang.org/x/net/ipv6"
)

type batchWriteResult struct {
	n   int
	err error
}
type batchWriterStub struct {
	results []batchWriteResult
	calls   [][]net.Addr
}

func (s *batchWriterStub) ReadBatch([]ipv6.Message, int) (int, error) { panic("unused") }
func (s *batchWriterStub) WriteBatch(messages []ipv6.Message, _ int) (int, error) {
	addresses := make([]net.Addr, len(messages))
	for i := range messages {
		addresses[i] = messages[i].Addr
	}
	s.calls = append(s.calls, addresses)
	result := s.results[0]
	s.results = s.results[1:]
	return result.n, result.err
}
func TestUDPBatchFlushPreservesPartialProgressAndOtherClients(t *testing.T) {
	failure := errors.New("bad destination")
	peers := []net.Addr{&net.UDPAddr{Port: 1}, &net.UDPAddr{Port: 2}, &net.UDPAddr{Port: 3}}
	for _, tc := range []struct {
		name      string
		results   []batchWriteResult
		wantCalls [][]net.Addr
		wantError error
	}{
		{"partial", []batchWriteResult{{1, nil}, {2, nil}}, [][]net.Addr{peers, peers[1:]}, nil},
		{"one bad peer", []batchWriteResult{{1, failure}, {0, failure}, {1, nil}}, [][]net.Addr{peers, peers[1:2], peers[2:]}, failure},
		{"no progress", []batchWriteResult{{0, nil}, {1, nil}, {1, nil}, {1, nil}}, [][]net.Addr{peers, peers[:1], peers[1:2], peers[2:]}, io.ErrNoProgress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &batchWriterStub{results: tc.results}
			b := &udpPacketBatch{conn: stub, receive: make([]ipv6.Message, 3), send: make([]ipv6.Message, 3)}
			for i := range peers {
				b.receive[i] = ipv6.Message{Buffers: [][]byte{make([]byte, 4096)}, Addr: peers[i]}
				b.send[i].Buffers = make([][]byte, 1)
				b.reply(i, 17, []byte{1})
			}
			if err := b.flush(); !errors.Is(err, tc.wantError) {
				t.Fatalf("flush=%v want=%v", err, tc.wantError)
			}
			if !reflect.DeepEqual(stub.calls, tc.wantCalls) {
				t.Fatalf("calls=%v want=%v", stub.calls, tc.wantCalls)
			}
			if b.pending != 0 {
				t.Fatal("pending replies retained")
			}
			for i := range b.receive {
				if len(b.receive[i].Buffers[0]) != 4096 {
					t.Fatal("small reply shortened receive slot")
				}
				if b.send[i].Addr != nil || b.send[i].OOB != nil || b.send[i].Buffers[0] != nil {
					t.Fatal("flushed reply retained references")
				}
			}
		})
	}
}
