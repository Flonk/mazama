package ui

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"
	"time"
)

// Protocol guessing for addresses typed without a scheme (command line and
// the ":" prompt). Hints from the address itself are tried first; if there
// are none, the host is probed.

const (
	protoGopher = "gopher"
	protoGemini = "gemini"
	protoHTTPS  = "https"
	protoHTTP   = "http"
	// news:// still upgrades to TLS with STARTTLS when the server offers it.
	protoNews    = "news"
	protoNewsTLS = "nntps"
)

// isNewsHost reports whether a host name suggests a Usenet server.
func isNewsHost(host string) bool {
	host = strings.ToLower(host)
	return strings.HasPrefix(host, "news.") || strings.HasPrefix(host, "usenet.")
}

// probeOrder is the preference when several protocols answer: this is a
// small-web client, so gopher and gemini win over the web, gopher first.
var probeOrder = []string{protoGopher, protoGemini, protoHTTPS}

// probePorts are the ports probed per protocol. Tests point them elsewhere.
var probePorts = map[string]string{
	protoGemini: "1965",
	protoGopher: "70",
	protoNews:   "119",
	protoHTTPS:  "443",
}

// probeTimeout bounds a whole probe; handshakeTimeout bounds the TLS check on
// an explicit port (a gopher server just waits for a selector line, so a
// failed handshake usually shows up as a timeout).
var (
	probeTimeout     = 3 * time.Second
	handshakeTimeout = 1500 * time.Millisecond
	probeGrace       = 750 * time.Millisecond
)

// hasScheme reports whether raw starts with "<scheme>://".
func hasScheme(raw string) bool {
	i := strings.Index(raw, "://")
	if i <= 0 {
		return false
	}
	for _, r := range raw[:i] {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

// hintProtocol guesses from the address alone, or returns "".
func hintProtocol(u *url.URL) string {
	switch u.Port() {
	case "70":
		return protoGopher
	case "1965":
		return protoGemini
	case "443":
		return protoHTTPS
	case "80", "8080", "8000":
		return protoHTTP
	case "119":
		return protoNews
	case "563":
		return protoNewsTLS
	}

	host := strings.ToLower(u.Hostname())
	switch {
	case strings.HasPrefix(host, "nntp."):
		return protoNews
	case strings.HasPrefix(host, "gopher."):
		return protoGopher
	case strings.HasPrefix(host, "gemini."):
		return protoGemini
	case strings.HasPrefix(host, "www."):
		return protoHTTPS
	}

	switch strings.ToLower(path.Ext(u.Path)) {
	case ".gmi", ".gemini":
		return protoGemini
	case ".html", ".htm", ".php", ".asp", ".aspx":
		return protoHTTPS
	}
	// Gopher URL paths start with an item type: /0/..., /1/..., /7/...
	if p := u.Path; len(p) >= 3 && p[0] == '/' && strings.ContainsRune("0179", rune(p[1])) && p[2] == '/' {
		return protoGopher
	}
	return ""
}

// probeProtocol checks which of the protocol ports accept a TCP connection
// and returns the most preferred one that does. It answers as soon as no
// pending probe could beat a successful one.
func probeProtocol(ctx context.Context, host string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	order := probeOrder
	if isNewsHost(host) {
		// news.example.org is usually a news server, but sometimes a
		// website (news.ycombinator.com): try news first, then the rest.
		order = append([]string{protoNews}, probeOrder...)
	}

	// Resolve once up front so an unknown host gets a clear error instead of
	// three failed connects.
	if _, err := net.DefaultResolver.LookupHost(ctx, host); err != nil {
		return "", fmt.Errorf("can't find host %s", host)
	}

	type result struct {
		proto string
		ok    bool
	}
	results := make(chan result, len(order))
	for _, proto := range order {
		go func() {
			var d net.Dialer
			c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, probePorts[proto]))
			if err == nil {
				c.Close()
			}
			results <- result{proto, err == nil}
		}()
	}

	state := map[string]*bool{}
	decide := func() (string, bool) {
		for _, proto := range order {
			ok := state[proto]
			if ok == nil {
				return "", false // still waiting on a more preferred protocol
			}
			if *ok {
				return proto, true
			}
		}
		return "", true // all failed
	}
	// Once a less preferred protocol has answered, only wait probeGrace more
	// for the preferred ones: hosts that silently drop packets on closed
	// ports would otherwise always cost the full timeout.
	var grace <-chan time.Time
	for range order {
		select {
		case r := <-results:
			state[r.proto] = &r.ok
			if proto, done := decide(); done {
				if proto == "" {
					return "", fmt.Errorf("%s doesn't answer on the gopher, gemini or https ports", host)
				}
				return proto, nil
			}
			if r.ok && grace == nil {
				grace = time.After(probeGrace)
			}
			continue
		case <-grace:
		case <-ctx.Done():
			if errors.Is(context.Cause(ctx), context.Canceled) {
				return "", ctx.Err()
			}
		}
		// Timed out (or grace over): whatever hasn't answered counts as failed.
		f := false
		for _, proto := range order {
			if state[proto] == nil {
				state[proto] = &f
			}
		}
		proto, _ := decide()
		if proto == "" {
			return "", fmt.Errorf("%s didn't answer on the gopher, gemini or https ports", host)
		}
		return proto, nil
	}
	return "", errors.New("unreachable")
}

