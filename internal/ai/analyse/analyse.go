// Package analyse is the Lab's local analysers: long sentences, hedge words and
// small punctuation fixes, found by fixed rules over the text with no model, no
// network and no cost. They work with every provider off. What they find is a
// mark: a range of the body and a reason, and for a punctuation fix the exact
// text that would replace it. They never change anything.
//
// The rules are deliberately plain so a writer can predict them; the hedge list
// is English for now and the punctuation rules know that French sets a space
// before ; : ! and ?. Text the writer has set aside (ghosted text), private
// notes, code, links, URLs and headings are never looked at.
package analyse

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/text"
)

// Mark kinds, the same words the Lab uses.
const (
	KindLong  = "long_sentence"
	KindHedge = "hedge_words"
	KindPunct = "punctuation_fix"
)

// Mark is a range of the body (byte offsets) and why it was marked.
type Mark struct {
	Kind       string
	Start, End int
	Reason     string
	// Fix, when set, is the exact text that replaces Start..End. A mark with no
	// fix is an observation: the Lab dims it and the writer decides.
	Fix *string
}

// Options choose what runs.
type Options struct {
	// Kinds limits what runs; empty runs everything.
	Kinds []string
	// Language is the project's language tag; it picks the hedge list and the
	// punctuation conventions.
	Language string
	// LongWords is the sentence length (in words) above which a sentence is
	// marked: default 40.
	LongWords int
}

// Limits on what one run returns.
const (
	MaxPerKind = 300
	MaxMarks   = 900
	// DefaultLongWords is the default threshold for a long sentence.
	DefaultLongWords = 40
)

// HedgeWords is the English hedge list, whole words and phrases, matched without
// regard to case. A hedge softens a statement the writer may want to make
// plainly; it is a prompt to look, not an error.
var HedgeWords = []string{"perhaps", "maybe", "somewhat", "rather", "quite", "fairly", "sort of", "kind of", "a bit", "a little", "slightly",
	"almost", "nearly", "probably", "possibly", "apparently", "seemed to", "seems to", "appeared to", "appears to", "tended to", "I think", "I guess",
	"I suppose", "in a way", "more or less", "a sort of", "a kind of", "somehow", "supposedly", "arguably"}

var hedgeRe = func() *regexp.Regexp {
	var parts []string
	for _, w := range HedgeWords {
		parts = append(parts, strings.ReplaceAll(regexp.QuoteMeta(w), " ", `\s+`))
	}
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(parts, "|") + `)\b`)
}()

