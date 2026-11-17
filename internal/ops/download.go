package ops

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Download is implemented by an op's output when it is a file rather than
// JSON. The REST binding writes the bytes with an attachment header; the other
// bindings (CLI, MCP) encode the value as JSON like any other output.
type Download interface {
	Download() (filename, contentType string, body []byte)
}

// Inline is implemented by a Download that is an image to show rather than a
// file to save (a cover). The REST binding then sends it inline with the
// returned ETag, and answers 304 to a matching If-None-Match.
type Inline interface {
	Inline() (etag string)
}

// SafeFilename makes a name that is safe to put in a Content-Disposition
// header and to save to a disk: no path separators, no control characters, no
// quotes, no leading dots, a bounded length. It returns "download" when
// nothing usable is left.
func SafeFilename(name string) string {
	var b strings.Builder
	sep := rune(0) // a pending run of separators: '-' if it held a path-like character, else ' '
	for _, r := range name {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '|' || r == '?' || r == '*' || r == '<' || r == '>':
			sep = '-'
		case unicode.IsSpace(r):
			if sep == 0 {
				sep = ' '
			}
		case r == '"' || r == '\'' || unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			// dropped: quotes, control characters, and invisible formatting such as a BOM
		default:
			if sep != 0 && b.Len() > 0 {
				b.WriteRune(sep)
			}
			sep = 0
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .-")
	for len(out) > 120 { // bytes; cut on a rune boundary
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	out = strings.Trim(out, " .-")
	if out == "" {
		return "download"
	}
	return out
}

// ContentDisposition is the attachment header for a file name: an ASCII
// fallback and the RFC 5987 UTF-8 form.
func ContentDisposition(name string) string {
	name = SafeFilename(name)
	var ascii strings.Builder
	for _, r := range name {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' || r == '%' {
			ascii.WriteByte('_')
		} else {
			ascii.WriteRune(r)
		}
	}
	return `attachment; filename="` + ascii.String() + `"; filename*=UTF-8''` + percentEncode(name)
}

// percentEncode is RFC 5987's attr-char encoding.
func percentEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '!', c == '#', c == '$', c == '&', c == '+', c == '-', c == '.', c == '^', c == '_', c == '`', c == '|', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
