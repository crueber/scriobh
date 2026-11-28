package publish

import (
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/text"
)

// xhtmlWriter renders the neutral structure as XHTML 5 for an EPUB. It writes
// only the elements named here, with attributes it builds itself; text is
// escaped and stripped of what XML cannot hold; void elements are closed; the
// only addresses it writes are http, https and mailto links and the images the
// book carries (as files inside the EPUB).
type xhtmlWriter struct {
	b        *Book
	o        Options
	imageSrc func(key string) (href string, ok bool)
	out      strings.Builder
	// paged writes HTML for a browser rather than XHTML for an EPUB: no epub:
	// attributes, notes as plain blocks.
	paged bool
}

func (x *xhtmlWriter) esc(s string) string { return xmlEscape(s) }

func (x *xhtmlWriter) inlines(in []text.Inline) {
	for _, n := range in {
		switch n.Kind {
		case text.IText:
			x.out.WriteString(x.esc(strings.ReplaceAll(n.Text, "\n", " ")))
		case text.ICode:
			x.out.WriteString("<code>" + x.esc(strings.ReplaceAll(n.Text, "\n", " ")) + "</code>")
		case text.IBreak:
			x.out.WriteString("<br/>")
		case text.IEmph:
			x.wrap("em", n.Kids)
		case text.IStrong:
			x.wrap("strong", n.Kids)
		case text.IStrike:
			x.wrap("del", n.Kids)
		case text.ILink:
			if x.paged {
				// A printed page has no live link: the label, and the address in
				// parentheses when asked for.
				x.inlines(n.Kids)
				if Set(x.o.LinkURLs) && (strings.HasPrefix(n.Text, "http://") || strings.HasPrefix(n.Text, "https://") || strings.HasPrefix(n.Text, "mailto:")) {
					x.out.WriteString(" (" + x.esc(n.Text) + ")")
				}
				continue
			}
			if !(strings.HasPrefix(n.Text, "http://") || strings.HasPrefix(n.Text, "https://") || strings.HasPrefix(n.Text, "mailto:")) {
				x.inlines(n.Kids) // an in-page address means nothing in a book
				continue
			}
			x.out.WriteString(`<a href="` + x.esc(n.Text) + `">`)
			x.inlines(n.Kids)
			x.out.WriteString("</a>")
			if Set(x.o.LinkURLs) {
				x.out.WriteString(" (" + x.esc(n.Text) + ")")
			}
		case text.IImage:
			if src, ok := x.imageSrc(n.Text); ok {
				x.out.WriteString(`<img src="` + x.esc(src) + `" alt="` + x.esc(strings.TrimSpace(n.Alt)) + `"/>`)
			} else {
				label := "[image"
				if a := strings.TrimSpace(n.Alt); a != "" {
					label += ": " + a
				}
				x.out.WriteString(`<span class="omitted">` + x.esc(label+" (not included)]") + `</span>`)
			}
		case text.INote:
			if x.paged {
				x.out.WriteString(fmt.Sprintf(`<sup><a id="fnref-%d" href="#fn-%d">%d</a></sup>`, n.Num, n.Num, n.Num))
			} else {
				x.out.WriteString(fmt.Sprintf(`<sup><a epub:type="noteref" id="fnref-%d" href="#fn-%d">%d</a></sup>`, n.Num, n.Num, n.Num))
			}
		}
	}
}

func (x *xhtmlWriter) wrap(tag string, kids []text.Inline) {
	x.out.WriteString("<" + tag + ">")
	x.inlines(kids)
	x.out.WriteString("</" + tag + ">")
}

// blocks writes nodes. level is the depth of the section heading above them,
// so a heading inside a text sits below it.
func (x *xhtmlWriter) blocks(nodes []text.Node, level int) {
	first := true
	for _, n := range nodes {
		switch n.Kind {
		case text.NPara:
			if first {
				x.out.WriteString(`<p class="first">`)
			} else {
				x.out.WriteString("<p>")
			}
			x.inlines(n.Inl)
			x.out.WriteString("</p>\n")
			first = false
		case text.NHeading:
			lv := n.Level + level
			if lv < 2 {
				lv = 2
			}
			if lv > 6 {
				lv = 6
			}
			x.out.WriteString(fmt.Sprintf("<h%d>", lv))
			x.inlines(n.Inl)
			x.out.WriteString(fmt.Sprintf("</h%d>\n", lv))
			first = true
		case text.NRule:
			x.out.WriteString(`<p class="scenebreak">` + x.esc(SceneBreakText(x.o)) + "</p>\n")
			first = true
		case text.NQuote:
			x.out.WriteString("<blockquote>\n")
			x.blocks(n.Kids, level)
			x.out.WriteString("</blockquote>\n")
			first = true
		case text.NList:
			tag := "ul"
			attr := ""
			if n.Ordered {
				tag = "ol"
				if n.Start > 1 {
					attr = fmt.Sprintf(` start="%d"`, n.Start)
				}
			}
			x.out.WriteString("<" + tag + attr + ">\n")
			for _, it := range n.Items {
				x.out.WriteString("<li>")
				x.listItem(it, level)
				x.out.WriteString("</li>\n")
			}
			x.out.WriteString("</" + tag + ">\n")
			first = true
		case text.NCode, text.NSource:
			x.out.WriteString("<pre><code>" + x.esc(n.Text) + "</code></pre>\n")
			first = true
		case text.NTable:
			x.table(n)
			first = true
		}
	}
}

