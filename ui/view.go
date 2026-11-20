package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Flonk/mazama/gopher"
)

func (m Model) View() string {
	if m.width == 0 {
		return ""
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), m.body(), m.footer())
}

func (m Model) header() string {
	title := "mazama"
	if m.cur != nil {
		title += "  " + m.cur.addr.String()
	}
	if m.loading {
		title += "  (loading…)"
	}
	return m.theme.Header.Width(m.width).MaxWidth(m.width).Render(title)
}

func (m Model) footer() string {
	var s string
	switch {
	case m.mode != noInput:
		return m.input.View()
	case m.cur != nil && m.cur.kind == textPage && m.cur.selLink >= 0:
		s = m.linkFooter()
	case strings.HasPrefix(m.status, "error"):
		s = m.theme.Error.Render(m.status)
	case m.status != "":
		s = m.status
	case m.cur != nil && m.cur.addr.News != nil && m.cur.kind == textPage:
		s = m.theme.Footer.Render("↑↓ scroll · ⌫ back · n group · : go · r reload · q quit")
	case m.cur != nil && m.cur.addr.News != nil:
		s = m.theme.Footer.Render("↑↓ move · ⏎ open · ⌫ back · n group · : go · r reload · q quit")
	case m.cur != nil && m.cur.markdown && len(m.cur.links) > 0:
		s = m.theme.Footer.Render("↑↓ scroll · click/tab link · ⌫ back · : go · r reload · q quit")
	case m.cur != nil && m.cur.kind == textPage:
		s = m.theme.Footer.Render("↑↓ scroll · ⌫ back · : go · r reload · q quit")
	default:
		s = m.theme.Footer.Render("↑↓ move · tab next link · ⏎ open · ⌫ back · : go · r reload · q quit")
	}
	return lipgloss.NewStyle().MaxWidth(m.width).Render(s)
}

func (m Model) body() string {
	h := m.bodyHeight()
	box := lipgloss.NewStyle().Width(m.width).Height(h).MaxHeight(h)
	if m.cur == nil {
		return box.Render("")
	}
	if m.cur.kind == textPage {
		return box.Render(m.vp.View())
	}

	p := m.cur
	end := min(p.offset+h, len(p.items))
	lines := make([]string, 0, h)
	for i := p.offset; i < end; i++ {
		lines = append(lines, m.menuLine(p.items[i], i == p.cursor))
	}
	return box.Render(strings.Join(lines, "\n"))
}

func (m Model) menuLine(it menuItem, selected bool) string {
	tag, style := m.describe(it)
	tagS := m.theme.Tag
	if selected {
		// Each segment carries the highlight itself: styled segments end in
		// an ANSI reset, so a background wrapped around the joined line would
		// drop out after the first segment.
		if m.theme.CursorBg != nil {
			tagS = tagS.Background(m.theme.CursorBg)
			style = style.Background(m.theme.CursorBg)
		} else {
			tagS = tagS.Reverse(true)
			style = style.Reverse(true)
		}
	}
	textWidth := max(m.width-tagS.GetWidth(), 0)
	display := ansi.Truncate(strings.ReplaceAll(it.Display, "\t", "    "), textWidth, "…")
	if selected {
		style = style.Width(textWidth)
	}
	return tagS.Render(tag) + style.Render(display)
}

// describe returns the short type tag and text style for a menu item.
func (m Model) describe(it menuItem) (string, lipgloss.Style) {
	t := m.theme
	style := t.Dead
	switch it.Type {
	case gopher.TypeInfo:
		style = t.Info
	case gopher.TypeError:
		style = t.Error
	case gopher.TypeMenu:
		style = t.Menu
	case gopher.TypeText:
		style = t.Text
	case gopher.TypeSearch:
		style = t.Other
	case gopher.TypeHTML:
		style = t.Text
		if it.ExternalURL() != "" {
			style = t.Other
		}
	}
	return itemTag(it), style
}
