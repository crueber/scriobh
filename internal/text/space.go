package text

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// isJSSpace reports whether r is whitespace as JavaScript's \s sees it. The
// editor splits words on \s, so the Go counter must too: unicode.IsSpace
// differs at U+0085 (Go only) and U+FEFF (JavaScript only).
func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

// jsTrim is String.prototype.trim: it strips JavaScript whitespace and
// line terminators from both ends.
func jsTrim(s string) string { return strings.TrimFunc(s, isJSSpace) }

func firstRune(b []byte) rune {
	r, _ := utf8.DecodeRune(b)
	return r
}

// isWord reports whether tok contains a letter or a digit (\p{L} or \p{N}).
func isWord(tok string) bool {
	for _, r := range tok {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// words calls fn for each whitespace-delimited run of s, as the editor splits
// text. fn receives the run.
func words(s string, fn func(string)) {
	start := -1
	for i, r := range s {
		if isJSSpace(r) {
			if start >= 0 {
				fn(s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		fn(s[start:])
	}
}
