package imports

import (
	"errors"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// HTML to Markdown, for the safe subset a post is written in. It reads the
// tokens of golang.org/x/net/html (a tokenizer, never a tree, so nesting costs a
// counter and a deep document costs the same as a flat one) and writes only
// what a list of elements says: paragraphs, headings, lists, quotes, code,
// links, images as a note, rules, simple tables, emphasis, footnotes. Everything
// else is dropped, or unwrapped so its text stays. Nothing is ever passed
// through as HTML: every character of text reaches the Markdown by way of the
// sink's escaping, so a post cannot make a heading, a list, a wiki link, a
// private note, a ghost or a directive that it did not write as one.
//
// Never done: fetching anything. An image is a note with its description; its
// address is counted and, for the first few, reported, never opened.

const (
	htmlMaxDepth = 200
	htmlMaxSegs  = 200_000 // segments in one paragraph before the formatting is merged away
)

// htmlStats is what the walker met and did not keep.
type htmlStats struct {
	images, scripts, iframes, forms, embeds, media, comments int
	badLinks, internalLinks, links, tables                   int
	deep, notes, tooBig                                      int
	imageSrc                                                 []string // the first few addresses, as written, for the report
	dropped                                                  map[string]int
	shortcodes                                               map[string]int
	// pic makes the picture an address names, when its bytes came with the export
	// (never fetched); placed counts them.
	pic    func(src, alt string) (seg, bool)
	placed int
}

func (st *htmlStats) drop(tag string) {
	if st.dropped == nil {
		st.dropped = map[string]int{}
	}
	st.dropped[tag]++
}

// add folds another walk's counts in.
func (st *htmlStats) add(o *htmlStats) {
	st.images += o.images
	st.scripts += o.scripts
	st.iframes += o.iframes
	st.forms += o.forms
	st.embeds += o.embeds
	st.media += o.media
	st.comments += o.comments
	st.badLinks += o.badLinks
	st.internalLinks += o.internalLinks
	st.links += o.links
	st.tables += o.tables
	st.deep += o.deep
	st.notes += o.notes
	st.tooBig += o.tooBig
	for _, s := range o.imageSrc {
		if len(st.imageSrc) < 20 {
			st.imageSrc = append(st.imageSrc, s)
		}
	}
	for k, v := range o.dropped {
		if st.dropped == nil {
			st.dropped = map[string]int{}
		}
		st.dropped[k] += v
	}
	for k, v := range o.shortcodes {
		if st.shortcodes == nil {
			st.shortcodes = map[string]int{}
		}
		st.shortcodes[k] += v
	}
}

// htmlOpts carry what a walk needs beside the markup.
type htmlOpts struct {
	// notes are the footnotes of a post by id (WordPress keeps them in a post's
	// metadata, and the text refers to them by id), as HTML.
	notes map[string]string
	// classic is a post written in the classic editor: its text is not wrapped in
	// paragraphs, so a blank line is a paragraph and a single new line a line break
	// (what WordPress does when it shows the post), whatever tags it also has.
	classic bool
	// shortcodeNotes are the contents of [footnote]...[/footnote] shortcodes taken out of the text.
	scNotes []string
	// xhtml reads the markup of an EPUB's content file: namespace prefixes on elements are ignored (a
	// <svg:script> is a script), a paragraph that is a scene break by its text or its class becomes one,
	// ruby annotations are left out, and the next three apply.
	xhtml bool
	// noteLink says whether a link is a reference to a footnote of the book, and gives the note's HTML.
	noteLink func(href string) (key, html string, ok bool)
	// classFmt is what the book's stylesheets say a class does (italic, bold, strike): many books are
	// made with spans whose classes carry the emphasis.
	classFmt map[string]cssFmt
	// dropClasses are classes whose elements are not text of the book (the "THE END" line this program's own
	// EPUB export writes).
	dropClasses map[string]bool
}

var (
	// dropTags are elements whose content is never text.
	dropTags = map[string]string{
		"script": "script", "style": "style", "iframe": "iframe", "object": "embed", "embed": "embed", "form": "form",
		"noscript": "noscript", "template": "template", "svg": "svg", "canvas": "canvas", "audio": "media", "video": "media",
		"button": "form", "select": "form", "textarea": "form", "input": "form", "head": "head", "title": "head", "math": "math",
		"link": "head", "meta": "head", "base": "head", "applet": "embed", "frameset": "embed", "frame": "embed", "dialog": "dialog",
		"datalist": "form", "fieldset": "form", "option": "form", "optgroup": "form", "map": "map", "source": "media", "track": "media",
	}
	voidTags = map[string]bool{
		"br": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "base": true, "embed": true, "source": true,
		"track": true, "wbr": true, "area": true, "col": true, "param": true, "frame": true,
	}
	blockTags = map[string]bool{
		"p": true, "div": true, "section": true, "article": true, "aside": true, "header": true, "footer": true, "main": true, "nav": true,
		"figure": true, "figcaption": true, "address": true, "details": true, "summary": true, "dl": true, "dt": true, "dd": true,
		"center": true, "hgroup": true,
	}
)

type hframe struct {
	tag     string
	b, i, s bool
	code    bool
	link    string
	list    bool // a list
	ordered bool
	quote   bool
	pre     bool
	num     int
}

// hinl accumulates the inline content of one block.
type hinl struct {
	segs []seg
	run  strings.Builder
	cur  seg // the formatting of the open run
	open bool
}

func (h *hinl) flushRun() {
	if h.open && h.run.Len() > 0 {
		s := h.cur
		s.text = h.run.String()
		h.segs = append(h.segs, s)
	}
	h.run.Reset()
	h.open = false
}

func (h *hinl) empty() bool { return len(h.segs) == 0 && h.run.Len() == 0 }

func sameFmt(a, b seg) bool {
	return a.b == b.b && a.i == b.i && a.s == b.s && a.code == b.code && a.link == b.link
}

// addText adds text with the formatting f. spaces collapse: white space is one
// space, and none at the start of a block or after a break.
func (h *hinl) addText(t string, f seg, collapse bool) {
	if t == "" {
		return
	}
	if collapse {
		var b strings.Builder
		b.Grow(len(t))
		space := h.endsSpace()
		for i := 0; i < len(t); i++ {
			c := t[i]
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' {
				if !space {
					b.WriteByte(' ')
					space = true
				}
				continue
			}
			// a no-break space is a space too
			if c == 0xC2 && i+1 < len(t) && t[i+1] == 0xA0 {
				if !space {
					b.WriteByte(' ')
					space = true
				}
				i++
				continue
			}
			b.WriteByte(c)
			space = false
		}
		t = b.String()
		if t == "" {
			return
		}
	}
	if len(h.segs) > htmlMaxSegs {
		f = seg{} // too many pieces: the formatting goes, the text stays
	}
	if h.open && sameFmt(h.cur, f) {
		h.run.WriteString(t)
		return
	}
	h.flushRun()
	h.cur, h.open = f, true
	h.run.WriteString(t)
}

