package publish

import (
	"fmt"
	"strings"
)

// The fixed parts of a DOCX: the styles, the settings, the numbering and the
// package plumbing. Child elements follow the order the OOXML schema demands
// (Word refuses a file whose properties are out of order), and the Go validator
// in internal/publish/check checks the order of everything written here.

const (
	nsW   = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsR   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	nsWP  = "http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
	nsA   = "http://schemas.openxmlformats.org/drawingml/2006/main"
	nsPic = "http://schemas.openxmlformats.org/drawingml/2006/picture"

	xmlDecl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"
)

// page is the page geometry of a document, in twips.
type page struct {
	w, h, margin, textW int
}

func pageOf(o Options) page {
	p := page{w: 12240, h: 15840}
	if w, h, ok := PageSize(o.PageSize); ok {
		p = page{w: int((w + 25) / 50), h: int((h + 25) / 50)} // milli-points to twentieths of a point
	}
	m := o.MarginInches
	if m == 0 {
		m = 1
	}
	p.margin = int(m*1440 + 0.5)
	p.textW = p.w - 2*p.margin
	return p
}

func twips(inches float64) int { return int(inches*1440 + 0.5) }

// docxDefaults fills what a caller left unset with the manuscript defaults, so
// a writer never meets a zero font size.
func docxDefaults(o Options) Options {
	if o.Font == "" {
		o.Font = "Times New Roman"
	}
	if o.FontSize == 0 {
		o.FontSize = 12
	}
	if o.LineSpacing == 0 {
		o.LineSpacing = 2
	}
	if o.ChapterHeading == "" {
		o.ChapterHeading = "number-title"
	}
	if o.ChapterStart == "" {
		o.ChapterStart = "new-page"
	}
	if o.TitleLayout == "" {
		o.TitleLayout = "manuscript"
	}
	if o.PageNumbers == "" {
		o.PageNumbers = "header"
	}
	if o.Header == "" {
		o.Header = "title-page"
	}
	if o.SceneBreak == "" {
		o.SceneBreak = "hash"
	}
	if o.IndentInches == nil {
		o.IndentInches = F(0.5)
	}
	return o
}

func langTag(l string) string {
	if l == "" {
		return "en-US"
	}
	if !strings.ContainsRune(l, '-') && strings.EqualFold(l, "en") {
		return "en-US"
	}
	return l
}

