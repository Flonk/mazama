// mazama is a terminal gopher and gemini client.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/Flonk/mazama/gemini"
	"github.com/Flonk/mazama/ui"
)

const defaultURL = "gopher://gopher.floodgap.com/"

func main() {
	style := flag.String("style", ui.DefaultStyle,
		"markdown style: auto, one of "+strings.Join(ui.StyleNames(), ", ")+", or a path to a glamour JSON style")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: mazama [flags] [address]\n\nThe address may be a gopher://, gemini:// or http(s):// URL, or a bare\nhost[/path], in which case the protocol is guessed.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

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

	// Mouse capture is toggled per page by the model.
	p := tea.NewProgram(ui.NewFromInput(raw, cfg, &gemini.Client{TOFU: tofu}), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "mazama:", err)
		os.Exit(1)
	}
}
