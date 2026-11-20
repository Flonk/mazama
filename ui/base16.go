package ui

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"text/template"

	gansi "github.com/charmbracelet/glamour/ansi"
)

// base16Template is a glamour style with {{.base00}}…{{.base0F}} slots. Roles
// follow the base16 styleguide and are laid out so that the base16 One Dark
// scheme looks like the bundled one-dark style.
//
//go:embed base16.json.tmpl
var base16Template string

var base16Tmpl = template.Must(template.New("base16").Option("missingkey=error").Parse(base16Template))

var hexColor = regexp.MustCompile(`^#?[0-9a-fA-F]{6}$`)

// base16Keys are the sixteen slots of a base16 scheme.
var base16Keys = func() []string {
	keys := make([]string, 16)
	for i := range keys {
		keys[i] = fmt.Sprintf("base%02X", i)
	}
	return keys
}()

// base16Palette extracts a base16 palette from JSON, accepting both a flat
// object ({"base00": "282c34", …}, as Stylix and base16 builders emit) and a
// tinted-theming scheme with the colours under "palette". It returns ok=false
// if b doesn't look like a base16 scheme at all.
func base16Palette(b []byte) (map[string]string, bool, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, false, nil
	}
	if p, ok := doc["palette"]; ok {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(p, &inner); err == nil {
			doc = inner
		}
	}
	if _, ok := doc["base00"]; !ok {
		return nil, false, nil
	}
	pal := make(map[string]string, len(base16Keys))
	for _, k := range base16Keys {
		raw, ok := doc[k]
		if !ok {
			return nil, true, fmt.Errorf("base16 scheme is missing %s", k)
		}
		var c string
		if err := json.Unmarshal(raw, &c); err != nil || !hexColor.MatchString(c) {
			return nil, true, fmt.Errorf("base16 %s = %s, want a hex colour", k, raw)
		}
		pal[k] = "#" + strings.ToLower(strings.TrimPrefix(c, "#"))
	}
	return pal, true, nil
}

// base16Style renders the base16 template with pal.
func base16Style(pal map[string]string) (gansi.StyleConfig, error) {
	var buf bytes.Buffer
	if err := base16Tmpl.Execute(&buf, pal); err != nil {
		return gansi.StyleConfig{}, err
	}
	var cfg gansi.StyleConfig
	if err := json.Unmarshal(buf.Bytes(), &cfg); err != nil {
		return gansi.StyleConfig{}, fmt.Errorf("base16 template: %w", err)
	}
	return cfg, nil
}
