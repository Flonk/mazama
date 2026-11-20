// Package nntptest runs an in-process news server for tests.
package nntptest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/mail"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Group is a newsgroup; Articles are raw articles (headers, blank line,
// body) numbered from 1.
type Group struct {
	Name     string
	Desc     string
	Articles []string
}

// Config describes the server.
type Config struct {
	Groups []Group
	// TLS serves implicit TLS; StartTLS offers STARTTLS on a plain
	// connection. Neither means plaintext only.
	TLS, StartTLS bool
	// User and Pass, when set, are required before any reader command.
	User, Pass string
	// Public, when set with User, lets anyone read: only these groups
	// are listed until the client logs in.
	Public []string
}

// Server is a news server on 127.0.0.1.
type Server struct {
	Addr string // host:port
	// Roots trusts the server's self-signed certificate.
	Roots *x509.CertPool

	cfg  Config
	cert tls.Certificate

	mu       sync.Mutex
	Logins   int      // successful logins
	Commands []string // every command received, passwords redacted
}

// New starts a server that is shut down when the test ends.
func New(t *testing.T, cfg Config) *Server {
	t.Helper()
	s := &Server{cfg: cfg}
	s.makeCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s.Addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

// Port returns the port the server listens on.
func (s *Server) Port() string {
	_, p, _ := net.SplitHostPort(s.Addr)
	return p
}

// Log returns the commands received so far.
func (s *Server) Log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.Commands...)
}

func (s *Server) makeCert(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	s.cert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	s.Roots = x509.NewCertPool()
	s.Roots.AddCert(leaf)
}

type session struct {
	s         *Server
	conn      net.Conn
	r         *bufio.Reader
	w         *bufio.Writer
	encrypted bool
	authed    bool
	user      string
	group     *Group
}

func (s *Server) serve(c net.Conn) {
	defer func() { c.Close() }()
	ss := &session{s: s, conn: c}
	if s.cfg.TLS {
		tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{s.cert}})
		if err := tc.Handshake(); err != nil {
			return
		}
		ss.conn, ss.encrypted = tc, true
	}
	ss.r, ss.w = bufio.NewReader(ss.conn), bufio.NewWriter(ss.conn)
	ss.reply("200 nntptest ready")
	for {
		line, err := ss.r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if !ss.handle(line) {
			return
		}
	}
}

func (ss *session) reply(format string, args ...any) {
	fmt.Fprintf(ss.w, format+"\r\n", args...)
	ss.w.Flush()
}

func (ss *session) lines(lines []string) {
	for _, l := range lines {
		if strings.HasPrefix(l, ".") {
			l = "." + l
		}
		ss.w.WriteString(l + "\r\n")
	}
	ss.w.WriteString(".\r\n")
	ss.w.Flush()
}

func (ss *session) handle(line string) bool {
	cmd, arg, _ := strings.Cut(line, " ")
	cmd = strings.ToUpper(cmd)
	logged := line
	if strings.HasPrefix(strings.ToUpper(line), "AUTHINFO PASS") {
		logged = "AUTHINFO PASS ***"
	}
	ss.s.mu.Lock()
	ss.s.Commands = append(ss.s.Commands, logged)
	ss.s.mu.Unlock()

	cfg := ss.s.cfg
	needAuth := cfg.User != "" && !ss.authed && cfg.Public == nil
	switch cmd {
	case "QUIT":
		ss.reply("205 bye")
		return false
	case "MODE":
		ss.reply("200 reader mode")
	case "CAPABILITIES":
		caps := []string{"VERSION 2", "READER", "OVER"}
		if cfg.StartTLS && !ss.encrypted {
			caps = append(caps, "STARTTLS")
		}
		if cfg.User != "" && ss.encrypted {
			caps = append(caps, "AUTHINFO USER")
		}
		ss.reply("101 capabilities")
		ss.lines(caps)
	case "STARTTLS":
		if !cfg.StartTLS || ss.encrypted {
			ss.reply("502 no")
			return true
		}
		ss.reply("382 go ahead")
		tc := tls.Server(ss.conn, &tls.Config{Certificates: []tls.Certificate{ss.s.cert}})
		if err := tc.Handshake(); err != nil {
			return false
		}
		ss.conn, ss.encrypted = tc, true
		ss.r, ss.w = bufio.NewReader(tc), bufio.NewWriter(tc)
	case "AUTHINFO":
		sub, val, _ := strings.Cut(arg, " ")
		switch strings.ToUpper(sub) {
		case "USER":
			ss.user = val
			ss.reply("381 password please")
		case "PASS":
			if ss.user == cfg.User && val == cfg.Pass {
				ss.authed = true
				ss.s.mu.Lock()
				ss.s.Logins++
				ss.s.mu.Unlock()
				ss.reply("281 welcome")
			} else {
				ss.reply("481 bad login")
			}
		default:
			ss.reply("501 syntax")
		}
	case "LIST", "GROUP", "OVER", "XOVER", "ARTICLE":
		if needAuth {
			ss.reply("480 login first")
			return true
		}
		ss.reader(cmd, arg)
	default:
		ss.reply("500 unknown command")
	}
	return true
}

