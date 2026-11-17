package text

import (
	"bytes"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"
)

// Two things a reader of the rendered text needs from the Markdown, both built on the
// same parse the renderer and the counter use, so that what is looked for, and what is
// handed out, is exactly what a reader can see:
//
//   - PlainBlocks: the visible text of each paragraph and heading, whitespace as a
//     browser shows it, with a map from every byte of it back to the source. Front
//     matter, private notes, ghosted text, code, link and image targets, wiki-link
//     targets (a wiki link shows its label), raw HTML and footnote bodies are not in
//     it. A selection made on a rendered page is placed in the source through it, and
//     a phrase that exists only in what a reader cannot see is not found.
//   - CleanMarkdown: the Markdown with exactly what the reading view leaves out cut
//     out of it (private notes, HTML comments, front matter, footnotes referenced only
//     from a private note), and everything else, code included, left byte for byte.

// PlainBlock is the visible text of one block of prose.
type PlainBlock struct {
	// Text has runs of white space collapsed to one space and no space at either end.
	Text string
	// Start and End are the block's place in the source (first to last line).
	Start, End int
	lo, hi     []int32 // per byte of Text: the source bytes it came from; lo < 0 when unknown
}

// Source maps the bytes [i, j) of Text to the source range that holds them, or false
// when the text there has no known place (a space made of a line break at the
// edge, text a parser rewrote beyond placing).
func (b PlainBlock) Source(i, j int) (int, int, bool) {
	if i < 0 || j > len(b.Text) || i >= j || b.lo[i] < 0 || b.hi[j-1] < 0 {
		return 0, 0, false
	}
	return int(b.lo[i]), int(b.hi[j-1]), true
}

// Visible is the inverse of Source: the bytes [i, j) of Text that came from the source range [a, e), that is the
// first to the last visible byte whose own place in the source lies inside it. A range with nothing visible in it
// (an insertion point, or only markup) gives i == j at the place the next visible byte starts; ok is false when
// the block has no known place for that.
func (b PlainBlock) Visible(a, e int) (i, j int, ok bool) {
	i, j = -1, -1
	for k := 0; k < len(b.Text); k++ { // by byte: a range over the string would visit only the first byte of each letter
		if b.lo[k] < 0 {
			continue
		}
		if int(b.lo[k]) >= a && int(b.hi[k]) <= e {
			if i < 0 {
				i = k
			}
			j = k + 1
		}
	}
	if i >= 0 {
		return i, j, true
	}
	for k := 0; k < len(b.Text); k++ { // by byte: a range over the string would visit only the first byte of each letter // nothing inside: the point just before the first visible byte at or after a
		if b.lo[k] >= 0 && int(b.lo[k]) >= a {
			return k, k, true
		}
	}
	return len(b.Text), len(b.Text), a >= b.Start
}

func inFootnote(n ast.Node) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if _, ok := p.(*footnoteDef); ok {
			return true
		}
	}
	return false
}

// PlainBlocks lists the visible text of the prose of md, in order. Offsets are bytes of md with line
// endings as LF (as Blocks gives them). ok is false when the text could not be placed.
func PlainBlocks(md string) (blocks []PlainBlock, ok bool) {
	src := normalize(md)
	bs := body(src)
	san := sanitize(bs)
	if len(san) != len(bs) {
		return nil, false
	}
	lead := len(src) - len(bs)
	ok = true
	walkBlocksNodes(san, func([]tok) {}, func(n ast.Node, toks []tok) {
		if n == nil {
			ok = false
			return
		}
		if inFootnote(n) { // a footnote's body may be hidden with its note: never offered
			return
		}
		var text []byte
		var lo, hi []int32
		pendingSpace := false
		add := func(c byte, l, h int) {
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				pendingSpace = len(text) > 0
				return
			}
			if pendingSpace {
				text, lo, hi = append(text, ' '), append(lo, -1), append(hi, -1)
				pendingSpace = false
			}
			text, lo, hi = append(text, c), append(lo, int32(l)), append(hi, int32(h))
		}
		for _, t := range toks {
			if t.ghost || t.private {
				pendingSpace = len(text) > 0
				continue
			}
			switch t.kind {
			case kText:
				exact := t.src > 0 && t.srcLen == len(t.s)
				for k := 0; k < len(t.s); k++ {
					switch {
					case t.src == 0:
						add(t.s[k], -1, -1)
					case exact:
						add(t.s[k], lead+t.src-1+k, lead+t.src+k)
					default:
						add(t.s[k], lead+t.src-1, lead+t.src-1+t.srcLen)
					}
				}
			case kSpecial, kWiki:
				for k := 0; k < len(t.s); k++ {
					if t.src == 0 {
						add(t.s[k], -1, -1)
					} else {
						add(t.s[k], lead+t.src-1, lead+t.src-1+t.srcLen)
					}
				}
			case kSoft, kHard:
				pendingSpace = len(text) > 0
			}
		}
		if len(text) == 0 {
			return
		}
		pb := PlainBlock{Text: string(text), lo: lo, hi: hi}
		if lines := n.Lines(); lines != nil && lines.Len() > 0 {
			pb.Start, pb.End = lead+lines.At(0).Start, lead+lines.At(lines.Len()-1).Stop
		}
		blocks = append(blocks, pb)
	})
	return blocks, ok
}

