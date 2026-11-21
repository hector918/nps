package proxy

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehang.io/nps/lib/common"
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

	dropped    int64 // packets refused since the last report
	lastReport int64 // unix seconds
}

type p2p struct {
	visitorAddr  *net.UDPAddr
	providerAddr *net.UDPAddr
}

func NewP2PServer(p2pPort int) *P2PServer {
	return &P2PServer{
		p2pPort: p2pPort,
		p2p:     make(map[string]*p2p),
		known: func(md5 string) bool {
			t := file.GetDb().GetTaskByMd5Password(md5)
			return t != nil && t.Mode == "p2p"
		},
	}
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
			if strings.Contains(err.Error(), "use of closed network connection") {
				break
			}
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
	if role == common.WORK_P2P_VISITOR {
		v.visitorAddr = addr
	} else {
		v.providerAddr = addr
	}
	s.mu.Unlock()
	logs.Trace("new p2p connection ,role %s ,local address %s", role, addr.String())
	if role != common.WORK_P2P_VISITOR {
		return
	}
	for i := 20; i > 0; i-- {
		s.mu.Lock()
		provider := v.providerAddr
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
