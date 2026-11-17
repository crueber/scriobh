package site

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/imagesafe"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/text"
)

// published is the text a reader of the site may see: the alternatives
// annotation is gone (the chosen wording is the text) and ghosted text is
// omitted. Private notes, HTML comments and front matter are dropped by
// text.Structure itself, which the compile pipeline also trusts.
func published(content string) string {
	return text.StripGhosts(alts.BodyWithoutComment(content), true)
}

// pubImage is a picture that went into the site.
type pubImage struct {
	path          string // assets/img/<hash>.<ext>
	data          []byte
	width, height int
	rotated       bool // the pixels were turned upright
}

// imageSet resolves the images of posts, once each: the bytes are sniffed by the
// caller, stripped of metadata here, named by their hash and counted.
type imageSet struct {
	in    map[string]Image
	done  map[string]*pubImage // nil value: refused
	order []string
	bytes int
	warn  func(code, msg string)
}

func (s *imageSet) get(dest string) (*pubImage, string) {
	if p, ok := s.done[dest]; ok {
		return p, ""
	}
	img, ok := s.in[dest]
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(dest)), "data:") {
		ok = false // an inline picture is never published, whatever the caller resolved
	}
	if !ok {
		s.done[dest] = nil
		if strings.HasPrefix(dest, "http://") || strings.HasPrefix(dest, "https://") {
			return nil, "remote"
		}
		return nil, "missing"
	}
	if len(img.Data) == 0 {
		s.done[dest] = nil
		return nil, "missing"
	}
	clean, info, err := imagesafe.Clean(img.Ext, img.Data)
	if err != nil {
		s.done[dest] = nil
		return nil, "damaged"
	}
	p := &pubImage{path: "assets/img/" + imageName(img.Ext, clean), data: clean, width: info.Width, height: info.Height, rotated: img.Rotated || info.Rotated}
	s.done[dest] = p
	s.order = append(s.order, dest)
	return p, ""
}

// writer renders the neutral structure as HTML that is also well-formed XML:
// void elements closed, attribute values quoted, only the five predefined
// entities. It writes only the elements named here, with attributes it builds;
// every string is escaped and stripped of what XML cannot hold; the addresses it
// writes are http, https and mailto links and the site's own images.
type writer struct {
	out strings.Builder
	// from is the site path of the page being written, for relative addresses;
	// empty when writing for a feed, where addresses are absolute.
	from string
	// abs, for a feed, is the site's absolute base; frag is the absolute address
	// of the post, for footnote links.
	abs  *url.URL
	frag string
	imgs *imageSet
	// counters
	missingAlt, imagesUsed, remote, missing, damaged, rotated int
}

