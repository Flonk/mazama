package nntp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"time"
)

// maxResponse caps one multi-line response (a full group list on a big
// server is a few MB).
const maxResponse = 64 << 20

// groupListTTL is how long a server's group list is reused before it is
// fetched again.
const groupListTTL = time.Hour

// AuthError means the server wants a login: none is known for Host, or
// (Failed) the one that was sent was rejected.
type AuthError struct {
	Host   string
	Failed bool
}

func (e *AuthError) Error() string {
	if e.Failed {
		return "login to " + e.Host + " was rejected"
	}
	return e.Host + " requires a login"
}

// InsecureError means the server wants a login but the connection isn't
// encrypted, so mazama won't send the password.
type InsecureError struct{ Host string }

func (e *InsecureError) Error() string {
	return e.Host + " requires a login but offers no TLS; refusing to send the password unencrypted (try nntps://" + e.Host + ")"
}

// Client opens connections to news servers. It remembers logins and caches
// each server's group list. The zero value is ready to use.
type Client struct {
	// TLSConfig is the base TLS configuration; nil uses the system roots.
	TLSConfig *tls.Config
	// Lookup finds a stored login for a host (e.g. NetrcLookup). Logins
	// set with SetLogin take precedence.
	Lookup func(host string) (user, pass string, ok bool)

	mu     sync.Mutex
	logins map[string][2]string
	lists  map[string]cachedList
}

type cachedList struct {
	groups  []Group
	fetched time.Time
}

// SetLogin remembers a login for host for the rest of the session.
func (c *Client) SetLogin(host, user, pass string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.logins == nil {
		c.logins = map[string][2]string{}
	}
	c.logins[host] = [2]string{user, pass}
}

func (c *Client) forgetLogin(host string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.logins, host)
}

func (c *Client) login(host string) (user, pass string, ok bool) {
	c.mu.Lock()
	l, ok := c.logins[host]
	c.mu.Unlock()
	if ok {
		return l[0], l[1], true
	}
	if c.Lookup != nil {
		return c.Lookup(host)
	}
	return "", "", false
}

func (c *Client) tlsConfig(host string) *tls.Config {
	cfg := &tls.Config{}
	if c.TLSConfig != nil {
		cfg = c.TLSConfig.Clone()
	}
	cfg.ServerName = host
	return cfg
}

// Conn is one session with a news server. Close it when done.
type Conn struct {
	c         *Client
	addr      Address
	raw       net.Conn
	text      *textproto.Conn
	encrypted bool
	authed    bool
	user      string
	stop      func() bool
}

// Dial connects to the server in a, encrypts the connection (implicit TLS,
// or STARTTLS when offered), and logs in if a login is known.
func (c *Client) Dial(ctx context.Context, a Address) (*Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	raw, err := d.DialContext(ctx, "tcp", a.hostport())
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		raw.SetDeadline(dl)
	} else {
		raw.SetDeadline(time.Now().Add(30 * time.Second))
	}
	cn := &Conn{c: c, addr: a, raw: raw}
	// Closing the socket unblocks any read if the context is cancelled.
	cn.stop = context.AfterFunc(ctx, func() { raw.Close() })
	if err := cn.setup(ctx); err != nil {
		cn.stop()
		raw.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	return cn, nil
}

func (cn *Conn) setup(ctx context.Context) error {
	var conn net.Conn = cn.raw
	if cn.addr.TLS {
		tc := tls.Client(cn.raw, cn.c.tlsConfig(cn.addr.Host))
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("TLS handshake with %s: %w", cn.addr.Host, err)
		}
		conn, cn.encrypted = tc, true
	}
	cn.text = textproto.NewConn(conn)

	code, msg, err := cn.text.ReadCodeLine(2)
	if err != nil {
		if code != 0 {
			return fmt.Errorf("%s: %d %s", cn.addr.Host, code, msg)
		}
		return err
	}
	// Mode-switching servers (INN) only offer reader commands, and
	// sometimes STARTTLS, after MODE READER. Others reply 5xx; that's fine.
	if _, _, err := cn.cmd("MODE READER"); err != nil {
		return err
	}
	if !cn.encrypted {
		if caps, err := cn.capabilities(); err == nil && caps["STARTTLS"] {
			if err := cn.startTLS(ctx); err != nil {
				return err
			}
		}
	}
	if cn.encrypted {
		if user, pass, ok := cn.c.login(cn.addr.Host); ok {
			return cn.auth(user, pass)
		}
	}
	return nil
}

