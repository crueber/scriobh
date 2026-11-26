package text

import (
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/fold"
)

// WikiLink is one distinct `[[Target]]` or `[[Target|alias]]` in a document.
type WikiLink struct {
	Target  string // the title the link points at, trimmed, as written
	Alias   string // the shown text of the first occurrence when it differs ("" for [[Target]])
	Snippet string // plain text of the paragraph around the first occurrence, at most snippetRunes long
	Count   int    // how many times the link occurs
}

// snippetRunes bounds the context kept for a link.
const snippetRunes = 160

// WikiLinks returns the distinct wiki links of a Markdown document, in order of
// first occurrence. It reads the same tree the word counter reads, so a link in
// a fenced or indented code block, in a source block (raw HTML, a table, front
// matter), inside inline code, or inside a %%private note%% is not a link here
// either: private text never leaks into a backlink snippet or creates a page.
// Links are distinct by target text as written; `[[Elara]]` and `[[elara]]` are
// two entries that resolve to the same document.
func WikiLinks(md string) []WikiLink {
	if !strings.Contains(md, "[[") {
		return nil
	}
	var out []WikiLink
	index := map[string]int{}
	walkBlocks(sanitize(body(normalize(md))), func(toks []tok) {
		var (
			b    strings.Builder
			hits []struct {
				at   int // rune offset into b where the link's shown text starts
				tok  tok
				size int
			}
		)
		runes := 0
		add := func(s string) {
			b.WriteString(s)
			runes += utf8.RuneCountInString(s)
		}
		for _, t := range toks {
			switch t.kind {
			case kText, kSpecial:
				if !t.private {
					add(t.s)
				}
			case kSoft, kHard:
				if t.kind == kHard || !t.private {
					add(" ")
				}
			case kWiki:
				if t.private {
					continue
				}
				hits = append(hits, struct {
					at   int
					tok  tok
					size int
				}{runes, t, utf8.RuneCountInString(t.s)})
				add(t.s)
			}
		}
		if len(hits) == 0 {
			return
		}
		block := []rune(b.String())
		for _, h := range hits {
			target := h.tok.target
			if target == "" {
				continue
			}
			if i, ok := index[target]; ok {
				out[i].Count++
				continue
			}
			index[target] = len(out)
			out = append(out, WikiLink{
				Target:  target,
				Alias:   h.tok.alias,
				Snippet: stripMarkers(snippetAround(block, h.at, h.size)),
				Count:   1,
			})
		}
	})
	return out
}

// snippetAround returns up to snippetRunes of block around [at, at+size), with
// whitespace collapsed and an ellipsis where the paragraph was cut.
func snippetAround(block []rune, at, size int) string {
	lo, hi := 0, len(block)
	if len(block) > snippetRunes {
		lo = at + size/2 - snippetRunes/2
		if lo < 0 {
			lo = 0
		}
		hi = lo + snippetRunes
		if hi > len(block) {
			hi = len(block)
			lo = hi - snippetRunes
		}
		// Start and end on word boundaries where there is one nearby.
		for lo > 0 && lo < at && block[lo-1] != ' ' && block[lo] != ' ' {
			lo++
		}
		for hi < len(block) && hi > at+size && block[hi] != ' ' && block[hi-1] != ' ' {
			hi--
		}
	}
	s := strings.Join(strings.Fields(string(block[lo:hi])), " ")
	if lo > 0 {
		s = "…" + s
	}
	if hi < len(block) {
		s += "…"
	}
	return s
}

// FoldTitle is the key wiki links and document titles are matched on: runs of
// whitespace collapse to one space and the ends are trimmed; the text is put in
// Unicode normal form C (so "Café" typed with a combining accent, as macOS
// file names are, equals the precomposed one) and given the full Unicode case
// fold (so "STRASSE" equals "Straße"). SQLite's lower() only folds ASCII, so
// matching is always done on this key in Go.
//
// Changing this changes every stored key: bump links.IndexVersion so the
// start-up rebuild recomputes them.
func FoldTitle(s string) string {
	return fold.Title(s)
}
