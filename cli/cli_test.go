package cli

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Flonk/mazama/gemini"
	"github.com/Flonk/mazama/gemini/geminitest"
	"github.com/Flonk/mazama/nntp"
	"github.com/Flonk/mazama/nntp/nntptest"
	"github.com/Flonk/mazama/ui"
)

// tester runs commands against one session in a temp directory.
type tester struct {
	t       *testing.T
	gem     *gemini.Client
	news    *nntp.Client
	saved   map[string][2]string
	mu      sync.Mutex
	session string
}

func newTester(t *testing.T) *tester {
	t.Helper()
	dir := t.TempDir()
	old := sessionDir
	sessionDir = func() (string, error) { return filepath.Join(dir, "sessions"), nil }
	t.Cleanup(func() { sessionDir = old })
	tofu, err := gemini.LoadTOFU(filepath.Join(dir, "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	tt := &tester{t: t, gem: &gemini.Client{TOFU: tofu}, saved: map[string][2]string{}, session: "test"}
	// The "keyring": logins saved by one run are found by the next.
	tt.news = &nntp.Client{Lookup: func(host string) (string, string, bool) {
		tt.mu.Lock()
		defer tt.mu.Unlock()
		l, ok := tt.saved[host]
		return l[0], l[1], ok
	}}
	return tt
}

// run runs one command (each with a fresh news client, like separate
// processes) and returns stdout, stderr and the exit code.
func (tt *tester) run(stdin string, args ...string) (string, string, int) {
	tt.t.Helper()
	var out, errOut bytes.Buffer
	news := &nntp.Client{TLSConfig: tt.news.TLSConfig, Lookup: tt.news.Lookup}
	args = append(args, "--session", tt.session)
	code := Run(args, Options{
		Gemini: tt.gem, News: news,
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errOut,
		SaveLogin: func(host, user, pass string) error {
			tt.mu.Lock()
			defer tt.mu.Unlock()
			tt.saved[host] = [2]string{user, pass}
			return nil
		},
	})
	return out.String(), errOut.String(), code
}

// ok runs a command that must succeed and returns its output.
func (tt *tester) ok(args ...string) string {
	tt.t.Helper()
	out, errOut, code := tt.run("", args...)
	if code != 0 {
		tt.t.Fatalf("mazama %s: exit %d\n%s%s", strings.Join(args, " "), code, out, errOut)
	}
	return out
}

func (tt *tester) page(args ...string) ui.Page {
	tt.t.Helper()
	var p ui.Page
	out := tt.ok(append(args, "--json")...)
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		tt.t.Fatalf("bad JSON from %v: %v\n%s", args, err, out)
	}
	return p
}

func gopherServer(t *testing.T, pages map[string]string) string {
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
			go func() {
				defer c.Close()
				line, _ := bufio.NewReader(c).ReadString('\n')
				c.Write([]byte(pages[strings.TrimRight(line, "\r\n")]))
			}()
		}
	}()
	return ln.Addr().String()
}

func mustContain(t *testing.T, s string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("missing %q in:\n%s", w, s)
		}
	}
}

