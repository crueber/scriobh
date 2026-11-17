package imports_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// F-375: a book added to a project that exists, as new chapters after what is there.
func TestEpubAppendsToAProject(t *testing.T) {
	e, dir := newEpubEnv(t)
	base := e.apply(files(map[string]string{"01-first.md": "# First\n\nMine.\n"}), "", nil)
	book := salt3(t)
	pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: base.ProjectID, Name: "salt.epub", Epub: book}).(imports.PreviewOut)
	if pv.Layout != imports.LayoutEpub || pv.Documents != 6 || pv.Epub == nil {
		t.Fatalf("preview: %q %d", pv.Layout, pv.Documents)
	}
	if n := count(t, e, `SELECT count(*) FROM editions`); n != 0 {
		t.Fatalf("the preview wrote %d editions", n)
	}
	ap := e.must(e.w, "import.append", imports.AppendIn{ProjectID: base.ProjectID, Token: pv.Token, Name: "salt.epub", Epub: book, EditionLabel: "Second life"}).(imports.ApplyOut)
	if ap.Documents != 6 || ap.EditionID == "" || ap.ProjectID != base.ProjectID {
		t.Fatalf("append: %+v", ap)
	}
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: base.ProjectID}).(docs.ListOut)
	if len(lo.Items) != 7 || lo.Items[0].Title != "First" {
		t.Errorf("documents: %d, first %q", len(lo.Items), lo.Items[0].Title)
	}
	var mine string
	_ = e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = 'First'`, base.ProjectID).Scan(&mine)
	if !strings.Contains(mine, "Mine.") {
		t.Errorf("an existing document was changed: %q", mine)
	}
	ed := e.must(e.w, "edition.list", editions.ProjectIn{ProjectID: base.ProjectID}).(editions.ListOut).Items
	if len(ed) != 1 || ed[0].Label != "Second life" || ed[0].Chapters != 6 || !ed[0].HasFile {
		t.Errorf("editions: %+v", ed)
	}
	// The same book again: another edition, one file, counted once.
	pv2 := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: base.ProjectID, Name: "salt.epub", Epub: book}).(imports.PreviewOut)
	e.must(e.w, "import.append", imports.AppendIn{ProjectID: base.ProjectID, Token: pv2.Token, Name: "salt.epub", Epub: book})
	if n := count(t, e, `SELECT count(*) FROM editions WHERE project_id = '`+base.ProjectID+`'`); n != 2 {
		t.Errorf("editions: %d", n)
	}
	files1, _ := filepath.Glob(filepath.Join(dir, "files", "projects", base.ProjectID, "editions", "*"))
	if len(files1) != 1 {
		t.Errorf("files: %v", files1)
	}
	var b, pics int64
	_ = e.d.Read().QueryRow(`SELECT bytes FROM file_usage WHERE scope = ?`, "owner:"+base.ProjectID).Scan(&b)
	_ = e.d.Read().QueryRow(`SELECT COALESCE(sum(bytes),0) FROM attachments WHERE project_id = ?`, base.ProjectID).Scan(&pics)
	if b != pics+int64(len(book)) {
		t.Errorf("usage %d, want %d + %d", b, pics, len(book))
	}
	// A reader cannot add a book, and a project in the trash takes none.
	reader := e.user("reader", ops.RoleReader)
	if _, err := e.call(reader, "import.append", imports.AppendIn{ProjectID: base.ProjectID, Token: pv.Token, Name: "x", Epub: book}); err == nil {
		t.Error("a stranger appended a book")
	}
	e.must(e.w, "project.trash", projects.StateIn{ID: base.ProjectID})
	if _, err := e.call(e.w, "import.append", imports.AppendIn{ProjectID: base.ProjectID, Token: pv.Token, Name: "salt.epub", Epub: book}); err == nil {
		t.Error("a book was added to a project in the trash")
	}
}

// The storage quota is a limit on keeping the file, too: a book that does not fit leaves nothing behind.
func TestEpubOverTheQuotaKeepsNothing(t *testing.T) {
	d := testutil.DB(t)
	dir := t.TempDir()
	reg := registry.BuildWith(d, events.NewStore(d, nil), dir, registry.Options{Attachments: attachments.Limits{MaxBytes: 25 << 20, OwnerBytes: 4000}})
	e := &env{t: t, d: d, reg: reg}
	e.w = e.user("writer", ops.RoleWriter)
	book := salt3(t) // about 7.6 KB
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	_, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "salt.epub", Epub: book})
	if err == nil || ops.AsError(err).Code != ops.CodeQuota {
		t.Fatalf("a book over the project's limit: %v", err)
	}
	if n := count(t, e, `SELECT (SELECT count(*) FROM editions) + (SELECT count(*) FROM projects) + (SELECT count(*) FROM attachments)`); n != 0 {
		t.Errorf("%d rows kept", n)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "files", "projects", "*", "editions", "*")); len(matches) > 0 {
		t.Errorf("files kept: %v", matches)
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "files", ".tmp")); len(ents) > 0 {
		t.Errorf("%d staged files left", len(ents))
	}
}

// A dry run keeps no file and no row.
func TestEpubApplyDryRun(t *testing.T) {
	e, dir := newEpubEnv(t)
	book := salt3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	res, err := e.reg.CallDry(t.Context(), e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "salt.epub", Epub: book})
	if err != nil {
		t.Fatal(err)
	}
	if res.Changes == 0 {
		t.Error("a dry run reported no changes")
	}
	if n := count(t, e, `SELECT (SELECT count(*) FROM editions) + (SELECT count(*) FROM projects) + (SELECT count(*) FROM attachments)`); n != 0 {
		t.Errorf("a dry run kept %d rows", n)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "files", "projects", "*", "*", "*")); len(matches) > 0 {
		t.Errorf("a dry run kept files: %v", matches)
	}
}