func esc(s string) string {
	s = publish.CleanXML(s)
	if !strings.ContainsAny(s, `&<>"'`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// linkTarget returns the address to write for a link in a text, or false when
// the link is dropped (only its label is shown). Anything but an http, https or
// mailto address is dropped: a javascript: or data: address, a path, a fragment.
func linkTarget(dest string) (string, bool) {
	dest = strings.TrimSpace(dest)
	if len(dest) > 2000 {
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(dest), "mailto:") {
		for _, r := range dest {
			if r <= ' ' || r == 0x7f || r == '<' || r == '>' || r == '"' || r == '\\' {
				return "", false
			}
		}
		u, err := url.Parse(dest)
		if err != nil || u.Opaque == "" || strings.Contains(u.Opaque, "@") == false {
			return "", false
		}
		return dest, true
	}
	if !ExternalURL(dest) {
		return "", false
	}
	return dest, true
}

func (w *writer) addr(sitePath string) string {
	if w.abs != nil {
		return absURL(w.abs, sitePath)
	}
	return rel(w.from, sitePath)
}

func absURL(base *url.URL, sitePath string) string {
	frag := ""
	if i := strings.IndexByte(sitePath, '#'); i >= 0 {
		sitePath, frag = sitePath[:i], sitePath[i:]
	}
	return strings.TrimRight(base.String(), "/") + "/" + escPath(sitePath) + frag
}

func plain(in []text.Inline, b *strings.Builder) {
	for _, n := range in {
		switch n.Kind {
		case text.IText, text.ICode:
			b.WriteString(strings.ReplaceAll(n.Text, "\n", " "))
		case text.IBreak:
			b.WriteByte(' ')
		case text.IEmph, text.IStrong, text.IStrike, text.ILink:
			plain(n.Kids, b)
		}
	}
}

func (w *writer) inlines(in []text.Inline) {
	for _, n := range in {
		switch n.Kind {
		case text.IText:
			w.out.WriteString(esc(strings.ReplaceAll(n.Text, "\n", " ")))
		case text.ICode:
			w.out.WriteString(`<code dir="ltr">` + esc(strings.ReplaceAll(n.Text, "\n", " ")) + "</code>")
		case text.IBreak:
			w.out.WriteString("<br/>")
		case text.IEmph:
			w.wrap("em", n.Kids)
		case text.IStrong:
			w.wrap("strong", n.Kids)
		case text.IStrike:
			w.wrap("del", n.Kids)
		case text.ILink:
			dest, ok := linkTarget(n.Text)
			if !ok {
				w.inlines(n.Kids)
				continue
			}
			w.out.WriteString(`<a href="` + esc(dest) + `" rel="noopener noreferrer">`)
			w.inlines(n.Kids)
			w.out.WriteString("</a>")
		case text.IImage:
			w.image(n)
		case text.INote:
			ref := fmt.Sprintf("fn-%d", n.Num)
			href := "#" + ref
			if w.abs != nil {
				href = w.frag + "#" + ref
			}
			fmt.Fprintf(&w.out, `<sup><a id="fnref-%d" href="%s">%d</a></sup>`, n.Num, esc(href), n.Num)
		}
	}
}

func (w *writer) image(n text.Inline) {
	alt := publish.CleanLine(n.Alt)
	p, why := w.imgs.get(n.Text)
	if p == nil {
		switch why {
		case "remote":
			w.remote++
		case "damaged":
			w.damaged++
		default:
			w.missing++
		}
		label := "[image"
		if alt != "" {
			label += ": " + alt
		}
		w.out.WriteString(`<span class="omitted">` + esc(label+"]") + `</span>`)
		return
	}
	w.imagesUsed++
	if p.rotated {
		w.rotated++
	}
	if alt == "" {
		w.missingAlt++
	}
	var dims string
	if p.width > 0 && p.height > 0 {
		dims = fmt.Sprintf(` width="%d" height="%d"`, p.width, p.height)
	}
	w.out.WriteString(`<img src="` + esc(w.addr(p.path)) + `" alt="` + esc(alt) + `"` + dims + ` loading="lazy" decoding="async"/>`)
}

func (w *writer) wrap(tag string, kids []text.Inline) {
	w.out.WriteString("<" + tag + ">")
	w.inlines(kids)
	w.out.WriteString("</" + tag + ">")
}

// blocks writes nodes. depth is how far a heading in the text sits below the
// page's own h1.
func (w *writer) blocks(nodes []text.Node, depth int) {
	for _, n := range nodes {
		switch n.Kind {
		case text.NPara:
			w.out.WriteString(`<p dir="auto">`)
			w.inlines(n.Inl)
			w.out.WriteString("</p>\n")
		case text.NHeading:
			lv := n.Level + depth
			if lv < 2 {
				lv = 2
			}
			if lv > 6 {
				lv = 6
			}
			fmt.Fprintf(&w.out, "<h%d dir=\"auto\">", lv)
			w.inlines(n.Inl)
			fmt.Fprintf(&w.out, "</h%d>\n", lv)
		case text.NRule:
			w.out.WriteString(`<p class="scene" role="separator">* * *</p>` + "\n")
		case text.NQuote:
			w.out.WriteString("<blockquote dir=\"auto\">\n")
			w.blocks(n.Kids, depth)
			w.out.WriteString("</blockquote>\n")
		case text.NList:
			tag, attr := "ul", ""
			if n.Ordered {
				tag = "ol"
				if n.Start > 1 {
					attr = fmt.Sprintf(` start="%d"`, n.Start)
				}
			}
			w.out.WriteString("<" + tag + ` dir="auto"` + attr + ">\n")
			for _, it := range n.Items {
				w.out.WriteString("<li>")
				w.listItem(it, depth)
				w.out.WriteString("</li>\n")
			}
			w.out.WriteString("</" + tag + ">\n")
		case text.NCode, text.NSource:
			w.out.WriteString(`<pre dir="ltr"><code>` + esc(n.Text) + "</code></pre>\n")
		case text.NTable:
			w.table(n)
		}
	}
}

func (w *writer) listItem(it []text.Node, depth int) {
	if len(it) == 1 && it[0].Kind == text.NPara {
		w.inlines(it[0].Inl)
		return
	}
	w.out.WriteString("\n")
	for _, k := range it {
		if k.Kind == text.NPara {
			w.out.WriteString(`<p dir="auto">`)
			w.inlines(k.Inl)
			w.out.WriteString("</p>\n")
		} else {
			w.blocks([]text.Node{k}, depth)
		}
	}
}

func (w *writer) table(n text.Node) {
	cols := 0
	for _, r := range n.Rows {
		cols = max(cols, len(r.Cells))
	}
	if cols == 0 {
		return
	}
	w.out.WriteString("<table>\n")
	var head, body []text.Row
	for _, r := range n.Rows {
		if r.Head {
			head = append(head, r)
		} else {
			body = append(body, r)
		}
	}
	emit := func(rows []text.Row, tag, cell string) {
		if len(rows) == 0 {
			return
		}
		w.out.WriteString("<" + tag + ">\n")
		for _, r := range rows {
			w.out.WriteString("<tr>")
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
				w.out.WriteString("<" + cell + attr + ">")
				w.inlines(c.Inl)
				w.out.WriteString("</" + cell + ">")
			}
			w.out.WriteString("</tr>\n")
		}
		w.out.WriteString("</" + tag + ">\n")
	}
	emit(head, "thead", "th")
	emit(body, "tbody", "td")
	w.out.WriteString("</table>\n")
}

// notes writes the footnotes a text refers to, after it.
func (w *writer) notes(notes []text.Footnote, used map[int]bool) {
	var kept []text.Footnote
	for _, f := range notes {
		if used[f.Num] {
			kept = append(kept, f)
		}
	}
	if len(kept) == 0 {
		return
	}
	w.out.WriteString(`<section class="footnotes" aria-label="Footnotes">` + "\n<ol>\n")
	for _, f := range kept {
		inner := &writer{from: w.from, abs: w.abs, frag: w.frag, imgs: w.imgs}
		inner.blocks(f.Nodes, 1)
		body := strings.TrimRight(inner.out.String(), "\n")
		back := fmt.Sprintf(` <a href="%s" aria-label="Back to the text">↩</a>`, esc(w.noteBack(f.Num)))
		if strings.HasSuffix(body, "</p>") {
			body = body[:len(body)-4] + back + "</p>"
		} else {
			body += "<p>" + strings.TrimSpace(back) + "</p>"
		}
		fmt.Fprintf(&w.out, "<li id=\"fn-%d\" value=\"%d\">%s</li>\n", f.Num, f.Num, body)
		w.missingAlt += inner.missingAlt
		w.imagesUsed += inner.imagesUsed
		w.remote += inner.remote
		w.missing += inner.missing
		w.damaged += inner.damaged
		w.rotated += inner.rotated
	}
	w.out.WriteString("</ol>\n</section>\n")
}

func (w *writer) noteBack(n int) string {
	if w.abs != nil {
		return w.frag + "#fnref-" + itoa(n)
	}
	return "#fnref-" + itoa(n)
}

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

// body is a parsed entry: its blocks and the notes it holds.
type body struct {
	nodes []text.Node
	notes []text.Footnote
	words int
}

// parse builds the structure of an entry's text and its scenes. Footnotes
// number on across them; their copies are bounded by one budget.
func parse(e Entry) body {
	budget := text.MaxNoteCopies
	var b body
	add := func(content string, first bool) {
		t := published(content)
		if strings.TrimSpace(t) == "" {
			return
		}
		doc := text.Structure(t, text.StructOptions{NoteStart: len(b.notes), NoteBudget: &budget})
		if !first && len(doc.Nodes) > 0 && len(b.nodes) > 0 {
			b.nodes = append(b.nodes, text.Node{Kind: text.NRule})
		}
		b.nodes = append(b.nodes, doc.Nodes...)
		b.notes = append(b.notes, doc.Notes...)
		b.words += text.WordCount(t)
	}
	add(e.Content, true)
	for _, s := range e.Scenes {
		add(s, len(b.nodes) == 0)
	}
	return b
}

// excerptOf is the first paragraph of the text as plain words, cut at a word
// boundary at 200 characters.
func excerptOf(nodes []text.Node) string {
	for _, n := range nodes {
		if n.Kind != text.NPara {
			continue
		}
		var b strings.Builder
		plain(n.Inl, &b)
		if t := publish.CleanLine(b.String()); t != "" {
			return cut(t, 200)
		}
	}
	return ""
}

// cut shortens s to at most max characters, at a space when there is one near
// the end, and adds an ellipsis.
func cut(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	c := string(r[:max])
	if i := strings.LastIndexByte(c, ' '); i > len(c)*6/10 {
		c = c[:i]
	}
	return strings.TrimRight(c, " ,;:-—–") + "…"
}
