package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/x/ansi"

	"github.com/Flonk/mazama/nntp"
)

func TestArticleMarkdown(t *testing.T) {
	raw := "From: Bob <bob@example.com>\n" +
		"Subject: Re: *pointers* in C\n" +
		"Newsgroups: comp.lang.c,alt.test\n" +
		"References: <root@x> <parent@x>\n" +
		"Date: Tue, 03 Jan 2006 15:04:05 +0000\n\n" +
		"Ann <ann@x> wrote:\n" +
		"> Carl wrote:\n" +
		">> int *p = &x;\n" +
		">\n" +
		"> what does *p mean?\n" +
		"\n" +
		"It's the value. See https://c-faq.com/ptrs.html.\n" +
		"    for (;;) { }\n" +
		"1. not a list\n" +
		"===\n" +
		"-- \n" +
		"Bob\n"
	art, err := nntp.ParseArticle([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	md := articleMarkdown(art, nntp.Address{Host: "news.example", Port: "119"})

	for _, want := range []string{
		"# Re: \\*pointers\\* in C",
		"[comp\\.lang\\.c](<news://news.example/comp.lang.c>), [alt\\.test](<news://news.example/alt.test>)",
		"[in reply to](<news://news.example/parent@x>) · [start of thread](<news://news.example/root@x>)",
		"*Ann \\<ann@x\\> wrote:*",
		"> *Carl wrote:*",
		"> > int \\*p = &x;",
		"[https://c\\-faq\\.com/ptrs\\.html](<https://c-faq.com/ptrs.html>)\\.",
		"\u00a0\u00a0\u00a0\u00a0for",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}

	content, links, _, err := renderMarkdown(md, quoteBars(*styles.DefaultStyles[styles.DraculaStyle]), 80, true)
	if err != nil {
		t.Fatal(err)
	}
	v := ansi.Strip(content)
	for _, want := range []string{
		"│ │ int *p = &x;", // nested quote, text literal
		"│ what does *p mean?",
		"1. not a list", // not turned into a list
		"    for (;;)",  // indentation kept
		"Bob",
		"===", // shown, not a heading underline
	} {
		if !strings.Contains(strings.ReplaceAll(v, "\u00a0", " "), want) {
			t.Errorf("rendered missing %q:\n%s", want, v)
		}
	}
	// Lines stay lines.
	if strings.Contains(v, "value. See") && strings.Contains(v, "See https://c-faq.com/ptrs.html. for") {
		t.Errorf("lines merged:\n%s", v)
	}
	wantLinks := []string{"news://news.example/comp.lang.c", "news://news.example/alt.test",
		"news://news.example/parent@x", "news://news.example/root@x", "https://c-faq.com/ptrs.html"}
	if strings.Join(links, " ") != strings.Join(wantLinks, " ") {
		t.Errorf("links = %q", links)
	}
}

func TestQuoteDepth(t *testing.T) {
	for in, want := range map[string]int{"> a": 1, ">> a": 2, "> > a": 2, " > a": 1, "a > b": 0, ">": 1} {
		if d, _ := quoteDepth(in); d != want {
			t.Errorf("quoteDepth(%q) = %d, want %d", in, d, want)
		}
	}
}

func TestMarkdownArticle(t *testing.T) {
	raw := "From: Ann <ann@x>\nSubject: notes\nContent-Type: text/markdown\n\n" +
		"Some **bold** text and a [link](https://example.com).\n\n- one\n- two\n\n```\ncode  here\n```\n"
	art, err := nntp.ParseArticle([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	md := articleMarkdown(art, nntp.Address{Host: "h", Port: "119"})
	content, links, _, err := renderMarkdown(md, quoteBars(*styles.DefaultStyles[styles.DraculaStyle]), 80, true)
	if err != nil {
		t.Fatal(err)
	}
	v := ansi.Strip(content)
	for _, want := range []string{"Some bold text and a link.", "• one", "code  here"} {
		if !strings.Contains(v, want) {
			t.Errorf("rendered missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "**") {
		t.Errorf("markdown shown literally:\n%s", v)
	}
	if len(links) != 1 || links[0] != "https://example.com" {
		t.Errorf("links = %q", links)
	}
}