func (h *hinl) endsSpace() bool {
	if h.run.Len() > 0 {
		s := h.run.String()
		return s[len(s)-1] == ' '
	}
	if len(h.segs) == 0 {
		return true
	}
	l := h.segs[len(h.segs)-1]
	if l.brk {
		return true
	}
	return l.text != "" && l.text[len(l.text)-1] == ' '
}

func (h *hinl) addSeg(s seg) {
	h.flushRun()
	h.segs = append(h.segs, s)
}

// take returns the segments, trimmed, and empties the buffer.
func (h *hinl) take() []seg {
	h.flushRun()
	segs := h.segs
	h.segs = nil
	// leading and trailing white space and breaks are nothing
	for len(segs) > 0 {
		f := &segs[0]
		if f.brk {
			segs = segs[1:]
			continue
		}
		if f.text != "" && f.note == "" && !f.priv && f.wiki == "" {
			if t := strings.TrimLeft(f.text, " "); t != f.text {
				f.text = t
			}
			if f.text == "" {
				segs = segs[1:]
				continue
			}
		}
		break
	}
	for len(segs) > 0 {
		l := &segs[len(segs)-1]
		if l.brk {
			segs = segs[:len(segs)-1]
			continue
		}
		if l.text != "" && l.note == "" && !l.priv && l.wiki == "" {
			if t := strings.TrimRight(l.text, " "); t != l.text {
				l.text = t
			}
			if l.text == "" {
				segs = segs[:len(segs)-1]
				continue
			}
		}
		break
	}
	return segs
}

