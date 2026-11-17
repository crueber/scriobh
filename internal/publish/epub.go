package publish

import (
	"fmt"
	"strings"
)

// WriteEPUB writes the book as an EPUB 3 package: the mimetype entry first and
// stored, a container, a package document with a stable identifier and a fixed
// modified date, a navigation document (and an NCX for older readers), one
// XHTML file per section, one stylesheet that sets no colour and names only
// generic font families, and the cover as an SVG image. Nothing in it loads
// anything from outside the package.
func WriteEPUB(b *Book, o Options) ([]byte, error) {
	lang := firstNonEmpty(o.Language, b.Language, "en")
	if !validLang(lang) {
		lang = "en"
	}
	dir := "ltr"
	if IsRTL(lang) {
		dir = "rtl"
	}
	w := &epubWriter{b: b, o: o, lang: lang, dir: dir}
	return w.build()
}

type epubItem struct {
	id, href, mediaType, props string
	data                       []byte
	linear                     bool
	inSpine                    bool
}

type epubWriter struct {
	b    *Book
	o    Options
	lang string
	dir  string

	items  []epubItem
	images map[string]string // key -> href relative to OEBPS
	media  []epubItem
	nav    []navEntry
}

type navEntry struct {
	label string
	href  string
	level int
}

func (w *epubWriter) xhtml(title, bodyAttr, body string) []byte {
	var s strings.Builder
	s.WriteString(xmlDecl)
	fmt.Fprintf(&s, `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="%s" xml:lang="%s" dir="%s">`+"\n", xmlEscape(w.lang), xmlEscape(w.lang), w.dir)
	s.WriteString(`<head>` + "\n" + `<meta charset="utf-8"/>` + "\n" + `<title>` + xmlEscape(title) + `</title>` + "\n")
	s.WriteString(`<link rel="stylesheet" type="text/css" href="../css/book.css"/>` + "\n</head>\n")
	s.WriteString("<body" + bodyAttr + ">\n" + body + "</body>\n</html>\n")
	return []byte(s.String())
}

func (w *epubWriter) imageSrc(key string) (string, bool) {
	if href, ok := w.images[key]; ok {
		return "../" + href, true
	}
	img, ok := w.b.Images[key]
	if !ok || len(img.Data) == 0 {
		return "", false
	}
	switch img.Ext {
	case "png", "jpg", "gif", "webp":
	default:
		return "", false
	}
	hash := img.Hash
	if len(hash) < 12 {
		hash = fmt.Sprintf("%012x", len(img.Data))
	}
	href := "images/img-" + hash[:12] + "." + img.Ext
	if w.images == nil {
		w.images = map[string]string{}
	}
	for _, m := range w.media { // the same bytes under another key
		if m.href == href {
			w.images[key] = href
			return "../" + href, true
		}
	}
	w.images[key] = href
	w.media = append(w.media, epubItem{id: "img-" + hash[:12], href: href, mediaType: img.Mime, data: img.Data})
	return "../" + href, true
}

// hasCover says whether the book has a cover page and a cover-image item: the option is on and there is a cover to put in.
func (w *epubWriter) hasCover() bool {
	return On(w.o.Cover) && (len(w.b.CoverImage) > 0 || len(w.b.CoverSVG) > 0)
}

