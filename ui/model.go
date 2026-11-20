// Package ui is the bubbletea front end: menus are drawn with lipgloss,
// text documents go into a viewport, and markdown documents are rendered
// with glamour first.
package ui

import (
	"context"
	"errors"
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
	"github.com/Flonk/mazama/nntp"
)

type pageKind int

const (
	menuPage pageKind = iota
	textPage
)

// page is one entry in the history stack. Scroll positions are stored on it
// so going back returns you to where you were.
type page struct {
	addr      Location
	kind      pageKind
	items     []menuItem
	cursor    int
	offset    int
	raw       string
	markdown  bool
	keepLines bool // line-oriented markdown (from gemtext or news): keep its line breaks
	yOffset   int

	// Markdown links (see markdown.go). rendered is the display content
	// without the selected-link highlight; selLink is -1 when none is selected.
	rendered string
	links    []string
	spans    []linkSpan
	selLink  int
}

// menuItem is one menu line. Gopher menus only use the embedded item; other
// menus (news) set target, where the line leads, and tag, the type column.
type menuItem struct {
	gopher.Item
	target *Location
	search bool // ask for a query before opening target
	login  bool // ask for a news login, then reload target
	tag    string
}

func gopherItems(items []gopher.Item) []menuItem {
	out := make([]menuItem, len(items))
	for i, it := range items {
		out[i] = menuItem{Item: it}
	}
	return out
}

type inputMode int

const (
	noInput inputMode = iota
	gotoInput
	searchInput
	callbackInput // the answer goes to Model.onInput
	confirmInput  // a y/n question; "y" runs Model.onInput
)

type fetchedMsg struct {
	seq     int
	addr    Location
	mime    string        // gemini content type
	input   *inputRequest // gemini asked for input instead of a page
	data    []byte
	err     error
	replace bool // reload: swap the current page instead of pushing

	// News pages arrive ready-made: a menu, or an article as markdown.
	menu  bool
	items []menuItem
	text  string
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
	// onInput receives the answer in callbackInput mode.
	onInput func(m *Model, val string) tea.Cmd
	// typedLogin is a news login the user just typed, waiting for the
	// server to accept it before offering to save it.
	typedLogin *newsLogin

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

	gem  *gemini.Client
	news *nntp.Client
}

