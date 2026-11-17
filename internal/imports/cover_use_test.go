package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/imports"
)

// An EPUB's cover picture may become the new project's own cover: the preview says so, apply with use_cover makes a
// layout of one front image from the picture the import stored (cleaned, flagged for the cover), and without it the
// project keeps the generated cover.
func TestEpubCoverCanBecomeTheProjectsCover(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := salt3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	if pv.Epub == nil || !pv.Epub.Cover.Kept || !pv.Epub.Cover.CanUse || pv.Epub.Cover.UseReason != "" {
		t.Fatalf("the preview does not offer the cover: %+v", pv.Epub)
	}
	// without use_cover: no layout
	plain := e.applyConv(pv, imports.ApplyIn{Name: "salt.epub", Epub: book})
	if plain.CoverUsed || count(t, e, `SELECT count(*) FROM cover_layouts`) != 0 {
		t.Fatalf("a cover was made without being asked: %+v", plain)
	}
	// with it
	pv2 := e.previewConv(imports.PreviewIn{Name: "salt2.epub", Epub: book})
	ap := e.applyConv(pv2, imports.ApplyIn{Name: "salt2.epub", Epub: book, UseCover: true})
	if !ap.CoverUsed || ap.ProjectID == "" {
		t.Fatalf("apply: %+v", ap)
	}
	got := e.must(e.w, "project.cover_layout.get", coverlayout.ProjectIn{ID: ap.ProjectID}).(coverlayout.Item).Layout
	if got == nil || got.Shape != "sides" || got.Front.Mode != "image" || got.Revision != 1 || got.Alt != "Cover of "+ap.Title || got.Back.Mode != "none" || got.FrontURL == "" {
		t.Fatalf("the layout: %+v", got)
	}
	var clean, refs int
	if err := e.d.Read().QueryRow(`SELECT a.cover_clean, (SELECT count(*) FROM attachment_refs r WHERE r.attachment_id = a.id AND r.ref_kind = 'cover') FROM attachments a WHERE a.id = ?`, got.Front.Attachment).Scan(&clean, &refs); err != nil {
		t.Fatal(err)
	}
	if clean != 1 || refs != 1 {
		t.Errorf("cover_clean %d, cover refs %d", clean, refs)
	}
	// the other project (without use_cover) still has none
	if n := count(t, e, `SELECT count(*) FROM cover_layouts WHERE project_id = '`+plain.ProjectID+`'`); n != 0 {
		t.Errorf("the plain import has a layout")
	}
	if strings.Contains(strings.Join(ap.Warnings, " "), "cover") {
		t.Errorf("warnings: %v", ap.Warnings)
	}
}

// A pushed repository or a mirror folder that holds a cover (the cover/ folder and the manifest's cover object) is
// imported as before: the manuscript comes in, the picture and the layout are left behind, and nothing is refused.
func TestAnImportFromAFolderWithACoverIsNotRefused(t *testing.T) {
	e := newEnv(t)
	png := pngBytes(30, 45, 4)
	manifest := `{"schema_version":2,"format":"scriobh-mirror","project":{"title":"The Salt Road","kind":"novel","language":"en"},` +
		`"documents":[{"path":"manuscript/01-opening.md","kind":"chapter","title":"Opening","status":"draft","include_in_compile":true}],` +
		`"cover":{"shape":"sides","trim":"6x9","front":{"mode":"image","file":"cover/front-aaaaaaaaaaaa.png"},"back":{"mode":"none"}}}`
	f := files(map[string]string{"scriobh.json": manifest, "manuscript/01-opening.md": "The harbour woke.\n", "cover/front-aaaaaaaaaaaa.png": string(png)})
	pv := e.preview(f, "")
	if pv.Layout != imports.LayoutScriobh || pv.Project.Title != "The Salt Road" {
		t.Fatalf("preview: %+v", pv)
	}
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "the-salt-road", Files: f}).(imports.ApplyOut)
	if out.Documents != 1 || out.CoverUsed {
		t.Errorf("apply: %+v", out)
	}
	if n := count(t, e, `SELECT count(*) FROM cover_layouts`); n != 0 {
		t.Errorf("a layout was made from a folder: %d", n)
	}
}
