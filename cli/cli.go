// Package cli is mazama's non-interactive interface: subcommands that do
// what the TUI's keys do and print pages as plain text (or JSON) with
// numbered links, so scripts and agents can browse. Commands other than
// get share a session (the current page and history) stored on disk.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Flonk/mazama/gemini"
	"github.com/Flonk/mazama/nntp"
	"github.com/Flonk/mazama/ui"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
	// ExitLogin means a news server wants a login: run mazama login.
	ExitLogin = 3
)

type command struct {
	name    string
	args    string
	summary string
	run     func(e *env, args []string) error
	// stateless commands don't load or save the session.
	stateless bool
}

var commands []command

func init() {
	commands = []command{
		{name: "open", args: "<address>", summary: "open an address (URL or host[/path]) and make it the current page", run: cmdOpen},
		{name: "follow", args: "<n>", summary: "follow link n on the current page", run: cmdFollow},
		{name: "input", args: "<text>", summary: "answer the current page's input prompt (search, gemini input)", run: cmdInput},
		{name: "back", summary: "go back to the previous page", run: cmdBack},
		{name: "reload", summary: "fetch the current page again", run: cmdReload},
		{name: "show", summary: "print the current page again (from the session, no fetch)", run: cmdShow},
		{name: "links", summary: "list the current page's links", run: cmdLinks},
		{name: "history", summary: "list the pages back will return to", run: cmdHistory},
		{name: "group", args: "<name>", summary: "open a Usenet group (or search groups) on the current news server", run: cmdGroup},
		{name: "login", args: "[server]", summary: "log in to a news server (password on stdin) and save it in the keyring", run: cmdLogin},
		{name: "get", args: "<address>", summary: "print a page without touching the session", run: cmdGet, stateless: true},
		{name: "help", summary: "show this help", run: cmdHelp, stateless: true},
	}
}

// IsCommand reports whether arg names a subcommand.
func IsCommand(arg string) bool {
	_, ok := lookup(arg)
	return ok
}

