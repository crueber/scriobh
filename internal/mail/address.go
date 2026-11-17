package mail

import (
	"errors"
	"net"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Errors a caller may show to a person. None carries input.
var (
	ErrAddress = errors.New("that is not a single plain email address (name@example.com: no list, no display name, ASCII only)")
	ErrHeader  = errors.New("a header value may not contain a line break or a control character")
)

// ValidAddress checks that s is exactly one bare, ASCII email address and returns it. A list, a display name,
// a quoted local part, an address literal and anything with a control character are refused: the address ends
// up in an SMTP command and in a header, and the only safe address is a plain one.
func ValidAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 254 {
		return "", ErrAddress
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f || strings.IndexByte(`<>()[]\,;:"`, c) >= 0 {
			return "", ErrAddress
		}
	}
	at := strings.LastIndexByte(s, '@')
	if at < 1 || at == len(s)-1 || strings.Count(s, "@") != 1 {
		return "", ErrAddress
	}
	local, domain := s[:at], s[at+1:]
	if len(local) > 64 || strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return "", ErrAddress
	}
	if !validDomain(domain) {
		return "", ErrAddress
	}
	// net/mail agrees that it is one bare address.
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" || a.Address != s {
		return "", ErrAddress
	}
	return s, nil
}

func validDomain(d string) bool {
	if len(d) > 253 || !strings.Contains(d, ".") || net.ParseIP(d) != nil {
		return false
	}
	labels := strings.Split(d, ".")
	// A top-level label is never all digits (RFC 3696): x@127.1 and x@10.1 are numbers, not names.
	if last := labels[len(labels)-1]; strings.Trim(last, "0123456789") == "" {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// DomainOf is the part after the @ of an address, lower-cased, or "" when there is none.
func DomainOf(addr string) string {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 {
		return ""
	}
	return strings.ToLower(addr[i+1:])
}

// checkHeaderValue refuses CR, LF, NUL and the other control characters (a tab is allowed) and invalid UTF-8.
func checkHeaderValue(s string) error {
	if !utf8.ValidString(s) {
		return ErrHeader
	}
	for _, r := range s {
		if r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == 0x2028 || r == 0x2029 {
			return ErrHeader
		}
	}
	return nil
}

// flatText makes text a person wrote safe to show on one line of a plain-text body: control characters and
// line breaks become spaces, runs of white space collapse, and it is cut to max runes. It is for titles in a
// digest, where a collaborator chose the words.
func flatText(s string, max int) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == utf8.RuneError {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
		if n++; n >= max {
			b.WriteString("...")
			break
		}
	}
	out := b.String()
	// A mail client turns what looks like an address into a link. The app adds no link of its own but its base
	// address, so a title cannot carry one: the scheme separator and a leading www. are broken up.
	out = strings.ReplaceAll(out, "://", ": //")
	out = strings.ReplaceAll(out, "www.", "www .")
	return out
}
