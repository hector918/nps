// Package bridgetls is the transport security of the bridge connection: every
// connection a client opens to the server, whatever it is for, is TLS 1.3
// from its first byte, and the client accepts only the server whose public key
// it has been told to expect.
//
// The server makes its own key and self-signed certificate the first time it
// starts and prints the fingerprint. There is no certificate authority to
// trust and no name to check, so the fingerprint is the whole of the server's
// identity: a client with the wrong one, or none, does not talk to anyone.
package bridgetls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// HandshakeTimeout bounds the TLS handshake on both ends, so a peer that
// connects and says nothing does not hold a goroutine and a socket.
const HandshakeTimeout = 10 * time.Second

const prefix = "sha256:"

// Fingerprint is the pin of a certificate: the SHA-256 of its public key,
// not of the certificate, so a certificate renewed on the same key keeps the
// pin that clients already hold.
func Fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return prefix + hex.EncodeToString(sum[:])
}

// ParsePin normalises what an operator typed into the form Fingerprint
// returns. It takes the prefix or leaves it off, and hex with or without
// colons, in either case.
func ParsePin(s string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(s))
	h = strings.TrimPrefix(h, prefix)
	h = strings.NewReplacer(":", "", " ", "").Replace(h)
	if b, err := hex.DecodeString(h); err != nil || len(b) != sha256.Size {
		return "", fmt.Errorf("server_fingerprint %q is not a SHA-256 fingerprint (%d hex digits expected)", s, 2*sha256.Size)
	}
	return prefix + h, nil
}

// LoadOrCreate returns the server's TLS configuration and the fingerprint
// clients must pin. The key and certificate live in dir/bridge.key and
// dir/bridge.crt; they are made on the first call and reused ever after, so
// the fingerprint does not change between restarts.
func LoadOrCreate(dir string) (*tls.Config, string, error) {
	keyPath := filepath.Join(dir, "bridge.key")
	crtPath := filepath.Join(dir, "bridge.crt")
	// The certificate is the last file to appear (see create), so its being
	// there means the pair is complete.
	if _, err := os.Stat(crtPath); os.IsNotExist(err) {
		if err := create(keyPath, crtPath); err != nil {
			return nil, "", err
		}
	} else if err != nil {
		return nil, "", err
	}
	cert, err := tls.LoadX509KeyPair(crtPath, keyPath)
	if err != nil {
		return nil, "", err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, "", err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, Fingerprint(leaf), nil
}

func create(keyPath, crtPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "nps bridge"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(100 * 365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDer, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return err
	}
	// Each file is written whole and then renamed into place, the key first:
	// a crash can leave a stray key, which the next start replaces, but never
	// a certificate without its key or a half-written file that the server
	// would then refuse to start on, every time, until someone deleted it.
	if err := writeAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDer}), 0o600); err != nil {
		return err
	}
	return writeAtomic(crtPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// ClientConfig verifies the server by pin alone. pin may be empty, in which
// case no server is accepted and the error says which fingerprint the server
// presented, the way ssh does, so that setting it is a copy and paste.
func ClientConfig(pin string) (*tls.Config, error) {
	if pin != "" {
		var err error
		if pin, err = ParsePin(pin); err != nil {
			return nil, err
		}
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// Chain and name checks are replaced by the pin below; there is no
		// chain to check, the certificate is self-signed.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("the server presented no certificate")
			}
			leaf, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			got := Fingerprint(leaf)
			if pin == "" {
				return fmt.Errorf("server_fingerprint is not set; the server presented %s", got)
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(pin)) != 1 {
				return fmt.Errorf("the server presented %s, not the pinned %s: wrong server, or someone is in the way", got, pin)
			}
			return nil
		},
	}, nil
}

// Client runs the client side of the handshake on c.
func Client(c net.Conn, cfg *tls.Config) (net.Conn, error) {
	return handshake(tls.Client(c, cfg), c)
}

// Server runs the server side of the handshake on c.
func Server(c net.Conn, cfg *tls.Config) (net.Conn, error) {
	return handshake(tls.Server(c, cfg), c)
}

func handshake(t *tls.Conn, c net.Conn) (net.Conn, error) {
	_ = c.SetDeadline(time.Now().Add(HandshakeTimeout))
	err := t.Handshake()
	_ = c.SetDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return t, nil
}

// TCPConn digs the TCP socket out from under c and any TLS layers over it, for
// the options that only the socket has, keepalive among them.
func TCPConn(c net.Conn) (*net.TCPConn, bool) {
	for {
		switch v := c.(type) {
		case *net.TCPConn:
			return v, true
		case interface{ NetConn() net.Conn }:
			c = v.NetConn()
		default:
			return nil, false
		}
	}
}
