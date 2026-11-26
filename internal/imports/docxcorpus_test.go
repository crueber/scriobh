package imports_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"math/rand"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// The corpus: one small .docx for each way writers mark chapters. It is a port of
// internal/imports/testdata/docxcorpus/mk.py (the samples) and big.py (the long books), as builders, so
// `go test` needs no Python. Each sample carries what the plan (docs/plans/DOCX-IMPORT-CHAPTERS.md,
// section 8) says `auto` must make of it.

const corpusW = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`

func corpusStyle(id, name, based string, outline int, extraP string) string {
	b, o := "", ""
	if based != "" {
		b = `<w:basedOn w:val="` + based + `"/>`
	}
	if outline >= 0 {
		o = fmt.Sprintf(`<w:outlineLvl w:val="%d"/>`, outline)
	}
	return `<w:style w:type="paragraph" w:styleId="` + id + `"><w:name w:val="` + name + `"/>` + b + `<w:qFormat/><w:pPr>` + extraP + o + `</w:pPr><w:rPr></w:rPr></w:style>`
}

var corpusStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles ` + corpusW + `><w:docDefaults><w:rPrDefault><w:rPr><w:lang w:val="en-US"/></w:rPr></w:rPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/><w:qFormat/></w:style>` +
	corpusStyle("Title", "Title", "Normal", -1, "") + corpusStyle("Subtitle", "Subtitle", "Normal", -1, "") +
	corpusStyle("Heading1", "heading 1", "Normal", 0, "") + corpusStyle("Heading2", "heading 2", "Normal", 1, "") + corpusStyle("Heading3", "heading 3", "Normal", 2, "") +
	corpusStyle("ChapterTitle", "Chapter Title", "Normal", -1, "") + corpusStyle("ChapterHeading", "Chapter Heading", "Heading1", -1, "") +
	corpusStyle("PartTitle", "Part Title", "Normal", -1, "") + corpusStyle("SceneBreak", "Scene Break", "Normal", -1, "") +
	corpusStyle("H1PB", "Chapter PB", "Normal", -1, `<w:pageBreakBefore/>`) + corpusStyle("Dedication", "Dedication", "Normal", -1, "") +
	corpusStyle("TOCHeading", "TOC Heading", "Normal", -1, "") +
	corpusStyle("ChapterNumber", "Chapter Number", "Normal", -1, "") + corpusStyle("ChapterSubtitle", "Chapter Subtitle", "Normal", -1, "") +
	corpusStyle("TableTitle", "Table Title", "Normal", -1, "") + `</w:styles>`

const corpusNumbering = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering ` + corpusW + `><w:abstractNum w:abstractNumId="0"><w:multiLevelType w:val="multilevel"/><w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:lvlText w:val="Chapter %1"/><w:lvlJc w:val="left"/></w:lvl></w:abstractNum><w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num></w:numbering>`

const corpusCore = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Sample Novel</dc:title><dc:creator>A. Writer</dc:creator></cp:coreProperties>`

