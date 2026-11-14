package ui

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"github.com/yuin/goldmark"
	emojiast "github.com/yuin/goldmark-emoji/ast"
	"github.com/yuin/goldmark-emoji/definition"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Links are rendered as their text only, wrapped in private-use marker runes
// so their on-screen position can be recovered after glamour has styled and
// word-wrapped the document. The markers are stripped before display.
const (
	linkEnd       rune = ''
	linkStartBase rune = 0xF0000 // + link id (Supplementary Private Use Area-A)
	maxLinks           = 0xFFFD
)

// linkSpan is one on-screen piece of a link. A link that wraps across lines
// has one span per line.
type linkSpan struct {
	id    int // index into the page's link URLs
	line  int // content line
	start int // first column
	end   int // column after the last character
}

// renderMarkdown renders src like glamour's TermRenderer does, but with link
// URLs hidden. keepLines preserves line breaks inside paragraphs (needed for
// gemtext, which is line-oriented). It returns the display content, the link
// targets indexed by id, and where each link landed.
func renderMarkdown(src string, cfg gansi.StyleConfig, width int, keepLines bool) (string, []string, []linkSpan, error) {
	var urls []string
	// Same goldmark setup as glamour.NewTermRenderer, plus our transformer.
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
			parser.WithASTTransformers(util.Prioritized(linkTransformer{urls: &urls}, 1000)),
		),
		goldmark.WithRenderer(renderer.NewRenderer(renderer.WithNodeRenderers(
			util.Prioritized(gansi.NewRenderer(gansi.Options{
				WordWrap:         width,
				PreserveNewLines: keepLines,
				ColorProfile:     termenv.TrueColor,
				Styles:           cfg,
			}), 1000),
		))),
	)
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "", nil, nil, err
	}
	out := strings.ReplaceAll(buf.String(), "\t", "    ")
	content, spans := extractLinks(out)
	return content, urls, spans, nil
}

// linkTransformer records each link's target, points the link at an anchor
// (which glamour renders as text only, no URL), and brackets its text with
// marker runes. Bare URLs become links whose text is the URL itself.
type linkTransformer struct {
	urls *[]string
}

func (t linkTransformer) Transform(doc *gast.Document, reader text.Reader, _ parser.Context) {
	src := reader.Source()
	var links []gast.Node
	gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		switch n.(type) {
		case *gast.Link, *gast.AutoLink:
			links = append(links, n)
			return gast.WalkSkipChildren, nil
		}
		return gast.WalkContinue, nil
	})

	for _, n := range links {
		if len(*t.urls) >= maxLinks {
			return
		}
		id := len(*t.urls)
		anchor := []byte(fmt.Sprintf("#mazama-link-%d", id))
		switch l := n.(type) {
		case *gast.Link:
			*t.urls = append(*t.urls, string(l.Destination))
			l.Destination = anchor
			if first := l.FirstChild(); first != nil {
				l.InsertBefore(l, first, marker(string(linkStartBase+rune(id))))
			} else {
				l.AppendChild(l, marker(string(linkStartBase+rune(id))))
			}
			l.AppendChild(l, marker(string(linkEnd)))
		case *gast.AutoLink:
			if l.AutoLinkType != gast.AutoLinkURL {
				continue // leave email autolinks alone
			}
			*t.urls = append(*t.urls, string(l.URL(src)))
			nl := gast.NewLink()
			nl.Destination = anchor
			nl.AppendChild(nl, marker(string(linkStartBase+rune(id))+string(l.Label(src))+string(linkEnd)))
			l.Parent().ReplaceChild(l.Parent(), l, nl)
		}
	}
}

// marker makes an inline node that glamour prints verbatim. Emoji nodes are
// the only inline kind glamour renders from a plain string rather than from
// a slice of the source.
func marker(s string) gast.Node {
	return emojiast.NewEmoji(nil, &definition.Emoji{Unicode: []rune(s)})
}

// extractLinks strips the marker runes from rendered output and returns the
// cleaned content plus the span of every link.
func extractLinks(s string) (string, []linkSpan) {
	lines := strings.Split(s, "\n")
	var spans []linkSpan
	open, openStart := -1, -1

	for li, line := range lines {
		var b strings.Builder
		col, lastInk := 0, 0
		if open >= 0 {
			openStart = -1 // a wrapped link resumes at the next non-space
		}
		for i := 0; i < len(line); {
			if line[i] == 0x1b {
				j := escapeEnd(line, i)
				b.WriteString(line[i:j])
				i = j
				continue
			}
			r, size := utf8.DecodeRuneInString(line[i:])
			i += size
			switch {
			case r >= linkStartBase && r < linkStartBase+maxLinks:
				open, openStart = int(r-linkStartBase), col
				continue
			case r == linkEnd:
				if open >= 0 && openStart >= 0 && col > openStart {
					spans = append(spans, linkSpan{id: open, line: li, start: openStart, end: col})
				}
				open, openStart = -1, -1
				continue
			}
			if open >= 0 && openStart < 0 && r != ' ' {
				openStart = col
			}
			b.WriteRune(r)
			col += runewidth.RuneWidth(r)
			if r != ' ' {
				lastInk = col
			}
		}
		if open >= 0 && openStart >= 0 && lastInk > openStart {
			spans = append(spans, linkSpan{id: open, line: li, start: openStart, end: lastInk})
		}
		lines[li] = b.String()
	}
	return strings.Join(lines, "\n"), spans
}

// escapeEnd returns the index just past the escape sequence starting at i.
func escapeEnd(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[': // CSI: parameters, then a final byte in 0x40–0x7E
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
	case ']': // OSC: terminated by BEL or ESC \
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
	default:
		return i + 2
	}
	return len(s)
}
