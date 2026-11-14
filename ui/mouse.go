package ui

import tea "github.com/charmbracelet/bubbletea"

// wheelLines is how far one wheel notch scrolls a menu, matching the
// viewport's default for text pages.
const wheelLines = 3

// headerRows is the number of screen rows above the body.
const headerRows = 1

// syncMouse captures the mouse on menus and markdown pages (which have
// clickable links) and releases it on plain text pages, so the terminal's
// native text selection works there. Returns nil when nothing needs to change.
func (m *Model) syncMouse() tea.Cmd {
	want := m.cur != nil && (m.cur.kind == menuPage || m.cur.markdown)
	if want == m.mouseOn {
		return nil
	}
	m.mouseOn = want
	if want {
		return tea.EnableMouseCellMotion
	}
	return tea.DisableMouse
}

func (m Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if msg.Action != tea.MouseActionPress {
		return m, nil
	}
	if msg.Button == tea.MouseButtonBackward {
		m.back()
		return m, nil
	}
	if m.cur == nil {
		return m, nil
	}

	if m.cur.kind == textPage {
		if msg.Button == tea.MouseButtonLeft {
			return m.clickText(msg)
		}
		// The viewport handles the wheel itself.
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}

	switch msg.Button {
	case tea.MouseButtonWheelDown:
		m.scrollMenu(wheelLines)
	case tea.MouseButtonWheelUp:
		m.scrollMenu(-wheelLines)
	case tea.MouseButtonLeft:
		i, ok := m.itemAt(msg.Y)
		if !ok {
			return m, nil
		}
		m.cur.cursor = i
		// A single click opens links, as in a browser; clicking text just
		// moves the cursor there.
		if m.cur.items[i].Selectable() {
			return m.activate(m.cur.items[i])
		}
	}
	return m, nil
}

// clickText handles a click on a markdown page: the first click on a link
// selects it and shows its URL in the footer; clicking it again, or clicking
// the footer, opens it. Clicking anywhere else clears the selection.
func (m Model) clickText(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	p := m.cur
	if msg.Y == m.height-1 && p.selLink >= 0 {
		return m.openLink(p.links[p.selLink])
	}
	row := msg.Y - headerRows
	if row >= 0 && row < m.vp.Height {
		if id, ok := linkAt(p.spans, m.vp.YOffset+row, msg.X); ok {
			if id == p.selLink {
				return m.openLink(p.links[id])
			}
			m.selectLink(id)
			return m, nil
		}
	}
	if p.selLink >= 0 {
		m.selectLink(-1)
	}
	return m, nil
}

// itemAt maps a screen row to a menu item index.
func (m Model) itemAt(y int) (int, bool) {
	row := y - headerRows
	if row < 0 || row >= m.bodyHeight() {
		return 0, false
	}
	i := m.cur.offset + row
	if i >= len(m.cur.items) {
		return 0, false
	}
	return i, true
}

// scrollMenu moves the visible window and drags the cursor along only as far
// as needed to keep it on screen, so the view doesn't snap back.
func (m *Model) scrollMenu(delta int) {
	p := m.cur
	h := m.bodyHeight()
	p.offset = max(min(p.offset+delta, len(p.items)-h), 0)
	p.cursor = max(min(p.cursor, p.offset+h-1), p.offset)
	p.cursor = min(p.cursor, len(p.items)-1)
}