func (ss *session) reader(cmd, arg string) {
	groups := ss.s.cfg.Groups
	switch cmd {
	case "LIST":
		kw, pat, _ := strings.Cut(arg, " ")
		if pat == "" {
			pat = "*"
		}
		var out []string
		for _, g := range groups {
			if ok, _ := path.Match(pat, g.Name); !ok || !ss.visible(g.Name) {
				continue
			}
			switch strings.ToUpper(kw) {
			case "", "ACTIVE":
				out = append(out, fmt.Sprintf("%s %d %d y", g.Name, len(g.Articles), min(1, len(g.Articles))))
			case "NEWSGROUPS":
				out = append(out, g.Name+"\t"+g.Desc)
			}
		}
		ss.reply("215 list follows")
		ss.lines(out)
	case "GROUP":
		for i := range groups {
			if groups[i].Name == arg {
				ss.group = &groups[i]
				n := len(groups[i].Articles)
				ss.reply("211 %d %d %d %s", n, min(1, n), n, arg)
				return
			}
		}
		ss.reply("411 no such group")
	case "OVER", "XOVER":
		if ss.group == nil {
			ss.reply("412 no group selected")
			return
		}
		lo, hi := parseRange(arg, len(ss.group.Articles))
		var out []string
		for n := lo; n <= hi; n++ {
			msg, err := mail.ReadMessage(strings.NewReader(ss.group.Articles[n-1]))
			if err != nil {
				continue
			}
			h := msg.Header
			out = append(out, strings.Join([]string{strconv.Itoa(n), h.Get("Subject"), h.Get("From"),
				h.Get("Date"), h.Get("Message-ID"), h.Get("References"), "100", "3"}, "\t"))
		}
		if len(out) == 0 {
			ss.reply("423 no articles in range")
			return
		}
		ss.reply("224 overview follows")
		ss.lines(out)
	case "ARTICLE":
		art, ok := ss.find(arg)
		if !ok {
			ss.reply("430 no such article")
			return
		}
		ss.reply("220 article follows")
		ss.lines(strings.Split(strings.ReplaceAll(strings.TrimRight(art, "\n"), "\r\n", "\n"), "\n"))
	}
}

func (ss *session) find(arg string) (string, bool) {
	if strings.HasPrefix(arg, "<") {
		for _, g := range ss.s.cfg.Groups {
			for _, a := range g.Articles {
				if msg, err := mail.ReadMessage(strings.NewReader(a)); err == nil && msg.Header.Get("Message-ID") == arg {
					return a, true
				}
			}
		}
		return "", false
	}
	n, err := strconv.Atoi(arg)
	if err != nil || ss.group == nil || n < 1 || n > len(ss.group.Articles) {
		return "", false
	}
	return ss.group.Articles[n-1], true
}

func parseRange(arg string, n int) (int, int) {
	a, b, _ := strings.Cut(arg, "-")
	lo, _ := strconv.Atoi(a)
	hi, err := strconv.Atoi(b)
	if err != nil || hi > n {
		hi = n
	}
	return max(lo, 1), hi
}

func (ss *session) visible(group string) bool {
	cfg := ss.s.cfg
	if cfg.User == "" || ss.authed || cfg.Public == nil {
		return true
	}
	return slices.Contains(cfg.Public, group)
}
