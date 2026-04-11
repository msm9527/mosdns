package server

import (
	"net"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestServeUDPReturnsNilOnClosedListener(t *testing.T) {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer c.Close()

	obsCore, logs := observer.New(zap.WarnLevel)
	logger := zap.New(obsCore)

	errCh := make(chan error, 1)
	go func() {
		errCh <- ServeUDP(c, nil, UDPServerOpts{Logger: logger})
	}()

	time.Sleep(100 * time.Millisecond)
	if err := c.Close(); err != nil && !isListenerCloseErr(err) {
		t.Fatalf("close udp listener: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ServeUDP returned err on normal close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeUDP did not exit after listener close")
	}

	if got := logs.FilterMessage("read err").Len(); got != 0 {
		t.Fatalf("expected no read err warnings on normal close, got %d", got)
	}
}

func TestServeTCPReturnsNilOnClosedListener(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp: %v", err)
	}
	defer l.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- ServeTCP(l, nil, TCPServerOpts{})
	}()

	time.Sleep(100 * time.Millisecond)
	if err := l.Close(); err != nil && !isListenerCloseErr(err) {
		t.Fatalf("close tcp listener: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ServeTCP returned err on normal close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeTCP did not exit after listener close")
	}
}