type hwalk struct {
	s     *sink
	st    *htmlStats
	o     htmlOpts
	inl   hinl
	stack []hframe
	over  int // tags opened beyond htmlMaxDepth

	noteCache map[string]string // footnotes already written, by the key the post refers to them by

	skipTag   string
	skipDepth int

	listSeq    int
	heading    int // inside a heading of this level (its text is collected)
	headText   strings.Builder
	pre        bool
	preText    strings.Builder
	liOpen     bool
	liDepth    int // list nesting of the open item
	liOrdered  bool
	liNum      int
	liID       string
	table      *htab
	tableDepth int
	cell       bool
	inlineOnly bool // collect inline segments only: blocks become breaks
	noteSkip   int  // depth inside a footnote marker whose text is not wanted
	blockClass string
	err        error
}

type htab struct {
	rows  [][]string
	notes []string
	cur   []string
}

// htmlToBlocks writes an HTML fragment to the sink.
func htmlToBlocks(s *sink, src string, o htmlOpts, st *htmlStats) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("the HTML could not be read")
		}
	}()
	w := &hwalk{s: s, st: st, o: o}
	w.run(src)
	w.finish()
	return w.err
}

// htmlInline reads a fragment (a footnote) as segments in one paragraph.
func htmlInline(s *sink, src string, o htmlOpts, st *htmlStats) []seg {
	w := &hwalk{s: s, st: st, o: o, inlineOnly: true}
	w.run(src)
	return w.inl.take()
}

func (w *hwalk) fmtNow() seg {
	var f seg
	for i := range w.stack {
		fr := &w.stack[i]
		f.b = f.b || fr.b
		f.i = f.i || fr.i
		f.s = f.s || fr.s
		f.code = f.code || fr.code
		if fr.link != "" {
			f.link = fr.link
		}
	}
	return f
}

func (w *hwalk) run(src string) {
	z := html.NewTokenizer(strings.NewReader(src))
	// No limit on one token: the input is already bounded, and a limit would end the
	// post at a long text, comment or data address without a word.
	for w.err == nil {
		tt := z.Next()
		if tt == html.ErrorToken {
			if err := z.Err(); err != io.EOF {
				w.st.tooBig++ // not the end of the input: something the tokenizer would not read
			}
			return
		}
		if w.s.overflow() || w.s.size() > 2*MaxTotalBytes {
			w.err = errors.New("the text is larger than the import limit")
			return
		}
		switch tt {
		case html.TextToken:
			if w.skipTag != "" || w.noteSkip > 0 {
				continue
			}
			w.text(string(z.Text()))
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)
			if w.o.xhtml {
				tag = stripNS(tag)
			}
			var attrs map[string]string
			if hasAttr {
				attrs = map[string]string{}
				for {
					k, v, more := z.TagAttr()
					if _, dup := attrs[string(k)]; !dup && len(attrs) < 32 {
						attrs[string(k)] = string(v)
					}
					if !more {
						break
					}
				}
			}
			w.start(tag, attrs, tt == html.SelfClosingTagToken)
		case html.EndTagToken:
			name, _ := z.TagName()
			if w.o.xhtml {
				w.end(stripNS(string(name)))
			} else {
				w.end(string(name))
			}
		case html.CommentToken:
			w.st.comments++
		}
	}
}

func (w *hwalk) text(t string) {
	if w.pre {
		if w.preText.Len() < 4<<20 {
			w.preText.WriteString(t)
		}
		return
	}
	if w.heading > 0 {
		w.headText.WriteString(t)
		return
	}
	t = w.stripShortcodes(t)
	f := w.fmtNow()
	// Footnote markers made out of [footnote] shortcodes.
	for {
		i := strings.Index(t, noteOpen)
		if i < 0 {
			break
		}
		j := strings.Index(t[i:], noteClose)
		if j < 0 {
			break
		}
		w.prose(t[:i], f)
		w.noteMark(t[i+len(noteOpen):i+j], f)
		t = t[i+j+len(noteClose):]
	}
	w.prose(t, f)
}

