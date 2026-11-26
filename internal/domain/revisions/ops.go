package revisions

import (
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/ops"
)

// Revision is one version of a document's body.
type Revision struct {
	ID          string `json:"id"`
	DocumentID  string `json:"document_id"`
	Revision    int    `json:"revision" desc:"The document revision number this text had."`
	Reason      string `json:"reason" enum:"autosave,manual,snapshot,restore,import,ai-accepted,alternative,suggestion-accepted,cut,split,join"`
	Label       string `json:"label"`
	Note        string `json:"note"`
	WordCount   int    `json:"word_count"`
	Delta       int    `json:"delta" desc:"Words added (positive) or removed since the previous version; 0 for the oldest."`
	AuthorID    string `json:"author_id,omitempty"`
	AuthorName  string `json:"author_name,omitempty"`
	CreatedAt   string `json:"created_at" desc:"When this text became the document's head. Show this."`
	RecordedAt  string `json:"recorded_at" desc:"When the version was stored. Differs from created_at for a version captured late."`
	Size        int    `json:"size" desc:"Bytes of Markdown."`
	Pinned      bool   `json:"pinned" desc:"Kept forever: a named snapshot or a labelled version."`
	Current     bool   `json:"current" desc:"The document's text right now is exactly this version."`
	ContentHash string `json:"content_hash,omitempty"`
}

// Head describes the document as it stands, for the top row of a history list.
type Head struct {
	Revision    int    `json:"revision"`
	WordCount   int    `json:"word_count"`
	ContentHash string `json:"content_hash"`
	UpdatedAt   string `json:"updated_at"`
	Saved       bool   `json:"saved" desc:"Some version holds exactly the current text. False means the current text is newer than every stored version."`
}

// ---- inputs and outputs ----------------------------------------------------------

// ListIn lists a document's versions, newest first.
type ListIn struct {
	ID     string `path:"id" json:"-" desc:"Document id."`
	Limit  int    `query:"limit" desc:"Versions per page, default 50, at most 200."`
	Before string `query:"before" desc:"Revision id to continue after: pass the previous page's next."`
	From   string `query:"from" desc:"Only versions created at or after this UTC time (RFC 3339, or YYYY-MM-DD). With to, this is how to ask for one day."`
	To     string `query:"to" desc:"Only versions created before this UTC time."`
	Reason string `query:"reason" desc:"Only versions with this reason; manual lists the named snapshots."`
}

// DocID lets the registry find the owning project.
func (in ListIn) DocID() string { return in.ID }

// ListOut is a page of versions.
type ListOut struct {
	Items []Revision `json:"items"`
	Next  string     `json:"next,omitempty" desc:"Pass as before to get the next page; empty on the last page."`
	Head  Head       `json:"head"`
}

// DaysIn asks which days have versions.
type DaysIn struct {
	ID        string `path:"id" json:"-" desc:"Document id."`
	UTCOffset int    `query:"utc_offset" desc:"Minutes east of UTC for the reader's day boundaries, for example -300. Default 0."`
	Limit     int    `query:"limit" desc:"Most recent days to return, default 30, at most 366."`
}

// DocID lets the registry find the owning project.
func (in DaysIn) DocID() string { return in.ID }

// Day is one calendar day with versions.
type Day struct {
	Day       string `json:"day" desc:"YYYY-MM-DD in the reader's offset."`
	Count     int    `json:"count"`
	LastID    string `json:"last_id" desc:"The day's last version: what Restore yesterday's chapter restores."`
	LastAt    string `json:"last_at"`
	WordCount int    `json:"word_count" desc:"Words in the day's last version."`
}

// DaysOut lists days, newest first.
type DaysOut struct {
	Days []Day `json:"days"`
}

// GetIn names one version.
type GetIn struct {
	ID         string `path:"id" json:"-" desc:"Document id."`
	RevisionID string `query:"revision_id" desc:"Revision id."`
}

// DocID lets the registry find the owning project.
func (in GetIn) DocID() string { return in.ID }

// GetOut is a version with its text.
type GetOut struct {
	Item    Revision `json:"item"`
	Content string   `json:"content" desc:"The Markdown of this version."`
}

// SnapshotIn takes a named snapshot of the document as it stands.
type SnapshotIn struct {
	ID    string `path:"id" json:"-" desc:"Document id."`
	Label string `json:"label,omitempty" desc:"A short name, for example 'Draft 2, after the editor call'."`
	Note  string `json:"note,omitempty"`
}