func (w *epubWriter) build() ([]byte, error) {
	b, o := w.b, w.o
	title := CleanLine(b.Title)
	if title == "" {
		title = "Untitled"
	}
	if w.hasCover() {
		// The project's own cover picture (a JPEG made for screens) when it has one, else the generated SVG. The cover
		// page is one image with its alt text and no background colour; the picture is also the cover-image item the
		// shops and readers use, and the EPUB 2 cover meta points at it.
		href, mt, data, alt := "images/cover.svg", "image/svg+xml", b.CoverSVG, "Cover: "+title
		if len(b.CoverImage) > 0 {
			href, mt, data = "images/cover.jpg", "image/jpeg", b.CoverImage
			if a := CleanLine(b.CoverAlt); a != "" {
				alt = a
			}
		}
		w.items = append(w.items,
			epubItem{id: "cover", href: "text/cover.xhtml", mediaType: "application/xhtml+xml", inSpine: true, linear: true,
				data: w.xhtml(title, ` epub:type="cover"`, `<section class="cover" epub:type="cover"><img src="../`+href+`" alt="`+xmlEscape(alt)+`"/></section>`+"\n")})
		w.media = append(w.media, epubItem{id: "cover-image", href: href, mediaType: mt, props: "cover-image", data: data})
	}
	if On(o.TitlePage) {
		var t strings.Builder
		t.WriteString(`<section class="titlepage" epub:type="titlepage">` + "\n")
		t.WriteString(`<h1 class="title">` + xmlEscape(title) + "</h1>\n")
		if s := CleanLine(b.Subtitle); s != "" {
			t.WriteString(`<p class="subtitle">` + xmlEscape(s) + "</p>\n")
		}
		if a := CleanLine(b.Byline); a != "" {
			t.WriteString(`<p class="byline">` + xmlEscape(a) + "</p>\n")
		}
		t.WriteString("</section>\n")
		w.items = append(w.items, epubItem{id: "titlepage", href: "text/titlepage.xhtml", mediaType: "application/xhtml+xml", inSpine: true, linear: true,
			data: w.xhtml(title, ` epub:type="frontmatter"`, t.String())})
	}
	for i, s := range b.Sections {
		w.section(i, s)
	}

	css := w.css()
	manifest := []epubItem{{id: "nav", href: "nav.xhtml", mediaType: "application/xhtml+xml", props: "nav", data: w.navXHTML(title)},
		{id: "ncx", href: "toc.ncx", mediaType: "application/x-dtbncx+xml", data: w.ncx(title)},
		{id: "css", href: "css/book.css", mediaType: "text/css", data: []byte(css)}}
	manifest = append(manifest, w.items...)
	manifest = append(manifest, w.media...)

	entries := []zipEntry{
		{name: "mimetype", data: []byte("application/epub+zip"), store: true},
		{name: "META-INF/container.xml", data: []byte(xmlDecl + `<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>` + "\n")},
		{name: "OEBPS/content.opf", data: []byte(w.opf(title, manifest))},
	}
	for _, it := range manifest {
		entries = append(entries, zipEntry{name: "OEBPS/" + it.href, data: it.data, store: strings.HasPrefix(it.mediaType, "image/") && it.mediaType != "image/svg+xml"})
	}
	return writeZip(entries, b.Modified)
}

func (w *epubWriter) section(i int, s Section) {
	o := w.o
	level := s.Level
	if level < 1 {
		level = 1
	}
	heading := HeadingText(s, o)
	x := &xhtmlWriter{b: w.b, o: o, imageSrc: w.imageSrc}
	file := fmt.Sprintf("text/s%03d.xhtml", i+1)
	cls := map[SectionKind]string{SecFront: "frontmatter", SecPart: "part", SecChapter: "chapter", SecBack: "backmatter", SecNotes: "backmatter"}[s.Kind]
	epubType := map[SectionKind]string{SecFront: "frontmatter", SecPart: "part", SecChapter: "chapter", SecBack: "backmatter", SecNotes: "backmatter"}[s.Kind]
	// The raw title goes on the section as data-title, because the heading may say "Chapter 3: Title" and a
	// book read back should get the title the writer wrote, not the heading. Only when the title is printed:
	// in the heading, or in the contents when the heading is empty (the contents label falls back to the title); never for a
	// numbered chapter under chapter_heading: number.
	// A chapter whose title the writer chose not to print ("Chapter 3" only) must not have it written into the file.
	titleAttr := ""
	numberOnly := s.Kind == SecChapter && s.Number > 0 && o.ChapterHeading == "number" // the writer chose to print "Chapter 3" and not the title
	if t := CleanLine(s.Title); t != "" && !numberOnly {
		titleAttr = ` data-title="` + xmlEscape(t) + `"`
	}
	x.out.WriteString(fmt.Sprintf(`<section class="%s" epub:type="%s"%s>`+"\n", cls, epubType, titleAttr))
	if heading != "" {
		hl := 1
		if s.Kind == SecChapter && level >= 2 {
			hl = 2
		}
		x.out.WriteString(fmt.Sprintf("<h%d>%s</h%d>\n", hl, xmlEscape(heading), hl))
	}
	bodyLevel := level
	if heading == "" {
		bodyLevel = level - 1
	}
	x.blocks(s.Nodes, bodyLevel)
	used := map[int]bool{}
	noteRefs(s.Nodes, used)
	x.notes(s.Notes, used)
	x.out.WriteString("</section>\n")
	label := heading
	if label == "" {
		label = CleanLine(s.Title)
	}
	if label == "" && s.Number > 0 {
		label = firstNonEmpty(o.ChapterLabel, "Chapter") + " " + itoa(s.Number)
	}
	if label == "" {
		label = "Section " + itoa(i+1)
	}
	w.nav = append(w.nav, navEntry{label: label, href: file, level: level})
	pageTitle := firstNonEmpty(label, CleanLine(w.b.Title))
	w.items = append(w.items, epubItem{id: fmt.Sprintf("s%03d", i+1), href: file, mediaType: "application/xhtml+xml", inSpine: true, linear: true,
		data: w.xhtml(pageTitle, "", x.out.String())})
	if Set(o.TheEnd) && i == len(w.b.Sections)-1 {
		// "THE END" closes the last section's file.
		last := &w.items[len(w.items)-1]
		last.data = []byte(strings.Replace(string(last.data), "</section>\n</body>", `<p class="theend">THE END</p>`+"\n</section>\n</body>", 1))
	}
}