// prose adds a run of text. In a classic post (outside a list item or a table)
// a blank line ends the paragraph and a single new line is a line break.
func (w *hwalk) prose(t string, f seg) {
	if !w.o.classic || w.liOpen || w.table != nil || !strings.Contains(t, "\n") {
		w.inl.addText(t, f, true)
		return
	}
	for i, para := range splitBlank(t) {
		if i > 0 {
			w.boundary()
		}
		for j, line := range strings.Split(para, "\n") {
			if j > 0 && !w.inl.empty() && !w.lastIsBreak() {
				w.inl.addSeg(seg{brk: true})
			}
			w.inl.addText(line, f, true)
		}
	}
}

// noteMark adds the footnote a shortcode marker stands for.
func (w *hwalk) noteMark(idx string, f seg) {
	n := 0
	for _, c := range idx {
		if c < '0' || c > '9' || n > 1<<20 {
			return
		}
		n = n*10 + int(c-'0')
	}
	if n < 0 || n >= len(w.o.scNotes) || w.inlineOnly {
		return
	}
	w.noteRef("sc"+itoa(n), w.o.scNotes[n])
}

// noteRef adds a reference to the footnote named key, whose text is the HTML given.
// A note is read and written once however many times the post refers to it (a
// hundred thousand references to a note of a hundred kilobytes are one note), and
// once the notes of a document are as many or as large as they may be, no more are
// read.
func (w *hwalk) noteRef(key, h string) {
	id, seen := w.noteCache[key]
	if !seen {
		if w.s.notesFull() {
			w.st.tooBig++
			return
		}
		segs := htmlInline(w.s, h, htmlOpts{xhtml: w.o.xhtml, classFmt: w.o.classFmt}, w.st)
		id = w.s.note([][]seg{segs})
		if w.noteCache == nil {
			w.noteCache = map[string]string{}
		}
		w.noteCache[key] = id
		if id != "" {
			w.st.notes++
		}
	}
	if id != "" {
		w.inl.addSeg(seg{note: id})
	}
}

func (w *hwalk) inBlockCtx() (quote, inLi bool) {
	for i := range w.stack {
		quote = quote || w.stack[i].quote
	}
	return quote, w.liOpen
}

// boundary ends the paragraph in progress.
func (w *hwalk) boundary() {
	if w.table != nil && w.cell {
		if !w.inl.empty() {
			w.inl.addText(" ", seg{}, false)
		}
		return
	}
	if w.liOpen || w.inlineOnly {
		if !w.inl.empty() && !w.lastIsBreak() {
			w.inl.addSeg(seg{brk: true})
		}
		return
	}
	w.flush()
}

// leaveItem ends what is in progress before a block that is written at once (a code
// block, a table): the text of an open list item is written first, so the block
// comes after it, as it does in the post; anything else is a paragraph that ends.
func (w *hwalk) leaveItem() {
	if w.liOpen && !w.inlineOnly && w.table == nil {
		w.flushItem()
		w.liOpen = false
		return
	}
	w.boundary()
}

func (w *hwalk) lastIsBreak() bool {
	if w.inl.run.Len() > 0 {
		return false
	}
	n := len(w.inl.segs)
	return n > 0 && w.inl.segs[n-1].brk
}

// flush writes the paragraph in progress.
func (w *hwalk) flush() {
	segs := w.inl.take()
	cls := w.blockClass
	w.blockClass = ""
	if w.o.xhtml && !w.inlineOnly && w.table == nil && !w.liOpen && sceneBlock(segs, cls) {
		if q, _ := w.inBlockCtx(); !q {
			if n := len(w.s.c.blocks); n == 0 || w.s.c.blocks[n-1].kind != bScene {
				w.s.scene()
			}
			return
		}
	}
	if len(segs) == 0 {
		return
	}
	if q, _ := w.inBlockCtx(); q {
		w.s.quote(segs)
		return
	}
	w.s.para(segs)
}

func (w *hwalk) flushItem() {
	if !w.liOpen {
		return
	}
	segs := w.inl.take()
	if len(segs) == 0 {
		return
	}
	if q, _ := w.inBlockCtx(); q {
		w.s.quote(append([]seg{{text: "• "}}, segs...))
		return
	}
	w.s.item(segs, w.liDepth, w.liOrdered, w.liNum, w.liID)
}

func (w *hwalk) listFrames() int {
	n := 0
	for i := range w.stack {
		if w.stack[i].list {
			n++
		}
	}
	return n
}

func (w *hwalk) push(f hframe) {
	if len(w.stack) >= htmlMaxDepth {
		w.over++
		w.st.deep++
		return
	}
	w.stack = append(w.stack, f)
}

