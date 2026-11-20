package nntp_test

import (
	"context"
	"crypto/tls"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Flonk/mazama/nntp"
	"github.com/Flonk/mazama/nntp/nntptest"
)

func TestParseURL(t *testing.T) {
	cases := []struct {
		in   string
		want nntp.Address
		str  string
	}{
		{"news://news.example.org", nntp.Address{Host: "news.example.org", Port: "119"}, "news://news.example.org/"},
		{"nntps://news.example.org/comp.lang.*", nntp.Address{Host: "news.example.org", Port: "563", TLS: true, Group: "comp.lang.*"}, "nntps://news.example.org/comp.lang.*"},
		{"news://news.example.org:563/comp.lang.go", nntp.Address{Host: "news.example.org", Port: "563", TLS: true, Group: "comp.lang.go"}, "nntps://news.example.org/comp.lang.go"},
		{"snews://h:9999/x.y", nntp.Address{Host: "h", Port: "9999", TLS: true, Group: "x.y"}, "nntps://h:9999/x.y"},
		{"news://h/<abc@example.com>", nntp.Address{Host: "h", Port: "119", MessageID: "abc@example.com"}, "news://h/abc@example.com"},
		{"news://h/a%2Fb%3Fc@example.com", nntp.Address{Host: "h", Port: "119", MessageID: "a/b?c@example.com"}, "news://h/a/b%3Fc@example.com"},
		{"nntp://h/comp.lang.go/42", nntp.Address{Host: "h", Port: "119", Group: "comp.lang.go", Number: 42}, "news://h/comp.lang.go/42"},
		{"news://h/comp.lang.go?before=100", nntp.Address{Host: "h", Port: "119", Group: "comp.lang.go", Before: 100}, "news://h/comp.lang.go?before=100"},
	}
	for _, c := range cases {
		got, err := nntp.ParseURL(c.in)
		if err != nil {
			t.Errorf("ParseURL(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseURL(%q) = %+v, want %+v", c.in, got, c.want)
		}
		s := got.String()
		if s != c.str {
			t.Errorf("String(%q) = %q, want %q", c.in, s, c.str)
		}
		if back, err := nntp.ParseURL(s); err != nil || back != got {
			t.Errorf("round trip %q: %+v, %v", s, back, err)
		}
	}
	for _, bad := range []string{"news:comp.lang.go", "http://h/", "news://h/g/x"} {
		if _, err := nntp.ParseURL(bad); err == nil {
			t.Errorf("ParseURL(%q) succeeded", bad)
		}
	}
}

func TestWildmat(t *testing.T) {
	cases := []struct {
		pat, name string
		want      bool
	}{
		{"*", "comp.lang.go", true},
		{"comp.*", "comp.lang.go", true},
		{"comp.*", "alt.comp", false},
		{"*go*", "comp.lang.go", true},
		{"comp.*,!comp.lang.*", "comp.lang.go", false},
		{"comp.*,!comp.lang.*", "comp.os.linux", true},
		{"!alt.*,alt.test", "alt.test", true},
	}
	for _, c := range cases {
		if got, err := nntp.MatchWildmat(c.pat, c.name); err != nil || got != c.want {
			t.Errorf("MatchWildmat(%q, %q) = %v, %v", c.pat, c.name, got, err)
		}
	}
	for pat, want := range map[string]string{"*": "", "comp.lang.*": "comp.lang."} {
		if got, ok := nntp.HierarchyPrefix(pat); !ok || got != want {
			t.Errorf("HierarchyPrefix(%q) = %q, %v", pat, got, ok)
		}
	}
	for _, pat := range []string{"*go*", "comp.*.misc.*", "comp.lang.go"} {
		if _, ok := nntp.HierarchyPrefix(pat); ok {
			t.Errorf("HierarchyPrefix(%q) ok", pat)
		}
	}
}

