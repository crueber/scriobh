package publish

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/crueber/scriobh/internal/text"
)

// ErrRTL is returned for a right-to-left language: the layout sets text left
// to right, one glyph per character, and would draw such a book wrongly.
var ErrRTL = errors.New("right-to-left text is not supported in PDF output; use the EPUB or DOCX")

// ErrTruncated is returned by Render when the layout had to leave part of the
// book out (it is longer than the layout will set): a file that silently lacks
// text is never written.
var ErrTruncated = errors.New("the book is larger than the PDF layout will set")

// PDFLayout is a book laid out in pages. Building it is the whole of the work
// of typesetting; rendering turns it into bytes.
type PDFLayout struct {
	b  *Book
	o  Options
	pr params
	ts *typeface
	t  *typesetter

	sheets []*sheet
	// SecPage is the page (0-based) each section starts on, -1 when it has none.
	secPage []int
	secY    []mpt
	// BodyStart is the first page of the numbered body (0-based).
	bodyStart int
	// Warnings are what the layout found.
	Warnings []Warn

	secKind map[int]specialKind
	toc     []tocEntry
	ctx     context.Context
	// truncated says what the layout left out, when it did.
	truncated []string
}

// Truncated lists what the layout had to leave out, empty for a whole book.
func (l *PDFLayout) Truncated() []string { return l.truncated }

type specialKind uint8

const (
	spNone specialKind = iota
	spDedication
	spEpigraph
	spCopyright
)

type tocEntry struct {
	level int
	title string
	sec   int
}

type tocLine struct {
	runs  []glyphRun
	w     mpt
	level int
	sec   int
	head  bool
}

// specialOf recognises front matter that has a page of its own.
func specialOf(s Section) specialKind {
	if s.Kind != SecFront {
		return spNone
	}
	switch strings.ToLower(strings.TrimSpace(CleanLine(s.Title))) {
	case "dedication":
		return spDedication
	case "epigraph":
		return spEpigraph
	case "copyright", "copyright page", "imprint":
		return spCopyright
	}
	return spNone
}

// PrintDefaults fills what a caller left unset for a print layout.
func printDefaults(o Options) Options {
	if o.Font == "" {
		o.Font = "Liberation Serif"
	}
	if o.FontSize == 0 {
		o.FontSize = 11
	}
	if o.LineSpacing == 0 {
		o.LineSpacing = 1.1
	}
	if o.ChapterHeading == "" {
		o.ChapterHeading = "number-title"
	}
	if o.PageSize == "" {
		o.PageSize = "6x9"
	}
	return o
}

// LayoutPDF sets the book in pages. It reads Images only for their size (a
// preview may pass images without their bytes).
func LayoutPDF(b *Book, o Options) (*PDFLayout, error) {
	return LayoutPDFContext(context.Background(), b, o)
}

// LayoutPDFContext is LayoutPDF that stops, with the context's error, when the
// context is cancelled: it looks between sections and every few hundred boxes.
func LayoutPDFContext(ctx context.Context, b *Book, o Options) (*PDFLayout, error) {
	o = printDefaults(o)
	lang := firstNonEmpty(o.Language, b.Language, "en")
	if IsRTL(lang) {
		return nil, ErrRTL
	}
	ts, err := newTypeface(o.Font)
	if err != nil {
		return nil, err
	}
	pr := resolveParams(o)
	t := newTypesetter(b, o, ts, pr)
	l := &PDFLayout{b: b, o: o, pr: pr, ts: ts, t: t, secKind: map[int]specialKind{}, ctx: ctx}
	l.layout()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.Warnings = append(l.Warnings, ts.warnings()...)
	l.Warnings = append(l.Warnings, t.warnings()...)
	return l, nil
}

// PageCount is the number of pages, a blank one added at the end when the
// count would be odd (printers print sheets, two pages to a leaf).
func (l *PDFLayout) PageCount() int { return len(l.sheets) }

// SectionPage is the page (1-based) a section starts on, 0 when it has none.
func (l *PDFLayout) SectionPage(i int) int {
	if i < 0 || i >= len(l.secPage) || l.secPage[i] < 0 {
		return 0
	}
	return l.secPage[i] + 1
}

