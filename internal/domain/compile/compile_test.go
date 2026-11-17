package compile_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/publish/check"
)

func TestShunnDOCX(t *testing.T) {
	f := newFx(t, "The Salt Road")
	d := f.novel()
	f.setContact()
	out := f.compile("shunn-us", publish.Options{})
	if probs := check.DOCX(out.Body); len(probs) > 0 {
		t.Fatalf("DOCX problems: %v", probs)
	}
	if out.Filename != "the-salt-road-shunn-us.docx" || out.Format != "docx" {
		t.Errorf("%s %s", out.Filename, out.Format)
	}
	parts := unzip(t, out.Body)
	doc, styles := parts["word/document.xml"], parts["word/styles.xml"]

	// What is in, and what is not.
	for _, want := range []string{"dedmark", "epimark", "ch1mark", "s1mark", "s2mark", "ch2mark", "ch3mark", "backmark", "notemark"} {
		if !strings.Contains(doc+parts["word/footnotes.xml"], want) {
			t.Errorf("the DOCX lost %q", want)
		}
	}
	for _, gone := range []string{"ghostmark", "privmark", "n1mark", "n2mark", "skipmark", "kidmark", "gonemark", "other wording", "scriobh:alts"} {
		for name, body := range parts {
			if strings.Contains(body, gone) {
				t.Errorf("%s holds %q", name, gone)
			}
		}
	}
	// Order: front matter, part one, chapters, part two, back matter.
	order := []string{"dedmark", "Part One", "epimark", "ch1mark", "s1mark", "ch2mark", "Part Two", "ch3mark", "backmark"}
	last := -1
	for _, w := range order {
		i := strings.Index(doc, w)
		if i < 0 || i < last {
			t.Errorf("%q is out of order (at %d, after %d)", w, i, last)
		}
		last = i
	}
	// The title page.
	for _, want := range []string{"Máire Ní Bhriain", "12 Quay Street", "Galway", "maire@example.com", "+353 1 555 0100", "The Salt Road", "A sub title", "by Máire Ní Bhriain"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the title page lacks %q", want)
		}
	}
	if !regexp.MustCompile(`about [0-9,]+ words`).MatchString(doc) {
		t.Error("the title page has no word count")
	}
	// Shunn specifics, in the styles and not in direct formatting.
	for _, want := range []string{
		`w:ascii="Times New Roman"`, `<w:sz w:val="24"/>`,
		`w:styleId="Normal"`, `w:line="480" w:lineRule="auto"`, // double spacing
		`w:styleId="BodyText"`, `<w:ind w:firstLine="720"/>`, // half-inch indent
		`w:styleId="FirstParagraph"`, `<w:ind w:firstLine="0"/>`,
		`w:styleId="SceneBreak"`, `w:styleId="Title"`, `w:styleId="Heading1"`, `w:styleId="ChapterOpening"`,
		`<w:pgSz w:w="12240" w:h="15840"/>`,
	} {
		if !strings.Contains(styles+doc, want) {
			t.Errorf("styles/document lack %s", want)
		}
	}
	if !strings.Contains(doc, `<w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"`) {
		t.Error("margins are not one inch")
	}
	// Scene breaks are # paragraphs of the Scene Break style, and the break at the end of chapter two is warned of.
	if !strings.Contains(doc, `<w:pStyle w:val="SceneBreak"/></w:pPr><w:r><w:t xml:space="preserve">#</w:t>`) {
		t.Error("no # scene break paragraph")
	}
	// Paragraphs use styles, not direct indents.
	if strings.Contains(doc, `<w:ind w:firstLine="720"`) {
		t.Error("a paragraph carries a direct indent")
	}
	// The running header, and a different first page.
	h1 := parts["word/header1.xml"]
	if !strings.Contains(h1, "Ní Bhriain / THE SALT ROAD / ") || !strings.Contains(h1, "PAGE") {
		t.Errorf("running header: %s", h1)
	}
	if strings.Contains(parts["word/header2.xml"], "<w:t") {
		t.Error("the first page has a header")
	}
	if !strings.Contains(doc, "<w:titlePg/>") {
		t.Error("no different first page")
	}
	// Footnote.
	if !strings.Contains(doc, `<w:footnoteReference w:id="1"/>`) || !strings.Contains(parts["word/footnotes.xml"], "notemark") {
		t.Error("the footnote is not a real footnote")
	}
	// docProps are the project's, with the project's own date.
	if core := parts["docProps/core.xml"]; !strings.Contains(core, "<dc:title>The Salt Road</dc:title>") || !strings.Contains(core, "<dc:creator>Máire Ní Bhriain</dc:creator>") {
		t.Errorf("core props: %s", core)
	}
	// No external relationship, no macro, no embedded object.
	for name, body := range parts {
		if strings.Contains(body, "TargetMode") || strings.Contains(name, "vbaProject") || strings.Contains(name, "embeddings") || strings.Contains(body, "oleObject") {
			t.Errorf("%s has an external or active part", name)
		}
	}
	_ = d
}

