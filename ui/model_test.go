package ui

import (
	"bufio"
	"net"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Flonk/mazama/gopher"
)

// fakeServer serves pages (selector -> response) on localhost. The map is
// read per request, so it may be filled in after the server starts.
func fakeServer(t *testing.T, pages map[string]string) (host, port string) {
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
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	return h, p
}

// run executes cmd synchronously and feeds its message back into the model.
func run(m Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return m
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = run(m, c)
		}
		return m
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

func key(m Model, k tea.KeyMsg) Model {
	next, cmd := m.Update(k)
	return run(next.(Model), cmd)
}

var (
	enter     = tea.KeyMsg{Type: tea.KeyEnter}
	backspace = tea.KeyMsg{Type: tea.KeyBackspace}
	tab       = tea.KeyMsg{Type: tea.KeyTab}
)

func TestNavigateAndBack(t *testing.T) {
	pages := map[string]string{}
	host, port := fakeServer(t, pages)
	link := "\t" + host + "\t" + port + "\r\n"
	pages[""] = "iHello there\t\t\t0\r\n" +
		"0Readme\t/readme.md" + link +
		"1Sub\t/sub" + link +
		".\r\n"
	pages["/readme.md"] = "# Big Title\n\nSome *markdown*.\n.\n"
	pages["/sub"] = "iInside sub\t\t\t0\r\n.\r\n"

	m := newTestModel(t, Location{Gopher: gopher.Address{Host: host, Port: port, Type: gopher.TypeMenu}})
	initCmd := m.Init()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = run(next.(Model), initCmd)

	if m.cur == nil || m.cur.kind != menuPage || len(m.cur.items) != 3 {
		t.Fatalf("root menu not loaded: status=%q", m.status)
	}
	if m.cur.cursor != 1 {
		t.Errorf("cursor should start on first link, got %d", m.cur.cursor)
	}
	if v := m.View(); !strings.Contains(v, "Hello there") || !strings.Contains(v, "TXT  Readme") {
		t.Errorf("menu view wrong:\n%s", v)
	}

	// Open the markdown document: glamour strips the *emphasis* markers.
	m = key(m, enter)
	if m.cur.kind != textPage || !m.cur.markdown {
		t.Fatalf("expected markdown text page, status=%q", m.status)
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, "Big Title") || strings.Contains(v, "*markdown*") {
		t.Errorf("markdown not rendered:\n%s", v)
	}

	// Back restores the menu with the cursor where it was.
	m = key(m, backspace)
	if m.cur.kind != menuPage || m.cur.cursor != 1 {
		t.Fatalf("back did not restore menu: %+v", m.cur)
	}

	// Tab to the submenu and open it.
	m = key(m, tab)
	m = key(m, enter)
	if !strings.Contains(m.View(), "Inside sub") || len(m.history) != 1 {
		t.Errorf("submenu not opened, history=%d:\n%s", len(m.history), m.View())
	}
}

func TestFetchErrorShowsInFooter(t *testing.T) {
	// Port 1 on localhost should refuse the connection.
	m := newTestModel(t, Location{Gopher: gopher.Address{Host: "127.0.0.1", Port: "1", Type: gopher.TypeMenu}})
	initCmd := m.Init()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = run(next.(Model), initCmd)
	if !strings.HasPrefix(m.status, "error:") || m.loading {
		t.Errorf("expected error status, got %q (loading=%v)", m.status, m.loading)
	}
}