func TestGopherBrowsing(t *testing.T) {
	pages := map[string]string{}
	hostport := gopherServer(t, pages)
	host, port, _ := net.SplitHostPort(hostport)
	link := "\t" + host + "\t" + port + "\r\n"
	pages[""] = "iHello there\t\t\t0\r\n" +
		"0Readme\t/readme.md" + link +
		"1Sub\t/sub" + link +
		"7Find\t/find" + link +
		"hWeb\tURL:https://example.org/" + link +
		"9Binary\t/bin" + link +
		".\r\n"
	pages["/readme.md"] = "# Big Title\n\nSee [the sub menu](/sub) and <https://example.com/x>.\n.\n"
	pages["/sub"] = "iInside sub\t\t\t0\r\n.\r\n"
	pages["/find\tcats"] = "iFound cats\t\t\t0\r\n.\r\n"

	tt := newTester(t)
	root := "gopher://" + hostport + "/1"
	out := tt.ok("open", root)
	mustContain(t, out, "url: "+root, "kind: menu", "Hello there", "[1] TXT  Readme", "[2] DIR  Sub",
		"[3] ASK  Find", "[4] URL  Web", "[5] BIN  Binary",
		`[3] gopher://`+hostport+`/7/find  (asks "search:")`, "[4] https://example.org/  (web)",
		"can't follow: item type '9' is not supported yet")

	// Documents: links are marked after their text and resolved.
	p := tt.page("follow", "1")
	if p.Kind != ui.KindText || len(p.Links) != 2 {
		t.Fatalf("readme: %+v", p)
	}
	mustContain(t, p.Text, "Big Title", "the sub menu[1]", "https://example.com/x[2]")
	if l := p.Links[0]; l.URL != "gopher://"+hostport+"/1/sub" || l.Action != ui.ActionOpen || l.Label != "the sub menu" {
		t.Errorf("link 1 = %+v", l)
	}
	if p.Links[1].Action != ui.ActionExternal {
		t.Errorf("link 2 = %+v", p.Links[1])
	}
	mustContain(t, tt.ok("follow", "1"), "Inside sub")

	// back returns through the history without fetching.
	mustContain(t, tt.ok("back"), "Big Title")
	mustContain(t, tt.ok("back"), "Hello there")
	if _, errOut, code := tt.run("", "back"); code != 1 || !strings.Contains(errOut, "no history") {
		t.Errorf("back at the start: %d %q", code, errOut)
	}

	// A search link asks first; input answers and replaces the prompt.
	out = tt.ok("follow", "3")
	mustContain(t, out, "kind: input", "input: search:", "mazama input <text>")
	mustContain(t, tt.ok("input", "cats"), "Found cats", "/7/find?cats")
	hist := tt.ok("history")
	if strings.Count(hist, "\n") != 2 || strings.Contains(hist, "/7/find\n") {
		t.Errorf("history:\n%s", hist)
	}
	if _, errOut, _ := tt.run("", "input", "dogs"); !strings.Contains(errOut, "doesn't ask for input") {
		t.Errorf("input on a normal page: %q", errOut)
	}

	// ...or -q does it in one go.
	tt.ok("back")
	mustContain(t, tt.ok("follow", "3", "-q", "cats"), "Found cats")

	// Web links aren't fetched.
	tt.ok("back")
	out = tt.ok("follow", "4")
	mustContain(t, out, "web link", "https://example.org/")
	if p := tt.page("show"); p.URL != root {
		t.Errorf("following a web link changed the page to %s", p.URL)
	}
	if _, errOut, code := tt.run("", "follow", "5"); code != 1 || !strings.Contains(errOut, "not supported") {
		t.Errorf("follow binary: %d %q", code, errOut)
	}
	if _, _, code := tt.run("", "follow", "x"); code != ExitUsage {
		t.Errorf("follow x: exit %d", code)
	}

	// get leaves the session alone.
	mustContain(t, tt.ok("get", "gopher://"+hostport+"/1/sub"), "Inside sub")
	if p := tt.page("show"); p.URL != root {
		t.Errorf("get changed the session to %s", p.URL)
	}

	// Sessions are separate.
	tt.session = "other"
	if _, errOut, code := tt.run("", "show"); code != 1 || !strings.Contains(errOut, "no page open") {
		t.Errorf("fresh session: %d %q", code, errOut)
	}
}