// Run analyses a body (the text of a document without its alternatives
// comment, with LF line endings). The marks are in document order. ok is false
// when the text is too irregular to place marks in (nothing is returned).
func Run(body string, o Options) (marks []Mark, ok bool) {
	want := func(k string) bool {
		if len(o.Kinds) == 0 {
			return true
		}
		for _, x := range o.Kinds {
			if x == k {
				return true
			}
		}
		return false
	}
	if strings.ContainsAny(body, "\x00\r") {
		// the block parser reads such text with other offsets (it replaces a NUL by a
		// longer character and folds line endings); marks could not be placed in it
		return nil, false
	}
	blocks, ok := text.Blocks(body)
	if !ok {
		return nil, false
	}
	skip := skipRanges(body)
	long := o.LongWords
	if long <= 0 {
		long = DefaultLongWords
	}
	lang := strings.ToLower(o.Language)
	french := strings.HasPrefix(lang, "fr")
	english := lang == "" || strings.HasPrefix(lang, "en")
	counts := map[string]int{}
	add := func(m Mark) {
		if counts[m.Kind] >= MaxPerKind || len(marks) >= MaxMarks {
			return
		}
		counts[m.Kind]++
		marks = append(marks, m)
	}
	type seg struct{ a, b int }
	// the document's quotation marks decide which form is the odd one out
	var straight, curly int
	for _, bl := range blocks {
		if !bl.Heading {
			t := body[bl.Start:bl.End]
			straight += strings.Count(t, `"`)
			curly += strings.Count(t, "“") + strings.Count(t, "”")
		}
	}
	for _, bl := range blocks {
		if bl.Heading {
			continue
		}
		var segs []seg
		if bl.Contiguous {
			segs = []seg{{bl.Start, bl.End}}
		} else {
			for _, ln := range bl.Lines {
				segs = append(segs, seg{ln[0], ln[1]})
			}
		}
		for _, sg := range segs {
			s := body[sg.a:sg.b]
			if want(KindLong) {
				for _, sp := range Sentences(s) {
					a, b := sg.a+sp[0], sg.a+sp[1]
					if overlaps(skip, a, b) {
						continue
					}
					if n := WordsIn(body[a:b]); n > long {
						add(Mark{Kind: KindLong, Start: a, End: b, Reason: itoa(n) + " words in one sentence"})
					}
				}
			}
			if want(KindHedge) && english {
				for _, m := range hedgeRe.FindAllStringIndex(s, -1) {
					a, b := sg.a+m[0], sg.a+m[1]
					if !overlaps(skip, a, b) {
						add(Mark{Kind: KindHedge, Start: a, End: b, Reason: "hedge: " + strings.ToLower(strings.Join(strings.Fields(body[a:b]), " "))})
					}
				}
			}
			if want(KindPunct) {
				punctuation(body, sg.a, sg.b, skip, french, straight, curly, add)
			}
		}
	}
	sort.SliceStable(marks, func(i, j int) bool {
		if marks[i].Start != marks[j].Start {
			return marks[i].Start < marks[j].Start
		}
		return marks[i].End < marks[j].End
	})
	return marks, true
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

// ---- skipped ranges ---------------------------------------------------------------------

type rng struct{ a, b int }

var (
	privRe    = regexp.MustCompile(`(?s)%%.*?%%`)
	codeRe    = regexp.MustCompile("`[^`\n]*`")
	urlRe     = regexp.MustCompile(`https?://[^\s)>\]]+`)
	wikiRe    = regexp.MustCompile(`\[\[[^\]\n]*\]\]`)
	linkURLRe = regexp.MustCompile(`\]\([^)\n]*\)`)
	tagRe     = regexp.MustCompile(`</?[A-Za-z][^>\n]*>`)
)

// skipRanges are the parts of the body no analyser looks at: ghosted text with
// its tags and private notes (and the rest of a paragraph after a lone %%), from
// text.HiddenSpans, and inline code, URLs, wiki links, link targets and inline
// tags. They are merged into sorted ranges that do not overlap, so a lookup is a
// binary search and a long range cannot be passed over.
func skipRanges(body string) []rng {
	var spans [][2]int
	spans = append(spans, text.HiddenSpans(body)...)
	for _, re := range []*regexp.Regexp{codeRe, urlRe, wikiRe, linkURLRe, tagRe} {
		for _, m := range re.FindAllStringIndex(body, -1) {
			spans = append(spans, [2]int{m[0], m[1]})
		}
	}
	spans = text.MergeSpans(spans)
	out := make([]rng, len(spans))
	for i, sp := range spans {
		out[i] = rng{sp[0], sp[1]}
	}
	return out
}

// overlaps reports whether [a,b) meets a skipped range. skip is merged: sorted,
// disjoint.
func overlaps(skip []rng, a, b int) bool {
	i := sort.Search(len(skip), func(i int) bool { return skip[i].b > a })
	return i < len(skip) && skip[i].a < b
}

// ---- sentences and words ---------------------------------------------------------------------

var abbreviations = map[string]bool{"mr": true, "mrs": true, "ms": true, "dr": true, "st": true, "prof": true, "sr": true, "jr": true, "vs": true, "etc": true,
	"e.g": true, "i.e": true, "mt": true, "gen": true, "col": true, "lt": true, "capt": true, "sgt": true, "fig": true, "approx": true}

// Sentences splits a paragraph into sentences: ranges (byte offsets into s,
// trimmed of white space) that end at . ! ? or … followed by white space and
// then a capital letter, a digit, an opening quote or the end. Abbreviations,
// initials and decimal numbers do not end one.
func Sentences(s string) [][2]int {
	var out [][2]int
	start := 0
	n := len(s)
	trim := func(a, b int) (int, int) {
		for a < b && isSpace(s[a]) {
			a++
		}
		for b > a && isSpace(s[b-1]) {
			b--
		}
		return a, b
	}
	i := 0
	for i < n {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r != '.' && r != '!' && r != '?' && r != '…' {
			i += w
			continue
		}
		j := i + w
		for j < n { // the run of terminators and closing marks after it
			r2, w2 := utf8.DecodeRuneInString(s[j:])
			if r2 == '.' || r2 == '!' || r2 == '?' || r2 == '…' || r2 == '"' || r2 == '”' || r2 == '’' || r2 == '\'' || r2 == ')' || r2 == ']' || r2 == '»' || r2 == '*' || r2 == '_' {
				j += w2
				continue
			}
			break
		}
		end := j
		k := j
		for k < n && isSpace(s[k]) {
			k++
		}
		boundary := k >= n
		if !boundary && k > j {
			nr, _ := utf8.DecodeRuneInString(s[k:])
			boundary = unicode.IsUpper(nr) || unicode.IsDigit(nr) || nr == '"' || nr == '“' || nr == '‘' || nr == '\'' || nr == '(' || nr == '«' || nr == '—' || nr == '*' || nr == '_'
		}
		if boundary && r == '.' && abbrevBefore(s, i) {
			boundary = false
		}
		if boundary {
			if a, b := trim(start, end); b > a {
				out = append(out, [2]int{a, b})
			}
			start = k
		}
		i = j
	}
	if a, b := trim(start, n); b > a {
		out = append(out, [2]int{a, b})
	}
	return out
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// abbrevBefore reports whether the period at s[i] ends an abbreviation, an
// initial or a number rather than a sentence.
func abbrevBefore(s string, i int) bool {
	j := i
	for j > 0 && (isLetterByte(s[j-1]) || s[j-1] == '.') {
		j--
	}
	word := strings.ToLower(strings.TrimRight(s[j:i], "."))
	if abbreviations[word] {
		return true
	}
	if utf8.RuneCountInString(word) == 1 && unicode.IsUpper(rune(s[j])) { // an initial
		return true
	}
	return false
}

func isLetterByte(c byte) bool { return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= 0x80 }

// WordsIn counts the words of s: runs between white space that hold a letter or
// a digit.
func WordsIn(s string) int {
	n := 0
	for _, f := range strings.Fields(s) {
		for _, r := range f {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				n++
				break
			}
		}
	}
	return n
}

// ---- punctuation ---------------------------------------------------------------------------

var (
	multiSpace  = regexp.MustCompile(` {2,}`)
	repeatMark  = regexp.MustCompile(`,,+|;;+|::+|(?:[^.]|^)\.\.(?:[^.]|$)`)
	spaceBefore = regexp.MustCompile(`([\p{L}\p{N}"”’')\]]) +([,.;:!?])(?:[^.\p{L}\p{N}]|$| )`)
)

func punctuation(body string, a, b int, skip []rng, french bool, straight, curly int, add func(Mark)) {
	s := body[a:b]
	fix := func(kind string, from, to int, with, why string) {
		if overlaps(skip, a+from, a+to) {
			return
		}
		w := with
		add(Mark{Kind: kind, Start: a + from, End: a + to, Reason: why, Fix: &w})
	}
	for _, m := range multiSpace.FindAllStringIndex(s, -1) {
		// interior runs only: a run at the start is an indent, one before a line
		// break is Markdown's hard break
		if m[0] == 0 || m[1] >= len(s) || s[m[0]-1] == '\n' || s[m[1]] == '\n' {
			continue
		}
		fix(KindPunct, m[0], m[1], " ", "extra spaces")
	}
	for _, m := range repeatMark.FindAllStringIndex(s, -1) {
		seg := s[m[0]:m[1]]
		from, to := m[0], m[1]
		// trim the one character of context the pattern may have taken on each side
		if strings.HasPrefix(seg, ",,") || strings.HasPrefix(seg, ";;") || strings.HasPrefix(seg, "::") {
			fix(KindPunct, from, to, seg[:1], "repeated punctuation")
			continue
		}
		if i := strings.Index(seg, ".."); i >= 0 {
			fix(KindPunct, from+i, from+i+2, ".", "two dots; an ellipsis is three")
		}
	}
	for _, m := range spaceBefore.FindAllStringSubmatchIndex(s, -1) {
		p := s[m[4]:m[5]]
		if french && strings.Contains(";:!?", p) {
			continue
		}
		// the spaces run from the end of the first group to the start of the second
		fix(KindPunct, m[3], m[4], "", "space before "+p)
	}
	quotes(s, straight, curly, fix)
}

// quotes marks the minority form when a text uses both straight and curly
// quotation marks: the text should use one kind. The fix is the exact character
// in the majority form, chosen by what is next to it.
func quotes(s string, straight, curly int, fix func(kind string, from, to int, with, why string)) {
	if straight == 0 || curly == 0 {
		return
	}
	for i, r := range s {
		prevOpen := i == 0
		if i > 0 {
			pr, _ := utf8.DecodeLastRuneInString(s[:i])
			prevOpen = unicode.IsSpace(pr) || strings.ContainsRune("([{—–“‘", pr)
		}
		switch {
		case r == '"' && curly >= straight:
			if prevOpen {
				fix(KindPunct, i, i+1, "“", "straight quotation mark among curly ones")
			} else {
				fix(KindPunct, i, i+1, "”", "straight quotation mark among curly ones")
			}
		case (r == '“' || r == '”') && straight > curly:
			fix(KindPunct, i, i+utf8.RuneLen(r), `"`, "curly quotation mark among straight ones")
		}
	}
}
