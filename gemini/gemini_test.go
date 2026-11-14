package gemini_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Flonk/mazama/gemini"
	"github.com/Flonk/mazama/gemini/geminitest"
)

func newClient(t *testing.T) (*gemini.Client, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "known_hosts")
	tofu, err := gemini.LoadTOFU(path)
	if err != nil {
		t.Fatal(err)
	}
	return &gemini.Client{TOFU: tofu}, path
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestGetFollowsRedirectsAndReadsBody(t *testing.T) {
	srv := geminitest.New(t, func(u *url.URL) (string, string) {
		switch u.Path {
		case "/old":
			return "31 /new", ""
		case "/new":
			return "20 text/gemini; charset=utf-8", "# Hello\n"
		}
		return "51 not found", ""
	})
	c, _ := newClient(t)
	resp, err := c.Get(context.Background(), mustURL(t, srv.URL("/old")))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 20 || string(resp.Body) != "# Hello\n" || resp.URL.Path != "/new" {
		t.Errorf("got status=%d url=%s body=%q", resp.Status, resp.URL, resp.Body)
	}
	if !strings.HasPrefix(resp.Meta, "text/gemini") {
		t.Errorf("meta = %q", resp.Meta)
	}
}

func TestInputAndFailureStatuses(t *testing.T) {
	srv := geminitest.New(t, func(u *url.URL) (string, string) {
		if u.Path == "/search" && u.RawQuery == "" {
			return "10 Search for what?", ""
		}
		if u.Path == "/search" {
			q, _ := url.PathUnescape(u.RawQuery)
			return "20 text/gemini", "results for " + q
		}
		return "51 Not found", ""
	})
	c, _ := newClient(t)
	resp, err := c.Get(context.Background(), mustURL(t, srv.URL("/search")))
	if err != nil || resp.Class() != gemini.StatusInput || resp.Meta != "Search for what?" {
		t.Fatalf("input: %+v %v", resp, err)
	}
	u := mustURL(t, srv.URL("/search"))
	u.RawQuery = gemini.QueryEscape("hello world")
	resp, err = c.Get(context.Background(), u)
	if err != nil || string(resp.Body) != "results for hello world" {
		t.Fatalf("query: %+v %v", resp, err)
	}
	resp, err = c.Get(context.Background(), mustURL(t, srv.URL("/nope")))
	if err != nil || resp.Status != 51 {
		t.Fatalf("not found: %+v %v", resp, err)
	}
}

func TestTOFUPinsAndRejectsChangedCert(t *testing.T) {
	srv := geminitest.New(t, func(*url.URL) (string, string) { return "20 text/plain", "ok" })
	c, path := newClient(t)
	ctx := context.Background()

	if _, err := c.Get(ctx, mustURL(t, srv.URL("/"))); err != nil {
		t.Fatalf("first visit should be trusted: %v", err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), srv.Addr+" sha256:") {
		t.Errorf("known_hosts not written:\n%s", b)
	}

	// Reloading the store from disk keeps the pin.
	reloaded, err := gemini.LoadTOFU(path)
	if err != nil {
		t.Fatal(err)
	}
	c2 := &gemini.Client{TOFU: reloaded}
	if _, err := c2.Get(ctx, mustURL(t, srv.URL("/"))); err != nil {
		t.Fatalf("same cert after reload: %v", err)
	}

	srv.RotateCert(t)
	_, err = c2.Get(ctx, mustURL(t, srv.URL("/")))
	if !errors.Is(err, gemini.ErrCertChanged) {
		t.Fatalf("expected ErrCertChanged, got %v", err)
	}
}

func TestToMarkdown(t *testing.T) {
	src := "# Title\n" +
		"Some text with *stars* and [brackets].\n" +
		"=> gemini://example.org/a.gmi A link\n" +
		"=> /relative\n" +
		"\n" +
		"* one\n" +
		"* two\n" +
		"> quoted\n" +
		"```alt\n" +
		"  ascii *art*\n" +
		"```\n"
	got := gemini.ToMarkdown(src)
	for _, want := range []string{
		"# Title",
		`Some text with \*stars\* and \[brackets\]\.\` + "\n" + "→ [A link](<gemini://example.org/a.gmi>)",
		"→ [/relative](</relative>)",
		"- one\n- two",
		"> quoted",
		"```\n  ascii *art*\n```",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