// popTo closes the innermost open frame named tag, and what was opened inside it.
func (w *hwalk) popTo(tag string) bool {
	if w.over > 0 && !w.hasFrame(tag) {
		w.over--
		return true
	}
	for i := len(w.stack) - 1; i >= 0; i-- {
		if w.stack[i].tag == tag {
			w.stack = w.stack[:i]
			return true
		}
	}
	return false
}

func (w *hwalk) hasFrame(tag string) bool {
	for i := len(w.stack) - 1; i >= 0; i-- {
		if w.stack[i].tag == tag {
			return true
		}
	}
	return false
}

func (w *hwalk) start(tag string, attrs map[string]string, selfClosing bool) {
	if w.skipTag != "" {
		if tag == w.skipTag && !voidTags[tag] && !selfClosing {
			w.skipDepth++
		}
		return
	}
	if w.noteSkip > 0 {
		if tag == "sup" {
			w.noteSkip++
		}
		return
	}
	if kind, ok := dropTags[tag]; ok {
		w.dropStart(tag, kind, selfClosing)
		return
	}
	if w.o.xhtml {
		if tag == "rt" || tag == "rp" { // ruby annotations repeat the text they annotate
			w.dropStart(tag, "ruby", selfClosing)
			return
		}
		if len(w.o.dropClasses) > 0 && classIn(attrs["class"], w.o.dropClasses) && !selfClosing && !voidTags[tag] {
			w.skipTag, w.skipDepth = tag, 1
			return
		}
	}
	if w.pre {
		if tag == "br" {
			w.preText.WriteByte('\n')
		}
		return
	}
	switch tag {
	case "br":
		if w.heading > 0 {
			w.headText.WriteByte(' ')
		} else if !w.inl.empty() {
			w.inl.addSeg(seg{brk: true})
		}
	case "hr":
		w.boundary()
		if !w.liOpen && w.table == nil && !w.inlineOnly {
			w.s.scene()
		}
	case "img":
		w.image(attrs)
	case "p", "div", "section", "article", "aside", "header", "footer", "main", "nav", "figure", "figcaption", "address", "details", "summary", "dl", "dt", "dd", "center", "hgroup":
		w.boundary()
		w.resetFmt()
		if w.o.xhtml {
			w.blockClass = attrs["class"]
			if f := w.classFormat(attrs); f.any() && (tag == "p" || tag == "div" || tag == "blockquote") {
				w.push(hframe{tag: tag, i: f.i, b: f.b, s: f.s})
			}
		}
	case "h1", "h2", "h3", "h4", "h5", "h6":
		w.boundary()
		w.resetFmt()
		if w.liOpen || w.table != nil || w.inlineOnly || w.hasQuote() {
			// a heading in a list item, a cell or a quote is bold text
			w.push(hframe{tag: tag, b: true})
			return
		}
		w.heading = int(tag[1] - '0')
		w.headText.Reset()
	case "blockquote":
		w.boundary()
		w.resetFmt()
		w.push(hframe{tag: tag, quote: true})
	case "ul", "ol":
		if w.inlineOnly { // a list in a note is lines of the note
			w.boundary()
			return
		}
		if w.liOpen {
			w.flushItem()
			w.liOpen = false
		} else {
			w.boundary()
		}
		if w.listFrames() == 0 {
			w.listSeq++
		}
		f := hframe{tag: tag, list: true, ordered: tag == "ol"}
		if f.ordered {
			f.num = 0
			if v, ok := attrs["start"]; ok {
				f.num = atoiClamp(v) - 1
			}
		}
		w.push(f)
	case "li":
		if w.inlineOnly {
			w.boundary()
			return
		}
		w.flushItem()
		w.resetFmt()
		w.liOpen = true
		if i := w.lastList(); i >= 0 && w.over == 0 {
			fr := &w.stack[i]
			fr.num++
			w.liOrdered, w.liNum = fr.ordered, fr.num
			w.liDepth = min(w.listFrames()-1, 8)
		} else {
			w.liOrdered, w.liNum, w.liDepth = false, 0, 0
		}
		w.liID = "h" + itoa(w.listSeq)
	case "pre":
		if w.table != nil || w.inlineOnly {
			// Code in a table cell or a footnote is text of it: a block cannot be written in
			// the middle of a table or a note, and written before it would come out of order.
			w.push(hframe{tag: tag})
			w.boundary()
			return
		}
		w.leaveItem()
		w.pre = true
		w.preText.Reset()
	case "table":
		if w.table != nil {
			w.tableDepth++
			return
		}
		w.leaveItem()
		w.table = &htab{}
		w.tableDepth = 1
	case "tr":
		if w.table != nil && w.tableDepth == 1 {
			w.endCell()
			w.table.cur = nil
		}
	case "td", "th":
		if w.table != nil && w.tableDepth == 1 {
			w.endCell()
			w.cell = true
			w.inl.take()
		}
	case "a":
		if w.o.xhtml && w.o.noteLink != nil && !w.inlineOnly && w.heading == 0 {
			if key, h, ok := w.o.noteLink(attrs["href"]); ok {
				w.noteRef(key, h)
				if !selfClosing {
					w.skipTag, w.skipDepth = "a", 1
				}
				return
			}
		}
		href := ""
		if v, ok := attrs["href"]; ok {
			if t, ok := safeTarget(strings.TrimSpace(v)); ok {
				href = t
				w.st.links++
			} else if strings.HasPrefix(strings.TrimSpace(v), "#") || !strings.Contains(v, ":") {
				w.st.internalLinks++
			} else {
				w.st.badLinks++
			}
		}
		w.push(hframe{tag: tag, link: href})
	case "em", "i", "cite", "dfn", "var":
		w.push(hframe{tag: tag, i: true})
	case "strong", "b":
		w.push(hframe{tag: tag, b: true})
	case "del", "s", "strike":
		w.push(hframe{tag: tag, s: true})
	case "code", "kbd", "samp", "tt":
		w.push(hframe{tag: tag, code: true})
	case "sup":
		// A footnote marker the editor writes: the note is in the post's metadata.
		if id, ok := attrs["data-fn"]; ok && w.o.notes != nil {
			if h, ok := w.o.notes[id]; ok && !w.inlineOnly {
				w.noteRef("fn"+id, h)
				w.noteSkip = 1
				return
			}
		}
		w.push(hframe{tag: tag})
	default:
		// span, font, u, ins, mark, small, sub, label, time, abbr, q ...: unwrapped, text kept.
		if !voidTags[tag] && !selfClosing {
			f := hframe{tag: tag}
			if w.o.xhtml {
				cf := w.classFormat(attrs)
				f.i, f.b, f.s = cf.i, cf.b, cf.s
			}
			w.push(f)
		}
	}
}