// listItem writes an item's blocks: a lone paragraph is written without its
// own element.
func (x *xhtmlWriter) listItem(it []text.Node, level int) {
	if len(it) == 1 && it[0].Kind == text.NPara {
		x.inlines(it[0].Inl)
		return
	}
	x.out.WriteString("\n")
	for _, k := range it {
		if k.Kind == text.NPara {
			x.out.WriteString("<p>")
			x.inlines(k.Inl)
			x.out.WriteString("</p>\n")
		} else {
			x.blocks([]text.Node{k}, level)
		}
	}
}

func (x *xhtmlWriter) table(n text.Node) {
	cols := 0
	for _, r := range n.Rows {
		cols = max(cols, len(r.Cells))
	}
	if cols == 0 {
		return
	}
	x.out.WriteString("<table>\n")
	var head, body []text.Row
	for _, r := range n.Rows {
		if r.Head {
			head = append(head, r)
		} else {
			body = append(body, r)
		}
	}
	emit := func(rows []text.Row, tag string, cell string) {
		if len(rows) == 0 {
			return
		}
		x.out.WriteString("<" + tag + ">\n")
		for _, r := range rows {
			x.out.WriteString("<tr>")
			for i := 0; i < cols; i++ {
				var c text.Cell
				if i < len(r.Cells) {
					c = r.Cells[i]
				}
				attr := ""
				switch c.Align {
				case "right":
					attr = ` class="ar"`
				case "center":
					attr = ` class="ac"`
				}
				x.out.WriteString("<" + cell + attr + ">")
				x.inlines(c.Inl)
				x.out.WriteString("</" + cell + ">")
			}
			x.out.WriteString("</tr>\n")
		}
		x.out.WriteString("</" + tag + ">\n")
	}
	emit(head, "thead", "th")
	emit(body, "tbody", "td")
	x.out.WriteString("</table>\n")
}

// notes writes a section's footnotes as a footnotes section at its end.
func (x *xhtmlWriter) notes(notes []text.Footnote, used map[int]bool) {
	var kept []text.Footnote
	for _, f := range notes {
		if used[f.Num] {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		return
	}
	if x.paged {
		x.out.WriteString(`<section class="footnotes">` + "\n")
	} else {
		x.out.WriteString(`<section class="footnotes" epub:type="footnotes">` + "\n")
	}
	for _, f := range kept {
		if x.paged {
			x.out.WriteString(fmt.Sprintf(`<div class="footnote" id="fn-%d">`+"\n", f.Num))
		} else {
			x.out.WriteString(fmt.Sprintf(`<aside epub:type="footnote" id="fn-%d">`+"\n", f.Num))
		}
		inner := &xhtmlWriter{b: x.b, o: x.o, paged: x.paged, imageSrc: func(string) (string, bool) { return "", false }}
		inner.blocks(f.Nodes, 1)
		body := strings.TrimRight(inner.out.String(), "\n")
		back := fmt.Sprintf(` <a href="#fnref-%d">↩</a>`, f.Num)
		if i := strings.LastIndex(body, "</p>"); i >= 0 && strings.HasSuffix(body, "</p>") {
			body = body[:i] + back + "</p>"
		} else {
			body += "<p>" + strings.TrimSpace(back) + "</p>"
		}
		// Footnotes are never the first paragraph of a text: drop the class.
		body = strings.ReplaceAll(body, ` class="first"`, "")
		if x.paged {
			x.out.WriteString(body + "\n</div>\n")
		} else {
			x.out.WriteString(body + "\n</aside>\n")
		}
	}
	x.out.WriteString("</section>\n")
}

// noteRefs lists the footnote numbers a node list refers to.
func noteRefs(nodes []text.Node, into map[int]bool) {
	var inl func([]text.Inline)
	inl = func(in []text.Inline) {
		for _, x := range in {
			if x.Kind == text.INote {
				into[x.Num] = true
			}
			inl(x.Kids)
		}
	}
	var walk func([]text.Node)
	walk = func(ns []text.Node) {
		for _, n := range ns {
			inl(n.Inl)
			walk(n.Kids)
			for _, it := range n.Items {
				walk(it)
			}
			for _, r := range n.Rows {
				for _, c := range r.Cells {
					inl(c.Inl)
				}
			}
		}
	}
	walk(nodes)
}