// New returns a model that opens start on launch, styled by a glamour style
// config (see LoadStyle). Menus get a matching Theme. gem fetches gemini://
// pages and carries the TOFU certificate store. News logins are looked up in
// the system keyring, ~/.authinfo and ~/.netrc, or asked for.
func New(start Location, style gansi.StyleConfig, gem *gemini.Client) Model {
	ti := textinput.New()
	ti.CharLimit = 1024
	m := Model{
		vp:       viewport.New(0, 0),
		input:    ti,
		styleCfg: style,
		theme:    NewTheme(style),
		gem:      gem,
		news:     defaultNewsClient(),
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
// On a news page, a bare group name (sci.astro.amateur) opens that group on
// the same server if it exists there.
func (m *Model) navigateInput(raw string) tea.Cmd {
	var from *Location
	if m.cur != nil {
		from = &m.cur.addr
	}
	news := m.news
	return m.startFetch(func(ctx context.Context) (Location, error) {
		return resolveInput(ctx, news, from, raw)
	}, false)
}

// resolveInput turns typed input into a Location: a bare group name on a
// news page (from) opens that group on the same server if it exists there;
// anything else goes to guessLocation.
func resolveInput(ctx context.Context, news *nntp.Client, from *Location, raw string) (Location, error) {
	name := strings.TrimSpace(raw)
	if from != nil && from.News != nil && looksLikeGroup(name) {
		if loc, found, err := resolveGroup(ctx, news, from.News.Server(), name, false); err == nil && found {
			return loc, nil
		}
	}
	return guessLocation(ctx, raw)
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
	gem, news := m.gem, m.news
	return func() tea.Msg {
		defer cancel()
		a, err := resolve(ctx)
		if err != nil {
			return fetchedMsg{seq: seq, err: err}
		}
		msg := fetchLocation(ctx, gem, news, a)
		msg.seq, msg.replace = seq, replace
		return msg
	}
}

// fetchLocation loads a in the foreground. Web locations aren't fetched;
// buildPage turns them into webPage.
func fetchLocation(ctx context.Context, gem *gemini.Client, news *nntp.Client, a Location) fetchedMsg {
	switch {
	case a.Web != nil:
		return fetchedMsg{addr: a}
	case a.Gemini != nil:
		return fetchGemini(ctx, gem, a.Gemini)
	case a.News != nil:
		return fetchNews(ctx, news, *a.News)
	default:
		data, err := gopher.Fetch(ctx, a.Gopher)
		return fetchedMsg{addr: a, data: data, err: err}
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
		var authErr *nntp.AuthError
		if errors.As(msg.err, &authErr) && msg.addr.News != nil {
			m.typedLogin = nil
			return m, m.askLogin(msg.addr, authErr.Failed, msg.replace)
		}
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
		if l := m.typedLogin; l != nil && msg.addr.News != nil && msg.addr.News.Host == l.host {
			// The server took the login just typed in.
			m.typedLogin = nil
			return m, m.offerSaveLogin(*l)
		}
		return m, nil

	case loginSavedMsg:
		if msg.err != nil {
			m.status = "error: couldn't save login: " + msg.err.Error()
		} else {
			m.status = "login for " + msg.host + " saved in the keyring"
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
	p, err := buildPage(msg)
	if err != nil {
		return err
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

// buildPage turns a successful fetch into an unrendered page.
func buildPage(msg fetchedMsg) (*page, error) {
	p := &page{addr: msg.addr, selLink: -1}
	switch {
	case msg.addr.Web != nil:
		p = webPage(msg.addr.Web)
	case msg.addr.Gemini != nil:
		if err := geminiPage(p, msg.mime, msg.data); err != nil {
			return nil, err
		}
	case msg.addr.News != nil && msg.menu:
		p.kind = menuPage
		p.items = msg.items
		p.cursor = firstSelectable(p.items)
	case msg.addr.News != nil:
		p.kind = textPage
		p.raw, p.markdown, p.keepLines = msg.text, true, true
	case msg.addr.Gopher.Type == gopher.TypeMenu, msg.addr.Gopher.Type == gopher.TypeSearch:
		p.kind = menuPage
		p.items = gopherItems(gopher.ParseMenu(msg.data))
		p.cursor = firstSelectable(p.items)
	default:
		p.kind = textPage
		p.raw = string(gopher.StripTerminator(msg.data))
		p.markdown = isMarkdown(msg.addr.Gopher.Selector)
	}
	return p, nil
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

// askInput opens the footer input and hands the answer to done.
func (m *Model) askInput(prompt string, sensitive bool, done func(m *Model, val string) tea.Cmd) tea.Cmd {
	m.mode = callbackInput
	m.onInput = done
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
		cfg := m.styleCfg
		if p.addr.News != nil {
			cfg = quoteBars(cfg)
		}
		content, links, spans, err := renderMarkdown(p.raw, cfg, min(m.width-4, 100), p.keepLines)
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
	case "n":
		if m.cur != nil && m.cur.addr.News != nil {
			return m, m.promptGroup()
		}
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

func (m Model) activate(it menuItem) (tea.Model, tea.Cmd) {
	if it.target != nil {
		if it.login {
			return m, m.askLogin(*it.target, false, true)
		}
		if it.search {
			return m, m.promptInput(*it.target, it.Display+":", false)
		}
		return m, m.navigate(*it.target, false)
	}
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
	if m.mode == confirmInput {
		// Any key answers; only y means yes.
		yes, done := msg.String() == "y" || msg.String() == "Y", m.onInput
		m.mode, m.onInput = noInput, nil
		m.input.Blur()
		if yes && done != nil {
			cmd := done(&m, "y")
			return m, cmd
		}
		return m, nil
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		m.mode = noInput
		m.onInput = nil
		m.input.SetValue("")
		m.input.Blur()
		return m, nil
	case "enter":
		if m.mode == callbackInput {
			// Taken verbatim: it may be a password.
			val, done := m.input.Value(), m.onInput
			m.mode, m.onInput = noInput, nil
			m.input.SetValue("")
			m.input.Blur()
			if val == "" || done == nil {
				return m, nil
			}
			cmd := done(&m, val)
			return m, cmd
		}
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

func firstSelectable(items []menuItem) int {
	for i, it := range items {
		if it.Selectable() {
			return i
		}
	}
	return 0
}

func nextSelectable(items []menuItem, from, dir int) int {
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
