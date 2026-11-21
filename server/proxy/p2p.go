package proxy

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
	"ehang.io/nps/lib/crypt"
	"ehang.io/nps/lib/file"
	"github.com/astaxie/beego/logs"
)

// P2PServer matches a visitor and a provider that asked for the same tunnel
// and tells each the other's public address. It is reachable by anyone who can
// send a UDP packet, so a packet is only acted on if its password is the md5 of
// a p2p tunnel that exists here. Anything else is dropped without a reply,
// without an entry and without a goroutine: to the sender the port looks
// closed, and a flood of guesses costs one lookup each.
type P2PServer struct {
	BaseServer
	p2pPort  int
	mu       sync.Mutex // guards p2p and the addresses in it
	p2p      map[string]*p2p
	listener *net.UDPConn

	// known says whether md5 is the password of a p2p tunnel. A field so a
	// test can stand in for the tunnel database.
	known func(md5 string) bool

	visitorWait int // seconds; a field so a test need not wait them out

	dropped    int64 // packets refused since the last report
	lastReport int64 // unix seconds
}

type p2p struct {
	providerAddr *net.UDPAddr
	providerAt   time.Time
}

// providerTTL is how long a provider's registration is believed. The provider
// registers when a visitor asks, and its client gives up after 15s, so an older
// one has most likely gone and its address would only send the visitor to
// nowhere.
const providerTTL = 30 * time.Second

// defaultVisitorWait is how many seconds a visitor waits for its provider to
// register.
const defaultVisitorWait = 20

// knownTTL is how long the set of p2p passwords is reused. Checking a packet
// against the tunnel table costs an md5 per tunnel, which a flood must not be
// able to make the read loop pay for every packet.
const knownTTL = 2 * time.Second

// p2pKnown is shared by the servers on the three ports, which see the same
// packets and would each rebuild the same set.
var p2pKnown p2pPasswords

func NewP2PServer(p2pPort int) *P2PServer {
	return &P2PServer{
		p2pPort: p2pPort,
		p2p:     make(map[string]*p2p),
		known:   p2pKnown.has,

		visitorWait: defaultVisitorWait,
	}
}

// p2pPasswords is the set of md5 passwords of the p2p tunnels, rebuilt at most
// every knownTTL.
type p2pPasswords struct {
	mu    sync.Mutex
	at    time.Time
	known map[string]bool
}

func (c *p2pPasswords) has(md5 string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known == nil || time.Since(c.at) > knownTTL {
		set := make(map[string]bool)
		file.GetDb().JsonDb.Tasks.Range(func(_, value interface{}) bool {
			if t := value.(*file.Tunnel); t.Mode == "p2p" {
				set[crypt.Md5(t.Password)] = true
			}
			return true
		})
		c.known, c.at = set, time.Now()
	}
	return c.known[md5]
}

func (s *P2PServer) Start() error {
	logs.Info("start p2p server port", s.p2pPort)
	var err error
	s.listener, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: s.p2pPort})
	if err != nil {
		return err
	}
	for {
		buf := common.BufPoolUdp.Get().([]byte)
		n, addr, err := s.listener.ReadFromUDP(buf)
		if err != nil {
			common.PutBufPoolUdp(buf)
			if strings.Contains(err.Error(), "use of closed network connection") {
				break
			}
			// a persistent error must not spin the loop
			time.Sleep(10 * time.Millisecond)
			continue
		}
		str := string(buf[:n])
		common.PutBufPoolUdp(buf)
		pw, role, ok := s.accept(str)
		if !ok {
			s.refuse(addr)
			continue
		}
		go s.handleP2P(addr, pw, role)
	}
	return nil
}

// accept parses what the client sends, common.GetWriteStr(md5, role), which
// ends each field with the separator: "<md5>*#*<role>*#*". It says whether the
// packet is worth answering.
func (s *P2PServer) accept(str string) (pw, role string, ok bool) {
	arr := strings.Split(str, common.CONN_DATA_SEQ)
	if len(arr) != 3 || arr[2] != "" {
		return
	}
	pw, role = arr[0], arr[1]
	if role != common.WORK_P2P_VISITOR && role != common.WORK_P2P_PROVIDER {
		return "", "", false
	}
	if len(pw) != 32 || !isHex(pw) || !s.known(pw) {
		return "", "", false
	}
	return pw, role, true
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// refuse counts a dropped packet and, at most once a minute, says so: one line
// is enough to notice a scan, and one line per packet would be the flood.
func (s *P2PServer) refuse(addr *net.UDPAddr) {
	atomic.AddInt64(&s.dropped, 1)
	now := time.Now().Unix()
	last := atomic.LoadInt64(&s.lastReport)
	if now-last < 60 || !atomic.CompareAndSwapInt64(&s.lastReport, last, now) {
		return
	}
	logs.Warn("p2p port %d: dropped %d packets with an unknown password since the last report, latest from %s",
		s.p2pPort, atomic.SwapInt64(&s.dropped, 0), addr)
}

func (s *P2PServer) handleP2P(addr *net.UDPAddr, pw, role string) {
	s.mu.Lock()
	v, ok := s.p2p[pw]
	if !ok {
		v = new(p2p)
		s.p2p[pw] = v
	}
	if role != common.WORK_P2P_VISITOR {
		v.providerAddr, v.providerAt = addr, time.Now()
	}
	s.mu.Unlock()
	logs.Trace("new p2p connection ,role %s ,local address %s", role, addr.String())
	if role != common.WORK_P2P_VISITOR {
		return
	}
	for i := s.visitorWait; i > 0; i-- {
		s.mu.Lock()
		provider := v.providerAddr
		if provider != nil && time.Since(v.providerAt) > providerTTL {
			provider = nil // registered long ago, most likely gone
		}
		s.mu.Unlock()
		if provider != nil {
			s.listener.WriteTo([]byte(provider.String()), addr)
			s.listener.WriteTo([]byte(addr.String()), provider)
			break
		}
		time.Sleep(time.Second)
	}
	s.mu.Lock()
	if s.p2p[pw] == v {
		delete(s.p2p, pw)
	}
	s.mu.Unlock()
}