// DocID lets the registry find the owning project.
func (in SnapshotIn) DocID() string { return in.ID }

// Item wraps one revision.
type Item struct {
	Item Revision `json:"item"`
}

// LabelIn renames or annotates a version.
type LabelIn struct {
	ID         string  `path:"id" json:"-" desc:"Document id."`
	RevisionID string  `json:"revision_id"`
	Label      *string `json:"label,omitempty" desc:"A new label; empty clears it. A labelled version is kept forever."`
	Note       *string `json:"note,omitempty"`
}

// DocID lets the registry find the owning project.
func (in LabelIn) DocID() string { return in.ID }

// DeleteOut says a snapshot is gone.
type DeleteOut struct {
	Deleted bool `json:"deleted"`
}

// DiffIn compares two versions, or one version with the current text.
type DiffIn struct {
	ID   string `path:"id" json:"-" desc:"Document id."`
	From string `query:"from" desc:"The older revision id."`
	To   string `query:"to" desc:"The newer revision id; empty or 'current' for the document as it stands."`
	Unit string `query:"unit" desc:"word (default) shows changes inside a paragraph word by word; paragraph shows whole lines."`
}

// DocID lets the registry find the owning project.
func (in DiffIn) DocID() string { return in.ID }

// DiffOut is a diff plus what was compared.
type DiffOut struct {
	Segments []Segment `json:"segments" desc:"Equal, delete and insert runs in reading order. The equal and delete runs rebuild the older text; the equal and insert runs rebuild the newer."`
	Stats    Stats     `json:"stats"`
	Changed  bool      `json:"changed"`
	Coarse   bool      `json:"coarse" desc:"A size limit stopped some change being refined to words: it is shown as a larger delete then insert."`
	From     Revision  `json:"from"`
	To       *Revision `json:"to,omitempty" desc:"Absent when comparing with the current text."`
}

// PruneIn applies the retention policy to a project.
type PruneIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	DryRun    bool   `json:"dry_run,omitempty" desc:"Report what would be removed and remove nothing."`
}

// ProjectRef lets the registry authorize by project.
func (in PruneIn) ProjectRef() string { return in.ProjectID }

// ---- registration ---------------------------------------------------------------

// Register adds the revision ops that do not write a document body.
// revision.restore is registered by docs, which owns the save path.
func Register(r *ops.Registry) {
	via := docref.Via
	ops.Register(r, ops.Op{Name: "revision.list", Method: "GET", Path: "/api/v1/docs/{id}/revisions",
		Desc: "List a document's versions, newest first, with reason, label, words and word delta. Filter by from/to (one day) or reason.", Auth: via(ops.ProjectReader)}, list)
	ops.Register(r, ops.Op{Name: "revision.days", Method: "GET", Path: "/api/v1/docs/{id}/revisions/days",
		Desc: "List the days a document has versions, newest first, with each day's last version: three clicks to restore yesterday's chapter.", Auth: via(ops.ProjectReader)}, days)
	ops.Register(r, ops.Op{Name: "revision.get", Method: "GET", Path: "/api/v1/docs/{id}/revision",
		Desc: "Get one version of a document with its text.", Auth: via(ops.ProjectReader)}, get)
	ops.Register(r, ops.Op{Name: "revision.diff", Method: "GET", Path: "/api/v1/docs/{id}/revisions/diff",
		Desc: "Compare two versions, or a version with the current text, word by word or paragraph by paragraph.", Auth: via(ops.ProjectReader)}, diff)
	ops.Register(r, ops.Op{Name: "revision.snapshot", Method: "POST", Path: "/api/v1/docs/{id}/revisions", Status: 201,
		Desc: "Take a named snapshot of a document as it stands. Snapshots are kept forever.", Auth: via(ops.ProjectEditor)}, snapshot)
	ops.Register(r, ops.Op{Name: "revision.label", Method: "PATCH", Path: "/api/v1/docs/{id}/revision",
		Desc: "Rename or annotate a version. A labelled version is kept forever.", Auth: via(ops.ProjectEditor)}, label)
	ops.Register(r, ops.Op{Name: "revision.delete", Permanent: true, Method: "DELETE", Path: "/api/v1/docs/{id}/revision",
		Desc: "Delete a named snapshot. Automatic versions cannot be deleted; retention thins them.", Auth: via(ops.ProjectEditor)}, remove)
	ops.Register(r, ops.Op{Name: "revision.prune", Permanent: true, Method: "POST", Path: "/api/v1/projects/{id}/revisions/prune",
		Desc: "Apply the retention policy to a project's history now (it also runs on a schedule). Use dry_run to see the effect.", Auth: ops.RequireProject(ops.ProjectOwner)}, prune)
}

