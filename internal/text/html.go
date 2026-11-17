package text

import (
	"bytes"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/util"
)

// RenderHTML renders stored Markdown as an HTML fragment for a reading
// document (the HTML export). Manuscript text is untrusted, so this is a
// whitelist, not a filter: it walks the parse tree the counter reads and writes
// only the elements named here, with attributes it builds itself.
//
//   - Raw HTML in the text (blocks and inline) is shown as text, escaped, never
//     interpreted. HTML comments are dropped: the alternatives annotation
//     (`<!-- scriobh:alts ... -->`) is not prose and must not appear in it.
//   - A link keeps its address only for http, https, mailto and in-page
//     targets; any other destination leaves the link text.
//   - An image is kept only when it is a small inline data URI of a raster
//     type. Anything else (a remote address, a path) becomes a short note, so
//     the document makes no request.
//   - Front matter is dropped. %%Private notes%% are dropped unless asked for,
//     and then marked.
//   - Directive (`:::`) blocks, and any other block the editor holds as source,
//     are shown as written in a preformatted block.
//
// The text it renders is the text WordCount and SearchBody read: a test checks
// that over the whole editor corpus.
type HTMLOptions struct {
	// IDPrefix is put before the ids of footnotes, so several documents can
	// share one page.
	IDPrefix string
	// HeadingShift moves every heading down this many levels (at most h6).
	HeadingShift int
	// Private keeps %%private notes%%, marked as such, instead of dropping them.
	Private bool
	// WikiHref resolves the title of a [[wiki link]] to a relative address
	// (a fragment such as #chapter-3, or a path in the same bundle). A link that
	// does not resolve, or resolves to anything else, is shown as its text.
	WikiHref func(target string) (href string, ok bool)
	// LocalFiles also keeps an image whose address is exactly an attachment of this
	// installation, /files/att_<id>: the reading view is served from the same origin,
	// which serves that path to a signed-in person who may read it. Every other
	// address, remote or local, stays a note.
	LocalFiles bool
}

// RenderHTML renders md as described on HTMLOptions.
func RenderHTML(md string, o HTMLOptions) string {
	src := sanitize(body(normalize(md)))
	if !o.Private {
		src = dropSpanningNotes(src)
	}
	var b bytes.Buffer
	doc, ok := safeParseWith(src)
	if !ok {
		// The parser failed: show the text as one paragraph, still without the
		// parts that must never leave: comments (the alternatives annotation) and,
		// unless asked for, everything between %% marks (more than the pairing
		// rule would take; this is a last resort).
		t := reComment.ReplaceAllString(string(src), "")
		if !o.Private {
			t = rePrivateSpan.ReplaceAllString(t, "")
		}
		b.WriteString("<p>")
		escape(&b, t)
		b.WriteString("</p>\n")
		return b.String()
	}
	r := &htmlRenderer{src: src, o: o, w: &b, refs: map[string]int{}, defs: map[string]*footnoteDef{}, hiddenRef: map[string]bool{}, hasPrivate: bytes.Contains(src, []byte("%%"))}
	r.findDefs(doc)
	r.children(doc)
	r.footnotes()
	return b.String()
}

var (
	reComment     = regexp.MustCompile(`(?s)<!--.*?(?:-->|$)`)
	rePrivateSpan = regexp.MustCompile(`(?s)%%.*?(?:%%|$)`)
)

func safeParseWith(src []byte) (doc ast.Node, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			doc, ok = nil, false
		}
	}()
	return parseWith(newHTMLParser(), src), true
}

type htmlRenderer struct {
	src        []byte
	o          HTMLOptions
	w          *bytes.Buffer
	hasPrivate bool
	plan       *privatePlan
	refs       map[string]int // footnote label -> number, in order of first reference
	order      []string
	defs       map[string]*footnoteDef
	defOrder   []string
	hiddenRef  map[string]bool // footnote labels referenced only from a private note that was left out
	// cuts, when set, collects the source range of every private note the renderer pairs (from its
	// opening %% to the end of its closing %%): CleanMarkdown cuts exactly these.
	cuts *[][2]int
}

