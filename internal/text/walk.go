package text

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/util"
)

// The walker turns a goldmark tree into the text of each textblock (paragraph
// or heading), the way the editor's document model holds it. It is the single
// definition behind WordCount and SearchBody.
//
// What the editor counts (web/editor/src/count.js): the text of paragraphs and
// headings, except text inside a %%private note%%. Code blocks and source
// blocks (raw HTML, tables, front matter, `:::` blocks, link reference
// definitions) are skipped whole. Inline code is text and counts. A wiki link
// counts as its shown text. A hard break separates words; images, footnote
// references and inline HTML are zero-width, so the words either side of one
// run together.

type kind uint8

const (
	kText    kind = iota // plain text; may hold %% delimiters
	kSpecial             // an escape, an entity, or a code span: text, never a delimiter
	kSoft                // soft line break: a space
	kHard                // hard line break: a space, even inside a private note
	kWiki                // wiki link: its shown text, even inside a private note
	kAtom                // zero-width: image, footnote reference, inline HTML, a mark boundary
)

type tok struct {
	kind    kind
	s       string
	private bool
	target  string // wiki links: the title the link points at
	alias   string // wiki links: the text after the bar, if any
	// raw is the source of an inline HTML tag (a kAtom), so the ghost markers can
	// be told from any other tag.
	raw string
	// ghost marks a token between a pair of ghost markers (and the markers).
	ghost bool
	// src and srcLen say where the token came from in the parsed source: src
	// is the byte offset plus one (0 means unknown) and srcLen the bytes it
	// covers there. Only the mention scanner reads them; counting never does.
	src, srcLen int
}

// Walk calls fn with the tokens of every textblock in the document, in order.
func walkBlocks(src []byte, fn func([]tok)) {
	walkBlocksNodes(src, fn, nil)
}

// walkBlocksNodes is walkBlocks that also reports the AST node of each block.
// The node is nil when the parser failed and the text is walked as one block.
func walkBlocksNodes(src []byte, fn func([]tok), blockFn func(ast.Node, []tok)) {
	doc, ok := safeParse(src)
	if !ok {
		// The parser failed on this text. A save must never fail with it, so
		// fall back to the whole text as one block of prose (private notes
		// still removed): a rough count rather than none.
		toks := pairPrivate([]tok{{kind: kText, s: string(src), src: 1, srcLen: len(src)}})
		fn(toks)
		if blockFn != nil {
			blockFn(nil, toks)
		}
		return
	}
	w := &walker{src: src, fn: fn, blockFn: blockFn}
	w.block(doc, false)
}

func safeParse(src []byte) (doc ast.Node, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			doc, ok = nil, false
		}
	}()
	return parseDoc(src), true
}

type walker struct {
	src      []byte
	fn       func([]tok)
	blockFn  func(ast.Node, []tok) // optional: also told which node the tokens came from
	lastStop int                   // end of the previous text segment, to join contiguous runs
}

func (w *walker) block(n ast.Node, firstOfItem bool) {
	switch n.Kind() {
	case ast.KindFencedCodeBlock, ast.KindCodeBlock, ast.KindHTMLBlock, ast.KindThematicBreak, kindDirective:
		return
	case ast.KindParagraph, ast.KindTextBlock, ast.KindHeading:
		toks := w.inlineTokens(n)
		toks = pairPrivate(toks)
		toks = markGhosts(toks)
		// Only a paragraph opens a task item; a heading that starts one does not.
		if firstOfItem && n.Kind() != ast.KindHeading {
			toks = taskMarker(toks)
		}
		w.fn(toks)
		if w.blockFn != nil {
			w.blockFn(n, toks)
		}
		return
	case ast.KindListItem:
		for c, i := n.FirstChild(), 0; c != nil; c, i = c.NextSibling(), i+1 {
			w.block(c, i == 0)
		}
		return
	}
	if n.Type() == ast.TypeInline {
		return
	}
	if n.Kind().String() == "Table" || n.Kind().String() == "TableRow" {
		return // tables are source blocks
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.block(c, false)
	}
}

// inlineTokens flattens the inline children of a textblock.
func (w *walker) inlineTokens(n ast.Node) []tok {
	var out []tok
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.inline(c, &out)
	}
	return out
}