func (w *hwalk) hasQuote() bool {
	q, _ := w.inBlockCtx()
	return q
}

func (w *hwalk) lastList() int {
	for i := len(w.stack) - 1; i >= 0; i-- {
		if w.stack[i].list {
			return i
		}
	}
	return -1
}

func (w *hwalk) resetFmt() {
	// An unclosed <b> does not carry into the next block.
	keep := w.stack[:0]
	for _, f := range w.stack {
		if f.list || f.quote || f.tag == "h1" || f.tag == "h2" || f.tag == "h3" || f.tag == "h4" || f.tag == "h5" || f.tag == "h6" {
			keep = append(keep, f)
		}
	}
	w.stack = keep
}

func (w *hwalk) dropStart(tag, kind string, selfClosing bool) {
	switch kind {
	case "script":
		w.st.scripts++
	case "iframe":
		w.st.iframes++
	case "form":
		w.st.forms++
	case "embed":
		w.st.embeds++
	case "media":
		w.st.media++
	}
	w.st.drop(tag)
	if !voidTags[tag] && !selfClosing {
		w.skipTag, w.skipDepth = tag, 1
	}
}

func (w *hwalk) end(tag string) {
	if w.skipTag != "" {
		if tag == w.skipTag {
			if w.skipDepth--; w.skipDepth <= 0 {
				w.skipTag = ""
			}
		}
		return
	}
	if w.noteSkip > 0 {
		if tag == "sup" {
			w.noteSkip--
		}
		return
	}
	if w.pre {
		if tag == "pre" {
			w.pre = false
			if t := strings.Trim(w.preText.String(), "\n"); strings.TrimSpace(t) != "" && !w.inlineOnly {
				w.s.codeLines(t)
			}
			w.preText.Reset()
		}
		return
	}
	switch tag {
	case "p", "div", "section", "article", "aside", "header", "footer", "main", "nav", "figure", "figcaption", "address", "details", "summary", "dl", "dt", "dd", "center", "hgroup":
		w.boundary()
	case "h1", "h2", "h3", "h4", "h5", "h6":
		if w.heading > 0 && int(tag[1]-'0') == w.heading {
			w.s.heading(w.heading, w.headText.String())
			w.heading = 0
			w.headText.Reset()
			return
		}
		w.popTo(tag)
		w.boundary()
	case "blockquote":
		w.boundary()
		w.popTo(tag)
	case "ul", "ol":
		if w.inlineOnly {
			w.boundary()
			return
		}
		if w.liOpen {
			w.flushItem()
			w.liOpen = false
		} else {
			w.boundary()
		}
		w.popTo(tag)
		// Back inside an outer item, its next text belongs to the outer list.
		if w.listFrames() > 0 && w.lastList() >= 0 && w.over == 0 {
			w.liOpen = true
			fr := &w.stack[w.lastList()]
			w.liOrdered, w.liNum, w.liDepth = fr.ordered, fr.num, min(w.listFrames()-1, 8)
			w.liID = "h" + itoa(w.listSeq)
			// the continuation of an item after a nested list is a paragraph of its own
			w.liOpen = false
		}
	case "li":
		if w.inlineOnly {
			w.boundary()
			return
		}
		w.flushItem()
		w.liOpen = false
	case "table":
		if w.table == nil {
			return
		}
		if w.tableDepth--; w.tableDepth > 0 {
			return
		}
		w.endCell()
		if len(w.table.cur) > 0 {
			w.table.rows = append(w.table.rows, w.table.cur)
		}
		if len(w.table.rows) > 0 && !w.inlineOnly {
			w.s.table(w.table.rows, w.table.notes)
			w.st.tables++
		}
		w.table, w.cell = nil, false
	case "tr":
		if w.table != nil && w.tableDepth == 1 {
			w.endCell()
			if len(w.table.cur) > 0 {
				w.table.rows = append(w.table.rows, w.table.cur)
			}
			w.table.cur = nil
		}
	case "td", "th":
		if w.table != nil && w.tableDepth == 1 {
			w.endCell()
		}
	default:
		w.popTo(tag)
	}
}

