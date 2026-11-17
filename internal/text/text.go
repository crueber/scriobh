// Package text measures and flattens Markdown: word and character counts, and
// the plain text the search index reads. The count is defined once, over the
// stored Markdown, so the status bar, the tree, an export and an agent all
// agree. It must equal the editor's own count (web/editor/src/count.js applied
// to the parsed document); a corpus test drives the editor through Node and
// compares, file by file.
//
// What is text: the prose of paragraphs and headings, including inline code.
// What is not: %%private notes%%, fenced and indented code, and everything the
// editor holds as a source block (raw HTML and comments, tables, front matter,
// `:::` blocks, link reference definitions). A wiki link counts as the text it
// shows; images, footnote references and inline HTML are zero-width.
package text

import (
	"strings"
	"unicode/utf8"
)

// ReadingWPM is the reading speed behind ReadingMinutes.
const ReadingWPM = 238

// normalize makes the stored text what the parser sees: LF line endings and no
// NUL characters.
func normalize(md string) []byte {
	if strings.ContainsAny(md, "\r\x00") {
		md = strings.ReplaceAll(md, "\r\n", "\n")
		md = strings.ReplaceAll(md, "\r", "\n")
		md = strings.ReplaceAll(md, "\x00", "�")
	}
	return []byte(md)
}

// body drops the front matter, the only construct that depends on where in
// the document it sits: it is a source block only on the very first lines.
func body(src []byte) []byte {
	if len(src) < 4 || src[0] != '-' {
		return src
	}
	if n := frontMatterEnd(string(src)); n > 0 {
		return src[n:]
	}
	return src
}

// WordCount counts the words of a Markdown document: whitespace-delimited runs
// that contain a letter or a digit, in the text the editor would show.
func WordCount(md string) int {
	n := 0
	walkBlocks(sanitize(body(normalize(md))), func(toks []tok) {
		words(blockText(toks, true), func(w string) {
			if isWord(w) {
				n++
			}
		})
	})
	return n
}

// CharCount counts characters (runes) of the stored Markdown.
func CharCount(md string) int { return utf8.RuneCountInString(md) }

// ReadingMinutes is the reading time in whole minutes, rounded up, at 238
// words a minute. A document with any words takes at least a minute.
func ReadingMinutes(words int) int {
	if words <= 0 {
		return 0
	}
	return (words + ReadingWPM - 1) / ReadingWPM
}

// SearchBody is the text the search index holds for a document: one line per
// paragraph or heading, markup removed, private notes and code and source
// blocks left out, wiki links expanded to the text they show. It reads the
// same text the counter does, except that a wiki link inside a private note is
// private too.
func SearchBody(md string) string {
	var b strings.Builder
	walkBlocks(sanitize(body(normalize(md))), func(toks []tok) {
		first := true
		var line strings.Builder
		words(blockText(toks, false), func(w string) {
			if !first {
				line.WriteByte(' ')
			}
			first = false
			line.WriteString(w)
		})
		if line.Len() == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line.String())
	})
	return stripMarkers(b.String())
}

// Plain is SearchBody: the readable text of Markdown.
func Plain(md string) string { return SearchBody(md) }

// blockText joins the tokens of one textblock the way the editor builds the
// string it counts. In count mode a wiki link inside a private note still
// counts (the editor checks the note mark on text only); for search nothing
// private is kept.
func blockText(toks []tok, count bool) string {
	var b strings.Builder
	for _, t := range toks {
		if t.ghost {
			continue // ghosted text is in the document but not in its count or its index
		}
		switch t.kind {
		case kText, kSpecial, kSoft:
			if t.private {
				continue
			}
			if t.kind == kSoft {
				b.WriteByte(' ')
			} else {
				b.WriteString(t.s)
			}
		case kHard:
			b.WriteByte(' ')
		case kWiki:
			if count || !t.private {
				b.WriteString(t.s)
			}
		}
	}
	return b.String()
}

// stripMarkers removes the private-use code points the search snippets use as
// highlight markers, so a document can never forge one.
func stripMarkers(s string) string {
	if !strings.ContainsFunc(s, isMarker) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isMarker(r) {
			return -1
		}
		return r
	}, s)
}

func isMarker(r rune) bool { return r >= 0xE000 && r <= 0xF8FF }
