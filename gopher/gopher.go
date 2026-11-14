// Package gopher implements the client side of RFC 1436: addressing,
// fetching a selector, and parsing menus.
package gopher

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

// Item types from RFC 1436 plus the common de-facto extensions.
const (
	TypeText   byte = '0'
	TypeMenu   byte = '1'
	TypeCSO    byte = '2'
	TypeError  byte = '3'
	TypeBinHex byte = '4'
	TypeDOS    byte = '5'
	TypeUUE    byte = '6'
	TypeSearch byte = '7'
	TypeTelnet byte = '8'
	TypeBinary byte = '9'
	TypeGIF    byte = 'g'
	TypeImage  byte = 'I'
	TypeHTML   byte = 'h'
	TypeInfo   byte = 'i'
	TypeSound  byte = 's'
	TypeDoc    byte = 'd'
	TypeTN3270 byte = 'T'
	TypeMirror byte = '+'
)

// maxResponse caps how much a single fetch reads, so a misbehaving server
// cannot exhaust memory.
const maxResponse = 16 << 20

// Address identifies one gopher resource.
type Address struct {
	Host     string
	Port     string
	Type     byte
	Selector string
	// Query is sent after a tab for type 7 (search) items.
	Query string
}

// ParseURL parses gopher://host[:port]/<type><selector>. A missing scheme is
// assumed to be gopher, and an empty path means the root menu.
func ParseURL(raw string) (Address, error) {
	if !strings.Contains(raw, "://") {
		raw = "gopher://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Address{}, err
	}
	if u.Scheme != "gopher" {
		return Address{}, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return Address{}, fmt.Errorf("missing host in %q", raw)
	}
	a := Address{Host: u.Hostname(), Port: u.Port(), Type: TypeMenu}
	if a.Port == "" {
		a.Port = "70"
	}
	path := strings.TrimPrefix(u.Path, "/")
	if path != "" {
		a.Type = path[0]
		a.Selector = path[1:]
	}
	if u.RawQuery != "" {
		if q, err := url.QueryUnescape(u.RawQuery); err == nil {
			a.Query = q
		}
	}
	return a, nil
}

// String renders the address as a gopher:// URL.
func (a Address) String() string {
	host := a.Host
	if a.Port != "" && a.Port != "70" {
		host = net.JoinHostPort(a.Host, a.Port)
	}
	s := fmt.Sprintf("gopher://%s/%c%s", host, a.Type, a.Selector)
	if a.Query != "" {
		s += "?" + url.QueryEscape(a.Query)
	}
	return s
}

// Fetch sends the selector and returns the full response body. For menus and
// text the trailing "." terminator line is left in place; ParseMenu and
// StripTerminator handle it.
func Fetch(ctx context.Context, a Address) ([]byte, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(a.Host, a.Port))
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(30 * time.Second))
	}
	// Close the connection if the context is cancelled mid-read.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	req := a.Selector
	if a.Query != "" {
		req += "\t" + a.Query
	}
	if _, err := io.WriteString(conn, req+"\r\n"); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(io.LimitReader(conn, maxResponse))
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return body, err
}

// Item is one line of a gopher menu.
type Item struct {
	Type     byte
	Display  string
	Selector string
	Host     string
	Port     string
}

// Address returns where the item points.
func (it Item) Address() Address {
	return Address{Host: it.Host, Port: it.Port, Type: it.Type, Selector: it.Selector}
}

// ExternalURL returns the target of an "h" item that uses the URL: selector
// convention (e.g. "URL:https://example.com"), or "" if it isn't one.
func (it Item) ExternalURL() string {
	if it.Type == TypeHTML && strings.HasPrefix(it.Selector, "URL:") {
		return strings.TrimPrefix(it.Selector, "URL:")
	}
	return ""
}

// Selectable reports whether the item is a link rather than decoration.
func (it Item) Selectable() bool {
	return it.Type != TypeInfo && it.Type != TypeError
}

// ParseMenu parses a gophermap. Malformed lines are kept as info lines so
// nothing the server sent is silently dropped.
func ParseMenu(data []byte) []Item {
	var items []Item
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "." {
			break
		}
		if line == "" {
			items = append(items, Item{Type: TypeInfo})
			continue
		}
		fields := strings.Split(line[1:], "\t")
		it := Item{Type: line[0], Display: fields[0]}
		if len(fields) >= 4 {
			it.Selector, it.Host, it.Port = fields[1], fields[2], strings.TrimSpace(fields[3])
		} else if it.Type != TypeInfo {
			// Not a valid link line; show it verbatim.
			it = Item{Type: TypeInfo, Display: line}
		}
		items = append(items, it)
	}
	return items
}

// StripTerminator removes the trailing "." line that ends a text response.
func StripTerminator(data []byte) []byte {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	trimmed := bytes.TrimRight(data, "\n")
	if bytes.HasSuffix(trimmed, []byte("\n.")) {
		return trimmed[:len(trimmed)-2]
	}
	if bytes.Equal(trimmed, []byte(".")) {
		return nil
	}
	return data
}
