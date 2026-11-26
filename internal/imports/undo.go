package imports

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

// A project keeps a record of each import (project_sources) and, for every document an import made, the hash of the text it wrote
// (document_sources.stored_hash) and of the state it left the document in (state_hash, attach_hash: docstate.go). That is enough to take an
// import back without touching what the writer has done since: import.undo trashes the documents of one import that are exactly as it left
// them, and keeps every other. Nothing here deletes anything: the documents go to the project's trash, where doc.restore brings each back.
// Pictures, research notes, entities and the edition of an EPUB are not documents and stay.

// maxSourcesListed bounds the history: the most recent imports of a project.
const maxSourcesListed = 20

// maxKeptListed bounds the titles an undo names.
const maxKeptListed = 20

// Why a document was kept by an undo.
const (
	KeptEdited   = "edited"   // its text, title, synopsis or status was saved since the import
	KeptPlaced   = "placed"   // moved, put in another order, linked to an entity, marked, or anything else about the document itself that is not its text
	KeptAttached = "attached" // something hangs from it that the import did not make: a comment, a suggestion, an alternative, a tag, a timeline or share link, a site flag, a chapter marked done
	KeptOpen     = "open"     // it is open for writing in a tab now
	KeptHolds    = "contains" // untouched, but a document that is not from the import (or was kept) is inside it
	KeptUnsure   = "unknown"  // an import from before Scríobh recorded the state of what it wrote: it cannot be shown to be unchanged
)

// SourceListIn asks for a project's imports.
type SourceListIn struct {
	ProjectID string `path:"id" desc:"Project id."`
}

func (in SourceListIn) ProjectRef() string { return in.ProjectID }

// SourceItem is one import into a project, and what an undo of it would do now.
type SourceItem struct {
	ID         string    `json:"id" desc:"The import's id (project_sources): what import.undo takes and import.apply's source_id."`
	Kind       string    `json:"kind" desc:"What was imported: markdown, docx, epub, pdf or git."`
	Layout     string    `json:"layout,omitempty" desc:"How it was read (docx, pdf, epub, obsidian, scrivener, wordpress, ghost, scriobh, markdown-folder, pandoc)."`
	Name       string    `json:"name" desc:"What the file or folder was called; never a path."`
	ImportedAt string    `json:"imported_at"`
	Hash       string    `json:"hash,omitempty" desc:"The SHA-256 of the file (a folder: of every file's path and content) that import.preview says is already imported."`
	Recorded   int       `json:"documents_recorded" desc:"How many documents the import made."`
	Live       int       `json:"documents_live" desc:"How many of them are in the project now (not in the trash)."`
	WouldTrash int       `json:"would_trash" desc:"How many an undo would move to the trash now: as they were written, with nothing in them that is not the import's."`
	WouldKeep  int       `json:"would_keep" desc:"How many an undo would keep: saved since, or holding a document that is not the import's."`
	CanUndo    bool      `json:"can_undo" desc:"An undo would trash at least one document."`
	KeptDocs   []KeptDoc `json:"kept_documents,omitempty" desc:"The first 20 documents an undo would keep, and why."`
}

// SourceListOut is a project's imports, newest first.
type SourceListOut struct {
	Items []SourceItem `json:"items"`
	More  bool         `json:"more,omitempty" desc:"There are older imports than these."`
}

// UndoIn takes back one import.
type UndoIn struct {
	ProjectID string `path:"id" desc:"Project id."`
	SourceID  string `path:"source" desc:"The import's id: import.source.list's id, or the source_id import.apply and import.append answered."`
}

func (in UndoIn) ProjectRef() string { return in.ProjectID }

// KeptDoc names a document an undo kept.
type KeptDoc struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Why   string `json:"why" enum:"edited,placed,attached,open,contains,unknown" desc:"edited: its text, title, synopsis or status was saved since it was imported. placed: moved, reordered, linked to an entity or marked in some other way. attached: a comment, suggestion, alternative, tag, timeline or share link, site flag or done mark that the import did not make. open: open for writing now. contains: untouched, but a document that is not from this import is inside it. unknown: the import is from before Scríobh recorded the state of what it wrote, so it cannot be shown to be unchanged."`
}

