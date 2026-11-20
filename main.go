// mazama is a terminal gopher, gemini and usenet client.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/Flonk/mazama/cli"
	"github.com/Flonk/mazama/gemini"
	"github.com/Flonk/mazama/ui"
)

const defaultURL = "gopher://gopher.floodgap.com/"

func main() {
	// mazama <command> ...: the command-line interface (see cli).
	if len(os.Args) > 1 && cli.IsCommand(os.Args[1]) {
		os.Exit(cli.Run(os.Args[1:], cli.Options{Gemini: geminiClient()}))
	}

	style := flag.String("style", ui.DefaultStyle,
		"markdown style: auto, one of "+strings.Join(ui.StyleNames(), ", ")+
			", or a path to a glamour JSON style or base16 scheme;\n"+
			"defaults to $MAZAMA_STYLE, then $XDG_CONFIG_HOME/mazama/style.json if it exists")
	flag.Usage = func() {
		cli.Usage(os.Stderr)
		fmt.Fprintln(os.Stderr, "\nInteractive flags:")
		flag.PrintDefaults()
	}
	flag.Parse()
	if !flagSet("style") {
		*style = defaultStyle()
	}

	raw := defaultURL
	if flag.NArg() > 0 {
		raw = flag.Arg(0)
	}
	if *style == "auto" {
		// Detect the background before bubbletea takes over the terminal;
		// glamour's own auto style would query it mid-session.
		*style = styles.LightStyle
		if lipgloss.HasDarkBackground() {
			*style = styles.DarkStyle
		}
	}
	cfg, err := ui.LoadStyle(*style)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mazama: invalid -style %q: %v\n", *style, err)
		os.Exit(2)
	}

	// Mouse capture is toggled per page by the model.
	p := tea.NewProgram(ui.NewFromInput(raw, cfg, geminiClient()), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "mazama:", err)
		os.Exit(1)
	}
}

func flagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// defaultStyle picks the style when -style isn't given: $MAZAMA_STYLE, then
// the user's style file (written by e.g. the Home Manager / Stylix module),
// then the built-in default.
func defaultStyle() string {
	if s := os.Getenv("MAZAMA_STYLE"); s != "" {
		return s
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".config")
		}
	}
	if dir != "" {
		p := filepath.Join(dir, "mazama", "style.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ui.DefaultStyle
}

// geminiClient returns a gemini client with the trust-on-first-use
// certificate store, or exits.
func geminiClient() *gemini.Client {
	tofuPath, err := gemini.DefaultTOFUPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mazama:", err)
		os.Exit(1)
	}
	tofu, err := gemini.LoadTOFU(tofuPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mazama: reading %s: %v\n", tofuPath, err)
		os.Exit(1)
	}
	return &gemini.Client{TOFU: tofu}
}
