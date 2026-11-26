package imports_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// A project to import into, with one document of the writer's own.
func (e *env) undoProject() string {
	e.t.Helper()
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "The Salt Road", Kind: "novel"}).(projects.Item).Item.ID
	e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Mine", Kind: "chapter", Content: "Written by me.\n"})
	return pid
}

func (e *env) appendFiles(pid string, f []imports.File) imports.ApplyOut {
	e.t.Helper()
	pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Name: "salt", Files: f}).(imports.PreviewOut)
	return e.must(e.w, "import.append", imports.AppendIn{ProjectID: pid, Name: "salt", Token: pv.Token, Files: f}).(imports.ApplyOut)
}

// docByTitle finds a live or trashed document of the project by its title.
func (e *env) docByTitle(pid, title string) (id string, trashed bool, rev int) {
	e.t.Helper()
	var del sql.NullString
	if err := e.d.Read().QueryRow(`SELECT id, deleted_at, revision FROM documents WHERE project_id = ? AND title = ?`, pid, title).Scan(&id, &del, &rev); err != nil {
		e.t.Fatalf("no document %q: %v", title, err)
	}
	return id, del.Valid, rev
}

func (e *env) saveDoc(id string, mut func(*docs.Doc)) {
	e.t.Helper()
	d := e.must(e.w, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
	mut(&d)
	e.must(e.w, "doc.update", docs.UpdateIn{ID: id, BaseRevision: d.Revision, Title: &d.Title, Content: &d.Content})
}

var undoFiles = map[string]string{
	"01-arrival.md":         "# Arrival\n\nThe bus left them at the crossroads.\n",
	"02-the-road.md":        "# The Road\n\nThey walked.\n",
	"03-the-mill.md":        "# The Mill\n\nThe wheel was still.\n",
	"04-snow.md":            "# Snow\n\nIt began in the night.\n",
	"05-part/01-inside.md":  "# Inside\n\nA room.\n",
	"05-part/02-outside.md": "# Outside\n\nA yard.\n",
}

// An import is taken back without touching what the writer has done since: a document saved since is kept (its text, its title, or even
// text put back as it was), one the writer added inside an imported part keeps the part, and nothing is deleted.
func TestUndoTrashesOnlyWhatIsUnedited(t *testing.T) {
	e := newEnv(t)
	pid := e.undoProject()
	ap := e.appendFiles(pid, files(undoFiles))
	if ap.Documents != 7 { // six files and the folder
		t.Fatalf("imported %d documents", ap.Documents)
	}
	id, _, _ := e.docByTitle(pid, "The Road")
	e.saveDoc(id, func(d *docs.Doc) { d.Content += "More words of mine.\n" }) // edited
	id, _, _ = e.docByTitle(pid, "The Mill")
	e.saveDoc(id, func(d *docs.Doc) { d.Title = "The Old Mill" }) // renamed
	id, _, _ = e.docByTitle(pid, "Snow")
	orig := e.must(e.w, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item.Content
	e.saveDoc(id, func(d *docs.Doc) { d.Content = "Changed.\n" })
	e.saveDoc(id, func(d *docs.Doc) { d.Content = orig }) // back to what was imported, but saved twice
	partID, _, _ := e.docByTitle(pid, "Part")
	if partID == "" {
		t.Fatal("no part")
	}
	e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, ParentID: partID, Title: "Added inside", Kind: "chapter", Content: "Mine too.\n"})

	list := e.must(e.w, "import.source.list", imports.SourceListIn{ProjectID: pid}).(imports.SourceListOut)
	if len(list.Items) != 1 || list.Items[0].ID != ap.SourceID || list.Items[0].Recorded != 7 || list.Items[0].Live != 7 {
		t.Fatalf("list: %+v", list)
	}
	// Arrival and the two inside the part are untouched; the part holds a document that is not the import's.
	if w := list.Items[0]; w.WouldTrash != 3 || w.WouldKeep != 4 || !w.CanUndo {
		t.Fatalf("what an undo would do: %+v", w)
	}

	out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut)
	if out.Trashed != 3 || out.Kept != 4 || out.Gone != 0 || out.Remaining != 4 {
		t.Fatalf("undo: %+v", out)
	}
	why := map[string]string{}
	for _, k := range out.KeptDocs {
		why[k.Title] = k.Why
	}
	if why["The Road"] != imports.KeptEdited || why["The Old Mill"] != imports.KeptEdited || why["Snow"] != imports.KeptEdited || why["Part"] != imports.KeptHolds || len(why) != 4 {
		t.Errorf("kept: %v", why)
	}
	for title, want := range map[string]bool{"Arrival": true, "Inside": true, "Outside": true, "The Road": false, "The Old Mill": false, "Snow": false, "Part": false, "Added inside": false, "Mine": false} {
		if _, trashed, _ := e.docByTitle(pid, title); trashed != want {
			t.Errorf("%s: in the trash is %v, want %v", title, trashed, want)
		}
	}
	// The text of what was kept is the writer's, byte for byte.
	id, _, _ = e.docByTitle(pid, "The Road")
	if got := e.must(e.w, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item.Content; !strings.Contains(got, "More words of mine.") {
		t.Errorf("edited text: %q", got)
	}
	// Saying it again trashes nothing more, and a document comes back with the restore that always existed.
	again := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut)
	if again.Trashed != 0 || again.Kept != 4 || again.Gone != 3 {
		t.Errorf("a second undo: %+v", again)
	}
	arr, _, _ := e.docByTitle(pid, "Arrival")
	e.must(e.w, "doc.restore", docs.DocIn{ID: arr})
	if _, trashed, _ := e.docByTitle(pid, "Arrival"); trashed {
		t.Error("a trashed document did not come back")
	}
}