func (w *epubWriter) css() string {
	align := "left"
	if Set(w.o.Justify) {
		align = "justify"
	}
	// A paragraph's indent is set in inches for a page; a reflowing book wants em,
	// and a quarter inch at 12 point is one and a half em.
	ind := 1.5
	if w.o.IndentInches != nil {
		ind = *w.o.IndentInches * 6
	}
	return fmt.Sprintf(`html{-webkit-text-size-adjust:100%%}
body{font-family:serif;line-height:1.5;margin:0 5%%}
h1,h2,h3,h4,h5,h6{font-family:serif;line-height:1.25;font-weight:bold;text-align:left;margin:1.6em 0 .6em;page-break-after:avoid;break-after:avoid}
h1{font-size:1.6em;text-align:center;margin:3em 0 1.5em;page-break-before:always;break-before:page}
h2{font-size:1.3em}h3{font-size:1.15em}
section.chapter>h2:first-child,section.part>h1:first-child{text-align:center}
p{margin:0;text-indent:%.2fem;text-align:%s;orphans:2;widows:2}
p.first,h1+p,h2+p,h3+p,h4+p,h5+p,h6+p,blockquote p,li p,td p,aside p{text-indent:0}
p.scenebreak{text-indent:0;text-align:center;margin:1.5em 0}
p.theend{text-indent:0;text-align:center;margin:3em 0 0;font-weight:bold}
blockquote{margin:1em 2em}
ul,ol{margin:1em 0;padding-left:2em}
pre{font-family:monospace;font-size:.9em;white-space:pre-wrap;margin:1em 0}
code{font-family:monospace;font-size:.95em}
table{border-collapse:collapse;margin:1em 0;max-width:100%%}
th,td{border:1px solid currentColor;padding:.25em .5em;vertical-align:top;text-align:left}
th{font-weight:bold}.ar{text-align:right}.ac{text-align:center}
img{max-width:100%%;height:auto}
.omitted{font-style:italic}
.cover{text-align:center;margin:0;padding:0}.cover img{max-height:100%%;max-width:100%%}
.titlepage{text-align:center;margin-top:25%%}.titlepage h1{page-break-before:avoid;break-before:avoid;margin:0 0 .5em}
.titlepage p{text-indent:0;text-align:center}.subtitle{font-style:italic}.byline{margin-top:2em}
.footnotes{margin-top:2.5em;padding-top:.8em;border-top:1px solid currentColor;font-size:.85em}
.footnotes aside{margin:0 0 .5em}
sup a{text-decoration:none}
a{color:inherit}
`, ind, align)
}

func (w *epubWriter) navXHTML(title string) []byte {
	var s strings.Builder
	s.WriteString(`<nav epub:type="toc" id="toc">` + "\n<h1>Contents</h1>\n")
	w.navList(&s, "text/", func(e navEntry) string { return e.href }, "")
	s.WriteString("</nav>\n")
	if len(w.nav) > 0 {
		s.WriteString(`<nav epub:type="landmarks" id="landmarks" hidden="hidden">` + "\n<h2>Guide</h2>\n<ol>\n")
		if w.hasCover() {
			s.WriteString(`<li><a epub:type="cover" href="text/cover.xhtml">Cover</a></li>` + "\n")
		}
		s.WriteString(`<li><a epub:type="bodymatter" href="` + w.nav[0].href + `">Start of the text</a></li>` + "\n</ol>\n</nav>\n")
	}
	data := w.xhtmlNav(title, s.String())
	return data
}

// xhtmlNav is the navigation document: its stylesheet link sits one level up.
func (w *epubWriter) xhtmlNav(title, body string) []byte {
	var s strings.Builder
	s.WriteString(xmlDecl)
	fmt.Fprintf(&s, `<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops" lang="%s" xml:lang="%s" dir="%s">`+"\n", xmlEscape(w.lang), xmlEscape(w.lang), w.dir)
	s.WriteString(`<head>` + "\n" + `<meta charset="utf-8"/>` + "\n<title>" + xmlEscape(title) + `</title>` + "\n" + `<link rel="stylesheet" type="text/css" href="css/book.css"/>` + "\n</head>\n<body>\n")
	s.WriteString(body + "</body>\n</html>\n")
	return []byte(s.String())
}