func (d *docxWriter) styles() string {
	o := d.o
	p := d.pg
	f := xmlEscape(o.Font)
	half := int(o.FontSize*2 + 0.5)
	line := int(o.LineSpacing*240 + 0.5)
	after := int(o.spaceAfter()*20 + 0.5)
	ind := twips(o.indent())
	manuscript := o.TitleLayout == "manuscript"
	jc := ""
	if Set(o.Justify) {
		jc = `<w:jc w:val="both"/>`
	}
	bidi := ""
	if d.rtl {
		bidi = `<w:bidi/>`
	}
	var b strings.Builder
	b.WriteString(xmlDecl)
	b.WriteString(`<w:styles xmlns:w="` + nsW + `">`)
	fmt.Fprintf(&b, `<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="%s" w:hAnsi="%s" w:eastAsia="%s" w:cs="%s"/><w:sz w:val="%d"/><w:szCs w:val="%d"/><w:lang w:val="%s" w:eastAsia="%s" w:bidi="ar-SA"/></w:rPr></w:rPrDefault>`,
		f, f, f, f, half, half, xmlEscape(d.lang), xmlEscape(d.lang))
	fmt.Fprintf(&b, `<w:pPrDefault><w:pPr>%s<w:spacing w:after="0" w:line="240" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>`, bidi)

	style := func(kind, id, name string, attrs string, body string) {
		fmt.Fprintf(&b, `<w:style w:type="%s" w:styleId="%s"%s><w:name w:val="%s"/>%s</w:style>`, kind, id, attrs, name, body)
	}
	// Defaults every document names.
	style("paragraph", "Normal", "Normal", ` w:default="1"`,
		fmt.Sprintf(`<w:qFormat/><w:pPr><w:spacing w:after="%d" w:line="%d" w:lineRule="auto"/>%s</w:pPr>`, after, line, jc))
	style("character", "DefaultParagraphFont", "Default Paragraph Font", ` w:default="1"`, `<w:uiPriority w:val="1"/><w:semiHidden/><w:unhideWhenUsed/>`)
	style("table", "TableNormal", "Normal Table", ` w:default="1"`,
		`<w:uiPriority w:val="99"/><w:semiHidden/><w:unhideWhenUsed/><w:tblPr><w:tblInd w:w="0" w:type="dxa"/><w:tblCellMar><w:top w:w="0" w:type="dxa"/><w:left w:w="108" w:type="dxa"/><w:bottom w:w="0" w:type="dxa"/><w:right w:w="108" w:type="dxa"/></w:tblCellMar></w:tblPr>`)
	style("numbering", "NoList", "No List", ` w:default="1"`, `<w:uiPriority w:val="99"/><w:semiHidden/><w:unhideWhenUsed/>`)

	// Body text.
	style("paragraph", "BodyText", "Body Text", "",
		fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:ind w:firstLine="%d"/></w:pPr>`, ind))
	style("paragraph", "FirstParagraph", "First Paragraph", "",
		`<w:basedOn w:val="BodyText"/><w:next w:val="BodyText"/><w:qFormat/><w:pPr><w:ind w:firstLine="0"/></w:pPr>`)

	// Headings. Heading 1 is the chapter (or part) opening; the others are
	// subheadings inside a text and never break the page.
	// The opening of a chapter: with a drop, the Chapter Opening paragraph breaks
	// the page and takes the space; without one the heading breaks the page.
	h1pb := ""
	h1before := 0
	switch {
	case o.ChapterStart == "continuous":
		h1before = 480
	case twips(o.drop()) == 0:
		h1pb = `<w:pageBreakBefore/>`
	}
	h1size, hsize := half, half
	h1bold, hbold := "", ""
	h1after := line
	hjc := `<w:jc w:val="center"/>`
	if !manuscript {
		h1size, hsize = half+8, half+4
		h1bold, hbold = `<w:b/><w:bCs/>`, `<w:b/><w:bCs/>`
		h1after = 360
		hjc = `<w:jc w:val="left"/>`
	}
	style("paragraph", "Heading1", "heading 1", "",
		fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:next w:val="FirstParagraph"/><w:uiPriority w:val="9"/><w:qFormat/><w:pPr><w:keepNext/><w:keepLines/>%s<w:spacing w:before="%d" w:after="%d" w:line="%d" w:lineRule="auto"/><w:ind w:firstLine="0"/>%s<w:outlineLvl w:val="0"/></w:pPr><w:rPr>%s<w:sz w:val="%d"/><w:szCs w:val="%d"/></w:rPr>`,
			h1pb, h1before, h1after, 240, `<w:jc w:val="center"/>`, h1bold, h1size, h1size))
	style("paragraph", "ChapterOpening", "Chapter Opening", "",
		fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:next w:val="Heading1"/><w:pPr><w:keepNext/><w:pageBreakBefore/><w:spacing w:before="0" w:after="0" w:line="%d" w:lineRule="exact"/><w:ind w:firstLine="0"/></w:pPr>`, max(twips(o.drop()), 20)))
	style("paragraph", "ChapterTitle", "Chapter Title", "",
		`<w:basedOn w:val="Heading1"/><w:next w:val="FirstParagraph"/><w:qFormat/><w:pPr><w:outlineLvl w:val="1"/></w:pPr>`)
	for lv := 2; lv <= 6; lv++ {
		sz := hsize
		if !manuscript && lv > 3 {
			sz = half + 2
		}
		before, aft := 0, 0
		if !manuscript {
			before, aft = 360, 120
		}
		style("paragraph", fmt.Sprintf("Heading%d", lv), fmt.Sprintf("heading %d", lv), "",
			fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:next w:val="FirstParagraph"/><w:uiPriority w:val="9"/><w:unhideWhenUsed/><w:qFormat/><w:pPr><w:keepNext/><w:keepLines/><w:spacing w:before="%d" w:after="%d" w:line="%d" w:lineRule="auto"/><w:ind w:firstLine="0"/>%s<w:outlineLvl w:val="%d"/></w:pPr><w:rPr>%s<w:sz w:val="%d"/><w:szCs w:val="%d"/></w:rPr>`,
				before, aft, line, hjc, lv-1, hbold, sz, sz))
	}

	// Title page.
	titleBefore := twips(2.2)
	titleSize := half
	titleBold := ""
	if !manuscript {
		titleBefore, titleSize, titleBold = twips(2.0), half+16, `<w:b/><w:bCs/>`
	}
	style("paragraph", "Title", "Title", "",
		fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:next w:val="Byline"/><w:uiPriority w:val="10"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="%d" w:after="%d" w:line="240" w:lineRule="auto"/><w:ind w:firstLine="0"/><w:jc w:val="center"/></w:pPr><w:rPr>%s<w:sz w:val="%d"/><w:szCs w:val="%d"/></w:rPr>`,
			titleBefore, 240, titleBold, titleSize, titleSize))
	style("paragraph", "Subtitle", "Subtitle", "",
		fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:next w:val="Byline"/><w:uiPriority w:val="11"/><w:qFormat/><w:pPr><w:spacing w:before="0" w:after="240" w:line="240" w:lineRule="auto"/><w:ind w:firstLine="0"/><w:jc w:val="center"/></w:pPr><w:rPr><w:i/><w:iCs/><w:sz w:val="%d"/><w:szCs w:val="%d"/></w:rPr>`, half, half))
	style("paragraph", "Byline", "Byline", "",
		`<w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/><w:ind w:firstLine="0"/><w:jc w:val="center"/></w:pPr>`)
	style("paragraph", "ContactBlock", "Contact Block", "",
		fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:tabs><w:tab w:val="right" w:pos="%d"/></w:tabs><w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/><w:ind w:firstLine="0"/></w:pPr>`, p.textW))

	// Scene breaks and the end.
	style("paragraph", "SceneBreak", "Scene Break", "",
		fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:next w:val="FirstParagraph"/><w:qFormat/><w:pPr><w:keepLines/><w:spacing w:before="0" w:after="0" w:line="%d" w:lineRule="auto"/><w:ind w:firstLine="0"/><w:jc w:val="center"/></w:pPr>`, line))
	style("paragraph", "TheEnd", "The End", "",
		fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:before="%d" w:after="0" w:line="%d" w:lineRule="auto"/><w:ind w:firstLine="0"/><w:jc w:val="center"/></w:pPr>`, line, line))

	// Quotes, lists, code, figures, tables.
	style("paragraph", "Quote", "Quote", "",
		`<w:basedOn w:val="Normal"/><w:next w:val="FirstParagraph"/><w:uiPriority w:val="29"/><w:qFormat/><w:pPr><w:ind w:left="720" w:right="720" w:firstLine="0"/></w:pPr>`)
	style("paragraph", "ListParagraph", "List Paragraph", "",
		`<w:basedOn w:val="Normal"/><w:uiPriority w:val="34"/><w:qFormat/><w:pPr><w:ind w:left="720" w:firstLine="0"/><w:contextualSpacing/></w:pPr>`)
	style("paragraph", "Code", "Code", "",
		`<w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/><w:ind w:left="360" w:firstLine="0"/></w:pPr><w:rPr><w:rFonts w:ascii="Courier New" w:hAnsi="Courier New" w:eastAsia="Courier New" w:cs="Courier New"/><w:sz w:val="20"/><w:szCs w:val="20"/></w:rPr>`)
	style("character", "CodeChar", "Code Char", "",
		`<w:basedOn w:val="DefaultParagraphFont"/><w:rPr><w:rFonts w:ascii="Courier New" w:hAnsi="Courier New" w:eastAsia="Courier New" w:cs="Courier New"/></w:rPr>`)
	style("paragraph", "Figure", "Figure", "",
		`<w:basedOn w:val="Normal"/><w:next w:val="FirstParagraph"/><w:qFormat/><w:pPr><w:keepNext/><w:spacing w:before="120" w:after="120" w:line="240" w:lineRule="auto"/><w:ind w:firstLine="0"/><w:jc w:val="center"/></w:pPr>`)
	style("paragraph", "TableText", "Table Text", "",
		`<w:basedOn w:val="Normal"/><w:qFormat/><w:pPr><w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/><w:ind w:firstLine="0"/></w:pPr>`)
	style("table", "TableGrid", "Table Grid", "",
		`<w:basedOn w:val="TableNormal"/><w:uiPriority w:val="39"/><w:tblPr><w:tblBorders><w:top w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:left w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:bottom w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:right w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:insideH w:val="single" w:sz="4" w:space="0" w:color="auto"/><w:insideV w:val="single" w:sz="4" w:space="0" w:color="auto"/></w:tblBorders></w:tblPr>`)

	// Notes, header and footer.
	style("paragraph", "FootnoteText", "footnote text", "",
		fmt.Sprintf(`<w:basedOn w:val="Normal"/><w:uiPriority w:val="99"/><w:unhideWhenUsed/><w:pPr><w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/><w:ind w:firstLine="0"/></w:pPr><w:rPr><w:sz w:val="%d"/><w:szCs w:val="%d"/></w:rPr>`, max(half-4, 16), max(half-4, 16)))
	style("character", "FootnoteReference", "footnote reference", "",
		`<w:basedOn w:val="DefaultParagraphFont"/><w:uiPriority w:val="99"/><w:unhideWhenUsed/><w:rPr><w:vertAlign w:val="superscript"/></w:rPr>`)
	style("paragraph", "Header", "header", "",
		`<w:basedOn w:val="Normal"/><w:uiPriority w:val="99"/><w:unhideWhenUsed/><w:pPr><w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/><w:ind w:firstLine="0"/><w:jc w:val="right"/></w:pPr>`)
	style("paragraph", "Footer", "footer", "",
		`<w:basedOn w:val="Normal"/><w:uiPriority w:val="99"/><w:unhideWhenUsed/><w:pPr><w:spacing w:before="0" w:after="0" w:line="240" w:lineRule="auto"/><w:ind w:firstLine="0"/><w:jc w:val="center"/></w:pPr>`)
	b.WriteString(`</w:styles>`)
	return b.String()
}

func settingsXML(lang string) string {
	return xmlDecl + `<w:settings xmlns:w="` + nsW + `"><w:zoom w:percent="100"/><w:defaultTabStop w:val="720"/><w:characterSpacingControl w:val="doNotCompress"/>` +
		`<w:footnotePr><w:footnote w:id="-1"/><w:footnote w:id="0"/></w:footnotePr>` +
		`<w:compat><w:compatSetting w:name="compatibilityMode" w:uri="http://schemas.microsoft.com/office/word" w:val="15"/></w:compat>` +
		`<w:themeFontLang w:val="` + xmlEscape(lang) + `"/></w:settings>`
}

// numberingXML defines one bullet list and one decimal list (nine levels each),
// and an instance of the decimal list for each ordered list of the document, so
// that each starts where its Markdown says.
func numberingXML(starts []int) string {
	var b strings.Builder
	b.WriteString(xmlDecl + `<w:numbering xmlns:w="` + nsW + `">`)
	abstract := func(id int, bullet bool) {
		fmt.Fprintf(&b, `<w:abstractNum w:abstractNumId="%d"><w:multiLevelType w:val="hybridMultilevel"/>`, id)
		for lv := 0; lv < 9; lv++ {
			left := 720 * (lv + 1)
			if bullet {
				sym := []string{"•", "o", "▪"}[lv%3]
				font := ""
				if lv%3 == 1 {
					font = `<w:rPr><w:rFonts w:ascii="Courier New" w:hAnsi="Courier New" w:cs="Courier New" w:hint="default"/></w:rPr>`
				}
				fmt.Fprintf(&b, `<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="bullet"/><w:lvlText w:val="%s"/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr>%s</w:lvl>`, lv, sym, left, font)
			} else {
				fmts := []string{"decimal", "lowerLetter", "lowerRoman"}
				fmt.Fprintf(&b, `<w:lvl w:ilvl="%d"><w:start w:val="1"/><w:numFmt w:val="%s"/><w:lvlText w:val="%%%d."/><w:lvlJc w:val="left"/><w:pPr><w:ind w:left="%d" w:hanging="360"/></w:pPr></w:lvl>`, lv, fmts[lv%3], lv+1, left)
			}
		}
		b.WriteString(`</w:abstractNum>`)
	}
	abstract(0, true)
	abstract(1, false)
	b.WriteString(`<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>`)
	for i, s := range starts {
		fmt.Fprintf(&b, `<w:num w:numId="%d"><w:abstractNumId w:val="1"/><w:lvlOverride w:ilvl="0"><w:startOverride w:val="%d"/></w:lvlOverride></w:num>`, i+2, s)
	}
	b.WriteString(`</w:numbering>`)
	return b.String()
}

func contentTypesXML(imageExts []string, hasFooter bool) string {
	var b strings.Builder
	b.WriteString(xmlDecl + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	b.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>`)
	mimes := map[string]string{"png": "image/png", "jpg": "image/jpeg", "gif": "image/gif"}
	for _, e := range imageExts {
		fmt.Fprintf(&b, `<Default Extension="%s" ContentType="%s"/>`, e, mimes[e])
	}
	ov := func(part, ct string) {
		fmt.Fprintf(&b, `<Override PartName="%s" ContentType="application/vnd.openxmlformats-%s"/>`, part, ct)
	}
	ov("/word/document.xml", "officedocument.wordprocessingml.document.main+xml")
	ov("/word/styles.xml", "officedocument.wordprocessingml.styles+xml")
	ov("/word/settings.xml", "officedocument.wordprocessingml.settings+xml")
	ov("/word/numbering.xml", "officedocument.wordprocessingml.numbering+xml")
	ov("/word/footnotes.xml", "officedocument.wordprocessingml.footnotes+xml")
	ov("/word/header1.xml", "officedocument.wordprocessingml.header+xml")
	ov("/word/header2.xml", "officedocument.wordprocessingml.header+xml")
	if hasFooter {
		ov("/word/footer1.xml", "officedocument.wordprocessingml.footer+xml")
		ov("/word/footer2.xml", "officedocument.wordprocessingml.footer+xml")
	}
	ov("/docProps/core.xml", "package.core-properties+xml")
	ov("/docProps/app.xml", "officedocument.extended-properties+xml")
	b.WriteString(`</Types>`)
	return b.String()
}

const rootRels = xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
	`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
	`<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` +
	`<Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` +
	`</Relationships>`
