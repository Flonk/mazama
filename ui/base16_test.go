package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// base16 One Dark, as Stylix's config.lib.stylix.colors.withHashtag emits it.
const oneDarkBase16 = `{
  "base00": "#282c34", "base01": "#353b45", "base02": "#3e4451", "base03": "#545862",
  "base04": "#565c64", "base05": "#abb2bf", "base06": "#b6bdca", "base07": "#c8ccd4",
  "base08": "#e06c75", "base09": "#d19a66", "base0A": "#e5c07b", "base0B": "#98c379",
  "base0C": "#56b6c2", "base0D": "#61afef", "base0E": "#c678dd", "base0F": "#be5046",
  "scheme": "One Dark", "author": "x"
}`

func writeStyle(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "style.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBase16Style(t *testing.T) {
	// Flat with '#', tinted-theming "palette" nesting, and bare hex all work.
	nested := `{"system": "base16", "name": "One Dark", "palette": ` +
		strings.ReplaceAll(oneDarkBase16, `"#`, `"`) + `}`
	for name, body := range map[string]string{"flat": oneDarkBase16, "palette": nested} {
		cfg, err := LoadStyle(writeStyle(t, body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		th := NewTheme(cfg)
		want := map[string]struct {
			got  lipgloss.TerminalColor
			want lipgloss.Color
		}{
			"DIR":    {th.Menu.GetForeground(), "#e06c75"},
			"TXT":    {th.Text.GetForeground(), "#61afef"},
			"URL":    {th.Other.GetForeground(), "#56b6c2"},
			"text":   {th.Info.GetForeground(), "#abb2bf"},
			"tag":    {th.Tag.GetForeground(), "#545862"},
			"header": {th.Header.GetBackground(), "#61afef"},
			"cursor": {th.CursorBg, "#353b45"},
		}
		for role, c := range want {
			if c.got != c.want {
				t.Errorf("%s: %s = %v, want %v", name, role, c.got, c.want)
			}
		}
		if f := cfg.ImageText.Format; f != "Image: {{.text}} →" {
			t.Errorf("%s: image_text format = %q", name, f)
		}
	}
}

func TestBase16Invalid(t *testing.T) {
	missing := strings.Replace(oneDarkBase16, `"base0F": "#be5046",`, ``, 1)
	if _, err := LoadStyle(writeStyle(t, missing)); err == nil || !strings.Contains(err.Error(), "base0F") {
		t.Errorf("missing slot: err = %v", err)
	}
	bad := strings.Replace(oneDarkBase16, `"#be5046"`, `"red"`, 1)
	if _, err := LoadStyle(writeStyle(t, bad)); err == nil {
		t.Error("non-hex colour accepted")
	}
}
