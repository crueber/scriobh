// Package alts is the pure half of alternatives: what the trailing comment of a
// document looks like, how the text is split from it, where an alternative's
// anchor sits in the text and how it is found again after the text changes.
// It touches no database; internal/domain/altstore keeps the tables and calls
// it from the write path. See docs/OPEN-QUESTIONS.md "Phase 3.5 alternatives
// decisions" for the design and the invariant this package helps keep:
//
//	after every committed write to a document, Serialize(its alternatives) is
//	byte-equal to the comment that ends documents.content, and the body holds
//	the chosen wording of every active alternative at its anchor.
//
// Offsets in an Item are UTF-16 code units over the body with LF line endings
// (the measure doc.split and mention.list use). Inside this package the work is
// done in bytes and converted at the edges (Index).
package alts

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// Scopes of an alternative.
const (
	ScopeWord      = "word"
	ScopeSentence  = "sentence"
	ScopeParagraph = "paragraph"
	// ScopeSpan is not a scope of an alternative (ValidateItem refuses it). It is
	// the shape of a comment's anchor: any range inside one block of prose, cut at
	// character edges, trimmed, clear of markers. Comments are anchored by the same
	// Rebase as alternatives, one item per comment.
	ScopeSpan = "span"
)

// Sources of a variant. "generated" is text suggested by a configured provider.
const (
	SourceManual    = "manual"
	SourceGenerated = "generated"
)

// Statuses an alternative can have in a listing.
const (
	StatusActive   = "active"
	StatusOrphaned = "orphaned"
)

// Marker opens the comment line.
const Marker = "<!-- scriobh:alts "

// Version is the comment format version this package writes.
const Version = 1

// ContextRunes is how much text before and after an anchor is kept to find it
// again.
const ContextRunes = 24

// Variant is one wording of an alternative.
type Variant struct {
	ID       string
	Text     string // Markdown source
	Original bool   // the wording that was in the text when the alternative was made; immutable
	Source   string // SourceManual or SourceGenerated
	By       string // author id, "" when unknown
	At       string // when it was added (db time)
}

// Item is one alternative: a range of the body and the wordings that can fill it.
type Item struct {
	ID    string
	Scope string
	// Start and End are the anchor in UTF-16 units of the body. End is always
	// Start plus the length of the chosen text. Start is -1 for an item that has
	// not been placed yet (it came from a comment and is yet to be located).
	// An orphaned item keeps the last anchor it had; it is not trusted.
	Start, End    int
	Before, After string // up to ContextRunes of the Markdown around the anchor
	Chosen        string // variant id whose text is in the body
	Orphan        bool
	Variants      []Variant // in position order
}

// Placed reports whether the item has an anchor.
func (it Item) Placed() bool { return it.Start >= 0 }

// ChosenVariant returns the chosen variant.
func (it Item) ChosenVariant() (Variant, bool) { return it.Find(it.Chosen) }

// Find returns the variant with an id.
func (it Item) Find(id string) (Variant, bool) {
	for _, v := range it.Variants {
		if v.ID == id {
			return v, true
		}
	}
	return Variant{}, false
}

// Original returns the immutable original variant.
func (it Item) Original() (Variant, bool) {
	for _, v := range it.Variants {
		if v.Original {
			return v, true
		}
	}
	return Variant{}, false
}

// ChosenText is the text the anchor holds.
func (it Item) ChosenText() string {
	v, _ := it.ChosenVariant()
	return v.Text
}

// Clone copies an item deeply.
func (it Item) Clone() Item {
	it.Variants = append([]Variant(nil), it.Variants...)
	return it
}

// SortItems puts items in document order: placed active ones by start, then
// the orphaned ones by id, so the comment is the same whatever order they were
// handled in.
func SortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Orphan != b.Orphan {
			return !a.Orphan
		}
		if !a.Orphan && a.Start != b.Start {
			return a.Start < b.Start
		}
		return a.ID < b.ID
	})
}

// ---- splitting the text from its comment -----------------------------------

// Parts is a document's text taken apart at its trailing alternatives comment.
// Body + Sep + Line + Tail is the text exactly as it was given.
type Parts struct {
	Body    string // everything before the comment, minus one separating line break
	Sep     string // "" or "\n": the blank line between the body and the comment
	Line    string // the comment line, without its line break; "" when there is none
	Tail    string // white space after the comment
	HasLine bool
}

