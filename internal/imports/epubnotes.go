package imports

import (
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

// Footnotes of an EPUB, found the way books make them:
//
//   - EPUB 3: a link typed epub:type="noteref" (or role="doc-noteref") whose target
//     is an <aside epub:type="footnote"> (or role="doc-footnote"), in the same
//     file or in a notes file of its own.
//   - EPUB 2, and books made by tools that never learned of epub:type (Pandoc,
//     Calibre, Sigil, Gutenberg): an in-document anchor that links forward to a
//     note, and the note links back to the anchor. A link is taken for a note
//     reference only when its text is a number, a symbol or a word like "back",
//     it has an id, the thing it names comes later in reading order, and that
//     thing links back to it.
//
// The notes are taken out of the flow (the text of a note is not the text of a
// chapter) and written as Markdown footnotes at the reference. A container that
// held notes and nothing else (a "Notes" heading and a rule above them) goes with
// them.

// xspan is a range of bytes of a file.
type xspan struct{ a, b int }

// epubDoc is a content file of the flow.
type epubDoc struct {
	path   string
	item   *epubItem
	linear bool
	src    string
	sc     *xScan
	skip   string // why the file is not part of the flow: "cover", "toc", "fixed layout", "no text"; "" when it is
	types  string // epub:type of the body and of its first element
	// removed are the note bodies and their containers, sorted and merged.
	removed       []xspan
	notesConsumed int
	stem          string
}

// piece is src[a:b] without the notes.
func (d *epubDoc) piece(a, b int) string {
	a, b = max(a, 0), min(b, len(d.src))
	if a >= b {
		return ""
	}
	if len(d.removed) == 0 {
		return d.src[a:b]
	}
	var sb strings.Builder
	cur := a
	for _, r := range d.removed {
		if r.b <= cur {
			continue
		}
		if r.a >= b {
			break
		}
		if r.a > cur {
			sb.WriteString(d.src[cur:r.a])
		}
		cur = max(cur, r.b)
	}
	if cur < b {
		sb.WriteString(d.src[cur:b])
	}
	return sb.String()
}

// textIn is the visible text of [a, b) outside the notes.
func (d *epubDoc) textIn(a, b int) int {
	n := d.sc.textBetween(a, b)
	for _, r := range d.removed {
		lo, hi := max(r.a, a), min(r.b, b)
		if lo < hi {
			n -= d.sc.textBetween(lo, hi)
		}
	}
	return max(n, 0)
}

// epubNote is a note: its body as HTML, cleaned of back links and labels.
type epubNote struct {
	html string
}

// findNotes finds the notes of the book and records their ranges as removed.
// It returns the notes by "path#id" and how many were found.
func findNotes(docs []*epubDoc, byPath map[string]int, stop func() error) map[string]*epubNote {
	notes := map[string]*epubNote{}
	ranges := make([][]noteRange, len(docs))
	total, seen := 0, 0
scan:
	for di, d := range docs {
		for _, l := range d.sc.links {
			if seen++; seen%4096 == 0 && stop != nil && stop() != nil {
				return notes // the caller has gone; epubConvert says so
			}
			p, frag, ok := resolveRef(d.path, l.href)
			if !ok || frag == "" {
				continue
			}
			ti, ok := byPath[p]
			if !ok {
				continue
			}
			td := docs[ti]
			ei, ok := td.sc.byID[frag]
			if !ok {
				continue
			}
			key := p + "#" + frag
			if _, done := notes[key]; done {
				continue
			}
			te := td.sc.elems[ei]
			bi := ei
			if !blockish[te.tag] {
				bi = td.sc.blockOf(ei)
			}
			be := td.sc.elems[bi]
			typed := typeHas(l.types, "noteref") || l.role == "doc-noteref" || classHas(l.class, "noteref", "footnote-ref", "footnoteref", "fnref", "footnote-link", "fn-ref", "note-ref")
			declared := te.noteBody || be.noteBody
			switch {
			case declared:
			case typed:
				// A link that says it is a reference, to something that does not say it is a note: only when the
				// thing is small, so a link to a chapter is not taken for a note.
				if be.tag == "body" || be.end-be.start > 32<<10 {
					continue
				}
			default:
				if l.id == "" || !labelLike(l.text) || be.tag == "body" || be.end-be.start > 32<<10 {
					continue
				}
				if ti < di || (ti == di && te.start <= l.start) { // a note comes after what refers to it
					continue
				}
				back := false
				for _, bli := range td.sc.linksIn(be.start, be.end) {
					bp, bf, bok := resolveRef(td.path, td.sc.links[bli].href)
					if bok && bp == d.path && bf == l.id {
						back = true
						break
					}
				}
				if !back {
					continue
				}
			}
			if td == d && l.start >= be.start && l.start < be.end { // a link inside the very thing it names
				continue
			}
			if total++; total > maxEpubNotes {
				break scan // the notes found so far are still taken out of the flow
			}
			notes[key] = &epubNote{html: cleanNoteBody(td.src[be.start:be.end])}
			ranges[ti] = append(ranges[ti], noteRange{xspan{be.start, be.end}, bi})
			td.notesConsumed++
		}
	}
	steps := 0
	for di, d := range docs {
		rs := ranges[di]
		if len(rs) == 0 {
			continue
		}
		spans := make([]xspan, 0, len(rs))
		// A container of notes that holds nothing else goes with them: each note climbs to the containers it sits in
		// (at most htmlMaxDepth levels, each container once), so the work is the notes times their depth, not the
		// notes times the containers.
		taken := map[int]bool{}
		for i, r := range rs {
			spans = append(spans, r.span)
			if i%4096 == 0 && stop != nil && stop() != nil {
				return notes
			}
			for e, depth := r.elem, 0; e >= 0 && depth < htmlMaxDepth; e, depth = d.sc.elems[e].parent, depth+1 {
				if steps++; steps > maxNoteClimb {
					break
				}
				if d.sc.elems[e].container && !taken[e] {
					taken[e] = true
					spans = append(spans, xspan{d.sc.elems[e].start, d.sc.elems[e].end})
				}
			}
		}
		d.removed = mergeSpans(spans)
	}
	return notes
}

// noteRange is a note's body and the element it is.
type noteRange struct {
	span xspan
	elem int
}

// Budgets of the note search: notes found in a book, and steps taken climbing from notes to their containers (the
// notes times the depth the scan keeps, with room to spare).
const (
	maxEpubNotes = 100_000
	maxNoteClimb = 20_000_000
)

func mergeSpans(s []xspan) []xspan {
	sort.Slice(s, func(i, j int) bool { return s[i].a < s[j].a || s[i].a == s[j].a && s[i].b > s[j].b })
	out := s[:0:0]
	for _, x := range s {
		if n := len(out); n > 0 && x.a <= out[n-1].b {
			out[n-1].b = max(out[n-1].b, x.b)
			continue
		}
		out = append(out, x)
	}
	return out
}

var leadingLabel = regexp.MustCompile(`^\s*(\[\d{1,5}\]|\(\d{1,5}\)|\d{1,5}[.)\]:]|[*†‡§]+)\s*`)

// cleanNoteBody removes what a note body has besides the note: the link back to
// the reference, and a number written before the text. What is left is HTML the
// walker turns into the note's one paragraph.
func cleanNoteBody(s string) string {
	z := html.NewTokenizer(strings.NewReader(s))
	var out strings.Builder
	var hold strings.Builder // an internal link being read: kept only if it is not a label
	holding := false
	var holdText strings.Builder
	first := true // nothing but markup so far
	supOpen := 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		raw := string(z.Raw())
		switch tt {
		case html.StartTagToken:
			name, hasAttr := z.TagName()
			tag := stripNS(string(name))
			if tag == "a" && hasAttr && !holding {
				href := ""
				for {
					k, v, more := z.TagAttr()
					if string(k) == "href" {
						href = string(v)
					}
					if !more {
						break
					}
				}
				if href != "" && (strings.HasPrefix(href, "#") || !strings.Contains(href, ":")) {
					holding = true
					hold.Reset()
					holdText.Reset()
					hold.WriteString(raw)
					continue
				}
			}
			if tag == "sup" && first {
				supOpen++
			}
			if holding {
				hold.WriteString(raw)
			} else {
				out.WriteString(raw)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := stripNS(string(name))
			if holding && tag == "a" {
				holding = false
				if !labelLike(holdText.String()) {
					out.WriteString(hold.String())
					out.WriteString(raw)
					first = false
				}
				continue
			}
			if tag == "sup" && supOpen > 0 {
				supOpen--
			}
			if holding {
				hold.WriteString(raw)
			} else {
				out.WriteString(raw)
			}
		case html.TextToken:
			t := string(z.Text())
			if holding {
				hold.WriteString(raw)
				holdText.WriteString(t)
				continue
			}
			if first {
				if strings.TrimSpace(t) == "" {
					out.WriteString(raw)
					continue
				}
				if supOpen > 0 && labelLike(t) { // <sup>1</sup> before the text
					continue
				}
				first = false
				if loc := leadingLabel.FindStringIndex(t); loc != nil && loc[1] < len(t) {
					out.WriteString(html.EscapeString(t[loc[1]:]))
					continue
				}
			}
			out.WriteString(raw)
		default:
			if holding {
				hold.WriteString(raw)
			} else {
				out.WriteString(raw)
			}
		}
	}
	if holding {
		out.WriteString(hold.String())
	}
	return out.String()
}