func (w *walker) inline(n ast.Node, out *[]tok) {
	switch v := n.(type) {
	case *ast.Text:
		// goldmark splits plain text at brackets and delimiters that turned out
		// to be nothing; the editor's text token is the whole run.
		n0 := len(*out)
		w.text(v.Segment.Value(w.src), v.Segment.Start, out)
		if n0 > 0 && len(*out) > n0 && (*out)[n0-1].kind == kText && (*out)[n0].kind == kText && w.lastStop == v.Segment.Start {
			(*out)[n0-1].s += (*out)[n0].s
			(*out)[n0-1].srcLen += (*out)[n0].srcLen
			*out = append((*out)[:n0], (*out)[n0+1:]...)
		}
		w.lastStop = v.Segment.Stop
		switch {
		case v.HardLineBreak():
			*out = append(*out, tok{kind: kHard})
		case v.SoftLineBreak():
			*out = append(*out, tok{kind: kSoft})
		}
	case *ast.String:
		*out = append(*out, tok{kind: kSpecial, s: string(v.Value)})
	case *ast.CodeSpan:
		var b strings.Builder
		first, last := -1, 0
		for c := v.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				b.Write(t.Segment.Value(w.src))
				if first < 0 {
					first = t.Segment.Start
				}
				last = t.Segment.Stop
			}
		}
		t := tok{kind: kSpecial, s: codeSpanText(b.String())}
		if first >= 0 {
			t.src, t.srcLen = first+1, last-first
		}
		*out = append(*out, t)
	case *ast.AutoLink:
		*out = append(*out, tok{kind: kAtom}, tok{kind: kText, s: string(v.Label(w.src))}, tok{kind: kAtom})
	case *ast.RawHTML:
		t := tok{kind: kAtom}
		if v.Segments.Len() > 0 {
			first := v.Segments.At(0)
			t.src = first.Start + 1
			var raw []byte
			for i := 0; i < v.Segments.Len(); i++ {
				seg := v.Segments.At(i)
				raw = append(raw, seg.Value(w.src)...)
			}
			t.raw, t.srcLen = string(raw), len(raw)
		}
		*out = append(*out, t)
	case *ast.Image, *footnoteRef:
		*out = append(*out, tok{kind: kAtom})
	case *rawText:
		w.text(v.v, -1, out)
	case *wikiLink:
		*out = append(*out, tok{kind: kWiki, s: v.label, target: v.target, alias: v.alias, src: v.at + 1, srcLen: v.n})
	default: // emphasis, links, literal groups: their text, between two boundaries
		*out = append(*out, tok{kind: kAtom})
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			w.inline(c, out)
		}
		*out = append(*out, tok{kind: kAtom})
	}
}

// text splits raw inline text into plain runs and decoded escapes/entities.
// base is the source offset of raw[0], or -1 when it is not known.
func (w *walker) text(raw []byte, base int, out *[]tok) {
	start := 0
	at := func(i int) int {
		if base < 0 {
			return 0
		}
		return base + i + 1
	}
	flush := func(to int) {
		if to > start {
			*out = append(*out, tok{kind: kText, s: string(raw[start:to]), src: at(start), srcLen: to - start})
		}
	}
	for i := 0; i < len(raw); {
		switch c := raw[i]; {
		case c == '\\' && i+1 < len(raw) && util.IsPunct(raw[i+1]):
			flush(i)
			*out = append(*out, tok{kind: kSpecial, s: string(raw[i+1 : i+2]), src: at(i), srcLen: 2})
			i += 2
			start = i
		case c == '&':
			if dec, n := entity(raw[i:]); n > 0 {
				flush(i)
				*out = append(*out, tok{kind: kSpecial, s: dec, src: at(i), srcLen: n})
				i += n
				start = i
			} else {
				i++
			}
		default:
			i++
		}
	}
	flush(len(raw))
}

var reEntity = regexp.MustCompile(`^&(?:#[xX]([0-9a-fA-F]{1,6})|#([0-9]{1,7})|([A-Za-z][A-Za-z0-9]{1,31}));`)

// entity decodes a character reference at the start of b, like markdown-it:
// a known name or a number. It returns the text and the bytes consumed, or 0.
func entity(b []byte) (string, int) {
	m := reEntity.FindSubmatch(b)
	if m == nil {
		return "", 0
	}
	switch {
	case m[1] != nil:
		code, _ := strconv.ParseUint(string(m[1]), 16, 32)
		return codePoint(code), len(m[0])
	case m[2] != nil:
		code, _ := strconv.ParseUint(string(m[2]), 10, 32)
		return codePoint(code), len(m[0])
	}
	if e, ok := util.LookUpHTML5EntityByName(string(m[3])); ok {
		return string(e.Characters), len(m[0])
	}
	return "", 0
}

