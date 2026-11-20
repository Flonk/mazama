package ui

import (
	"net/mail"
	"regexp"
	"strings"

	gansi "github.com/charmbracelet/glamour/ansi"

	"github.com/Flonk/mazama/nntp"
)

// Articles are shown as line-oriented markdown (rendered with keepLines):
// a header with the subject, author, date, groups and thread links, then
// the body with ">" quoting turned into nested blockquotes. All article
// text is escaped, so only the structure added here is markdown.

// articleMarkdown lays out an article from server.
func articleMarkdown(art *nntp.Article, server nntp.Address) string {
	h := art.Header
	var b strings.Builder

	subject := sanitize(nntp.DecodeHeader(h.Get("Subject")))
	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}
	b.WriteString("# " + mdEscape(subject) + "\n\n")

	var meta []string
	if from := h.Get("From"); from != "" {
		meta = append(meta, fromMarkdown(from))
	}
	if d := h.Get("Date"); d != "" {
		if t, err := mail.ParseDate(d); err == nil {
			meta = append(meta, mdEscape(t.Local().Format("Mon 2 Jan 2006 15:04")))
		} else {
			meta = append(meta, mdEscape(sanitize(d)))
		}
	}
	var head []string
	if len(meta) > 0 {
		head = append(head, strings.Join(meta, " · "))
	}
	if groups := splitList(h.Get("Newsgroups")); len(groups) > 0 {
		links := make([]string, len(groups))
		for i, g := range groups {
			a := server
			a.Group = g
			links[i] = mdLink(g, a.String())
		}
		head = append(head, "in "+strings.Join(links, ", "))
	}
	if refs := messageIDs(h.Get("References")); len(refs) > 0 {
		parent := server
		parent.MessageID = refs[len(refs)-1]
		line := "↩ " + mdLink("in reply to", parent.String())
		if len(refs) > 1 {
			root := server
			root.MessageID = refs[0]
			line += " · " + mdLink("start of thread", root.String())
		}
		head = append(head, line)
	}
	b.WriteString(strings.Join(head, "\\\n"))
	b.WriteString("\n\n---\n\n")
	if art.ContentType == "text/markdown" {
		// Posted as markdown (RFC 7763): use it as it is.
		b.WriteString(sanitize(art.Body))
	} else {
		b.WriteString(bodyMarkdown(sanitize(art.Body)))
	}
	return b.String()
}

func fromMarkdown(from string) string {
	// The name only: addresses are long, and on gateways like gmane
	// obfuscated. DisplayName falls back to the address when there's no name.
	return "**" + mdEscape(sanitize(nntp.DisplayName(from))) + "**"
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
		if !nntp.IsWildmat(f) {
			out = append(out, f)
		}
	}
	return out
}

var messageIDRe = regexp.MustCompile(`<([^<>\s]+@[^<>\s]+)>`)

