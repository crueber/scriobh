package text

import (
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Whole-word matching of names and aliases in prose. This is the engine behind
// the mention index: one Matcher is built from a project's vocabulary and run
// over the plain text of each document (the stored search text) and, for
// highlighting, over the Markdown itself (Spans).
//
// The rules, in one place:
//
//   - Words. A word is a run of letters, digits and combining marks. A single
//     hyphen or apostrophe (ASCII, U+2010, U+2011, U+2019) between two word
//     characters joins them into one word ("Jean-Luc", "O'Neill"), so "Mary"
//     is not found inside "Mary-Anne". A zero-width non-joiner or joiner
//     between two word characters (Persian "می‌روم") does not split a word and
//     is ignored when comparing. Everything else separates words. Han,
//     Hiragana, Katakana and Thai have no spaces, so each character is a word.
//   - Folding. Each word is put in Unicode normal form C and given the full
//     case fold, the same fold as FoldTitle, so "STRASSE" matches "Straße".
//     Diacritics are significant: "Maire" is not "Máire". One deliberate
//     difference from FoldTitle: Turkish dotted and dotless I are not told
//     apart (İ, I, ı and i all fold to i), so "IŞIK" finds "Işık" whichever
//     way the author types it. The cost is that two names differing only in
//     those letters match the same text; the language is not known here, and a
//     missed Turkish name is worse than that coincidence.
//   - Endings that are not part of a name. A word ending in 's (or ’s) also
//     matches without it, as the last word of a name: "Máire's" finds "Máire".
//     A Hangul word ending in a particle (은 는 이 가 을 를 의 에 도 와 과 로
//     으로 에서) also matches without it ("민수가" finds "민수"), as the last
//     word of a name, but only when what is left is two or more syllables, so
//     "나이" (age) is not found as "나".
//   - Elision at the start of a word. A word that begins with one of the
//     elided French and Italian forms l' d' j' m' n' s' t' c' qu' jusqu'
//     lorsqu' puisqu' dell' all' nell' sull' dall' coll' un' (with ' or ’) also
//     matches without it, as the first word of a name: "d'Arthur" and
//     "dell'Alba" find Arthur and Alba. The match starts after the apostrophe.
//   - Longest match wins, scanning left to right, and matches do not overlap.
//     When two phrases are equally long and equal, the lower Rank wins and the
//     other is reported as a candidate (an ambiguity), never dropped silently.
//   - A name written with an initial capital matches only text that does not
//     start with a lower-case letter, so a character called Will is not found
//     in every "will". Names written in lower case, and scripts without case,
//     match any text. Explicit links (LookupText) ignore the guard.
//   - A name of one Han, Kana or Thai character matches only where that
//     character stands alone, with no letter touching it on either side (a
//     space or punctuation, or the ends of the text); inside a compound word
//     it is just a syllable. Names of two or more characters are not limited.
//     Explicit links ignore the rule.
//   - A line break ends a match: names do not run across paragraphs. (Plain
//     text from SearchBody has one line per paragraph; a soft break inside a
//     paragraph is a space already.)
//   - A bare web address (http://, https:// or www. up to the next space) is
//     not prose and is skipped.

// MaxPhraseWords is the longest phrase the matcher keeps: a name or alias of
// more words is truncated to its first MaxPhraseWords words for matching (the
// entity ops refuse such aliases, but older data may hold one). It bounds both
// the trie's depth and the work done at every word of every text.
const MaxPhraseWords = 12

// Phrase is one name or alias to look for.
type Phrase struct {
	Text  string // as written
	Ref   int    // the caller's handle for what it names
	Rank  int    // precedence; lower wins when two refs share the phrase
	Alias bool   // an alias rather than a name
}

// Candidate is one thing a phrase can mean.
type Candidate struct {
	Ref     int
	Rank    int
	Alias   bool
	NeedCap bool
	// Lone marks a name of a single Han, Kana or Thai character, which matches
	// only where nothing else touches it.
	Lone bool
}

// Match is one place a phrase was found.
type Match struct {
	Start, End int         // byte offsets in the scanned text
	Words      int         // how many words long
	Cands      []Candidate // winner first; distinct Refs; already past the capital guard
}

type node struct {
	next  map[string]int32
	cands []Candidate // sorted by Rank, then Ref; non-nil marks a phrase end
}

// Matcher finds phrases. It is read-only after NewMatcher and safe for
// concurrent use.
type Matcher struct {
	nodes []node
	n     int // phrases that made it in
}

// NewMatcher builds a matcher. A phrase with no word in it is skipped.
func NewMatcher(phrases []Phrase) *Matcher {
	m := &Matcher{nodes: make([]node, 1, 64)}
	for _, p := range phrases {
		var keys []string
		var first rune
		tokenize(p.Text, func(w word) {
			if len(keys) >= MaxPhraseWords {
				return
			}
			keys = append(keys, w.key)
			if len(keys) == 1 {
				first = w.first
			}
		})
		if len(keys) == 0 {
			continue
		}
		at := int32(0)
		for _, k := range keys {
			nx, ok := m.nodes[at].next[k]
			if !ok {
				nx = int32(len(m.nodes))
				m.nodes = append(m.nodes, node{})
				if m.nodes[at].next == nil {
					m.nodes[at].next = map[string]int32{}
				}
				m.nodes[at].next[k] = nx
			}
			at = nx
		}
		c := Candidate{Ref: p.Ref, Rank: p.Rank, Alias: p.Alias, NeedCap: unicode.IsUpper(first) || unicode.IsTitle(first),
			Lone: len(keys) == 1 && isUnspaced(first)}
		list := m.nodes[at].cands
		dup := false
		for i := range list {
			if list[i].Ref == c.Ref {
				dup = true
				if list[i].Alias && !c.Alias { // a name beats an alias of the same entity
					list[i] = c
				}
			}
		}
		if !dup {
			list = append(list, c)
			m.n++
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].Rank != list[j].Rank {
				return list[i].Rank < list[j].Rank
			}
			return list[i].Ref < list[j].Ref
		})
		m.nodes[at].cands = list
	}
	return m
}