const corpusCT = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/></Types>`

// cp is one paragraph of a corpus sample.
type cp struct {
	text, style                  string
	b, center, pbb, pagebr, sect bool
	num, caps, small             bool
	sb, outline, size            int
	runs                         []string
	hasOutline                   bool
	toc                          bool
}

func (p cp) xml() string {
	pp := ""
	if p.style != "" {
		pp += `<w:pStyle w:val="` + p.style + `"/>`
	}
	if p.pbb {
		pp += `<w:pageBreakBefore/>`
	}
	if p.num {
		pp += `<w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr>`
	}
	if p.sb > 0 {
		pp += fmt.Sprintf(`<w:spacing w:before="%d"/>`, p.sb)
	}
	if p.center {
		pp += `<w:jc w:val="center"/>`
	}
	if p.hasOutline {
		pp += fmt.Sprintf(`<w:outlineLvl w:val="%d"/>`, p.outline)
	}
	if p.sect {
		pp += `<w:sectPr><w:type w:val="nextPage"/><w:pgSz w:w="12240" w:h="15840"/></w:sectPr>`
	}
	if pp != "" {
		pp = `<w:pPr>` + pp + `</w:pPr>`
	}
	body := strings.Join(p.runs, "")
	if p.runs == nil && p.text != "" {
		rp := ""
		if p.b {
			rp += `<w:b/>`
		}
		if p.caps {
			rp += `<w:caps/>`
		}
		if p.size > 0 {
			rp += fmt.Sprintf(`<w:sz w:val="%d"/>`, p.size)
		}
		if rp != "" {
			rp = `<w:rPr>` + rp + `</w:rPr>`
		}
		body = `<w:r>` + rp + `<w:t xml:space="preserve">` + html.EscapeString(p.text) + `</w:t></w:r>`
	}
	if p.pagebr {
		body = `<w:r><w:br w:type="page"/></w:r>` + body
	}
	return `<w:p>` + pp + body + `</w:p>`
}

// The paragraph makers, as mk.py has them.
func cP(text string) cp               { return cp{text: text} }
func cPs(text, style string) cp       { return cp{text: text, style: style} }
func cPb(text string) cp              { return cp{text: text, b: true} }
func cPbc(text string) cp             { return cp{text: text, b: true, center: true} }
func cPc(text string) cp              { return cp{text: text, center: true} }
func cPbreak() cp                     { return cp{pagebr: true} }
func cPout(text string, level int) cp { return cp{text: text, hasOutline: true, outline: level} }
func cPh1(text string) cp             { return cPs(text, "Heading1") }
func cPh2(text string) cp             { return cPs(text, "Heading2") }

var corpusLorem = []string{
	"The road ran north out of the village and nobody she knew had ever followed it past the mill.",
	"He said nothing for a long while. The fire had burned down to a line of orange along the bars.",
	"By morning the snow had covered the tracks, and with them the last argument for turning back.",
}

func corpusBody(n int) []cp {
	var out []cp
	for i := 0; i < n; i++ {
		out = append(out, cP(corpusLorem[i%3]))
	}
	return out
}

func corpusCat(parts ...[]cp) []cp {
	var out []cp
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func one(p ...cp) []cp { return p }

// corpusDoc zips paragraphs into a .docx.
func corpusDoc(paras []cp) []byte {
	var sb strings.Builder
	for _, p := range paras {
		sb.WriteString(p.xml())
	}
	d := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document ` + corpusW + `><w:body>` + sb.String() + `<w:sectPr><w:pgSz w:w="12240" w:h="15840"/></w:sectPr></w:body></w:document>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ n, b string }{
		{"[Content_Types].xml", corpusCT},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`},
		{"word/_rels/document.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/numbering" Target="numbering.xml"/></Relationships>`},
		{"word/document.xml", d}, {"word/styles.xml", corpusStyles}, {"word/numbering.xml", corpusNumbering}, {"docProps/core.xml", corpusCore},
	} {
		w, _ := zw.Create(f.n)
		_, _ = w.Write([]byte(f.b))
	}
	_ = zw.Close()
	return buf.Bytes()
}

// corpusSample is one sample with what the plan says `auto` makes of it. out is the outline of the proposed documents
// ("kind:title", two spaces for each level of nesting). left names what the import leaves out on its own. weak is how many
// weak candidates (page breaks, section breaks, lone numerals, unformatted labels) are listed as suggestions.
type corpusSample struct {
	name  string
	paras []cp
	out   []string
	left  []string
	weak  int
	note  string // where the expectation is not the plan's table, and why
}

func corpusSamples() []corpusSample {
	chap := func(n ...string) []string {
		var o []string
		for _, t := range n {
			o = append(o, "chapter:"+t)
		}
		return o
	}
	threeH1 := func(labels ...string) []cp {
		var out []cp
		for _, l := range labels {
			out = append(out, cPh1(l))
			out = append(out, corpusBody(2)...)
		}
		return out
	}
	scenes := func() []cp {
		return corpusCat(corpusBody(2), one(cP("***")), corpusBody(1), one(cPc("#")), corpusBody(1), one(cPc("* * *")), corpusBody(1))
	}
	var s01 []cp
	s01 = append(s01, cPs("Sample Novel", "Title"), cPs("A. Writer", "Subtitle"))
	for k := 1; k <= 3; k++ {
		s01 = append(s01, cPh1(fmt.Sprintf("Chapter %d", k)))
		s01 = append(s01, scenes()...)
	}
	var s02 []cp
	s02 = append(s02, cPh1("Part One"))
	for _, k := range []int{1, 2} {
		s02 = append(s02, cPh2(fmt.Sprintf("Chapter %d", k)))
		s02 = append(s02, corpusBody(2)...)
	}
	s02 = append(s02, cPh1("Part Two"))
	for _, k := range []int{3, 4} {
		s02 = append(s02, cPh2(fmt.Sprintf("Chapter %d", k)))
		s02 = append(s02, corpusBody(2)...)
	}
	s03 := one(cPbc("Sample Novel"))
	for _, w := range []string{"One", "Two", "Three"} {
		s03 = append(s03, cPbc("Chapter "+w))
		s03 = append(s03, corpusBody(3)...)
	}
	var s04 []cp
	for _, wt := range [][2]string{{"ONE", "THE ROAD"}, {"TWO", "THE MILL"}, {"THREE", "SNOW"}} {
		s04 = append(s04, cPc("CHAPTER "+wt[0]), cPc(wt[1]))
		s04 = append(s04, corpusBody(3)...)
	}
	var s05 []cp
	for _, r := range []string{"I", "II", "III", "IV"} {
		s05 = append(s05, cPbc(r))
		s05 = append(s05, corpusBody(3)...)
	}
	s06 := corpusCat(one(cP("Chapter 1")), corpusBody(3))
	for _, k := range []int{2, 3} {
		s06 = append(s06, cPbreak(), cP(fmt.Sprintf("Chapter %d", k)))
		s06 = append(s06, corpusBody(3)...)
	}
	s06b := corpusBody(3)
	for k := 0; k < 2; k++ {
		s06b = append(s06b, cPbreak())
		s06b = append(s06b, corpusBody(3)...)
	}
	var s07 []cp
	for _, t := range []string{"The Road", "The Mill", "Snow"} {
		s07 = append(s07, cp{text: t, pbb: true, b: true})
		s07 = append(s07, corpusBody(3)...)
	}
	s07b := corpusCat(one(cPs("The Road", "H1PB")), corpusBody(3), one(cPs("The Mill", "H1PB")), corpusBody(3))
	last := func(i int) cp { return cp{text: corpusLorem[i], sect: true} }
	s08 := corpusCat(corpusBody(2), one(last(0)), corpusBody(2), one(last(1)), corpusBody(3))
	s08b := corpusCat(one(cPb("Chapter 1")), corpusBody(2), one(last(0), cPb("Chapter 2")), corpusBody(2), one(last(1), cPb("Chapter 3")), corpusBody(2))
	var s09, s09b, s09c []cp
	for k := 1; k <= 3; k++ {
		s09 = append(s09, cPs(fmt.Sprintf("The Chapter %d", k), "ChapterTitle"))
		s09 = append(s09, corpusBody(2)...)
		s09b = append(s09b, cPs(fmt.Sprintf("The Chapter %d", k), "ChapterHeading"))
		s09b = append(s09b, corpusBody(2)...)
		s09c = append(s09c, cPout(fmt.Sprintf("Some Title %d", k), 0))
		s09c = append(s09c, corpusBody(2)...)
	}
	var s10 []cp
	for _, t := range []string{"The Road", "The Mill", "Snow"} {
		s10 = append(s10, cp{text: t, style: "Heading1", num: true})
		s10 = append(s10, corpusBody(2)...)
	}
	s11 := corpusCat(one(cPs("Sample Novel", "Title"), cPs("A. Writer", "Subtitle"), cp{text: "Copyright 2026 A. Writer. All rights reserved.", pbb: true}, cP("ISBN 000-0-00-000000-0"),
		cp{text: "For my mother", style: "Dedication", pbb: true, center: true}, cp{text: "Contents", style: "TOCHeading", pbb: true}, cP("Chapter 1 ........ 5"), cP("Chapter 2 ........ 19")),
		threeH1("Chapter 1", "Chapter 2"), one(cPh1("Acknowledgments")), corpusBody(1), one(cPh1("About the Author")), corpusBody(1))
	var s12 []cp
	for i := 0; i < 40; i++ {
		s12 = append(s12, cP(fmt.Sprintf("%s (%d)", corpusLorem[i%3], i)))
	}
	s13 := corpusCat(one(cPh1("Chapter 1")), corpusBody(1), one(cP("***")), corpusBody(1), one(cPc("#")), corpusBody(1), one(cPc("* * *")), corpusBody(1), one(cPc("—")), corpusBody(1), one(cP(""), cP("")), corpusBody(1))
	s14 := corpusCat(one(cPh1("Chapter 1")), corpusBody(2), one(cPbc("Chapter 2")), corpusBody(2), one(cPh1("Chapter 3")), corpusBody(2), one(cP("CHAPTER 4")), corpusBody(2))
	s15 := corpusCat(one(cPh1("Chapter 1"), cP("She read the line twice. Chapter 3 of the report was missing."), cP("Chapter 3 of the report was missing, he said."), cP("I"), cP("\"I,\" she said."), cP("1."), cPc("The End"), cPh1("Chapter 2")), corpusBody(2))
	s16 := one(cPh1("Sample Novel"))
	for k := 1; k <= 3; k++ {
		s16 = append(s16, cPh2(fmt.Sprintf("Chapter %d", k)))
		s16 = append(s16, corpusBody(2)...)
	}
	var s17 []cp
	for _, w := range []string{"One", "Two", "Three"} {
		s17 = append(s17, cp{text: "Chapter " + w, pbb: true, sb: 4000, center: true})
		s17 = append(s17, corpusBody(3)...)
	}
	// 18: a prologue and an epilogue are chapters (D3)
	s18 := corpusCat(one(cPh1("Prologue")), corpusBody(2), threeH1("Chapter 1", "Chapter 2"), one(cPh1("Epilogue")), corpusBody(2))
	// 19: a contents page typed by hand, with dot leaders, ahead of real chapters
	s19 := corpusCat(one(cPc("Contents"), cP("Chapter 1 ........ 3"), cP("Chapter 2 ........ 9"), cP("Chapter 3 ........ 14")), threeH1("Chapter 1", "Chapter 2", "Chapter 3"))
	// 20: the title page repeats the book's title
	s20 := corpusCat(one(cPbc("SAMPLE NOVEL"), cPc("by A. Writer")), []cp{cp{text: "Chapter One", b: true, center: true, pbb: true}}, corpusBody(2), []cp{cp{text: "Chapter Two", b: true, center: true, pbb: true}}, corpusBody(2))

	// 21: caption-prefixed headings: the chapter line ends the heading, and its numeral is not its place (II, III, IV at 1, 2, 3)
	s21 := corpusCat(one(cPs("Sample Novel", "Title")),
		one(cPh1("I hope Mr. Bingley will like it. CHAPTER II.")), corpusBody(2),
		one(cPh1("Mr. Bingley is a pleasant young man. CHAPTER III.")), corpusBody(2),
		one(cPh1("Mrs. Bennet has her own opinions. CHAPTER IV.")), corpusBody(2))

	noneFound := func(title string) []string { return []string{"chapter:" + title} }
	return []corpusSample{
		{name: "01-h1-chapters", paras: s01, out: chap("Chapter 1", "Chapter 2", "Chapter 3")},
		{name: "02-parts-h1-chapters-h2", paras: s02, out: []string{"part:Part One", "  chapter:Chapter 1", "  chapter:Chapter 2", "part:Part Two", "  chapter:Chapter 3", "  chapter:Chapter 4"}},
		{name: "03-plain-bold-centred", paras: s03, out: chap("Chapter One", "Chapter Two", "Chapter Three")},
		{name: "04-caps-two-line", paras: s04, out: chap("THE ROAD", "THE MILL", "SNOW"), note: "the plan's table says titled The Road; the owner kept capitals (D5), so the title stays and 'The Road' is offered"},
		{name: "05-roman-numerals", paras: s05, out: noneFound("Sample Novel"), weak: 4},
		{name: "06-pagebreak-run", paras: s06, out: chap("Chapter 1", "Chapter 2", "Chapter 3")},
		{name: "06b-pagebreak-run-no-label", paras: s06b, out: noneFound("Sample Novel"), weak: 2},
		{name: "07-pagebreakbefore-para", paras: s07, out: noneFound("Sample Novel"), weak: 2},
		{name: "07b-pagebreakbefore-style", paras: s07b, out: chap("The Road", "The Mill"), note: "the style is named Chapter PB and used twice: strong rule 2, so 2 chapters, not the table's 1 document"},
		{name: "08-section-breaks", paras: s08, out: noneFound("Sample Novel"), weak: 2},
		{name: "08b-section-breaks-with-chapter-label", paras: s08b, out: chap("Chapter 1", "Chapter 2", "Chapter 3")},
		{name: "09-custom-style-no-outline", paras: s09, out: chap("The Chapter 1", "The Chapter 2", "The Chapter 3")},
		{name: "09b-custom-style-based-on-h1", paras: s09b, out: chap("The Chapter 1", "The Chapter 2", "The Chapter 3")},
		{name: "09c-direct-outline-level", paras: s09c, out: chap("Some Title 1", "Some Title 2", "Some Title 3")},
		{name: "10-auto-numbered-heading", paras: s10, out: chap("The Road", "The Mill", "Snow")},
		{name: "11-front-back-matter", paras: s11, out: []string{"front_matter:Copyright", "front_matter:Dedication", "chapter:Chapter 1", "chapter:Chapter 2", "back_matter:Acknowledgments", "back_matter:About the Author"}, left: []string{"Contents"}},
		{name: "12-no-breaks", paras: s12, out: noneFound("Sample Novel")},
		{name: "13-scene-markers", paras: s13, out: chap("Chapter 1")},
		{name: "14-mixed", paras: s14, out: chap("Chapter 1", "Chapter 2", "Chapter 3", "CHAPTER 4")},
		{name: "15-false-positives", paras: s15, out: chap("Chapter 1", "Chapter 2")},
		{name: "16-h1-title-h2-chapters", paras: s16, out: chap("Chapter 1", "Chapter 2", "Chapter 3")},
		{name: "17-shunn-like", paras: s17, out: chap("Chapter One", "Chapter Two", "Chapter Three")},
		{name: "18-prologue-epilogue", paras: s18, out: chap("Prologue", "Chapter 1", "Chapter 2", "Epilogue")},
		{name: "19-contents-dot-leaders", paras: s19, out: chap("Chapter 1", "Chapter 2", "Chapter 3"), left: []string{"Contents"}},
		{name: "20-title-page-repeated", paras: s20, out: chap("Chapter One", "Chapter Two")},
		{name: "21-caption-prefixed-headings", paras: s21, out: chap("I hope Mr. Bingley will like it. CHAPTER II.", "Mr. Bingley is a pleasant young man. CHAPTER III.", "Mrs. Bennet has her own opinions. CHAPTER IV.")},
		{name: "22-gutenberg-licence", paras: gutenbergParas(), out: []string{"front_matter:Front Matter", "chapter:The Gate", "chapter:The Well", "back_matter:Project Gutenberg licence"}},
	}
}

// corpusBig is big.py: a long book, 60 chapters, kind h1, plain (bold centred labels with a page break before) or none.
func corpusBig(kind string, chapters, wordsPerPara int) []cp {
	rng := rand.New(rand.NewSource(3))
	words := strings.Fields("the road ran north out of village and nobody she knew had ever followed it past mill said nothing for a long while fire burned down to line of orange along bars morning snow covered tracks last argument turning back")
	para := func(n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = words[rng.Intn(len(words))]
		}
		s := strings.Join(w, " ")
		return strings.ToUpper(s[:1]) + s[1:] + "."
	}
	var ps []cp
	for c := 1; c <= chapters; c++ {
		switch kind {
		case "h1":
			ps = append(ps, cPh1(fmt.Sprintf("Chapter %d", c)))
		case "plain":
			ps = append(ps, cp{text: fmt.Sprintf("Chapter %d", c), b: true, center: true, pbb: true})
		}
		for i := 0; i < 25; i++ {
			ps = append(ps, cP(para(wordsPerPara)))
			if i%8 == 7 {
				ps = append(ps, cPc("* * *"))
			}
		}
	}
	return ps
}

func TestCorpusBuildersMakeReadableFiles(t *testing.T) {
	for _, s := range corpusSamples() {
		if len(corpusDoc(s.paras)) == 0 {
			t.Fatalf("%s: empty", s.name)
		}
	}
}

// corpusPreview previews a sample the way import.preview does with a Word file.
func (e *env) corpusPreview(s corpusSample, in imports.PreviewIn) imports.PreviewOut {
	e.t.Helper()
	in.Docx, in.Name = corpusDoc(s.paras), "book.docx"
	return e.must(e.w, "import.preview", in).(imports.PreviewOut)
}

// TestDocxCorpusAuto is the detection accuracy measure: for each sample, does `auto` make what the plan says it should, leave out what
// it should, and list the weak candidates it should.
func TestDocxCorpusAuto(t *testing.T) {
	e := newEnv(t)
	right := 0
	samples := corpusSamples()
	for _, s := range samples {
		pv := e.corpusPreview(s, imports.PreviewIn{})
		got := outline(pv.Nodes)
		var left []string
		for _, l := range pv.LeftOut {
			left = append(left, l.Title)
		}
		weak := 0
		if pv.Breaks != nil {
			weak = pv.Breaks.SuggestionsTotal
		}
		if strings.Join(got, "\n") == strings.Join(s.out, "\n") && strings.Join(left, "|") == strings.Join(s.left, "|") && weak == s.weak {
			right++
			t.Logf("ok   %s", s.name)
			continue
		}
		t.Errorf("MISS %s\n got:  %s (left out %v, %d suggestions)\n want: %s (left out %v, %d suggestions)", s.name, strings.Join(got, " | "), left, weak, strings.Join(s.out, " | "), s.left, s.weak)
	}
	t.Logf("ACCURACY %d of %d samples cut as the plan says", right, len(samples))
}

// sample finds a corpus sample by its name's prefix.
func corpusByName(t *testing.T, prefix string) corpusSample {
	t.Helper()
	for _, s := range corpusSamples() {
		if strings.HasPrefix(s.name, prefix) {
			return s
		}
	}
	t.Fatalf("no sample %s", prefix)
	return corpusSample{}
}

// A weak rule cuts only when the writer chooses it, and then the first line of a chapter is its title when it is title-like.
func TestDocxCorpusWeakRulesAreTheWritersChoice(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		sample, split string
		want          []string
	}{
		{"05", imports.SplitNumerals, []string{"chapter:I", "chapter:II", "chapter:III", "chapter:IV"}},
		{"06b", imports.SplitPageBreaks, []string{"chapter:Chapter 1", "chapter:Chapter 2", "chapter:Chapter 3"}},
		{"07-", imports.SplitPageBreaks, []string{"chapter:The Road", "chapter:The Mill", "chapter:Snow"}},
		{"08-", imports.SplitSectionBreaks, []string{"chapter:Chapter 1", "chapter:Chapter 2", "chapter:Chapter 3"}},
		{"06-", imports.SplitNone, []string{"chapter:Sample Novel"}},
	} {
		pv := e.corpusPreview(corpusByName(t, c.sample), imports.PreviewIn{DocxSplit: c.split})
		eq(t, outline(pv.Nodes), c.want)
	}
	// choosing one by hand: "Start a chapter here"
	s := corpusByName(t, "06b")
	pv := e.corpusPreview(s, imports.PreviewIn{})
	if pv.Breaks == nil || !pv.Breaks.NoneFound || len(pv.Breaks.Suggestions) != 2 {
		t.Fatalf("06b: %+v", pv.Breaks)
	}
	pv = e.corpusPreview(s, imports.PreviewIn{DocxBreaks: []int{pv.Breaks.Suggestions[0].Ordinal}})
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter 1", "chapter:Chapter 2"})
	if len(pv.Breaks.Suggestions) != 1 || len(pv.Breaks.Added) != 1 {
		t.Fatalf("one suggestion is left: %+v", pv.Breaks)
	}
}

// D5, D8 and the reasons: capitals are kept and title case offered; Word's number is not in the title; each chapter says why.
func TestDocxCorpusTitlesAndReasons(t *testing.T) {
	e := newEnv(t)
	pv := e.corpusPreview(corpusByName(t, "04"), imports.PreviewIn{})
	var sug []string
	for _, n := range pv.Nodes {
		sug = append(sug, n.Title+">"+n.SuggestedTitle)
	}
	eq(t, sug, []string{"THE ROAD>The Road", "THE MILL>The Mill", "SNOW>Snow"})
	pv = e.corpusPreview(corpusByName(t, "10"), imports.PreviewIn{})
	if pv.Nodes[0].Title != "The Road" || pv.Nodes[0].WordNumber != "Chapter 1" || pv.Nodes[2].WordNumber != "Chapter 3" {
		t.Fatalf("a Word number is shown and not in the title: %+v", pv.Nodes[0])
	}
	pv = e.corpusPreview(corpusByName(t, "11"), imports.PreviewIn{})
	if len(pv.LeftOut) != 1 || pv.LeftOut[0].Why != "contents" || pv.LeftOut[0].Words < 6 || pv.Documents != 6 {
		t.Fatalf("contents is left out on its own: %+v", pv.LeftOut)
	}
	// "Put back"
	in := imports.PreviewIn{Edits: []imports.Edit{{Key: pv.LeftOut[0].Key, Keep: true}}}
	back := e.corpusPreview(corpusByName(t, "11"), in)
	if back.Documents != 7 || len(back.LeftOut) != 0 || strings.Contains(strings.Join(back.Warnings, "|"), "empty") {
		t.Fatalf("put back: %d documents, left out %v", back.Documents, back.LeftOut)
	}
	pv = e.corpusPreview(corpusByName(t, "03"), imports.PreviewIn{})
	if pv.Nodes[0].Confidence == 0 || len(pv.Nodes[0].Why) == 0 || pv.Nodes[0].Ordinal == 0 || pv.Nodes[0].FirstLine == "" {
		t.Fatalf("a chapter says why and where: %+v", pv.Nodes[0])
	}
}

// The same file and options give the same cut.
func TestDocxCutIsDeterministic(t *testing.T) {
	e := newEnv(t)
	for _, s := range corpusSamples() {
		a := e.corpusPreview(s, imports.PreviewIn{})
		for i := 0; i < 3; i++ {
			b := e.corpusPreview(s, imports.PreviewIn{})
			if a.Token != b.Token || strings.Join(outline(a.Nodes), "|") != strings.Join(outline(b.Nodes), "|") {
				t.Fatalf("%s: two previews differ", s.name)
			}
		}
	}
}
