package site

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// MaxSlugBytes bounds a slug, so a file name stays well under every file
// system's limit.
const MaxSlugBytes = 60

// extra folds letters that do not decompose.
var extra = map[rune]string{'ß': "ss", 'æ': "ae", 'œ': "oe", 'ø': "o", 'đ': "d", 'ð': "d", 'þ': "th", 'ł': "l", 'ı': "i", 'ħ': "h"}

// windowsReserved are the names Windows will not make a file of, with or without
// an extension.
var windowsReserved = map[string]bool{"con": true, "prn": true, "aux": true, "nul": true, "clock$": true}

func reservedName(s string) bool {
	if windowsReserved[s] {
		return true
	}
	if len(s) == 4 && (strings.HasPrefix(s, "com") || strings.HasPrefix(s, "lpt")) && s[3] >= '0' && s[3] <= '9' {
		return true
	}
	return false
}

// Slugify makes a file-name-safe, URL-safe slug from a title: letters and
// digits of any script, lower-cased, Latin accents folded away, everything else
// one hyphen. The result holds no dot, slash, backslash, colon or control
// character and is never a name Windows reserves, so it cannot climb out of a
// folder or collide on a case-insensitive disk. Empty means nothing usable was
// left (a title of only emoji).
func Slugify(title string) string {
	var b strings.Builder
	dash := true
	var prev rune
	for _, r := range norm.NFD.String(foldCase(title)) {
		if s, ok := extra[r]; ok {
			b.WriteString(s)
			dash = false
			prev = 'a'
			continue
		}
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			dash = false
			prev = r
		case unicode.IsMark(r):
			// A mark on a Latin letter is an accent: dropped. A mark on any other
			// script is part of the letter (a vowel sign): kept.
			if !dash && !unicode.Is(unicode.Latin, prev) {
				b.WriteRune(r)
			}
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(norm.NFC.String(b.String()), "-")
	if len(s) > MaxSlugBytes {
		cut := MaxSlugBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.Trim(norm.NFC.String(s[:cut]), "-")
	}
	if reservedName(s) {
		s += "-post"
	}
	return s
}

// foldCase is full Unicode case folding (final sigma, the Turkish dotted
// capital, the sharp s and ligatures included), which strings.ToLower is not.
func foldCase(s string) string {
	return cases.Fold().String(s)
}

// fold is the key two names are compared by: what a case-insensitive,
// normalization-insensitive disk (APFS, NTFS, exFAT) would treat as the same
// file.
func fold(s string) string {
	return norm.NFC.String(foldCase(norm.NFC.String(s)))
}

// FoldName is the key two file names are compared by on a disk that ignores case
// and normalization (see fold).
func FoldName(s string) string { return fold(s) }

func hash6(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:6]
}

func hashHex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// uniq hands out slugs one at a time and never the same one twice (compared
// folded).
type uniq struct{ used map[string]bool }

func newUniq() *uniq { return &uniq{used: map[string]bool{}} }

// take returns want if free, else want with a stable suffix made from salt,
// else a counter. fallback is used when want is empty. suffixed reports that
// the slug is not the one asked for.
func (u *uniq) take(want, salt, fallback string) (slug string, suffixed bool) {
	if want == "" {
		want = fallback
	}
	if k := fold(want); !u.used[k] {
		u.used[k] = true
		return want, false
	}
	base := want
	if len(base) > MaxSlugBytes-7 {
		cut := MaxSlugBytes - 7
		for cut > 0 && !utf8.RuneStart(base[cut]) {
			cut--
		}
		base = strings.TrimRight(base[:cut], "-")
	}
	cand := base + "-" + hash6(salt)
	for i := 2; u.used[fold(cand)]; i++ {
		cand = base + "-" + hash6(salt) + "-" + itoa(i)
	}
	u.used[fold(cand)] = true
	return cand, true
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// ---- paths ------------------------------------------------------------------------------

// esc percent-encodes each segment of a slash-separated site path.
func escPath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// rel is the relative address of site path `to` from the page at site path
// `from`. Both are slash-separated without a leading slash; to may end in a
// fragment.
func rel(from, to string) string {
	frag := ""
	if i := strings.IndexByte(to, '#'); i >= 0 {
		to, frag = to[:i], to[i:]
	}
	fd := strings.Split(from, "/")
	fd = fd[:len(fd)-1]
	td := strings.Split(to, "/")
	name := td[len(td)-1]
	td = td[:len(td)-1]
	i := 0
	for i < len(fd) && i < len(td) && fd[i] == td[i] {
		i++
	}
	var b strings.Builder
	for j := i; j < len(fd); j++ {
		b.WriteString("../")
	}
	for j := i; j < len(td); j++ {
		b.WriteString(url.PathEscape(td[j]))
		b.WriteByte('/')
	}
	b.WriteString(url.PathEscape(name))
	out := b.String()
	if out == "" {
		out = "./"
	}
	return out + frag
}

// sortedKeys is the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