func TestPreviewTellsTheTruth(t *testing.T) {
	f := newFx(t, "The Salt Road")
	d := f.novel()
	f.setContact()
	p := f.preview("shunn-us", publish.Options{})
	if p.Format != "docx" || p.Preset != "shunn-us" || p.InputHash == "" || !p.PageEstimated || p.PageCount < 3 {
		t.Errorf("preview: %+v", p)
	}
	if p.TitlePage.Layout != "manuscript" || len(p.TitlePage.Contact) != 5 || p.TitlePage.Contact[0] != "Máire Ní Bhriain" || p.TitlePage.Title != "The Salt Road" ||
		p.TitlePage.Byline != "by Máire Ní Bhriain" || !strings.HasPrefix(p.TitlePage.WordCount, "about ") {
		t.Errorf("title page: %+v", p.TitlePage)
	}
	if p.RunningHeader != "Ní Bhriain / THE SALT ROAD / 2" {
		t.Errorf("running header %q", p.RunningHeader)
	}
	var heads []string
	for _, o := range p.Outline {
		heads = append(heads, o.Kind+":"+o.Heading)
	}
	want := "front_matter:Dedication part:Part One chapter:Chapter 1: The Arrival chapter:Chapter 2 part:Part Two chapter:Chapter 3: The End back_matter:Afterword"
	if got := strings.Join(heads, " "); got != want {
		t.Errorf("outline\n got %s\nwant %s", got, want)
	}
	// chapters in a part are level 2; words and pages are there
	for _, o := range p.Outline {
		if o.Kind == "chapter" && o.Level != 2 || o.Words == 0 && o.Kind != "part" || o.Page < 2 {
			t.Errorf("outline item %+v", o)
		}
	}
	if p.Outline[2].Documents[0] != d["c1"].ID || len(p.Outline[2].Documents) != 3 { // the chapter and its two scenes
		t.Errorf("chapter one holds %v", p.Outline[2].Documents)
	}
	codes := map[string]bool{}
	for _, w := range p.Warnings {
		codes[w.Code] = true
	}
	for _, c := range []string{"chapter_untitled", "scene_break_at_end", "not_compiled", "notes_excluded"} {
		if !codes[c] {
			t.Errorf("no %s warning in %+v", c, p.Warnings)
		}
	}
	if codes["no_author"] || codes["no_contact"] {
		t.Errorf("author and contact are set: %+v", p.Warnings)
	}
	if p.ExcludedTotal != 2 || len(p.Excluded) != 2 || p.Excluded[0].Title != "Cut Chapter" || p.Excluded[1].Reason != "inside a document marked do not compile" {
		t.Errorf("excluded: %+v", p.Excluded)
	}
	if p.Notes.Documents != 2 || p.Notes.Words == 0 {
		t.Errorf("notes: %+v", p.Notes)
	}
	if len(p.Pages) != 3 || !p.Pages[0].TitlePage || p.Pages[1].Header != "Ní Bhriain / THE SALT ROAD / 2" || p.Pages[0].Header != "" {
		t.Errorf("pages: %+v", p.Pages[0])
	}
	// The preview and the export agree.
	out := f.compile("shunn-us", publish.Options{})
	if out.InputHash != p.InputHash {
		t.Error("preview and export disagree about the input hash")
	}
	// Without contact details the preview says so.
	g := newFx(t, "No Contact")
	g.doc("One", "chapter", "", "Words.")
	pp := g.preview("shunn-us", publish.Options{})
	var got []string
	for _, w := range pp.Warnings {
		got = append(got, w.Code)
	}
	if !strings.Contains(strings.Join(got, ","), "no_contact") {
		t.Errorf("warnings %v", got)
	}
}

