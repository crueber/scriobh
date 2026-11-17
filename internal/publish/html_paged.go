package publish

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/publish/pdfw"
)

// WriteHTMLPaged writes the book as one self-contained HTML file with CSS
// paged media: @page for the trim size and margins (mirrored for left and
// right pages), margin boxes for the running heads and page numbers, named
// pages so that chapter openers have no head, break-before for chapters and
// orphans and widows. A browser prints it to a PDF; a print engine that
// supports more of paged media (Prince, WeasyPrint, Paged.js) does better.
//
// The file has one inline stylesheet, allowed by its hash in a
// Content-Security-Policy meta that permits nothing else but data: images; it
// has no script, no external resource and no live link to another page. The
// stylesheet is written in this file because the file is a download, never a
// page of the application.
//
// What a browser cannot do is said in the file's own comments and in the
// documentation: footnotes are endnotes at the end of each chapter (CSS
// float: footnote is not implemented by browsers), the contents carry no page
// numbers in a browser (target-counter is not either), and hyphenation is off.
func WriteHTMLPaged(b *Book, o Options) ([]byte, []Warn, error) {
	o = printDefaults(o)
	lang := firstNonEmpty(o.Language, b.Language, "en")
	if !validLang(lang) {
		lang = "en"
	}
	dir := ""
	if IsRTL(lang) {
		dir = ` dir="rtl"`
	}
	pr := resolveParams(o)
	var warns []Warn
	css := pagedCSS(b, o, pr)
	sum := sha256.Sum256([]byte(css))
	csp := "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; img-src data:"

	var s strings.Builder
	s.WriteString("<!DOCTYPE html>\n")
	fmt.Fprintf(&s, `<html lang="%s"%s>`+"\n<head>\n", xmlEscape(lang), dir)
	s.WriteString(`<meta charset="utf-8">` + "\n")
	s.WriteString(`<meta http-equiv="Content-Security-Policy" content="` + attrEscape(csp) + `">` + "\n")
	s.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">` + "\n")
	title := CleanLine(b.Title)
	if title == "" {
		title = "Untitled"
	}
	s.WriteString("<title>" + xmlEscape(title) + "</title>\n")
	if a := CleanLine(b.Byline); a != "" {
		s.WriteString(`<meta name="author" content="` + xmlEscape(a) + `">` + "\n")
	}
	// The hash covers exactly the text between the tags, so there is nothing else there.
	s.WriteString("<style>" + css + "</style>\n</head>\n<body>\n")

	images := map[string]string{} // key -> data URI
	var imgBytes int
	imageSrc := func(key string) (string, bool) {
		if u, ok := images[key]; ok {
			return u, true
		}
		img, ok := b.Images[key]
		if !ok || len(img.Data) == 0 || !embeddable(img.Ext) {
			return "", false
		}
		if imgBytes+len(img.Data) > 24<<20 {
			return "", false
		}
		imgBytes += len(img.Data)
		u := "data:" + img.Mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
		images[key] = u
		return u, true
	}

	if On(o.TitlePage) {
		s.WriteString(`<section class="titlepage">` + "\n")
		s.WriteString(`<h1 class="title">` + xmlEscape(title) + "</h1>\n")
		if st := CleanLine(b.Subtitle); st != "" {
			s.WriteString(`<p class="subtitle">` + xmlEscape(st) + "</p>\n")
		}
		if a := CleanLine(b.Byline); a != "" {
			s.WriteString(`<p class="byline">` + xmlEscape(a) + "</p>\n")
		}
		s.WriteString("</section>\n")
	}
	if pr.copyright {
		txt := o.Imprint
		if strings.TrimSpace(txt) == "" {
			if a := CleanLine(b.Byline); a != "" {
				txt = "Copyright © " + a + "\nAll rights reserved."
			} else {
				txt = "All rights reserved."
			}
		}
		s.WriteString(`<section class="copyright">` + "\n")
		for _, ln := range strings.Split(txt, "\n") {
			s.WriteString("<p>" + xmlEscape(ln) + "</p>\n")
		}
		s.WriteString("</section>\n")
	}
	kinds := make([]specialKind, len(b.Sections))
	var toc []tocEntry
	hasPart := false
	for _, sc := range b.Sections {
		if sc.Kind == SecPart {
			hasPart = true
		}
	}
	for i, sc := range b.Sections {
		kinds[i] = specialOf(sc)
		if kinds[i] != spNone {
			continue
		}
		label := HeadingText(sc, o)
		if label == "" {
			label = CleanLine(sc.Title)
		}
		if label == "" && sc.Number > 0 {
			label = firstNonEmpty(o.ChapterLabel, "Chapter") + " " + itoa(sc.Number)
		}
		if label == "" {
			continue
		}
		lv := 0
		if sc.Kind == SecChapter && hasPart && sc.Number > 0 {
			lv = 1
		}
		toc = append(toc, tocEntry{level: lv, title: label, sec: i})
	}
	if pr.toc && len(toc) > 0 {
		s.WriteString(`<nav class="toc"><h1>Contents</h1>` + "\n<ul>\n")
		for _, e := range toc {
			fmt.Fprintf(&s, `<li class="l%d"><a href="#s%d">%s</a></li>`+"\n", e.level, e.sec+1, xmlEscape(e.title))
		}
		s.WriteString("</ul>\n</nav>\n")
	}
	bodyStarted := false
	for i, sc := range b.Sections {
		x := &xhtmlWriter{b: b, o: o, paged: true, imageSrc: imageSrc}
		cls := map[SectionKind]string{SecFront: "frontmatter", SecPart: "part", SecChapter: "chapter", SecBack: "backmatter", SecNotes: "backmatter"}[sc.Kind]
		switch kinds[i] {
		case spDedication:
			cls = "dedication"
		case spEpigraph:
			cls = "epigraph"
		case spCopyright:
			continue // the generated or the written copyright page is one: the book's own is above
		}
		// Arabic numbers begin at the first part or chapter. The page's CSP has no
		// inline style attributes, so the counter reset is a class of the stylesheet.
		attr := ` class="` + cls + `"`
		if sc.Kind != SecFront && !bodyStarted {
			bodyStarted = true
			attr = ` class="` + cls + ` bodystart"`
		}
		fmt.Fprintf(&s, `<section id="s%d"%s>`+"\n", i+1, attr)
		switch {
		case sc.Kind == SecChapter:
			label, ttl := chapterParts(o, sc)
			if label != "" {
				s.WriteString(`<p class="chlabel">` + xmlEscape(label) + "</p>\n")
			}
			if ttl != "" {
				s.WriteString(`<h2 class="chtitle">` + xmlEscape(ttl) + "</h2>\n")
			}
			if Set(o.ChapterOrnament) && (label != "" || ttl != "") {
				s.WriteString(`<p class="orn">` + xmlEscape(SceneBreakText(o)) + "</p>\n")
			}
		case kinds[i] == spNone && CleanLine(sc.Title) != "":
			s.WriteString(`<h1 class="parttitle">` + xmlEscape(CleanLine(sc.Title)) + "</h1>\n")
		case sc.Kind == SecNotes:
			s.WriteString(`<h1 class="parttitle">Notes</h1>` + "\n")
		}
		x.blocks(sc.Nodes, 1)
		used := map[int]bool{}
		noteRefs(sc.Nodes, used)
		x.notes(sc.Notes, used)
		s.WriteString(x.out.String())
		if Set(o.TheEnd) && i == len(b.Sections)-1 {
			s.WriteString(`<p class="theend">THE END</p>` + "\n")
		}
		s.WriteString("</section>\n")
	}
	s.WriteString("</body>\n</html>\n")
	for key := range b.Images {
		if _, ok := images[key]; !ok && len(b.Images[key].Data) > 0 && embeddable(b.Images[key].Ext) {
			warns = append(warns, Warn{"image_not_embedded", "Some images were left out of the HTML file because it is limited to 24 MB of pictures."})
			break
		}
	}
	return []byte(s.String()), warns, nil
}