// cmd sends a command and reads the status line, without interpreting it.
func (cn *Conn) cmd(format string, args ...any) (int, string, error) {
	if err := cn.text.PrintfLine(format, args...); err != nil {
		return 0, "", err
	}
	line, err := cn.text.ReadLine()
	if err != nil {
		return 0, "", err
	}
	code, msg, _ := strings.Cut(line, " ")
	n, err := strconv.Atoi(code)
	if err != nil || len(code) != 3 {
		return 0, "", fmt.Errorf("malformed response %q", line)
	}
	return n, msg, nil
}

// do sends a command and expects status want. A 480 (login required) is
// answered by logging in and retrying once.
func (cn *Conn) do(want int, format string, args ...any) (string, error) {
	code, msg, err := cn.cmd(format, args...)
	if err != nil {
		return "", err
	}
	if code == 480 && !cn.authed {
		if !cn.encrypted {
			return "", &InsecureError{Host: cn.addr.Host}
		}
		user, pass, ok := cn.c.login(cn.addr.Host)
		if !ok {
			return "", &AuthError{Host: cn.addr.Host}
		}
		if err := cn.auth(user, pass); err != nil {
			return "", err
		}
		if code, msg, err = cn.cmd(format, args...); err != nil {
			return "", err
		}
	}
	if code != want {
		return "", &textproto.Error{Code: code, Msg: msg}
	}
	return msg, nil
}

func (cn *Conn) capabilities() (map[string]bool, error) {
	if _, err := cn.do(101, "CAPABILITIES"); err != nil {
		return nil, err
	}
	lines, err := cn.text.ReadDotLines()
	if err != nil {
		return nil, err
	}
	caps := map[string]bool{}
	for _, l := range lines {
		if f := strings.Fields(l); len(f) > 0 {
			caps[strings.ToUpper(f[0])] = true
		}
	}
	return caps, nil
}

func (cn *Conn) startTLS(ctx context.Context) error {
	code, _, err := cn.cmd("STARTTLS")
	if err != nil {
		return err
	}
	if code != 382 {
		// Advertised but refused: carry on unencrypted; a login will
		// still be refused by do.
		return nil
	}
	tc := tls.Client(cn.raw, cn.c.tlsConfig(cn.addr.Host))
	if err := tc.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("STARTTLS with %s: %w", cn.addr.Host, err)
	}
	cn.text, cn.encrypted = textproto.NewConn(tc), true
	return nil
}

func (cn *Conn) auth(user, pass string) error {
	if strings.ContainsAny(user+pass, "\r\n") {
		return errors.New("login contains a line break")
	}
	code, msg, err := cn.cmd("AUTHINFO USER %s", user)
	if err != nil {
		return err
	}
	if code == 381 {
		if code, msg, err = cn.cmd("AUTHINFO PASS %s", pass); err != nil {
			return err
		}
	}
	switch code {
	case 281:
		cn.authed, cn.user = true, user
		return nil
	case 481, 482:
		cn.c.forgetLogin(cn.addr.Host)
		return &AuthError{Host: cn.addr.Host, Failed: true}
	default:
		return fmt.Errorf("login to %s: %d %s", cn.addr.Host, code, msg)
	}
}

// Encrypted reports whether the session runs over TLS.
func (cn *Conn) Encrypted() bool { return cn.encrypted }

// LoggedIn reports whether the session has logged in.
func (cn *Conn) LoggedIn() bool { return cn.authed }

// Close says goodbye and closes the connection.
func (cn *Conn) Close() error {
	cn.stop()
	cn.raw.SetDeadline(time.Now().Add(time.Second))
	cn.text.PrintfLine("QUIT")
	return cn.text.Close()
}

func (cn *Conn) readBody() ([]byte, error) {
	r := cn.text.DotReader()
	data, err := io.ReadAll(io.LimitReader(r, maxResponse))
	if err != nil {
		return nil, err
	}
	if len(data) == maxResponse {
		return nil, errors.New("response too large")
	}
	return data, nil
}

// Group is one line of LIST ACTIVE.
type Group struct {
	Name      string
	High, Low int64
	Status    string // y: posting allowed, n: no posting, m: moderated, ...
}

