package nntp

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
)

// Article is a parsed article: decoded headers and the text body.
type Article struct {
	Header mail.Header // raw headers; use DecodeHeader on values
	Body   string
	// ContentType is the media type of Body: text/plain, or text/markdown
	// (RFC 7763) when the poster sent markdown.
	ContentType string
}

// ParseArticle parses an article as returned by ARTICLE, decoding MIME
// transfer encodings and charsets into UTF-8 text. For multipart articles
// the best text part is used: markdown, then plain text, then other text.
func ParseArticle(raw []byte) (*Article, error) {
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	p, err := decodePart(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Body, 0)
	if err != nil {
		return nil, err
	}
	mt := p.mediaType
	if mt != "text/markdown" {
		mt = "text/plain"
	}
	return &Article{Header: msg.Header, Body: p.text, ContentType: mt}, nil
}

type decodedPart struct {
	text      string
	mediaType string
}

// partRank orders the parts of a multipart article by how well mazama shows
// them.
func partRank(mt string) int {
	switch {
	case mt == "text/markdown":
		return 3
	case mt == "text/plain":
		return 2
	case strings.HasPrefix(mt, "text/"):
		return 1
	}
	return 0
}

func decodePart(contentType, cte string, r io.Reader, depth int) (decodedPart, error) {
	mt, params := "text/plain", map[string]string{}
	if contentType != "" {
		if t, p, err := mime.ParseMediaType(contentType); err == nil {
			mt, params = t, p
		}
	}

	if strings.HasPrefix(mt, "multipart/") && depth < 3 {
		mr := multipart.NewReader(r, params["boundary"])
		var best decodedPart
		bestRank := -1
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return decodedPart{}, fmt.Errorf("reading multipart article: %w", err)
			}
			// multipart.Reader already undoes quoted-printable.
			p, err := decodePart(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part, depth+1)
			if err != nil {
				continue
			}
			if rank := partRank(p.mediaType); rank > bestRank {
				best, bestRank = p, rank
			}
		}
		if bestRank < 0 {
			return decodedPart{}, errors.New("article has no text part")
		}
		return best, nil
	}

	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, r)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return decodedPart{}, fmt.Errorf("decoding article: %w", err)
	}
	if !strings.HasPrefix(mt, "text/") {
		return decodedPart{fmt.Sprintf("[%s attachment, %d bytes, not shown]", mt, len(data)), mt}, nil
	}
	return decodedPart{decodeCharset(data, params["charset"]), mt}, nil
}

// decodeCharset converts text in the named charset to UTF-8. Unknown or
// missing charsets fall back to UTF-8 if valid, else Windows-1252, which is
// what unlabelled 8-bit Usenet text usually is.
func decodeCharset(data []byte, charset string) string {
	cs := strings.ToLower(strings.TrimSpace(charset))
	if cs != "" && cs != "utf-8" && cs != "us-ascii" {
		if enc, err := htmlindex.Get(cs); err == nil {
			if out, err := enc.NewDecoder().Bytes(data); err == nil {
				return string(out)
			}
		}
	}
	return toUTF8(string(data))
}

func toUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	out, err := charmap.Windows1252.NewDecoder().String(s)
	if err != nil {
		return strings.ToValidUTF8(s, "�")
	}
	return out
}

var wordDecoder = mime.WordDecoder{
	CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		enc, err := htmlindex.Get(charset)
		if err != nil {
			return nil, err
		}
		return enc.NewDecoder().Reader(input), nil
	},
}

// DecodeHeader decodes RFC 2047 encoded words and stray 8-bit text in a
// header value.
func DecodeHeader(v string) string {
	if out, err := wordDecoder.DecodeHeader(v); err == nil {
		v = out
	}
	return toUTF8(v)
}

// DisplayName returns the name part of a From header, or the address if
// there is no name.
func DisplayName(from string) string {
	from = DecodeHeader(from)
	if a, err := mail.ParseAddress(from); err == nil {
		if a.Name != "" {
			return a.Name
		}
		return a.Address
	}
	// Old style: "user@host (Real Name)".
	if i, j := strings.Index(from, "("), strings.LastIndex(from, ")"); i >= 0 && j > i+1 {
		return from[i+1 : j]
	}
	return strings.Trim(from, `" `)
}