func TestGeminiInput(t *testing.T) {
	s := geminitest.New(t, func(u *url.URL) (string, string) {
		switch u.Path {
		case "/":
			return "20 text/gemini", "# Capsule\n=> /search Search\n=> /secret Secret\n=> docs/a.gmi Docs\n"
		case "/search":
			if u.RawQuery == "" {
				return "10 What are you looking for?", ""
			}
			return "20 text/gemini", "results for " + u.RawQuery + "\n"
		case "/secret":
			if u.RawQuery == "" {
				return "11 Password", ""
			}
			return "20 text/plain", "secret was " + u.RawQuery + "\n"
		}
		return "51 not found", ""
	})
	tt := newTester(t)
	p := tt.page("open", s.URL("/"))
	if len(p.Links) != 3 || p.Links[2].URL != s.URL("/docs/a.gmi") {
		t.Fatalf("links: %+v", p.Links)
	}
	out := tt.ok("follow", "1")
	mustContain(t, out, "kind: input", "input: What are you looking for?")
	mustContain(t, tt.ok("input", "two", "words"), "results for two%20words")

	tt.ok("back")
	out = tt.ok("follow", "2")
	mustContain(t, out, "(sensitive)", "--stdin")
	out, errOut, code := tt.run("hunter2\n", "input", "--stdin")
	if code != 0 || !strings.Contains(out, "secret was hunter2") {
		t.Errorf("sensitive input: %d %s%s", code, out, errOut)
	}

	tt.ok("back")
	if _, errOut, code := tt.run("", "follow", "3"); code != 1 || !strings.Contains(errOut, "not found") {
		t.Errorf("missing page: %d %q", code, errOut)
	}
	// get explains -q for input pages.
	mustContain(t, tt.ok("get", s.URL("/search")), "mazama get "+s.URL("/search")+" -q <text>")
	mustContain(t, tt.ok("get", s.URL("/search"), "-q", "x"), "results for x")
}

func newsTester(t *testing.T, cfg nntptest.Config) (*tester, string) {
	cfg.Groups = append(cfg.Groups,
		nntptest.Group{Name: "comp.misc", Desc: "Misc"},
		nntptest.Group{Name: "alt.test", Desc: "Testing", Articles: []string{
			"From: Ann <ann@example.com>\nSubject: first\nDate: Mon, 02 Jan 2006 15:04:05 +0000\nMessage-ID: <1@example.com>\n\nhello\n",
			"From: Bob <bob@example.com>\nSubject: Re: first\nDate: Tue, 03 Jan 2006 15:04:05 +0000\nMessage-ID: <2@example.com>\nReferences: <1@example.com>\n\nreply\n",
		}},
	)
	s := nntptest.New(t, cfg)
	tt := newTester(t)
	tt.news.TLSConfig = &tls.Config{RootCAs: s.Roots}
	return tt, "127.0.0.1:" + s.Port()
}

func TestNews(t *testing.T) {
	tt, srv := newsTester(t, nntptest.Config{TLS: true})
	out := tt.ok("open", "nntps://"+srv+"/")
	mustContain(t, out, "2 groups", "ASK  search groups", "GRP  alt.test")

	// group opens a group on the current server; unknown names search.
	out = tt.ok("group", "alt.test")
	mustContain(t, out, "alt.test · 2 articles", "Re: first")
	p := tt.page("show")
	var art int
	for _, l := range p.Links {
		if strings.Contains(l.Label, "Ann") {
			art = l.N
		}
	}
	a := tt.page("follow", fmt.Sprint(art))
	if a.URL != "nntps://"+srv+"/1@example.com" {
		t.Errorf("article URL %s", a.URL)
	}
	mustContain(t, a.Text, "first", "Ann", "hello")
	if got := tt.page("group", "misc").URL; got != "nntps://"+srv+"/*misc*" {
		t.Errorf("group misc went to %s", got)
	}
	// A bare group name at open works on news pages.
	if got := tt.page("open", "comp.misc").URL; got != "nntps://"+srv+"/comp.misc" {
		t.Errorf("open comp.misc went to %s", got)
	}

	// Searching from the search link.
	tt.ok("open", "nntps://"+srv+"/")
	mustContain(t, tt.ok("follow", "1", "-q", "test"), "alt.test", "1 group")
}

