package ui

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"

	"github.com/Flonk/mazama/gopher"
	"github.com/Flonk/mazama/nntp"
)

// Usenet pages. A server root and wildmat addresses list groups, a group
// address lists its newest articles, and an article opens as text.

const (
	// articlesPerPage is how many overview lines a group page shows.
	articlesPerPage = 200
	// collapseAbove: a hierarchy listing with more groups than this shows
	// sub-hierarchies (comp.lang.*) instead of every group.
	collapseAbove = 100
	// maxGroupsListed caps flat listings, e.g. a search for "*a*".
	maxGroupsListed = 1000
)

// fetchNews loads a news page in the background. Menus come back as items,
// articles as text.
func fetchNews(ctx context.Context, c *nntp.Client, a nntp.Address) fetchedMsg {
	msg := fetchedMsg{addr: Location{News: &a}}
	cn, err := c.Dial(ctx, a)
	if err != nil {
		msg.err = err
		return msg
	}
	defer cn.Close()
	switch {
	case a.IsArticle():
		msg.text, msg.err = articleText(cn, a)
	case a.IsListing():
		msg.items, msg.err = groupListing(cn, a)
		msg.menu = true
	default:
		msg.items, msg.err = groupMenu(cn, a)
		msg.menu = true
	}
	if msg.err != nil && ctx.Err() != nil {
		msg.err = ctx.Err()
	}
	return msg
}

func newsInfo(s string) menuItem {
	return menuItem{Item: gopher.Item{Type: gopher.TypeInfo, Display: s}}
}

func newsLink(a nntp.Address, typ byte, tag, display string) menuItem {
	return menuItem{Item: gopher.Item{Type: typ, Display: display}, target: &Location{News: &a}, tag: tag}
}

// groupListing lists the groups matching a wildmat. Whole hierarchies that
// are too big to list are folded into their sub-hierarchies, so the root of
// a server with tens of thousands of groups is a short menu.
func groupListing(cn *nntp.Conn, a nntp.Address) ([]menuItem, error) {
	groups, err := cn.ActiveGroups()
	if err != nil {
		return nil, err
	}
	pattern := a.Group
	if pattern == "" {
		pattern = "*"
	}
	var names []string
	for _, g := range groups {
		ok, err := nntp.MatchWildmat(pattern, g.Name)
		if err != nil {
			return nil, fmt.Errorf("bad pattern %q: %v", pattern, err)
		}
		if ok {
			names = append(names, g.Name)
		}
	}
	sort.Strings(names)

	srv := a.Server()
	title := a.Host
	if a.Group != "" {
		title = a.Group + " on " + a.Host
	}
	items := []menuItem{newsInfo(fmt.Sprintf("%s · %s", title, plural(len(names), "group")))}
	search := newsLink(srv, gopher.TypeSearch, "ASK", "search groups")
	search.search = true
	items = append(items, search)
	// Servers like Eternal September show a few groups to anyone and the
	// rest after logging in, without ever asking for a login.
	if !cn.LoggedIn() && cn.Encrypted() {
		login := newsLink(a, gopher.TypeSearch, "KEY", "log in")
		login.login = true
		items = append(items, login)
	}
	items = append(items, newsInfo(""))

	if prefix, ok := nntp.HierarchyPrefix(pattern); ok && len(names) > collapseAbove {
		// Folding a flat hierarchy (comp.lang.a, comp.lang.b, ...) gains
		// nothing; list it with descriptions instead.
		if folded := foldHierarchy(srv, prefix, names); len(folded) < len(names) {
			return append(items, folded...), nil
		}
	}
	if len(names) == 0 {
		return append(items, newsInfo("no matching groups")), nil
	}
	shown := names
	if len(shown) > maxGroupsListed {
		shown = shown[:maxGroupsListed]
	}
	descs, _ := cn.Descriptions(pattern) // optional; many servers have none
	width := 0
	for _, n := range shown {
		width = max(width, min(runewidth.StringWidth(n), 40))
	}
	for _, n := range shown {
		g := srv
		g.Group = n
		display := n
		if d := descs[n]; d != "" && d != "?" {
			display = runewidth.FillRight(n, width) + "  " + d
		}
		items = append(items, newsLink(g, gopher.TypeMenu, "GRP", display))
	}
	if len(shown) < len(names) {
		items = append(items, newsInfo(""), newsInfo(fmt.Sprintf("… and %d more; search to narrow it down", len(names)-len(shown))))
	}
	return items, nil
}