// Len is how many phrases the matcher holds.
func (m *Matcher) Len() int { return m.n }

// Empty reports whether the matcher can find nothing.
func (m *Matcher) Empty() bool { return m == nil || m.n == 0 }

// LookupText resolves a whole string (the target of a wiki link) to the
// candidates of the phrase it spells, ignoring case, spacing and punctuation
// the way matching does. The capital guard and the single-character rule do
// not apply.
func (m *Matcher) LookupText(s string) []Candidate {
	if m.Empty() {
		return nil
	}
	at := int32(0)
	words := 0
	ok := true
	tokenize(s, func(w word) {
		if !ok {
			return
		}
		nx, found := m.nodes[at].next[w.key]
		if !found {
			ok = false
			return
		}
		at = nx
		words++
	})
	if !ok || words == 0 {
		return nil
	}
	return m.nodes[at].cands
}

// Find calls fn for each match in s, in order. It reads the text through a
// small window of words, so memory does not grow with the length of s.
func (m *Matcher) Find(s string, fn func(Match)) {
	if m.Empty() {
		return
	}
	sc := scanner{s: s}
	var win []word
	head := 0
	done := false
	const compactAt = 4096
	for {
		for len(win)-head < MaxPhraseWords+1 && !done {
			w, ok := sc.next()
			if !ok {
				done = true
				break
			}
			win = append(win, w)
		}
		if head >= len(win) {
			return
		}
		best, last, start, end := m.longest(win[head:])
		if best == nil {
			head++
		} else {
			fn(Match{Start: start, End: end, Words: last + 1, Cands: best})
			head += last + 1
		}
		if head >= compactAt {
			win = append(win[:0], win[head:]...)
			head = 0
		}
	}
}