// endCell closes the table cell in progress.
func (w *hwalk) endCell() {
	if w.table == nil || !w.cell {
		return
	}
	segs := w.inl.take()
	txt, notes := w.s.inline(segs)
	w.table.cur = append(w.table.cur, txt)
	w.table.notes = append(w.table.notes, notes...)
	if len(w.table.cur) > 63 {
		w.table.cur = w.table.cur[:63]
	}
	w.cell = false
}

func (w *hwalk) finish() {
	if w.heading > 0 {
		w.s.heading(w.heading, w.headText.String())
		w.heading = 0
	}
	if w.pre {
		if t := strings.Trim(w.preText.String(), "\n"); strings.TrimSpace(t) != "" && !w.inlineOnly {
			w.s.codeLines(t)
		}
		w.pre = false
	}
	if w.table != nil {
		w.endCell()
		if len(w.table.cur) > 0 {
			w.table.rows = append(w.table.rows, w.table.cur)
		}
		if len(w.table.rows) > 0 && !w.inlineOnly {
			w.s.table(w.table.rows, w.table.notes)
		}
		w.table = nil
	}
	if w.liOpen {
		w.flushItem()
		w.liOpen = false
	}
	if !w.inlineOnly {
		w.flush()
	}
}

// image notes an image and marks it in the text; it is never fetched.
func (w *hwalk) image(attrs map[string]string) {
	alt := cleanText(attrs["alt"], 120)
	if w.st.pic != nil && w.heading == 0 {
		if sg, ok := w.st.pic(attrs["src"], alt); ok {
			w.st.placed++
			w.inl.addSeg(sg)
			return
		}
	}
	w.st.images++
	if src := strings.TrimSpace(attrs["src"]); src != "" && len(w.st.imageSrc) < 20 {
		w.st.imageSrc = append(w.st.imageSrc, cleanText(src, 200))
	}
	label := "[image]"
	if alt != "" {
		label = "[image: " + alt + "]"
	}
	if w.heading > 0 {
		return
	}
	// plain, not italic: an italic mark beside a word would leave asterisks in the Markdown (the emphasis does not close)
	w.inl.addText(label, w.fmtNow(), false)
}

