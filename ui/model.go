// Package ui is the bubbletea front end: menus are drawn with lipgloss,
// text documents go into a viewport, and markdown documents are rendered
// with glamour first.
package ui

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/x/ansi"

	"github.com/Flonk/mazama/gemini"
	"github.com/Flonk/mazama/gopher"
)

type pageKind int

const (
	menuPage pageKind = iota
	textPage
)

// page is one entry in the history stack. Scroll positions are stored on it
// so going back returns you to where you were.
type page struct {
	addr     Location
	kind     pageKind
	items    []gopher.Item
	cursor   int
	offset   int
	raw      string
	markdown bool
	gemtext  bool // markdown converted from gemtext: keep its line breaks
	yOffset  int

	// Markdown links (see markdown.go). rendered is the display content
	// without the selected-link highlight; selLink is -1 when none is selected.
	rendered string
	links    []string
	spans    []linkSpan
	selLink  int
}

type inputMode int

const (
	noInput inputMode = iota
	gotoInput
	searchInput
)

type fetchedMsg struct {
	seq     int
	addr    Location
	mime    string        // gemini content type
	input   *inputRequest // gemini asked for input instead of a page
	data    []byte
	err     error
	replace bool // reload: swap the current page instead of pushing
}

// Model is the root bubbletea model.
type Model struct {
	cur     *page
	history []*page

	vp    viewport.Model
	input textinput.Model
	mode  inputMode
	// searchTarget is the gopher search item or gemini URL waiting for input.
	searchTarget Location

	styleCfg gansi.StyleConfig
	theme    Theme
	width    int
	height   int

	loading bool
	seq     int
	cancel  context.CancelFunc
	status  string
	// initCmd is the first fetch. It is started in New because Init has a
	// value receiver and cannot record the fetch's seq on the model.
	initCmd tea.Cmd
	// mouseOn tracks whether the terminal is currently reporting mouse
	// events to us; see syncMouse.
	mouseOn bool

	gem *gemini.Client
}

// New returns a model that opens start on launch, styled by a glamour style
// config (see LoadStyle). Menus get a matching Theme. gem fetches gemini://
// pages and carries the TOFU certificate store.
func New(start Location, style gansi.StyleConfig, gem *gemini.Client) Model {
	ti := textinput.New()
	ti.CharLimit = 1024
	m := Model{
		vp:       viewport.New(0, 0),
		input:    ti,
		styleCfg: style,
		theme:    NewTheme(style),
		gem:      gem,
	}
	m.initCmd = m.navigate(start, false)
	return m
}

// NewFromInput is New for an address as the user typed it: without a
// scheme, the protocol is guessed (see guess.go).
func NewFromInput(raw string, style gansi.StyleConfig, gem *gemini.Client) Model {
	m := New(Location{}, style, gem)
	m.initCmd = m.navigateInput(raw)
	return m
}

func (m Model) Init() tea.Cmd {
	return m.initCmd
}

// navigate starts an async fetch. Only the most recent fetch is honoured;
// starting a new one cancels the previous.
func (m *Model) navigate(a Location, replace bool) tea.Cmd {
	return m.startFetch(func(context.Context) (Location, error) { return a, nil }, replace)
}

// navigateInput is navigate for typed input, guessing the protocol when
// there's no scheme. Probing happens inside the fetch, so it shows as
// loading and esc cancels it.
func (m *Model) navigateInput(raw string) tea.Cmd {
	return m.startFetch(func(ctx context.Context) (Location, error) {
		return guessLocation(ctx, raw)
	}, false)
}

func (m *Model) startFetch(resolve func(context.Context) (Location, error), replace bool) tea.Cmd {
	if m.cancel != nil {
		m.cancel()
	}
	m.seq++
	seq := m.seq
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	m.cancel = cancel
	m.loading = true
	m.status = ""
	gem := m.gem
	return func() tea.Msg {
		defer cancel()
		a, err := resolve(ctx)
		if err != nil {
			return fetchedMsg{seq: seq, err: err}
		}
		var msg fetchedMsg
		switch {
		case a.Web != nil:
			msg = fetchedMsg{addr: a} // nothing to fetch; openPage shows webPage
		case a.Gemini != nil:
			msg = fetchGemini(ctx, gem, a.Gemini)
		default:
			data, err := gopher.Fetch(ctx, a.Gopher)
			msg = fetchedMsg{addr: a, data: data, err: err}
		}
		msg.seq, msg.replace = seq, replace
		return msg
	}
}