// PageLabel is what a reader sees as the number of a page (0-based index):
// lower-case roman in the front matter, then arabic from 1.
func (l *PDFLayout) PageLabel(i int) string {
	if i < l.bodyStart {
		return roman(i+1, false)
	}
	return itoa(i - l.bodyStart + 1)
}

func roman(n int, upper bool) string {
	vals := []int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1}
	syms := []string{"m", "cm", "d", "cd", "c", "xc", "l", "xl", "x", "ix", "v", "iv", "i"}
	var b strings.Builder
	for i, v := range vals {
		for n >= v {
			b.WriteString(syms[i])
			n -= v
		}
	}
	if upper {
		return strings.ToUpper(b.String())
	}
	return b.String()
}

// Geometry returns the page and the text block in points, for a preview.
func (l *PDFLayout) Geometry() (pageW, pageH, textW, textH, size, leading float64) {
	f := func(v mpt) float64 { return float64(v) / 1000 }
	return f(l.pr.pageW), f(l.pr.pageH), f(l.pr.textW), f(l.pr.textH), f(l.pr.size), f(l.t.lead)
}

// LinesPerPage is how many lines of body text a page holds.
func (l *PDFLayout) LinesPerPage() int { return int(l.pr.textH / l.t.lead) }

// minInside is the inside margin printers typically ask of a paperback with
// this many pages. The figures are typical, not any printer's (see the
// decisions): they only produce a warning.
func (l *PDFLayout) minInside() mpt {
	switch n := len(l.sheets); {
	case n <= 150:
		return 3 * inch / 8
	case n <= 300:
		return inch / 2
	case n <= 500:
		return 5 * inch / 8
	case n <= 700:
		return 3 * inch / 4
	}
	return 7 * inch / 8
}

// ---- assembling the book -----------------------------------------------------------------------

