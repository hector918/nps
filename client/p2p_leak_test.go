package client

import (
	"net"
	"testing"
	"time"
)

// A failed handleP2PUdp must give its local UDP port back. It used to return
// with the socket still bound, so the retries on the same address all failed
// with "address already in use" and hid the real error.
func TestHandleP2PUdpReleasesPortOnError(t *testing.T) {
	probe, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	local := probe.LocalAddr().String()
	probe.Close()

	// A server address with no port: what a server without p2p_port hands out.
	for i := 0; i < 3; i++ {
		_, _, err := handleP2PUdp(local, "127.0.0.1:", "md5", "p2pv")
		if err == nil {
			t.Fatal("want an error for a server address without a port")
		}
		if _, isInUse := err.(*net.OpError); isInUse {
			t.Fatalf("try %d: the port was still held: %v", i+1, err)
		}
	}
	c, err := net.ListenUDP("udp", mustAddr(t, local))
	if err != nil {
		t.Fatalf("the port was not released: %v", err)
	}
	c.Close()
}

// With a server that never answers, handleP2PUdp has to give up, not wait
// forever.
func TestHandleP2PUdpGivesUpOnSilentServer(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the 15s read deadline")
	}
	silent, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := handleP2PUdp("127.0.0.1:0", silent.LocalAddr().String(), "md5", "p2pv")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("want a timeout error")
		}
	case <-time.After(25 * time.Second):
		t.Fatal("still waiting for a silent server after 25s")
	}
}

func mustAddr(t *testing.T, s string) *net.UDPAddr {
	a, err := net.ResolveUDPAddr("udp", s)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