func atoiClamp(s string) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		if n > 1_000_000 {
			return 1_000_000
		}
	}
	return n
}

// knownShortcodes are the WordPress shortcodes that are removed from the text
// (their tags, not what they enclose).
var knownShortcodes = map[string]bool{
	"caption": true, "wp_caption": true, "gallery": true, "embed": true, "audio": true, "video": true, "playlist": true,
	"more": true, "nextpage": true, "contact-form-7": true, "wpforms": true, "button": true, "youtube": true, "vimeo": true,
	"tweet": true, "twitter": true, "instagram": true, "toc": true, "gist": true, "soundcloud": true, "spotify": true,
	"su_note": true, "su_spacer": true, "row": true, "column": true, "vc_row": true, "vc_column": true, "vc_column_text": true,
}

// stripShortcodes removes known shortcode tags from a run of text and counts them.
func (w *hwalk) stripShortcodes(t string) string {
	if !strings.Contains(t, "[") {
		return t
	}
	var b strings.Builder
	for i := 0; i < len(t); {
		if t[i] != '[' {
			j := strings.IndexByte(t[i:], '[')
			if j < 0 {
				b.WriteString(t[i:])
				break
			}
			b.WriteString(t[i : i+j])
			i += j
			continue
		}
		// [name ...] or [/name]
		j := i + 1
		if j < len(t) && t[j] == '/' {
			j++
		}
		k := j
		for k < len(t) && (t[k] >= 'a' && t[k] <= 'z' || t[k] >= '0' && t[k] <= '9' || t[k] == '_' || t[k] == '-') {
			k++
		}
		name := t[j:k]
		end := -1
		if knownShortcodes[name] && k < len(t) && (t[k] == ']' || t[k] == ' ' || t[k] == '=' || t[k] == '/') {
			lim := min(len(t), k+500)
			for e := k; e < lim; e++ {
				if t[e] == ']' {
					end = e
					break
				}
				if t[e] == '[' {
					break
				}
			}
		}
		if end < 0 {
			b.WriteByte('[')
			i++
			continue
		}
		if w.st.shortcodes == nil {
			w.st.shortcodes = map[string]int{}
		}
		if t[i+1] != '/' {
			w.st.shortcodes[name]++
		}
		i = end + 1
	}
	return b.String()
}

// The marker a footnote shortcode becomes: two private-use characters round its
// number. The characters are removed from the source first, so a post cannot write one.
var (
	noteOpen  = string(rune(0xE000))
	noteClose = string(rune(0xE001))
	noteStrip = strings.NewReplacer(noteOpen, "", noteClose, "")
)

// takeNoteShortcodes cuts [footnote]...[/footnote] (and [ref] and [fn]) out of
// raw post HTML, leaving a marker for each; the contents go to o.scNotes. It is one
// pass, and looks no further than 8,000 bytes for the closing tag.
func takeNoteShortcodes(src string, o *htmlOpts) string {
	src = noteStrip.Replace(src)
	if !strings.Contains(src, "[") {
		return src
	}
	var out strings.Builder
	out.Grow(len(src))
	for i := 0; i < len(src); {
		j := strings.IndexByte(src[i:], '[')
		if j < 0 {
			out.WriteString(src[i:])
			break
		}
		out.WriteString(src[i : i+j])
		i += j
		name := ""
		for _, n := range [3]string{"footnote", "ref", "fn"} {
			if strings.HasPrefix(src[i:], "["+n+"]") {
				name = n
				break
			}
		}
		if name == "" || len(o.scNotes) >= 5000 {
			out.WriteByte('[')
			i++
			continue
		}
		rest := src[i+len(name)+2:]
		e := strings.Index(rest[:min(len(rest), 8000+len(name)+3)], "[/"+name+"]")
		if e < 0 {
			out.WriteByte('[')
			i++
			continue
		}
		out.WriteString(noteOpen + itoa(len(o.scNotes)) + noteClose)
		o.scNotes = append(o.scNotes, rest[:e])
		i += len(name) + 2 + e + len(name) + 3
	}
	return out.String()
}