func TestCtrlJumpsTenLines(t *testing.T) {
	pages := map[string]string{}
	host, port := fakeServer(t, pages)
	var b strings.Builder
	for i := range 25 {
		if i%2 == 0 {
			b.WriteString("iinfo\t\t\t0\r\n") // info lines count as lines too
		} else {
			b.WriteString("1dir\t/d\t" + host + "\t" + port + "\r\n")
		}
	}
	pages[""] = b.String() + ".\r\n"

	m := newTestModel(t, Location{Gopher: gopher.Address{Host: host, Port: port, Type: gopher.TypeMenu}})
	initCmd := m.Init()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	m = run(next.(Model), initCmd)

	ctrlDown, ctrlUp := tea.KeyMsg{Type: tea.KeyCtrlDown}, tea.KeyMsg{Type: tea.KeyCtrlUp}
	start := m.cur.cursor // first link, index 1
	m = key(m, ctrlDown)
	if m.cur.cursor != start+10 {
		t.Errorf("ctrl+down: cursor %d, want %d", m.cur.cursor, start+10)
	}
	m = key(m, ctrlDown)
	m = key(m, ctrlDown)
	if m.cur.cursor != 24 {
		t.Errorf("ctrl+down should clamp at the last line: cursor %d, want 24", m.cur.cursor)
	}
	m = key(m, ctrlUp)
	if m.cur.cursor != 14 {
		t.Errorf("ctrl+up: cursor %d, want 14", m.cur.cursor)
	}
}

func TestThemesBuild(t *testing.T) {
	// Every built-in style must give menus a focus highlight, either a
	// colour or reverse video, and dracula should use its own palette.
	for name, cfg := range styles.DefaultStyles {
		th := NewTheme(*cfg)
		_ = th.Header.Render("x")
		t.Logf("%-12s cursor=%v", name, th.CursorBg)
	}
	d := NewTheme(*styles.DefaultStyles[styles.DraculaStyle])
	if c, _ := d.Menu.GetForeground().(lipgloss.Color); c != "#bd93f9" {
		t.Errorf("dracula DIR colour = %v, want #bd93f9", d.Menu.GetForeground())
	}
}

func TestMouse(t *testing.T) {
	pages := map[string]string{}
	host, port := fakeServer(t, pages)
	var b strings.Builder
	for i := range 50 {
		b.WriteString("1Dir " + string(rune('A'+i%26)) + "\t/d\t" + host + "\t" + port + "\r\n")
	}
	pages[""] = b.String() + ".\r\n"
	pages["/d"] = "iInside\t\t\t0\r\n.\r\n"

	m := newTestModel(t, Location{Gopher: gopher.Address{Host: host, Port: port, Type: gopher.TypeMenu}})
	initCmd := m.Init()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 12}) // 10 body rows
	m = run(next.(Model), initCmd)

	mouse := func(m Model, b tea.MouseButton, y int) Model {
		next, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: b, Y: y})
		return run(next.(Model), cmd)
	}

	m = mouse(m, tea.MouseButtonWheelDown, 5)
	m = mouse(m, tea.MouseButtonWheelDown, 5)
	if m.cur.offset != 6 || m.cur.cursor != 6 {
		t.Errorf("after 2 wheel notches: offset=%d cursor=%d, want 6/6", m.cur.offset, m.cur.cursor)
	}
	m = mouse(m, tea.MouseButtonWheelUp, 5)
	if m.cur.offset != 3 {
		t.Errorf("wheel up: offset=%d, want 3", m.cur.offset)
	}

	// Row 3 on screen is body row 2 → item offset+2 = 5. Clicking opens it.
	m = mouse(m, tea.MouseButtonLeft, 3)
	if len(m.history) != 1 || !strings.Contains(m.View(), "Inside") {
		t.Fatalf("click did not open the link:\n%s", m.View())
	}
	if m.history[0].cursor != 5 {
		t.Errorf("clicked item index = %d, want 5", m.history[0].cursor)
	}

	m = mouse(m, tea.MouseButtonBackward, 0)
	if len(m.history) != 0 || m.cur.offset != 3 {
		t.Errorf("back button: history=%d offset=%d", len(m.history), m.cur.offset)
	}

	// Clicking the header does nothing.
	m = mouse(m, tea.MouseButtonLeft, 0)
	if len(m.history) != 0 {
		t.Error("header click navigated")
	}
}

