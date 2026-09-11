package render

import "strings"

// StripMarkdown reduces the small Markdown subset the Text component allows
// (bold, italic, inline code, links and ATX headings) to plain text. Markup
// markers are removed; link URLs are dropped in favor of their text. The
// input comes from untrusted agents, so the strip is a single linear scan
// with no regex backtracking. Unpaired markers are preserved literally.
func StripMarkdown(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	runes := []rune(s)
	emphasis := false // inside *...* or **...**
	underscore := false
	inCode := false
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '*':
			// Toggle emphasis markers; drop them.
			if emphasis || strings.ContainsRune(string(runes[i+1:]), '*') {
				for i+1 < len(runes) && runes[i+1] == '*' {
					i++
				}
				emphasis = !emphasis
				continue
			}
			out.WriteRune(runes[i])
		case '_':
			if underscore || strings.ContainsRune(string(runes[i+1:]), '_') {
				for i+1 < len(runes) && runes[i+1] == '_' {
					i++
				}
				underscore = !underscore
				continue
			}
			out.WriteRune(runes[i])
		case '`':
			if inCode || strings.ContainsRune(string(runes[i+1:]), '`') {
				inCode = !inCode
				continue
			}
			out.WriteRune(runes[i])
		case '#':
			// ATX heading markers: only at a line start, followed by a
			// space (or another #). Skip the marker run and its spaces.
			if i == 0 || runes[i-1] == '\n' {
				if i+1 >= len(runes) || runes[i+1] == ' ' || runes[i+1] == '#' {
					for i+1 < len(runes) && (runes[i+1] == '#' || runes[i+1] == ' ') {
						i++
					}
					continue
				}
			}
			out.WriteRune(runes[i])
		case '[':
			// [text](url) -> text.
			if close := indexRune(runes, i+1, ']'); close >= 0 &&
				close+1 < len(runes) && runes[close+1] == '(' {
				if open := indexRune(runes, close+2, ')'); open >= 0 {
					out.WriteString(string(runes[i+1 : close]))
					i = open
					continue
				}
			}
			out.WriteRune(runes[i])
		case '\\':
			// Escaped punctuation: keep the escaped character.
			if i+1 < len(runes) && strings.ContainsRune("\\`*_{}[]()#+-.!", runes[i+1]) {
				i++
				out.WriteRune(runes[i])
				continue
			}
			out.WriteRune(runes[i])
		default:
			out.WriteRune(runes[i])
		}
	}
	return strings.TrimSpace(out.String())
}

func indexRune(runes []rune, from int, target rune) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}