var attrEscaper = strings.NewReplacer("&", "&amp;", "\"", "&quot;", "<", "&lt;", ">", "&gt;")

// attrEscape escapes a double-quoted attribute value but leaves apostrophes.
func attrEscape(s string) string { return attrEscaper.Replace(CleanXML(s)) }

// cssString writes s as a CSS string: every character that is not plain ASCII
// text is a hexadecimal escape, so nothing in a title can end the string, the
// rule or the style element.
func cssString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range CleanLine(s) {
		switch {
		case r == ' ' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".,;:!?-_()[]'", r):
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "\\%X ", r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func cssLen(v mpt) string { return pdfw.U(v) + "pt" }

func pagedCSS(b *Book, o Options, pr params) string {
	justify := "left"
	if pr.justify {
		justify = "justify"
	}
	ind := "1.5em"
	if o.IndentInches != nil {
		ind = cssLen(pr.indent)
	}
	lh := o.LineSpacing
	if lh <= 0 {
		lh = 1.1
	}
	lh *= 1.15
	family := "'Liberation Serif', 'Times New Roman', Times, serif"
	if o.Font == "Liberation Mono" || o.Font == "Courier New" {
		family = "'Liberation Mono', 'Courier New', Courier, monospace"
	}
	headAuthor := cssString(CleanLine(b.Byline))
	headTitle := cssString(RunningTitle(b, o))
	if pr.upperHead {
		headAuthor = cssString(strings.ToUpper(CleanLine(b.Byline)))
		headTitle = cssString(strings.ToUpper(RunningTitle(b, o)))
	}
	var s strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&s, format, a...) }
	w("/* Scriobh paged HTML. Print this file from a browser with margins set to None and background graphics on.\n")
	w("   Footnotes are endnotes at the end of each chapter and the contents carry no page numbers in a browser;\n")
	w("   an engine that implements CSS paged media level 3 (Prince, WeasyPrint, Paged.js) does both. */\n")
	if pr.cropMarks {
		w("@page { size: %s %s; margin: %s %s %s %s; marks: crop; bleed: %s; }\n", cssLen(pr.pageW), cssLen(pr.pageH), cssLen(pr.top), cssLen(pr.outside), cssLen(pr.bottom), cssLen(pr.inside), cssLen(pr.bleed))
	} else {
		w("@page { size: %s %s; margin: %s %s %s %s; }\n", cssLen(pr.pageW), cssLen(pr.pageH), cssLen(pr.top), cssLen(pr.outside), cssLen(pr.bottom), cssLen(pr.inside))
	}
	w("@page :left { margin-left: %s; margin-right: %s; }\n", cssLen(pr.outside), cssLen(pr.inside))
	w("@page :right { margin-left: %s; margin-right: %s; }\n", cssLen(pr.inside), cssLen(pr.outside))
	margin := "font: italic 8.8pt " + family + "; color: #000;"
	if pr.upperHead {
		margin = "font: 8.8pt " + family + "; letter-spacing: .1em; color: #000;"
	}
	folio := "font: 9.9pt " + family + "; color: #000;"
	if pr.runningHeads {
		w("@page :left { @top-center { content: %s; %s } }\n", headAuthor, margin)
		w("@page :right { @top-center { content: %s; %s } }\n", headTitle, margin)
	}
	// Named pages: the front matter is numbered in roman, the body in arabic from
	// the first part or chapter; the first page of a chapter has no running head.
	w("@page blank { @top-center { content: none } @bottom-center { content: none } }\n")
	w("@page titlepage { @top-center { content: none } @bottom-center { content: none } }\n")
	if pr.folio != "none" {
		w("@page front { @bottom-center { content: counter(page, lower-roman); %s } }\n", folio)
		w("@page chapter { @bottom-center { content: counter(page); %s } }\n", folio)
	}
	w("@page front:first { @top-center { content: none } }\n")
	w("@page chapter:first { @top-center { content: none } }\n")
	w("html { font: %.4gpt/%.4g %s; hyphens: manual; -webkit-hyphens: manual; }\n", float64(pr.size)/1000, lh, family)
	w("body { margin: 0; }\n")
	w("p { margin: 0; text-indent: %s; text-align: %s; orphans: 2; widows: 2; }\n", ind, justify)
	w("p.first, h1 + p, h2 + p, h3 + p, h4 + p, p.chlabel + p, p.orn + p, p.scenebreak + p, blockquote p, li p, td p, th p, .footnote p { text-indent: 0; }\n")
	w("section { break-before: page; }\n")
	if pr.recto {
		w("section.chapter, section.part, section.frontmatter, section.backmatter, nav.toc, section.dedication, section.epigraph { break-before: right; }\n")
	}
	if pr.continuous {
		w("section.chapter { break-before: auto; }\n")
	}
	w("section.chapter, section.part, section.backmatter { page: chapter; }\n")
	w("section.frontmatter, nav.toc { page: front; }\n")
	w("section.titlepage { page: titlepage; text-align: center; padding-top: 22%%; break-before: auto; }\n")
	w("section.titlepage h1 { font-size: 2.4em; margin: 0 0 .4em; text-align: center; }\n")
	w("section.titlepage p { text-indent: 0; text-align: center; } .subtitle { font-style: italic; font-size: 1.4em; } .byline { font-size: 1.4em; margin-top: 4em; }\n")
	w("section.copyright { page: blank; padding-top: 70%%; text-align: center; font-size: .8em; }\n")
	w("section.copyright p, section.dedication p, section.epigraph p { text-indent: 0; text-align: center; }\n")
	w("section.dedication, section.epigraph { page: blank; padding-top: 28%%; font-style: italic; }\n")
	w("section.bodystart { counter-reset: page 1; }\n")
	w("p.chlabel { text-indent: 0; text-align: center; margin-top: %s; font-size: .82em; font-weight: bold; letter-spacing: .12em; text-transform: uppercase; }\n", cssLen(pr.sink))
	w("h2.chtitle { text-align: center; font-weight: normal; font-size: 1.75em; line-height: 1.2; margin: .3em 0 3em; }\n")
	w("p.orn { text-indent: 0; text-align: center; margin: -1.5em 0 2.5em; }\n")
	w("h1.parttitle { text-align: center; font-size: 1.9em; margin: %s 0 3em; }\n", cssLen(pr.sink*6/10))
	w("section.part h1.parttitle { margin-top: %s; font-size: 2em; }\n", cssLen(pr.textH/4))
	w("h1, h2, h3, h4, h5, h6 { break-after: avoid; page-break-after: avoid; }\n")
	w("h3 { font-size: 1.2em; margin: 1.5em 0 .5em; } h4 { font-size: 1.08em; margin: 1.5em 0 .5em; } h5, h6 { font-size: 1em; font-style: italic; margin: 1.5em 0 .5em; }\n")
	w("p.scenebreak { text-indent: 0; text-align: center; margin: 1em 0; break-before: avoid; break-after: avoid; }\n")
	w("p.theend { text-indent: 0; text-align: center; margin: 3em 0 0; font-weight: bold; }\n")
	if Set(o.DropCap) {
		w("section.chapter > p.first::first-letter, section.chapter > p.orn + p::first-letter, section.chapter > p.chlabel + h2 + p::first-letter, section.chapter > h2.chtitle + p::first-letter { initial-letter: 3; font-weight: normal; margin-right: .1em; }\n")
	}
	w("blockquote { margin: .5em 2em; } blockquote p { text-indent: 0; }\n")
	w("ul, ol { margin: .5em 0; padding-left: 1.7em; }\n")
	w("pre { font-family: 'Liberation Mono', 'Courier New', monospace; font-size: .85em; white-space: pre-wrap; margin: .5em 0 .5em 1em; break-inside: avoid-page; }\n")
	w("code { font-family: 'Liberation Mono', 'Courier New', monospace; font-size: .92em; }\n")
	w("table { border-collapse: collapse; margin: .5em 0; width: 100%%; font-size: .9em; break-inside: auto; }\n")
	w("thead { display: table-header-group; } tr { break-inside: avoid; }\n")
	w("th, td { border: 0; border-bottom: .25pt solid #b3b3b3; padding: .25em .4em; vertical-align: top; text-align: left; }\n")
	w("thead th { border-top: .75pt solid #000; border-bottom: .5pt solid #000; } tbody tr:last-child td { border-bottom: .75pt solid #000; }\n")
	w(".ar { text-align: right; } .ac { text-align: center; }\n")
	w("img { display: block; max-width: 100%%; max-height: %s; margin: 1em auto; break-inside: avoid; }\n", cssLen(pr.textH*75/100))
	w(".omitted { font-style: italic; }\n")
	w(".footnotes { margin-top: 2em; padding-top: .6em; border-top: .5pt solid #000; font-size: .85em; }\n")
	w(".footnote { margin: 0 0 .4em; } .footnote p { text-indent: 0; }\n")
	w("sup a { text-decoration: none; } a { color: inherit; }\n")
	w("nav.toc h1 { text-align: center; font-size: 1.6em; margin: %s 0 1.5em; }\n", cssLen(pr.sink/2))
	w("nav.toc ul { list-style: none; margin: 0; padding: 0; } nav.toc li { margin: 0; } nav.toc li.l1 { margin-left: 1.5em; } nav.toc li.l0 { font-weight: bold; margin-top: .5em; } nav.toc li.l1 { font-weight: normal; margin-top: 0; }\n")
	w("nav.toc a { text-decoration: none; }\n")
	w("nav.toc a::after { content: leader('.') target-counter(attr(href url), page); }\n")
	return s.String()
}