// longest returns the candidates of the longest phrase starting at ws[0] that
// passes the guards, the index of its last word, and the byte offsets where
// the match starts and ends. A word with an elided prefix is tried both whole
// and without the prefix, and the longer match wins (the whole word on a tie).
func (m *Matcher) longest(ws []word) (best []Candidate, last, start, end int) {
	w0 := ws[0]
	best, last, end = m.walk(ws, w0.key, w0.alt, w0.stemEnd, w0.first)
	start = w0.start
	if w0.pre != "" {
		if c, l, e := m.walk(ws, w0.pre, w0.preAlt, w0.stemEnd, w0.preFirst); c != nil && (best == nil || l > last) {
			best, last, start, end = c, l, w0.preStart, e
		}
	}
	return best, last, start, end
}

// walk follows the trie from the first word (given by its key variants) and on
// through the following words. Later words are always longer, so a later hit
// simply replaces an earlier one, and the full form (tried second) replaces the
// possessive or particle form at the same length.
func (m *Matcher) walk(ws []word, key, alt string, stemEnd int, first rune) (best []Candidate, bestEnd, endByte int) {
	bestEnd = -1
	refused := -1 // the last word of a longer name the text spells, which a guard refuses
	at := int32(0)
	edge := ws[0].prevWord || ws[0].nextWord
	for j := 0; j < len(ws); j++ {
		if j > 0 && ws[j].brk {
			break
		}
		k, a, se := key, alt, stemEnd
		if j > 0 {
			k, a, se = ws[j].key, ws[j].alt, ws[j].stemEnd
		}
		if a != "" {
			if nx, ok := m.nodes[at].next[a]; ok {
				if c := m.guard(m.nodes[nx].cands, first, j == 0 && edge); c != nil {
					best, bestEnd, endByte = c, j, se
				} else if len(m.nodes[nx].cands) > 0 {
					refused = j
				}
			}
		}
		nx, ok := m.nodes[at].next[k]
		if !ok {
			break
		}
		at = nx
		if c := m.guard(m.nodes[at].cands, first, j == 0 && edge); c != nil {
			best, bestEnd, endByte = c, j, ws[j].end
		} else if len(m.nodes[at].cands) > 0 {
			refused = j
		}
	}
	// A longer name the text spells out, refused by the capital or lone-character rule, takes its words with it: a shorter
	// name inside those words is not linked either ("the Guild" is not found in "the Guild of Lamplighters" when that name is
	// capitalised and the text is not). The longest name wins, and a refused longest name is no name at all for this text.
	if refused > bestEnd {
		return nil, -1, 0
	}
	return best, bestEnd, endByte
}

// guard applies the capital rule, and the rule for single-character names, to
// the candidates of a phrase whose first character is first. touched says the
// phrase is one word with a letter against it on a side.
func (m *Matcher) guard(cs []Candidate, first rune, touched bool) []Candidate {
	if len(cs) == 0 {
		return nil
	}
	lower := unicode.IsLower(first)
	var out []Candidate
	for _, c := range cs {
		if (lower && c.NeedCap) || (touched && c.Lone) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ---- words ------------------------------------------------------------------------------------------------------

type word struct {
	start, end int    // byte offsets in the scanned string
	key        string // folded
	alt        string // folded without a possessive or particle ending; "" when there is none
	stemEnd    int    // byte offset where the word ends without that ending
	// An elided prefix (l', d', dell', ...): pre is the folded rest of the word
	// and preStart where it begins; preAlt is pre without a possessive.
	pre, preAlt string
	preStart    int
	preFirst    rune
	first       rune // the first character as written, for the capital guard
	brk         bool // a line break comes before this word
	// A letter touches the word's start or end: matters only to the rule for
	// names of a single Han, Kana or Thai character.
	prevWord, nextWord bool
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsNumber(r) || unicode.IsMark(r)
}

// isUnspaced reports scripts written without spaces, where every character is
// a word of its own.
func isUnspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai)
}

