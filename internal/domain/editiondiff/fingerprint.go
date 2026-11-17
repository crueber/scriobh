package editiondiff

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A chapter is fingerprinted by the hashes of its runs of three words (shingles),
// kept sorted and unique. Two chapters that share most of their text share most
// of their shingles, whatever was done to a paragraph in between, and the
// intersection of two sorted lists is a single merge. A very large book keeps
// only the shingles whose hash is a multiple of a step chosen from the size of
// the books, the same step on both sides, so that the fingerprints, and the
// index that finds candidates, stay a fixed size.

const shingleWords = 3

// countWords counts the words of a text the way revision.diff does: runs of text
// between blanks that hold a letter or a digit.
func countWords(s string) int {
	n := 0
	in, has := false, false
	for i := 0; i < len(s); {
		r := rune(s[i])
		w := 1
		if r >= utf8.RuneSelf {
			r, w = utf8.DecodeRuneInString(s[i:])
		}
		i += w
		if unicode.IsSpace(r) {
			if in && has {
				n++
			}
			in, has = false, false
			continue
		}
		in = true
		if !has && (unicode.IsLetter(r) || unicode.IsNumber(r)) {
			has = true
		}
	}
	if in && has {
		n++
	}
	return n
}

const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

// fingerprint is the sorted unique sampled shingle hashes of text. step 1 keeps
// them all.
func fingerprint(s string, step uint32) []uint32 {
	var out []uint32
	var w1, w2 uint32 // the two words before the one being read
	have := 0
	var h uint64 = fnvOffset
	in := false
	flush := func() {
		if !in {
			return
		}
		in = false
		w := uint32(h ^ h>>32)
		h = fnvOffset
		if have >= shingleWords-1 {
			x := (w1*0x9E3779B1 ^ w2*0x85EBCA6B) ^ w*0xC2B2AE35
			x ^= x >> 15
			x *= 0x2C1B3C6D
			x ^= x >> 12
			if step <= 1 || x%step == 0 {
				out = append(out, x)
			}
		}
		w1, w2 = w2, w
		have++
	}
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			i++
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
				in = true
				h = (h ^ uint64(c)) * fnvPrime
			case c >= 'A' && c <= 'Z':
				in = true
				h = (h ^ uint64(c+32)) * fnvPrime
			default:
				flush()
			}
			continue
		}
		r, w := utf8.DecodeRuneInString(s[i:])
		i += w
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			in = true
			h = (h ^ uint64(unicode.ToLower(r))) * fnvPrime
		} else {
			flush()
		}
	}
	flush()
	if len(out) < 2 {
		return out
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	u := out[:1]
	for _, v := range out[1:] {
		if v != u[len(u)-1] {
			u = append(u, v)
		}
	}
	return u
}

// inter is the size of the intersection of two sorted unique lists.
func inter(a, b []uint32) int {
	n := 0
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			n++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return n
}

// dice is 2|a∩b| / (|a|+|b|).
func dice(ov, na, nb int) float64 {
	if na+nb == 0 {
		return 0
	}
	return 2 * float64(ov) / float64(na+nb)
}

var (
	reGeneric = regexp.MustCompile(`^(?:(?:chapter|part|scene|section|book|act|letter|day)\b[\s.:#-]*)?(?:[0-9]+|[ivxlc]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)?[\s.:-]*$`)
	reSpace   = regexp.MustCompile(`\s+`)
)

// titleKey is a title as it is compared: lower case, one space, no marks at the ends.
func titleKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(reSpace.ReplaceAllString(s, " ")))
	return strings.Trim(s, " .:;,-–—*_#\"'“”‘’")
}

// genericTitle is a title that names a position, not a chapter: "Chapter 3", "Part II", "Untitled".
func genericTitle(key string) bool {
	return key == "" || key == "untitled" || key == "scene" || reGeneric.MatchString(key)
}