// Every document of an untouched import goes, a part with its chapters in one trash, and the writer's own stay.
func TestUndoOfAnUntouchedImportTakesItAll(t *testing.T) {
	e := newEnv(t)
	pid := e.undoProject()
	ap := e.appendFiles(pid, files(undoFiles))
	out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut)
	if out.Trashed != 7 || out.Kept != 0 || out.Remaining != 0 {
		t.Fatalf("undo: %+v", out)
	}
	if _, trashed, _ := e.docByTitle(pid, "Mine"); trashed {
		t.Error("the writer's own document was trashed")
	}
	// Restoring the part brings back what that trash took of it.
	part, _, _ := e.docByTitle(pid, "Part")
	e.must(e.w, "doc.restore", docs.DocIn{ID: part})
	for _, title := range []string{"Part", "Inside", "Outside"} {
		if _, trashed, _ := e.docByTitle(pid, title); trashed {
			t.Errorf("%s is still in the trash", title)
		}
	}
	if _, trashed, _ := e.docByTitle(pid, "Arrival"); !trashed {
		t.Error("restoring the part brought back a document that was not inside it")
	}
}

// A Word document, whose chapters are made and not read from files, is taken back whole.
func TestUndoOfAWordImport(t *testing.T) {
	e := newEnv(t)
	pid := e.undoProject()
	data := miniDocx(t, map[string]string{"word/styles.xml": headingStyles, "word/document.xml": body(
		para("Heading1", "The Road") + para("", "Salt on the road.") + para("", "* * *") + para("", "After.") + para("Heading1", "The Mill") + para("", "Still."))})
	pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Name: "book.docx", Docx: data}).(imports.PreviewOut)
	if pv.Already != nil {
		t.Fatalf("a first import is flagged: %+v", pv.Already)
	}
	ap := e.must(e.w, "import.append", imports.AppendIn{ProjectID: pid, Name: "book.docx", Token: pv.Token, Docx: data}).(imports.ApplyOut)
	// The same file again, with another way of cutting it, is the same file.
	again := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Name: "book.docx", Docx: data, DocxSplit: imports.SplitNone}).(imports.PreviewOut)
	if again.Already == nil || again.Already.SourceID != ap.SourceID || again.Already.Documents != 2 || !again.Already.CanUndo {
		t.Fatalf("already imported: %+v", again.Already)
	}
	out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut)
	if out.Trashed != 2 || out.Kept != 0 {
		t.Fatalf("undo: %+v", out)
	}
	// With nothing of it left in the project, importing it again is not a repeat.
	if pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Name: "book.docx", Docx: data}).(imports.PreviewOut); pv.Already != nil {
		t.Errorf("flagged after an undo: %+v", pv.Already)
	}
}

// The warning is for the project that holds the file: another file, another project and a new project have none.
func TestAlreadyImportedIsPerProjectAndPerFile(t *testing.T) {
	e := newEnv(t)
	pid := e.undoProject()
	other := e.undoProject()
	f := files(undoFiles)
	ap := e.appendFiles(pid, f)
	prev := func(project string, f []imports.File) imports.PreviewOut {
		return e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: project, Name: "salt", Files: f}).(imports.PreviewOut)
	}
	if a := prev(pid, f).Already; a == nil || a.SourceID != ap.SourceID || a.Documents != 7 || a.ImportedAt == "" || a.Times != 0 {
		t.Errorf("the same folder: %+v", a)
	}
	if a := prev(other, f).Already; a != nil {
		t.Errorf("another project: %+v", a)
	}
	changed := files(map[string]string{"01-arrival.md": "# Arrival\n\nOther words.\n"})
	if a := prev(pid, changed).Already; a != nil {
		t.Errorf("another file: %+v", a)
	}
	if pv := e.preview(f, ""); pv.Already != nil {
		t.Errorf("a new project: %+v", pv.Already)
	}
	// Imported twice, it is two copies, and the warning counts them.
	e.appendFiles(pid, f)
	if a := prev(pid, f).Already; a == nil || a.Times != 2 || a.Documents != 7 {
		t.Errorf("twice: %+v", a)
	}
	// The preview's hash is what apply records.
	var hash string
	if err := e.d.Read().QueryRow(`SELECT source_hash FROM project_sources WHERE id = ?`, ap.SourceID).Scan(&hash); err != nil || hash == "" || hash != prev(pid, f).Token {
		t.Errorf("recorded hash %q %v", hash, err)
	}
}

