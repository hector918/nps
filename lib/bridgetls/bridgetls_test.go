package bridgetls

import (
	"crypto/tls"
	"io"
	"net"
	"strings"
	"testing"
)

func serve(t *testing.T, cfg *tls.Config) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				s, err := Server(c, cfg)
				if err != nil {
					return
				}
				defer s.Close()
				io.Copy(s, s)
			}()
		}
	}()
	return l.Addr().String()
}

func dial(addr, pin string) (net.Conn, error) {
	cc, err := ClientConfig(pin)
	if err != nil {
		return nil, err
	}
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return Client(raw, cc)
}

func TestPinnedHandshake(t *testing.T) {
	cfg, fp, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, cfg)

	c, err := dial(addr, fp)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo %q %v", buf, err)
	}
	if v := c.(*tls.Conn).ConnectionState().Version; v != tls.VersionTLS13 {
		t.Fatalf("negotiated %#x, want TLS 1.3", v)
	}

	// the pin may be written with colons, upper case or no prefix
	loose := strings.ToUpper(strings.TrimPrefix(fp, "sha256:"))
	var colon []string
	for i := 0; i < len(loose); i += 2 {
		colon = append(colon, loose[i:i+2])
	}
	if c2, err := dial(addr, strings.Join(colon, ":")); err != nil {
		t.Fatalf("loosely written pin refused: %v", err)
	} else {
		c2.Close()
	}
}

func TestWrongOrMissingPinIsRefused(t *testing.T) {
	cfg, fp, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, cfg)

	_, other, _ := LoadOrCreate(t.TempDir())
	if _, err := dial(addr, other); err == nil || !strings.Contains(err.Error(), fp) {
		t.Fatalf("a wrong pin must be refused and name what was presented, got %v", err)
	}
	if _, err := dial(addr, ""); err == nil || !strings.Contains(err.Error(), fp) {
		t.Fatalf("no pin must be refused and name what was presented, got %v", err)
	}
	if _, err := ClientConfig("not a fingerprint"); err == nil {
		t.Fatal("a malformed pin must be rejected up front")
	}
}

func TestOlderTLSIsRefused(t *testing.T) {
	cfg, fp, _ := LoadOrCreate(t.TempDir())
	addr := serve(t, cfg)
	cc, _ := ClientConfig(fp)
	cc.MinVersion, cc.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if c, err := Client(raw, cc); err == nil {
		c.Close()
		t.Fatal("a TLS 1.2 client got through")
	}
}

func TestKeyIsKeptAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	_, a, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := LoadOrCreate(dir)
	if err != nil || a != b {
		t.Fatalf("fingerprint changed between loads: %s then %s (%v)", a, b, err)
	}
}

func TestPlainClientIsRefused(t *testing.T) {
	cfg, _, _ := LoadOrCreate(t.TempDir())
	addr := serve(t, cfg)
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.Write([]byte("TST plain old handshake"))
	raw.(*net.TCPConn).CloseWrite()
	b, _ := io.ReadAll(raw)
	if strings.Contains(string(b), "sucs") || len(b) > 7 && string(b[:3]) != "\x15\x03\x03" {
		t.Fatalf("a plaintext client got an answer other than a TLS alert: %q", b)
	}
}