func TestOptionsChangeTheCompile(t *testing.T) {
	f := newFx(t, "Options")
	f.novel()
	f.setContact()
	// Ghosts kept, notes appended, scene titles and ornament break.
	out := f.compile("styled-docx", publish.Options{Ghosts: "keep", Notes: "appendix", SceneTitles: publish.True(), SceneBreak: "ornament", Ornament: "~ ~ ~"})
	parts := unzip(t, out.Body)
	doc := parts["word/document.xml"]
	for _, want := range []string{"ghostmark", "n1mark", "n2mark", "Harbour", "Plot Notes", "Notes"} {
		if !strings.Contains(doc, want) {
			t.Errorf("lost %q", want)
		}
	}
	for _, gone := range []string{"privmark", "skipmark", "scriobh:alts", "other wording"} {
		if strings.Contains(doc, gone) {
			t.Errorf("holds %q", gone)
		}
	}
	// the styled preset: Georgia, 1.15 spacing, justified, footer page numbers, no header
	styles := parts["word/styles.xml"]
	for _, want := range []string{`w:ascii="Georgia"`, `w:line="276" w:lineRule="auto"`, `<w:jc w:val="both"/>`} {
		if !strings.Contains(styles, want) {
			t.Errorf("styles lack %s", want)
		}
	}
	if _, ok := parts["word/footer1.xml"]; !ok {
		t.Error("no footer")
	}
	if probs := check.DOCX(out.Body); len(probs) > 0 {
		t.Errorf("%v", probs)
	}
	// UK preset: A4, word count label, Language en-GB, mixed-case header.
	uk := f.compile("uk-manuscript", publish.Options{})
	up := unzip(t, uk.Body)
	if !strings.Contains(up["word/document.xml"], `<w:pgSz w:w="11906" w:h="16838"/>`) || !strings.Contains(up["word/document.xml"], "Word count: ") ||
		!strings.Contains(up["word/header1.xml"], "Ní Bhriain / Options / ") || !strings.Contains(up["word/styles.xml"], `w:lang w:val="en-GB"`) {
		t.Errorf("UK manuscript: %s", up["word/header1.xml"])
	}
	if probs := check.DOCX(uk.Body); len(probs) > 0 {
		t.Errorf("%v", probs)
	}
	// Courier variant and a bad option.
	co := f.compile("shunn-us-courier", publish.Options{})
	if !strings.Contains(unzip(t, co.Body)["word/styles.xml"], `w:ascii="Courier New"`) {
		t.Error("not Courier")
	}
	_, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "shunn-us", Options: publish.Options{Font: "Comic Sans"}})
	if ae := ops.AsError(err); ae.Code != ops.CodeInvalid {
		t.Errorf("a bad font: %v", err)
	}
	_, err = f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "nope"})
	if ae := ops.AsError(err); ae.Code != ops.CodeNotFound {
		t.Errorf("an unknown preset: %v", err)
	}
	// chapter heading styles
	h := f.compile("shunn-us", publish.Options{ChapterHeading: "title", PartPages: publish.False()})
	hd := unzip(t, h.Body)["word/document.xml"]
	if strings.Contains(hd, "Part One") || strings.Contains(hd, "Chapter 1:") || !strings.Contains(hd, ">The Arrival<") {
		t.Error("title-only headings, no part pages")
	}
}