// foldHierarchy turns the groups under prefix into one entry per next name
// component: the group itself if it exists, and a "prefix.x.*" entry if it
// has subgroups (or the subgroup itself, if there's only one).
func foldHierarchy(srv nntp.Address, prefix string, names []string) []menuItem {
	type node struct {
		exact bool
		below int
		only  string // the subgroup, when below == 1
	}
	nodes := map[string]*node{}
	var comps []string
	for _, n := range names {
		comp, _, deeper := strings.Cut(strings.TrimPrefix(n, prefix), ".")
		nd := nodes[comp]
		if nd == nil {
			nd = &node{}
			nodes[comp] = nd
			comps = append(comps, comp)
		}
		if deeper {
			nd.below++
			nd.only = n
		} else {
			nd.exact = true
		}
	}
	sort.Strings(comps)
	var items []menuItem
	for _, c := range comps {
		nd := nodes[c]
		if nd.exact {
			g := srv
			g.Group = prefix + c
			items = append(items, newsLink(g, gopher.TypeMenu, "GRP", g.Group))
		}
		if nd.below == 1 {
			g := srv
			g.Group = nd.only
			items = append(items, newsLink(g, gopher.TypeMenu, "GRP", g.Group))
		} else if nd.below > 1 {
			h := srv
			h.Group = prefix + c + ".*"
			items = append(items, newsLink(h, gopher.TypeMenu, "DIR", fmt.Sprintf("%s  (%s)", h.Group, plural(nd.below, "group"))))
		}
	}
	return items
}

// groupMenu lists a page of a group's articles, newest first.
func groupMenu(cn *nntp.Conn, a nntp.Address) ([]menuItem, error) {
	gi, err := cn.SelectGroup(a.Group)
	if err != nil {
		return nil, err
	}
	hi := gi.High
	if a.Before > 0 {
		hi = min(hi, a.Before-1)
	}
	lo := max(gi.Low, hi-articlesPerPage+1)
	var ov []nntp.Overview
	if gi.Count > 0 && hi >= lo {
		if ov, err = cn.Overviews(lo, hi); err != nil {
			return nil, err
		}
	}

	head := fmt.Sprintf("%s · %s", a.Group, plural(int(gi.Count), "article"))
	if a.Before > 0 {
		head += fmt.Sprintf(" · older than #%d", a.Before)
	}
	items := []menuItem{newsInfo(head), newsInfo("")}
	if len(ov) == 0 {
		items = append(items, newsInfo("no articles"))
	}
	srv := a.Server()
	for i := len(ov) - 1; i >= 0; i-- {
		o := ov[i]
		target := srv
		if o.MessageID != "" {
			target.MessageID = o.MessageID
		} else {
			target.Group, target.Number = a.Group, o.Number
		}
		items = append(items, newsLink(target, gopher.TypeText, "ART", overviewLine(o)))
	}
	if lo > gi.Low {
		older := a
		older.Before = lo
		items = append(items, newsInfo(""), newsLink(older, gopher.TypeMenu, "OLD", "older articles"))
	}
	return items, nil
}

func overviewLine(o nntp.Overview) string {
	date := "          "
	if t, err := mail.ParseDate(o.Date); err == nil {
		date = t.Local().Format(time.DateOnly)
	}
	from := runewidth.FillRight(runewidth.Truncate(nntp.DisplayName(o.From), 20, "…"), 20)
	return date + "  " + from + "  " + sanitize(o.Subject)
}

// articleText fetches an article and lays it out as markdown (see
// article.go).
func articleText(cn *nntp.Conn, a nntp.Address) (string, error) {
	raw, err := cn.Article(a)
	if err != nil {
		return "", err
	}
	art, err := nntp.ParseArticle(raw)
	if err != nil {
		return "", err
	}
	return articleMarkdown(art, a.Server()), nil
}

