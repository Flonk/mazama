package ui

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/Flonk/mazama/gemini"
	"github.com/Flonk/mazama/gemini/geminitest"
	"github.com/Flonk/mazama/gopher"
)

// newTestModel builds a sized model with its own throwaway TOFU store and
// runs the initial fetch.
func newTestModel(t *testing.T, start Location) Model {
	t.Helper()
	tofu, err := gemini.LoadTOFU(filepath.Join(t.TempDir(), "known_hosts"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(start, *styles.DefaultStyles[styles.DraculaStyle], &gemini.Client{TOFU: tofu})
	initCmd := m.Init()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return run(next.(Model), initCmd)
}

func mustLoc(t *testing.T, raw string) Location {
	t.Helper()
	l, err := ParseLocation(raw)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// typeText types into the focused input. The returned command is only the
// cursor blink timer, so it is dropped rather than run (it would sleep).
func typeText(m Model, s string) Model {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return next.(Model)
}

func plainView(m Model) string { return ansi.Strip(m.View()) }

func geminiSite(t *testing.T) *geminitest.Server {
	return geminitest.New(t, func(u *url.URL) (string, string) {
		switch u.Path {
		case "/", "", "/index.gmi":
			return "20 text/gemini", "# Capsule\n" +
				"Welcome to the *capsule*.\n" +
				"=> docs/intro.gmi Read the intro\n" +
				"=> /search Search\n" +
				"=> /image.png A picture\n"
		case "/docs/intro.gmi":
			return "20 text/gemini", "## Intro\nYou made it.\n=> ../ Home\n"
		case "/search":
			if u.RawQuery == "" {
				return "10 What are you looking for?", ""
			}
			q, _ := url.PathUnescape(u.RawQuery)
			return "20 text/gemini", "Results for " + q + "\n"
		case "/image.png":
			return "20 image/png", "\x89PNG"
		}
		return "51 Not found", ""
	})
}

// clickLink selects and then opens the link whose visible text is label.
func clickLink(t *testing.T, m Model, label string) Model {
	t.Helper()
	lines := strings.Split(ansi.Strip(m.cur.rendered), "\n")
	for _, s := range m.cur.spans {
		if ansi.Cut(lines[s.line], s.start, s.end) == label {
			y := s.line - m.vp.YOffset + headerRows
			press := func(m Model) Model {
				next, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: s.start, Y: y})
				return run(next.(Model), cmd)
			}
			return press(press(m)) // first click selects, second opens
		}
	}
	t.Fatalf("no link labelled %q in:\n%s", label, plainView(m))
	return m
}

func TestGeminiPageLinksAndRelativeNavigation(t *testing.T) {
	srv := geminiSite(t)
	m := newTestModel(t, mustLoc(t, srv.URL("/")))

	v := plainView(m)
	if !strings.Contains(v, "Welcome to the *capsule*.") {
		t.Errorf("gemtext should be shown literally, not as markdown:\n%s", v)
	}
	if !strings.Contains(v, "→ Read the intro") || strings.Contains(v, "docs/intro.gmi") {
		t.Errorf("link lines should show only their label:\n%s", v)
	}
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "Read the intro") && strings.Contains(l, "Search") {
			t.Errorf("gemtext lines must not be merged: %q", l)
		}
	}
	if len(m.cur.links) != 3 || !m.mouseOn {
		t.Fatalf("links=%v mouseOn=%v", m.cur.links, m.mouseOn)
	}

	m = clickLink(t, m, "Read the intro")
	if !strings.Contains(plainView(m), "You made it.") {
		t.Fatalf("relative link not followed (status %q):\n%s", m.status, plainView(m))
	}
	if got := m.cur.addr.String(); got != srv.URL("/docs/intro.gmi") {
		t.Errorf("addr = %s", got)
	}

	// "../" from /docs/intro.gmi resolves back to the root.
	m = clickLink(t, m, "Home")
	if !strings.Contains(plainView(m), "Welcome to the") {
		t.Errorf("../ link not resolved to root:\n%s", plainView(m))
	}
}

func TestGeminiInputPrompt(t *testing.T) {
	srv := geminiSite(t)
	m := newTestModel(t, mustLoc(t, srv.URL("/")))
	m = clickLink(t, m, "Search")
	if m.mode != searchInput || !strings.Contains(plainView(m), "What are you looking for?") {
		t.Fatalf("expected input prompt, mode=%v:\n%s", m.mode, plainView(m))
	}
	m = typeText(m, "tiny internet")
	m = key(m, enter)
	if !strings.Contains(plainView(m), "Results for tiny internet") {
		t.Errorf("query not sent:\n%s", plainView(m))
	}
}

func TestGeminiUnsupportedContentKeepsPage(t *testing.T) {
	srv := geminiSite(t)
	m := newTestModel(t, mustLoc(t, srv.URL("/")))
	m = clickLink(t, m, "A picture")
	if !strings.Contains(m.status, "can't display image/png") || !strings.Contains(plainView(m), "Welcome to the") {
		t.Errorf("status=%q:\n%s", m.status, plainView(m))
	}
}

func TestGopherMenuLinksToGemini(t *testing.T) {
	srv := geminiSite(t)
	pages := map[string]string{}
	host, port := fakeServer(t, pages)
	pages[""] = "hMy capsule\tURL:" + srv.URL("/") + "\t" + host + "\t" + port + "\r\n.\r\n"

	m := newTestModel(t, Location{Gopher: gopher.Address{Host: host, Port: port, Type: gopher.TypeMenu}})
	m = key(m, enter)
	if !strings.Contains(plainView(m), "Welcome to the") {
		t.Errorf("gopher URL: item should open gemini in mazama (status %q):\n%s", m.status, plainView(m))
	}
	m = key(m, backspace)
	if m.cur.kind != menuPage {
		t.Error("back should return to the gopher menu")
	}
}
