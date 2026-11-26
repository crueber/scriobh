package imports_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/testutil"
)

// An undo takes only a document that is exactly as the import left it: not one that was moved, put in another order, linked to an entity,
// marked, commented on, tagged or otherwise attached to by anyone, and not one that is open for writing. "If in doubt, keep it."

var stateFiles = map[string]string{
	"01-alpha.md": "# Alpha\n\nThe first chapter, which is left alone.\n",
	"02-beta.md":  "# Beta\n\nThe second chapter, which is touched.\n",
	"03-gamma.md": "# Gamma\n\nThe third chapter, which is left alone.\n",
}

func (e *env) stateImport() (pid string, ap imports.ApplyOut) {
	e.t.Helper()
	pid = e.undoProject()
	return pid, e.appendFiles(pid, files(stateFiles))
}

// undoKeeps runs the undo and checks that Beta (and only Beta) was kept, for the reason.
func (e *env) undoKeeps(pid, source, why string) {
	e.t.Helper()
	out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: source}).(imports.UndoOut)
	if out.Trashed != 2 || out.Kept != 1 || len(out.KeptDocs) != 1 || out.KeptDocs[0].Title != "Beta" || out.KeptDocs[0].Why != why {
		e.t.Fatalf("undo: %+v (Beta should be the one kept, for %q)", out, why)
	}
	for title, want := range map[string]bool{"Alpha": true, "Beta": false, "Gamma": true, "Mine": false} {
		if _, trashed, _ := e.docByTitle(pid, title); trashed != want {
			e.t.Errorf("%s: in the trash is %v, want %v", title, trashed, want)
		}
	}
}

func (e *env) betaID(pid string) string {
	id, _, _ := e.docByTitle(pid, "Beta")
	return id
}

// The reviewer's three cases, and the other things that change a document without saving it.
func TestUndoKeepsADocumentThatWasOrganised(t *testing.T) {
	t.Run("moved out of its place under the writer's own document", func(t *testing.T) {
		e := newEnv(t)
		pid, ap := e.stateImport()
		mine, _, _ := e.docByTitle(pid, "Mine")
		e.must(e.w, "doc.move", docs.MoveIn{ID: e.betaID(pid), ParentID: mine})
		e.undoKeeps(pid, ap.SourceID, imports.KeptPlaced)
	})
	t.Run("moved to the top of its own list", func(t *testing.T) {
		e := newEnv(t)
		pid, ap := e.stateImport()
		after := ""
		e.must(e.w, "doc.move", docs.MoveIn{ID: e.betaID(pid), ParentID: "", After: &after})
		e.undoKeeps(pid, ap.SourceID, imports.KeptPlaced)
	})
	t.Run("a comment", func(t *testing.T) {
		e := newEnv(t)
		pid, ap := e.stateImport()
		e.attachRow(pid, "comments", e.betaID(pid))
		e.undoKeeps(pid, ap.SourceID, imports.KeptAttached)
	})
	t.Run("excluded from the AI", func(t *testing.T) {
		e := newEnv(t)
		pid, ap := e.stateImport()
		if err := e.d.WithTx(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE documents SET ai_exclude = 1 WHERE id = ?`, e.betaID(pid))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		e.undoKeeps(pid, ap.SourceID, imports.KeptPlaced)
	})
	t.Run("its entity links changed", func(t *testing.T) {
		e := newEnv(t)
		pid, ap := e.stateImport()
		if err := e.d.WithTx(t.Context(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE documents SET pov_entity_id = 'ent_made_by_the_writer' WHERE id = ?`, e.betaID(pid))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		e.undoKeeps(pid, ap.SourceID, imports.KeptPlaced)
	})
	t.Run("an untouched import, with a document of the writer's own beside it, is taken whole", func(t *testing.T) {
		e := newEnv(t)
		pid, ap := e.stateImport()
		out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut)
		if out.Trashed != 3 || out.Kept != 0 {
			t.Fatalf("undo: %+v", out)
		}
	})
}