// sanitize drops control characters (escape sequences in an article must
// not reach the terminal), keeping newlines and tabs.
func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// searchPattern turns a group search into a wildmat: plain words match
// anywhere in the name.
func searchPattern(q string) string {
	if nntp.IsWildmat(q) {
		return q
	}
	return "*" + q + "*"
}

// askLogin prompts for a news login, then retries target with it.
func (m *Model) askLogin(target Location, failed, replace bool) tea.Cmd {
	host := target.News.Host
	prompt := "login for " + host + ":"
	if failed {
		prompt = "login rejected; user for " + host + ":"
	}
	return m.askInput(prompt, false, func(m *Model, user string) tea.Cmd {
		user = strings.TrimSpace(user)
		if user == "" {
			return nil
		}
		return m.askInput("password:", true, func(m *Model, pass string) tea.Cmd {
			m.news.SetLogin(host, user, pass)
			m.typedLogin = &newsLogin{host, user, pass}
			return m.navigate(target, replace)
		})
	})
}

// looksLikeGroup reports whether typed input could be a group name or
// wildmat rather than a URL or host/path.
func looksLikeGroup(s string) bool {
	return s != "" && !strings.ContainsAny(s, "/: \t") && !hasScheme(s)
}

// resolveGroup finds where a typed group name leads on server: the group if
// it exists, else (when searchMissing) a search for it. found is false when
// the server has no such group and searchMissing is off.
func resolveGroup(ctx context.Context, c *nntp.Client, server nntp.Address, name string, searchMissing bool) (loc Location, found bool, err error) {
	a := server
	if nntp.IsWildmat(name) {
		a.Group = name
		return Location{News: &a}, true, nil
	}
	cn, err := c.Dial(ctx, server)
	if err == nil {
		_, err = cn.SelectGroup(name)
		cn.Close()
	}
	switch {
	case errors.Is(err, nntp.ErrNoSuchGroup) && searchMissing:
		a.Group = searchPattern(name)
	case errors.Is(err, nntp.ErrNoSuchGroup):
		return Location{}, false, nil
	case err != nil && !isLoginError(err):
		return Location{}, false, err
	default:
		// Found, or the server wants a login first: opening the group
		// will ask for it.
		a.Group = name
	}
	return Location{News: &a}, true, nil
}

func isLoginError(err error) bool {
	var ae *nntp.AuthError
	var ie *nntp.InsecureError
	return errors.As(err, &ae) || errors.As(err, &ie)
}

// promptGroup asks for a group on the current news server and opens it.
func (m *Model) promptGroup() tea.Cmd {
	server := m.cur.addr.News.Server()
	return m.askInput("group:", false, func(m *Model, name string) tea.Cmd {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil
		}
		news := m.news
		return m.startFetch(func(ctx context.Context) (Location, error) {
			loc, _, err := resolveGroup(ctx, news, server, name, true)
			return loc, err
		}, false)
	})
}

// newsLogin is a login typed in at the prompt.
type newsLogin struct{ host, user, pass string }

// saveLogin stores a login; tests replace it to keep off the real keyring.
var saveLogin = nntp.SaveLogin

type loginSavedMsg struct {
	host string
	err  error
}

// offerSaveLogin asks whether to keep a login that just worked.
func (m *Model) offerSaveLogin(l newsLogin) tea.Cmd {
	m.mode = confirmInput
	m.onInput = func(m *Model, _ string) tea.Cmd {
		return func() tea.Msg {
			// The keyring may prompt to unlock, so save off the UI loop.
			return loginSavedMsg{l.host, saveLogin(l.host, l.user, l.pass)}
		}
	}
	m.input.Prompt = ansi.Truncate("remember login for "+l.host+" in the keyring? (y/n)", max(m.width-2, 10), "…") + " "
	m.input.SetValue("")
	m.input.EchoMode = textinput.EchoNormal
	return m.input.Focus()
}
