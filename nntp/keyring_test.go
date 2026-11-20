package nntp

import (
	"strings"
	"testing"
)

func TestLoginEncoding(t *testing.T) {
	for _, c := range [][2]string{{"u", "secret"}, {"u\nx", "p\nq\r\\n"}, {"u", ""}} {
		s, err := encodeLogin(c[0], c[1])
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(s, "\r\n\\") {
			t.Errorf("encoded %q contains a line break or backslash", s)
		}
		u, p, ok := decodeLogin(s)
		if !ok || u != c[0] || p != c[1] {
			t.Errorf("round trip %q: got %q %q %v", c, u, p, ok)
		}
	}
	if u, p, ok := decodeLogin("old\npass"); !ok || u != "old" || p != "pass" {
		t.Errorf("legacy format: got %q %q %v", u, p, ok)
	}
	if _, err := encodeLogin("", "p"); err == nil {
		t.Error("empty user accepted")
	}
}
