package text

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// What may be written into a manuscript on a model's say-so. These are the
// shared rules of the AI layers: the recipe parsers (internal/ai) refuse early
// and the document write seam (internal/domain/docs) refuses finally, both with
// the same functions, so the two cannot disagree.

// IsInvisible reports whether r is a character that shows nothing or changes how
// the text around it is shown: every Unicode format character (Cf: soft hyphen,
// zero-width space, joiners, direction marks and overrides, the Arabic letter
// mark U+061C, invisible operators, the tag block U+E0000 to U+E007F, ...) except
// the zero-width joiner and non-joiner, which emoji sequences and several scripts
// (Indic, Persian) need; also the line and paragraph separators and every
// variation selector except the text and emoji presentation selectors U+FE0E and
// U+FE0F. The left-to-right and right-to-left marks are removed too: a text that
// needs them can be corrected by hand.
func IsInvisible(r rune) bool {
	switch r {
	case 0x200c, 0x200d, 0xfe0e, 0xfe0f:
		return false
	case 0x2028, 0x2029:
		return true
	}
	if r >= 0xfe00 && r <= 0xfe0f || r >= 0xe0100 && r <= 0xe01ef {
		return true
	}
	return unicode.Is(unicode.Cf, r)
}

// HasInvisible reports whether s holds an invisible character.
func HasInvisible(s string) bool { return strings.IndexFunc(s, IsInvisible) >= 0 }

// StripInvisible removes the invisible characters of s.
func StripInvisible(s string) string {
	if !HasInvisible(s) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if IsInvisible(r) {
			return -1
		}
		return r
	}, s)
}

// ---- links, images and addresses -----------------------------------------------------------------

var (
	badScheme = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:javascript|vbscript):|(?:^|[^a-z0-9])data:[a-z]+/[a-z0-9.+-]+`)
	imageRe   = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)|!\[[^\]]*\]\[[^\]]*\]`)
	targetRe  = regexp.MustCompile(`\]\(\s*<?([^)\s>]*)`)
	bareURLRe = regexp.MustCompile(`(?i)\b(?:https?://|ftp://|www\.)[^\s<>)\]]+`)
	refDefRe  = regexp.MustCompile(`(?m)^[ ]{0,3}\[[^\]\n]+\]:[ \t]*(\S+)`)
	wikiRe    = regexp.MustCompile(`\[\[[^\]\n]*\]\]`)
)

// LinkProblem says why a model-written fragment may not carry the links it
// carries, or "". The rule: an address or a link it uses must already be in
// original (the text the fragment replaces), so a suggestion can keep a link
// the writer put there and can never add one. javascript:, vbscript: and data:
// addresses are refused whatever the original says. A markdown image, a link
// target, a bare address, a reference definition and a [[wiki link]] are each
// checked: the same words, character for character, must be in original. A
// fragment inserted where nothing is replaced (original empty) has none.
func LinkProblem(s, original string) string {
	if badScheme.MatchString(s) {
		return "it contains a javascript:, vbscript: or data: address"
	}
	for _, m := range imageRe.FindAllString(s, -1) {
		if !strings.Contains(original, m) {
			return "it contains an image the text did not have"
		}
	}
	for _, m := range targetRe.FindAllStringSubmatch(s, -1) {
		if !strings.Contains(original, m[1]) || m[1] == "" {
			return "it contains a link target the text did not have"
		}
	}
	for _, m := range refDefRe.FindAllStringSubmatch(s, -1) {
		if !strings.Contains(original, m[1]) {
			return "it contains a link address the text did not have"
		}
	}
	for _, m := range bareURLRe.FindAllString(s, -1) {
		if !strings.Contains(original, m) {
			return "it contains a web address the text did not have"
		}
	}
	for _, m := range wikiRe.FindAllString(s, -1) {
		if !strings.Contains(original, m) {
			return "it contains a [[link]] the text did not have"
		}
	}
	return ""
}

// ---- what is hidden from the model and may not be changed ------------------------------------------

var privateRe = regexp.MustCompile(`(?s)%%.*?%%`)

// HiddenSpans are the byte ranges of a body that an AI edit may not touch, and
// that the model never saw: ghosted text with its tags, and the author's
// %%private notes%% (a half-typed note holds the rest of its paragraph). The
// ranges are merged, sorted and do not overlap.
func HiddenSpans(body string) [][2]int {
	var out [][2]int
	if spans, _ := Ghosts(body); len(spans) > 0 {
		for _, g := range spans {
			out = append(out, [2]int{g.OpenStart, g.CloseEnd})
		}
	}
	if strings.Contains(body, "%%") {
		for _, m := range privateRe.FindAllStringIndex(body, -1) {
			out = append(out, [2]int{m[0], m[1]})
		}
		rest := privateRe.ReplaceAllStringFunc(body, func(m string) string { return strings.Repeat(" ", len(m)) })
		for i := 0; i < len(rest); {
			j := strings.Index(rest[i:], "%%")
			if j < 0 {
				break
			}
			a, b := i+j, len(rest)
			if k := strings.Index(rest[a:], "\n\n"); k >= 0 {
				b = a + k
			}
			out = append(out, [2]int{a, b})
			i = b
		}
	}
	return MergeSpans(out)
}

// MergeSpans sorts ranges and joins the ones that overlap or touch.
func MergeSpans(in [][2]int) [][2]int {
	if len(in) < 2 {
		return in
	}
	sort.Slice(in, func(i, j int) bool { return in[i][0] < in[j][0] })
	out := in[:1]
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if s[0] <= last[1] {
			if s[1] > last[1] {
				last[1] = s[1]
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// TouchesSpans reports whether the range [a,b) overlaps a merged span. An empty
// range (an insertion point) touches one only when it lies strictly inside it.
func TouchesSpans(spans [][2]int, a, b int) bool {
	i := sort.Search(len(spans), func(i int) bool { return spans[i][1] > a })
	for ; i < len(spans) && spans[i][0] < max(b, a+1); i++ {
		s := spans[i]
		if a == b {
			if s[0] < a && a < s[1] {
				return true
			}
			continue
		}
		if s[0] < b && s[1] > a {
			return true
		}
	}
	return false
}