func escape(w *bytes.Buffer, s string) { w.WriteString(html.EscapeString(s)) }

// ---- footnotes --------------------------------------------------------------------

func (r *htmlRenderer) findDefs(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if c.Type() == ast.TypeInline {
			continue
		}
		if d, ok := c.(*footnoteDef); ok {
			if _, dup := r.defs[d.label]; !dup {
				r.defs[d.label] = d
				r.defOrder = append(r.defOrder, d.label)
			}
			continue // its own blocks are rendered with the list
		}
		r.findDefs(c)
	}
}

func (r *htmlRenderer) fnID(kind string, n int) string {
	return r.o.IDPrefix + kind + strconv.Itoa(n)
}

func (r *htmlRenderer) footnotes() {
	if len(r.defs) == 0 {
		return
	}
	labels := append([]string(nil), r.order...)
	seen := map[string]bool{}
	for _, l := range labels {
		seen[l] = true
	}
	for _, l := range r.defOrder {
		if !seen[l] && !r.hiddenRef[l] {
			r.number(l)
			labels = append(labels, l)
		}
	}
	r.w.WriteString("<section class=\"footnotes\" role=\"doc-endnotes\">\n<ol>\n")
	for _, l := range labels {
		d, ok := r.defs[l]
		if !ok {
			continue
		}
		n := r.refs[l]
		r.w.WriteString("<li id=\"" + r.fnID("fn", n) + "\">")
		var inner bytes.Buffer
		save := r.w
		r.w = &inner
		r.children(d)
		r.w = save
		body := strings.TrimRight(inner.String(), "\n")
		back := " <a class=\"fnback\" href=\"#" + r.fnID("fnref", n) + "\" aria-label=\"Back to the text\">↩</a>"
		if strings.HasSuffix(body, "</p>") {
			body = strings.TrimSuffix(body, "</p>") + back + "</p>"
		} else {
			body += back
		}
		r.w.WriteString(body)
		r.w.WriteString("</li>\n")
	}
	r.w.WriteString("</ol>\n</section>\n")
}

func (r *htmlRenderer) number(label string) int {
	if n, ok := r.refs[label]; ok {
		return n
	}
	r.order = append(r.order, label)
	r.refs[label] = len(r.order)
	return len(r.order)
}

// ---- blocks ------------------------------------------------------------------------

func (r *htmlRenderer) children(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		r.block(c)
	}
}

func (r *htmlRenderer) block(n ast.Node) {
	w := r.w
	switch v := n.(type) {
	case *footnoteDef:
		return // rendered at the end
	case *directive:
		w.WriteString("<pre class=\"source\">")
		escape(w, strings.TrimRight(string(r.src[v.start:v.stop]), "\r\n"))
		w.WriteString("</pre>\n")
		return
	case *ast.Heading:
		lv := v.Level + r.o.HeadingShift
		if lv > 6 {
			lv = 6
		}
		if lv < 1 {
			lv = 1
		}
		tag := "h" + strconv.Itoa(lv)
		w.WriteString("<" + tag + ">")
		r.inlineBlock(v)
		w.WriteString("</" + tag + ">\n")
	case *ast.Paragraph:
		w.WriteString("<p>")
		r.inlineBlock(v)
		w.WriteString("</p>\n")
	case *ast.TextBlock:
		r.inlineBlock(v)
		if v.NextSibling() != nil {
			w.WriteString("\n")
		}
	case *ast.Blockquote:
		w.WriteString("<blockquote>\n")
		r.children(v)
		w.WriteString("</blockquote>\n")
	case *ast.List:
		tag := "ul"
		if v.IsOrdered() {
			tag = "ol"
		}
		w.WriteString("<" + tag)
		if v.IsOrdered() && v.Start != 1 && v.Start >= 0 {
			w.WriteString(" start=\"" + strconv.Itoa(v.Start) + "\"")
		}
		w.WriteString(">\n")
		r.children(v)
		w.WriteString("</" + tag + ">\n")
	case *ast.ListItem:
		w.WriteString("<li>")
		if f := v.FirstChild(); f != nil && f.Kind() != ast.KindTextBlock {
			w.WriteString("\n")
		}
		r.children(v)
		w.WriteString("</li>\n")
	case *ast.ThematicBreak:
		w.WriteString("<hr class=\"scene\">\n")
	case *ast.FencedCodeBlock:
		lang := ""
		if l := v.Language(r.src); l != nil {
			lang = safeClass(string(l))
		}
		r.code(v, lang)
	case *ast.CodeBlock:
		r.code(v, "")
	case *ast.HTMLBlock:
		var raw strings.Builder
		for i := 0; i < v.Lines().Len(); i++ {
			ln := v.Lines().At(i)
			raw.Write(ln.Value(r.src))
		}
		if v.HasClosure() {
			raw.Write(v.ClosureLine.Value(r.src))
		}
		text := strings.TrimRight(raw.String(), "\r\n")
		if isComment(text) {
			return
		}
		w.WriteString("<pre class=\"source\">")
		escape(w, text)
		w.WriteString("</pre>\n")
	case *extast.Table:
		w.WriteString("<table>\n")
		r.children(v)
		w.WriteString("</table>\n")
	case *extast.TableHeader:
		w.WriteString("<thead><tr>\n")
		r.cells(v, "th")
		w.WriteString("</tr></thead>\n")
	case *extast.TableRow:
		w.WriteString("<tr>\n")
		r.cells(v, "td")
		w.WriteString("</tr>\n")
	default:
		// A container this renderer does not know: show what is inside it.
		if n.Type() == ast.TypeInline {
			return
		}
		r.children(n)
	}
}

