package ui

import (
	"context"
	"fmt"
	"mime"
	"net/url"
	"strings"

	"github.com/Flonk/mazama/gemini"
	"github.com/Flonk/mazama/gopher"
)

// Location is where a page came from: a gopher address, a gemini URL (when
// Gemini is non-nil), or a web URL the user typed (when Web is non-nil; shown
// as an "outgoing link" page, never fetched).
type Location struct {
	Gopher gopher.Address
	Gemini *url.URL
	Web    *url.URL
}

// ParseLocation parses a gemini:// or gopher:// URL. Anything without a
// scheme is treated as gopher, as before.
func ParseLocation(raw string) (Location, error) {
	if strings.HasPrefix(strings.ToLower(raw), "gemini://") {
		u, err := url.Parse(raw)
		if err != nil {
			return Location{}, err
		}
		if u.Hostname() == "" {
			return Location{}, fmt.Errorf("missing host in %q", raw)
		}
		return Location{Gemini: u}, nil
	}
	a, err := gopher.ParseURL(raw)
	if err != nil {
		return Location{}, err
	}
	return Location{Gopher: a}, nil
}

func (l Location) String() string {
	if l.Gemini != nil {
		return l.Gemini.String()
	}
	if l.Web != nil {
		return l.Web.String()
	}
	return l.Gopher.String()
}

// withQuery returns the location with user input attached: a gopher search
// query, or a gemini query string.
func (l Location) withQuery(q string) Location {
	if l.Gemini != nil {
		u := *l.Gemini
		u.RawQuery = gemini.QueryEscape(q)
		return Location{Gemini: &u}
	}
	l.Gopher.Query = q
	return l
}

// inputRequest is a gemini 1x response asking the user for input.
type inputRequest struct {
	prompt    string
	sensitive bool
}

// fetchGemini performs a gemini request and turns the response into a
// fetchedMsg: a page, an input request, or an error.
func fetchGemini(ctx context.Context, c *gemini.Client, u *url.URL) fetchedMsg {
	resp, err := c.Get(ctx, u)
	if err != nil {
		return fetchedMsg{err: err}
	}
	msg := fetchedMsg{addr: Location{Gemini: resp.URL}}
	switch resp.Class() {
	case gemini.StatusInput:
		prompt := resp.Meta
		if prompt == "" {
			prompt = "input:"
		}
		msg.input = &inputRequest{prompt: prompt, sensitive: resp.Status == 11}
	case gemini.StatusSuccess:
		msg.data, msg.mime = resp.Body, resp.Meta
	case gemini.StatusClientCrt:
		msg.err = fmt.Errorf("%s requires a client certificate, which mazama doesn't support yet", resp.URL.Host)
	default:
		meta := resp.Meta
		if meta == "" {
			meta = "request failed"
		}
		msg.err = fmt.Errorf("gemini %d: %s", resp.Status, meta)
	}
	return msg
}

// geminiPage fills a page from a gemini success response.
func geminiPage(p *page, mimeType string, body []byte) error {
	mt := "text/gemini"
	if mimeType != "" {
		parsed, _, err := mime.ParseMediaType(mimeType)
		if err != nil {
			return fmt.Errorf("bad content type %q", mimeType)
		}
		mt = parsed
	}
	p.kind = textPage
	switch {
	case mt == "text/gemini":
		p.raw, p.markdown, p.gemtext = gemini.ToMarkdown(string(body)), true, true
	case mt == "text/markdown":
		p.raw, p.markdown = string(body), true
	case strings.HasPrefix(mt, "text/"):
		p.raw = string(body)
	default:
		return fmt.Errorf("can't display %s content", mt)
	}
	return nil
}