func validID(field, id string) error {
	if id == "" || len(id) > 64 {
		return ops.Invalid(field, "Name the version.")
	}
	return nil
}

// ---- list ------------------------------------------------------------------------

const revCols = `r.id, r.document_id, r.revision, r.reason, r.label, r.note, r.word_count, r.delta, COALESCE(r.author_id,''),
	COALESCE(NULLIF(u.display_name,''), u.email, ''), r.created_at, r.recorded_at, b.raw_size, r.content_hash = d.content_hash, r.content_hash`

// withDelta is every revision of one document with the word change since the
// version before it. The window needs the whole history to see the previous
// row, which is bounded by retention (a few thousand small rows at the worst).
const withDelta = `SELECT r.*, r.word_count - COALESCE(LAG(r.word_count) OVER (ORDER BY r.created_at, r.id), r.word_count) AS delta
	FROM document_revisions r WHERE r.document_id = ?`

func scanRevision(s interface{ Scan(...any) error }) (Revision, error) {
	var r Revision
	err := s.Scan(&r.ID, &r.DocumentID, &r.Revision, &r.Reason, &r.Label, &r.Note, &r.WordCount, &r.Delta, &r.AuthorID,
		&r.AuthorName, &r.CreatedAt, &r.RecordedAt, &r.Size, &r.Current, &r.ContentHash)
	r.Pinned = pinned(r.Reason, r.Label)
	return r, err
}

// parseBound accepts RFC 3339 (with or without milliseconds) or a bare date,
// and returns the stored-time form it sorts against.
func parseBound(field, s string) (string, error) {
	if s == "" {
		return "", nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return db.Format(t), nil
		}
	}
	return "", ops.Invalid(field, "Use an RFC 3339 time or YYYY-MM-DD.")
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = 50
	case limit > 200:
		limit = 200
	}
	from, err := parseBound("from", in.From)
	if err != nil {
		return ListOut{}, err
	}
	to, err := parseBound("to", in.To)
	if err != nil {
		return ListOut{}, err
	}
	if in.Reason != "" && !oneOf(reasons, in.Reason) {
		return ListOut{}, ops.Invalid("reason", "Unknown reason.")
	}
	q := `SELECT ` + revCols + ` FROM (` + withDelta + `) r
		JOIN documents d ON d.id = r.document_id
		JOIN revision_blobs b ON b.document_id = r.document_id AND b.content_hash = r.content_hash
		LEFT JOIN users u ON u.id = r.author_id WHERE 1=1`
	args := []any{in.ID}
	if from != "" {
		q += ` AND r.created_at >= ?`
		args = append(args, from)
	}
	if to != "" {
		q += ` AND r.created_at < ?`
		args = append(args, to)
	}
	if in.Reason != "" {
		q += ` AND r.reason = ?`
		args = append(args, in.Reason)
	}
	if in.Before != "" {
		var at string
		err := c.DB().Read().QueryRowContext(c, `SELECT created_at FROM document_revisions WHERE id = ? AND document_id = ?`, in.Before, in.ID).Scan(&at)
		if errors.Is(err, sql.ErrNoRows) {
			return ListOut{}, ops.Invalid("before", "That version is not in this document.")
		}
		if err != nil {
			return ListOut{}, err
		}
		q += ` AND (r.created_at < ? OR (r.created_at = ? AND r.id < ?))`
		args = append(args, at, at, in.Before)
	}
	q += ` ORDER BY r.created_at DESC, r.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := c.DB().Read().QueryContext(c, q, args...)
	if err != nil {
		return ListOut{}, err
	}
	defer rows.Close()
	out := ListOut{Items: []Revision{}}
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return ListOut{}, err
		}
		out.Items = append(out.Items, r)
	}
	if err := rows.Err(); err != nil {
		return ListOut{}, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Next = out.Items[limit-1].ID
	}
	var saved int
	err = c.DB().Read().QueryRowContext(c, `SELECT d.revision, d.word_count, d.content_hash, d.updated_at,
			EXISTS(SELECT 1 FROM revision_blobs b WHERE b.document_id = d.id AND b.content_hash = d.content_hash)
		FROM documents d WHERE d.id = ?`, in.ID).Scan(&out.Head.Revision, &out.Head.WordCount, &out.Head.ContentHash, &out.Head.UpdatedAt, &saved)
	out.Head.Saved = saved == 1
	return out, err
}

// ---- days ------------------------------------------------------------------------

func days(c *ops.Ctx, in DaysIn) (DaysOut, error) {
	if in.UTCOffset < -14*60 || in.UTCOffset > 14*60 {
		return DaysOut{}, ops.Invalid("utc_offset", "The offset is minutes east of UTC, within 14 hours.")
	}
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = 30
	case limit > 366:
		limit = 366
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT id, created_at, word_count FROM document_revisions WHERE document_id = ? ORDER BY created_at DESC, id DESC`, in.ID)
	if err != nil {
		return DaysOut{}, err
	}
	defer rows.Close()
	out := DaysOut{Days: []Day{}}
	shift := time.Duration(in.UTCOffset) * time.Minute
	for rows.Next() {
		var id, at string
		var words int
		if err := rows.Scan(&id, &at, &words); err != nil {
			return DaysOut{}, err
		}
		t, err := db.Parse(at)
		if err != nil {
			continue
		}
		day := t.Add(shift).Format("2006-01-02")
		if n := len(out.Days); n > 0 && out.Days[n-1].Day == day {
			out.Days[n-1].Count++
			continue
		}
		if len(out.Days) == limit {
			break
		}
		out.Days = append(out.Days, Day{Day: day, Count: 1, LastID: id, LastAt: at, WordCount: words})
	}
	return out, rows.Err()
}

