// Package gemini implements a Gemini protocol client
// (https://geminiprotocol.net/docs/protocol-specification.gmi) with
// trust-on-first-use certificate pinning.
package gemini

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Status classes (first digit of the status code).
const (
	StatusInput     = 1
	StatusSuccess   = 2
	StatusRedirect  = 3
	StatusTempFail  = 4
	StatusPermFail  = 5
	StatusClientCrt = 6
)

const (
	defaultPort  = "1965"
	maxRedirects = 5
	maxResponse  = 16 << 20
	maxHeader    = 1024 + 3 + 2 // 2-digit status, space, 1024-byte meta, CRLF
)

// Response is a parsed Gemini response. Body is only set for 2x responses.
type Response struct {
	Status int
	Meta   string
	Body   []byte
	// URL is the URL that produced this response, after redirects.
	URL *url.URL
}

// Class returns the status class, e.g. 2 for 20.
func (r *Response) Class() int { return r.Status / 10 }

// Client fetches Gemini URLs.
type Client struct {
	// TOFU pins server certificates. Nil accepts any certificate, which is
	// only appropriate in tests.
	TOFU *TOFU
	// Timeout bounds a single request when the context has no deadline.
	Timeout time.Duration
}

// Get fetches u, following up to five same-protocol redirects.
func (c *Client) Get(ctx context.Context, u *url.URL) (*Response, error) {
	for range maxRedirects + 1 {
		resp, err := c.Fetch(ctx, u)
		if err != nil {
			return nil, err
		}
		if resp.Class() != StatusRedirect {
			return resp, nil
		}
		next, err := u.Parse(resp.Meta)
		if err != nil {
			return nil, fmt.Errorf("bad redirect target %q: %w", resp.Meta, err)
		}
		if next.Scheme != "gemini" {
			return nil, fmt.Errorf("redirect to non-gemini URL %s", next)
		}
		u = next
	}
	return nil, fmt.Errorf("too many redirects (last: %s)", u)
}

// Fetch performs a single request without following redirects.
func (c *Client) Fetch(ctx context.Context, u *url.URL) (*Response, error) {
	if u.Scheme != "gemini" {
		return nil, fmt.Errorf("not a gemini URL: %s", u)
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	hostport := net.JoinHostPort(host, port)

	req := *u
	req.Fragment = ""
	if req.Path == "" {
		req.Path = "/"
	}
	reqLine := req.String()
	if len(reqLine) > 1024 {
		return nil, errors.New("URL longer than 1024 bytes")
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config: &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS12,
			// Gemini relies on TOFU rather than CAs, and self-signed
			// certificates are the norm; VerifyConnection does the check.
			InsecureSkipVerify: true,
			VerifyConnection: func(cs tls.ConnectionState) error {
				if c.TOFU == nil {
					return nil
				}
				if len(cs.PeerCertificates) == 0 {
					return errors.New("server sent no certificate")
				}
				return c.TOFU.Check(hostport, cs.PeerCertificates[0])
			},
		},
	}
	conn, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	if _, err := io.WriteString(conn, reqLine+"\r\n"); err != nil {
		return nil, err
	}

	br := bufio.NewReaderSize(conn, 4096)
	status, meta, err := readHeader(br)
	if err != nil {
		return nil, err
	}
	resp := &Response{Status: status, Meta: meta, URL: u}
	if resp.Class() == StatusSuccess {
		body, err := io.ReadAll(io.LimitReader(br, maxResponse))
		// Many servers close without a TLS close_notify; treat that as EOF.
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		resp.Body = body
	}
	return resp, nil
}

func readHeader(br *bufio.Reader) (int, string, error) {
	var line []byte
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return 0, "", fmt.Errorf("reading response header: %w", err)
		}
		line = append(line, chunk...)
		if len(line) > maxHeader {
			return 0, "", errors.New("response header too long")
		}
		if !isPrefix {
			break
		}
	}
	s := string(line)
	if len(s) < 2 {
		return 0, "", fmt.Errorf("malformed response header %q", s)
	}
	status, err := strconv.Atoi(s[:2])
	if err != nil || status < 10 || status > 69 {
		return 0, "", fmt.Errorf("malformed status in header %q", s)
	}
	meta := strings.TrimSpace(s[2:])
	return status, meta, nil
}

// QueryEscape encodes user input for a Gemini query string. Gemini requires
// percent-encoding, so spaces become %20 rather than +.
func QueryEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}
