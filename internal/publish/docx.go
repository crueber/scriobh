package publish

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/text"
)

// WriteDOCX writes the book as a Word document. It is built by hand from zip
// and XML: real paragraph styles (Normal, Body Text, First Paragraph, Heading 1,
// Title, Scene Break, ...) carry every look the options choose, so the file
// stays editable in Word without fighting the export; headers use a PAGE field;
// footnotes are real footnotes; the package has no external relationship, no
// macro and no embedded object.
func WriteDOCX(b *Book, o Options) ([]byte, error) {
	d := &docxWriter{b: b, o: docxDefaults(o)}
	d.pg = pageOf(d.o)
	d.lang = langTag(firstNonEmpty(d.o.Language, b.Language))
	d.rtl = IsRTL(d.lang)
	d.imageRel = map[string]string{}
	return d.build()
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

type docxWriter struct {
	b    *Book
	o    Options
	pg   page
	lang string
	rtl  bool

	body strings.Builder
	// relationships of word/document.xml beyond the fixed ones: images.
	media    []zipEntry
	mediaExt map[string]bool
	imageRel map[string]string // image key -> relationship id
	relXML   strings.Builder
	nextRel  int
	docPr    int
	lists    []int // start of each ordered list instance
	notes    strings.Builder
	noteIDs  map[int]bool
	// sectionBreakNext: the next section has no heading, so its first paragraph starts the page.
	sectionBreakNext bool
	inNote           bool // writing a footnote: no pictures
}

const maxDocxHeading = 6

func (d *docxWriter) build() ([]byte, error) {
	b, o := d.b, d.o
	d.nextRel = 100 // the fixed relationships are rId1..rId99
	d.noteIDs = map[int]bool{}
	d.mediaExt = map[string]bool{}

	hasTitle := On(o.TitlePage)
	if hasTitle {
		d.titlePage()
	}
	d.sections()
	if Set(o.TheEnd) {
		d.para("TheEnd", "", d.run("THE END", runState{}))
	}

	footer := o.PageNumbers == "footer"
	header := d.headerLead() != "" || o.PageNumbers == "header"
	titlePg := hasTitle && (header || footer)
	sect := d.sectPr(header, footer, titlePg)

	var doc strings.Builder
	doc.WriteString(xmlDecl)
	fmt.Fprintf(&doc, `<w:document xmlns:w="%s" xmlns:r="%s" xmlns:wp="%s" xmlns:a="%s" xmlns:pic="%s"><w:body>`, nsW, nsR, nsWP, nsA, nsPic)
	doc.WriteString(d.body.String())
	doc.WriteString(sect)
	doc.WriteString(`</w:body></w:document>`)

	exts := make([]string, 0, len(d.mediaExt))
	for e := range d.mediaExt {
		exts = append(exts, e)
	}
	sort.Strings(exts)

	entries := []zipEntry{
		{name: "[Content_Types].xml", data: []byte(contentTypesXML(exts, footer))},
		{name: "_rels/.rels", data: []byte(rootRels)},
		{name: "word/document.xml", data: []byte(doc.String())},
		{name: "word/_rels/document.xml.rels", data: []byte(d.docRels(footer))},
		{name: "word/styles.xml", data: []byte(d.styles())},
		{name: "word/settings.xml", data: []byte(settingsXML(d.lang))},
		{name: "word/numbering.xml", data: []byte(numberingXML(d.lists))},
		{name: "word/footnotes.xml", data: []byte(d.footnotesXML())},
		{name: "word/header1.xml", data: []byte(d.headerXML(true))},
		{name: "word/header2.xml", data: []byte(d.headerXML(false))},
	}
	if footer {
		entries = append(entries,
			zipEntry{name: "word/footer1.xml", data: []byte(d.footerXML(true))},
			zipEntry{name: "word/footer2.xml", data: []byte(d.footerXML(false))})
	}
	entries = append(entries,
		zipEntry{name: "docProps/core.xml", data: []byte(d.coreXML())},
		zipEntry{name: "docProps/app.xml", data: []byte(d.appXML())})
	entries = append(entries, d.media...)
	return writeZip(entries, b.Modified)
}

// ---- paragraphs and runs -------------------------------------------------------------------

type runState struct {
	bold, italic, strike bool
	code                 bool
}

func (s runState) props() string {
	var p strings.Builder
	if s.code {
		p.WriteString(`<w:rStyle w:val="CodeChar"/>`)
	}
	if s.bold {
		p.WriteString(`<w:b/><w:bCs/>`)
	}
	if s.italic {
		p.WriteString(`<w:i/><w:iCs/>`)
	}
	if s.strike {
		p.WriteString(`<w:strike/>`)
	}
	if d := p.String(); d != "" {
		return `<w:rPr>` + d + `</w:rPr>`
	}
	return ""
}

func (d *docxWriter) run(t string, st runState) string {
	t = oneLine(t)
	if t == "" {
		return ""
	}
	rpr := st.props()
	if d.rtl {
		if rpr == "" {
			rpr = `<w:rPr><w:rtl/></w:rPr>`
		} else {
			rpr = strings.TrimSuffix(rpr, `</w:rPr>`) + `<w:rtl/></w:rPr>`
		}
	}
	return `<w:r>` + rpr + `<w:t xml:space="preserve">` + xmlEscape(t) + `</w:t></w:r>`
}

func (d *docxWriter) para(style, extraPPr, runs string) {
	d.body.WriteString(`<w:p>`)
	d.body.WriteString(pPr(style, extraPPr))
	d.body.WriteString(runs)
	d.body.WriteString(`</w:p>`)
}

// pPr writes the paragraph properties. extra is already in schema order and
// holds only what follows pStyle (numPr, ind, jc).
func pPr(style, extra string) string {
	if style == "" && extra == "" {
		return ""
	}
	s := ""
	if style != "" {
		s = `<w:pStyle w:val="` + style + `"/>`
	}
	return `<w:pPr>` + s + extra + `</w:pPr>`
}

func (d *docxWriter) inlines(in []text.Inline, st runState) string {
	var b strings.Builder
	for _, x := range in {
		switch x.Kind {
		case text.IText:
			b.WriteString(d.run(x.Text, st))
		case text.ICode:
			s := st
			s.code = true
			b.WriteString(d.run(x.Text, s))
		case text.IBreak:
			b.WriteString(`<w:r><w:br/></w:r>`)
		case text.IEmph:
			s := st
			s.italic = !st.italic
			b.WriteString(d.inlines(x.Kids, s))
		case text.IStrong:
			s := st
			s.bold = true
			b.WriteString(d.inlines(x.Kids, s))
		case text.IStrike:
			s := st
			s.strike = true
			b.WriteString(d.inlines(x.Kids, s))
		case text.ILink:
			// A link is its text. The address is written after it only when
			// asked, as text: the file never carries a live link.
			b.WriteString(d.inlines(x.Kids, st))
			if Set(d.o.LinkURLs) && x.Text != "" {
				b.WriteString(d.run(" ("+x.Text+")", st))
			}
		case text.IImage:
			b.WriteString(d.image(x, st))
		case text.INote:
			d.noteIDs[x.Num] = true
			b.WriteString(fmt.Sprintf(`<w:r><w:rPr><w:rStyle w:val="FootnoteReference"/></w:rPr><w:footnoteReference w:id="%d"/></w:r>`, x.Num))
		}
	}
	return b.String()
}

// ---- blocks -----------------------------------------------------------------------------------

type blockCtx struct {
	quote int  // nesting depth of quotes
	list  int  // nesting depth of lists
	note  bool // inside a footnote
}

func (d *docxWriter) blocks(nodes []text.Node, ctx blockCtx, level int) {
	first := true // the next paragraph opens a text or follows a break
	for _, n := range nodes {
		switch n.Kind {
		case text.NPara:
			style := "BodyText"
			switch {
			case ctx.note:
				style = "FootnoteText"
			case ctx.quote > 0:
				style = "Quote"
			case first:
				style = "FirstParagraph"
			}
			d.para(style, d.quoteInd(ctx), d.inlines(n.Inl, runState{}))
			first = false
		case text.NHeading:
			lv := n.Level + level
			if lv < 2 {
				lv = 2
			}
			if lv > maxDocxHeading {
				lv = maxDocxHeading
			}
			if ctx.note {
				d.para("FootnoteText", "", d.inlines(n.Inl, runState{bold: true}))
			} else {
				d.para(fmt.Sprintf("Heading%d", lv), "", d.inlines(n.Inl, runState{}))
			}
			first = true
		case text.NRule:
			if ctx.note {
				continue
			}
			d.para("SceneBreak", "", d.run(SceneBreakText(d.o), runState{}))
			first = true
		case text.NQuote:
			c := ctx
			c.quote++
			d.blocks(n.Kids, c, level)
			first = true
		case text.NList:
			d.list(n, ctx, level)
			first = true
		case text.NCode, text.NSource:
			style := "Code"
			for _, line := range strings.Split(n.Text, "\n") {
				d.para(style, d.quoteInd(ctx), d.run(strings.TrimRight(line, "\r"), runState{}))
			}
			first = true
		case text.NTable:
			d.table(n, ctx)
			first = true
		}
	}
}

func (d *docxWriter) quoteInd(ctx blockCtx) string {
	if ctx.quote > 1 {
		n := ctx.quote
		if n > 8 {
			n = 8
		}
		return fmt.Sprintf(`<w:ind w:left="%d" w:right="720" w:firstLine="0"/>`, 720*n)
	}
	return ""
}

func (d *docxWriter) list(n text.Node, ctx blockCtx, level int) {
	ilvl := ctx.list
	if ilvl > 8 {
		ilvl = 8
	}
	numID := 1
	if n.Ordered {
		start := n.Start
		if start < 1 {
			start = 1
		}
		d.lists = append(d.lists, start)
		numID = len(d.lists) + 1
	}
	for _, item := range n.Items {
		placed := false
		var rest []text.Node
		for i, k := range item {
			if !placed && k.Kind == text.NPara {
				d.para("ListParagraph", fmt.Sprintf(`<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr>`, ilvl, numID), d.inlines(k.Inl, runState{}))
				placed = true
				rest = item[i+1:]
				break
			}
			if !placed {
				// a list item that does not start with text: an empty bullet carries the number
				d.para("ListParagraph", fmt.Sprintf(`<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr>`, ilvl, numID), "")
				placed = true
				rest = item[i:]
				break
			}
		}
		if !placed {
			d.para("ListParagraph", fmt.Sprintf(`<w:numPr><w:ilvl w:val="%d"/><w:numId w:val="%d"/></w:numPr>`, ilvl, numID), "")
			continue
		}
		c := ctx
		c.list++
		for _, k := range rest {
			if k.Kind == text.NPara {
				d.para("ListParagraph", fmt.Sprintf(`<w:ind w:left="%d" w:firstLine="0"/>`, 720*(ilvl+1)), d.inlines(k.Inl, runState{}))
			} else {
				d.blocks([]text.Node{k}, c, level)
			}
		}
	}
}

const maxTableCols = 63

func (d *docxWriter) table(n text.Node, ctx blockCtx) {
	cols := 0
	for _, r := range n.Rows {
		cols = max(cols, len(r.Cells))
	}
	if cols == 0 || len(n.Rows) == 0 {
		return
	}
	if cols > maxTableCols {
		d.b.warn(fmt.Sprintf("A table has more than %d columns; the rest were left out.", maxTableCols))
		cols = maxTableCols
	}
	w := d.pg.textW / cols
	var t strings.Builder
	t.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="5000" w:type="pct"/><w:tblLayout w:type="fixed"/><w:tblLook w:val="04A0" w:firstRow="1" w:lastRow="0" w:firstColumn="1" w:lastColumn="0" w:noHBand="0" w:noVBand="1"/></w:tblPr><w:tblGrid>`)
	for i := 0; i < cols; i++ {
		fmt.Fprintf(&t, `<w:gridCol w:w="%d"/>`, w)
	}
	t.WriteString(`</w:tblGrid>`)
	for _, r := range n.Rows {
		t.WriteString(`<w:tr>`)
		if r.Head {
			t.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
		}
		for i := 0; i < cols; i++ {
			var cell text.Cell
			if i < len(r.Cells) {
				cell = r.Cells[i]
			}
			jc := ""
			switch cell.Align {
			case "right":
				jc = `<w:jc w:val="right"/>`
			case "center":
				jc = `<w:jc w:val="center"/>`
			}
			fmt.Fprintf(&t, `<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/></w:tcPr><w:p>%s%s</w:p></w:tc>`, w, pPr("TableText", jc), d.inlines(cell.Inl, runState{bold: r.Head}))
		}
		t.WriteString(`</w:tr>`)
	}
	t.WriteString(`</w:tbl>`)
	d.body.WriteString(t.String())
	d.body.WriteString(`<w:p>` + pPr("TableText", "") + `</w:p>`) // a table is never last, and never touches the next one
}

// ---- images -----------------------------------------------------------------------------------

const emuPerPx = 9525

func (d *docxWriter) image(x text.Inline, st runState) string {
	img, ok := d.b.Images[x.Text]
	// A picture goes in the body only: a footnote has no relationships of its own.
	if d.inNote || !ok || len(img.Data) == 0 || img.Width <= 0 || img.Height <= 0 || (img.Ext != "png" && img.Ext != "jpg" && img.Ext != "gif") {
		if ok && img.Ext == "webp" {
			d.b.warn("A WebP image was left out: Word cannot embed it. Use PNG or JPEG.")
		}
		alt := strings.TrimSpace(x.Alt)
		label := "[image"
		if alt != "" {
			label += ": " + alt
		}
		return d.run(label+" (not included)]", st)
	}
	rid, ok := d.imageRel[x.Text]
	if !ok {
		d.nextRel++
		rid = fmt.Sprintf("rId%d", d.nextRel)
		d.imageRel[x.Text] = rid
		name := fmt.Sprintf("media/image%d.%s", len(d.media)+1, img.Ext)
		d.media = append(d.media, zipEntry{name: "word/" + name, data: img.Data, store: true})
		d.mediaExt[img.Ext] = true
		fmt.Fprintf(&d.relXML, `<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="%s"/>`, rid, name)
	}
	w, h := int64(img.Width)*emuPerPx, int64(img.Height)*emuPerPx
	maxW := int64(d.pg.textW) * 635 // twips to EMU
	maxH := int64(d.pg.h-2*d.pg.margin) * 635 * 8 / 10
	if w > maxW {
		h = h * maxW / w
		w = maxW
	}
	if h > maxH {
		w = w * maxH / h
		h = maxH
	}
	if w < 1 || h < 1 {
		w, h = 1, 1
	}
	d.docPr++
	n := d.docPr
	alt := xmlEscape(truncRunes(strings.TrimSpace(x.Alt), 250))
	return fmt.Sprintf(`<w:r><w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="%d" cy="%d"/><wp:docPr id="%d" name="Picture %d" descr="%s"/><wp:cNvGraphicFramePr><a:graphicFrameLocks noChangeAspect="1"/></wp:cNvGraphicFramePr><a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic><pic:nvPicPr><pic:cNvPr id="%d" name="Picture %d"/><pic:cNvPicPr/></pic:nvPicPr><pic:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill><pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr></pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r>`,
		w, h, n, n, alt, n, n, rid, w, h)
}

func truncRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// ---- title page and sections -------------------------------------------------------------------

func (d *docxWriter) titlePage() {
	b, o := d.b, d.o
	title := CleanLine(b.Title)
	byline := CleanLine(b.Byline)
	var count string
	switch o.WordCount {
	case "none":
	case "label":
		count = "Word count: " + Thousands(RoundedWords(b.Words))
	default:
		if o.TitleLayout == "manuscript" {
			count = "about " + Thousands(RoundedWords(b.Words)) + " words"
		}
	}
	if o.TitleLayout == "manuscript" {
		// Contact block top left, word count top right on the first line.
		name := firstNonEmpty(CleanLine(b.Contact.Name), byline)
		lines := []string{}
		if On(o.Contact) {
			for _, l := range b.Contact.Address {
				if l = CleanLine(l); l != "" {
					lines = append(lines, l)
				}
			}
			if e := CleanLine(b.Contact.Email); e != "" {
				lines = append(lines, e)
			}
			if p := CleanLine(b.Contact.Phone); p != "" {
				lines = append(lines, p)
			}
		} else {
			name = "" // the block is left off; the word count stays top right
		}
		first := d.run(name, runState{})
		if count != "" {
			first += `<w:r><w:tab/></w:r>` + d.run(count, runState{})
		}
		if first != "" {
			d.para("ContactBlock", "", first)
		}
		for _, l := range lines {
			d.para("ContactBlock", "", d.run(l, runState{}))
		}
	}
	d.para("Title", "", d.run(title, runState{}))
	if st := CleanLine(b.Subtitle); st != "" {
		d.para("Subtitle", "", d.run(st, runState{}))
	}
	if byline != "" {
		d.para("Byline", "", d.run("by "+byline, runState{}))
	}
	if o.TitleLayout != "manuscript" && count != "" {
		d.para("Byline", "", d.run(count, runState{}))
	}
}

func (d *docxWriter) sections() {
	o := d.o
	hasTitle := On(o.TitlePage)
	newPage := o.ChapterStart != "continuous"
	spacer := newPage && twips(o.drop()) > 0
	for i, s := range d.b.Sections {
		heading := HeadingText(s, o)
		level := s.Level
		if level < 1 {
			level = 1
		}
		style := "Heading1"
		if s.Kind == SecChapter && level >= 2 {
			style = "ChapterTitle"
		}
		// How the section starts its page. With a drop, an empty paragraph of
		// that height (style Chapter Opening) starts the page and the heading
		// follows it: a space before a heading is ignored at the top of a page by
		// some programs, a paragraph of exact height is not. Without a drop the
		// heading style itself breaks the page.
		direct := false // the first paragraph of the section must carry the break itself
		switch {
		case spacer:
			d.para("ChapterOpening", "", "")
		case newPage:
			direct = heading == "" && (i > 0 || hasTitle)
		default:
			direct = i == 0 && hasTitle
		}
		if heading != "" {
			extra := ""
			if direct {
				extra = `<w:pageBreakBefore/>`
			}
			d.para(style, extra, d.run(heading, runState{}))
		} else if direct {
			d.sectionBreakNext = true
		}
		bodyLevel := level
		if heading == "" {
			bodyLevel = level - 1
		}
		d.sectionBody(s, bodyLevel)
	}
}

func (d *docxWriter) sectionBody(s Section, level int) {
	// A section without a heading starts its page on its first paragraph.
	if d.sectionBreakNext {
		d.sectionBreakNext = false
		if len(s.Nodes) > 0 && s.Nodes[0].Kind == text.NPara {
			d.para("FirstParagraph", `<w:pageBreakBefore/>`, d.inlines(s.Nodes[0].Inl, runState{}))
			d.blocks(s.Nodes[1:], blockCtx{}, level)
			d.noteSection(s)
			return
		}
	}
	d.blocks(s.Nodes, blockCtx{}, level)
	d.noteSection(s)
}

// noteSection renders a section's footnotes into footnotes.xml.
func (d *docxWriter) noteSection(s Section) {
	for _, f := range s.Notes {
		if !d.noteIDs[f.Num] {
			continue
		}
		saveBody := d.body
		d.body = strings.Builder{}
		d.inNote = true
		d.blocks(f.Nodes, blockCtx{note: true}, 1)
		d.inNote = false
		inner := d.body.String()
		d.body = saveBody
		// the first paragraph carries the note's own mark
		mark := `<w:r><w:rPr><w:rStyle w:val="FootnoteReference"/></w:rPr><w:footnoteRef/></w:r><w:r><w:t xml:space="preserve"> </w:t></w:r>`
		if i := strings.Index(inner, `</w:pPr>`); i >= 0 && strings.HasPrefix(inner, `<w:p><w:pPr>`) {
			inner = inner[:i+len(`</w:pPr>`)] + mark + inner[i+len(`</w:pPr>`):]
		} else {
			inner = `<w:p><w:pPr><w:pStyle w:val="FootnoteText"/></w:pPr>` + mark + `</w:p>` + inner
		}
		fmt.Fprintf(&d.notes, `<w:footnote w:id="%d">%s</w:footnote>`, f.Num, inner)
	}
}

// ---- the other parts --------------------------------------------------------------------------

func (d *docxWriter) sectPr(header, footer, titlePg bool) string {
	var s strings.Builder
	s.WriteString(`<w:sectPr>`)
	if header {
		s.WriteString(`<w:headerReference w:type="default" r:id="rId11"/>`)
		if titlePg {
			s.WriteString(`<w:headerReference w:type="first" r:id="rId12"/>`)
		}
	}
	if footer {
		s.WriteString(`<w:footerReference w:type="default" r:id="rId13"/>`)
		if titlePg {
			s.WriteString(`<w:footerReference w:type="first" r:id="rId14"/>`)
		}
	}
	p := d.pg
	fmt.Fprintf(&s, `<w:pgSz w:w="%d" w:h="%d"/><w:pgMar w:top="%d" w:right="%d" w:bottom="%d" w:left="%d" w:header="720" w:footer="720" w:gutter="0"/>`, p.w, p.h, p.margin, p.margin, p.margin, p.margin)
	s.WriteString(`<w:cols w:space="720"/>`)
	if titlePg {
		s.WriteString(`<w:titlePg/>`)
	}
	if d.rtl {
		s.WriteString(`<w:bidi/>`)
	}
	s.WriteString(`</w:sectPr>`)
	return s.String()
}

func (d *docxWriter) docRels(footer bool) string {
	var s strings.Builder
	s.WriteString(xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	rel := func(id, typ, target string) {
		fmt.Fprintf(&s, `<Relationship Id="%s" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/%s" Target="%s"/>`, id, typ, target)
	}
	rel("rId1", "styles", "styles.xml")
	rel("rId2", "settings", "settings.xml")
	rel("rId3", "numbering", "numbering.xml")
	rel("rId4", "footnotes", "footnotes.xml")
	rel("rId11", "header", "header1.xml")
	rel("rId12", "header", "header2.xml")
	if footer {
		rel("rId13", "footer", "footer1.xml")
		rel("rId14", "footer", "footer2.xml")
	}
	s.WriteString(d.relXML.String())
	s.WriteString(`</Relationships>`)
	return s.String()
}

