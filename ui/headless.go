package ui

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/Flonk/mazama/gemini"
	"github.com/Flonk/mazama/gopher"
	"github.com/Flonk/mazama/nntp"
)

// Headless browsing: the fetching, page building and link handling the TUI
// uses, with pages returned as plain text plus a numbered list of links.
// This is what the command-line subcommands (and agents driving them) use.

// DefaultWidth is the wrap width for headless text pages.
const DefaultWidth = 100

// Browser fetches pages without a terminal UI.
type Browser struct {
	Gemini *gemini.Client
	News   *nntp.Client
	// Width wraps markdown and gemtext; DefaultWidth if <= 0.
	Width int
}

// NewBrowser returns a browser that looks up news logins like the TUI does
// (keyring, ~/.authinfo, ~/.netrc).
func NewBrowser(gem *gemini.Client) *Browser {
	return &Browser{Gemini: gem, News: defaultNewsClient()}
}

func defaultNewsClient() *nntp.Client {
	return &nntp.Client{Lookup: nntp.Lookups(nntp.KeyringLookup, nntp.NetrcLookup())}
}

// Page kinds.
const (
	KindMenu  = "menu"  // a gopher menu or news listing: one link per line
	KindText  = "text"  // a document, with links inline
	KindInput = "input" // the address asks for input first; see Page.Input
)

// Link actions: what following a link does.
const (
	ActionOpen     = "open"     // loads URL in mazama
	ActionQuery    = "query"    // needs text first (a search, or a gemini input)
	ActionLogin    = "login"    // needs a news login for URL's server, then loads URL
	ActionExternal = "external" // a web link, for a web browser
	ActionNone     = "none"     // can't be followed; Error says why
)

// Page is a fetched page as plain text.
type Page struct {
	URL  string `json:"url"`
	Kind string `json:"kind"`
	// Text is the page with each link marked [n] (menus: at the line start,
	// documents: after the link text).
	Text  string  `json:"text,omitempty"`
	Links []Link  `json:"links,omitempty"`
	Input *Prompt `json:"input,omitempty"` // set for KindInput
}

// Prompt is a request for input: answering it loads the page's URL with the
// answer as its query.
type Prompt struct {
	Prompt    string `json:"prompt"`
	Sensitive bool   `json:"sensitive,omitempty"` // a password or similar
}

// Link is one followable item on a page, numbered from 1.
type Link struct {
	N      int    `json:"n"`
	Label  string `json:"label"`
	URL    string `json:"url,omitempty"`
	Action string `json:"action"`
	Prompt string `json:"prompt,omitempty"` // for ActionQuery
	Error  string `json:"error,omitempty"`  // for ActionNone
}

// LinkN returns link n (1-based).
func (p *Page) LinkN(n int) (Link, error) {
	if n < 1 || n > len(p.Links) {
		if len(p.Links) == 0 {
			return Link{}, fmt.Errorf("%s has no links", p.URL)
		}
		return Link{}, fmt.Errorf("no link %d on %s (links are 1–%d)", n, p.URL, len(p.Links))
	}
	return p.Links[n-1], nil
}

// Open loads an address as typed: a URL, or a host[/path] whose protocol is
// guessed. from is the current page's URL ("" for none); on a news page a
// bare group name opens that group on the same server.
func (b *Browser) Open(ctx context.Context, raw, from string) (*Page, error) {
	var fromLoc *Location
	if from != "" {
		if l, err := parseAnyLocation(from); err == nil {
			fromLoc = &l
		}
	}
	loc, err := resolveInput(ctx, b.News, fromLoc, raw)
	if err != nil {
		return nil, err
	}
	return b.load(ctx, loc)
}

// Query loads an absolute URL with an answer attached: a gopher search, a
// gemini input, or a news group search (when rawURL is a news server).
func (b *Browser) Query(ctx context.Context, rawURL, query string) (*Page, error) {
	loc, err := parseAnyLocation(rawURL)
	if err != nil {
		return nil, err
	}
	if loc.Web != nil {
		return nil, errors.New("web pages don't take input")
	}
	return b.load(ctx, loc.withQuery(query))
}

// Group opens a Usenet group on the server of from, the current page; a
// name that isn't a group there becomes a search.
func (b *Browser) Group(ctx context.Context, from, name string) (*Page, error) {
	loc, err := parseAnyLocation(from)
	if err != nil {
		return nil, err
	}
	if loc.News == nil {
		return nil, fmt.Errorf("%s isn't a news page", from)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("no group name")
	}
	loc, _, err = resolveGroup(ctx, b.News, loc.News.Server(), name, true)
	if err != nil {
		return nil, err
	}
	return b.load(ctx, loc)
}

