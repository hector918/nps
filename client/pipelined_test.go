package client

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"ehang.io/nps/lib/bridgetls"
	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/conn"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/version"
)

// fakeBridge is the server side of Bridge.cliProcess: TLS first, then one
// hello read field by field, then a single verdict. It echoes what follows a
// secret hello, and reports the secret it was given.
type fakeBridge struct {
	addr   string
	fp     string
	secret chan []byte
}

func startFake(t *testing.T, vkey, verdict, protocol string) *fakeBridge {
	cfg, fp, err := bridgetls.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	f := &fakeBridge{addr: l.Addr().String(), fp: fp, secret: make(chan []byte, 1)}
	go func() {
		raw, err := l.Accept()
		if err != nil {
			return
		}
		c, err := bridgetls.Server(raw, cfg)
		if err != nil {
			return
		}
		defer c.Close()
		sc := conn.NewConn(c)
		if b, err := sc.GetShortLenContent(); err != nil || string(b) != protocol {
			t.Errorf("protocol: %q %v", b, err)
			return
		}
		if b, err := sc.GetShortLenContent(); err != nil || string(b) != version.VERSION {
			t.Errorf("version: %q %v", b, err)
			return
		}
		if b, err := sc.GetShortContent(32); err != nil || string(b) != common.Getverifyval(vkey) {
			t.Errorf("vkey: %q %v", b, err)
			return
		}
		flag, err := sc.ReadFlag()
		if err != nil {
			t.Errorf("work flag: %v", err)
			return
		}
		sc.Write([]byte(verdict))
		if verdict != common.VERIFY_SUCCESS || flag != common.WORK_SECRET {
			return
		}
		key, err := sc.GetShortContent(32)
		if err != nil {
			t.Errorf("secret: %v", err)
			return
		}
		f.secret <- key
		io.Copy(c, c) // echo
	}()
	return f
}

func TestNewConnPipelined(t *testing.T) {
	f := startFake(t, "vk", common.VERIFY_SUCCESS, version.Protocol)
	key := []byte(crypt.Md5("pw"))
	c, err := NewConnPipelined("tcp", "vk", f.addr, common.WORK_SECRET, "", f.fp, key)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Data may be written before the verdict is read.
	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(c.Conn, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("echo: %q %v", buf, err)
	}
	if got := <-f.secret; string(got) != string(key) {
		t.Fatalf("server saw secret %q", got)
	}
}

func TestNewConnPipelinedBadVkey(t *testing.T) {
	f := startFake(t, "vk", common.VERIFY_EER, version.Protocol)
	c, err := NewConnPipelined("tcp", "vk", f.addr, common.WORK_SECRET, "", f.fp, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Conn.Read(make([]byte, 1))
	if err == nil || !strings.Contains(err.Error(), "incorrect") {
		t.Fatalf("want a validation error from the first read, got %v", err)
	}
}

func TestNewConnWaitsForTheVerdict(t *testing.T) {
	f := startFake(t, "vk", common.VERIFY_SUCCESS, version.Protocol)
	c, err := NewConn("tcp", "vk", f.addr, common.WORK_MAIN, "", f.fp)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	f = startFake(t, "vk", common.VERIFY_EER, version.Protocol)
	if _, err := NewConn("tcp", "vk", f.addr, common.WORK_MAIN, "", f.fp); err == nil || !strings.Contains(err.Error(), "incorrect") {
		t.Fatalf("a refused vkey must fail NewConn, got %v", err)
	}

	f = startFake(t, "vk", common.VERIFY_PROTOCOL, version.Protocol)
	if _, err := NewConn("tcp", "vk", f.addr, common.WORK_MAIN, "", f.fp); err == nil || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("a protocol mismatch must fail NewConn and say so, got %v", err)
	}
}

func TestNewConnRefusesAServerItIsNotPinnedTo(t *testing.T) {
	f := startFake(t, "vk", common.VERIFY_SUCCESS, version.Protocol)
	done := make(chan error, 1)
	go func() {
		_, err := NewConn("tcp", "vk", f.addr, common.WORK_MAIN, "", "")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), f.fp) {
			t.Fatalf("with no pin NewConn must fail and show the fingerprint to pin, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("NewConn hung")
	}
}
