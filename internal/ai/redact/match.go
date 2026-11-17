package redact

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Names are matched on a canonical view of the text, not rune for rune, so that
// the ways the same name gets typed all find it:
//
//   - Accents: a name matches with its accents dropped or written differently
//     ("Maire Nolan", "MAIRE NOLAN", "Ma" + combining acute + "ire Nolan" all find
//     "Máire Nolan"). The view is decomposed and its combining marks removed. This
//     redacts a little more than the name (the unaccented spelling of another word),
//     which is the safe side for a tool whose job is to hide names.
//   - Whitespace: any run of spaces, tabs, line breaks (a hard-wrapped Markdown
//     paragraph), no-break and other Unicode spaces is one space.
//   - Invisible characters (zero-width spaces and joiners, the byte order mark,
//     soft hyphens, bidirectional controls) are ignored, so they cannot split a name.
//   - Case, and the apostrophe and hyphen variants (straight and curly).
//
// The view keeps, for each of its runes, the span of the original text it came
// from, so a match replaces exactly the original characters, and the text
// around it is untouched.

type view struct {
	canon      []rune
	start, end []int // per canonical rune: the half-open range of original runes
}

func ignorable(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return r == 0xFEFF || r == 0x00AD
}

func fold(r rune) rune {
	switch r {
	case '’', '‘', 'ʼ', '′', '`', '´', 'ʹ':
		return '\''
	case '‐', '‑', '‒', '–', '—', '−':
		return '-'
	}
	return unicode.ToLower(r)
}

func buildView(rs []rune) view {
	v := view{canon: make([]rune, 0, len(rs)), start: make([]int, 0, len(rs)), end: make([]int, 0, len(rs))}
	emit := func(r rune, a, b int) {
		v.canon = append(v.canon, r)
		v.start = append(v.start, a)
		v.end = append(v.end, b)
	}
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case ignorable(r):
			i++
		case unicode.IsSpace(r):
			j := i
			for j < len(rs) && (unicode.IsSpace(rs[j]) || ignorable(rs[j])) {
				j++
			}
			emit(' ', i, j)
			i = j
		default:
			j := i + 1
			for j < len(rs) && (unicode.Is(unicode.Mn, rs[j]) || unicode.Is(unicode.Me, rs[j])) {
				j++
			}
			c := string(rs[i:j])
			if j-i > 1 || rs[i] >= 0x80 {
				c = norm.NFD.String(c)
			}
			for _, cr := range c {
				if unicode.Is(unicode.Mn, cr) || unicode.Is(unicode.Me, cr) {
					continue
				}
				emit(fold(cr), i, j)
			}
			i = j
		}
	}
	return v
}

// canonical is the canonical form of a name, for matching.
func canonical(s string) []rune {
	return buildView([]rune(strings.TrimSpace(s))).canon
}

// slugOf is the form a name takes in a URL or a file name: accents dropped,
// lower case, anything that is not a letter or a digit one hyphen.
func slugOf(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r) || ignorable(r):
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(unicode.ToLower(r))
		default:
			dash = true
		}
	}
	return b.String()
}