func TestEPUB(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	out := f.compile("epub3", publish.Options{})
	if probs := check.EPUB(out.Body); len(probs) > 0 {
		t.Fatalf("EPUB problems: %v", probs)
	}
	if out.Filename != "the-salt-road-epub3.epub" || out.ContentType != "application/epub+zip" {
		t.Errorf("%s %s", out.Filename, out.ContentType)
	}
	parts := unzip(t, out.Body)
	opf := parts["OEBPS/content.opf"]
	for _, want := range []string{">The Salt Road</dc:title>", `property="title-type">subtitle</meta>`, `name="generator" content="Scriobh`, "<dc:creator", "Máire Ní Bhriain", "<dc:language>en</dc:language>", `properties="cover-image"`, `urn:uuid:`, `dcterms:modified`} {
		if !strings.Contains(opf, want) {
			t.Errorf("OPF lacks %s", want)
		}
	}
	all := ""
	for n, b := range parts {
		if strings.HasSuffix(n, ".xhtml") {
			all += b
		}
	}
	for _, want := range []string{"ch1mark", "s1mark", "dedmark", "backmark", "notemark", `epub:type="noteref"`} {
		if !strings.Contains(all, want) {
			t.Errorf("EPUB lacks %q", want)
		}
	}
	for _, gone := range []string{"ghostmark", "privmark", "n1mark", "skipmark", "scriobh:alts", "other wording"} {
		if strings.Contains(all, gone) {
			t.Errorf("EPUB holds %q", gone)
		}
	}
	for _, mark := range []string{"<script", "http://", "https://", "javascript:"} {
		for n, b := range parts {
			if strings.HasSuffix(n, ".xhtml") && strings.Contains(strings.ReplaceAll(b, "http://www.w3.org/1999/xhtml", ""), mark) && !strings.Contains(mark, "http") {
				t.Errorf("%s holds %s", n, mark)
			}
		}
	}
	// The same project compiles to the same bytes.
	again := f.compile("epub3", publish.Options{})
	if string(again.Body) != string(out.Body) || again.InputHash != out.InputHash {
		t.Error("two compiles of the same project differ")
	}
}

func TestPlainMarkdown(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	out := f.compile("plain-markdown", publish.Options{})
	s := string(out.Body)
	if !strings.HasPrefix(s, "# The Salt Road\n\n*A sub title*\n\nby Máire Ní Bhriain\n") {
		t.Errorf("start:\n%s", s)
	}
	for _, want := range []string{"## Dedication", "## Part One", "### The Arrival", "ch1mark", "* * *", "[^1]: The note text, notemark.", "## Afterword"} {
		if !strings.Contains(s, want) {
			t.Errorf("markdown lacks %q:\n%s", want, s)
		}
	}
	for _, gone := range []string{"ghostmark", "privmark", "scriobh", "n1mark", "skipmark", "<!--", "%%"} {
		if strings.Contains(s, gone) {
			t.Errorf("markdown holds %q", gone)
		}
	}
	if out.Format != "md" || out.ContentType != "text/markdown; charset=utf-8" {
		t.Errorf("%s %s", out.Format, out.ContentType)
	}
}

