package publish

import (
	"strings"
)

// xmlEscape escapes text for an XML text node or a double-quoted attribute,
// after removing what XML cannot hold.
func xmlEscape(s string) string {
	s = CleanXML(s)
	if !strings.ContainsAny(s, `&<>"'`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// oneLine turns the white space of a text into single spaces: the reading of a
// soft line break, a tab or a run of blanks in Markdown.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\n\t\r") {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}
