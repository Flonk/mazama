package ui

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type openedMsg struct {
	url string
	err error
}

// externalSchemes are the URL: link schemes handed to the OS. Menus come from
// arbitrary servers, so file:, custom app handlers and the like are refused.
var externalSchemes = map[string]bool{"http": true, "https": true}

// openExternal opens u with the OS default handler (xdg-open, open, or the
// Windows URL protocol handler). It returns once the handler has started.
func openExternal(u string) tea.Cmd {
	return func() tea.Msg {
		parsed, err := url.Parse(u)
		if err != nil {
			return openedMsg{url: u, err: err}
		}
		if !externalSchemes[strings.ToLower(parsed.Scheme)] {
			return openedMsg{url: u, err: fmt.Errorf("not opening %q links: %s", parsed.Scheme, u)}
		}

		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", u)
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
		default:
			cmd = exec.Command("xdg-open", u)
		}
		// Stdin/stdout/stderr stay nil (the null device) so the handler
		// can't scribble over the TUI.
		if err := cmd.Start(); err != nil {
			return openedMsg{url: u, err: err}
		}
		go cmd.Wait() // reap the child
		return openedMsg{url: u}
	}
}