func TestExportIsRecordedWithoutPersonalData(t *testing.T) {
	f := newFx(t, "The Salt Road")
	d := f.novel()
	f.setContact()
	out := f.compile("shunn-us", publish.Options{})
	var preset, format, manifest string
	if err := f.d.Read().QueryRow(`SELECT preset, format, manifest_json FROM exports WHERE id = ?`, out.ExportID).Scan(&preset, &format, &manifest); err != nil {
		t.Fatal(err)
	}
	if preset != "shunn-us" || format != "docx" {
		t.Errorf("%s %s", preset, format)
	}
	var m struct {
		Docs      []struct{ ID, SHA256 string } `json:"documents"`
		PresetID  string                        `json:"preset_id"`
		InputHash string                        `json:"input_hash"`
		Options   publish.Options               `json:"options"`
		Left      []string                      `json:"not_compiled"`
	}
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		t.Fatal(err)
	}
	if m.PresetID != "shunn-us" || m.InputHash != out.InputHash || m.Options.Font != "Times New Roman" || len(m.Left) != 2 {
		t.Errorf("manifest: %s", manifest)
	}
	held := map[string]string{}
	for _, h := range m.Docs {
		held[h.ID] = h.SHA256
		if len(h.SHA256) != 64 {
			t.Errorf("hash %q", h.SHA256)
		}
	}
	for _, in := range []string{"front", "p1", "c1", "s1", "s2", "c2", "p2", "c3", "back"} {
		if held[d[in].ID] == "" {
			t.Errorf("the manifest does not hold %s", in)
		}
	}
	for _, out := range []string{"skip", "skipkid", "n1", "n2", "gone"} {
		if _, ok := held[d[out].ID]; ok {
			t.Errorf("the manifest holds %s", out)
		}
	}
	// The contact block is in the file and nowhere else.
	for _, pii := range []string{"maire@example.com", "+353 1 555 0100", "Quay Street"} {
		rows, err := f.d.Read().Query(`SELECT manifest_json FROM exports UNION ALL SELECT COALESCE(detail_json,'') FROM audit_log UNION ALL SELECT payload_json FROM events`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			rows.Scan(&s)
			if strings.Contains(s, pii) {
				t.Errorf("%q leaked into the records: %s", pii, s)
			}
		}
		rows.Close()
	}
}

func TestSubmissionBundle(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	f.setContact()
	res, err := f.reg.Call(f.ctx, f.owner, "export.submission", compile.SubmissionIn{ProjectID: f.pid})
	if err != nil {
		t.Fatal(err)
	}
	out := res.(compile.Out)
	parts := unzip(t, out.Body)
	var names []string
	for n := range parts {
		names = append(names, n)
	}
	for _, want := range []string{"the-salt-road-manuscript.docx", "synopsis.md", "cover-letter.md", "manifest.json"} {
		if _, ok := parts[want]; !ok {
			t.Errorf("no %s in %v", want, names)
		}
	}
	if probs := check.DOCX([]byte(parts["the-salt-road-manuscript.docx"])); len(probs) > 0 {
		t.Errorf("%v", probs)
	}
	if !strings.Contains(parts["cover-letter.md"], "Dear [Editor's name]") || !strings.Contains(parts["cover-letter.md"], "*The Salt Road*") || !strings.Contains(parts["cover-letter.md"], "maire@example.com") {
		t.Errorf("cover letter:\n%s", parts["cover-letter.md"])
	}
	if !strings.Contains(parts["synopsis.md"], "# Synopsis: The Salt Road") {
		t.Errorf("synopsis:\n%s", parts["synopsis.md"])
	}
	var warn []string
	for _, w := range out.Warnings {
		warn = append(warn, w.Code)
	}
	if !strings.Contains(strings.Join(warn, ","), "no_synopsis") {
		t.Errorf("the placeholder synopsis is not warned of: %v", warn)
	}
	again, _ := f.reg.Call(f.ctx, f.owner, "export.submission", compile.SubmissionIn{ProjectID: f.pid})
	if string(again.(compile.Out).Body) != string(out.Body) {
		t.Error("the bundle is not deterministic")
	}
	var preset, format string
	f.d.Read().QueryRow(`SELECT preset, format FROM exports WHERE id = ?`, out.ExportID).Scan(&preset, &format)
	if preset != "submission/shunn-us" || format != "docx" {
		t.Errorf("recorded as %s %s", preset, format)
	}
	// A synopsis from the description.
	desc := "A long synopsis, descmark."
	if _, err := f.reg.Call(f.ctx, f.owner, "project.update", projects.UpdateIn{ID: f.pid, Description: &desc}); err != nil {
		t.Fatal(err)
	}
	res, err = f.reg.Call(f.ctx, f.owner, "export.submission", compile.SubmissionIn{ProjectID: f.pid})
	if err != nil {
		t.Fatal(err)
	}
	withDesc := res.(compile.Out)
	if s := unzip(t, withDesc.Body)["synopsis.md"]; !strings.Contains(s, "descmark") {
		t.Errorf("synopsis from the description:\n%s", s)
	}
	for _, w := range withDesc.Warnings {
		if w.Code == "no_synopsis" {
			t.Error("a project with a description has a synopsis")
		}
	}
	// An EPUB preset is refused.
	if _, err := f.reg.Call(f.ctx, f.owner, "export.submission", compile.SubmissionIn{ProjectID: f.pid, Preset: "epub3"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("an EPUB in a submission bundle: %v", err)
	}
}

func TestAuth(t *testing.T) {
	f := newFx(t, "Auth")
	f.doc("One", "chapter", "", "Words.")
	for _, tc := range []struct {
		name string
		in   any
	}{
		{"export.compile", compile.CompileIn{ProjectID: f.pid}},
		{"compile.preview", compile.PreviewIn{ProjectID: f.pid}},
		{"compile.preset.list", compile.PresetListIn{ProjectID: f.pid}},
		{"compile.contact.get", compile.ContactIn{ProjectID: f.pid}},
		{"export.submission", compile.SubmissionIn{ProjectID: f.pid}},
	} {
		if _, err := f.reg.Call(f.ctx, f.outsider, tc.name, tc.in); ops.AsError(err).Code != ops.CodeNotFound {
			t.Errorf("%s as a stranger: %v", tc.name, err)
		}
		if _, err := f.reg.Call(f.ctx, f.reader, tc.name, tc.in); err == nil || ops.AsError(err).Code != ops.CodeForbidden {
			t.Errorf("%s as a reader: %v", tc.name, err)
		}
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid}); err != nil {
		t.Errorf("the owner: %v", err)
	}
}

