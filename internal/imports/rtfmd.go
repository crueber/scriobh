package imports

import (
	"strings"
)

// rtfEmit says how a parsed RTF document is written.
type rtfEmit struct {
	// wiki makes the wiki link for a link to another document of the project:
	// label is the text the link showed. "" means it cannot be a link (the
	// document is not in the import), and the text stays text.
	wiki func(id, label string) string
	// pic makes the picture a NeXTGraphic names (a file beside the text), or
	// false when it is not imported and the [image] mark stays.
	pic func(name string) (seg, bool)
}

// wikiFor makes a [[wiki link]] out of a document title and the text a link
// showed, or "" when either holds a character that could end or change a link.
// A title is a title the converter read, never text from a body, but it still
// came from the file, so it is held to a short list of characters.
func wikiFor(title, label string) string {
	if !wikiSafe(title) {
		return ""
	}
	label = strings.Join(strings.Fields(label), " ")
	if label == "" || strings.EqualFold(label, title) {
		return "[[" + title + "]]"
	}
	if !wikiSafe(label) {
		return "" // the writer's words stay as they are, with no link
	}
	return "[[" + title + "|" + label + "]]"
}

func wikiSafe(s string) bool {
	if s == "" || len(s) > 200 || strings.TrimSpace(s) != s || strings.Contains(s, "  ") {
		return false
	}
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return false
		case strings.ContainsRune("[]|#^\\<>%`*_~&{}()!:$\u2028\u2029\u202E\u202D\u202A\u202B\u202C", r):
			return false
		}
	}
	return true
}

// emit writes the document's paragraphs to the sink: bold, italic and
// strikethrough kept, headings by style, lists, quotes, footnotes, images
// marked, and annotations and comments as private notes, closed at the end of
// the block they are in.
func (d *rtfDoc) emit(s *sink, o rtfEmit) {
	noteID := map[int]string{}
	listSeq := 0
	inList := false
	counters := map[int]int{}
	for _, p := range d.paras {
		segs := make([]seg, 0, len(p.segs))
		var priv []seg
		for _, rs := range p.segs {
			switch {
			case rs.image:
				if o.pic != nil && rs.pic != "" && !rs.priv {
					if sg, ok := o.pic(rs.pic); ok {
						segs = append(segs, sg)
						continue
					}
				}
				segs = append(segs, seg{text: "\x00image"})
			case rs.note != 0:
				id, ok := noteID[rs.note]
				if !ok && rs.note-1 < len(d.notes) {
					var paras [][]seg
					for _, np := range d.notes[rs.note-1] {
						paras = append(paras, plainSegs(np.segs, o))
					}
					id = s.note(paras)
					noteID[rs.note] = id
				}
				if id != "" {
					segs = append(segs, seg{note: id})
				}
			default:
				t := rs.text
				if t == "\n" && !rs.priv {
					segs = append(segs, seg{brk: true})
					continue
				}
				if t == "\n" {
					segs = append(segs, seg{brk: true, priv: true})
					continue
				}
				sg := seg{text: t, b: rs.f.b, i: rs.f.i, s: rs.f.s, priv: rs.priv}
				switch {
				case strings.HasPrefix(rs.link, "scrivlnk:"):
					// Inside a private run the text stays private text: a link made of it
					// would be visible.
					if o.wiki != nil && !rs.priv {
						if w := o.wiki(strings.TrimPrefix(rs.link, "scrivlnk:"), t); w != "" {
							sg = seg{text: t, wiki: w}
						}
					}
				case rs.link != "" && !rs.priv:
					sg.link = rs.link
				}
				segs = append(segs, sg)
			}
		}
		if p.heading > 0 {
			// A heading holds plain text: a note in it moves to a paragraph of its own after it.
			var ht strings.Builder
			for _, g := range segs {
				if g.priv {
					priv = append(priv, g)
				} else if g.brk {
					ht.WriteByte(' ')
				} else if g.note == "" && g.text != "\x00image" {
					ht.WriteString(g.text)
				}
			}
			s.heading(p.heading, ht.String())
			if len(priv) > 0 {
				s.para(priv)
			}
			inList = false
			continue
		}
		switch {
		case p.list:
			if !inList {
				listSeq++
				counters = map[int]int{}
			}
			inList = true
			n := p.num
			if !p.ordered {
				n = 0
			} else if n == 0 {
				n = counters[p.level] + 1
			}
			counters[p.level] = n
			s.item(segs, p.level, p.ordered, n, "l"+itoa(listSeq))
		case p.quote:
			inList = false
			s.quote(segs)
		default:
			inList = false
			s.para(segs)
		}
	}
}

// plainSegs is a footnote paragraph's text: formatting kept, no notes inside notes.
func plainSegs(in []rseg, o rtfEmit) []seg {
	out := make([]seg, 0, len(in))
	for _, rs := range in {
		switch {
		case rs.image:
			out = append(out, seg{text: "\x00image"})
		case rs.note != 0:
		case rs.text == "\n":
			out = append(out, seg{brk: true, priv: rs.priv})
		default:
			sg := seg{text: rs.text, b: rs.f.b, i: rs.f.i, s: rs.f.s, priv: rs.priv}
			if rs.link != "" && !rs.priv && !strings.HasPrefix(rs.link, "scrivlnk:") {
				sg.link = rs.link
			}
			out = append(out, sg)
		}
	}
	return out
}