// ActiveGroups returns every group on the server, sorted as the server sent
// it. The list is cached per server and login for an hour: servers often
// show more groups after logging in.
func (cn *Conn) ActiveGroups() ([]Group, error) {
	key := cn.addr.hostport() + " " + cn.user
	c := cn.c
	c.mu.Lock()
	cached, ok := c.lists[key]
	c.mu.Unlock()
	if ok && time.Since(cached.fetched) < groupListTTL {
		return cached.groups, nil
	}

	if _, err := cn.do(215, "LIST ACTIVE"); err != nil {
		return nil, err
	}
	data, err := cn.readBody()
	if err != nil {
		return nil, err
	}
	var groups []Group
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		g := Group{Name: f[0]}
		g.High, _ = strconv.ParseInt(f[1], 10, 64)
		g.Low, _ = strconv.ParseInt(f[2], 10, 64)
		if len(f) > 3 {
			g.Status = f[3]
		}
		groups = append(groups, g)
	}

	c.mu.Lock()
	if c.lists == nil {
		c.lists = map[string]cachedList{}
	}
	c.lists[key] = cachedList{groups, time.Now()}
	c.mu.Unlock()
	return groups, nil
}

// Descriptions returns the descriptions of the groups matching a wildmat.
func (cn *Conn) Descriptions(wildmat string) (map[string]string, error) {
	if _, err := cn.do(215, "LIST NEWSGROUPS %s", wildmat); err != nil {
		return nil, err
	}
	data, err := cn.readBody()
	if err != nil {
		return nil, err
	}
	descs := map[string]string{}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		i := strings.IndexAny(line, " \t")
		if i <= 0 {
			continue
		}
		descs[line[:i]] = toUTF8(strings.Join(strings.Fields(line[i:]), " "))
	}
	return descs, nil
}

// GroupInfo is the reply to GROUP.
type GroupInfo struct {
	Name             string
	Count, Low, High int64
}

// SelectGroup makes name the current group.
func (cn *Conn) SelectGroup(name string) (GroupInfo, error) {
	msg, err := cn.do(211, "GROUP %s", name)
	if err != nil {
		var te *textproto.Error
		if errors.As(err, &te) && te.Code == 411 {
			return GroupInfo{}, fmt.Errorf("%w %s", ErrNoSuchGroup, name)
		}
		return GroupInfo{}, err
	}
	f := strings.Fields(msg)
	if len(f) < 3 {
		return GroupInfo{}, fmt.Errorf("malformed GROUP reply %q", msg)
	}
	gi := GroupInfo{Name: name}
	gi.Count, _ = strconv.ParseInt(f[0], 10, 64)
	gi.Low, _ = strconv.ParseInt(f[1], 10, 64)
	gi.High, _ = strconv.ParseInt(f[2], 10, 64)
	return gi, nil
}

// Overview is one article's line from OVER.
type Overview struct {
	Number     int64
	Subject    string
	From       string
	Date       string
	MessageID  string // without angle brackets
	References string
}

// Overviews returns the overview of articles lo..hi in the current group.
func (cn *Conn) Overviews(lo, hi int64) ([]Overview, error) {
	_, err := cn.do(224, "OVER %d-%d", lo, hi)
	var te *textproto.Error
	if errors.As(err, &te) && te.Code == 500 {
		_, err = cn.do(224, "XOVER %d-%d", lo, hi)
	}
	if errors.As(err, &te) && te.Code == 423 {
		return nil, nil // no articles in range
	}
	if err != nil {
		return nil, err
	}
	data, err := cn.readBody()
	if err != nil {
		return nil, err
	}
	var out []Overview
	for line := range strings.SplitSeq(string(data), "\n") {
		f := strings.Split(strings.TrimRight(line, "\r"), "\t")
		if len(f) < 6 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, Overview{
			Number:     n,
			Subject:    DecodeHeader(f[1]),
			From:       DecodeHeader(f[2]),
			Date:       f[3],
			MessageID:  strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(f[4]), "<"), ">"),
			References: f[5],
		})
	}
	return out, nil
}

// Article fetches the raw article (headers and body) a names, by message-id
// or by group and number.
func (cn *Conn) Article(a Address) ([]byte, error) {
	var err error
	if a.MessageID != "" {
		_, err = cn.do(220, "ARTICLE <%s>", a.MessageID)
	} else {
		if _, err = cn.SelectGroup(a.Group); err != nil {
			return nil, err
		}
		_, err = cn.do(220, "ARTICLE %d", a.Number)
	}
	var te *textproto.Error
	if errors.As(err, &te) && (te.Code == 430 || te.Code == 423) {
		return nil, errors.New("no such article (it may have expired)")
	}
	if err != nil {
		return nil, err
	}
	return cn.readBody()
}

// ErrNoSuchGroup is returned (wrapped) by SelectGroup for unknown groups.
var ErrNoSuchGroup = errors.New("no such group")