func (r *htmlRenderer) cells(row ast.Node, tag string) {
	for c := row.FirstChild(); c != nil; c = c.NextSibling() {
		cell, ok := c.(*extast.TableCell)
		if !ok {
			continue
		}
		class := ""
		switch cell.Alignment {
		case extast.AlignLeft:
			class = " class=\"al\""
		case extast.AlignRight:
			class = " class=\"ar\""
		case extast.AlignCenter:
			class = " class=\"ac\""
		}
		r.w.WriteString("<" + tag + class + ">")
		r.inlineBlock(cell)
		r.w.WriteString("</" + tag + ">\n")
	}
}

func (r *htmlRenderer) code(n ast.Node, lang string) {
	r.w.WriteString("<pre><code")
	if lang != "" {
		r.w.WriteString(" class=\"language-" + lang + "\"")
	}
	r.w.WriteString(">")
	for i := 0; i < n.Lines().Len(); i++ {
		ln := n.Lines().At(i)
		escape(r.w, string(ln.Value(r.src)))
	}
	r.w.WriteString("</code></pre>\n")
}

var reClass = regexp.MustCompile(`[^a-z0-9_+-]`)

func safeClass(s string) string {
	s = strings.ToLower(s)
	if i := strings.IndexAny(s, " \t{"); i >= 0 {
		s = s[:i]
	}
	s = reClass.ReplaceAllString(s, "")
	if len(s) > 24 {
		s = s[:24]
	}
	return s
}

func isComment(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "<!--") {
		return false
	}
	end := strings.Index(s, "-->")
	return end >= 0 && strings.TrimSpace(s[end+3:]) == ""
}

// ---- inline ------------------------------------------------------------------------

// inlineBlock renders the inline children of a textblock, with its private notes
// planned first.
func (r *htmlRenderer) inlineBlock(n ast.Node) {
	r.plan = nil
	if r.hasPrivate {
		r.plan = r.planPrivate(n)
	}
	r.inlines(n)
	r.plan = nil
}

func (r *htmlRenderer) inlines(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		r.inline(c)
	}
}

// hidden reports whether a leaf is inside a private note that is not wanted;
// open and shut wrap a kept note's leaf.
func (r *htmlRenderer) private(n ast.Node) bool { return r.plan != nil && r.plan.leaf[n] }