func TestMouseOnlyCapturedOnMenus(t *testing.T) {
	pages := map[string]string{}
	host, port := fakeServer(t, pages)
	pages[""] = "0Doc\t/doc.txt\t" + host + "\t" + port + "\r\n.\r\n"
	pages["/doc.txt"] = "select me\n.\n"

	m := newTestModel(t, Location{Gopher: gopher.Address{Host: host, Port: port, Type: gopher.TypeMenu}})
	initCmd := m.Init()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = run(next.(Model), initCmd)
	if !m.mouseOn {
		t.Fatal("mouse should be captured on a menu")
	}
	m = key(m, enter)
	if m.cur.kind != textPage || m.mouseOn {
		t.Fatalf("mouse should be released on a text page (kind=%v mouseOn=%v)", m.cur.kind, m.mouseOn)
	}
	m = key(m, backspace)
	if !m.mouseOn {
		t.Error("mouse should be captured again after going back to the menu")
	}
}

func TestOneDarkBundled(t *testing.T) {
	cfg, err := LoadStyle(DefaultStyle)
	if err != nil {
		t.Fatal(err)
	}
	th := NewTheme(cfg)
	want := map[string]struct {
		got  lipgloss.TerminalColor
		want lipgloss.Color
	}{
		"DIR":  {th.Menu.GetForeground(), "#e06c75"},
		"TXT":  {th.Text.GetForeground(), "#61afef"},
		"text": {th.Info.GetForeground(), "#ffffff"},
		"URL":  {th.Other.GetForeground(), "#56b6c2"},
		"tag":  {th.Tag.GetForeground(), "#5c6370"},
	}
	for role, c := range want {
		if c.got != c.want {
			t.Errorf("one-dark %s = %v, want %v", role, c.got, c.want)
		}
	}
	t.Logf("one-dark cursor bg = %v", th.CursorBg)
	if !slices.Contains(StyleNames(), "one-dark") || !slices.Contains(StyleNames(), "dracula") {
		t.Errorf("StyleNames missing entries: %v", StyleNames())
	}
}

func TestMarkdownLinkSelectThenOpen(t *testing.T) {
	pages := map[string]string{}
	host, port := fakeServer(t, pages)
	target := "gopher://" + host + ":" + port + "/1/next"
	pages[""] = "0Post\t/post.md\t" + host + "\t" + port + "\r\n.\r\n"
	pages["/post.md"] = "[Quite a while ago](" + target + ") I wrote [another](https://example.com/x).\n"
	pages["/next"] = "iArrived\t\t\t0\r\n.\r\n"

	m := newTestModel(t, Location{Gopher: gopher.Address{Host: host, Port: port, Type: gopher.TypeMenu}})
	initCmd := m.Init()
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = run(next.(Model), initCmd)
	m = key(m, enter)
	if !m.cur.markdown || len(m.cur.links) != 2 || !m.mouseOn {
		t.Fatalf("markdown page: links=%v mouseOn=%v", m.cur.links, m.mouseOn)
	}
	if v := ansi.Strip(m.View()); strings.Contains(v, target) || !strings.Contains(v, "Quite a while ago I wrote another.") {
		t.Fatalf("URLs should be hidden and text should flow:\n%s", v)
	}

	// Find where link 0 is on screen and click it.
	s := m.cur.spans[0]
	click := func(m Model, x, y int) Model {
		next, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
		return run(next.(Model), cmd)
	}
	y := s.line - m.vp.YOffset + headerRows
	m = click(m, s.start+2, y)
	if m.cur.selLink != 0 {
		t.Fatalf("first click should select link 0, selLink=%d", m.cur.selLink)
	}
	if v := ansi.Strip(m.View()); !strings.Contains(v, target) || !strings.Contains(v, "open ↗") {
		t.Errorf("footer should show the URL and an open button:\n%s", v)
	}

	// Clicking empty space clears; tab selects the first visible link again.
	m = click(m, 79, y)
	if m.cur.selLink != -1 {
		t.Errorf("click on plain text should clear selection, selLink=%d", m.cur.selLink)
	}
	m = key(m, tab)
	m = key(m, tab)
	if m.cur.selLink != 1 {
		t.Errorf("two tabs should select link 1, got %d", m.cur.selLink)
	}
	m = key(m, tea.KeyMsg{Type: tea.KeyShiftTab})

	// Clicking the footer opens the selected (gopher) link inside mazama.
	m = click(m, 3, 23)
	if !strings.Contains(m.View(), "Arrived") {
		t.Errorf("footer click should open the link:\n%s", ansi.Strip(m.View()))
	}
}
