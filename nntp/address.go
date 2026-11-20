// Package nntp is a small read-only Usenet client: RFC 3977 commands, TLS
// (implicit on port 563, or STARTTLS from RFC 4642), AUTHINFO USER/PASS
// (RFC 4643), and news:// URLs (RFC 5538).
package nntp

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

const (
	DefaultPort    = "119"
	DefaultTLSPort = "563"
)

// Address is one news:// location: a server root (no Group or MessageID), a
// group listing (Group is a wildmat such as "comp.lang.*"), a group's
// articles (Group is a name), or one article (MessageID, or Group+Number).
type Address struct {
	Host string
	Port string
	// TLS means implicit TLS (nntps:// or port 563). Plain connections still
	// upgrade with STARTTLS when the server offers it.
	TLS       bool
	Group     string
	MessageID string // without angle brackets
	Number    int64  // article number within Group
	Before    int64  // group pages: show articles numbered below this
}

// IsWildmat reports whether a group name is a pattern rather than a group.
func IsWildmat(g string) bool {
	return strings.ContainsAny(g, "*?[,!\\")
}

// IsArticle reports whether the address names a single article.
func (a Address) IsArticle() bool { return a.MessageID != "" || a.Number > 0 }

// IsListing reports whether the address is a list of groups.
func (a Address) IsListing() bool {
	return !a.IsArticle() && (a.Group == "" || IsWildmat(a.Group))
}

// ParseURL parses news://, nntp://, nntps:// and snews:// URLs:
//
//	news://host/                 server root
//	news://host/comp.lang.*      groups matching a wildmat
//	news://host/comp.lang.go     a group's newest articles
//	news://host/id@example.com   an article by message-id
//	nntp://host/comp.lang.go/42  an article by number
func ParseURL(raw string) (Address, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Address{}, err
	}
	var a Address
	switch strings.ToLower(u.Scheme) {
	case "news", "nntp":
	case "nntps", "snews":
		a.TLS = true
	default:
		return Address{}, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return Address{}, fmt.Errorf("missing server in %q (use news://server/...)", raw)
	}
	a.Host, a.Port = u.Hostname(), u.Port()
	switch {
	case a.Port == "" && a.TLS:
		a.Port = DefaultTLSPort
	case a.Port == "":
		a.Port = DefaultPort
	case a.Port == DefaultTLSPort:
		a.TLS = true
	}

	p := strings.TrimPrefix(u.Path, "/")
	switch {
	case p == "":
	case strings.Contains(p, "@"):
		a.MessageID = strings.TrimSuffix(strings.TrimPrefix(p, "<"), ">")
	default:
		group, num, hasNum := strings.Cut(strings.TrimSuffix(p, "/"), "/")
		a.Group = group
		if hasNum {
			n, err := strconv.ParseInt(num, 10, 64)
			if err != nil || n <= 0 {
				return Address{}, fmt.Errorf("bad article number %q", num)
			}
			a.Number = n
		}
	}
	if b := u.Query().Get("before"); b != "" && a.Group != "" && !a.IsArticle() && !IsWildmat(a.Group) {
		n, err := strconv.ParseInt(b, 10, 64)
		if err != nil || n <= 0 {
			return Address{}, fmt.Errorf("bad article number %q", b)
		}
		a.Before = n
	}
	return a, nil
}

// String renders the address as a news:// or (for TLS) nntps:// URL.
func (a Address) String() string {
	scheme, def := "news", DefaultPort
	if a.TLS {
		scheme, def = "nntps", DefaultTLSPort
	}
	host := a.Host
	if a.Port != def {
		host = net.JoinHostPort(a.Host, a.Port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	u := url.URL{Scheme: scheme, Host: host, Path: "/"}
	switch {
	case a.MessageID != "":
		u.Path = "/" + a.MessageID
	case a.Group != "":
		u.Path = "/" + a.Group
		if a.Number > 0 {
			u.Path += "/" + strconv.FormatInt(a.Number, 10)
		} else if a.Before > 0 {
			u.RawQuery = "before=" + strconv.FormatInt(a.Before, 10)
		}
	}
	// Keep wildmats readable: news://h/comp.* rather than comp.%2A.
	u.RawPath = strings.ReplaceAll(u.EscapedPath(), "%2A", "*")
	return u.String()
}

// Server returns the address of the server root.
func (a Address) Server() Address {
	return Address{Host: a.Host, Port: a.Port, TLS: a.TLS}
}

func (a Address) hostport() string { return net.JoinHostPort(a.Host, a.Port) }
