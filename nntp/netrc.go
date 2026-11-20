package nntp

import (
	"os"
	"path/filepath"
	"strings"
)

// NetrcLookup returns a Client.Lookup that reads logins from ~/.authinfo and
// ~/.netrc ("machine HOST login USER password PASS"), the files Gnus, slrn
// and curl use.
func NetrcLookup() func(host string) (user, pass string, ok bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	paths := []string{filepath.Join(home, ".authinfo"), filepath.Join(home, ".netrc")}
	return func(host string) (string, string, bool) {
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if user, pass, ok := parseNetrc(string(data), host); ok {
				return user, pass, true
			}
		}
		return "", "", false
	}
}

// parseNetrc finds the login for host, falling back to a "default" entry.
func parseNetrc(data, host string) (user, pass string, ok bool) {
	type entry struct {
		machine, login, password string
		isDefault                bool
	}
	var entries []*entry
	var cur *entry
	toks := netrcTokens(data)
	for i := 0; i < len(toks); i++ {
		next := func() string {
			if i+1 < len(toks) {
				i++
				return toks[i]
			}
			return ""
		}
		switch toks[i] {
		case "machine":
			cur = &entry{machine: next()}
			entries = append(entries, cur)
		case "default":
			cur = &entry{isDefault: true}
			entries = append(entries, cur)
		case "login", "user":
			if v := next(); cur != nil {
				cur.login = v
			}
		case "password":
			if v := next(); cur != nil {
				cur.password = v
			}
		case "account", "port", "protocol", "force":
			next()
		case "macdef":
			// A macro runs to the next blank line, which the tokenizer
			// has lost; macros are rare in files used for news logins.
			next()
		}
	}
	for _, e := range entries {
		if strings.EqualFold(e.machine, host) && e.login != "" {
			return e.login, e.password, true
		}
	}
	for _, e := range entries {
		if e.isDefault && e.login != "" {
			return e.login, e.password, true
		}
	}
	return "", "", false
}

// netrcTokens splits on whitespace, honouring "double quotes" as authinfo
// files allow, and skips # comments.
func netrcTokens(s string) []string {
	var toks []string
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for i := 0; i < len(line); {
			switch c := line[i]; {
			case c == ' ' || c == '\t' || c == '\r':
				i++
			case c == '"':
				var b strings.Builder
				i++
				for i < len(line) && line[i] != '"' {
					if line[i] == '\\' && i+1 < len(line) {
						i++
					}
					b.WriteByte(line[i])
					i++
				}
				i++
				toks = append(toks, b.String())
			default:
				j := i
				for j < len(line) && !strings.ContainsRune(" \t\r", rune(line[j])) {
					j++
				}
				toks = append(toks, line[i:j])
				i = j
			}
		}
	}
	return toks
}