// navList writes the contents list: parts hold the chapters that follow them.
func (w *epubWriter) navList(s *strings.Builder, _ string, href func(navEntry) string, _ string) {
	s.WriteString("<ol>\n")
	open := false // a level-1 item is open and may hold a nested list
	nested := false
	for _, e := range w.nav {
		if e.level >= 2 && open {
			if !nested {
				s.WriteString("\n<ol>\n")
				nested = true
			}
			s.WriteString(`<li><a href="` + xmlEscape(href(e)) + `">` + xmlEscape(e.label) + "</a></li>\n")
			continue
		}
		if open {
			if nested {
				s.WriteString("</ol>\n")
				nested = false
			}
			s.WriteString("</li>\n")
		}
		s.WriteString(`<li><a href="` + xmlEscape(href(e)) + `">` + xmlEscape(e.label) + "</a>")
		open = true
	}
	if open {
		if nested {
			s.WriteString("</ol>\n")
		}
		s.WriteString("</li>\n")
	}
	s.WriteString("</ol>\n")
}

func (w *epubWriter) ncx(title string) []byte {
	var s strings.Builder
	s.WriteString(xmlDecl)
	s.WriteString(`<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/" version="2005-1" xml:lang="` + xmlEscape(w.lang) + `"><head>`)
	s.WriteString(`<meta name="dtb:uid" content="` + xmlEscape(w.identifier()) + `"/><meta name="dtb:depth" content="2"/><meta name="dtb:totalPageCount" content="0"/><meta name="dtb:maxPageNumber" content="0"/></head>`)
	s.WriteString(`<docTitle><text>` + xmlEscape(title) + `</text></docTitle><navMap>`)
	play := 0
	open := false
	for _, e := range w.nav {
		play++
		pt := fmt.Sprintf(`<navPoint id="np%d" playOrder="%d"><navLabel><text>%s</text></navLabel><content src="%s"/>`, play, play, xmlEscape(e.label), xmlEscape(e.href))
		if e.level >= 2 && open {
			s.WriteString(pt + `</navPoint>`)
			continue
		}
		if open {
			s.WriteString(`</navPoint>`)
		}
		s.WriteString(pt)
		open = true
	}
	if open {
		s.WriteString(`</navPoint>`)
	}
	s.WriteString(`</navMap></ncx>` + "\n")
	return []byte(s.String())
}

func (w *epubWriter) identifier() string {
	id := strings.TrimSpace(w.b.Identifier)
	if id == "" {
		id = "00000000-0000-5000-8000-000000000000"
	}
	return "urn:uuid:" + id
}

func (w *epubWriter) opf(title string, manifest []epubItem) string {
	var s strings.Builder
	s.WriteString(xmlDecl)
	fmt.Fprintf(&s, `<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub-id" xml:lang="%s" dir="%s">`+"\n", xmlEscape(w.lang), w.dir)
	s.WriteString(`<metadata xmlns:dc="http://purl.org/dc/elements/1.1/">` + "\n")
	s.WriteString(`<dc:identifier id="pub-id">` + xmlEscape(w.identifier()) + "</dc:identifier>\n")
	if sub := CleanLine(w.b.Subtitle); sub != "" {
		s.WriteString(`<dc:title id="title-main">` + xmlEscape(title) + "</dc:title>\n")
		s.WriteString(`<meta refines="#title-main" property="title-type">main</meta>` + "\n")
		s.WriteString(`<dc:title id="title-sub">` + xmlEscape(sub) + "</dc:title>\n")
		s.WriteString(`<meta refines="#title-sub" property="title-type">subtitle</meta>` + "\n")
	} else {
		s.WriteString("<dc:title>" + xmlEscape(title) + "</dc:title>\n")
	}
	if a := CleanLine(w.b.Byline); a != "" {
		s.WriteString(`<dc:creator id="creator">` + xmlEscape(a) + "</dc:creator>\n")
	}
	s.WriteString("<dc:language>" + xmlEscape(w.lang) + "</dc:language>\n")
	s.WriteString(`<meta name="generator" content="Scriobh EPUB 1"/>` + "\n")
	s.WriteString(`<meta property="dcterms:modified">` + w.b.Modified.UTC().Format("2006-01-02T15:04:05Z") + "</meta>\n")
	if w.hasCover() {
		s.WriteString(`<meta name="cover" content="cover-image"/>` + "\n")
	}
	s.WriteString("</metadata>\n<manifest>\n")
	for _, it := range manifest {
		s.WriteString(`<item id="` + xmlEscape(it.id) + `" href="` + xmlEscape(it.href) + `" media-type="` + xmlEscape(it.mediaType) + `"`)
		if it.props != "" {
			s.WriteString(` properties="` + it.props + `"`)
		}
		s.WriteString("/>\n")
	}
	s.WriteString("</manifest>\n")
	s.WriteString(`<spine toc="ncx"`)
	if w.dir == "rtl" {
		s.WriteString(` page-progression-direction="rtl"`)
	}
	s.WriteString(">\n")
	for _, it := range manifest {
		if it.inSpine {
			s.WriteString(`<itemref idref="` + xmlEscape(it.id) + `"/>` + "\n")
		}
	}
	s.WriteString("</spine>\n</package>\n")
	return s.String()
}
