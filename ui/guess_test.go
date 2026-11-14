package ui

import (
	"context"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHasScheme(t *testing.T) {
	for raw, want := range map[string]bool{
		"gemini://x":     true,
		"gopher://x/1/":  true,
		"https://x":      true,
		"x.org":          false,
		"x.org:70/1/foo": false,
		"x.org/a://b":    false,
		"://nothing":     false,
	} {
		if got := hasScheme(raw); got != want {
			t.Errorf("hasScheme(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestHintProtocol(t *testing.T) {
	for raw, want := range map[string]string{
		"example.org:70":            protoGopher,
		"example.org:1965/x":        protoGemini,
		"example.org:443":           protoHTTPS,
		"example.org:8080/app":      protoHTTP,
		"gopher.example.org":        protoGopher,
		"gemini.circumlunar.space":  protoGemini,
		"www.example.org":           protoHTTPS,
		"example.org/notes/a.gmi":   protoGemini,
		"example.org/index.html":    protoHTTPS,
		"example.org/1/phlog":       protoGopher,
		"example.org/0/about.txt":   protoGopher,
		"example.org":               "",
		"example.org/s/some-slug":   "",
		"example.org:7070/whatever": "",
	} {
		u, err := url.Parse("//" + raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := hintProtocol(u); got != want {
			t.Errorf("hintProtocol(%q) = %q, want %q", raw, got, want)
		}
	}
}

// listen returns a port that accepts connections, and one that refuses them.
func ports(t *testing.T) (open, closed string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, closed, _ = net.SplitHostPort(dead.Addr().String())
	dead.Close()
	_, open, _ = net.SplitHostPort(ln.Addr().String())
	return open, closed
}

func withProbePorts(t *testing.T, gem, goph, https string) {
	t.Helper()
	saved := probePorts
	probePorts = map[string]string{protoGemini: gem, protoGopher: goph, protoHTTPS: https}
	t.Cleanup(func() { probePorts = saved })
}

func TestProbeProtocolPreference(t *testing.T) {
	open, closed := ports(t)
	ctx := context.Background()

	withProbePorts(t, closed, open, closed)
	if got, err := probeProtocol(ctx, "127.0.0.1"); err != nil || got != protoGopher {
		t.Errorf("only gopher open: got %q, %v", got, err)
	}

	withProbePorts(t, open, open, open)
	if got, err := probeProtocol(ctx, "127.0.0.1"); err != nil || got != protoGemini {
		t.Errorf("all open: got %q, %v, want gemini", got, err)
	}

	withProbePorts(t, closed, closed, open)
	if got, err := probeProtocol(ctx, "127.0.0.1"); err != nil || got != protoHTTPS {
		t.Errorf("only https open: got %q, %v", got, err)
	}

	withProbePorts(t, closed, closed, closed)
	if _, err := probeProtocol(ctx, "127.0.0.1"); err == nil || !strings.Contains(err.Error(), "doesn't answer") {
		t.Errorf("nothing open: err = %v", err)
	}
}

func TestProbeExplicitPort(t *testing.T) {
	srv := geminiSite(t)
	if got, err := probePort(context.Background(), srv.Addr); err != nil || got != protoGemini {
		t.Errorf("TLS server: got %q, %v, want gemini", got, err)
	}

	pages := map[string]string{}
	host, port := fakeServer(t, pages)
	saved := handshakeTimeout
	handshakeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { handshakeTimeout = saved })
	if got, err := probePort(context.Background(), net.JoinHostPort(host, port)); err != nil || got != protoGopher {
		t.Errorf("plain server: got %q, %v, want gopher", got, err)
	}
}

func TestTypedAddresses(t *testing.T) {
	// Web: port 443 is a hint, so nothing is probed or fetched.
	m := newTestModel(t, Location{Web: mustURLui(t, "https://placeholder.invalid/")})
	m = typeGoto(m, "example.invalid:443/blog")
	v := plainView(m)
	if !strings.Contains(v, "Outgoing web link") || !strings.Contains(v, "Open in browser") {
		t.Fatalf("expected the web interstitial (status %q):\n%s", m.status, v)
	}
	if m.cur.links[0] != "https://example.invalid:443/blog" {
		t.Errorf("open link = %q", m.cur.links[0])
	}
	if !strings.Contains(m.cur.links[1], "gemini://example.invalid") || !strings.Contains(m.cur.links[2], "gopher://example.invalid") {
		t.Errorf("retry links = %v", m.cur.links[1:])
	}

	// Gemini by path hint, on the test server's port.
	srv := geminiSite(t)
	m = typeGoto(m, srv.Addr+"/index.gmi")
	if !strings.Contains(plainView(m), "Welcome to the") {
		t.Errorf("gemini guess failed (status %q):\n%s", m.status, plainView(m))
	}

	// Gemini by probing the explicit port (no hint at all).
	m = typeGoto(m, srv.Addr)
	if m.cur.addr.Gemini == nil {
		t.Errorf("explicit-port probe should pick gemini, got %s (status %q)", m.cur.addr, m.status)
	}
}

func mustURLui(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// typeGoto opens the ":" prompt, types s and submits it.
func typeGoto(m Model, s string) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(":")}) // skip the blink timer
	m = next.(Model)
	m.input.SetValue("")
	m = typeText(m, s)
	return key(m, enter)
}