func (l *PDFLayout) layout() {
	b, o, pr, t := l.b, l.o, l.pr, l.t
	l.secPage = make([]int, len(b.Sections))
	l.secY = make([]mpt, len(b.Sections))
	for i := range l.secPage {
		l.secPage[i] = -1
	}
	f := &filler{pr: &l.pr, pages: &l.sheets, sepH: t.lead * 2, leading: t.lead, secFirst: map[int]int{}, ctx: l.ctx}
	firstBody := -1
	hasCopyrightSection := false
	for i, s := range b.Sections {
		l.secKind[i] = specialOf(s)
		if l.secKind[i] == spCopyright {
			hasCopyrightSection = true
		}
		if s.Kind != SecFront && firstBody < 0 {
			firstBody = i
		}
	}
	// 1. the title page, the copyright page, the dedication and the epigraph
	if On(o.TitlePage) {
		l.titlePage()
	}
	if pr.copyright && !hasCopyrightSection {
		l.copyrightPage(nil)
	}
	for i := range b.Sections {
		s := b.Sections[i]
		switch l.secKind[i] {
		case spCopyright:
			if pr.copyright {
				l.copyrightPage(&s)
			}
		case spDedication, spEpigraph:
			l.dedicationPage(i, s, l.secKind[i])
		}
	}
	// 2. the contents
	if pr.toc {
		l.buildTOC()
		if len(l.toc) > 0 {
			f.fill(l.tocFlow())
			f.endPage()
		}
	}
	// 3. the rest of the front matter, in roman numerals, then the body
	var front, body []*vitem
	for i, s := range b.Sections {
		if l.ctx.Err() != nil {
			return
		}
		if l.secKind[i] != spNone {
			continue
		}
		dst := &body
		if s.Kind == SecFront {
			dst = &front
		}
		*dst = append(*dst, l.section(i, s)...)
		if pr.notes == "chapter" {
			*dst = append(*dst, l.chapterEndnotes(i, s)...)
		}
	}
	f.fill(front)
	f.endPage()
	if pr.notes == "book" {
		body = append(body, l.bookEndnotes()...)
	}
	if len(body) > 0 {
		// the body always starts a page, whatever the chapters do after it
		body = append([]*vitem{{kind: vBreak, recto: pr.recto}}, body...)
	}
	f.fill(body)
	f.endPage()
	for sec, pgIdx := range f.secFirst {
		l.secPage[sec] = pgIdx
	}
	for pgIdx, sh := range l.sheets {
		for _, st := range sh.starts {
			if l.secPage[st.sec] == pgIdx {
				l.secY[st.sec] = st.y
			}
		}
	}
	// The numbered body starts where its first section does.
	l.bodyStart = len(l.sheets)
	if firstBody >= 0 && l.secPage[firstBody] >= 0 {
		l.bodyStart = l.secPage[firstBody]
	} else if firstBody >= 0 {
		for i := firstBody; i < len(b.Sections); i++ {
			if l.secPage[i] >= 0 {
				l.bodyStart = l.secPage[i]
				break
			}
		}
	}
	if len(l.sheets) == 0 || len(l.sheets)%2 == 1 {
		l.blankSheet(f)
	}
	if f.overflow {
		l.truncated = append(l.truncated, fmt.Sprintf("the book is longer than %d pages", maxBookPages))
	}
	if l.t.budget.lines <= 0 || l.t.tableCapped {
		l.truncated = append(l.truncated, "the text is more than the layout's limit of lines or table cells")
	}
	if l.t.noteCut > 0 {
		l.truncated = append(l.truncated, fmt.Sprintf("%d footnotes are over the layout's limit of note text", l.t.noteCut))
	}
	if len(l.truncated) == 0 { // a cut book would report every cut note as unset
		l.checkNotes()
	}
	if f.unfixed > 0 {
		l.Warnings = append(l.Warnings, Warn{"widow_orphan", fmt.Sprintf("%d page(s) end in the middle of a paragraph or table in a way the keep rules could not avoid (a single line, or a heading, left alone at the foot or head of a page).", f.unfixed)})
	}
	if f.overflow {
		l.Warnings = append(l.Warnings, Warn{"layout_limit", fmt.Sprintf("The book is longer than %d pages, and the export will be refused; split it and compile a part at a time.", maxBookPages)})
	}
	if l.pr.inside < l.minInside() {
		l.Warnings = append(l.Warnings, Warn{"gutter_small", fmt.Sprintf("The inside margin is %.2f in (with the gutter) and a book of %d pages usually needs at least %.3g in so the text does not run into the binding; check your printer's guide.",
			float64(l.pr.inside)/72000, len(l.sheets), float64(l.minInside())/72000)})
	}
	if len(l.sheets) < 24 {
		l.Warnings = append(l.Warnings, Warn{"few_pages", fmt.Sprintf("The book has %d pages; print-on-demand printers commonly ask for at least 24 (some for 18 or 32).", len(l.sheets))})
	}
}

func (l *PDFLayout) blankSheet(f *filler) {
	sh := &sheet{kind: pageBlank, noHead: true, noFolio: true}
	l.sheets = append(l.sheets, sh)
}

// ---- the front pages ---------------------------------------------------------------------------

// stack places lines at given y positions on a direct page.
func (l *PDFLayout) directPage(kind pageKind, items []*vitem, top mpt) *sheet {
	sh := &sheet{kind: kind, noHead: true, noFolio: true}
	y := top
	for k, it := range items {
		y += it.space
		sh.items = append(sh.items, placed{it: it, y: y, idx: k})
		y += it.h
	}
	l.sheets = append(l.sheets, sh)
	return sh
}

func heightOf(items []*vitem) mpt {
	var h mpt
	for _, it := range items {
		h += it.space + it.h
	}
	return h
}

