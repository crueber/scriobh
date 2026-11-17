package text

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
)

// Ghosts. A ghosted range is wrapped in a pair of inline HTML tags:
//
//	<scriobh-ghost>text that is dimmed</scriobh-ghost>
//
// The editor holds the tags as two inline source atoms, so the Markdown
// round-trips with no parser support. Everything between them (text, wiki
// links, hard breaks) is excluded from the word count, the search text and
// mention matching, and stays in the document and in the exports.
//
// Pairing is the private-note rule, per text block: scanning the block's
// tokens in order, the first opener starts a ghost and the next closer ends
// it; an opener met while one is open and a closer with none open are inert; an
// opener still open at the end of the block ghosts nothing. web/editor/src/
// count.js implements the same rule; a corpus test compares the two.

const (
	// GhostOpen and GhostClose are the markers, byte for byte.
	GhostOpen  = "<scriobh-ghost>"
	GhostClose = "</scriobh-ghost>"
)

// ghostPairs returns the token indexes of each pair of markers in one block.
func ghostPairs(toks []tok) [][2]int {
	var out [][2]int
	open := -1
	for i, t := range toks {
		if t.kind != kAtom {
			continue
		}
		switch t.raw {
		case GhostOpen:
			if open < 0 {
				open = i
			}
		case GhostClose:
			if open >= 0 {
				out = append(out, [2]int{open, i})
				open = -1
			}
		}
	}
	return out
}

// markGhosts sets ghost on every token of each pair, markers included.
func markGhosts(toks []tok) []tok {
	pairs := ghostPairs(toks)
	if len(pairs) == 0 {
		return toks
	}
	out := append([]tok(nil), toks...)
	for _, p := range pairs {
		for i := p[0]; i <= p[1]; i++ {
			out[i].ghost = true
		}
	}
	return out
}

// GhostSpan is one ghosted range in a body.
type GhostSpan struct {
	// OpenStart..OpenEnd and CloseStart..CloseEnd are the two markers; the
	// ghosted text is OpenEnd..CloseStart. Byte offsets in the body with line
	// endings normalised to LF.
	OpenStart, OpenEnd, CloseStart, CloseEnd int
	Words                                    int // words the range holds (what the count leaves out)
}

// Ghosts finds the ghosted ranges of a body, in order. ok is false when the
// text could not be placed (the parser had to sanitise it, or failed); the
// spans found so far are still returned.
func Ghosts(md string) (spans []GhostSpan, ok bool) {
	if !strings.Contains(md, GhostOpen) {
		return nil, true
	}
	src := normalize(md)
	san := sanitize(body(src))
	if len(san) != len(body(src)) {
		return nil, false
	}
	lead := len(src) - len(san)
	ok = true
	walkBlocksNodes(san, func([]tok) {}, func(_ ast.Node, toks []tok) {
		for _, p := range ghostPairs(toks) {
			o, c := toks[p[0]], toks[p[1]]
			if o.src == 0 || c.src == 0 {
				ok = false
				continue
			}
			sp := GhostSpan{OpenStart: lead + o.src - 1, OpenEnd: lead + o.src - 1 + o.srcLen,
				CloseStart: lead + c.src - 1, CloseEnd: lead + c.src - 1 + c.srcLen}
			var b strings.Builder
			for _, t := range toks[p[0]+1 : p[1]] {
				switch t.kind {
				case kText, kSpecial:
					if !t.private {
						b.WriteString(t.s)
					}
				case kSoft, kHard:
					b.WriteByte(' ')
				case kWiki:
					b.WriteString(t.s)
				}
			}
			words(b.String(), func(w string) {
				if isWord(w) {
					sp.Words++
				}
			})
			spans = append(spans, sp)
		}
	})
	return spans, ok
}

// StripGhosts returns md without the ghost markers; with omit, also without
// the ghosted text. Line endings are normalised to LF when something changed.
// A text with no ghosts is returned as it is.
func StripGhosts(md string, omit bool) string {
	return stripGhostsInRows(stripGhostsInBlocks(md, omit), omit)
}

func stripGhostsInBlocks(md string, omit bool) string {
	spans, ok := Ghosts(md)
	if !ok {
		// The text could not be placed (the parser had to sanitise a pathological
		// paragraph): fall back to the tags themselves, so a ghost is never left in.
		return stripGhostsByText(md, omit)
	}
	if len(spans) == 0 {
		return md
	}
	src := string(normalize(md))
	var b strings.Builder
	at := 0
	for _, sp := range spans {
		b.WriteString(src[at:sp.OpenStart])
		if !omit {
			b.WriteString(src[sp.OpenEnd:sp.CloseStart])
		}
		at = sp.CloseEnd
	}
	b.WriteString(src[at:])
	return b.String()
}

// reGhostText is a pair of tags with what is between them, not across a blank
// line (a ghost lies inside one paragraph).
var reGhostText = regexp.MustCompile(`<scriobh-ghost>(?:[^\n]|\n[^\n])*?</scriobh-ghost>`)

func stripGhostsByText(md string, omit bool) string {
	if !strings.Contains(md, GhostOpen) {
		return md
	}
	src := string(normalize(md))
	if omit {
		return reGhostText.ReplaceAllString(src, "")
	}
	return reGhostText.ReplaceAllStringFunc(src, func(m string) string {
		return m[len(GhostOpen) : len(m)-len(GhostClose)]
	})
}

// Block is one text block (a paragraph or a heading) of a body, for the code
// that checks where an alternative or a ghost may sit.
type Block struct {
	// Start and End are byte offsets of the block's text in the body with LF
	// line endings: the first character of its first line to the end of its
	// last line, without the line break.
	Start, End int
	Heading    bool
	// Top is true when the block is not inside a quote, a list or a footnote,
	// so its lines start at the left edge and it is its own run of lines.
	Top bool
	// Lines are the byte ranges of each source line's text, in order.
	Lines [][2]int
	// Contiguous is true when only white space lies between consecutive lines
	// (no quote or list markers), so a range may span them.
	Contiguous bool
}

// Blocks lists the text blocks of a body in order. ok is false when the text
// could not be placed.
func Blocks(md string) (blocks []Block, ok bool) {
	src := normalize(md)
	bs := body(src)
	san := sanitize(bs)
	if len(san) != len(bs) {
		return nil, false
	}
	lead := len(src) - len(bs)
	ok = true
	walkBlocksNodes(san, func([]tok) {}, func(n ast.Node, _ []tok) {
		if n == nil {
			ok = false
			return
		}
		lines := n.Lines()
		if lines == nil || lines.Len() == 0 {
			return
		}
		b := Block{Heading: n.Kind() == ast.KindHeading, Top: topLevel(n), Contiguous: true}
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			start, stop := seg.Start, seg.Stop
			for stop > start && (san[stop-1] == '\n' || san[stop-1] == '\r') {
				stop--
			}
			b.Lines = append(b.Lines, [2]int{lead + start, lead + stop})
			if i > 0 {
				prev := b.Lines[i-1][1] - lead
				if strings.TrimSpace(string(san[prev:start])) != "" {
					b.Contiguous = false
				}
			}
		}
		b.Start, b.End = b.Lines[0][0], b.Lines[len(b.Lines)-1][1]
		blocks = append(blocks, b)
	})
	return blocks, ok
}

func topLevel(n ast.Node) bool {
	p := n.Parent()
	return p != nil && p.Kind() == ast.KindDocument
}

// RuneLen16 is the number of UTF-16 code units of s.
func RuneLen16(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

var _ = utf8.RuneError