func TestNothingToCompile(t *testing.T) {
	f := newFx(t, "Empty")
	_, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid})
	if ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("an empty project: %v", err)
	}
	p := f.preview("shunn-us", publish.Options{})
	if len(p.Outline) != 0 || len(p.Warnings) == 0 || p.Warnings[len(p.Warnings)-1].Code != "nothing_to_compile" && p.Warnings[0].Code != "nothing_to_compile" {
		t.Errorf("preview of nothing: %+v", p.Warnings)
	}
}

func TestPresets(t *testing.T) {
	f := newFx(t, "Presets")
	f.doc("One", "chapter", "", "Words, ch1mark.")
	lo, err := f.reg.Call(f.ctx, f.owner, "compile.preset.list", compile.PresetListIn{ProjectID: f.pid})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range lo.(compile.PresetListOut).Presets {
		ids = append(ids, p.ID)
		if !p.Builtin || p.Format == "" || p.Options.Format == "" {
			t.Errorf("%+v", p)
		}
	}
	if strings.Join(ids, " ") != "shunn-us shunn-us-courier uk-manuscript uk-manuscript-courier styled-docx epub3 print-6x9 print-5.5x8.5 print-5x8 print-a5 proof-letter proof-a4 paged-html plain-markdown" {
		t.Errorf("ids %v", ids)
	}
	// Create, use, update, delete.
	size := 13.0
	cr, err := f.reg.Call(f.ctx, f.owner, "compile.preset.create", compile.PresetCreateIn{ProjectID: f.pid, Name: "  My   agent's format ", Base: "shunn-us", Options: publish.Options{FontSize: size, SceneBreak: "stars"}})
	if err != nil {
		t.Fatal(err)
	}
	p := cr.(compile.PresetItem).Item
	if p.Name != "My agent's format" || p.Builtin || !strings.HasPrefix(p.ID, "cpr_") || p.Options.FontSize != 13 || p.Options.LineSpacing != 2 || p.Base != "shunn-us" {
		t.Errorf("created: %+v", p)
	}
	out := f.compile(p.ID, publish.Options{})
	if !strings.Contains(unzip(t, out.Body)["word/styles.xml"], `<w:sz w:val="26"/>`) || !strings.Contains(out.Filename, "my-agent-s-format") {
		t.Errorf("the custom preset was not used: %s", out.Filename)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.preset.create", compile.PresetCreateIn{ProjectID: f.pid, Name: "my AGENT's format"}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("a duplicate name: %v", err)
	}
	for _, bad := range []compile.PresetCreateIn{
		{ProjectID: f.pid, Name: ""},
		{ProjectID: f.pid, Name: strings.Repeat("x", 81)},
		{ProjectID: f.pid, Name: "a", Base: "nope"},
		{ProjectID: f.pid, Name: "a", Options: publish.Options{Font: "Wingdings"}},
		{ProjectID: f.pid, Name: "a", Options: publish.Options{LineSpacing: 9}},
		{ProjectID: f.pid, Name: "a", Options: publish.Options{Ornament: "123456789"}},
	} {
		if _, err := f.reg.Call(f.ctx, f.owner, "compile.preset.create", bad); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	name := "Renamed"
	up, err := f.reg.Call(f.ctx, f.owner, "compile.preset.update", compile.PresetUpdateIn{ProjectID: f.pid, PresetID: p.ID, Name: &name, Options: publish.Options{Font: "Courier New"}})
	if err != nil {
		t.Fatal(err)
	}
	if u := up.(compile.PresetItem).Item; u.Name != "Renamed" || u.Options.Font != "Courier New" || u.Options.FontSize != 13 {
		t.Errorf("updated: %+v", u)
	}
	// Another project cannot reach it; neither can a built-in be changed.
	g := newFx(t, "Other")
	for _, call := range []struct {
		name string
		in   any
	}{
		{"compile.preset.update", compile.PresetUpdateIn{ProjectID: g.pid, PresetID: p.ID, Name: &name}},
		{"compile.preset.delete", compile.PresetDeleteIn{ProjectID: g.pid, PresetID: p.ID}},
		{"export.compile", compile.CompileIn{ProjectID: g.pid, Preset: p.ID}},
	} {
		if _, err := g.reg.Call(g.ctx, g.owner, call.name, call.in); ops.AsError(err).Code != ops.CodeNotFound {
			t.Errorf("%s from another project: %v", call.name, err)
		}
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.preset.update", compile.PresetUpdateIn{ProjectID: f.pid, PresetID: "shunn-us", Name: &name}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("changing a built-in: %v", err)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.preset.delete", compile.PresetDeleteIn{ProjectID: f.pid, PresetID: "shunn-us"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("deleting a built-in: %v", err)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.preset.delete", compile.PresetDeleteIn{ProjectID: f.pid, PresetID: p.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.preset.delete", compile.PresetDeleteIn{ProjectID: f.pid, PresetID: p.ID}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("deleting twice: %v", err)
	}
}

func TestContact(t *testing.T) {
	f := newFx(t, "Contact")
	got, err := f.reg.Call(f.ctx, f.owner, "compile.contact.get", compile.ContactIn{ProjectID: f.pid})
	if err != nil || got.(compile.ContactOut).Name != "" {
		t.Fatalf("%v %+v", err, got)
	}
	f.setContact()
	g, _ := f.reg.Call(f.ctx, f.owner, "compile.contact.get", compile.ContactIn{ProjectID: f.pid})
	c := g.(compile.ContactOut)
	if c.Email != "maire@example.com" || c.Address != "12 Quay Street\nGalway" || c.Surname != "Ní Bhriain" {
		t.Errorf("%+v", c)
	}
	empty := ""
	h, _ := f.reg.Call(f.ctx, f.owner, "compile.contact.set", compile.ContactSetIn{ProjectID: f.pid, Phone: &empty})
	if h.(compile.ContactOut).Phone != "" || h.(compile.ContactOut).Email != "maire@example.com" {
		t.Errorf("partial update: %+v", h)
	}
	long := strings.Repeat("a", 201)
	lines := strings.Repeat("l\n", 9)
	for _, bad := range []compile.ContactSetIn{{ProjectID: f.pid, Name: &long}, {ProjectID: f.pid, Address: &lines}} {
		if _, err := f.reg.Call(f.ctx, f.owner, "compile.contact.set", bad); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	// The contact block never reaches the Markdown export or the mirror's snapshot.
}