// NormalizeEOL turns CRLF and CR into LF. A document that carries alternatives
// is stored with LF only, so the offsets are exact.
func NormalizeEOL(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// Split takes content (LF line endings) apart. The comment is the last
// non-blank line when it starts with Marker and ends with " -->".
func Split(content string) Parts {
	t := strings.TrimRight(content, " \t\r\n")
	ls := strings.LastIndexByte(t, '\n') + 1
	line := t[ls:]
	if !strings.HasPrefix(line, Marker) || !strings.HasSuffix(line, " -->") || len(line) < len(Marker)+len(" -->") {
		return Parts{Body: content}
	}
	pre := content[:ls]
	p := Parts{Line: line, Tail: content[len(t):], HasLine: true}
	if strings.HasSuffix(pre, "\n\n") {
		p.Body, p.Sep = pre[:len(pre)-1], "\n"
	} else {
		p.Body = pre
	}
	return p
}

// Compose joins parts with a (possibly new) comment line. An empty line drops
// the comment: the body stands alone.
func (p Parts) Compose(line string) string {
	if line == "" {
		return p.Body
	}
	if p.HasLine {
		return p.Body + p.Sep + line + p.Tail
	}
	body := p.Body
	switch {
	case body == "":
		return line + "\n"
	case !strings.HasSuffix(body, "\n"):
		body += "\n"
	}
	return body + "\n" + line + "\n"
}

// BodyOf is the text before the comment: what the word count, search and
// anchors are about.
func BodyOf(content string) string {
	if !strings.Contains(content, Marker) {
		return content
	}
	return Split(NormalizeEOL(content)).Body
}

// ---- UTF-16 offsets --------------------------------------------------------

// Index converts between byte offsets and UTF-16 code units of one string.
type Index struct {
	ascii    bool
	n        int
	runeByte []int32 // byte offset of each rune start, plus the end
	runeUnit []int32 // UTF-16 unit of each rune start, plus the end
}

// NewIndex indexes s. An ASCII string needs no tables.
func NewIndex(s string) *Index {
	ix := &Index{ascii: true, n: len(s)}
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			ix.ascii = false
			break
		}
	}
	if ix.ascii {
		return ix
	}
	ix.runeByte = make([]int32, 0, len(s)+1)
	ix.runeUnit = make([]int32, 0, len(s)+1)
	unit := int32(0)
	for i, r := range s {
		ix.runeByte = append(ix.runeByte, int32(i))
		ix.runeUnit = append(ix.runeUnit, unit)
		if r >= 0x10000 {
			unit += 2
		} else {
			unit++
		}
	}
	ix.runeByte = append(ix.runeByte, int32(len(s)))
	ix.runeUnit = append(ix.runeUnit, unit)
	return ix
}

// Units is the length of the string in UTF-16 units.
func (ix *Index) Units() int {
	if ix.ascii {
		return ix.n
	}
	return int(ix.runeUnit[len(ix.runeUnit)-1])
}

// Unit converts a byte offset (on a rune boundary) to UTF-16 units.
func (ix *Index) Unit(b int) int {
	if ix.ascii {
		return b
	}
	i := sort.Search(len(ix.runeByte), func(i int) bool { return int(ix.runeByte[i]) >= b })
	if i >= len(ix.runeByte) {
		return ix.Units()
	}
	return int(ix.runeUnit[i])
}

// Byte converts UTF-16 units to a byte offset. ok is false when the offset is
// out of range or falls inside a surrogate pair.
func (ix *Index) Byte(u int) (int, bool) {
	if u < 0 {
		return 0, false
	}
	if ix.ascii {
		return u, u <= ix.n
	}
	i := sort.Search(len(ix.runeUnit), func(i int) bool { return int(ix.runeUnit[i]) >= u })
	if i >= len(ix.runeUnit) || int(ix.runeUnit[i]) != u {
		return 0, false
	}
	return int(ix.runeByte[i]), true
}

// Len16 is the length of s in UTF-16 units.
func Len16(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c < utf8.RuneSelf:
			n++
		case c >= 0xF0:
			n += 2
		case c >= 0xC0:
			n++
		}
	}
	return n
}

// context returns up to ContextRunes runes before byte offset at, and after it.
func contextBefore(s string, at int) string {
	i := at
	for n := 0; n < ContextRunes && i > 0; n++ {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return s[i:at]
}

func contextAfter(s string, at int) string {
	i := at
	for n := 0; n < ContextRunes && i < len(s); n++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[at:i]
}

// Contexts returns the text kept around the byte range [a,b) of body to find an
// anchor again.
func Contexts(body string, a, b int) (before, after string) {
	return contextBefore(body, a), contextAfter(body, b)
}