// What an import wrote is what the hash of the stored text says, for every layout the importer reads, so an undo of an untouched import takes all.
func TestUndoKnowsWhatEveryLayoutWrote(t *testing.T) {
	e := newEnv(t)
	pid := e.undoProject()
	// CRLF, a byte order mark and alternatives: text that is not the bytes of the file once it is stored.
	f := files(map[string]string{
		"01-crlf.md":  "# Crlf\r\n\r\nOne.\r\n\r\nTwo.\r\n",
		"02-bom.md":   "\xef\xbb\xbf# Bom\n\nThree.\n",
		"03-plain.md": "# Plain\n\nFour.\n",
	})
	ap := e.appendFiles(pid, f)
	out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut)
	if out.Trashed != 3 || out.Kept != 0 {
		t.Fatalf("undo: %+v", out)
	}
}

// An import made before the state of what it wrote was recorded cannot be shown to have been left alone, whatever its text is: nothing of
// it is taken (if in doubt, keep it), a document that was saved since is said to be edited, and the rest "unknown".
func TestUndoOfAnImportFromBeforeTheStoredHash(t *testing.T) {
	e := newEnv(t)
	pid := e.undoProject()
	ap := e.appendFiles(pid, files(map[string]string{
		"01-lf.md":     "# Lf\n\nOne.\n", // the heading becomes the title: the text is not the file's
		"02-plain.md":  "Two.\n",
		"03-crlf.md":   "Three.\r\n",
		"04-edited.md": "Four.\n",
	}))
	legacy := func(project, source string) {
		if err := e.d.WithTx(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE document_sources SET stored_hash = '', state_hash = '', attach_hash = '' WHERE source_id = ?`, source)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	legacy(pid, ap.SourceID)
	id, _, _ := e.docByTitle(pid, "Edited")
	e.saveDoc(id, func(d *docs.Doc) { d.Content += "Mine.\n" })
	out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut)
	why := map[string]string{}
	for _, k := range out.KeptDocs {
		why[k.Title] = k.Why
	}
	if out.Trashed != 0 || out.Kept != 4 || why["Lf"] != imports.KeptUnsure || why["Plain"] != imports.KeptUnsure || why["Crlf"] != imports.KeptUnsure || why["Edited"] != imports.KeptEdited {
		t.Fatalf("undo: %+v", out)
	}
	if _, trashed, _ := e.docByTitle(pid, "Plain"); trashed {
		t.Error("a document of an import that did not record its state was taken")
	}
}

// Another project's import is not found, and a person who cannot edit the project cannot undo.
func TestUndoIsForTheProjectsEditors(t *testing.T) {
	e := newEnv(t)
	pid := e.undoProject()
	ap := e.appendFiles(pid, files(undoFiles))
	other := e.undoProject()
	if _, err := e.call(e.w, "import.undo", imports.UndoIn{ProjectID: other, SourceID: ap.SourceID}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("an import of another project: %v", err)
	}
	stranger := e.user("stranger", ops.RoleWriter)
	if _, err := e.call(stranger, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a stranger: %v", err)
	}
	if _, err := e.call(stranger, "import.source.list", imports.SourceListIn{ProjectID: pid}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a stranger's list: %v", err)
	}
	reader := e.user("reader", ops.RoleReader)
	if err := e.d.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, reader.UserID, ops.ProjectReader, db.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.call(reader, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reader: %v", err)
	}
	if _, err := e.call(reader, "import.source.list", imports.SourceListIn{ProjectID: pid}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reader's list: %v", err)
	}
	if _, trashed, _ := e.docByTitle(pid, "Arrival"); trashed {
		t.Error("a refused undo trashed a document")
	}
	// A dry run changes nothing.
	if _, err := e.reg.CallDry(t.Context(), e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, trashed, _ := e.docByTitle(pid, "Arrival"); trashed {
		t.Error("a dry run trashed a document")
	}
}

// Everything an import writes after inserting a document (an entity reference on a scene, alternatives, the suggestions and comments of a Word
// file) leaves the document at the revision the undo expects, so an untouched import is taken back whole whatever it carried.
func TestUndoOfAnUntouchedImportTakesAllWhateverItCarried(t *testing.T) {
	t.Run("entity references on scenes", func(t *testing.T) {
		e := newEnv(t)
		cmp := e.must(e.w, "compendium.create", compendiums.CreateIn{Name: "Bible"}).(compendiums.Item).Item.ID
		elara := e.must(e.w, "entity.create", entities.CreateIn{ID: cmp, Kind: "character", Name: "Elara"}).(entities.Item).Item.ID
		port := e.must(e.w, "entity.create", entities.CreateIn{ID: cmp, Kind: "place", Name: "Port Salt"}).(entities.Item).Item.ID
		pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Existing"}).(projects.Item).Item.ID
		e.must(e.w, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cmp})
		manifest := `{"schema_version":3,"project":{"title":"P"},"documents":[
			{"path":"manuscript/01-a.md","kind":"scene","title":"In scope","status":"draft","pov":"Elara","pov_entity":{"id":"` + elara + `","name":"Elara"},"location":"Port Salt","location_entity":{"id":"` + port + `"}},
			{"path":"manuscript/02-b.md","kind":"scene","title":"Plain","status":"draft"}]}`
		ap := e.appendFiles(pid, files(map[string]string{"project.json": manifest, "manuscript/01-a.md": "Elara came to Port Salt.\n", "manuscript/02-b.md": "Nothing.\n"}))
		var linked, revs int
		_ = e.d.Read().QueryRow(`SELECT count(*), sum(d.revision) FROM documents d JOIN document_sources ds ON ds.document_id = d.id WHERE ds.source_id = ? AND d.pov_entity_id IS NOT NULL`, ap.SourceID).Scan(&linked, &revs)
		if linked != 1 {
			t.Fatalf("the entity reference was not made (%d linked), so this proves nothing", linked)
		}
		if out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut); out.Trashed != 2 || out.Kept != 0 {
			t.Errorf("undo: %+v (the linked scene is at revision sum %d)", out, revs)
		}
	})
	t.Run("alternatives", func(t *testing.T) {
		e := newEnv(t)
		src := e.must(e.w, "project.create", projects.CreateIn{Title: "Alts Book"}).(projects.Item).Item.ID
		d := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: src, Title: "Chapter One", Content: altsText}).(docs.Item).Item
		e.must(e.w, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: d.Revision, Scope: "word", Quote: "quiet", Occurrence: new(int), Variants: []string{"still", "hushed"}})
		exp := e.must(e.w, "export.markdown", export.MarkdownIn{ProjectID: src, Mode: "bundle"}).(export.File)
		fs, err := imports.ReadZip(exp.Body)
		if err != nil {
			t.Fatal(err)
		}
		pid := e.undoProject()
		ap := e.appendFiles(pid, fs)
		var alts int
		_ = e.d.Read().QueryRow(`SELECT count(*) FROM alternatives a JOIN document_sources ds ON ds.document_id = a.document_id WHERE ds.source_id = ?`, ap.SourceID).Scan(&alts)
		if alts == 0 {
			t.Fatal("the alternatives were not imported, so this proves nothing")
		}
		if out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut); out.Kept != 0 || out.Trashed != ap.Documents {
			t.Errorf("undo: %+v of %d documents", out, ap.Documents)
		}
	})
	t.Run("tracked changes and comments of a Word file", func(t *testing.T) {
		e := newEnv(t)
		pid := e.undoProject()
		d1 := "2024-03-05T10:21:00Z"
		data := miniDocx(t, map[string]string{"word/styles.xml": headingStyles, "word/document.xml": body(
			para("Heading1", "The Road") +
				`<w:p><w:r><w:t xml:space="preserve">Salt on the </w:t></w:r><w:ins w:id="1" w:author="Ann" w:date="` + d1 + `"><w:r><w:t>wet </w:t></w:r></w:ins><w:r><w:t>road.</w:t></w:r></w:p>` +
				para("Heading1", "The Mill") + para("", "Still."))})
		pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Name: "book.docx", Docx: data}).(imports.PreviewOut)
		if pv.Suggestions == 0 {
			t.Fatal("no suggestion was made from the tracked change, so this proves nothing")
		}
		ap := e.must(e.w, "import.append", imports.AppendIn{ProjectID: pid, Name: "book.docx", Token: pv.Token, Docx: data}).(imports.ApplyOut)
		if ap.Suggestions == 0 {
			t.Fatal("no suggestion was written")
		}
		if out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut); out.Trashed != 2 || out.Kept != 0 {
			t.Errorf("undo: %+v", out)
		}
	})
}