func isJoiner(r rune) bool {
	switch r {
	case '-', '\'', '’', '‐', '‑':
		return true
	}
	return false
}

// isZW is a zero-width non-joiner or joiner: it sits inside a word without
// splitting it.
func isZW(r rune) bool { return r == '‌' || r == '‍' }

// scanner reads words one at a time.
type scanner struct {
	s   string
	i   int
	brk bool
}

// tokenize calls fn for each word of s.
func tokenize(s string, fn func(word)) {
	sc := scanner{s: s}
	for {
		w, ok := sc.next()
		if !ok {
			return
		}
		fn(w)
	}
}

// urlAt reports whether a bare web address starts at s[i:].
func urlAt(s string, i int) bool {
	rest := s[i:]
	has := func(p string) bool { return len(rest) > len(p) && strings.EqualFold(rest[:len(p)], p) }
	return has("http://") || has("https://") || (has("www.") && isWordRune(rune(rest[4])))
}

func (sc *scanner) next() (word, bool) {
	s := sc.s
	for sc.i < len(s) {
		r, size := utf8.DecodeRuneInString(s[sc.i:])
		if !isWordRune(r) {
			if r == '\n' || r == '\r' || r == ' ' || r == ' ' {
				sc.brk = true
			}
			sc.i += size
			continue
		}
		if urlAt(s, sc.i) { // not prose: skip to the next space
			for sc.i < len(s) {
				r2, s2 := utf8.DecodeRuneInString(s[sc.i:])
				if unicode.IsSpace(r2) {
					break
				}
				sc.i += s2
			}
			continue
		}
		start := sc.i
		first := r
		sc.i += size
		if isUnspaced(r) {
			// Combining marks stay with their character.
			for sc.i < len(s) {
				r2, s2 := utf8.DecodeRuneInString(s[sc.i:])
				if !unicode.IsMark(r2) {
					break
				}
				sc.i += s2
			}
		} else {
			for sc.i < len(s) {
				r2, s2 := utf8.DecodeRuneInString(s[sc.i:])
				if isWordRune(r2) && !isUnspaced(r2) {
					sc.i += s2
					continue
				}
				if (isJoiner(r2) || isZW(r2)) && sc.i+s2 < len(s) {
					r3, _ := utf8.DecodeRuneInString(s[sc.i+s2:])
					if isWordRune(r3) && !isUnspaced(r3) {
						sc.i += s2
						continue
					}
				}
				break
			}
		}
		w := buildWord(s, start, sc.i, first, sc.brk)
		sc.brk = false
		return w, true
	}
	return word{}, false
}

// possessives are the endings of a word that are not part of the name.
var possessives = [...]string{"'s", "’s", "'S", "’S"}

// particles are the Hangul particles, longest first.
var particles = [...]string{"에서", "으로", "은", "는", "이", "가", "을", "를", "의", "에", "도", "와", "과", "로"}

// elisions are the elided forms that may begin a word, compared folded and
// without the apostrophe.
var elisions = map[string]bool{
	"l": true, "d": true, "j": true, "m": true, "n": true, "s": true, "t": true, "c": true, "qu": true,
	"jusqu": true, "lorsqu": true, "puisqu": true, "dell": true, "all": true, "nell": true, "sull": true, "dall": true, "coll": true, "un": true,
}

func isHangul(s string) bool {
	for _, r := range s {
		if r < 0xAC00 || r > 0xD7A3 {
			return false
		}
	}
	return s != ""
}

// stripEnding finds a possessive or a Hangul particle at the end of raw and
// returns the length of what is left, or -1.
func stripEnding(raw string) int {
	n := len(raw)
	if n > 2 {
		for _, suf := range possessives {
			if strings.HasSuffix(raw, suf) && n > len(suf) {
				return n - len(suf)
			}
		}
	}
	if isHangul(raw) {
		for _, p := range particles {
			if strings.HasSuffix(raw, p) && utf8.RuneCountInString(raw)-utf8.RuneCountInString(p) >= 2 {
				return n - len(p)
			}
		}
	}
	return -1
}

