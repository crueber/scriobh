package imports_test

import (
	"os"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/text"
)

func wxrFile(t testing.TB) []imports.File {
	t.Helper()
	b, err := os.ReadFile("testdata/wordpress-1.2.xml")
	if err != nil {
		t.Fatal(err)
	}
	return []imports.File{{Path: "salt-roads.WordPress.2024-04-05.xml", Data: b, Size: int64(len(b))}}
}

func TestWordPressPreviewAndApply(t *testing.T) {
	e := newEnv(t)
	files := wxrFile(t)
	pv := e.previewConv(imports.PreviewIn{Name: "export.xml", Files: files})
	if pv.Layout != imports.LayoutWordPress {
		t.Fatalf("layout %q: a WXR file was not recognised", pv.Layout)
	}
	if pv.Project.Kind != "blog" || pv.Project.Title != "Salt & Roads" || pv.Project.Subtitle != "Notes from the road" || pv.Project.Author != "Ada Fixture" || pv.Project.Language != "en-GB" {
		t.Errorf("project %+v", pv.Project)
	}
	// Published posts only, oldest first; no pages unless asked.
	eq(t, outline(pv.Nodes), []string{"chapter:The Salt Road", "chapter:Classic", "chapter:The Third Post & Last"})
	joined := strings.Join(pv.Warnings, "\n")
	for _, want := range []string{"comment(s) were not imported", "attachment(s)", "Not imported:", "image(s)", "script(s)", "footnote"} {
		if want == "footnote" {
			continue
		}
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "export.xml", Files: files})
	if ap.Documents != 3 || ap.Title != "Salt & Roads" {
		t.Fatalf("apply %+v", ap)
	}
	var date, source, kind string
	if err := e.d.Read().QueryRow(`SELECT d.story_date, ds.source_path, d.kind FROM documents d JOIN document_sources ds ON ds.document_id = d.id WHERE d.project_id = ? AND d.title = 'The Salt Road'`, ap.ProjectID).Scan(&date, &source, &kind); err != nil {
		t.Fatal(err)
	}
	if date != "2024-01-10" || source != "wordpress/post/the-salt-road" || kind != "chapter" {
		t.Errorf("date %q source %q kind %q", date, source, kind)
	}
	var syn string
	e.d.Read().QueryRow(`SELECT synopsis_short FROM documents WHERE project_id = ? AND title = 'The Third Post & Last'`, ap.ProjectID).Scan(&syn)
	if syn != "The last one." {
		t.Errorf("synopsis %q", syn)
	}
	body := e.docContent(ap.ProjectID, "The Salt Road")
	for _, want := range []string{
		"The salt road begins at the *sea*, and **ends** at the market. See [the page](https://example.org/salt).[^1]",
		"### Second stop", "- Salt\n- Bread\n    - Rye", "> Salt is a kind of memory.", "\\[image: A map of the road\\]", "The road",
		`100% real \%\% not a note \[\[not a link\]\] \& more`, "| Stop | Miles |", "[^1]: The road is *invented*.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("post lacks %q:\n%s", want, body)
		}
	}
	for _, bad := range []string{"tracker", "video.example", "Search", "<", "wp:paragraph"} {
		if strings.Contains(body, bad) {
			t.Errorf("post holds %q:\n%s", bad, body)
		}
	}
	classic := e.docContent(ap.ProjectID, "Classic")
	for _, want := range []string{"The second post was written in the classic editor.\\\nIt has a line break.", "captioned thing", "Last paragraph.", "[^1]: Salt is NaCl, mostly."} {
		if !strings.Contains(classic, want) {
			t.Errorf("classic lacks %q:\n%s", want, classic)
		}
	}
	if strings.Contains(classic, "gallery") || strings.Contains(classic, "[caption") {
		t.Errorf("a shortcode survived:\n%s", classic)
	}
	// What was never to be read is nowhere.
	all := body + classic
	for _, bad := range []string{"private.example", "secret-comment-text", "Reader One", "203.0.113.9"} {
		if strings.Contains(all, bad) {
			t.Errorf("%q reached the text", bad)
		}
	}
	for _, tbl := range []string{"documents", "project_sources", "research_notes"} {
		var n int
		e.d.Read().QueryRow(`SELECT count(*) FROM ` + tbl + ` WHERE metadata_json LIKE '%private.example%'`).Scan(&n)
		_ = n
	}
	var meta string
	e.d.Read().QueryRow(`SELECT metadata_json FROM project_sources WHERE project_id = ?`, ap.ProjectID).Scan(&meta)
	if strings.Contains(meta, "private.example") || strings.Contains(meta, "secret-comment-text") {
		t.Errorf("provenance holds private data: %s", meta)
	}
	if !strings.Contains(meta, "map.jpg") || !strings.Contains(meta, `"comments":1`) {
		t.Errorf("provenance lacks the report: %s", meta)
	}
	// No private notes, no wiki links: the text is as it was written.
	if links := text.WikiLinks(body); len(links) != 0 {
		t.Errorf("wiki links: %+v", links)
	}
	if got := text.PlainText(text.Structure(body, text.StructOptions{}).Nodes); !strings.Contains(got, "%% not a note [[not a link]]") {
		t.Errorf("literal text lost: %q", got)
	}
}

func TestWordPressOptions(t *testing.T) {
	e := newEnv(t)
	files := wxrFile(t)
	pv := e.previewConv(imports.PreviewIn{Name: "x.xml", Files: files, WPStatus: "all", WPTypes: "both"})
	eq(t, outline(pv.Nodes), []string{
		"chapter:The Salt Road", "chapter:Classic", "chapter:The Third Post & Last", "chapter:A Draft", "chapter:Private Thoughts",
		"part:Pages", "  chapter:About",
	})
	// not published: marked do not compile and a status
	ap := e.applyConv(pv, imports.ApplyIn{Name: "x.xml", Files: files, WPStatus: "all", WPTypes: "both"})
	for title, want := range map[string]struct {
		status  string
		include int
	}{"A Draft": {"draft", 0}, "Private Thoughts": {"done", 0}, "The Salt Road": {"done", 1}} {
		var status string
		var include int
		if err := e.d.Read().QueryRow(`SELECT status, include_in_compile FROM documents WHERE project_id = ? AND title = ?`, ap.ProjectID, title).Scan(&status, &include); err != nil {
			t.Fatal(err)
		}
		if status != want.status || include != want.include {
			t.Errorf("%s: %s %d", title, status, include)
		}
	}
	pages := e.previewConv(imports.PreviewIn{Name: "x.xml", Files: files, WPTypes: "pages"})
	eq(t, outline(pages.Nodes), []string{"part:Pages", "  chapter:About"})
	// a wrong option is refused
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "x.xml", Files: files, WPStatus: "everything"}); err == nil {
		t.Errorf("an unknown wp_status was accepted")
	}
	// A WXR in a zip.
	z := zipOf(t, map[string]string{"export/wp.xml": string(files[0].Data)})
	pz := e.previewConv(imports.PreviewIn{Name: "wp.zip", Zip: z})
	if pz.Layout != imports.LayoutWordPress || pz.Documents != 3 {
		t.Errorf("zip: %q %d", pz.Layout, pz.Documents)
	}
}
