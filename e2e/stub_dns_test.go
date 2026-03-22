package e2e_test

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/miekg/dns"
)

type serviceE2EStubServer struct {
	addr string
	udp  *dns.Server
	tcp  *dns.Server
	a    net.IP
	aaaa net.IP
}

func newServiceE2EStubServer(ipv4, ipv6 string) (*serviceE2EStubServer, error) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		_ = pc.Close()
		return nil, err
	}

	server := &serviceE2EStubServer{
		addr: fmt.Sprintf("127.0.0.1:%d", port),
		udp:  &dns.Server{PacketConn: pc},
		tcp:  &dns.Server{Listener: ln},
		a:    net.ParseIP(ipv4).To4(),
		aaaa: net.ParseIP(ipv6),
	}
	mux := dns.NewServeMux()
	mux.HandleFunc(".", server.serveDNS)
	server.udp.Handler = mux
	server.tcp.Handler = mux
	startServiceE2EStub(server.udp)
	startServiceE2EStub(server.tcp)
	return server, nil
}

func startServiceE2EStub(server *dns.Server) {
	go func() {
		_ = server.ActivateAndServe()
	}()
}

func startServiceE2EUpstreams() (serviceE2EUpstreams, func(), error) {
	domestic, err := newServiceE2EStubServer("1.1.1.1", "2001:db8::1")
	if err != nil {
		return serviceE2EUpstreams{}, nil, err
	}
	foreign, err := newServiceE2EStubServer("8.8.8.8", "2001:db8::8")
	if err != nil {
		domestic.Close()
		return serviceE2EUpstreams{}, nil, err
	}
	cnfake, err := newServiceE2EStubServer("30.0.0.2", "2400::2")
	if err != nil {
		domestic.Close()
		foreign.Close()
		return serviceE2EUpstreams{}, nil, err
	}
	nocnfake, err := newServiceE2EStubServer("28.0.0.2", "f2b0::2")
	if err != nil {
		domestic.Close()
		foreign.Close()
		cnfake.Close()
		return serviceE2EUpstreams{}, nil, err
	}
	return serviceE2EUpstreams{
			domestic:   domestic.addr,
			foreign:    foreign.addr,
			foreignecs: foreign.addr,
			cnfake:     cnfake.addr,
			nocnfake:   nocnfake.addr,
		}, func() {
			domestic.Close()
			foreign.Close()
			cnfake.Close()
			nocnfake.Close()
		}, nil
}

func (s *serviceE2EStubServer) Close() {
	_ = s.udp.Shutdown()
	_ = s.tcp.Shutdown()
}

func (s *serviceE2EStubServer) serveDNS(w dns.ResponseWriter, req *dns.Msg) {
	resp := new(dns.Msg)
	resp.SetReply(req)
	if len(req.Question) == 0 {
		_ = w.WriteMsg(resp)
		return
	}
	resp.Answer = buildServiceE2EAnswers(req.Question[0], s.a, s.aaaa)
	_ = w.WriteMsg(resp)
}

func buildServiceE2EAnswers(q dns.Question, ipv4, ipv6 net.IP) []dns.RR {
	switch q.Qtype {
	case dns.TypeA:
		return []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
			A:   append(net.IP(nil), ipv4...),
		}}
	case dns.TypeAAAA:
		return []dns.RR{&dns.AAAA{
			Hdr:  dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 60},
			AAAA: append(net.IP(nil), ipv6...),
		}}
	case dns.TypeSOA:
		return []dns.RR{&dns.SOA{
			Hdr:     dns.RR_Header{Name: q.Name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
			Ns:      "ns1.example.",
			Mbox:    "hostmaster.example.",
			Serial:  1,
			Refresh: 60,
			Retry:   60,
			Expire:  60,
			Minttl:  60,
		}}
	default:
		return nil
	}
}

func requireServiceE2EARecord(t TestingT, resp *dns.Msg, want string) {
	t.Helper()
	a := requireServiceE2EAnyARecord(t, resp)
	if got := a.A.String(); got != want {
		t.Fatalf("unexpected A answer: got %s want %s", got, want)
	}
}

func requireServiceE2EAnyARecord(t TestingT, resp *dns.Msg) *dns.A {
	t.Helper()
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("unexpected rcode: %d", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("unexpected answer count: %d", len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("unexpected answer type: %T", resp.Answer[0])
	}
	return a
}

func requireServiceE2EAAAARecord(t TestingT, resp *dns.Msg, want string) {
	t.Helper()
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("unexpected rcode: %d", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("unexpected answer count: %d", len(resp.Answer))
	}
	aaaa, ok := resp.Answer[0].(*dns.AAAA)
	if !ok {
		t.Fatalf("unexpected answer type: %T", resp.Answer[0])
	}
	if got := aaaa.AAAA.String(); got != want {
		t.Fatalf("unexpected AAAA answer: got %s want %s", got, want)
	}
}

func requireServiceE2ESOARecord(t TestingT, resp *dns.Msg) {
	t.Helper()
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("unexpected rcode: %d", resp.Rcode)
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("unexpected answer count: %d", len(resp.Answer))
	}
	if _, ok := resp.Answer[0].(*dns.SOA); !ok {
		t.Fatalf("unexpected answer type: %T", resp.Answer[0])
	}
}

func requireServiceE2ERcode(t TestingT, resp *dns.Msg, want int) {
	t.Helper()
	if resp.Rcode != want {
		t.Fatalf("unexpected rcode: got %d want %d", resp.Rcode, want)
	}
}

func requireServiceE2EEmptyAnswer(t TestingT, resp *dns.Msg) {
	t.Helper()
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("unexpected rcode: %d", resp.Rcode)
	}
	if len(resp.Answer) != 0 {
		t.Fatalf("expected empty answer, got %d", len(resp.Answer))
	}
}

func waitServiceE2EEventually(timeout time.Duration, check func() bool, message string) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New(message)
}