// ---- get -------------------------------------------------------------------------

// one loads a single revision with its delta, author and current flag.
func one(c *ops.Ctx, docID, revID string) (Revision, error) {
	row := c.DB().Read().QueryRowContext(c, `SELECT `+revCols+` FROM (`+withDelta+`) r
		JOIN documents d ON d.id = r.document_id
		JOIN revision_blobs b ON b.document_id = r.document_id AND b.content_hash = r.content_hash
		LEFT JOIN users u ON u.id = r.author_id WHERE r.id = ?`, docID, revID)
	r, err := scanRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, ops.NotFound("revision")
	}
	return r, err
}

func get(c *ops.Ctx, in GetIn) (GetOut, error) {
	if err := validID("revision_id", in.RevisionID); err != nil {
		return GetOut{}, err
	}
	r, err := one(c, in.ID, in.RevisionID)
	if err != nil {
		return GetOut{}, err
	}
	_, content, err := Content(c.DB().Read(), in.ID, in.RevisionID)
	if err != nil {
		return GetOut{}, err
	}
	return GetOut{Item: r, Content: content}, nil
}

// ---- diff ------------------------------------------------------------------------

func diff(c *ops.Ctx, in DiffIn) (DiffOut, error) {
	if err := validID("from", in.From); err != nil {
		return DiffOut{}, err
	}
	if in.Unit != "" && in.Unit != UnitWord && in.Unit != UnitParagraph {
		return DiffOut{}, ops.Invalid("unit", "Use word or paragraph.")
	}
	fromRev, err := one(c, in.ID, in.From)
	if err != nil {
		return DiffOut{}, err
	}
	_, a, err := Content(c.DB().Read(), in.ID, in.From)
	if err != nil {
		return DiffOut{}, err
	}
	var out DiffOut
	var b string
	if in.To == "" || in.To == "current" {
		if err := c.DB().Read().QueryRowContext(c, `SELECT content FROM documents WHERE id = ?`, in.ID).Scan(&b); err != nil {
			return DiffOut{}, err
		}
	} else {
		toRev, err := one(c, in.ID, in.To)
		if err != nil {
			return DiffOut{}, err
		}
		_, b, err = Content(c.DB().Read(), in.ID, in.To)
		if err != nil {
			return DiffOut{}, err
		}
		out.To = &toRev
	}
	r := Diff(a, b, in.Unit)
	out.Segments, out.Stats, out.Changed, out.Coarse = r.Segments, r.Stats, r.Changed, r.Coarse
	out.From = fromRev
	return out, nil
}

// ---- writes ----------------------------------------------------------------------

func cleanLabel(s string) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if utf8.RuneCountInString(s) > maxLabel {
		return "", ops.Invalid("label", "Labels are limited to 120 characters.")
	}
	return s, nil
}