// Login logs in to the news server of rawURL (a news URL or a bare host) to
// check the login works, and keeps it for this browser's later requests.
func (b *Browser) Login(ctx context.Context, rawURL, user, pass string) (host string, err error) {
	a, err := newsServerOf(rawURL)
	if err != nil {
		return "", err
	}
	b.News.SetLogin(a.Host, user, pass)
	cn, err := b.News.Dial(ctx, a)
	if err != nil {
		return a.Host, err
	}
	defer cn.Close()
	if !cn.LoggedIn() {
		if !cn.Encrypted() {
			return a.Host, &nntp.InsecureError{Host: a.Host}
		}
		return a.Host, &nntp.AuthError{Host: a.Host, Failed: true}
	}
	return a.Host, nil
}

// newsServerOf parses a news URL, or a host[:port] which is taken to be
// nntps when the port is 563 and news otherwise.
func newsServerOf(raw string) (nntp.Address, error) {
	raw = strings.TrimSpace(raw)
	if !hasScheme(raw) {
		scheme := "news"
		if u, err := url.Parse("//" + raw); err == nil && u.Port() == nntp.DefaultTLSPort {
			scheme = "nntps"
		}
		raw = scheme + "://" + raw
	}
	loc, err := ParseLocation(raw)
	if err != nil {
		return nntp.Address{}, err
	}
	if loc.News == nil {
		return nntp.Address{}, fmt.Errorf("%s isn't a news server", raw)
	}
	return loc.News.Server(), nil
}

func (b *Browser) load(ctx context.Context, loc Location) (*Page, error) {
	if loc.Web == nil && loc.Gemini == nil && loc.News == nil &&
		loc.Gopher.Type == gopher.TypeSearch && loc.Gopher.Query == "" {
		// A gopher search without a query: ask for one, as following
		// the search item in the TUI does.
		return &Page{URL: loc.String(), Kind: KindInput, Input: &Prompt{Prompt: "search:"}}, nil
	}
	msg := fetchLocation(ctx, b.Gemini, b.News, loc)
	if msg.err != nil {
		return nil, msg.err
	}
	if msg.input != nil {
		return &Page{URL: msg.addr.String(), Kind: KindInput,
			Input: &Prompt{Prompt: sanitize(msg.input.prompt), Sensitive: msg.input.sensitive}}, nil
	}
	p, err := buildPage(msg)
	if err != nil {
		return nil, err
	}
	if p.kind == menuPage {
		return menuText(p), nil
	}
	return b.docText(p)
}

// menuText lays a menu out one item per line: "[n] TAG display" for links,
// indented text for info lines.
func menuText(p *page) *Page {
	out := &Page{URL: p.addr.String(), Kind: KindMenu}
	n := 0
	for _, it := range p.items {
		if it.Selectable() {
			n++
		}
	}
	numW := len(fmt.Sprintf("[%d] ", n))
	var b strings.Builder
	n = 0
	for _, it := range p.items {
		display := sanitize(strings.ReplaceAll(it.Display, "\t", "    "))
		if !it.Selectable() {
			tag := ""
			if it.Type == gopher.TypeError {
				tag = "ERR"
			}
			b.WriteString(strings.TrimRight(fmt.Sprintf("%s%-5s%s", strings.Repeat(" ", numW), tag, display), " ") + "\n")
			continue
		}
		n++
		l := menuLink(p.addr, it)
		l.N, l.Label = n, strings.TrimSpace(display)
		out.Links = append(out.Links, l)
		b.WriteString(strings.TrimRight(fmt.Sprintf("%-*s%-5s%s", numW, fmt.Sprintf("[%d]", n), itemTag(it), display), " ") + "\n")
	}
	out.Text = strings.TrimRight(b.String(), " \n") + "\n"
	return out
}

// menuLink says where a menu item leads, like Model.activate does.
func menuLink(base Location, it menuItem) Link {
	if it.target != nil {
		switch {
		case it.login:
			return Link{URL: it.target.String(), Action: ActionLogin}
		case it.search:
			return Link{URL: it.target.String(), Action: ActionQuery, Prompt: it.Display + ":"}
		}
		return Link{URL: it.target.String(), Action: ActionOpen}
	}
	addr := Location{Gopher: it.Address()}.String()
	switch it.Type {
	case gopher.TypeMenu, gopher.TypeText:
		return Link{URL: addr, Action: ActionOpen}
	case gopher.TypeSearch:
		return Link{URL: addr, Action: ActionQuery, Prompt: "search:"}
	case gopher.TypeHTML:
		if u := it.ExternalURL(); u != "" {
			return classifyLink(base, u)
		}
		return Link{URL: addr, Action: ActionOpen}
	}
	return Link{URL: addr, Action: ActionNone, Error: fmt.Sprintf("item type %q is not supported yet", it.Type)}
}

