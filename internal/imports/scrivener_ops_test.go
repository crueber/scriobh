package imports_test

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/text"
)

// bundle lists a fixture folder as the command line does.
func bundle(t testing.TB, root string) []imports.File {
	t.Helper()
	var out []imports.File
	err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, imports.File{Path: filepath.ToSlash(rel), Data: b, Size: int64(len(b))})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// zipOf zips a folder with the folder's own name as the wrapper, as a person does.
func zipDir(t testing.TB, root string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range bundle(t, root) {
		w, err := zw.Create(filepath.Base(root) + "/" + f.Path)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(f.Data)
	}
	zw.Close()
	return buf.Bytes()
}

func (e *env) previewConv(in imports.PreviewIn) imports.PreviewOut {
	e.t.Helper()
	return e.must(e.w, "import.preview", in).(imports.PreviewOut)
}

func (e *env) applyConv(pv imports.PreviewOut, in imports.ApplyIn) imports.ApplyOut {
	e.t.Helper()
	in.Token = pv.Token
	return e.must(e.w, "import.apply", in).(imports.ApplyOut)
}

func TestScrivener3ZipPreviewAndApply(t *testing.T) {
	e := newEnv(t)
	z := zipDir(t, "testdata/Salt Road.scriv")
	pv := e.previewConv(imports.PreviewIn{Name: "Salt Road.scriv.zip", Zip: z})
	if pv.Layout != imports.LayoutScrivener {
		t.Fatalf("a .scrivx was not recognised: layout %q", pv.Layout)
	}
	if pv.Project.Title != "The Salt Road" || pv.Project.Author != "Ada Fixture" {
		t.Errorf("project %+v", pv.Project)
	}
	eq(t, outline(pv.Nodes), []string{
		"front_matter:Front Matter",
		"  scene:Title Page",
		"part:Part One",
		"  chapter:The Road",
		"    scene:Dusk",
		"    scene:Dawn",
		"  chapter:The Wall",
		"    scene:Rain",
		"chapter:Epilogue",
		"back_matter:Acknowledgements",
		"  scene:Thanks",
	})
	if pv.ResearchTotal != 7 || len(pv.Research) != 7 {
		t.Fatalf("research: %d", pv.ResearchTotal)
	}
	cands := 0
	for _, r := range pv.Research {
		if r.Candidate != "" {
			cands++
		}
	}
	if cands != 2 {
		t.Errorf("entity candidates: %d", cands)
	}
	if pv.Report["scrivener"] == nil {
		t.Errorf("no report")
	}
	joined := strings.Join(pv.Warnings, "\n")
	for _, want := range []string{"Trash", "underlined", "Needs Courage", "annotation"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings lack %q:\n%s", want, joined)
		}
	}
	// A preview writes nothing.
	if n := rowCount(e, `SELECT count(*) FROM research_notes`); n != 0 {
		t.Errorf("preview wrote %d research notes", n)
	}

	ap := e.applyConv(pv, imports.ApplyIn{Name: "Salt Road.scriv.zip", Zip: z})
	if ap.Documents != 11 || ap.ResearchNotes != 7 || ap.Title != "The Salt Road" {
		t.Fatalf("apply %+v", ap)
	}
	// Statuses, the compile flag, synopsis and source path.
	var status, syn string
	var include int
	for title, want := range map[string]struct {
		status  string
		include int
		syn     string
	}{
		"Dusk":     {"draft", 1, "They arrive at the crossroads and Mara decides to go on."},
		"Dawn":     {"done", 0, "A short one."},
		"Epilogue": {"draft", 1, ""},
	} {
		if err := e.d.Read().QueryRow(`SELECT status, include_in_compile, synopsis_short FROM documents WHERE project_id = ? AND title = ?`, ap.ProjectID, title).Scan(&status, &include, &syn); err != nil {
			t.Fatal(err)
		}
		if status != want.status || include != want.include || syn != want.syn {
			t.Errorf("%s: %s %d %q", title, status, include, syn)
		}
	}
	var src string
	if err := e.d.Read().QueryRow(`SELECT ds.source_path FROM document_sources ds JOIN documents d ON d.id = ds.document_id WHERE d.title = 'Dusk' AND d.project_id = ?`, ap.ProjectID).Scan(&src); err != nil || src != "Draft/Front Matter/Part One/The Road/Dusk" && src != "Draft/Part One/The Road/Dusk" {
		t.Errorf("source path %q %v", src, err)
	}
	var kind, hash, meta string
	if err := e.d.Read().QueryRow(`SELECT kind, source_hash, metadata_json FROM project_sources WHERE project_id = ?`, ap.ProjectID).Scan(&kind, &hash, &meta); err != nil {
		t.Fatal(err)
	}
	if kind != "markdown" || hash == "" || !strings.Contains(meta, `"layout":"scrivener"`) || !strings.Contains(meta, `"scrivener"`) {
		t.Errorf("provenance: %s %q %s", kind, hash, meta)
	}

	// The text.
	dusk := e.docContent(ap.ProjectID, "Dusk")
	for _, want := range []string{"%%Check the timeline: is it still August?%%", "**Bold** words and *italic ones*", "“Salt is a kind of memory”.[^1]", "[[Dawn]] was coming", "[the salt page](https://example.org/salt?a=1&b=2)", `100% sure, \%\% not a note, \[\[not a link\]\]`, "[^1]: The crossroads is invented."} {
		if !strings.Contains(dusk, want) {
			t.Errorf("Dusk lacks %q:\n%s", want, dusk)
		}
	}
	// What is private never shows: not in the structure, the search text or the count.
	d := text.Structure(dusk, text.StructOptions{})
	plain := text.PlainText(d.Nodes)
	if strings.Contains(plain, "timeline") || strings.Contains(plain, "August") {
		t.Errorf("an annotation is visible: %q", plain)
	}
	if strings.Contains(text.SearchBody(dusk), "August") {
		t.Errorf("an annotation is searchable")
	}
	epi := e.docContent(ap.ProjectID, "Epilogue")
	if !strings.Contains(epi, "%%Notes: Consider cutting this. Maybe move it earlier%%") {
		t.Errorf("notes: %q", epi)
	}
	if got := text.PlainText(text.Structure(epi, text.StructOptions{}).Nodes); strings.Contains(got, "cutting") {
		t.Errorf("a document note is visible: %q", got)
	}
	// A wiki link exists in the index, so Dawn knows Dusk points at it.
	// Research.
	lo := e.must(e.w, "project.research.list", research.ListIn{ID: ap.ProjectID}).(research.ListOut)
	byTitle := map[string]research.Entry{}
	for _, n := range lo.Items {
		byTitle[n.Title] = n
	}
	if len(lo.Items) != 7 || byTitle["Mara"].ParentID != byTitle["Characters"].ID || byTitle["Characters"].Kind != "folder" {
		t.Errorf("research tree: %+v", lo.Items)
	}
	if got := strings.Join(byTitle["Mara"].Tags, ","); got != "protagonist" {
		t.Errorf("Mara's tags %q", got)
	}
	if got := strings.Join(byTitle["Timeline notes"].Tags, ","); got != "Idea,plot" && got != "plot,Idea" {
		t.Errorf("timeline tags %q", got)
	}
	// The research is searchable, and not a document.
	hits := e.must(e.w, "search.research", search.ResearchQueryIn{ID: ap.ProjectID, Q: "courier"}).(search.ResearchQueryOut)
	if hits.Total != 1 || hits.Items[0].Title != "Mara" {
		t.Errorf("the research is not searchable: %+v", hits)
	}
	if got := e.must(e.w, "search.query", search.QueryIn{ProjectID: ap.ProjectID, Q: "courier"}).(search.QueryOut); got.Total != 0 {
		t.Errorf("research notes appear among the manuscript's search results: %+v", got)
	}
	lod := e.must(e.w, "doc.list", docs.ListIn{ProjectID: ap.ProjectID}).(docs.ListOut)
	if len(lod.Items) != 11 {
		t.Errorf("documents: %d", len(lod.Items))
	}
}

