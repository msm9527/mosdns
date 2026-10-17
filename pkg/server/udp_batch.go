package server

import (
	"io"
	"net"
	"net/netip"
	"runtime"

	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

type udpBatchConn interface {
	ReadBatch([]ipv6.Message, int) (int, error)
	WriteBatch([]ipv6.Message, int) (int, error)
}

type udpPacketBatch struct {
	conn    udpBatchConn
	native  *net.UDPConn
	peer    netip.AddrPort
	receive []ipv6.Message
	send    []ipv6.Message
	buffers []*[]byte
	pending int
}

func newUDPPacketBatch(conn *net.UDPConn, withOOB, fast bool) *udpPacketBatch {
	size := 1
	if runtime.GOOS == "linux" && fast {
		// Receive available packets without waiting for the batch to fill. Four
		// slots bound additional receive storage to 192 KiB per fast listener.
		size = 4
	}
	b := &udpPacketBatch{
		native:  conn,
		receive: make([]ipv6.Message, size),
		send:    make([]ipv6.Message, size),
		buffers: make([]*[]byte, size),
	}
	if size > 1 {
		if conn.LocalAddr().(*net.UDPAddr).IP.To4() != nil {
			b.conn = ipv4.NewPacketConn(conn)
		} else {
			b.conn = ipv6.NewPacketConn(conn)
		}
	}
	for i := range b.receive {
		b.buffers[i] = pool.GetBuf(dns.MaxMsgSize)
		b.receive[i].Buffers = [][]byte{*b.buffers[i]}
		b.send[i].Buffers = make([][]byte, 1)
		if withOOB {
			b.receive[i].OOB = make([]byte, 1024)
		}
	}
	return b
}

func (b *udpPacketBatch) close() {
	for _, buffer := range b.buffers {
		pool.ReleaseBuf(buffer)
	}
}

func (b *udpPacketBatch) read() (int, error) {
	if b.conn != nil {
		return b.conn.ReadBatch(b.receive, 0)
	}
	m := &b.receive[0]
	var err error
	m.N, m.NN, m.Flags, b.peer, err = b.native.ReadMsgUDPAddrPort(m.Buffers[0], m.OOB)
	if err != nil {
		return 0, err
	}
	return 1, nil
}

func (b *udpPacketBatch) remote(index int) netip.AddrPort {
	if b.conn == nil {
		return b.peer
	}
	return b.receive[index].Addr.(*net.UDPAddr).AddrPort()
}

func (b *udpPacketBatch) reply(index, size int, oob []byte) {
	input, output := &b.receive[index], &b.send[b.pending]
	// Separate slice headers keep the next receive buffer at its full length.
	output.Buffers[0] = input.Buffers[0][:size]
	output.OOB = oob
	output.Addr = input.Addr
	b.pending++
}

func (b *udpPacketBatch) flush() error {
	defer func() {
		for i := range b.send[:b.pending] {
			b.send[i].Buffers[0] = nil
			b.send[i].OOB = nil
			b.send[i].Addr = nil
		}
		b.pending = 0
	}()
	if b.conn == nil {
		if b.pending == 0 {
			return nil
		}
		m := &b.send[0]
		_, _, err := b.native.WriteMsgUDPAddrPort(m.Buffers[0], m.OOB, b.peer)
		return err
	}
	for sent := 0; sent < b.pending; {
		n, err := b.conn.WriteBatch(b.send[sent:b.pending], 0)
		sent += n
		if err == nil && n > 0 {
			continue
		}
		if err == nil {
			err = io.ErrNoProgress
		}
		// One bad destination must not discard other clients' responses.
		// Retry unsent packets separately, preserving partial-write progress.
		for ; sent < b.pending; sent++ {
			_, _ = b.conn.WriteBatch(b.send[sent:sent+1], 0)
		}
		return err
	}
	return nil
}