func (d *docxWriter) footnotesXML() string {
	return xmlDecl + `<w:footnotes xmlns:w="` + nsW + `" xmlns:r="` + nsR + `">` +
		`<w:footnote w:type="separator" w:id="-1"><w:p><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:r><w:separator/></w:r></w:p></w:footnote>` +
		`<w:footnote w:type="continuationSeparator" w:id="0"><w:p><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr><w:r><w:continuationSeparator/></w:r></w:p></w:footnote>` +
		d.notes.String() + `</w:footnotes>`
}

func pageField() string {
	return `<w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>2</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r>`
}

// headerLead is the text of the running header before the page number.
func (d *docxWriter) headerLead() string {
	switch d.o.Header {
	case "surname-title-page":
		return joinNonEmpty(" / ", CleanLine(d.b.Surname), d.headerTitle())
	case "title-page":
		return d.headerTitle()
	}
	return ""
}

func (d *docxWriter) headerTitle() string {
	t := RunningTitle(d.b, d.o)
	if Set(d.o.HeaderUpper) {
		return strings.ToUpper(t)
	}
	return t
}

// headerXML is the running header (default) or the empty first-page header.
func (d *docxWriter) headerXML(def bool) string {
	content := ""
	if def {
		lead := d.headerLead()
		page := d.o.PageNumbers == "header"
		if lead != "" {
			if page {
				lead += " / "
			}
			content = `<w:r><w:t xml:space="preserve">` + xmlEscape(lead) + `</w:t></w:r>`
		}
		if page {
			content += pageField()
		}
	}
	return xmlDecl + `<w:hdr xmlns:w="` + nsW + `" xmlns:r="` + nsR + `"><w:p><w:pPr><w:pStyle w:val="Header"/></w:pPr>` + content + `</w:p></w:hdr>`
}

