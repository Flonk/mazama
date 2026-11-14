package ui

import (
	"errors"
	"net/url"
	"path"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/Flonk/mazama/gopher"
)

// applyContent puts the current text page into the viewport, with the
// selected link (if any) highlighted.
func (m *Model) applyContent() {
	p := m.cur
	content := p.rendered
	if p.selLink >= 0 {
		hl := m.theme.Text.Underline(true)
		if m.theme.CursorBg != nil {
			hl = hl.Background(m.theme.CursorBg)
		} else {
			hl = hl.Reverse(true)
		}
		lines := strings.Split(content, "\n")
		for _, s := range p.spans {
			if s.id != p.selLink || s.line >= len(lines) {
				continue
			}
			line := lines[s.line]
			lines[s.line] = ansi.Cut(line, 0, s.start) +
				hl.Render(ansi.Strip(ansi.Cut(line, s.start, s.end))) +
				ansi.Cut(line, s.end, ansi.StringWidth(line))
		}
		content = strings.Join(lines, "\n")
	}
	m.vp.SetContent(content)
}

// selectLink highlights link id (-1 clears) and scrolls it into view.
func (m *Model) selectLink(id int) {
	p := m.cur
	if p == nil || p.kind != textPage {
		return
	}
	p.selLink = id
	m.applyContent()
	if id < 0 {
		return
	}
	for _, s := range p.spans {
		if s.id == id {
			if s.line < m.vp.YOffset || s.line >= m.vp.YOffset+m.vp.Height {
				m.vp.SetYOffset(max(s.line-m.vp.Height/3, 0))
			}
			return
		}
	}
}

// cycleLink moves the selection to the next/previous link. With nothing
// selected it starts from the first link on screen (or last, going up).
func (m *Model) cycleLink(dir int) {
	p := m.cur
	if len(p.links) == 0 {
		return
	}
	if p.selLink < 0 {
		top, bottom := m.vp.YOffset, m.vp.YOffset+m.vp.Height
		pick := -1
		for _, s := range p.spans {
			if s.line < top || s.line >= bottom {
				continue
			}
			if pick < 0 || (dir > 0 && s.id < pick) || (dir < 0 && s.id > pick) {
				pick = s.id
			}
		}
		if pick < 0 {
			pick = 0
			if dir < 0 {
				pick = len(p.links) - 1
			}
		}
		m.selectLink(pick)
		return
	}
	m.selectLink(max(min(p.selLink+dir, len(p.links)-1), 0))
}

// linkAt returns the link under a content position.
func linkAt(spans []linkSpan, line, col int) (int, bool) {
	for _, s := range spans {
		if s.line == line && col >= s.start && col < s.end {
			return s.id, true
		}
	}
	return 0, false
}

func (m Model) updateTextKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.cur
	switch msg.String() {
	case "tab":
		m.cycleLink(1)
		return m, nil
	case "shift+tab":
		m.cycleLink(-1)
		return m, nil
	case "ctrl+down":
		m.vp.ScrollDown(lineJump)
		return m, nil
	case "ctrl+up":
		m.vp.ScrollUp(lineJump)
		return m, nil
	case "enter":
		if p.selLink >= 0 {
			return m.openLink(p.links[p.selLink])
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

// resolveLink turns a link target into an absolute URL, resolving relative
// links against the current page.
func (m Model) resolveLink(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "" || m.cur == nil {
		return raw, nil
	}
	if u.Path == "" && u.Host == "" {
		return "", errors.New("in-page anchors aren't supported")
	}
	if base := m.cur.addr.Gemini; base != nil {
		return base.ResolveReference(u).String(), nil
	}
	// Gopher has no URL hierarchy; treat the selector as a path.
	sel := u.Path
	if !strings.HasPrefix(sel, "/") {
		sel = path.Join(path.Dir(m.cur.addr.Gopher.Selector), sel)
	}
	typ := gopher.TypeMenu
	if path.Ext(sel) != "" {
		typ = gopher.TypeText
	}
	a := m.cur.addr.Gopher
	a.Type, a.Selector, a.Query = typ, sel, ""
	return a.String(), nil
}

// openLink follows a link: gopher and gemini URLs (and relative ones) load
// in mazama, everything else goes to the OS handler.
func (m Model) openLink(raw string) (tea.Model, tea.Cmd) {
	m.selectLink(-1)
	abs, err := m.resolveLink(raw)
	if err != nil {
		m.status = "error: " + err.Error()
		return m, nil
	}
	u, _ := url.Parse(abs)
	switch strings.ToLower(u.Scheme) {
	case "gopher", "gemini":
		loc, err := ParseLocation(abs)
		if err != nil {
			m.status = "error: " + err.Error()
			return m, nil
		}
		return m, m.navigate(loc, false)
	default:
		return m, openExternal(abs)
	}
}

// linkFooter renders the selected link's URL with an open button.
func (m Model) linkFooter() string {
	target := m.cur.links[m.cur.selLink]
	if abs, err := m.resolveLink(target); err == nil {
		target = abs
	}
	button := m.theme.Header.Render("open ↗")
	hint := m.theme.Footer.Render("  click / ⏎ · esc")
	avail := max(m.width-ansi.StringWidth(button)-ansi.StringWidth(hint)-1, 8)
	return button + " " + m.theme.Other.Render(ansi.Truncate(target, avail, "…")) + hint
}