// codePoint is markdown-it's isValidEntityCode: anything outside the usable
// range becomes the replacement character.
func codePoint(c uint64) string {
	switch {
	case c >= 0xD800 && c <= 0xDFFF, c >= 0xFDD0 && c <= 0xFDEF, c&0xFFFF == 0xFFFF || c&0xFFFF == 0xFFFE,
		c <= 8, c == 0xB, c >= 0xE && c <= 0x1F, c >= 0x7F && c <= 0x9F, c > 0x10FFFF:
		return "�"
	}
	return string(rune(c))
}

// codeSpanText is the text of a code span: line endings are spaces. (goldmark
// has already dropped the one space either side that a span may be padded with,
// the same rule the editor applies.)
func codeSpanText(s string) string {
	return strings.ReplaceAll(s, "\n", " ")
}

// pairPrivate finds the %%private notes%% of one textblock and marks the
// tokens between each pair. It is a literal port of the editor's privateNotes
// rule: delimiters are looked for inside plain text tokens only, taken in
// order and paired two by two; an empty pair (%%%%) and an unpaired last
// delimiter stay as literal text.
func pairPrivate(toks []tok) []tok {
	type hit struct{ tok, at int }
	var hits []hit
	for i, t := range toks {
		if t.kind != kText || !strings.Contains(t.s, "%%") {
			continue
		}
		for from := 0; ; {
			at := strings.Index(t.s[from:], "%%")
			if at < 0 {
				break
			}
			hits = append(hits, hit{i, from + at})
			from += at + 2
		}
	}
	if len(hits) < 2 {
		return toks
	}
	var used []hit
	for h := 0; h+1 < len(hits); {
		a, b := hits[h], hits[h+1]
		if a.tok == b.tok && b.at == a.at+2 {
			h += 2
			continue
		}
		used = append(used, a, b)
		h += 2
	}
	if len(used) == 0 {
		return toks
	}
	byTok := map[int][]int{} // token index -> offsets of delimiters used
	for _, u := range used {
		byTok[u.tok] = append(byTok[u.tok], u.at)
	}
	out := make([]tok, 0, len(toks)+len(used))
	inNote := false
	n := 0 // delimiters seen; even ones open a note
	for i, t := range toks {
		offs, ok := byTok[i]
		if !ok {
			t.private = inNote
			out = append(out, t)
			continue
		}
		pos := 0
		for _, at := range offs {
			if at > pos {
				out = append(out, tok{kind: kText, s: t.s[pos:at], private: inNote, src: shiftSrc(t, pos), srcLen: at - pos})
			}
			inNote = n%2 == 0
			n++
			out = append(out, tok{kind: kAtom, private: inNote})
			pos = at + 2
		}
		if pos < len(t.s) {
			out = append(out, tok{kind: kText, s: t.s[pos:], private: inNote, src: shiftSrc(t, pos), srcLen: len(t.s) - pos})
		}
	}
	return out
}

// shiftSrc is the source position of byte pos of a plain-text token.
func shiftSrc(t tok, pos int) int {
	if t.src == 0 {
		return 0
	}
	return t.src + pos
}

var (
	reTaskText = regexp.MustCompile(`^\[([ xX])\][ \t]+`)
	reTaskBare = regexp.MustCompile(`^\[[ xX]\][ \t]*$`)
)

// taskMarker drops the `[ ]` or `[x]` that opens a task item, as the editor
// does after parsing. A bare marker with nothing after it stays literal text.
func taskMarker(toks []tok) []tok {
	if len(toks) == 0 || toks[0].kind != kText {
		return toks
	}
	first := toks[0].s
	cut := 0
	if m := reTaskText.FindString(first); m != "" {
		// `[ ]` and spaces, then something that is not whitespace.
		if r, _ := utf8.DecodeRuneInString(first[len(m):]); len(first) > len(m) && !isJSSpace(r) {
			cut = len(m)
		}
	}
	if cut == 0 {
		if !reTaskBare.MatchString(first) {
			return toks
		}
		cut = len(first)
	}
	rest := first[cut:]
	if !hasNonSpace(rest) && len(toks) == 1 {
		return toks
	}
	if rest == "" {
		return toks[1:]
	}
	out := append([]tok(nil), toks...)
	out[0].s = rest
	out[0].src = shiftSrc(out[0], cut)
	out[0].srcLen -= cut
	return out
}

func hasNonSpace(s string) bool {
	for _, r := range s {
		if !isJSSpace(r) {
			return true
		}
	}
	return false
}

var _ = ast.KindDocument