func keyOf(raw string) string {
	if strings.ContainsAny(raw, "‌‍") {
		raw = strings.NewReplacer("‌", "", "‍", "").Replace(raw)
	}
	return joinerKey(foldWord(raw))
}

func buildWord(s string, start, end int, first rune, brk bool) word {
	raw := s[start:end]
	w := word{start: start, end: end, first: first, brk: brk, key: keyOf(raw)}
	if cut := stripEnding(raw); cut > 0 {
		w.alt = keyOf(raw[:cut])
		w.stemEnd = start + cut
	}
	// Elision: l'Arthur, dell'Alba.
	if i := strings.IndexAny(raw, "'’"); i > 0 && elisions[foldWord(raw[:i])] {
		_, size := utf8.DecodeRuneInString(raw[i:])
		if rest := raw[i+size:]; rest != "" {
			w.preStart = start + i + size
			w.pre = keyOf(rest)
			w.preFirst, _ = utf8.DecodeRuneInString(rest)
			if cut := stripEnding(rest); cut > 0 {
				w.preAlt = keyOf(rest[:cut])
			}
		}
	}
	// A letter touching either end.
	if start > 0 {
		r, _ := utf8.DecodeLastRuneInString(s[:start])
		w.prevWord = isWordRune(r)
	}
	if end < len(s) {
		r, _ := utf8.DecodeRuneInString(s[end:])
		w.nextWord = isWordRune(r)
	}
	return w
}

// joinerKey makes every joiner inside a folded word the ASCII one it stands for.
func joinerKey(k string) string {
	if !strings.ContainsAny(k, "’‐‑") {
		return k
	}
	return strings.NewReplacer("’", "'", "‐", "-", "‑", "-").Replace(k)
}

var casers = sync.Pool{New: func() any { c := cases.Fold(); return &c }}

// foldWord is FoldTitle for one word (normal form C and the full case fold),
// except that Turkish dotted and dotless I are not told apart. ASCII is
// lower-cased without allocating a caser.
func foldWord(w string) string {
	ascii, lower := true, true
	for i := 0; i < len(w); i++ {
		c := w[i]
		if c >= utf8.RuneSelf {
			ascii = false
			break
		}
		if c >= 'A' && c <= 'Z' {
			lower = false
		}
	}
	if ascii {
		if lower {
			return w
		}
		return strings.ToLower(w)
	}
	cs := casers.Get().(*cases.Caser)
	cs.Reset()
	if !norm.NFC.IsNormalString(w) {
		w = norm.NFC.String(w)
	}
	out := cs.String(w)
	casers.Put(cs)
	if strings.ContainsAny(out, "ı̇") { // dotless i, and the dot the fold of İ leaves behind
		out = strings.NewReplacer("i̇", "i", "ı", "i").Replace(out)
	}
	if !norm.NFC.IsNormalString(out) {
		out = norm.NFC.String(out)
	}
	return out
}

// ---- spans in Markdown --------------------------------------------------------------------------------------------

// Span is one match located in the Markdown source.
type Span struct {
	Start, End int         // UTF-16 code units over the document with line endings normalised to LF
	Cands      []Candidate // as in Match
}

// piece maps a stretch of a block's text back to the source.
type piece struct {
	text, n   int // byte offset in the block text and its length
	src, srcN int // source byte offset + 1 (0 unknown) and length
	exact     bool
}