func rowCount(e *env, q string, args ...any) int {
	var n int
	if err := e.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestScrivener2FilesListing(t *testing.T) {
	e := newEnv(t)
	files := bundle(t, "testdata/Old Book.scriv")
	pv := e.previewConv(imports.PreviewIn{Name: "Old Book.scriv", Files: files})
	if pv.Layout != imports.LayoutScrivener {
		t.Fatalf("layout %q", pv.Layout)
	}
	eq(t, outline(pv.Nodes), []string{"chapter:Chapter One", "chapter:Chapter Two"})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "Old Book.scriv", Files: files})
	if ap.Documents != 2 || ap.ResearchNotes != 1 || ap.Title != "Old Book" {
		t.Fatalf("%+v", ap)
	}
	var status string
	var include int
	if err := e.d.Read().QueryRow(`SELECT status, include_in_compile FROM documents WHERE project_id = ? AND title = 'Chapter Two'`, ap.ProjectID).Scan(&status, &include); err != nil || include != 0 {
		t.Errorf("chapter two: %q %d %v", status, include, err)
	}
	if got := e.docContent(ap.ProjectID, "Chapter Two"); !strings.Contains(got, "It ended in May.") || !strings.Contains(got, "%%Notes: Needs a better ending.%%") {
		t.Errorf("chapter two: %q", got)
	}
	// With the options: no research, no notes.
	pv2 := e.previewConv(imports.PreviewIn{Name: "Old Book.scriv", Files: files, ScrivResearch: "skip", ScrivNotes: "skip"})
	if pv2.ResearchTotal != 0 {
		t.Errorf("research kept: %d", pv2.ResearchTotal)
	}
}

// An edit can leave a research note, or a folder with what is in it, out; and a
// token from another source or other options is refused.
func TestScrivenerEditsAndToken(t *testing.T) {
	e := newEnv(t)
	files := bundle(t, "testdata/Salt Road.scriv")
	pv := e.previewConv(imports.PreviewIn{Name: "x", Files: files})
	var charsKey string
	for _, r := range pv.Research {
		if r.Title == "Characters" {
			charsKey = r.Key
		}
	}
	title := "Cast"
	pv2 := e.previewConv(imports.PreviewIn{Name: "x", Files: files, Edits: []imports.Edit{{Key: charsKey, Skip: true}, {Key: pv.Research[2].Key, Title: &title}}})
	if pv2.ResearchTotal != 5 || pv2.Research[1].Title != "Cast" && pv2.Research[0].Title != "Cast" {
		t.Errorf("edits: %d %+v", pv2.ResearchTotal, pv2.Research)
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "x", Files: files, Edits: []imports.Edit{{Key: charsKey, Skip: true}}})
	if ap.ResearchNotes != 5 {
		t.Errorf("research written: %d", ap.ResearchNotes)
	}
	// Another file set, with the old token.
	changed := bundle(t, "testdata/Salt Road.scriv")
	for i := range changed {
		if strings.HasSuffix(changed[i].Path, "000000000053/content.rtf") {
			changed[i].Data = bytes.ReplaceAll(changed[i].Data, []byte("two roads"), []byte("three roads"))
		}
	}
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "x", Files: changed}); err == nil {
		t.Errorf("a changed research note was accepted with the old token")
	}
}