func (l *PDFLayout) titlePage() {
	t, b, pr := l.t, l.b, l.pr
	centre := paraOpt{x: 0, w: pr.textW * 88 / 100, align: alignCenter, ragged: true, noNotes: true, fitWords: true}
	centre.x = (pr.textW - centre.w) / 2
	var items []*vitem
	title := CleanLine(b.Title)
	if title == "" {
		title = "Untitled"
	}
	big := t.size * 24 / 10
	o := centre
	o.size, o.style, o.lead = big, stBold, t.lines(big*12/10)
	ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: title}}, o)
	items = append(items, ls...)
	if s := CleanLine(b.Subtitle); s != "" {
		o := centre
		o.size, o.style, o.space = t.size*14/10, stItalic, t.lead
		o.lead = t.lines(o.size * 12 / 10)
		ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: s}}, o)
		items = append(items, ls...)
	}
	l.directPage(pageTitle, items, pr.textH/4)
	// the author's name nearer the foot
	if by := CleanLine(b.Byline); by != "" {
		o := centre
		o.size = t.size * 14 / 10
		o.lead = t.lines(o.size * 12 / 10)
		ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: by}}, o)
		sh := l.sheets[len(l.sheets)-1]
		y := pr.textH * 3 / 4
		for k, it := range ls {
			sh.items = append(sh.items, placed{it: it, y: y, idx: 100 + k})
			y += it.h
		}
	}
}

func (l *PDFLayout) copyrightPage(from *Section) {
	t, b, pr := l.t, l.b, l.pr
	size := t.size * 80 / 100
	lead := t.lead * 80 / 100
	var items []*vitem
	opt := paraOpt{size: size, lead: lead, x: 0, w: pr.textW * 82 / 100, align: alignCenter, noNotes: true}
	opt.x = (pr.textW - opt.w) / 2
	if from != nil {
		items = l.t.plainBlock(from.Nodes, opt)
	} else {
		txt := l.o.Imprint
		if strings.TrimSpace(txt) == "" {
			name := CleanLine(b.Byline)
			if name != "" {
				txt = "Copyright © " + name + "\nAll rights reserved."
			} else {
				txt = "All rights reserved."
			}
		}
		for _, ln := range strings.Split(txt, "\n") {
			o := opt
			ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: ln}}, o)
			if len(ls) == 0 {
				items = append(items, &vitem{kind: vLine, h: lead})
			}
			items = append(items, ls...)
		}
	}
	// at the foot of the page, on the left-hand side of the spread
	h := heightOf(items)
	top := pr.textH - h - t.lead
	if top < 0 {
		top = 0
	}
	l.directPage(pageCopyright, items, top)
}

func (l *PDFLayout) dedicationPage(i int, s Section, kind specialKind) {
	t, pr := l.t, l.pr
	size := t.size * 110 / 100
	st := stItalic
	top := pr.textH / 3
	if kind == spEpigraph {
		size = t.size * 95 / 100
		top = pr.textH / 4
	}
	opt := paraOpt{size: size, style: st, x: pr.textW / 10, w: pr.textW * 8 / 10, align: alignCenter, noNotes: true, ragged: true}
	items := t.plainBlock(s.Nodes, opt)
	if len(items) == 0 {
		return
	}
	// start on a right-hand page
	if len(l.sheets)%2 == 1 {
		l.sheets = append(l.sheets, &sheet{kind: pageBlank, noHead: true, noFolio: true})
	}
	items[0].sec = i + 1
	sh := l.directPage(pageDedication, items, top)
	if kind == spEpigraph {
		sh.kind = pageEpigraph
	}
	l.secPage[i] = len(l.sheets) - 1
}

// plainBlock sets paragraphs (and the text of anything else) in one style.
func (t *typesetter) plainBlock(nodes []text.Node, o paraOpt) []*vitem {
	var out []*vitem
	var walk func([]text.Node)
	walk = func(ns []text.Node) {
		for _, n := range ns {
			switch n.Kind {
			case text.NPara:
				op := o
				if len(out) > 0 {
					op.space = t.lead / 2
				}
				ls, _ := t.paragraph(n.Inl, op)
				out = append(out, ls...)
			case text.NQuote:
				walk(n.Kids)
			case text.NRule:
				out = append(out, &vitem{kind: vLine, h: t.lead})
			default:
				if s := strings.TrimSpace(nodePlain(n)); s != "" {
					op := o
					if len(out) > 0 {
						op.space = t.lead / 2
					}
					ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: s}}, op)
					out = append(out, ls...)
				}
			}
		}
	}
	walk(nodes)
	return out
}

// ---- contents ------------------------------------------------------------------------------------

func (l *PDFLayout) sectionLabel(i int, s Section) string {
	label := HeadingText(s, l.o)
	if label == "" {
		label = CleanLine(s.Title)
	}
	if label == "" && s.Number > 0 {
		label = firstNonEmpty(l.o.ChapterLabel, "Chapter") + " " + itoa(s.Number)
	}
	return label
}