func cleanNote(s string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxNote {
		return "", ops.Invalid("note", "Notes are limited to 2,000 characters.")
	}
	return s, nil
}

func snapshot(c *ops.Ctx, in SnapshotIn) (Item, error) {
	label, err := cleanLabel(in.Label)
	if err != nil {
		return Item{}, err
	}
	note, err := cleanNote(in.Note)
	if err != nil {
		return Item{}, err
	}
	var id string
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var (
			projectID, content string
			rev, words         int
			deleted            sql.NullString
		)
		err := tx.QueryRow(`SELECT project_id, content, revision, word_count, deleted_at FROM documents WHERE id = ?`, in.ID).
			Scan(&projectID, &content, &rev, &words, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("document")
		}
		if err != nil {
			return err
		}
		if deleted.Valid {
			return ops.Conflict("This document is in the trash. Restore it before taking a snapshot.")
		}
		id, err = Record(tx, RecordIn{DocumentID: in.ID, Revision: rev, Content: content, Words: words, AuthorID: c.P.UserID,
			Reason: ReasonManual, Label: label, Note: note})
		if err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "revision.created", ProjectID: projectID, DocumentID: in.ID, Payload: map[string]any{"revision_id": id, "reason": ReasonManual, "words": words}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return Item{}, err
	}
	r, err := one(c, in.ID, id)
	return Item{Item: r}, err
}

func label(c *ops.Ctx, in LabelIn) (Item, error) {
	if err := validID("revision_id", in.RevisionID); err != nil {
		return Item{}, err
	}
	if in.Label == nil && in.Note == nil {
		return Item{}, ops.Invalid("label", "Send a label, a note, or both.")
	}
	var lab, note string
	var err error
	if in.Label != nil {
		if lab, err = cleanLabel(*in.Label); err != nil {
			return Item{}, err
		}
	}
	if in.Note != nil {
		if note, err = cleanNote(*in.Note); err != nil {
			return Item{}, err
		}
	}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var projectID, curLabel, curNote string
		err := tx.QueryRow(`SELECT d.project_id, r.label, r.note FROM document_revisions r JOIN documents d ON d.id = r.document_id
			WHERE r.id = ? AND r.document_id = ?`, in.RevisionID, in.ID).Scan(&projectID, &curLabel, &curNote)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("revision")
		}
		if err != nil {
			return err
		}
		if in.Label != nil {
			curLabel = lab
		}
		if in.Note != nil {
			curNote = note
		}
		if _, err := tx.Exec(`UPDATE document_revisions SET label = ?, note = ? WHERE id = ?`, curLabel, curNote, in.RevisionID); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "revision.labelled", ProjectID: projectID, DocumentID: in.ID, Payload: map[string]any{"revision_id": in.RevisionID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return Item{}, err
	}
	r, err := one(c, in.ID, in.RevisionID)
	return Item{Item: r}, err
}

func remove(c *ops.Ctx, in GetIn) (DeleteOut, error) {
	if err := validID("revision_id", in.RevisionID); err != nil {
		return DeleteOut{}, err
	}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var projectID, reason, hash string
		err := tx.QueryRow(`SELECT d.project_id, r.reason, r.content_hash FROM document_revisions r JOIN documents d ON d.id = r.document_id
			WHERE r.id = ? AND r.document_id = ?`, in.RevisionID, in.ID).Scan(&projectID, &reason, &hash)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("revision")
		}
		if err != nil {
			return err
		}
		if reason != ReasonManual {
			return ops.Conflict("Only named snapshots can be deleted. Automatic versions are thinned by retention.")
		}
		if _, err := tx.Exec(`DELETE FROM document_revisions WHERE id = ?`, in.RevisionID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM revision_blobs WHERE document_id = ? AND content_hash = ?
			AND NOT EXISTS (SELECT 1 FROM document_revisions WHERE document_id = ? AND content_hash = ?)`, in.ID, hash, in.ID, hash); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "revision.deleted", ProjectID: projectID, DocumentID: in.ID, Payload: map[string]any{"revision_id": in.RevisionID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return DeleteOut{}, err
	}
	return DeleteOut{Deleted: true}, nil
}

func prune(c *ops.Ctx, in PruneIn) (PruneResult, error) {
	return Prune(c, c.DB(), LoadPolicy(c, c.DB()), db.NowTime(), Scope{ProjectID: in.ProjectID}, in.DryRun)
}
