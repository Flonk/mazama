package ui

import (
	"crypto/tls"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/styles"

	"github.com/Flonk/mazama/nntp"
	"github.com/Flonk/mazama/nntp/nntptest"
)

func newsServer(t *testing.T, cfg nntptest.Config) *nntptest.Server {
	for i := range 150 {
		cfg.Groups = append(cfg.Groups, nntptest.Group{Name: fmt.Sprintf("comp.lang.g%03d", i), Desc: fmt.Sprintf("Language %d", i)})
	}
	cfg.Groups = append(cfg.Groups,
		nntptest.Group{Name: "comp.misc", Desc: "Misc"},
		nntptest.Group{Name: "alt.test", Desc: "Testing", Articles: []string{
			"From: Ann <ann@example.com>\nSubject: first\nDate: Mon, 02 Jan 2006 15:04:05 +0000\nMessage-ID: <1@example.com>\n\nhello\x1b[31m world\n",
			"From: =?UTF-8?Q?J=C3=BCrgen?= <j@example.com>\nSubject: Re: first\nDate: Tue, 03 Jan 2006 15:04:05 +0000\nMessage-ID: <2@example.com>\n\nreply\n",
		}},
	)
	return nntptest.New(t, cfg)
}

// newsModel opens raw against a test server, trusting its certificate and
// knowing no logins.
func newsModel(t *testing.T, s *nntptest.Server, path string) Model {
	t.Helper()
	m := New(Location{}, *styles.DefaultStyles[styles.DraculaStyle], nil)
	m.news = &nntp.Client{TLSConfig: &tls.Config{RootCAs: s.Roots}}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	loc, err := ParseLocation("news://127.0.0.1:" + s.Port() + path)
	if err != nil {
		t.Fatal(err)
	}
	return run(m, m.navigate(loc, false))
}