// UndoOut says what an undo did.
type UndoOut struct {
	SourceID  string    `json:"source_id"`
	Trashed   int       `json:"trashed" desc:"Documents moved to the trash, with whatever was inside them from the import."`
	Kept      int       `json:"kept" desc:"Documents of the import that were not touched."`
	KeptDocs  []KeptDoc `json:"kept_documents,omitempty" desc:"The first 20 of them."`
	Gone      int       `json:"gone" desc:"Documents of the import that were already in the trash or deleted."`
	Recorded  int       `json:"documents_recorded"`
	Remaining int       `json:"remaining" desc:"What the project still holds of the import (live documents)."`
}

func registerUndo(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "import.source.list", Method: "GET", Path: "/api/v1/projects/{id}/imports",
		Desc: "The imports a project has had, newest first (the 20 most recent): what each was, when, how many documents it made and how many are still here, and what import.undo would do with it now.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, listSources)
	ops.Register(r, ops.Op{Name: "import.undo", Method: "POST", Path: "/api/v1/projects/{id}/imports/{source}/undo", UI: "POST /ui/projects/{id}/imports/{source}/undo",
		Desc: "Take back one import: move to the project's trash the documents it made that are exactly as it left them, and keep every other. A document is kept (and the answer says why: edited, placed, attached, open, contains or unknown) when its text, title, synopsis or status was saved since; when it was moved, put in another order, linked to an entity, excluded from the AI or changed in any other way that is not a save; when anything was attached to it that the import did not make (a comment, suggestion, alternative, ghost, tag, timeline link, share link, site setting, done mark, stash item or AI work); when a tab has it open for writing; when something that is not from the import is inside it; or when the import was made before the state of what it wrote was recorded. If there is any doubt it is kept. Pictures, research notes, entities and an EPUB's edition are not documents and stay. Nothing is deleted: doc.restore brings a document back. Repeating it trashes nothing more.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, undoImport)
}

// ---- what an undo would do ---------------------------------------------------------------------------------------

type undoDoc struct {
	id, parent, title string
	live              bool
	why               string // empty: untouched; else the reason it is kept
}

type undoPlan struct {
	recorded, gone int
	trash          []string // the roots to trash: each document with everything inside it
	trashed        int      // every document the roots hold
	kept           []KeptDoc
	keptTotal      int
	live           int
}

type rower interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// planUndo decides, from the database as it is now, which documents of one import an undo would take. A document is taken only when it
// is exactly as the import left it, and when nothing is open for writing in it: its text has the hash the import stored and it is at its
// first revision; every column a person can change and everything attached to it is as it was at the end of the import (docstate.go); and no
// tab holds it (leased). A document is also taken only when everything inside it is taken, so a chapter the writer added to an imported part is
// never trashed with the part. If there is any doubt the document is kept.
func planUndo(ctx context.Context, q rower, projectID, sourceID string, leased func(string) bool) (*undoPlan, error) {
	rows, err := q.QueryContext(ctx, `SELECT d.id, COALESCE(d.parent_id,''), d.title, d.deleted_at IS NULL,
			d.revision = 1, d.content_hash = ds.stored_hash AND d.content_hash <> '' AND ds.stored_hash <> '',
			ds.state_hash, ds.attach_hash
		FROM document_sources ds JOIN documents d ON d.id = ds.document_id
		WHERE ds.source_id = ? AND d.project_id = ? ORDER BY ds.source_order, d.id`, sourceID, projectID)
	if err != nil {
		return nil, err
	}
	type was struct {
		d               undoDoc
		first, text     bool
		stateH, attachH string
	}
	var ws []was
	for rows.Next() {
		var w was
		if err := rows.Scan(&w.d.id, &w.d.parent, &w.d.title, &w.d.live, &w.first, &w.text, &w.stateH, &w.attachH); err != nil {
			rows.Close()
			return nil, err
		}
		ws = append(ws, w)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var now map[string]docState
	for _, w := range ws {
		if w.d.live && w.stateH != "" {
			if now, err = docStates(ctx, q, sourceID); err != nil {
				return nil, err
			}
			break
		}
	}
	all := make([]undoDoc, len(ws))
	for i, w := range ws {
		d := w.d
		switch cur, have := now[d.id]; {
		case !d.live:
		case !w.first:
			d.why = KeptEdited
		case w.stateH == "" || !have:
			d.why = KeptUnsure
		case !w.text:
			d.why = KeptEdited
		case cur.cols != w.stateH:
			d.why = KeptPlaced
		case cur.attach != w.attachH:
			d.why = KeptAttached
		case leased != nil && leased(d.id):
			d.why = KeptOpen
		}
		all[i] = d
	}
	rows.Close()
	p := &undoPlan{recorded: len(all)}
	byID := make(map[string]*undoDoc, len(all))
	for i := range all {
		byID[all[i].id] = &all[i]
		if all[i].live {
			p.live++
		} else {
			p.gone++
		}
	}
	// What is inside each live document of the import, including what is not the import's: only the children of the import's own documents
	// are read, not the project's whole tree.
	kids := map[string][]string{}
	cr, err := q.QueryContext(ctx, `SELECT id, parent_id FROM documents WHERE project_id = ? AND deleted_at IS NULL
		AND parent_id IN (SELECT document_id FROM document_sources WHERE source_id = ?)`, projectID, sourceID)
	if err != nil {
		return nil, err
	}
	for cr.Next() {
		var id, parent string
		if err := cr.Scan(&id, &parent); err != nil {
			cr.Close()
			return nil, err
		}
		kids[parent] = append(kids[parent], id)
	}
	if err := cr.Err(); err != nil {
		cr.Close()
		return nil, err
	}
	cr.Close()
	// A document is taken when it is as written and every live document inside it is taken. Decided from the bottom up, without recursion
	// (a tree is never deep, but nothing here depends on that).
	const (
		unknown = iota
		visiting
		takenYes
		takenNo
	)
	state := map[string]int{}
	var decide func(id string) bool
	decide = func(root string) bool {
		type frame struct {
			id string
			i  int
		}
		stack := []frame{{root, 0}}
		state[root] = visiting
		for len(stack) > 0 {
			f := &stack[len(stack)-1]
			ks := kids[f.id]
			if f.i < len(ks) {
				k := ks[f.i]
				f.i++
				if state[k] == unknown {
					state[k] = visiting
					stack = append(stack, frame{k, 0})
				}
				continue
			}
			d := byID[f.id]
			ok := d != nil && d.live && d.why == ""
			for _, k := range ks {
				if state[k] != takenYes {
					ok = false
					break
				}
			}
			if ok {
				state[f.id] = takenYes
			} else {
				state[f.id] = takenNo
			}
			stack = stack[:len(stack)-1]
		}
		return state[root] == takenYes
	}
	for i := range all {
		if all[i].live {
			decide(all[i].id)
		}
	}
	for i := range all {
		d := &all[i]
		if !d.live {
			continue
		}
		if state[d.id] == takenYes {
			p.trashed++
			if state[d.parent] != takenYes { // a root of what is taken: a taken parent's trash takes it along
				p.trash = append(p.trash, d.id)
			}
			continue
		}
		p.keptTotal++
		if len(p.kept) < maxKeptListed {
			why := d.why
			if why == "" {
				why = KeptHolds
			}
			p.kept = append(p.kept, KeptDoc{ID: d.id, Title: d.title, Why: why})
		}
	}
	return p, nil
}

func listSources(c *ops.Ctx, in SourceListIn) (SourceListOut, error) {
	rows, err := c.DB().Read().QueryContext(c, `SELECT id, kind, source_path, source_hash, imported_at, metadata_json FROM project_sources
		WHERE project_id = ? ORDER BY imported_at DESC, id DESC LIMIT ?`, in.ProjectID, maxSourcesListed+1)
	if err != nil {
		return SourceListOut{}, ops.Internal(err)
	}
	var items []SourceItem
	for rows.Next() {
		var it SourceItem
		var meta string
		if err := rows.Scan(&it.ID, &it.Kind, &it.Name, &it.Hash, &it.ImportedAt, &meta); err != nil {
			rows.Close()
			return SourceListOut{}, ops.Internal(err)
		}
		var m struct {
			Layout string `json:"layout"`
		}
		_ = json.Unmarshal([]byte(meta), &m)
		it.Layout = m.Layout
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return SourceListOut{}, ops.Internal(err)
	}
	rows.Close()
	out := SourceListOut{Items: []SourceItem{}}
	if len(items) > maxSourcesListed {
		items, out.More = items[:maxSourcesListed], true
	}
	for _, it := range items {
		p, err := planUndo(c, c.DB().Read(), in.ProjectID, it.ID, leaseCheck(c))
		if err != nil {
			return SourceListOut{}, ops.Internal(err)
		}
		it.Recorded, it.Live, it.WouldTrash, it.WouldKeep = p.recorded, p.live, p.trashed, p.keptTotal
		it.CanUndo = p.trashed > 0
		it.KeptDocs = p.kept
		out.Items = append(out.Items, it)
	}
	return out, nil
}

func undoImport(c *ops.Ctx, in UndoIn) (UndoOut, error) {
	var out UndoOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		out = UndoOut{SourceID: in.SourceID}
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM project_sources WHERE id = ? AND project_id = ?`, in.SourceID, in.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ops.NotFound("import") // another project's import is not found either
		}
		p, err := planUndo(c, txRower{tx}, in.ProjectID, in.SourceID, leaseCheck(c))
		if err != nil {
			return err
		}
		out.Recorded, out.Gone, out.Kept, out.KeptDocs, out.Trashed = p.recorded, p.gone, p.keptTotal, p.kept, p.trashed
		out.Remaining = p.live - p.trashed
		if len(p.trash) == 0 {
			return nil
		}
		now := db.Now()
		if _, err := docs.TrashTx(tx, in.ProjectID, p.trash, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`, now, in.ProjectID); err != nil {
			return err
		}
		for _, id := range p.trash {
			c.Emit(ops.Event{Type: "doc.trashed", ProjectID: in.ProjectID, DocumentID: id})
		}
		c.Emit(ops.Event{Type: "import.undone", ProjectID: in.ProjectID, Payload: map[string]any{"trashed": p.trashed, "kept": p.keptTotal}})
		c.Audit(ops.AuditEntry{Action: "import.undo", TargetType: "project", TargetID: in.ProjectID,
			Detail: map[string]any{"source_id": in.SourceID, "trashed": p.trashed, "kept": p.keptTotal}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return UndoOut{}, err
	}
	return out, nil
}

type txRower struct{ tx *sql.Tx }

func (t txRower) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, q, args...)
}

// ---- already imported -------------------------------------------------------------------------------------------

// AlreadyImported says a project has had this same file before: the preview of an import into a project carries it when the file's SHA-256
// (a folder: the hash of every file's path and content) is that of an earlier import of the project that still has documents in it.
type AlreadyImported struct {
	SourceID   string `json:"source_id" desc:"The earlier import (import.source.list, import.undo)."`
	ImportedAt string `json:"imported_at"`
	Name       string `json:"name,omitempty" desc:"What it was called then."`
	Documents  int    `json:"documents" desc:"How many of its documents are in the project now."`
	Times      int    `json:"times,omitempty" desc:"How many earlier imports of the file the project has, when more than one."`
	CanUndo    bool   `json:"can_undo" desc:"import.undo would trash at least one of its documents."`
}

// srcHashOf is the hash an import records as its source: the file's own for a Word document, an EPUB, a PDF and the other single files,
// the preview token for a folder.
func (in *intake) srcHashOf() string {
	if in.srcKind != "" {
		return in.srcHash
	}
	return in.token
}

// alreadyImported looks for an earlier import of the same file into the project. It reads; a failure to read is no reason to refuse the
// preview, so it answers nothing. It runs on every round trip of a review, so what it asks is small: the live documents of each match are
// counted in SQL, and only the newest match that still has documents is looked at in full, to say whether an undo could take any.
func alreadyImported(c *ops.Ctx, projectID, hash string) *AlreadyImported {
	if hash == "" || projectID == "" {
		return nil
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT ps.id, ps.imported_at, ps.source_path,
			(SELECT count(*) FROM document_sources ds JOIN documents d ON d.id = ds.document_id WHERE ds.source_id = ps.id AND d.deleted_at IS NULL)
		FROM project_sources ps WHERE ps.project_id = ? AND ps.source_hash = ? ORDER BY ps.imported_at DESC, ps.id DESC LIMIT 20`, projectID, hash)
	if err != nil {
		return nil
	}
	var out *AlreadyImported
	for rows.Next() {
		var id, at, name string
		var live int
		if rows.Scan(&id, &at, &name, &live) != nil || live == 0 {
			continue // an import with nothing left in the project is not a copy to be warned of
		}
		if out == nil {
			out = &AlreadyImported{SourceID: id, ImportedAt: at, Name: name, Documents: live}
		}
		out.Times++
	}
	rows.Close()
	if out == nil {
		return nil
	}
	if out.Times == 1 {
		out.Times = 0
	}
	if p, err := planUndo(c, c.DB().Read(), projectID, out.SourceID, leaseCheck(c)); err == nil {
		out.CanUndo = p.trashed > 0
	}
	return out
}

// leaseCheck says whether a document is open for writing in a tab: a live lease of docs. Without the lease table (a build with no document
// ops) nothing is.
func leaseCheck(c *ops.Ctx) func(string) bool {
	l := docs.LeasesOf(c.Env)
	if l == nil {
		return nil
	}
	return l.Held
}

// ElsewhereImport says a file was imported into a project the caller can read, earlier: for a new project it is the same file again, and
// a second copy. Only a project the caller can read is ever named or counted.
type ElsewhereImport struct {
	ProjectID    string `json:"project_id"`
	Slug         string `json:"slug"`
	ProjectTitle string `json:"project_title"`
	ImportedAt   string `json:"imported_at"`
	Documents    int    `json:"documents" desc:"How many of that import's documents are in the project now."`
}

// importedElsewhere is the earlier import of the same file (the same SHA-256) into a project the caller can read, which still holds some of
// its documents. It is asked of a new project's preview only. Each candidate is first checked with the caller's role on it, the check every
// op makes (a project the caller cannot open, or one locked to them, is skipped), and only then is anything of it read.
func importedElsewhere(c *ops.Ctx, hash string) *ElsewhereImport {
	if hash == "" || c.P.UserID == "" || c.Env == nil || c.Env.ProjectRole == nil {
		return nil
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT ps.id, ps.project_id, p.slug, p.title, ps.imported_at
		FROM project_sources ps JOIN projects p ON p.id = ps.project_id
		WHERE ps.source_hash = ? AND p.deleted_at IS NULL ORDER BY ps.imported_at DESC, ps.id DESC LIMIT 200`, hash)
	if err != nil {
		return nil
	}
	type candidate struct{ sourceID, projectID, slug, title, at string }
	var cands []candidate
	for rows.Next() {
		var cd candidate
		if rows.Scan(&cd.sourceID, &cd.projectID, &cd.slug, &cd.title, &cd.at) == nil {
			cands = append(cands, cd)
		}
	}
	rows.Close()
	for _, cd := range cands {
		if _, ok := c.Env.ProjectRole(c, c.P.UserID, cd.projectID); !ok {
			continue
		}
		var live int
		if err := c.DB().Read().QueryRowContext(c, `SELECT count(*) FROM document_sources ds JOIN documents d ON d.id = ds.document_id
			WHERE ds.source_id = ? AND d.deleted_at IS NULL`, cd.sourceID).Scan(&live); err != nil || live == 0 {
			continue // an import with nothing left in the project is not a copy to be warned of
		}
		return &ElsewhereImport{ProjectID: cd.projectID, Slug: cd.slug, ProjectTitle: cd.title, ImportedAt: cd.at, Documents: live}
	}
	return nil
}