func (r *htmlRenderer) inline(n ast.Node) {
	w := r.w
	switch v := n.(type) {
	case *ast.Text:
		r.textNode(v)
	case *ast.String:
		r.leaf(n, func() { escape(w, string(v.Value)) })
	case *rawText:
		r.leaf(n, func() { r.writeText(v.v) })
	case *ast.CodeSpan:
		r.leaf(n, func() {
			w.WriteString("<code>")
			for c := v.FirstChild(); c != nil; c = c.NextSibling() {
				if t, ok := c.(*ast.Text); ok {
					escape(w, strings.ReplaceAll(string(t.Segment.Value(r.src)), "\n", " "))
				}
			}
			w.WriteString("</code>")
		})
	case *ast.Emphasis:
		tag := "em"
		if v.Level >= 2 {
			tag = "strong"
		}
		r.wrapped("<"+tag+">", "</"+tag+">", v)
	case *extast.Strikethrough:
		r.wrapped("<del>", "</del>", v)
	case *ast.Link:
		href, ok := safeHref(v.Destination)
		if !ok {
			r.inlines(v)
			return
		}
		open := "<a href=\"" + href + "\" rel=\"noopener noreferrer\" referrerpolicy=\"no-referrer\""
		if t := v.Title; len(t) > 0 {
			open += " title=\"" + html.EscapeString(unescapeAll(t)) + "\""
		}
		// Its address and title are written only if some of its text is shown: a
		// link whose text sits inside a private note must not leave either behind.
		r.wrapped(open+">", "</a>", v)
	case *ast.AutoLink:
		label := string(v.Label(r.src))
		r.leaf(n, func() {
			if href, ok := safeHref(v.URL(r.src)); ok {
				w.WriteString("<a href=\"" + href + "\" rel=\"noopener noreferrer\" referrerpolicy=\"no-referrer\">")
				escape(w, label)
				w.WriteString("</a>")
			} else {
				escape(w, label)
			}
		})
	case *ast.Image:
		r.leaf(n, func() { r.image(v) })
	case *ast.RawHTML:
		r.leaf(n, func() {
			var raw strings.Builder
			for i := 0; i < v.Segments.Len(); i++ {
				seg := v.Segments.At(i)
				raw.Write(seg.Value(r.src))
			}
			if strings.HasPrefix(raw.String(), "<!--") {
				return
			}
			w.WriteString("<code class=\"raw\">")
			escape(w, raw.String())
			w.WriteString("</code>")
		})
	case *literal:
		r.inlines(v)
	case *wikiLink:
		r.leaf(n, func() {
			if r.o.WikiHref != nil {
				if href, ok := r.o.WikiHref(v.target); ok && safeRel(href) {
					w.WriteString("<a class=\"wiki\" href=\"" + html.EscapeString(href) + "\">")
					escape(w, v.label)
					w.WriteString("</a>")
					return
				}
			}
			w.WriteString("<span class=\"wiki\">")
			escape(w, v.label)
			w.WriteString("</span>")
		})
	case *footnoteRef:
		if r.private(n) && !r.o.Private {
			r.hiddenRef[v.label] = true // its note is not shown, so neither is the text it points at
			return
		}
		r.leaf(n, func() {
			num := r.number(v.label)
			if _, ok := r.defs[v.label]; ok {
				w.WriteString("<sup class=\"fnref\" id=\"" + r.fnID("fnref", num) + "\"><a href=\"#" + r.fnID("fn", num) + "\" role=\"doc-noteref\">" + strconv.Itoa(num) + "</a></sup>")
			} else {
				w.WriteString("<sup class=\"fnref\">" + strconv.Itoa(num) + "</sup>")
			}
		})
	default:
		r.inlines(n)
	}
}

// wrapped writes an element around n's children, or nothing when the children
// came out empty (a private note that took everything inside the emphasis).
func (r *htmlRenderer) wrapped(open, close string, n ast.Node) {
	save := r.w
	var inner bytes.Buffer
	r.w = &inner
	r.inlines(n)
	r.w = save
	if inner.Len() == 0 {
		return
	}
	r.w.WriteString(open)
	r.w.Write(inner.Bytes())
	r.w.WriteString(close)
}

// leaf writes one inline leaf, honouring the private-note plan.
func (r *htmlRenderer) leaf(n ast.Node, fn func()) {
	if r.private(n) {
		if !r.o.Private {
			return
		}
		r.w.WriteString("<span class=\"private-note\">")
		fn()
		r.w.WriteString("</span>")
		return
	}
	fn()
}