func lookup(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// env is what a command runs with.
type env struct {
	ctx     context.Context
	browser *ui.Browser
	sess    *session
	out     io.Writer
	errOut  io.Writer
	in      io.Reader

	json bool
	// command-specific flags
	query    string
	stdin    bool
	browse   bool
	user     string
	noSave   bool
	noLinks  bool
	saveFunc func(host, user, pass string) error
}

// Options configures Run.
type Options struct {
	Gemini         *gemini.Client
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	// News replaces the news client (tests).
	News *nntp.Client
	// SaveLogin replaces nntp.SaveLogin (tests).
	SaveLogin func(host, user, pass string) error
}

// Run runs the subcommand args[0] with args[1:] and returns the exit code.
func Run(args []string, o Options) int {
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
	cmd, ok := lookup(args[0])
	if !ok {
		fmt.Fprintf(o.Stderr, "mazama: unknown command %q\n", args[0])
		return ExitUsage
	}

	fs := flag.NewFlagSet("mazama "+cmd.name, flag.ContinueOnError)
	fs.SetOutput(o.Stderr)
	e := &env{out: o.Stdout, errOut: o.Stderr, in: o.Stdin, saveFunc: o.SaveLogin}
	if e.saveFunc == nil {
		e.saveFunc = nntp.SaveLogin
	}
	sessName := fs.String("session", envOr("MAZAMA_SESSION", "default"), "session name (or $MAZAMA_SESSION); separate sessions browse independently")
	width := fs.Int("width", ui.DefaultWidth, "wrap documents at this many columns")
	timeout := fs.Duration("timeout", 30*time.Second, "give up on a fetch after this long")
	fs.BoolVar(&e.json, "json", false, "print JSON instead of text")
	switch cmd.name {
	case "open", "get", "follow":
		fs.StringVar(&e.query, "q", "", "answer the input the page or link asks for (search query, gemini input)")
	}
	switch cmd.name {
	case "input":
		fs.BoolVar(&e.stdin, "stdin", false, "read the answer from stdin (for sensitive input)")
	case "follow":
		fs.BoolVar(&e.browse, "browser", false, "open web links in the web browser")
	case "login":
		fs.StringVar(&e.user, "user", "", "user name (required)")
		fs.BoolVar(&e.noSave, "no-save", false, "only check the login, don't save it in the keyring")
	}
	switch cmd.name {
	case "open", "get", "follow", "input", "back", "reload", "show", "group":
		fs.BoolVar(&e.noLinks, "no-links", false, "leave out the list of link URLs after the page")
	}
	fs.Usage = func() {
		fmt.Fprintf(o.Stderr, "usage: mazama %s [flags] %s\n\n%s\n\n", cmd.name, cmd.args, capitalize(cmd.summary))
		fs.PrintDefaults()
	}
	pos, err := parseInterspersed(fs, args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return ExitOK
	}
	if err != nil {
		return ExitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	e.ctx = ctx
	e.browser = ui.NewBrowser(o.Gemini)
	if o.News != nil {
		e.browser.News = o.News
	}
	e.browser.Width = *width
	if !cmd.stateless {
		if e.sess, err = loadSession(*sessName); err != nil {
			fmt.Fprintln(o.Stderr, "mazama:", err)
			return ExitError
		}
	}

	// Output is held back until the session is saved: a reader that stops
	// early (mazama show | head) must not lose the navigation.
	var buf bytes.Buffer
	e.out = &buf
	err = cmd.run(e, pos)
	flush := func() { o.Stdout.Write(buf.Bytes()) }
	defer flush()
	var ue usageError
	switch {
	case err == nil:
	case errors.As(err, &ue):
		fmt.Fprintln(o.Stderr, "mazama:", err)
		fs.Usage()
		return ExitUsage
	default:
		fmt.Fprintln(o.Stderr, "mazama:", err)
		var ae *nntp.AuthError
		if errors.As(err, &ae) {
			fmt.Fprintf(o.Stderr, "log in with: echo PASSWORD | mazama login %s --user USER\n", ae.Host)
			return ExitLogin
		}
		return ExitError
	}
	if e.sess != nil {
		if err := e.sess.save(); err != nil {
			fmt.Fprintln(o.Stderr, "mazama: saving session:", err)
			return ExitError
		}
	}
	return ExitOK
}

// parseInterspersed parses flags anywhere among the arguments, so
// "follow 3 -q foo" works like "follow -q foo 3". "--" ends flags.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if used := len(args) - len(rest); used > 0 && args[used-1] == "--" {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

type usageError string

func (u usageError) Error() string { return string(u) }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func oneArg(args []string, what string) (string, error) {
	if len(args) == 0 {
		return "", usageError("missing " + what)
	}
	if len(args) > 1 {
		return "", usageError(fmt.Sprintf("too many arguments: %q", args[1:]))
	}
	return args[0], nil
}

func noArgs(args []string) error {
	if len(args) > 0 {
		return usageError(fmt.Sprintf("unexpected arguments: %q", args))
	}
	return nil
}

// fetch loads an address, answering its input prompt with e.query if set.
func (e *env) fetch(addr, from string) (*ui.Page, error) {
	p, err := e.browser.Open(e.ctx, addr, from)
	if err != nil {
		return nil, err
	}
	return e.answer(p)
}

// answer fills in an input page with e.query, if one was given.
func (e *env) answer(p *ui.Page) (*ui.Page, error) {
	if p.Kind != ui.KindInput || e.query == "" {
		return p, nil
	}
	return e.browser.Query(e.ctx, p.URL, e.query)
}

func cmdOpen(e *env, args []string) error {
	addr, err := oneArg(args, "address")
	if err != nil {
		return err
	}
	from := ""
	if e.sess.Current != nil {
		from = e.sess.Current.URL
	}
	p, err := e.fetch(addr, from)
	if err != nil {
		return err
	}
	e.sess.push(p)
	return e.printPage(p)
}

func cmdGet(e *env, args []string) error {
	addr, err := oneArg(args, "address")
	if err != nil {
		return err
	}
	p, err := e.fetch(addr, "")
	if err != nil {
		return err
	}
	return e.printPage(p)
}

func cmdFollow(e *env, args []string) error {
	arg, err := oneArg(args, "link number")
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(strings.Trim(arg, "[]"))
	if err != nil {
		return usageError(fmt.Sprintf("%q isn't a link number", arg))
	}
	cur, err := e.sess.current()
	if err != nil {
		return err
	}
	l, err := cur.LinkN(n)
	if err != nil {
		return err
	}
	var p *ui.Page
	switch l.Action {
	case ui.ActionOpen:
		if p, err = e.fetch(l.URL, ""); err != nil {
			return err
		}
	case ui.ActionQuery:
		if e.query == "" {
			// Like the TUI's prompt: the next step is mazama input.
			p = &ui.Page{URL: l.URL, Kind: ui.KindInput, Input: &ui.Prompt{Prompt: l.Prompt}}
		} else if p, err = e.browser.Query(e.ctx, l.URL, e.query); err != nil {
			return err
		}
	case ui.ActionLogin:
		host := l.URL
		if a, err := nntp.ParseURL(l.URL); err == nil {
			host = a.Host
		}
		return fmt.Errorf("link %d logs in to %s: run `echo PASSWORD | mazama login --user USER`, then `mazama reload`", n, host)
	case ui.ActionExternal:
		if !e.browse {
			if e.json {
				return e.printJSON(l)
			}
			fmt.Fprintf(e.out, "web link (mazama doesn't fetch the web): %s\nopen it in a web browser with: mazama follow %d --browser\n", l.URL, n)
			return nil
		}
		if err := ui.OpenInBrowser(l.URL); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "opened", l.URL)
		return nil
	default:
		return fmt.Errorf("link %d (%s) can't be followed: %s", n, l.URL, l.Error)
	}
	e.sess.push(p)
	return e.printPage(p)
}

func cmdInput(e *env, args []string) error {
	var text string
	if e.stdin {
		if len(args) > 0 {
			return usageError("give the text either as an argument or with --stdin, not both")
		}
		line, err := bufio.NewReader(e.in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		text = strings.TrimRight(line, "\r\n")
	} else {
		if len(args) == 0 {
			return usageError("missing text")
		}
		text = strings.Join(args, " ")
	}
	if text == "" {
		return usageError("empty input")
	}
	cur, err := e.sess.current()
	if err != nil {
		return err
	}
	if cur.Kind != ui.KindInput {
		return errors.New("the current page doesn't ask for input; to answer a search link, use: mazama follow <n> -q <text>")
	}
	p, err := e.browser.Query(e.ctx, cur.URL, text)
	if err != nil {
		return err
	}
	// The prompt isn't a page to go back to.
	e.sess.replace(p)
	return e.printPage(p)
}

func cmdBack(e *env, args []string) error {
	if err := noArgs(args); err != nil {
		return err
	}
	p, err := e.sess.back()
	if err != nil {
		return err
	}
	return e.printPage(p)
}

func cmdReload(e *env, args []string) error {
	if err := noArgs(args); err != nil {
		return err
	}
	cur, err := e.sess.current()
	if err != nil {
		return err
	}
	if cur.Kind == ui.KindInput {
		return e.printPage(cur)
	}
	p, err := e.browser.Open(e.ctx, cur.URL, "")
	if err != nil {
		return err
	}
	e.sess.replace(p)
	return e.printPage(p)
}

func cmdShow(e *env, args []string) error {
	if err := noArgs(args); err != nil {
		return err
	}
	cur, err := e.sess.current()
	if err != nil {
		return err
	}
	return e.printPage(cur)
}

func cmdLinks(e *env, args []string) error {
	if err := noArgs(args); err != nil {
		return err
	}
	cur, err := e.sess.current()
	if err != nil {
		return err
	}
	if e.json {
		return e.printJSON(cur.Links)
	}
	if len(cur.Links) == 0 {
		fmt.Fprintln(e.out, "no links")
		return nil
	}
	for _, l := range cur.Links {
		fmt.Fprintf(e.out, "%s  %s\n", linkLine(l, len(cur.Links)), l.Label)
	}
	return nil
}

func cmdHistory(e *env, args []string) error {
	if err := noArgs(args); err != nil {
		return err
	}
	type entry struct {
		Back    int    `json:"back"` // how many "back"s reach it; 0 is the current page
		URL     string `json:"url"`
		Kind    string `json:"kind"`
		Current bool   `json:"current,omitempty"`
	}
	var list []entry
	if e.sess.Current != nil {
		list = append(list, entry{0, e.sess.Current.URL, e.sess.Current.Kind, true})
	}
	for i := len(e.sess.History) - 1; i >= 0; i-- {
		p := e.sess.History[i]
		list = append(list, entry{len(e.sess.History) - i, p.URL, p.Kind, false})
	}
	if e.json {
		if list == nil {
			list = []entry{}
		}
		return e.printJSON(list)
	}
	if len(list) == 0 {
		fmt.Fprintln(e.out, "no pages yet")
	}
	for _, en := range list {
		mark := " "
		if en.Current {
			mark = "*"
		}
		fmt.Fprintf(e.out, "%s %3d  %s\n", mark, en.Back, en.URL)
	}
	return nil
}

func cmdGroup(e *env, args []string) error {
	if len(args) == 0 {
		return usageError("missing group name")
	}
	cur, err := e.sess.current()
	if err != nil {
		return err
	}
	p, err := e.browser.Group(e.ctx, cur.URL, strings.Join(args, " "))
	if err != nil {
		return err
	}
	e.sess.push(p)
	return e.printPage(p)
}

func cmdLogin(e *env, args []string) error {
	if len(args) > 1 {
		return usageError(fmt.Sprintf("too many arguments: %q", args[1:]))
	}
	server := ""
	if len(args) == 1 {
		server = args[0]
	} else if e.sess.Current != nil {
		server = e.sess.Current.URL
	} else {
		return usageError("missing server (no current page to take it from)")
	}
	if e.user == "" {
		return usageError("missing --user")
	}
	// The password comes on stdin so it stays out of argv and shell history.
	line, err := bufio.NewReader(e.in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	pass := strings.TrimRight(line, "\r\n")
	if pass == "" {
		return usageError("no password on stdin")
	}
	host, err := e.browser.Login(e.ctx, server, e.user, pass)
	if err != nil {
		return err
	}
	if e.noSave {
		fmt.Fprintf(e.out, "login for %s works (not saved)\n", host)
		return nil
	}
	if err := e.saveFunc(host, e.user, pass); err != nil {
		return fmt.Errorf("login works, but saving it failed: %v", err)
	}
	fmt.Fprintf(e.out, "logged in to %s; login saved in the keyring\n", host)
	return nil
}

func cmdHelp(e *env, args []string) error {
	Usage(e.out)
	return nil
}

// Usage prints the overview of all modes and commands.
func Usage(w io.Writer) {
	fmt.Fprint(w, `usage: mazama [-style STYLE] [address]      browse interactively
       mazama <command> [flags] [args]        browse from the command line

The address may be a gopher://, gemini://, news://, nntps:// or http(s)://
URL, or a bare host[/path], in which case the protocol is guessed.

Commands print the page as plain text with numbered links ([n]) followed by
the list of link URLs; --json prints it as JSON instead. They share a session
(current page and history), named by --session or $MAZAMA_SESSION.

Commands:
`)
	for _, c := range commands {
		fmt.Fprintf(w, "  %-22s %s\n", strings.TrimSpace(c.name+" "+c.args), c.summary)
	}
	fmt.Fprint(w, `
Common flags: --json, --session NAME, --width N, --timeout D.
Run "mazama <command> -h" for a command's flags.

Example:
  mazama open gemini://geminiprotocol.net/
  mazama follow 3
  mazama follow 7 -q "search terms"     # or: mazama follow 7; mazama input ...
  mazama back

Exit codes: 0 ok, 1 error, 2 usage, 3 news login required.
`)
}

// printPage prints a page as text (header, body, link list) or JSON.
func (e *env) printPage(p *ui.Page) error {
	if e.json {
		return e.printJSON(p)
	}
	w := e.out
	fmt.Fprintf(w, "url: %s\nkind: %s\n", p.URL, p.Kind)
	if p.Kind == ui.KindInput {
		sens := ""
		if p.Input.Sensitive {
			sens = " (sensitive)"
		}
		fmt.Fprintf(w, "input: %s%s\n%s\n", p.Input.Prompt, sens, e.inputHint(p))
		return nil
	}
	fmt.Fprintln(w)
	io.WriteString(w, p.Text)
	if e.noLinks || len(p.Links) == 0 {
		return nil
	}
	fmt.Fprintln(w, "\nlinks:")
	for _, l := range p.Links {
		fmt.Fprintln(w, linkLine(l, len(p.Links)))
	}
	return nil
}

// linkLine is "[n] URL", plus a note when following it isn't a plain open.
func linkLine(l ui.Link, total int) string {
	num := fmt.Sprintf("[%d]", l.N)
	s := fmt.Sprintf("%-*s %s", len(fmt.Sprintf("[%d]", total)), num, l.URL)
	switch l.Action {
	case ui.ActionQuery:
		s += fmt.Sprintf("  (asks %q)", l.Prompt)
	case ui.ActionLogin:
		s += "  (log in)"
	case ui.ActionExternal:
		s += "  (web)"
	case ui.ActionNone:
		s += "  (can't follow: " + l.Error + ")"
	}
	return s
}

func (e *env) printJSON(v any) error {
	enc := json.NewEncoder(e.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// inputHint says how to answer an input page from the command that showed it.
func (e *env) inputHint(p *ui.Page) string {
	if e.sess == nil {
		return fmt.Sprintf("answer with: mazama get %s -q <text>", shellQuote(p.URL))
	}
	if p.Input.Sensitive {
		return "answer with: mazama input --stdin   (text on stdin)"
	}
	return "answer with: mazama input <text>"
}

func shellQuote(s string) string {
	if strings.ContainsAny(s, " \t\n'\"\\$`!*?&;|<>()[]{}#~") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}
