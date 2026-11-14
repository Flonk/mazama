package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"
)

func TestRenderMarkdownHidesURLsAndFindsLinks(t *testing.T) {
	src := "[Quite a while ago](https://t3.at/blog/a-fractal-modulith) I've talked about it.\n\n" +
		"See also https://example.com/bare and [a link whose text is long enough to wrap across the line](gopher://example.org/0/x.md).\n"
	content, urls, spans, err := renderMarkdown(src, *styles.DefaultStyles[styles.DarkStyle], 40, false)
	if err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(content)
	t.Logf("\n%s", plain)

	if strings.Contains(plain, "t3.at") || strings.Contains(plain, "gopher://") {
		t.Error("link URLs should be hidden")
	}
	if strings.ContainsAny(plain, string(linkEnd)) {
		t.Error("marker runes leaked into the output")
	}
	if !strings.Contains(plain, "https://example.com/bare") {
		t.Error("bare URLs are their own link text and should stay visible")
	}
	want := []string{"https://t3.at/blog/a-fractal-modulith", "https://example.com/bare", "gopher://example.org/0/x.md"}
	if strings.Join(urls, " ") != strings.Join(want, " ") {
		t.Errorf("urls = %v, want %v", urls, want)
	}

	// Every span must cover exactly the link's visible text.
	lines := strings.Split(plain, "\n")
	got := map[int]string{}
	for _, s := range spans {
		got[s.id] = strings.TrimSpace(got[s.id] + " " + ansi.Cut(lines[s.line], s.start, s.end))
	}
	if got[0] != "Quite a while ago" {
		t.Errorf("link 0 text = %q", got[0])
	}
	if got[1] != "https://example.com/bare" {
		t.Errorf("link 1 text = %q", got[1])
	}
	if got[2] != "a link whose text is long enough to wrap across the line" {
		t.Errorf("link 2 text = %q", got[2])
	}
	lineCount := map[int]int{}
	for _, s := range spans {
		lineCount[s.id]++
	}
	if lineCount[2] < 2 {
		t.Errorf("link 2 should wrap onto several lines at width 40, got %d span(s)", lineCount[2])
	}
}