func (m Model) bodyHeight() int {
	return max(m.height-2, 1)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	nm := next.(Model)
	if mc := nm.syncMouse(); mc != nil {
		return nm, tea.Batch(cmd, mc)
	}
	return nm, cmd
}

func (m Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.Width, m.vp.Height = msg.Width, m.bodyHeight()
		m.input.Width = max(msg.Width-12, 10)
		if m.cur != nil {
			if m.cur.kind == textPage {
				y := m.vp.YOffset
				m.setTextContent()
				m.vp.SetYOffset(y)
			} else {
				m.ensureCursorVisible()
			}
		}
		return m, nil

	case fetchedMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
			return m, nil
		}
		if msg.input != nil {
			return m, m.promptInput(msg.addr, msg.input.prompt, msg.input.sensitive)
		}
		if err := m.openPage(msg); err != nil {
			m.status = "error: " + err.Error()
		}
		return m, nil

	case openedMsg:
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
		} else {
			m.status = "opened " + msg.url
		}
		return m, nil

	case tea.KeyMsg:
		if m.mode != noInput {
			return m.updateInput(msg)
		}
		return m.updateKeys(msg)

	case tea.MouseMsg:
		if m.mode != noInput {
			return m, nil
		}
		return m.updateMouse(msg)
	}

	if m.cur != nil && m.cur.kind == textPage {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) openPage(msg fetchedMsg) error {
	p := &page{addr: msg.addr, selLink: -1}
	switch {
	case msg.addr.Web != nil:
		p = webPage(msg.addr.Web)
	case msg.addr.Gemini != nil:
		if err := geminiPage(p, msg.mime, msg.data); err != nil {
			return err
		}
	case msg.addr.Gopher.Type == gopher.TypeMenu, msg.addr.Gopher.Type == gopher.TypeSearch:
		p.kind = menuPage
		p.items = gopher.ParseMenu(msg.data)
		p.cursor = firstSelectable(p.items)
	default:
		p.kind = textPage
		p.raw = string(gopher.StripTerminator(msg.data))
		p.markdown = isMarkdown(msg.addr.Gopher.Selector)
	}

	if m.cur != nil && !msg.replace {
		m.saveScroll()
		m.history = append(m.history, m.cur)
	}
	m.cur = p
	if p.kind == textPage {
		m.setTextContent()
		m.vp.GotoTop()
	} else {
		m.ensureCursorVisible()
	}
	return nil
}

// promptInput opens the footer input for a gopher search or a gemini input
// request; the answer is sent to target as its query.
func (m *Model) promptInput(target Location, prompt string, sensitive bool) tea.Cmd {
	m.mode = searchInput
	m.searchTarget = target
	m.input.Prompt = ansi.Truncate(prompt, max(m.width/2, 10), "…") + " "
	m.input.SetValue("")
	m.input.EchoMode = textinput.EchoNormal
	if sensitive {
		m.input.EchoMode = textinput.EchoPassword
	}
	return m.input.Focus()
}

func (m *Model) saveScroll() {
	if m.cur != nil && m.cur.kind == textPage {
		m.cur.yOffset = m.vp.YOffset
	}
}

func (m *Model) back() {
	if len(m.history) == 0 {
		m.status = "no history"
		return
	}
	if m.cancel != nil {
		m.cancel()
		m.loading = false
	}
	m.cur = m.history[len(m.history)-1]
	m.history = m.history[:len(m.history)-1]
	if m.cur.kind == textPage {
		m.setTextContent()
		m.vp.SetYOffset(m.cur.yOffset)
	}
	m.status = ""
}

// setTextContent (re)renders the current text page at the current width.
func (m *Model) setTextContent() {
	p := m.cur
	p.rendered = strings.ReplaceAll(p.raw, "\t", "    ")
	p.links, p.spans = nil, nil
	if p.markdown && m.width > 0 {
		content, links, spans, err := renderMarkdown(p.raw, m.styleCfg, min(m.width-4, 100), p.gemtext)
		if err == nil {
			p.rendered, p.links, p.spans = content, links, spans
		}
	}
	if p.selLink >= len(p.links) {
		p.selLink = -1
	}
	m.applyContent()
}