// Spans finds the matches of m in the prose of a Markdown document, the same
// prose the index reads (SearchBody: private notes, fenced and indented code
// and source blocks are not prose), and says where each one is in the source.
// Offsets count UTF-16 code units over the text with CRLF and CR normalised to
// LF, the measure doc.split takes.
//
// complete is false when some match could not be placed (text the parser
// rewrote, such as an entity or a refused link, is placed approximately only
// inside one token; a pathological document the parser had to sanitise is not
// placed at all). Spans then holds the ones that could be, and the caller
// should report the index's counts as the truth.
func Spans(md string, m *Matcher) (spans []Span, complete bool) {
	if m.Empty() {
		return nil, true
	}
	src := normalize(md)
	bodySrc := body(src)
	lead := len(src) - len(bodySrc)
	san := sanitize(bodySrc)
	if len(san) != len(bodySrc) {
		return nil, false
	}
	type raw struct {
		start, end int // byte offsets in src
		c          []Candidate
	}
	var found []raw
	complete = true
	walkBlocks(san, func(toks []tok) {
		var b strings.Builder
		var ps []piece
		add := func(t tok, s string, exact bool) {
			ps = append(ps, piece{text: b.Len(), n: len(s), src: t.src, srcN: t.srcLen, exact: exact})
			b.WriteString(s)
		}
		for _, t := range toks {
			if t.ghost {
				continue
			}
			switch t.kind {
			case kText:
				if !t.private {
					add(t, t.s, t.src > 0 && t.srcLen == len(t.s))
				}
			case kSpecial:
				if !t.private {
					add(t, t.s, false)
				}
			case kSoft:
				if !t.private {
					ps = append(ps, piece{text: b.Len(), n: 1})
					b.WriteByte(' ')
				}
			case kHard:
				ps = append(ps, piece{text: b.Len(), n: 1})
				b.WriteByte(' ')
			case kWiki:
				if !t.private {
					add(t, t.s, false)
				}
			}
		}
		if b.Len() == 0 {
			return
		}
		text := b.String()
		m.Find(text, func(h Match) {
			lo, ok1 := place(ps, h.Start, false)
			hi, ok2 := place(ps, h.End-1, true)
			if !ok1 || !ok2 || hi <= lo {
				complete = false
				return
			}
			found = append(found, raw{lead + lo, lead + hi, h.Cands})
		})
	})
	// Bytes to UTF-16 units, in one pass: the matches are in source order.
	sort.SliceStable(found, func(i, j int) bool { return found[i].start < found[j].start })
	units, pos := 0, 0
	conv := func(at int) int {
		for pos < at && pos < len(src) {
			r, size := utf8.DecodeRune(src[pos:])
			pos += size
			if r >= 0x10000 {
				units += 2
			} else {
				units++
			}
		}
		return units
	}
	for _, f := range found {
		s := conv(f.start)
		e := conv(f.end)
		spans = append(spans, Span{Start: s, End: e, Cands: f.c})
	}
	return spans, complete
}

// place maps byte offset at of a block's text to the source. last asks for the
// end of the character at that offset instead of its start.
func place(ps []piece, at int, last bool) (int, bool) {
	i := sort.Search(len(ps), func(i int) bool { return ps[i].text+ps[i].n > at })
	if i >= len(ps) {
		return 0, false
	}
	p := ps[i]
	if p.src == 0 {
		return 0, false
	}
	if p.exact {
		off := at - p.text
		if last {
			// the end of the character containing this byte
			return p.src - 1 + off + 1, true
		}
		return p.src - 1 + off, true
	}
	if last {
		return p.src - 1 + p.srcN, true
	}
	return p.src - 1, true
}

// SnippetAt is up to 160 characters of the line of s that holds [start, end),
// centred on it, with whitespace collapsed and an ellipsis where the line was
// cut. Offsets are bytes. It is what a mention shows as its context.
func SnippetAt(s string, start, end int) string {
	if start < 0 || end > len(s) || start > end {
		return ""
	}
	ls := strings.LastIndexByte(s[:start], '\n') + 1
	le := len(s)
	if i := strings.IndexByte(s[end:], '\n'); i >= 0 {
		le = end + i
	}
	line := []rune(s[ls:le])
	return stripMarkers(snippetAround(line, utf8.RuneCountInString(s[ls:start]), utf8.RuneCountInString(s[start:end])))
}
