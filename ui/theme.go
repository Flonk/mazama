package ui

import (
	"embed"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	gansi "github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/lucasb-eyer/go-colorful"
)

// Theme holds the lipgloss styles for everything that isn't rendered by
// glamour. It is derived from the glamour style so menus and markdown pages
// share one palette.
type Theme struct {
	Header lipgloss.Style
	Footer lipgloss.Style
	Error  lipgloss.Style

	Tag   lipgloss.Style // the DIR/TXT/URL column
	Info  lipgloss.Style // i lines
	Menu  lipgloss.Style // 1 items
	Text  lipgloss.Style // 0 items
	Other lipgloss.Style // URL: links and search items
	Dead  lipgloss.Style // types we can't open

	// CursorBg is the focused-line background. Nil means the theme has no
	// usable colour (ascii, notty), and focus falls back to reverse video.
	CursorBg lipgloss.TerminalColor
}

// DefaultStyle is the style used when -style isn't given.
const DefaultStyle = "one-dark"

// bundledStyles are styles shipped with mazama on top of glamour's built-ins,
// keyed by name; the JSON lives in themes/<name>.json.
//
//go:embed themes/*.json
var bundledStyles embed.FS

// StyleNames lists every style name LoadStyle accepts, sorted.
func StyleNames() []string {
	names := slices.Collect(maps.Keys(styles.DefaultStyles))
	entries, _ := bundledStyles.ReadDir("themes")
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	slices.Sort(names)
	return names
}

// LoadStyle resolves a bundled style name, a glamour built-in style name, or
// a path to a glamour JSON style file, in that order.
func LoadStyle(nameOrPath string) (gansi.StyleConfig, error) {
	if b, err := bundledStyles.ReadFile("themes/" + nameOrPath + ".json"); err == nil {
		return parseStyle(nameOrPath, b)
	}
	if s, ok := styles.DefaultStyles[nameOrPath]; ok {
		return *s, nil
	}
	b, err := os.ReadFile(nameOrPath)
	if err != nil {
		return gansi.StyleConfig{}, err
	}
	return parseStyle(nameOrPath, b)
}

func parseStyle(name string, b []byte) (gansi.StyleConfig, error) {
	var cfg gansi.StyleConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return gansi.StyleConfig{}, fmt.Errorf("parse %s: %w", name, err)
	}
	return cfg, nil
}

// NewTheme maps glamour style roles onto gopher menu roles:
//
//	DIR          ← heading colour (what a "section" looks like)
//	TXT          ← link text colour
//	URL / ASK    ← link (URL) colour
//	tags, footer ← horizontal rule / code comment colour
//	errors       ← code "error" token colour
//	header bar   ← H1 if it has a background, else heading colour as background
//	focus        ← inline code background, else muted colour blended into the
//	               code block background
func NewTheme(cfg gansi.StyleConfig) Theme {
	var chroma gansi.Chroma
	if cfg.CodeBlock.Chroma != nil {
		chroma = *cfg.CodeBlock.Chroma
	}
	muted := first(cfg.HorizontalRule.Color, chroma.Comment.Color)
	heading := first(cfg.Heading.Color, cfg.H2.Color, cfg.Link.Color)
	themeBg := first(chroma.Background.BackgroundColor, cfg.Document.BackgroundColor)

	t := Theme{
		Info:   fg(first(cfg.Document.Color, cfg.Text.Color)),
		Menu:   fg(heading).Bold(true),
		Text:   fg(first(cfg.LinkText.Color, cfg.Link.Color)),
		Other:  fg(first(cfg.Link.Color, cfg.Strong.Color)),
		Dead:   fg(muted),
		Tag:    fg(muted).Width(5),
		Footer: fg(muted),
		Error:  fg(first(chroma.Error.BackgroundColor, chroma.Error.Color)),
	}
	if muted == "" {
		t.Dead = t.Dead.Faint(true)
		t.Tag = t.Tag.Faint(true)
		t.Footer = t.Footer.Faint(true)
	}

	t.Header = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	switch {
	case cfg.H1.BackgroundColor != nil:
		t.Header = t.Header.Background(lipgloss.Color(*cfg.H1.BackgroundColor))
		if cfg.H1.Color != nil {
			t.Header = t.Header.Foreground(lipgloss.Color(*cfg.H1.Color))
		}
	case heading != "":
		t.Header = t.Header.Background(lipgloss.Color(heading))
		if themeBg != "" {
			t.Header = t.Header.Foreground(lipgloss.Color(themeBg))
		}
	default:
		t.Header = t.Header.Reverse(true)
	}

	if c := first(cfg.Code.BackgroundColor); c != "" {
		t.CursorBg = lipgloss.Color(c)
	} else if c, ok := blend(themeBg, muted, 0.4); ok {
		t.CursorBg = lipgloss.Color(c)
	}
	return t
}

func first(vals ...*string) string {
	for _, v := range vals {
		if v != nil && *v != "" {
			return *v
		}
	}
	return ""
}

func fg(c string) lipgloss.Style {
	s := lipgloss.NewStyle()
	if c != "" {
		s = s.Foreground(lipgloss.Color(c))
	}
	return s
}

// blend mixes two hex colours; ANSI palette numbers can't be blended.
func blend(a, b string, t float64) (string, bool) {
	ca, errA := colorful.Hex(a)
	cb, errB := colorful.Hex(b)
	if errA != nil || errB != nil {
		return "", false
	}
	return ca.BlendLab(cb, t).Clamped().Hex(), true
}
