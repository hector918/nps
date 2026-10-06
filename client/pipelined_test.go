package client

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/version"
)

// serveHandshake plays the server side of Bridge.cliProcess step by step, the
// way the real one reads: one field at a time, replying as it goes. verdict
// is what it answers to the vkey.
func serveHandshake(t *testing.T, c net.Conn, vkey, verdict string, rest chan<- []byte) {
	defer c.Close()
	sc := conn.NewConn(c)
	if b, err := sc.GetShortContent(3); err != nil || string(b) != common.CONN_TEST {
		t.Errorf("test flag: %q %v", b, err)
		return
	}
	if b, err := sc.GetShortLenContent(); err != nil || string(b) != version.GetVersion() {
		t.Errorf("core version: %q %v", b, err)
		return
	}
	if b, err := sc.GetShortLenContent(); err != nil || string(b) != version.VERSION {
		t.Errorf("version: %q %v", b, err)
		return
	}
	sc.Write([]byte(crypt.Md5(version.GetVersion())))
	if b, err := sc.GetShortContent(32); err != nil || string(b) != common.Getverifyval(vkey) {
		t.Errorf("vkey: %q %v", b, err)
		return
	}
	sc.Write([]byte(verdict))
	if verdict != common.VERIFY_SUCCESS {
		return
	}
	flag, err := sc.ReadFlag()
	if err != nil || flag != common.WORK_SECRET {
		t.Errorf("work flag: %q %v", flag, err)
		return
	}
	key, err := sc.GetShortContent(32)
	if err != nil {
		t.Errorf("secret: %v", err)
		return
	}
	rest <- key
	io.Copy(c, c) // echo
}

func startFake(t *testing.T, vkey, verdict string) (string, chan []byte) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rest := make(chan []byte, 1)
	go func() {
		c, err := l.Accept()
		l.Close()
		if err == nil {
			serveHandshake(t, c, vkey, verdict, rest)
		}
	}()
	return l.Addr().String(), rest
}

func TestNewConnPipelined(t *testing.T) {
	addr, rest := startFake(t, "vk", common.VERIFY_SUCCESS)
	key := []byte(crypt.Md5("pw"))
	c, err := NewConnPipelined("tcp", "vk", addr, common.WORK_SECRET, "", key)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Data may be written before any reply is read.
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	c.Conn.(*verifyOnReadConn).Conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c.Conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo: %q %v", buf, err)
	}
	if got := <-rest; string(got) != string(key) {
		t.Fatalf("server saw secret %q", got)
	}
}

func TestNewConnPipelinedBadVkey(t *testing.T) {
	addr, _ := startFake(t, "vk", common.VERIFY_EER)
	c, err := NewConnPipelined("tcp", "vk", addr, common.WORK_SECRET, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Conn.Read(make([]byte, 1))
	if err == nil || !strings.Contains(err.Error(), "incorrect") {
		t.Fatalf("want a validation error from the first read, got %v", err)
	}
}