func (r *htmlRenderer) textNode(v *ast.Text) {
	segs := []span{{start: v.Segment.Start, stop: v.Segment.Stop}}
	if r.plan != nil {
		if s, ok := r.plan.text[v]; ok {
			segs = s
		}
	}
	lastHidden := false
	for _, s := range segs {
		if s.stop <= s.start {
			continue
		}
		switch {
		case s.private && !r.o.Private:
			lastHidden = true
		case s.private:
			r.w.WriteString("<span class=\"private-note\">")
			r.writeText(r.src[s.start:s.stop])
			r.w.WriteString("</span>")
			lastHidden = false
		default:
			r.writeText(r.src[s.start:s.stop])
			lastHidden = false
		}
	}
	switch {
	case v.HardLineBreak():
		r.w.WriteString("<br>\n")
	case v.SoftLineBreak():
		if !lastHidden {
			r.w.WriteString("\n")
		}
	}
}

// writeText writes raw inline text with its backslash escapes and character
// references resolved, HTML-escaped.
func (r *htmlRenderer) writeText(raw []byte) {
	start := 0
	flush := func(to int) {
		if to > start {
			escape(r.w, string(raw[start:to]))
		}
	}
	for i := 0; i < len(raw); {
		switch c := raw[i]; {
		case c == '\\' && i+1 < len(raw) && util.IsPunct(raw[i+1]):
			flush(i)
			escape(r.w, string(raw[i+1:i+2]))
			i += 2
			start = i
		case c == '&':
			if dec, n := entity(raw[i:]); n > 0 {
				flush(i)
				escape(r.w, dec)
				i += n
				start = i
			} else {
				i++
			}
		default:
			i++
		}
	}
	flush(len(raw))
}

var reDataImage = regexp.MustCompile(`^data:image/(?:gif|png|jpeg|webp);base64,[A-Za-z0-9+/=]+$`)

// reLocalFile is an attachment address as the editor writes it.
var reLocalFile = regexp.MustCompile(`^/files/att_[0-9A-Za-z]{10,40}$`)

// maxDataImage is the longest inline image kept: a larger one would turn a
// reading document into a download of its pictures.
const maxDataImage = 512 << 10

func (r *htmlRenderer) image(v *ast.Image) {
	alt := plainOf(v, r.src)
	dest := strings.TrimSpace(unescapeAll(v.Destination))
	if r.o.LocalFiles && reLocalFile.MatchString(dest) {
		r.w.WriteString("<img src=\"" + dest + "\" alt=\"")
		escape(r.w, alt)
		r.w.WriteString("\" loading=\"lazy\">")
		return
	}
	if len(dest) <= maxDataImage && reDataImage.MatchString(dest) {
		r.w.WriteString("<img src=\"" + dest + "\" alt=\"")
		escape(r.w, alt)
		r.w.WriteString("\">")
		return
	}
	r.w.WriteString("<span class=\"omitted\">[image")
	if alt != "" {
		r.w.WriteString(": ")
		escape(r.w, alt)
	}
	r.w.WriteString(" (not included)]</span>")
}