func (d *docxWriter) footerXML(def bool) string {
	content := ""
	if def {
		content = pageField()
	}
	return xmlDecl + `<w:ftr xmlns:w="` + nsW + `" xmlns:r="` + nsR + `"><w:p><w:pPr><w:pStyle w:val="Footer"/></w:pPr>` + content + `</w:p></w:ftr>`
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

func (d *docxWriter) coreXML() string {
	ts := d.b.Modified.UTC().Format("2006-01-02T15:04:05Z")
	return xmlDecl + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">` +
		`<dc:title>` + xmlEscape(CleanLine(d.b.Title)) + `</dc:title>` +
		`<dc:creator>` + xmlEscape(CleanLine(d.b.Byline)) + `</dc:creator>` +
		`<dc:language>` + xmlEscape(d.lang) + `</dc:language>` +
		`<cp:revision>1</cp:revision>` +
		`<dcterms:created xsi:type="dcterms:W3CDTF">` + ts + `</dcterms:created>` +
		`<dcterms:modified xsi:type="dcterms:W3CDTF">` + ts + `</dcterms:modified>` +
		`</cp:coreProperties>`
}

func (d *docxWriter) appXML() string {
	return xmlDecl + `<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties"><Application>Scr&#237;obh</Application><DocSecurity>0</DocSecurity><Words>` + itoa(d.b.Words) + `</Words></Properties>`
}

// HeaderText is the running header as it reads on a page: 'Surname / TITLE / 2'.
// It is what the preview shows; the DOCX uses a PAGE field.
func HeaderText(b *Book, o Options, page int) string {
	d := &docxWriter{b: b, o: docxDefaults(o)}
	lead := d.headerLead()
	if d.o.PageNumbers == "header" {
		return joinNonEmpty(" / ", lead, itoa(page))
	}
	return lead
}

// FooterText is the page number in the footer, when the options put it there.
func FooterText(o Options, page int) string {
	if docxDefaults(o).PageNumbers == "footer" {
		return itoa(page)
	}
	return ""
}