func TestNewsLoginAndBrowse(t *testing.T) {
	s := newsServer(t, nntptest.Config{StartTLS: true, User: "u", Pass: "secret"})
	m := newsModel(t, s, "/")

	// The server wants a login: mazama asks, password hidden.
	if m.mode != callbackInput || !strings.Contains(plainView(m), "login for 127.0.0.1:") {
		t.Fatalf("no login prompt; status=%q\n%s", m.status, plainView(m))
	}
	m = key(typeText(m, "u"), enter)
	if !strings.Contains(plainView(m), "password:") {
		t.Fatalf("no password prompt:\n%s", plainView(m))
	}
	m = typeText(m, "secret")
	if strings.Contains(plainView(m), "secret") {
		t.Error("password echoed")
	}
	m = key(m, enter)

	// It worked, so mazama offers to keep it; saying yes saves it.
	var saved []string
	saveLogin = func(host, user, pass string) error {
		saved = append(saved, host+" "+user+" "+pass)
		return nil
	}
	t.Cleanup(func() { saveLogin = nntp.SaveLogin })
	if m.mode != confirmInput || !strings.Contains(plainView(m), "remember login for 127.0.0.1 in the keyring? (y/n)") {
		t.Fatalf("no offer to save the login:\n%s", plainView(m))
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if len(saved) != 1 || saved[0] != "127.0.0.1 u secret" || !strings.Contains(m.status, "saved in the keyring") {
		t.Errorf("saved = %q, status %q", saved, m.status)
	}

	// 152 groups fold into hierarchies at the root.
	v := plainView(m)
	if m.cur == nil || m.cur.kind != menuPage {
		t.Fatalf("root not loaded: status=%q\n%s", m.status, v)
	}
	for _, want := range []string{"152 groups", "ASK  search groups", "GRP  alt.test", "DIR  comp.*  (151 groups)"} {
		if !strings.Contains(v, want) {
			t.Errorf("root missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "comp.lang.g000") {
		t.Error("root lists every group")
	}

	// comp.* is still too big: comp.lang.* folds, comp.misc is a group.
	m.cur.cursor = indexOf(t, m, "comp.*")
	m = key(m, enter)
	v = plainView(m)
	if !strings.Contains(v, "DIR  comp.lang.*  (150 groups)") || !strings.Contains(v, "GRP  comp.misc") {
		t.Errorf("comp.* page wrong:\n%s", v)
	}
	// comp.lang.* has more than collapseAbove groups but nothing to fold
	// into, so it lists them.
	m.cur.cursor = indexOf(t, m, "comp.lang.*")
	m = key(m, enter)
	if v = plainView(m); !strings.Contains(v, "GRP  comp.lang.g000  Language 0") {
		t.Errorf("comp.lang.* page wrong:\n%s", v)
	}

	// Search: plain words match anywhere in the name.
	m.cur.cursor = indexOf(t, m, "search groups")
	m = key(m, enter)
	m = key(typeText(m, "g04"), enter)
	if m.cur.addr.News.Group != "*g04*" {
		t.Fatalf("search went to %q (status %q)", m.cur.addr.String(), m.status)
	}
	if v = plainView(m); !strings.Contains(v, "10 groups") || !strings.Contains(v, "comp.lang.g049") || strings.Contains(v, "g050") {
		t.Errorf("search results wrong:\n%s", v)
	}

	// The login is remembered for the session.
	if s.Logins < 1 {
		t.Error("no login reached the server")
	}
	for _, l := range s.Log() {
		if strings.Contains(l, "secret") {
			t.Error("password in server log")
		}
	}
}

func TestNewsGroupAndArticle(t *testing.T) {
	s := newsServer(t, nntptest.Config{TLS: true})
	m := New(Location{}, *styles.DefaultStyles[styles.DraculaStyle], nil)
	m.news = &nntp.Client{TLSConfig: &tls.Config{RootCAs: s.Roots}}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	m = run(m, m.navigate(mustLoc(t, "nntps://127.0.0.1:"+s.Port()+"/alt.test"), false))

	v := plainView(m)
	if m.cur == nil || m.cur.kind != menuPage {
		t.Fatalf("group not loaded: status=%q", m.status)
	}
	// Newest first, with decoded names.
	first, second := strings.Index(v, "Re: first"), strings.Index(v, "Ann")
	if !strings.Contains(v, "alt.test · 2 articles") || first < 0 || second < 0 || first > second || !strings.Contains(v, "Jürgen") {
		t.Errorf("group page wrong:\n%s", v)
	}

	m.cur.cursor = indexOf(t, m, "Ann")
	m = key(m, enter)
	if m.cur.kind != textPage {
		t.Fatalf("article not opened: status=%q", m.status)
	}
	if m.cur.addr.String() != "nntps://127.0.0.1:"+s.Port()+"/1@example.com" {
		t.Errorf("article address = %s", m.cur.addr.String())
	}
	if v := plainView(m); !strings.Contains(v, "first") || !strings.Contains(v, "Ann · ") || !strings.Contains(v, "hello[31m world") {
		t.Errorf("article text wrong: %q", m.cur.raw)
	}
	if strings.Contains(m.cur.raw, "\x1b") {
		t.Error("escape sequence reached the screen")
	}
	m = key(m, backspace)
	if m.cur.kind != menuPage {
		t.Error("back didn't return to the group")
	}
}

func TestNewsRefusesCleartextLogin(t *testing.T) {
	s := newsServer(t, nntptest.Config{User: "u", Pass: "p"})
	m := newsModel(t, s, "/")
	if m.mode != noInput || !strings.Contains(m.status, "unencrypted") {
		t.Errorf("want refusal, got mode %d status %q", m.mode, m.status)
	}
}

func TestNewsGuess(t *testing.T) {
	for in, want := range map[string]string{
		"nntp.example.org":                    "news://nntp.example.org/",
		"news.example.org:563/comp.lang.go":   "nntps://news.example.org/comp.lang.go",
		"example.org:119":                     "news://example.org/",
		"news://news.example.org/x@y.example": "news://news.example.org/x@y.example",
	} {
		loc, err := guessLocation(t.Context(), in)
		if err != nil || loc.String() != want {
			t.Errorf("guess(%q) = %v, %v; want %s", in, loc, err, want)
		}
	}
}

func indexOf(t *testing.T, m Model, display string) int {
	t.Helper()
	for i, it := range m.cur.items {
		if strings.Contains(it.Display, display) && it.Selectable() {
			return i
		}
	}
	t.Fatalf("no item %q", display)
	return 0
}

// Some servers (Eternal September) list a few groups to anyone and never ask
// for a login; the "log in" entry gets the rest.
func TestNewsVoluntaryLogin(t *testing.T) {
	s := newsServer(t, nntptest.Config{StartTLS: true, User: "u", Pass: "p", Public: []string{"alt.test"}})
	m := newsModel(t, s, "/")
	v := plainView(m)
	if !strings.Contains(v, "1 group") || !strings.Contains(v, "KEY  log in") {
		t.Fatalf("anonymous root wrong (status %q):\n%s", m.status, v)
	}
	m.cur.cursor = indexOf(t, m, "log in")
	m = key(m, enter)
	m = key(typeText(m, "u"), enter)
	m = key(typeText(m, "p"), enter)
	// Declining the offer saves nothing and leaves the page usable.
	saveLogin = func(string, string, string) error { t.Error("login saved after no"); return nil }
	t.Cleanup(func() { saveLogin = nntp.SaveLogin })
	if m.mode != confirmInput {
		t.Fatalf("no offer to save the login (status %q)", m.status)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if m.mode != noInput {
		t.Error("prompt still open after n")
	}
	v = plainView(m)
	if !strings.Contains(v, "152 groups") || strings.Contains(v, "log in") {
		t.Errorf("root after login wrong (status %q):\n%s", m.status, v)
	}
	if len(m.history) != 0 {
		t.Errorf("login pushed history: %d pages", len(m.history))
	}

	// A wrong password asks again.
	m.news = &nntp.Client{TLSConfig: m.news.TLSConfig}
	m = run(m, m.navigate(m.cur.addr, true))
	m.cur.cursor = indexOf(t, m, "log in")
	m = key(m, enter)
	m = key(typeText(m, "u"), enter)
	m = key(typeText(m, "nope"), enter)
	if !strings.Contains(plainView(m), "login rejected") {
		t.Errorf("no retry prompt after a bad password:\n%s", plainView(m))
	}
}

func TestNewsGotoGroup(t *testing.T) {
	s := newsServer(t, nntptest.Config{TLS: true})
	m := New(Location{}, *styles.DefaultStyles[styles.DraculaStyle], nil)
	m.news = &nntp.Client{TLSConfig: &tls.Config{RootCAs: s.Roots}}
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	m = run(m, m.navigate(mustLoc(t, "nntps://127.0.0.1:"+s.Port()+"/"), false))
	if !strings.Contains(plainView(m), "n group") {
		t.Errorf("footer doesn't mention n:\n%s", plainView(m))
	}

	// n + an exact name opens the group.
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	if !strings.Contains(plainView(m), "group:") {
		t.Fatalf("no group prompt:\n%s", plainView(m))
	}
	m = key(typeText(m, "alt.test"), enter)
	if a := m.cur.addr.News; a == nil || a.Group != "alt.test" || m.cur.kind != menuPage {
		t.Fatalf("n alt.test went to %s (status %q)", m.cur.addr.String(), m.status)
	}

	// n works from an article too, and an unknown name becomes a search.
	m.cur.cursor = indexOf(t, m, "Ann")
	m = key(m, enter)
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	m = key(typeText(m, "g04"), enter)
	if a := m.cur.addr.News; a == nil || a.Group != "*g04*" {
		t.Fatalf("n g04 went to %s (status %q)", m.cur.addr.String(), m.status)
	}

	// The : prompt takes a bare group name on news pages.
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(":")})
	m.input.SetValue("")
	m = key(typeText(m, "comp.misc"), enter)
	if a := m.cur.addr.News; a == nil || a.Group != "comp.misc" {
		t.Fatalf(": comp.misc went to %s (status %q)", m.cur.addr.String(), m.status)
	}
	// ...but an unknown name still goes to host guessing.
	m = key(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(":")})
	m.input.SetValue("")
	m = key(typeText(m, "no-such-host.invalid"), enter)
	if !strings.Contains(m.status, "no-such-host.invalid") || m.cur.addr.News.Group != "comp.misc" {
		t.Errorf("unknown name: status %q, page %s", m.status, m.cur.addr.String())
	}
}
