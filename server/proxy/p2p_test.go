package proxy

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
)

func newTestServer(t *testing.T, known ...string) *P2PServer {
	s := NewP2PServer(0)
	set := map[string]bool{}
	for _, k := range known {
		set[crypt.Md5(k)] = true
	}
	s.known = func(md5 string) bool { return set[md5] }
	l, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	s.listener = l
	t.Cleanup(func() { l.Close() })
	return s
}

// Packets are built the way the client builds them, so a change to the
// encoding on either side shows up here.
func packet(pw, role string) string { return string(common.GetWriteStr(crypt.Md5(pw), role)) }

func TestAcceptOnlyKnownPasswordAndRole(t *testing.T) {
	s := newTestServer(t, "secret")
	for _, c := range []struct {
		name, in string
		want     bool
	}{
		{"visitor", packet("secret", common.WORK_P2P_VISITOR), true},
		{"provider", packet("secret", common.WORK_P2P_PROVIDER), true},
		{"unknown password", packet("guess", common.WORK_P2P_VISITOR), false},
		{"unknown role", packet("secret", "anything"), false},
		{"empty", "", false},
		{"no separator", crypt.Md5("secret"), false},
		{"not hex", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz*#*p2pv*#*", false},
		{"extra fields", packet("secret", common.WORK_P2P_VISITOR) + "x*#*", false},
		{"plain md5 without trailing separator", crypt.Md5("secret") + "*#*p2pv", false},
	} {
		if _, _, ok := s.accept(c.in); ok != c.want {
			t.Errorf("%s: accept = %v, want %v", c.name, ok, c.want)
		}
	}
}

func TestRefuseKeepsNoState(t *testing.T) {
	s := newTestServer(t, "secret")
	for i := 0; i < 1000; i++ {
		if _, _, ok := s.accept(packet(fmt.Sprintf("guess%d", i), common.WORK_P2P_PROVIDER)); ok {
			t.Fatal("accepted an unknown password")
		}
	}
	if len(s.p2p) != 0 {
		t.Fatalf("%d entries kept for refused packets", len(s.p2p))
	}
}

// The visitor and the provider are each told the other's address.
func TestPairing(t *testing.T) {
	s := newTestServer(t, "secret")
	listen := func() *net.UDPConn {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	visitor, provider := listen(), listen()
	pw := crypt.Md5("secret")
	go s.handleP2P(provider.LocalAddr().(*net.UDPAddr), pw, common.WORK_P2P_PROVIDER)
	time.Sleep(50 * time.Millisecond)
	go s.handleP2P(visitor.LocalAddr().(*net.UDPAddr), pw, common.WORK_P2P_VISITOR)

	read := func(c *net.UDPConn) string {
		c.SetReadDeadline(time.Now().Add(3 * time.Second))
		buf := make([]byte, 64)
		n, _, err := c.ReadFromUDP(buf)
		if err != nil {
			t.Fatal(err)
		}
		return string(buf[:n])
	}
	if got, want := read(visitor), provider.LocalAddr().String(); got != want {
		t.Errorf("visitor was told %s, want the provider %s", got, want)
	}
	if got, want := read(provider), visitor.LocalAddr().String(); got != want {
		t.Errorf("provider was told %s, want the visitor %s", got, want)
	}
	time.Sleep(50 * time.Millisecond)
	s.mu.Lock()
	left := len(s.p2p)
	s.mu.Unlock()
	if left != 0 {
		t.Errorf("%d entries left after pairing", left)
	}
}

// Concurrent packets used to be a fatal "concurrent map writes".
func TestConcurrentPacketsDoNotCrash(t *testing.T) {
	pws := make([]string, 8)
	for i := range pws {
		pws[i] = fmt.Sprintf("pw%d", i)
	}
	s := newTestServer(t, pws...)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				pw := crypt.Md5(pws[(i+j)%len(pws)])
				s.handleP2P(&net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40000 + i}, pw, common.WORK_P2P_PROVIDER)
			}
		}(i)
	}
	wg.Wait()
}

// Through the real listen loop: a known password is paired, an unknown one
// gets no reply at all.
func TestListenLoopEndToEnd(t *testing.T) {
	probe, _ := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	port := probe.LocalAddr().(*net.UDPAddr).Port
	probe.Close()

	s := NewP2PServer(port)
	s.known = func(md5 string) bool { return md5 == crypt.Md5("secret") }
	go s.Start()
	srv := &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: port}
	// Start sets s.listener itself; wait until the port is really taken.
	for i := 0; i < 50; i++ {
		c, err := net.ListenUDP("udp", srv)
		if err != nil {
			break
		}
		c.Close()
		time.Sleep(20 * time.Millisecond)
	}
	to := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
	sock := func() *net.UDPConn {
		c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	provider, visitor, stranger := sock(), sock(), sock()

	stranger.WriteTo([]byte(packet("guess", common.WORK_P2P_VISITOR)), to)
	provider.WriteTo([]byte(packet("secret", common.WORK_P2P_PROVIDER)), to)
	time.Sleep(100 * time.Millisecond)
	visitor.WriteTo([]byte(packet("secret", common.WORK_P2P_VISITOR)), to)

	buf := make([]byte, 64)
	visitor.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := visitor.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != provider.LocalAddr().String() {
		t.Fatalf("visitor got %q, %v; want the provider's address", buf[:n], err)
	}
	stranger.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if n, _, err := stranger.ReadFromUDP(buf); err == nil {
		t.Fatalf("a stranger with an unknown password got a reply: %q", buf[:n])
	}
}

// A provider that registered long ago has most likely gone: a visitor must not
// be handed its address.
func TestStaleProviderIsNotHandedOut(t *testing.T) {
	s := newTestServer(t, "secret")
	s.visitorWait = 2
	pw := crypt.Md5("secret")
	stale := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1}
	s.p2p[pw] = &p2p{providerAddr: stale, providerAt: time.Now().Add(-2 * providerTTL)}

	visitor, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer visitor.Close()
	s.handleP2P(visitor.LocalAddr().(*net.UDPAddr), pw, common.WORK_P2P_VISITOR)

	visitor.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, 64)
	if n, _, err := visitor.ReadFromUDP(buf); err == nil {
		t.Fatalf("the visitor was told %q, the address of a provider that registered long ago", buf[:n])
	}
}