// CleanMarkdown is md as it may be handed to a person who must not see what the reading view
// does not show: front matter, private notes (paired by the renderer's own rule), HTML comments and
// the footnotes that are referenced only from a private note are cut out; every other byte of the body
// stays, code blocks and spans included. ok is false when the text could not be placed (the caller
// must then hand out nothing: failing closed is the rule).
func CleanMarkdown(md string) (string, bool) { return CleanMarkdownHiding(md, nil) }

// CleanMarkdownHiding is CleanMarkdown for text whose private parts a caller has already cut with a rule of its own
// (an AI request cuts ghosted text and a note that is never closed before it gets here, so the renderer no longer sees
// the note that referenced a footnote). hidden names the footnote labels that were referenced from what the caller
// cut: a definition for one of them that nothing left refers to is cut as well, exactly as one referenced only from a
// private note is.
func CleanMarkdownHiding(md string, hidden map[string]bool) (string, bool) {
	src := normalize(md)
	bs := body(src)
	san := sanitize(bs)
	if len(san) != len(bs) {
		return "", false
	}
	doc, parsed := safeParseWith(san)
	if !parsed {
		return "", false
	}
	var cuts [][2]int
	hiddenRef := make(map[string]bool, len(hidden))
	for l := range hidden {
		hiddenRef[l] = true // seeded, so that the renderer does not number it as a definition nothing refers to
	}
	r := &htmlRenderer{src: san, w: &bytes.Buffer{}, refs: map[string]int{}, defs: map[string]*footnoteDef{}, hiddenRef: hiddenRef, hasPrivate: bytes.Contains(san, []byte("%%")), cuts: &cuts}
	r.findDefs(doc)
	r.children(doc)
	r.footnotes()
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *footnoteDef:
			if r.hiddenRef[v.label] && r.refs[v.label] == 0 {
				if a, b, ok := nodeExtent(v, san); ok {
					cuts = append(cuts, [2]int{a, b})
				}
			}
		case *ast.HTMLBlock:
			var raw strings.Builder
			for i := 0; i < v.Lines().Len(); i++ {
				ln := v.Lines().At(i)
				raw.Write(ln.Value(san))
			}
			if v.HasClosure() {
				raw.Write(v.ClosureLine.Value(san))
			}
			if isComment(strings.TrimRight(raw.String(), "\r\n")) && v.Lines().Len() > 0 {
				stop := v.Lines().At(v.Lines().Len() - 1).Stop
				if v.HasClosure() {
					stop = v.ClosureLine.Stop
				}
				cuts = append(cuts, [2]int{v.Lines().At(0).Start, stop})
			}
		case *ast.RawHTML:
			if v.Segments.Len() > 0 {
				var raw strings.Builder
				for i := 0; i < v.Segments.Len(); i++ {
					seg := v.Segments.At(i)
					raw.Write(seg.Value(san))
				}
				if strings.HasPrefix(raw.String(), "<!--") {
					cuts = append(cuts, [2]int{v.Segments.At(0).Start, v.Segments.At(v.Segments.Len() - 1).Stop})
				}
			}
		}
		return ast.WalkContinue, nil
	})
	sort.Slice(cuts, func(i, j int) bool { return cuts[i][0] < cuts[j][0] })
	var out bytes.Buffer
	at := 0
	for _, c := range cuts {
		if c[1] <= at {
			continue
		}
		if c[0] > at {
			out.Write(san[at:c[0]])
		}
		at = c[1]
	}
	out.Write(san[at:])
	return cutHiddenInRows(out.String()), true
}

// nodeExtent is the source range of a block that has no lines of its own (a footnote definition):
// from the start of the line its first child begins on, to the end of its last line.
func nodeExtent(n ast.Node, src []byte) (int, int, bool) {
	first, last := -1, -1
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && c != n && c.Type() != ast.TypeInline {
			if l := c.Lines(); l != nil && l.Len() > 0 {
				s, e := l.At(0).Start, l.At(l.Len()-1).Stop
				if first < 0 || s < first {
					first = s
				}
				if e > last {
					last = e
				}
			}
		}
		return ast.WalkContinue, nil
	})
	if first < 0 {
		return 0, 0, false
	}
	start := bytes.LastIndexByte(src[:first], '\n') + 1
	return start, last, true
}