func (l *PDFLayout) buildTOC() {
	hasPart := false
	for _, s := range l.b.Sections {
		if s.Kind == SecPart {
			hasPart = true
		}
	}
	for i, s := range l.b.Sections {
		if l.secKind[i] != spNone {
			continue
		}
		label := l.sectionLabel(i, s)
		if label == "" {
			continue
		}
		lv := 0
		if s.Kind == SecChapter && hasPart && s.Number > 0 {
			lv = 1
		}
		l.toc = append(l.toc, tocEntry{level: lv, title: label, sec: i})
	}
}

func (l *PDFLayout) tocFlow() []*vitem {
	t, pr := l.t, l.pr
	flow := []*vitem{{kind: vBreak, recto: true}}
	// the heading
	size := t.size * 16 / 10
	lead := t.lines(size * 12 / 10)
	ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: "Contents"}}, paraOpt{size: size, lead: lead, x: 0, w: pr.textW, align: alignCenter, ragged: true, space: pr.sink / 2, keepAll: true, noNotes: true})
	for _, it := range ls {
		it.keepTop = true
	}
	if len(ls) > 0 {
		ls[0].opener = true // no running head over the contents
	}
	flow = append(flow, ls...)
	gap := &vitem{kind: vLine, h: 2 * t.lead, keepPrev: true}
	flow = append(flow, gap)
	numW := t.ts.width("000", 0, t.size) + t.size
	for _, e := range l.toc {
		st := style(0)
		if e.level == 0 && l.hasLevels() {
			st = stBold
		}
		indent := mpt(e.level) * t.size * 3 / 2
		avail := pr.textW - indent - numW
		title := t.fit(e.title, st, t.size, avail)
		runs := t.ts.shape(title, st, t.size, 0)
		var w mpt
		for _, r := range runs {
			w += r.width
		}
		it := &vitem{kind: vTOC, h: t.lead, base: t.baseFor(t.size, t.lead), x: indent, w: pr.textW,
			toc: &tocLine{runs: runs, w: w, level: e.level, sec: e.sec, head: e.level == 0 && l.hasLevels()}}
		if e.level == 0 && l.hasLevels() {
			it.space = t.lead / 2
		}
		flow = append(flow, it)
	}
	return flow
}

func (l *PDFLayout) hasLevels() bool {
	for _, e := range l.toc {
		if e.level > 0 {
			return true
		}
	}
	return false
}

// fit shortens text with an ellipsis until it is no wider than w.
func (t *typesetter) fit(s string, st style, size, w mpt) string {
	s = CleanLine(s)
	if t.ts.width(s, st, size) <= w {
		return s
	}
	rs := []rune(s)
	for len(rs) > 1 {
		rs = rs[:len(rs)-1]
		cand := strings.TrimRight(string(rs), " ,;:-") + "…"
		if t.ts.width(cand, st, size) <= w {
			return cand
		}
	}
	return "…"
}

// ---- sections ------------------------------------------------------------------------------------