func (m Model) updateKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "backspace", "h", "left":
		m.back()
		return m, nil
	case ":", "o":
		m.mode = gotoInput
		m.input.Prompt = "go: "
		m.input.EchoMode = textinput.EchoNormal
		m.input.SetValue("")
		if m.cur != nil {
			m.input.SetValue(m.cur.addr.String())
			m.input.CursorEnd()
		}
		return m, m.input.Focus()
	case "r":
		if m.cur != nil {
			return m, m.navigate(m.cur.addr, true)
		}
		return m, nil
	case "esc":
		if m.loading && m.cancel != nil {
			m.cancel()
			m.loading = false
			m.status = "cancelled"
		} else if m.cur != nil && m.cur.selLink >= 0 {
			m.selectLink(-1)
		}
		return m, nil
	}

	if m.cur == nil {
		return m, nil
	}
	if m.cur.kind == textPage {
		return m.updateTextKeys(msg)
	}
	return m.updateMenu(msg)
}

func (m Model) updateMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.cur
	n := len(p.items)
	if n == 0 {
		return m, nil
	}
	switch msg.String() {
	case "j", "down":
		p.cursor = min(p.cursor+1, n-1)
	case "k", "up":
		p.cursor = max(p.cursor-1, 0)
	case "ctrl+down":
		p.cursor = min(p.cursor+lineJump, n-1)
	case "ctrl+up":
		p.cursor = max(p.cursor-lineJump, 0)
	case "tab":
		if i := nextSelectable(p.items, p.cursor, 1); i >= 0 {
			p.cursor = i
		}
	case "shift+tab":
		if i := nextSelectable(p.items, p.cursor, -1); i >= 0 {
			p.cursor = i
		}
	case "pgdown", "f", " ", "ctrl+d":
		p.cursor = min(p.cursor+m.bodyHeight(), n-1)
	case "pgup", "b", "ctrl+u":
		p.cursor = max(p.cursor-m.bodyHeight(), 0)
	case "g", "home":
		p.cursor = 0
	case "G", "end":
		p.cursor = n - 1
	case "enter", "l", "right":
		return m.activate(p.items[p.cursor])
	}
	m.ensureCursorVisible()
	return m, nil
}

func (m Model) activate(it gopher.Item) (tea.Model, tea.Cmd) {
	switch it.Type {
	case gopher.TypeMenu, gopher.TypeText:
		return m, m.navigate(Location{Gopher: it.Address()}, false)
	case gopher.TypeSearch:
		return m, m.promptInput(Location{Gopher: it.Address()}, "search:", false)
	case gopher.TypeHTML:
		if u := it.ExternalURL(); u != "" {
			// gemini:// and gopher:// targets open in mazama.
			return m.openLink(u)
		}
		return m, m.navigate(Location{Gopher: it.Address()}, false)
	case gopher.TypeInfo, gopher.TypeError:
		return m, nil
	default:
		m.status = fmt.Sprintf("item type %q is not supported yet", it.Type)
		return m, nil
	}
}

func (m Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = noInput
		m.input.Blur()
		return m, nil
	case "enter":
		val := strings.TrimSpace(m.input.Value())
		mode := m.mode
		m.mode = noInput
		m.input.Blur()
		if val == "" {
			return m, nil
		}
		if mode == searchInput {
			return m, m.navigate(m.searchTarget.withQuery(val), false)
		}
		return m, m.navigateInput(val)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) ensureCursorVisible() {
	p := m.cur
	h := m.bodyHeight()
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+h {
		p.offset = p.cursor - h + 1
	}
	p.offset = max(min(p.offset, len(p.items)-h), 0)
}

func firstSelectable(items []gopher.Item) int {
	for i, it := range items {
		if it.Selectable() {
			return i
		}
	}
	return 0
}

func nextSelectable(items []gopher.Item, from, dir int) int {
	for i := from + dir; i >= 0 && i < len(items); i += dir {
		if items[i].Selectable() {
			return i
		}
	}
	return -1
}

// lineJump is how many lines ctrl+up/down move.
const lineJump = 10

func isMarkdown(selector string) bool {
	switch strings.ToLower(path.Ext(selector)) {
	case ".md", ".markdown":
		return true
	}
	return false
}
