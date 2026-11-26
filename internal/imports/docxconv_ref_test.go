package imports

import (
	"math/rand"
	"strings"
	"testing"
	"unicode"
)

// The reference versions of the helpers a Word paragraph is read with, as they were before they were made faster. The one-pass
// versions must agree with them on every input, including invalid UTF-8 (a []rune turns each bad byte into U+FFFD), the Unicode
// spaces, digits that are not ASCII, and the private-use characters that mark revisions.

func refCollapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func refHasMark(s string) bool {
	return strings.ContainsAny(s, "")
}

func refEscMDx(s string, lead, trail bool) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		switch r {
		case '\\', '`', '*', '[', ']', '<', '>', '~', '|', '&':
			b.WriteByte('\\')
		case '_':
			prevAlnum := i > 0 && (unicode.IsLetter(rs[i-1]) || unicode.IsDigit(rs[i-1]))
			nextAlnum := i+1 < len(rs) && (unicode.IsLetter(rs[i+1]) || unicode.IsDigit(rs[i+1]))
			if !(prevAlnum && nextAlnum) {
				b.WriteByte('\\')
			}
		case '%':
			if (lead && i == 0) || (trail && i == len(rs)-1) || (i+1 < len(rs) && rs[i+1] == '%') || (i > 0 && rs[i-1] == '%') {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

func refEscLine(s string) string {
	if lead := leadMarks(s); lead != "" {
		return lead + refEscLine(s[len(lead):])
	}
	s = strings.TrimLeft(s, " \t ")
	if s == "" {
		return s
	}
	rs := []rune(s)
	switch rs[0] {
	case '#', '+', '-', '=', ':', '~':
		return "\\" + s
	}
	if unicode.IsDigit(rs[0]) {
		j := 0
		for j < len(rs) && unicode.IsDigit(rs[j]) {
			j++
		}
		if j < len(rs) && (rs[j] == '.' || rs[j] == ')') && (j+1 == len(rs) || rs[j+1] == ' ') {
			return string(rs[:j]) + "\\" + string(rs[j:])
		}
	}
	return s
}

var refAlphabet = []string{"a", "Z", "é", "中", "3", "٣", "_", "%", "\\", "`", "*", "[", "]", "<", ">", "~", "|", "&", " ", "\t", "\n",
	" ", "\u0085", " ", "　", "​", "", "", "", "\xff", "\xc3", "#", "-", "+", "=", ":", ".", ")",
	"1", "\r", "\v", "\f", "'", "\"", "é_", "x%"}

func refRandom(rng *rand.Rand) string {
	var b strings.Builder
	for n := rng.Intn(14); n > 0; n-- {
		b.WriteString(refAlphabet[rng.Intn(len(refAlphabet))])
	}
	return b.String()
}

func TestParagraphHelpersMatchTheirReferences(t *testing.T) {
	fixed := []string{"", " ", "a", "a  b\tc\nd", " x\u0085y z　", "\xffa \xc3\xa9 \xff", "x", "x", "_a_ b_ _c %%d% %",
		"\\`*[]<>~|&", "1. one", "12) two", "٣. arabic digit", "# head", "- item", "1.5 x", "  \t#", "*_x_*", "%a%b%", "é_é", "x_é"}
	rng := rand.New(rand.NewSource(7))
	inputs := append([]string(nil), fixed...)
	for i := 0; i < 30000; i++ {
		inputs = append(inputs, refRandom(rng))
	}
	for _, s := range inputs {
		if got, want := collapseSpace(s), refCollapse(s); got != want {
			t.Fatalf("collapseSpace(%q) = %q, want %q", s, got, want)
		}
		if got, want := hasRevisionMark(s), refHasMark(s); got != want {
			t.Fatalf("hasRevisionMark(%q) = %v, want %v", s, got, want)
		}
		for _, lead := range []bool{false, true} {
			for _, trail := range []bool{false, true} {
				if got, want := escMDx(s, lead, trail), refEscMDx(s, lead, trail); got != want {
					t.Fatalf("escMDx(%q, %v, %v) = %q, want %q", s, lead, trail, got, want)
				}
			}
		}
		if got, want := escLine(s), refEscLine(s); got != want {
			t.Fatalf("escLine(%q) = %q, want %q", s, got, want)
		}
	}
}