// section sets one section: a break, its opener and its text.
func (l *PDFLayout) section(i int, s Section) []*vitem {
	t, pr, o := l.t, l.pr, l.o
	var out []*vitem
	t.notes = map[int]text.Footnote{}
	t.noteBuf = nil
	for _, n := range s.Notes {
		t.notes[n.Num] = n
	}
	startNew := !pr.continuous || s.Kind == SecPart || s.Kind == SecFront || s.Kind == SecBack || s.Kind == SecNotes
	if startNew {
		out = append(out, &vitem{kind: vBreak, recto: pr.recto})
	}
	var opener []*vitem
	switch s.Kind {
	case SecPart:
		opener = t.opener("", l.partTitle(s), pr.textH/4, 2.0, !startNew)
	case SecChapter:
		label, title := l.chapterParts(s)
		opener = t.opener(label, title, pr.sink, 1.75, !startNew)
		if pr.ornamentChapters && (label != "" || title != "") {
			orn := t.ornamentLine(SceneBreakText(o), t.size*12/10, 2*t.lead, &flowCtx{x: 0, w: pr.textW})
			orn.keepNext = true
			opener = append(opener, orn)
		}
	default:
		title := CleanLine(s.Title)
		if s.Kind == SecNotes && title == "" {
			title = "Notes"
		}
		opener = t.opener("", title, pr.sink*6/10, 1.5, !startNew)
	}
	if len(opener) > 0 {
		opener[0].sec = i + 1
		if startNew {
			opener[0].opener = true
			opener[0].keepTop = true
		}
	}
	out = append(out, opener...)
	c := &flowCtx{x: 0, w: pr.textW, first: true, drop: pr.dropCap && s.Kind == SecChapter}
	body := t.blocks(s.Nodes, c)
	if len(opener) > 0 && len(body) > 0 {
		body[0].space += 3 * t.lead
	} else if len(opener) == 0 && len(body) > 0 {
		body[0].sec = i + 1 // a section with no heading still marks its place
		if startNew {
			body[0].opener = true
		}
	}
	out = append(out, body...)
	if Set(o.TheEnd) && i == len(l.b.Sections)-1 {
		end := t.ornamentLine("THE END", t.size, 3*t.lead, &flowCtx{x: 0, w: pr.textW})
		end.space = 2 * t.lead
		out = append(out, end)
	}
	if len(out) > 0 && out[len(out)-1].kind == vBreak {
		out = out[:len(out)-1]
	}
	return out
}

func (l *PDFLayout) partTitle(s Section) string {
	return CleanLine(s.Title)
}

// chapterParts splits a chapter's heading into the line above the title
// ("Chapter 3") and the title.
func (l *PDFLayout) chapterParts(s Section) (label, title string) {
	return chapterParts(l.o, s)
}

func chapterParts(o Options, s Section) (label, title string) {
	title = CleanLine(s.Title)
	lab := o.ChapterLabel
	if lab == "" {
		lab = "Chapter"
	}
	num := ""
	if s.Number > 0 {
		num = lab + " " + itoa(s.Number)
	}
	switch o.ChapterHeading {
	case "none":
		return "", ""
	case "number":
		if num != "" {
			return num, ""
		}
		return "", title
	case "title":
		if title != "" {
			return "", title
		}
		return "", num
	}
	return num, title
}

// opener sets a chapter or part heading: a drop from the top of the page, a
// small label in capitals, the title, and air below.
func (t *typesetter) opener(label, title string, sink mpt, scale float64, inline bool) []*vitem {
	pr := t.pr
	var out []*vitem
	w := pr.textW * 90 / 100
	x := (pr.textW - w) / 2
	if label != "" {
		size := t.size * 82 / 100
		runs := t.ts.shape(cleanText(strings.ToUpper(label)), stBold, size, 0)
		track := size / 8
		var wd mpt
		for i := range runs {
			runs[i].track = track
			runs[i].width += track * mpt(len(runs[i].gids))
			wd += runs[i].width
		}
		p := &para{runs: runs, items: []item{{kind: iBox, w: wd, r0: 0, r1: int32(len(runs))}}}
		ln := lineRec{from: 0, to: 1, nat: wd, width: pr.textW, last: true}
		p.lines = []lineRec{ln}
		lh := t.lines(size * 12 / 10)
		out = append(out, &vitem{kind: vLine, h: lh, base: t.baseFor(size, lh), par: p, ln: ln, x: 0, w: pr.textW, align: alignCenter})
	}
	if title != "" {
		size := mpt(math.Round(float64(t.size) * scale))
		lh := t.lines(size * 12 / 10)
		ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: title}}, paraOpt{size: size, lead: lh, x: x, w: w, align: alignCenter, ragged: true, noNotes: true, keepAll: true, fitWords: true})
		if label != "" && len(ls) > 0 {
			ls[0].space = t.lead / 2
		}
		out = append(out, ls...)
	}
	for _, it := range out {
		it.keepNext = true
	}
	if len(out) > 0 {
		out[0].space = sink
		if inline {
			out[0].space = 3 * t.lead
		}
	}
	return out
}

// ---- endnotes ------------------------------------------------------------------------------------

