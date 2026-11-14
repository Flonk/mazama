package gemini

import "strings"

// ToMarkdown converts gemtext (text/gemini) to markdown so it can go through
// the same renderer as markdown documents. Gemtext is line-oriented, so each
// source line keeps its own line (joined with hard breaks), and all text is
// escaped so it is never reinterpreted as markdown.
func ToMarkdown(src string) string {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	var blocks []string
	var group []string
	groupKind := ""

	flush := func() {
		if len(group) == 0 {
			return
		}
		switch groupKind {
		case "list":
			blocks = append(blocks, strings.Join(group, "\n"))
		default: // text, links and quotes keep one line per source line
			blocks = append(blocks, strings.Join(group, "\\\n"))
		}
		group, groupKind = nil, ""
	}
	add := func(kind, line string) {
		if kind != groupKind {
			flush()
			groupKind = kind
		}
		group = append(group, line)
	}

	var pre []string
	inPre := false
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, "```") {
			if inPre {
				blocks = append(blocks, fence(pre))
				pre, inPre = nil, false
			} else {
				flush()
				inPre = true
			}
			continue
		}
		if inPre {
			pre = append(pre, line)
			continue
		}

		switch {
		case strings.HasPrefix(line, "=>"):
			target, label := ParseLink(line)
			if target == "" {
				continue
			}
			if label == "" {
				label = target
			}
			// Text and links share a group so they stay on adjacent lines,
			// as they are in the source.
			add("flow", "→ ["+escape(label)+"](<"+strings.NewReplacer("<", "%3C", ">", "%3E").Replace(target)+">)")
		case strings.HasPrefix(line, "###"):
			flush()
			blocks = append(blocks, "### "+escape(strings.TrimSpace(line[3:])))
		case strings.HasPrefix(line, "##"):
			flush()
			blocks = append(blocks, "## "+escape(strings.TrimSpace(line[2:])))
		case strings.HasPrefix(line, "#"):
			flush()
			blocks = append(blocks, "# "+escape(strings.TrimSpace(line[1:])))
		case strings.HasPrefix(line, "* "):
			add("list", "- "+escape(strings.TrimSpace(line[2:])))
		case strings.HasPrefix(line, ">"):
			add("quote", "> "+escape(strings.TrimSpace(line[1:])))
		case strings.TrimSpace(line) == "":
			flush()
		default:
			add("flow", escape(strings.TrimLeft(line, " \t")))
		}
	}
	flush()
	if inPre { // unterminated preformatted block runs to the end
		blocks = append(blocks, fence(pre))
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// ParseLink splits a "=> URL [label]" line.
func ParseLink(line string) (target, label string) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, "=>"))
	if rest == "" {
		return "", ""
	}
	i := strings.IndexAny(rest, " \t")
	if i < 0 {
		return rest, ""
	}
	return rest[:i], strings.TrimSpace(rest[i:])
}

func fence(lines []string) string {
	return "```\n" + strings.Join(lines, "\n") + "\n```"
}

// escape backslash-escapes every ASCII punctuation character markdown could
// act on. CommonMark allows escaping any ASCII punctuation.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_{}[]()#+-.!|<>~&", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
