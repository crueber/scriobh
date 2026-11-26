package imports_test

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// A tiny hand-made DOCX: the parts named in m, with the standard wrapper.
func miniDocx(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	all := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`,
	}
	for k, v := range parts {
		all[k] = v
	}
	for name, body := range all {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const wns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`

func body(inner string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><w:document ` + wns + `><w:body>` + inner + `</w:body></w:document>`
}

func para(style, text string) string {
	s := ""
	if style != "" {
		s = `<w:pPr><w:pStyle w:val="` + style + `"/></w:pPr>`
	}
	return `<w:p>` + s + `<w:r><w:t xml:space="preserve">` + text + `</w:t></w:r></w:p>`
}

var headingStyles = `<?xml version="1.0"?><w:styles ` + wns + `><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style><w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:pPr><w:outlineLvl w:val="1"/></w:pPr></w:style><w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/></w:style></w:styles>`

func (e *env) previewDocx(data []byte, in imports.PreviewIn) imports.PreviewOut {
	e.t.Helper()
	in.Docx = data
	in.Name = "book.docx"
	return e.must(e.w, "import.preview", in).(imports.PreviewOut)
}

func (e *env) applyDocx(data []byte, pv imports.PreviewOut, in imports.ApplyIn) imports.ApplyOut {
	e.t.Helper()
	in.Docx, in.Token, in.Name = data, pv.Token, "book.docx"
	return e.must(e.w, "import.apply", in).(imports.ApplyOut)
}

func (e *env) docContent(projectID, title string) string {
	e.t.Helper()
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: projectID}).(docs.ListOut)
	for _, m := range lo.Items {
		if m.Title == title {
			return e.must(e.w, "doc.get", docs.DocIn{ID: m.ID}).(docs.Item).Item.Content
		}
	}
	e.t.Fatalf("no document %q", title)
	return ""
}

// What this program writes, it reads back: the Shunn DOCX of a project imports as
// the same chapters, the same text, the same scene breaks, the same footnotes.
func TestDocxRoundTrip(t *testing.T) {
	e := newEnv(t)
	out := e.must(e.w, "project.create", projects.CreateIn{Title: "The Salt Road", Subtitle: "A novel", AuthorName: "Máire Ní Bhriain", Kind: "novel"}).(projects.Item)
	pid := out.Item.ID
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	for _, m := range lo.Items {
		e.must(e.w, "doc.trash", docs.DocIn{ID: m.ID})
	}
	mk := func(title, kind, content string) {
		e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: title, Kind: kind, Content: content})
	}
	mk("Dedication", "front_matter", "For the ones who walked.")
	mk("The Arrival", "chapter", "The boat came in at *dawn*, and **loud**.[^1]\n\nA second paragraph with a link and `code`.\n\n* * *\n\nAfter the break.\n\n> A quote.\n\n- one\n- two\n\n1. first\n2. second\n\n[^1]: A footnote.\n")
	mk("The Road", "chapter", "Salt on the road.")
	o := e.must(e.w, "export.compile", compile.CompileIn{ProjectID: pid, Preset: "shunn-us"}).(compile.Out)

	pv := e.previewDocx(o.Body, imports.PreviewIn{})
	if pv.Layout != "docx" || pv.Project.Title != "The Salt Road" || pv.Project.Subtitle != "A novel" || pv.Project.Author != "Máire Ní Bhriain" {
		t.Fatalf("preview: %+v", pv.Project)
	}
	var titles []string
	for _, n := range pv.Nodes {
		titles = append(titles, n.Kind+":"+n.Title)
	}
	if got := strings.Join(titles, " | "); got != "front_matter:Dedication | chapter:The Arrival | chapter:The Road" {
		// a file named for the dedication is front matter, as in any folder import
		t.Fatalf("nodes: %s", got)
	}
	joined := strings.Join(pv.Warnings, "\n")
	if strings.Contains(joined, "comment") || strings.Contains(joined, "tracked") {
		t.Errorf("warnings: %s", joined)
	}
	ap := e.applyDocx(o.Body, pv, imports.ApplyIn{})
	got := e.docContent(ap.ProjectID, "The Arrival")
	want := "The boat came in at *dawn*, and **loud**.[^1]\n\nA second paragraph with a link and `code`.\n\n* * *\n\nAfter the break.\n\n> A quote.\n\n- one\n- two\n\n1. first\n2. second\n\n[^1]: A footnote.\n"
	if got != want {
		t.Errorf("chapter text\n got:\n%q\nwant:\n%q", got, want)
	}
	// provenance
	var kind, hash string
	if err := e.d.Read().QueryRow(`SELECT kind, source_hash FROM project_sources WHERE project_id = ?`, ap.ProjectID).Scan(&kind, &hash); err != nil || kind != "docx" || len(hash) != 64 {
		t.Errorf("provenance: %s %s %v", kind, hash, err)
	}
	var n int
	e.d.Read().QueryRow(`SELECT count(*) FROM document_sources ds JOIN documents d ON d.id = ds.document_id WHERE d.project_id = ?`, ap.ProjectID).Scan(&n)
	if n != 3 {
		t.Errorf("document_sources rows: %d", n)
	}
	// Exporting the imported project and importing again is stable.
	o2 := e.must(e.w, "export.compile", compile.CompileIn{ProjectID: ap.ProjectID, Preset: "shunn-us"}).(compile.Out)
	pv2 := e.previewDocx(o2.Body, imports.PreviewIn{})
	if len(pv2.Nodes) != 3 {
		t.Errorf("second round: %d nodes", len(pv2.Nodes))
	}
}

func TestDocxParts(t *testing.T) {
	e := newEnv(t)
	xml := body(para("Heading1", "Part One") + para("", "Epigraph.") + para("Heading2", "Chapter A") + para("", "Text A.") + para("Heading2", "Chapter B") + para("", "Text B.") + para("Heading1", "Part Two") + para("Heading2", "Chapter C") + para("", "Text C."))
	d := miniDocx(t, map[string]string{"word/document.xml": xml, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{DocxHeadings: "parts"})
	var got []string
	for _, n := range pv.Nodes {
		got = append(got, n.Kind+":"+n.Title)
	}
	if strings.Join(got, "|") != "part:Part One|chapter:Part One|chapter:Chapter A|chapter:Chapter B|part:Part Two|chapter:Chapter C" {
		t.Errorf("parts: %v", got)
	}
	// the default now tells parts from chapters by itself (D2): the same book needs no setting
	pv = e.previewDocx(d, imports.PreviewIn{})
	got = nil
	for _, n := range pv.Nodes {
		got = append(got, n.Kind+":"+n.Title)
	}
	if strings.Join(got, "|") != "part:Part One|chapter:Part One|chapter:Chapter A|chapter:Chapter B|part:Part Two|chapter:Chapter C" {
		t.Errorf("parts without a setting: %v", got)
	}
	// chapters mode: Heading 2 stays inside, and split_scenes turns it into scenes
	pv = e.previewDocx(d, imports.PreviewIn{SplitScenes: "h2", DocxHeadings: "chapters"})
	got = nil
	for _, n := range pv.Nodes {
		got = append(got, n.Kind+":"+n.Title)
	}
	if strings.Join(got, "|") != "chapter:Part One|scene:Chapter A|scene:Chapter B|chapter:Part Two|scene:Chapter C" {
		t.Errorf("chapters with scenes: %v", got)
	}
}

func TestDocxWhatItReports(t *testing.T) {
	e := newEnv(t)
	rels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId9" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com/x" TargetMode="External"/><Relationship Id="rId8" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="file:///etc/passwd" TargetMode="External"/></Relationships>`
	doc := body(
		para("Title", "A Title") + para("Heading1", "One") +
			`<w:p><w:r><w:t xml:space="preserve">Kept </w:t></w:r><w:ins w:id="1"><w:r><w:t>inserted </w:t></w:r></w:ins><w:del w:id="2"><w:r><w:delText>deleted</w:delText></w:r></w:del><w:r><w:rPr><w:u w:val="single"/></w:rPr><w:t>underlined</w:t></w:r></w:p>` +
			`<w:p><w:hyperlink r:id="rId9"><w:r><w:t>a web link</w:t></w:r></w:hyperlink> and <w:hyperlink r:id="rId8"><w:r><w:t>a file link</w:t></w:r></w:hyperlink></w:p>` +
			`<w:p><w:r><w:drawing><wp:inline xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"/></w:drawing></w:r></w:p>` +
			para("", "Gap before") + `<w:p/><w:p/>` + para("", "Gap after") + para("", "* * *") + para("", "#") + para("", "End."),
	)
	comments := `<?xml version="1.0"?><w:comments ` + wns + `><w:comment w:id="0" w:author="X"><w:p><w:r><w:t>Hmm</w:t></w:r></w:p></w:comment><w:comment w:id="1"><w:p/></w:comment></w:comments>`
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles, "word/_rels/document.xml.rels": rels, "word/comments.xml": comments,
		"word/vbaProject.bin": "MACRO", "word/media/image1.png": "x", "word/header1.xml": "<w:hdr/>", "word/embeddings/o.bin": "ole"})
	// docx_changes accept is what every import did before suggestions: the final view
	pv := e.previewDocx(d, imports.PreviewIn{DocxChanges: "accept"})
	joined := strings.Join(pv.Warnings, "\n")
	for _, want := range []string{"2 Word comment(s): 0 imported", "tracked change(s). The final view was imported, as asked", "image(s) were not imported", "macros", "embedded object", "underlined passage", "1 link(s) to something other than", "gap(s) of two or more empty paragraphs", "Headers and footers"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning with %q in:\n%s", want, joined)
		}
	}
	ap := e.applyDocx(d, pv, imports.ApplyIn{DocxChanges: "accept"})
	text := e.docContent(ap.ProjectID, "One")
	for _, want := range []string{"Kept inserted underlined", "[a web link](https://example.com/x)", "a file link", "*\\[image\\]*", "Gap before\n\n* * *\n\nGap after", "End."} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	for _, gone := range []string{"deleted", "file:///", "MACRO", "Hmm"} {
		if strings.Contains(text, gone) {
			t.Errorf("text holds %q", gone)
		}
	}
	// the two scene break paragraphs in a row are one break
	if strings.Count(text, "* * *") != 2 {
		t.Errorf("scene breaks: %q", text)
	}
	if pv.Project.Title != "A Title" {
		t.Errorf("title %q", pv.Project.Title)
	}
}