func TestParseArticle(t *testing.T) {
	raw := "From: =?ISO-8859-1?Q?J=FCrgen?= <j@example.com>\r\n" +
		"Subject: =?UTF-8?B?w5xiZXI=?= tests\r\n" +
		"Content-Type: text/plain; charset=ISO-8859-1\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"Gr=FC=DFe aus =\r\nWien\r\n"
	a, err := nntp.ParseArticle([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got := nntp.DisplayName(a.Header.Get("From")); got != "Jürgen" {
		t.Errorf("from = %q", got)
	}
	if got := nntp.DecodeHeader(a.Header.Get("Subject")); got != "Über tests" {
		t.Errorf("subject = %q", got)
	}
	if a.Body != "Grüße aus Wien\n" {
		t.Errorf("body = %q", a.Body)
	}

	multi := "From: x@example.com\n" +
		"Content-Type: multipart/alternative; boundary=BB\n\n" +
		"--BB\nContent-Type: text/html\n\n<p>html</p>\n" +
		"--BB\nContent-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64\n\naMOkbGxv\n" +
		"--BB--\n"
	a, err = nntp.ParseArticle([]byte(multi))
	if err != nil {
		t.Fatal(err)
	}
	if a.Body != "hällo" {
		t.Errorf("multipart body = %q", a.Body)
	}

	// Unlabelled 8-bit text is taken as Windows-1252.
	a, err = nntp.ParseArticle([]byte("From: x\n\ncaf\xe9\n"))
	if err != nil || a.Body != "café\n" {
		t.Errorf("8-bit body = %q, %v", a.Body, err)
	}

	if got := nntp.DisplayName("joe@example.com (Joe Bloggs)"); got != "Joe Bloggs" {
		t.Errorf("old-style from = %q", got)
	}
}

func TestNetrc(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeFile(t, home+"/.authinfo", "# news\nmachine other.example login a password b\n"+
		`machine news.example.org port 563 login "me" password "p w\"x"`+"\n")
	writeFile(t, home+"/.netrc", "default login anon password guest\n")
	lookup := nntp.NetrcLookup()
	if u, p, ok := lookup("news.example.org"); !ok || u != "me" || p != `p w"x` {
		t.Errorf("authinfo lookup = %q %q %v", u, p, ok)
	}
	if u, p, ok := lookup("elsewhere"); !ok || u != "anon" || p != "guest" {
		t.Errorf("default lookup = %q %q %v", u, p, ok)
	}
}

var articles = []string{
	"From: Ann <ann@example.com>\nSubject: hello\nDate: Mon, 02 Jan 2006 15:04:05 +0000\nMessage-ID: <1@example.com>\n\nfirst post\n.leading dot\n",
	"From: Bob <bob@example.com>\nSubject: Re: hello\nDate: Tue, 03 Jan 2006 15:04:05 +0000\nMessage-ID: <2@example.com>\nReferences: <1@example.com>\n\nreply\n",
}

func newServer(t *testing.T, cfg nntptest.Config) (*nntptest.Server, *nntp.Client) {
	cfg.Groups = []nntptest.Group{{Name: "test.group", Desc: "Testing", Articles: articles}}
	s := nntptest.New(t, cfg)
	return s, &nntp.Client{TLSConfig: &tls.Config{RootCAs: s.Roots}}
}

func addr(s *nntptest.Server, tlsOn bool) nntp.Address {
	return nntp.Address{Host: "127.0.0.1", Port: s.Port(), TLS: tlsOn}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestSessionPlain(t *testing.T) {
	s, c := newServer(t, nntptest.Config{})
	cn, err := c.Dial(ctx(t), addr(s, false))
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	if cn.Encrypted() {
		t.Error("plain server reported as encrypted")
	}
	groups, err := cn.ActiveGroups()
	if err != nil || len(groups) != 1 || groups[0].Name != "test.group" || groups[0].High != 2 {
		t.Fatalf("groups = %+v, %v", groups, err)
	}
	gi, err := cn.SelectGroup("test.group")
	if err != nil || gi.Count != 2 || gi.Low != 1 || gi.High != 2 {
		t.Fatalf("group = %+v, %v", gi, err)
	}
	ov, err := cn.Overviews(1, 2)
	if err != nil || len(ov) != 2 || ov[1].Subject != "Re: hello" || ov[1].MessageID != "2@example.com" {
		t.Fatalf("overview = %+v, %v", ov, err)
	}
	raw, err := cn.Article(nntp.Address{MessageID: "1@example.com"})
	if err != nil || !strings.Contains(string(raw), "\n.leading dot") {
		t.Fatalf("article = %q, %v", raw, err)
	}
	if _, err := cn.Article(nntp.Address{MessageID: "nope@example.com"}); err == nil {
		t.Error("missing article: no error")
	}
	if _, err := cn.SelectGroup("no.such"); err == nil {
		t.Error("missing group: no error")
	}
}

func TestSessionStartTLSLogin(t *testing.T) {
	s, c := newServer(t, nntptest.Config{StartTLS: true, User: "u", Pass: "p"})
	_, err := c.Dial(ctx(t), addr(s, false))
	if err != nil {
		t.Fatal(err)
	}
	// Without a login, a reader command asks for one.
	cn, _ := c.Dial(ctx(t), addr(s, false))
	if !cn.Encrypted() {
		t.Fatal("STARTTLS not used")
	}
	_, err = cn.ActiveGroups()
	var ae *nntp.AuthError
	if !errors.As(err, &ae) || ae.Failed {
		t.Fatalf("want AuthError, got %v", err)
	}
	cn.Close()

	c.SetLogin("127.0.0.1", "u", "wrong")
	cn, err = c.Dial(ctx(t), addr(s, false))
	if !errors.As(err, &ae) || !ae.Failed {
		t.Fatalf("want failed AuthError, got %v", err)
	}

	c.SetLogin("127.0.0.1", "u", "p")
	cn, err = c.Dial(ctx(t), addr(s, false))
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	if _, err := cn.ActiveGroups(); err != nil {
		t.Fatal(err)
	}
	for _, l := range s.Log() {
		if strings.Contains(l, "AUTHINFO PASS p") {
			t.Error("password logged by test server")
		}
	}
	if s.Logins != 1 {
		t.Errorf("logins = %d", s.Logins)
	}
}

func TestSessionImplicitTLS(t *testing.T) {
	s, c := newServer(t, nntptest.Config{TLS: true, User: "u", Pass: "p"})
	c.Lookup = func(host string) (string, string, bool) { return "u", "p", host == "127.0.0.1" }
	cn, err := c.Dial(ctx(t), addr(s, true))
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	if !cn.Encrypted() {
		t.Error("not encrypted")
	}
	if _, err := cn.ActiveGroups(); err != nil {
		t.Fatal(err)
	}

	// An untrusted certificate is refused.
	bad := &nntp.Client{}
	if _, err := bad.Dial(ctx(t), addr(s, true)); err == nil {
		t.Error("self-signed certificate accepted")
	}
}

func TestNoPasswordInTheClear(t *testing.T) {
	s, c := newServer(t, nntptest.Config{User: "u", Pass: "p"})
	c.SetLogin("127.0.0.1", "u", "p")
	cn, err := c.Dial(ctx(t), addr(s, false))
	if err != nil {
		t.Fatal(err)
	}
	defer cn.Close()
	_, err = cn.ActiveGroups()
	var ie *nntp.InsecureError
	if !errors.As(err, &ie) {
		t.Fatalf("want InsecureError, got %v", err)
	}
	for _, l := range s.Log() {
		if strings.HasPrefix(l, "AUTHINFO") {
			t.Errorf("sent %q over plaintext", l)
		}
	}
}

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestParseArticleMarkdown(t *testing.T) {
	a, err := nntp.ParseArticle([]byte("From: x\nContent-Type: text/markdown; charset=utf-8; variant=CommonMark\n\n# Hi\n"))
	if err != nil || a.ContentType != "text/markdown" || a.Body != "# Hi\n" {
		t.Errorf("markdown article = %+v, %v", a, err)
	}
	// multipart/alternative: markdown beats plain text, whatever the order.
	multi := "From: x\nContent-Type: multipart/alternative; boundary=BB\n\n" +
		"--BB\nContent-Type: text/markdown\n\n**hi**\n" +
		"--BB\nContent-Type: text/plain\n\nhi\n" +
		"--BB--\n"
	a, err = nntp.ParseArticle([]byte(multi))
	if err != nil || a.ContentType != "text/markdown" || a.Body != "**hi**" {
		t.Errorf("multipart markdown = %+v, %v", a, err)
	}
	a, err = nntp.ParseArticle([]byte("From: x\nContent-Type: text/plain\n\n**hi**\n"))
	if err != nil || a.ContentType != "text/plain" {
		t.Errorf("plain article = %+v, %v", a, err)
	}
	// Only an attachment: say so rather than fail.
	att := "From: x\nContent-Type: multipart/mixed; boundary=BB\n\n" +
		"--BB\nContent-Type: image/png\nContent-Transfer-Encoding: base64\n\naGk=\n--BB--\n"
	if a, err = nntp.ParseArticle([]byte(att)); err != nil || !strings.Contains(a.Body, "image/png attachment") {
		t.Errorf("attachment-only = %+v, %v", a, err)
	}
}
