// Package geminitest runs an in-process Gemini server for tests.
package geminitest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Handler answers one request with a full header line (e.g. "20 text/gemini")
// and a body, which is only sent for 2x responses.
type Handler func(u *url.URL) (header, body string)

// Server is a TLS Gemini server on 127.0.0.1 with a self-signed certificate.
type Server struct {
	Addr string // host:port

	mu   sync.Mutex
	cert tls.Certificate
}

// New starts a server that is shut down when the test ends.
func New(t *testing.T, h Handler) *Server {
	t.Helper()
	s := &Server{}
	s.RotateCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			return &s.cert, nil
		},
	})
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
			go func(c net.Conn) {
				defer c.Close()
				line, err := bufio.NewReader(c).ReadString('\n')
				if err != nil {
					return
				}
				u, err := url.Parse(strings.TrimRight(line, "\r\n"))
				if err != nil {
					c.Write([]byte("59 bad request\r\n"))
					return
				}
				header, body := h(u)
				c.Write([]byte(header + "\r\n"))
				if strings.HasPrefix(header, "2") {
					c.Write([]byte(body))
				}
			}(c)
		}
	}()
	return s
}

// URL returns gemini://<addr><path>.
func (s *Server) URL(path string) string {
	return "gemini://" + s.Addr + path
}

// RotateCert switches to a freshly generated key and certificate, which a
// TOFU client should reject.
func (s *Server) RotateCert(t *testing.T) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.cert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	s.mu.Unlock()
}
