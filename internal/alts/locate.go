package alts

import (
	"strings"
	"unicode/utf8"
)

// Finding a quote again without guessing. These were the AI accept helpers; they
// live here because a suggestion from a person is applied by the same rule: at
// the place it was made when the text has not changed, else where the original
// text is found again with ALL the context stored with it, exactly once.

// Span is a range of a body: bytes, and the same in UTF-16 units.
type Span struct {
	A, B   int // bytes
	U1, U2 int // UTF-16 code units
}

// MaxOccurrences bounds the scan for a quote that repeats. A quote that repeats
// more often than that is not unique: the scan fails closed.
const MaxOccurrences = 20000

// Markers for the start and the end of a paragraph inside a stored context. A
// quote that is a whole paragraph, or that starts or ends one, has no words on
// that side; without a marker "nothing" would match any quote that stood alone
// anywhere.
const (
	ParaStart = "\x02"
	ParaEnd   = "\x03"
)

// LocateIn finds a quote in a body without guessing. With a hint (offsets from
// the revision the quote was read at) the quote must be exactly there when
// hintOK says the revision has not changed. Otherwise, or when the hint no
// longer holds, every occurrence is looked at: with uniqueOK the context is the
// raw words around the quote (alts.Contexts) and the only occurrence in the text
// is taken when none has that context; without it the context is
// ParagraphContext's, compared whole, and an occurrence with other words around
// it is another place (a deletion must never land on a lookalike).
func LocateIn(body string, ix *Index, quote, before, after string, hint *Span, hintOK, uniqueOK bool) (Span, bool) {
	if quote == "" {
		return Span{}, false
	}
	if hint != nil && hintOK {
		if a, ok1 := ix.Byte(hint.U1); ok1 {
			if b, ok2 := ix.Byte(hint.U2); ok2 && a < b && b <= len(body) && body[a:b] == quote {
				return Span{A: a, B: b, U1: hint.U1, U2: hint.U2}, true
			}
		}
	}
	var all, exact []int
	for from := 0; from <= len(body); {
		i := strings.Index(body[from:], quote)
		if i < 0 {
			break
		}
		if len(all) >= MaxOccurrences {
			return Span{}, false // so many that none can be told from the rest
		}
		a := from + i
		all = append(all, a)
		if uniqueOK {
			if strings.HasSuffix(body[:a], before) && strings.HasPrefix(body[a+len(quote):], after) {
				exact = append(exact, a)
			}
		} else if pb, pa := ParagraphContext(body, a, a+len(quote)); pb == before && pa == after {
			exact = append(exact, a)
		}
		from = a + 1
		for from < len(body) && !utf8.RuneStart(body[from]) {
			from++
		}
	}
	at := -1
	switch {
	case len(exact) == 1:
		at = exact[0]
	case len(exact) == 0 && len(all) == 1 && uniqueOK:
		at = all[0]
	}
	if at < 0 {
		return Span{}, false
	}
	b := at + len(quote)
	return Span{at, b, ix.Unit(at), ix.Unit(b)}, true
}

// ParagraphContext is the context kept with a mark, a held selection or a
// suggestion: the words before and after it, as Contexts takes them, but never
// across a blank line, with a marker where the paragraph (or the document)
// begins or ends. An edit in the next paragraph then does not make it stale, and
// a lookalike in another paragraph is not the same place.
func ParagraphContext(body string, a, b int) (before, after string) {
	before, after = Contexts(body, a, b)
	if i := strings.LastIndex(before, "\n\n"); i >= 0 {
		before = ParaStart + before[i+2:]
	} else if utf8.RuneCountInString(before) < ContextRunes {
		before = ParaStart + before
	}
	if i := strings.Index(after, "\n\n"); i >= 0 {
		after = after[:i] + ParaEnd
	} else if utf8.RuneCountInString(after) < ContextRunes {
		after += ParaEnd
	}
	return before, after
}
