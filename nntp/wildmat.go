package nntp

import (
	"path"
	"strings"
)

// MatchWildmat matches a group name against an RFC 3977 wildmat: a
// comma-separated list of patterns using * ? and [...], where a pattern
// starting with ! excludes and the last matching pattern decides.
func MatchWildmat(wildmat, name string) (bool, error) {
	matched := false
	for _, pat := range strings.Split(wildmat, ",") {
		neg := strings.HasPrefix(pat, "!")
		pat = strings.TrimPrefix(pat, "!")
		// Group names never contain '/', so path.Match's special
		// treatment of it doesn't matter.
		ok, err := path.Match(pat, name)
		if err != nil {
			return false, err
		}
		if ok {
			matched = !neg
		}
	}
	return matched, nil
}

// HierarchyPrefix reports whether a wildmat lists one whole hierarchy ("*"
// or "comp.lang.*") and returns its prefix ("" or "comp.lang.").
func HierarchyPrefix(wildmat string) (string, bool) {
	if wildmat == "" || wildmat == "*" {
		return "", true
	}
	prefix, ok := strings.CutSuffix(wildmat, ".*")
	if !ok || prefix == "" || IsWildmat(prefix) {
		return "", false
	}
	return prefix + ".", true
}