func (l *PDFLayout) noteBlock(notes []text.Footnote, heading string) []*vitem {
	t, pr := l.t, l.pr
	var out []*vitem
	if heading != "" {
		size := t.size * 110 / 100
		lh := t.lines(size * 12 / 10)
		ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: heading}}, paraOpt{size: size, style: stBold, lead: lh, x: 0, w: pr.textW, align: alignLeft, space: 2 * t.lead, keepAll: true, noNotes: true})
		out = append(out, ls...)
	}
	size := t.size * 92 / 100
	hang := t.size * 2
	for _, n := range notes {
		lp := t.labelPara(itoa(n.Num) + ".")
		first := true
		for _, node := range n.Nodes {
			var in []text.Inline
			if node.Kind == text.NPara {
				in = node.Inl
			} else {
				in = []text.Inline{{Kind: text.IText, Text: nodePlain(node)}}
			}
			op := paraOpt{size: size, x: hang, w: pr.textW - hang, align: alignLeft, noNotes: true}
			if pr.justify {
				op.align = alignJustify
			}
			if first {
				op.label, op.labelX = lp, hang-t.size*3/10
				first = false
			}
			ls, _ := t.paragraph(in, op)
			out = append(out, ls...)
		}
		if first { // a note with no text
			ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: " "}}, paraOpt{size: size, x: hang, w: pr.textW - hang, noNotes: true, label: lp, labelX: hang - t.size*3/10})
			out = append(out, ls...)
		}
	}
	return out
}

func (l *PDFLayout) chapterEndnotes(i int, s Section) []*vitem {
	if len(s.Notes) == 0 || s.Kind != SecChapter && s.Kind != SecPart && s.Kind != SecBack && s.Kind != SecFront {
		return nil
	}
	return l.noteBlock(usedNotes(s), "Notes")
}

// usedNotes lists the notes a section refers to, in order.
func usedNotes(s Section) []text.Footnote {
	used := map[int]bool{}
	noteRefs(s.Nodes, used)
	var out []text.Footnote
	for _, n := range s.Notes {
		if used[n.Num] {
			out = append(out, n)
		}
	}
	return out
}

func (l *PDFLayout) bookEndnotes() []*vitem {
	var out []*vitem
	for i, s := range l.b.Sections {
		if l.secKind[i] != spNone || len(s.Notes) == 0 {
			continue
		}
		heading := l.sectionLabel(i, s)
		if heading == "" {
			continue
		}
		ns := usedNotes(s)
		if len(ns) == 0 {
			continue
		}
		out = append(out, l.noteBlock(ns, heading)...)
	}
	if len(out) == 0 {
		return nil
	}
	head := []*vitem{{kind: vBreak, recto: l.pr.recto}}
	t := l.t
	size := t.size * 15 / 10
	lh := t.lines(size * 12 / 10)
	ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: "Notes"}}, paraOpt{size: size, lead: lh, x: 0, w: l.pr.textW, align: alignCenter, ragged: true, space: l.pr.sink * 6 / 10, keepAll: true, noNotes: true})
	for _, it := range ls {
		it.keepTop = true
	}
	if len(ls) > 0 {
		ls[0].opener = true
	}
	return append(append(head, ls...), out...)
}

// checkNotes warns of a footnote reference whose note was not set at the foot of
// a page: a reference in a place that sets no notes (a dedication page, say).
func (l *PDFLayout) checkNotes() {
	if l.pr.notes != "page" {
		return
	}
	set := map[int]bool{}
	for _, sh := range l.sheets {
		for _, p := range sh.items {
			for _, n := range p.it.notes {
				set[n.num] = true
			}
		}
	}
	missing := 0
	for _, s := range l.b.Sections {
		used := map[int]bool{}
		noteRefs(s.Nodes, used)
		for n := range used {
			if !set[n] {
				missing++
			}
		}
	}
	if missing > 0 {
		l.Warnings = append(l.Warnings, Warn{"note_not_set", fmt.Sprintf("%d footnote(s) are referenced where no footnote can be set (a dedication or epigraph page, or a copyright section); their numbers are drawn and the text is not. Use footnotes: chapter, or move the note.", missing)})
	}
}