func TestNewsLogin(t *testing.T) {
	tt, srv := newsTester(t, nntptest.Config{StartTLS: true, User: "u", Pass: "secret"})
	out, errOut, code := tt.run("", "open", "news://"+srv+"/")
	if code != ExitLogin || !strings.Contains(errOut, "requires a login") || !strings.Contains(errOut, "mazama login") {
		t.Fatalf("no login demanded: %d %s%s", code, out, errOut)
	}
	if _, errOut, code := tt.run("nope\n", "login", "news://"+srv+"/", "--user", "u"); code != ExitLogin || !strings.Contains(errOut, "rejected") {
		t.Errorf("bad password: %d %q", code, errOut)
	}
	if len(tt.saved) != 0 {
		t.Error("bad login saved")
	}
	if _, _, code := tt.run("secret\n", "login", "news://"+srv+"/"); code != ExitUsage {
		t.Errorf("login without --user: exit %d", code)
	}
	out, errOut, code = tt.run("secret\n", "login", "news://"+srv+"/", "--user", "u")
	if code != 0 || !strings.Contains(out, "saved") || tt.saved["127.0.0.1"] != [2]string{"u", "secret"} {
		t.Fatalf("login: %d %s%s saved=%v", code, out, errOut, tt.saved)
	}
	mustContain(t, tt.ok("open", "news://"+srv+"/"), "2 groups")
}

func TestVoluntaryLogin(t *testing.T) {
	tt, srv := newsTester(t, nntptest.Config{StartTLS: true, User: "u", Pass: "p", Public: []string{"alt.test"}})
	p := tt.page("open", "news://"+srv+"/")
	var login ui.Link
	for _, l := range p.Links {
		if l.Action == ui.ActionLogin {
			login = l
		}
	}
	if login.N == 0 {
		t.Fatalf("no login link: %+v", p.Links)
	}
	if _, errOut, code := tt.run("", "follow", fmt.Sprint(login.N)); code != 1 || !strings.Contains(errOut, "mazama login") {
		t.Errorf("follow login link: %d %q", code, errOut)
	}
	// login takes the server from the current page; reload shows the rest.
	if out, errOut, code := tt.run("p\n", "login", "--user", "u"); code != 0 {
		t.Fatalf("login: %d %s%s", code, out, errOut)
	}
	out := tt.ok("reload")
	mustContain(t, out, "2 groups")
	if strings.Contains(out, "log in") {
		t.Error("still offers to log in")
	}
}

func TestParseInterspersed(t *testing.T) {
	for _, c := range []struct {
		args    []string
		q       string
		wantPos []string
	}{
		{[]string{"3", "-q", "foo"}, "foo", []string{"3"}},
		{[]string{"-q", "foo", "3"}, "foo", []string{"3"}},
		{[]string{"a", "-q=x", "b"}, "x", []string{"a", "b"}},
		{[]string{"--", "-3", "-q"}, "", []string{"-3", "-q"}},
	} {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		q := fs.String("q", "", "")
		pos, err := parseInterspersed(fs, c.args)
		if err != nil || *q != c.q || !slices.Equal(pos, c.wantPos) {
			t.Errorf("%q: pos %q q %q err %v", c.args, pos, *q, err)
		}
	}
}

func TestCommandErrors(t *testing.T) {
	tt := newTester(t)
	if _, _, code := tt.run("", "nope"); code != ExitUsage {
		t.Errorf("unknown command: exit %d", code)
	}
	out, _, code := tt.run("", "help")
	if code != 0 || !strings.Contains(out, "follow <n>") {
		t.Errorf("help: %d %s", code, out)
	}
	if _, _, code := tt.run("", "open"); code != ExitUsage {
		t.Errorf("open without address: exit %d", code)
	}
	tt.session = "../x"
	if _, errOut, code := tt.run("", "show"); code != 1 || !strings.Contains(errOut, "bad session name") {
		t.Errorf("bad session name: %d %q", code, errOut)
	}
}
