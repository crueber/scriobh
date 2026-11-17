package pdfread

import (
	"strings"
	"unicode"
)

// The document model the layout analysis produces: blocks of runs, in reading
// order, grouped into chapters. Markdown is made from it elsewhere.

// BlockKind says what a block is.
type BlockKind uint8

const (
	BlockPara BlockKind = iota
	BlockHeading
	BlockScene
	BlockRow // a row of what looked like a table, flattened
	BlockQuote
	BlockCode
	BlockItem // a list item
)

// Run is a piece of a paragraph with one style.
type Run struct {
	Text   string
	Bold   bool
	Italic bool
	Mono   bool
	Note   string // a footnote reference: Text is empty and Note names the note
}

// Note is a footnote recovered from the foot of a page.
type Note struct {
	Label string // unique in the document
	Runs  []Run
}

// Block is a paragraph, a heading, a scene break or a flattened table row.
type Block struct {
	Kind  BlockKind
	Level int // headings: 1 is the largest size class
	Runs  []Run
	Page  int // 0-based page the block starts on
	// List items: the marker said it was ordered, and its number.
	Ordered bool
	Num     int
	Notes   []Note
	// Joined and Dehyphenated count what was done to make the block.
	Joined, Dehyphenated int
}

// Text is the block's plain text.
func (b *Block) Text() string {
	var sb strings.Builder
	for _, r := range b.Runs {
		sb.WriteString(r.Text)
	}
	return sb.String()
}

// Chapter is a proposed chapter: a title and the blocks under it.
type Chapter struct {
	Title  string
	Part   bool // a part: its blocks are the text before its first chapter
	Blocks []Block
	Page   int // 0-based page it starts on
	Source string
}

// normalise cleans extracted text: ligature characters become letters, the
// soft hyphen is kept as one character for the line joiner, no-break and thin
// spaces become spaces, and control and bidirectional marks are dropped.
func normalise(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == 0xFB00:
			b.WriteString("ff")
		case r == 0xFB01:
			b.WriteString("fi")
		case r == 0xFB02:
			b.WriteString("fl")
		case r == 0xFB03:
			b.WriteString("ffi")
		case r == 0xFB04:
			b.WriteString("ffl")
		case r == 0xFB05 || r == 0xFB06:
			b.WriteString("st")
		case r == 0x00A0 || r == 0x2009 || r == 0x202F || r == 0x2002 || r == 0x2003 || r == 0x2007 || r == 0x2008 || r == 0x200A:
			b.WriteByte(' ')
		case r == 0x200B || r == 0x200C || r == 0x200D || r == 0x2060 || r == 0xFEFF || r == 0x2028 || r == 0x2029 || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069:
		case r == '\t' || r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 || r == 0xFFFD || unicode.Is(unicode.Co, r):
		case r == 0x2010 || r == 0x2011:
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

var noJoinPrefixes = map[string]bool{"co": true, "re": true, "pre": true, "non": true, "anti": true, "semi": true, "self": true, "ex": true, "vice": true,
	"half": true, "all": true, "ill": true, "well": true, "mid": true, "post": true, "pro": true, "sub": true, "ultra": true, "multi": true, "quasi": true, "pseudo": true, "cross": true, "inter": true}

// lastWord is the letters at the end of s.
func lastWord(s string) string {
	rs := []rune(s)
	i := len(rs)
	for i > 0 && unicode.IsLetter(rs[i-1]) {
		i--
	}
	return string(rs[i:])
}

// stripOverrides removes what can reorder or break the text around it: line and
// paragraph separators and the bidirectional embedding, override and isolate
// controls. The directional marks and the letters of a right-to-left script stay.
func stripOverrides(s string) string {
	return strings.Map(func(r rune) rune {
		if r == 0x2028 || r == 0x2029 || r >= 0x202A && r <= 0x202E || r >= 0x2066 && r <= 0x2069 {
			return -1
		}
		return r
	}, s)
}