func plainOf(n ast.Node, src []byte) string {
	var b strings.Builder
	var walk func(ast.Node)
	walk = func(x ast.Node) {
		for c := x.FirstChild(); c != nil; c = c.NextSibling() {
			switch t := c.(type) {
			case *ast.Text:
				b.WriteString(unescapeAll(t.Segment.Value(src)))
			case *ast.String:
				b.Write(t.Value)
			case *rawText:
				b.WriteString(unescapeAll(t.v))
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

var reCtl = regexp.MustCompile(`[\x00-\x20\x7f-\x9f]`)

// safeHref returns an attribute-safe address for a link destination, or false
// when the link should not be a link: only http, https, mailto and in-page
// fragments are kept.
func safeHref(raw []byte) (string, bool) {
	dest := unescapeAll(raw)
	probe := strings.ToLower(reCtl.ReplaceAllString(dest, ""))
	switch {
	case strings.HasPrefix(probe, "http://"), strings.HasPrefix(probe, "https://"), strings.HasPrefix(probe, "mailto:"):
	case strings.HasPrefix(probe, "#") && !strings.ContainsAny(probe, ":"):
	default:
		return "", false
	}
	return html.EscapeString(string(util.URLEscape([]byte(dest), false))), true
}

// safeRel reports whether a wiki link's resolved address is a relative
// reference (an in-page fragment or a path in the same bundle): no scheme, no
// root, no backslash.
func safeRel(h string) bool {
	return h != "" && !strings.ContainsAny(h, ":\\\x00\r\n\t \"'<>") && !strings.HasPrefix(h, "/")
}

// ---- private notes -------------------------------------------------------------------

type span struct {
	start, stop int
	private     bool
}

type privatePlan struct {
	text map[*ast.Text][]span // every text node of the block, cut at its delimiters
	leaf map[ast.Node]bool    // the other leaves that sit inside a note
}

// planPrivate finds the %%private notes%% of one textblock by the rule the
// counter uses (pairPrivate): delimiters are looked for in plain text only,
// taken in order and paired two by two; an empty pair and an unpaired last
// delimiter stay as text. It cuts the text nodes at the delimiters and marks
// what lies between each pair.
func (r *htmlRenderer) planPrivate(block ast.Node) *privatePlan {
	type leaf struct {
		node ast.Node
		txt  *ast.Text
	}
	var leaves []leaf
	var collect func(ast.Node)
	collect = func(x ast.Node) {
		for c := x.FirstChild(); c != nil; c = c.NextSibling() {
			switch v := c.(type) {
			case *ast.Text:
				leaves = append(leaves, leaf{node: c, txt: v})
			case *ast.String, *ast.CodeSpan, *ast.AutoLink, *ast.Image, *ast.RawHTML, *wikiLink, *footnoteRef, *rawText:
				leaves = append(leaves, leaf{node: c})
			default:
				collect(c)
			}
		}
	}
	collect(block)
	type hit struct{ leaf, at int } // at is an absolute source offset
	var hits []hit
	for i, l := range leaves {
		if l.txt == nil {
			continue
		}
		raw := l.txt.Segment.Value(r.src)
		for j := 0; j < len(raw); {
			switch {
			case raw[j] == '\\' && j+1 < len(raw) && util.IsPunct(raw[j+1]):
				j += 2
			case raw[j] == '&':
				if _, n := entity(raw[j:]); n > 0 {
					j += n
				} else {
					j++
				}
			case raw[j] == '%' && j+1 < len(raw) && raw[j+1] == '%':
				hits = append(hits, hit{i, l.txt.Segment.Start + j})
				j += 2
			default:
				j++
			}
		}
	}
	if len(hits) < 2 {
		return nil
	}
	var used []hit
	for h := 0; h+1 < len(hits); {
		a, b := hits[h], hits[h+1]
		if a.leaf == b.leaf && b.at == a.at+2 {
			h += 2
			continue
		}
		used = append(used, a, b)
		h += 2
	}
	if len(used) == 0 {
		return nil
	}
	if r.cuts != nil {
		for i := 0; i+1 < len(used); i += 2 {
			*r.cuts = append(*r.cuts, [2]int{used[i].at, used[i+1].at + 2})
		}
	}
	plan := &privatePlan{text: map[*ast.Text][]span{}, leaf: map[ast.Node]bool{}}
	byLeaf := map[int][]int{}
	for _, u := range used {
		byLeaf[u.leaf] = append(byLeaf[u.leaf], u.at)
	}
	inNote := false
	n := 0
	for i, l := range leaves {
		if l.txt == nil {
			if inNote {
				plan.leaf[l.node] = true
			}
			continue
		}
		pos := l.txt.Segment.Start
		var segs []span
		for _, at := range byLeaf[i] {
			if at > pos {
				segs = append(segs, span{pos, at, inNote})
			}
			inNote = n%2 == 0
			n++
			pos = at + 2
		}
		if pos < l.txt.Segment.Stop {
			segs = append(segs, span{pos, l.txt.Segment.Stop, inNote})
		}
		plan.text[l.txt] = segs
	}
	return plan
}