// probePort guesses the protocol on one explicit port: a TLS server is taken
// to be gemini (the small-web choice; an https site on an odd port is rare),
// a plain TCP server to be gopher. The handshake skips verification and never
// touches the TOFU store; the real request does that.
func probePort(ctx context.Context, hostport string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return "", fmt.Errorf("%s doesn't answer: %w", hostport, err)
	}
	defer raw.Close()
	if dl, ok := ctx.Deadline(); ok {
		raw.SetDeadline(dl)
	}
	host, _, _ := net.SplitHostPort(hostport)
	tc := tls.Client(raw, &tls.Config{ServerName: host, InsecureSkipVerify: true})
	hctx, hcancel := context.WithTimeout(ctx, handshakeTimeout)
	defer hcancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		if errors.Is(context.Cause(ctx), context.Canceled) {
			return "", ctx.Err()
		}
		return protoGopher, nil
	}
	return protoGemini, nil
}

// guessLocation resolves user input into a Location, guessing the protocol
// when there is no scheme.
func guessLocation(ctx context.Context, raw string) (Location, error) {
	raw = strings.TrimSpace(raw)
	if hasScheme(raw) {
		return parseAnyLocation(raw)
	}
	u, err := url.Parse("//" + raw)
	if err != nil {
		return Location{}, err
	}
	if u.Hostname() == "" {
		return Location{}, fmt.Errorf("missing host in %q", raw)
	}
	proto := hintProtocol(u)
	switch {
	case proto != "":
	case u.Port() != "":
		// A non-standard port says nothing about the protocol, and probing
		// the standard ports would test a different service.
		if proto, err = probePort(ctx, u.Host); err != nil {
			return Location{}, err
		}
	default:
		if proto, err = probeProtocol(ctx, u.Hostname()); err != nil {
			return Location{}, err
		}
	}
	return parseAnyLocation(proto + "://" + raw)
}

// parseAnyLocation is ParseLocation plus web URLs, which become the
// "outgoing link" page.
func parseAnyLocation(raw string) (Location, error) {
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(raw)
		if err != nil {
			return Location{}, err
		}
		return Location{Web: u}, nil
	}
	return ParseLocation(raw)
}

// webPage is the interstitial shown for a typed web address: mazama doesn't
// render the web, so it offers to hand the link to the browser, and to retry
// as gemini or gopher in case the guess was wrong.
func webPage(u *url.URL) *page {
	hostPath := u.Host + u.EscapedPath()
	md := "# Outgoing web link\n\n" +
		"`" + u.String() + "`\n\n" +
		"This looks like a website. mazama only browses gopher and gemini, " +
		"so it can hand this link to your browser instead.\n\n" +
		"→ [Open in browser](<" + u.String() + ">)\n\n" +
		"Not what you meant?\\\n" +
		"→ [Try it as gemini](<gemini://" + hostPath + ">)\\\n" +
		"→ [Try it as gopher](<gopher://" + u.Host + ">)\n"
	return &page{addr: Location{Web: u}, kind: textPage, raw: md, markdown: true, selLink: -1}
}