func messageIDs(s string) []string {
	var ids []string
	for _, m := range messageIDRe.FindAllStringSubmatch(s, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// quoteDepth counts leading quote markers (">", "> >", ">>", " > ") and
// returns the depth and the text after them.
func quoteDepth(line string) (int, string) {
	depth, rest := 0, line
	for {
		t := strings.TrimLeft(rest, " ")
		if !strings.HasPrefix(t, ">") {
			break
		}
		depth++
		rest = t[1:]
	}
	if depth == 0 {
		return 0, line
	}
	return depth, strings.TrimPrefix(rest, " ")
}

// attributionRe matches the line that introduces a quote: "Ann wrote:",
// "On Mon, Bob <b@x> writes:", "Carl schrieb:".
var attributionRe = regexp.MustCompile(`(?i)(wrote|writes|said|schrieb|a écrit|escribió|scrisse)\s*:\s*$`)

// bodyMarkdown converts a plain-text body. Each source line stays a line;
// quote levels become nested blockquotes; the signature goes below a rule.
func bodyMarkdown(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")

	// The signature starts at the last "-- " line, if it's near the end.
	sig := -1
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-15; i-- {
		if lines[i] == "-- " || lines[i] == "--" {
			sig = i
			break
		}
	}

	type qline struct {
		depth int
		text  string
	}
	var src []qline
	end := len(lines)
	if sig >= 0 {
		end = sig
	}
	for _, l := range lines[:end] {
		d, t := quoteDepth(l)
		src = append(src, qline{d, strings.TrimRight(t, " \t")})
	}

	var out []string
	lastDepth, inPara := 0, false
	// separate ends the current paragraph, keeping the quote levels both
	// sides share.
	separate := func(depth int) {
		out = append(out, strings.TrimSpace(strings.Repeat("> ", depth)))
		inPara = false
	}
	for i, l := range src {
		if l.text == "" {
			if inPara {
				separate(l.depth)
			}
			lastDepth = l.depth
			continue
		}
		if l.depth != lastDepth && len(out) > 0 {
			separate(min(l.depth, lastDepth))
		}
		text := lineMarkdown(l.text)
		if attributionRe.MatchString(l.text) {
			for j := i + 1; j < len(src); j++ {
				if src[j].text != "" {
					if src[j].depth > l.depth {
						text = "*" + text + "*"
					}
					break
				}
			}
		}
		if inPara {
			out[len(out)-1] += "\\" // hard break: keep the source line break
		}
		out = append(out, strings.Repeat("> ", l.depth)+text)
		lastDepth, inPara = l.depth, true
	}

	if sig >= 0 && sig+1 < len(lines) {
		var sigLines []string
		for _, l := range lines[sig+1:] {
			if t := strings.TrimRight(l, " \t"); t != "" {
				sigLines = append(sigLines, lineMarkdown(t))
			}
		}
		if len(sigLines) > 0 {
			out = append(out, "", "---", "", strings.Join(sigLines, "\\\n"))
		}
	}
	return strings.Join(out, "\n") + "\n"
}

var urlRe = regexp.MustCompile(`(?i)\b(?:https?|gemini|gopher|news|nntps?|ftp)://[^\s<>"]+`)

// lineMarkdown escapes one line of text, keeping its indentation (as
// no-break spaces, which markdown doesn't strip) and turning URLs into
// links.
func lineMarkdown(s string) string {
	indent := 0
	i := 0
	for ; i < len(s) && (s[i] == ' ' || s[i] == '\t'); i++ {
		if s[i] == '\t' {
			indent += 8 - indent%8
		} else {
			indent++
		}
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", indent))
	rest := s[i:]
	// A line of "=" would make the line above a heading. (Glamour shows
	// "\=" literally, so escaping doesn't help; a no-break space does.)
	if indent == 0 && strings.Trim(rest, "= ") == "" {
		b.WriteString(" ")
	}
	for {
		loc := urlRe.FindStringIndex(rest)
		if loc == nil {
			break
		}
		u := trimURL(rest[loc[0]:loc[1]])
		b.WriteString(mdEscape(rest[:loc[0]]))
		b.WriteString(mdLink(u, u))
		rest = rest[loc[0]+len(u):]
	}
	b.WriteString(mdEscape(rest))
	return b.String()
}

// trimURL drops trailing punctuation that belongs to the sentence, and a
// closing parenthesis the URL didn't open.
func trimURL(u string) string {
	for {
		t := strings.TrimRight(u, ".,;:!?'\"")
		if strings.HasSuffix(t, ")") && strings.Count(t, "(") < strings.Count(t, ")") {
			t = t[:len(t)-1]
		}
		if t == u {
			return u
		}
		u = t
	}
}

func mdLink(label, target string) string {
	return "[" + mdEscape(label) + "](<" + strings.NewReplacer("<", "%3C", ">", "%3E", " ", "%20").Replace(target) + ">)"
}

// mdEscape backslash-escapes the ASCII punctuation markdown could act on.
func mdEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_{}[]()#+-.!|<>~", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// quoteBars gives blockquotes a "│ " bar when the style has none, so nested
// quote levels in articles are easy to tell apart.
func quoteBars(cfg gansi.StyleConfig) gansi.StyleConfig {
	if cfg.BlockQuote.IndentToken == nil {
		bar, one := "│ ", uint(1)
		cfg.BlockQuote.IndentToken, cfg.BlockQuote.Indent = &bar, &one
	}
	return cfg
}
