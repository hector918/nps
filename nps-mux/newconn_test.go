package nps_mux

import (
	"net"
	"testing"
	"time"
)

// NewConn used to wait out its full two minute timer when the mux closed
// underneath it: Close wakes every conn in the map, but nothing in NewConn
// was listening.
func TestNewConnReturnsWhenMuxCloses(t *testing.T) {
	if raceEnabled {
		t.Skip("closing a mux races on upstream's unsynchronised close flags")
	}
	a, b := net.Pipe() // b never answers
	defer b.Close()
	m := NewMux(a, "tcp", 60)
	done := make(chan error, 1)
	go func() {
		_, err := m.NewConn()
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // let it send and start waiting
	_ = m.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("NewConn on a closed mux returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NewConn kept waiting after its mux closed")
	}
}

// A reply to a NewConn that is no longer waiting, such as the OK that comes
// after the two minute timer, used to block the mux's read loop on an
// unbuffered send, stalling every stream on the mux.
func TestUnclaimedNewConnReplyDoesNotStallReadLoop(t *testing.T) {
	if raceEnabled {
		t.Skip("closing a mux races on upstream's unsynchronised close flags")
	}
	a, b := net.Pipe()
	client := NewMux(a, "tcp", 60)
	server := NewMux(b, "tcp", 60)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	go func() {
		for {
			if _, err := server.Accept(); err != nil {
				return
			}
		}
	}()
	c, err := client.NewConn()
	if err != nil {
		t.Fatal(err)
	}
	// the OK for c was consumed; these have nobody to receive them
	server.sendInfo(muxNewConnOk, c.connId, nil)
	server.sendInfo(muxNewConnFail, c.connId, nil)
	server.sendInfo(muxNewConnOk, c.connId, nil)

	done := make(chan error, 1)
	go func() {
		_, err := client.NewConn()
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the read loop is stuck behind an unclaimed reply")
	}
}