// classifyLink resolves a link target on the page at base and says what
// following it does, like Model.openLink.
func classifyLink(base Location, raw string) Link {
	abs, err := resolveLink(base, raw)
	if err != nil {
		return Link{URL: raw, Action: ActionNone, Error: err.Error()}
	}
	u, err := url.Parse(abs)
	if err != nil {
		return Link{URL: abs, Action: ActionNone, Error: err.Error()}
	}
	switch strings.ToLower(u.Scheme) {
	case "gopher", "gemini", "news", "nntp", "nntps", "snews":
		loc, err := ParseLocation(abs)
		if err != nil {
			return Link{URL: abs, Action: ActionNone, Error: err.Error()}
		}
		if loc.Gemini == nil && loc.News == nil && loc.Gopher.Type == gopher.TypeSearch && loc.Gopher.Query == "" {
			return Link{URL: loc.String(), Action: ActionQuery, Prompt: "search:"}
		}
		return Link{URL: loc.String(), Action: ActionOpen}
	}
	if externalSchemes[strings.ToLower(u.Scheme)] {
		return Link{URL: abs, Action: ActionExternal}
	}
	return Link{URL: abs, Action: ActionNone, Error: fmt.Sprintf("mazama doesn't open %q links", u.Scheme)}
}

// docText renders a document as plain text. Markdown (and gemtext) is laid
// out with glamour's ASCII style, and each link's text is followed by [n].
func (b *Browser) docText(p *page) (*Page, error) {
	out := &Page{URL: p.addr.String(), Kind: KindText}
	if !p.markdown {
		out.Text = strings.TrimRight(sanitize(strings.ReplaceAll(p.raw, "\t", "    ")), "\n") + "\n"
		return out, nil
	}
	width := b.Width
	if width <= 0 {
		width = DefaultWidth
	}
	cfg := styles.ASCIIStyleConfig
	if p.addr.News != nil {
		cfg = quoteBars(cfg)
	}
	content, targets, spans, err := renderMarkdown(p.raw, cfg, width, p.keepLines)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(ansi.Strip(content), "\n")

	// Labels come from the rendered text, which may wrap across lines.
	labels := make([][]string, len(targets))
	last := map[int]linkSpan{} // id -> its last span, where [n] goes
	for _, s := range spans {
		if s.line < len(lines) {
			labels[s.id] = append(labels[s.id], strings.TrimSpace(ansi.Cut(lines[s.line], s.start, s.end)))
		}
		if l, ok := last[s.id]; !ok || s.line > l.line || (s.line == l.line && s.end > l.end) {
			last[s.id] = s
		}
	}
	marks := make([]linkSpan, 0, len(last))
	for _, s := range last {
		marks = append(marks, s)
	}
	// Right to left, so earlier columns stay valid.
	sort.Slice(marks, func(i, j int) bool {
		if marks[i].line != marks[j].line {
			return marks[i].line < marks[j].line
		}
		return marks[i].end > marks[j].end
	})
	for _, s := range marks {
		if s.line >= len(lines) {
			continue
		}
		line := lines[s.line]
		lines[s.line] = ansi.Cut(line, 0, s.end) + fmt.Sprintf("[%d]", s.id+1) + ansi.Cut(line, s.end, ansi.StringWidth(line))
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	out.Text = strings.Trim(strings.Join(lines, "\n"), "\n") + "\n"

	for id, t := range targets {
		l := classifyLink(p.addr, t)
		l.N, l.Label = id+1, strings.Join(labels[id], " ")
		if l.Label == "" {
			l.Label = t
		}
		out.Links = append(out.Links, l)
	}
	return out, nil
}

// itemTag is the type column of a menu line.
func itemTag(it menuItem) string {
	if it.tag != "" {
		return it.tag
	}
	switch it.Type {
	case gopher.TypeInfo:
		return ""
	case gopher.TypeError:
		return "ERR"
	case gopher.TypeMenu:
		return "DIR"
	case gopher.TypeText:
		return "TXT"
	case gopher.TypeSearch:
		return "ASK"
	case gopher.TypeHTML:
		if it.ExternalURL() != "" {
			return "URL"
		}
		return "HTM"
	case gopher.TypeGIF, gopher.TypeImage:
		return "IMG"
	case gopher.TypeSound:
		return "SND"
	case gopher.TypeBinary, gopher.TypeDOS, gopher.TypeBinHex, gopher.TypeUUE:
		return "BIN"
	case gopher.TypeTelnet, gopher.TypeTN3270:
		return "TEL"
	default:
		return "?" + string(it.Type)
	}
}