func TestDocxHostile(t *testing.T) {
	e := newEnv(t)
	preview := func(d []byte) error {
		_, err := e.call(e.w, "import.preview", imports.PreviewIn{Docx: d, Name: "x.docx"})
		return err
	}
	bad := map[string][]byte{
		"not a zip":        []byte("this is not a docx"),
		"no document part": miniDocx(t, map[string]string{"word/styles.xml": headingStyles}),
		"a DOCTYPE":        miniDocx(t, map[string]string{"word/document.xml": `<?xml version="1.0"?><!DOCTYPE d [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;">]>` + body(para("", "&b;"))}),
		"not XML":          miniDocx(t, map[string]string{"word/document.xml": "<w:document><w:body><w:p>"}),
		"deep nesting":     miniDocx(t, map[string]string{"word/document.xml": body(`<w:p>` + strings.Repeat("<w:r>", 500) + strings.Repeat("</w:r>", 500) + `</w:p>`)}),
		"a bomb":           miniDocx(t, map[string]string{"word/document.xml": body(`<w:p><w:r><w:t>` + strings.Repeat("a", 100<<20) + `</w:t></w:r></w:p>`)}),
	}
	for name, d := range bad {
		err := preview(d)
		if ae := ops.AsError(err); err == nil || ae.Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Files and a docx together are refused.
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Docx: miniDocx(t, map[string]string{"word/document.xml": body(para("", "x"))}), Files: files(map[string]string{"a.md": "x"})}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("both: %v", err)
	}
	// A document with only an image or empty body has nothing to import.
	ok := miniDocx(t, map[string]string{"word/document.xml": body(`<w:p/>`)})
	pv, err := e.call(e.w, "import.preview", imports.PreviewIn{Docx: ok, Name: "x.docx"})
	if err == nil && pv.(imports.PreviewOut).Documents != 0 {
		t.Errorf("an empty document made documents")
	}
}

func TestDocxStyledAsWordWritesIt(t *testing.T) {
	// A document without styles.xml at all (a very small writer) still imports: its text is one chapter.
	e := newEnv(t)
	d := miniDocx(t, map[string]string{"word/document.xml": body(para("", "Just some words."))})
	pv := e.previewDocx(d, imports.PreviewIn{})
	if pv.Documents != 1 || pv.Words != 3 {
		t.Errorf("%+v", pv)
	}
}