// One test for each kind of thing that can hang from a document: a row of each table on the list, added after the import, keeps the document.
func TestUndoKeepsADocumentForEveryKindOfAttachment(t *testing.T) {
	for _, table := range imports.AttachTables() {
		table := table
		t.Run(table, func(t *testing.T) {
			e := newEnv(t)
			pid, ap := e.stateImport()
			e.attachRow(pid, table, e.betaID(pid))
			e.undoKeeps(pid, ap.SourceID, imports.KeptAttached)
		})
	}
}

// A new table that refers to a document has to be decided: on the list of what an undo compares, or on the list of what is derived, with
// the reason. This fails for one that is on neither, and for an entry that no longer refers to a document.
func TestEveryTableThatReferencesADocumentIsClassifiedForUndo(t *testing.T) {
	d := testutil.DB(t)
	refs := map[string]string{} // table -> a column that refers to a document
	tables, err := d.Read().QueryContext(t.Context(), `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		_ = tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	for _, n := range names {
		cols, err := d.Read().QueryContext(t.Context(), fmt.Sprintf(`SELECT name FROM pragma_table_info('%s')`, n))
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var c string
			_ = cols.Scan(&c)
			switch c {
			case "document_id", "doc_id", "source_document_id", "last_document_id":
				refs[n] = c
			}
		}
		cols.Close()
		fks, err := d.Read().QueryContext(t.Context(), fmt.Sprintf(`SELECT "from" FROM pragma_foreign_key_list('%s') WHERE "table" = 'documents'`, n))
		if err != nil {
			t.Fatal(err)
		}
		for fks.Next() {
			var c string
			_ = fks.Scan(&c)
			refs[n] = c
		}
		fks.Close()
	}
	listed := map[string]bool{}
	for _, a := range imports.AttachTables() {
		listed[a] = true
	}
	for tbl := range refs {
		if tbl == "documents" {
			continue
		}
		_, derived := imports.DerivedTables()[tbl]
		if !listed[tbl] && !derived {
			t.Errorf("%s refers to a document (%s) and is on neither list in docstate.go: decide whether an undo must see what it holds", tbl, refs[tbl])
		}
	}
	for tbl := range listed {
		if _, ok := refs[tbl]; !ok {
			t.Errorf("%s is on the list of attachments and does not refer to a document", tbl)
		}
	}
	for tbl := range imports.DerivedTables() {
		if _, ok := refs[tbl]; !ok {
			t.Errorf("%s is on the list of derived tables and does not refer to a document: take it off", tbl)
		}
	}
}

var genCount int

// attachRow inserts one row of a table that refers to a document, with whatever its parents need, as a person's act after the import.
func (e *env) attachRow(project, table, doc string) {
	e.t.Helper()
	if err := e.d.WithTx(e.t.Context(), func(tx *sql.Tx) error {
		_, err := e.genRow(tx, table, map[string]any{"document_id": doc, "source_document_id": doc, "doc_id": doc, "project_id": project, "user_id": e.w.UserID,
			"author_id": e.w.UserID, "actor_id": e.w.UserID, "created_by": e.w.UserID, "owner_id": e.w.UserID})
		return err
	}); err != nil {
		e.t.Fatalf("a row of %s: %v", table, err)
	}
}

// genRow inserts a row with a value for every column that must have one: what is in set, a row of the table a foreign key names, or a
// plain value. Columns with a check on their words are named in checks.
func (e *env) genRow(tx *sql.Tx, table string, set map[string]any) (map[string]any, error) {
	type col struct {
		name, typ string
		notnull   bool
		dflt      sql.NullString
		pk        int
	}
	rows, err := tx.Query(fmt.Sprintf(`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info('%s')`, table))
	if err != nil {
		return nil, err
	}
	var cols []col
	for rows.Next() {
		var c col
		var nn int
		if err := rows.Scan(&c.name, &c.typ, &nn, &c.dflt, &c.pk); err != nil {
			rows.Close()
			return nil, err
		}
		c.notnull = nn == 1
		cols = append(cols, c)
	}
	rows.Close()
	fk := map[string][2]string{}
	fr, err := tx.Query(fmt.Sprintf(`SELECT "from", "table", "to" FROM pragma_foreign_key_list('%s')`, table))
	if err != nil {
		return nil, err
	}
	for fr.Next() {
		var f, tbl, to string
		_ = fr.Scan(&f, &tbl, &to)
		fk[f] = [2]string{tbl, to}
	}
	fr.Close()
	vals := map[string]any{}
	for _, c := range cols {
		if v, ok := set[c.name]; ok {
			vals[c.name] = v
			continue
		}
		if v, ok := checks[table+"."+c.name]; ok {
			vals[c.name] = v
			continue
		}
		if !c.notnull && c.pk == 0 || c.dflt.Valid {
			continue
		}
		if f, ok := fk[c.name]; ok {
			parent, err := e.genRow(tx, f[0], set)
			if err != nil {
				return nil, err
			}
			vals[c.name] = parent[f[1]]
			continue
		}
		genCount++
		switch strings.ToUpper(c.typ) {
		case "INTEGER", "REAL", "NUMERIC":
			vals[c.name] = genCount
		case "BLOB":
			vals[c.name] = []byte("x")
		default:
			if c.name == "created_at" || c.name == "updated_at" || strings.HasSuffix(c.name, "_at") {
				vals[c.name] = db.Now()
			} else {
				vals[c.name] = fmt.Sprintf("gen_%s_%d", c.name, genCount)
			}
		}
	}
	var names, marks []string
	var args []any
	for _, c := range cols {
		if v, ok := vals[c.name]; ok {
			names, marks, args = append(names, `"`+c.name+`"`), append(marks, "?"), append(args, v)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf(`INSERT INTO %s(%s) VALUES (%s)`, table, strings.Join(names, ","), strings.Join(marks, ",")), args...); err != nil {
		return nil, fmt.Errorf("%s: %w", table, err)
	}
	return vals, nil
}

// checks are the columns whose words a check on the table limits.
var checks = map[string]any{
	"alternatives.scope": "word", "share_links.scope": "read", "ai_pending.kind": "edit", "ai_reviews.kind": "critique", "ai_marks.source": "model",
	"ai_runs.status": "ok", "editions.kind": "epub",
}

// A document a tab holds the lease of is open for writing: the undo does not take it from under the writer, and says so. When the tab lets
// go, it is taken like any other.
func TestUndoKeepsADocumentThatIsOpenForWriting(t *testing.T) {
	e := newEnv(t)
	pid, ap := e.stateImport()
	e.must(e.w, "doc.lease", docs.LeaseIn{ID: e.betaID(pid), TabID: "tab-1"})
	list := e.must(e.w, "import.source.list", imports.SourceListIn{ProjectID: pid}).(imports.SourceListOut)
	if it := list.Items[0]; it.WouldTrash != 2 || it.WouldKeep != 1 || len(it.KeptDocs) != 1 || it.KeptDocs[0].Why != imports.KeptOpen {
		t.Fatalf("the summary says what an undo would do: %+v", it)
	}
	e.undoKeeps(pid, ap.SourceID, imports.KeptOpen)
	e.must(e.w, "doc.lease", docs.LeaseIn{ID: e.betaID(pid), TabID: "tab-1", Release: true})
	out := e.must(e.w, "import.undo", imports.UndoIn{ProjectID: pid, SourceID: ap.SourceID}).(imports.UndoOut)
	if out.Trashed != 1 || out.Kept != 0 {
		t.Errorf("after the tab let go: %+v", out)
	}
}
