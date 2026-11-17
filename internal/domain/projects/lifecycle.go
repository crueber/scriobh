package projects

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Statuses mirror the CHECK constraint in migration 0200 (F-024).
var Statuses = []string{"idea", "outlining", "drafting", "revising", "finished", "published"}

const (
	maxLine        = 200  // title, subtitle, author
	maxGenre       = 100  // genre
	maxDescription = 5000 // description, in characters
	maxWordGoal    = 10_000_000
)

func registerLifecycle(r *ops.Registry) {
	if env := r.Env(); env != nil && env.DataDir != "" {
		env.AddPurgeHook(removeConflicts(env.DataDir))
	}
	owner := ops.RequireProject(ops.ProjectOwner)
	ops.Register(r, ops.Op{
		Name: "project.update", Method: "PATCH", Path: "/api/v1/projects/{id}",
		Desc: "Change project details. Send only the fields to change; an empty body changes nothing. The slug and URL stay the same when the title changes.",
		Auth: owner,
	}, update)
	ops.Register(r, ops.Op{
		Name: "project.trash", Method: "POST", Path: "/api/v1/projects/{id}/trash",
		Desc: "Move a project to the trash. Owner only; nothing is deleted. Trashing a trashed project does nothing.", Auth: owner,
	}, transition("project.trash", "trashed_at", true, "project.trashed"))
	ops.Register(r, ops.Op{
		Name: "project.restore", Method: "POST", Path: "/api/v1/projects/{id}/restore",
		Desc: "Take a project out of the trash. Owner only; restoring a live project does nothing.", Auth: owner,
	}, transition("project.restore", "trashed_at", false, "project.restored"))
	ops.Register(r, ops.Op{
		Name: "project.archive", Method: "POST", Path: "/api/v1/projects/{id}/archive",
		Desc: "Archive a project: hidden from the default library, still fully readable. Owner only; archiving an archived project does nothing.", Auth: owner,
	}, transition("project.archive", "archived_at", true, "project.archived"))
	ops.Register(r, ops.Op{
		Name: "project.unarchive", Method: "POST", Path: "/api/v1/projects/{id}/unarchive",
		Desc: "Bring an archived project back to the library. Owner only; does nothing when it is not archived.", Auth: owner,
	}, transition("project.unarchive", "archived_at", false, "project.unarchived"))
	ops.Register(r, ops.Op{
		Name: "project.purge", Permanent: true, Method: "DELETE", Path: "/api/v1/projects/{id}",
		Desc: "Permanently delete a project and everything in it. This is the only hard delete, and it cannot be undone. " +
			"Owner only; the project must already be in the trash, and confirm_title must equal its title exactly.",
		Auth: owner,
	}, purge)
	ops.Register(r, ops.Op{
		Name: "project.pin", Method: "POST", Path: "/api/v1/projects/{id}/pin",
		Desc: "Pin or unpin a project on the caller's own library shelf. Per person: other members are not affected.",
		Auth: ops.RequireProject(ops.ProjectReader),
	}, pin)
	ops.Register(r, ops.Op{
		Name: "project.open", Method: "POST", Path: "/api/v1/projects/{id}/open",
		Desc: "Record that the caller opened the project, and optionally which document they are in, so the library can offer Continue where I left off. Per person.",
		Auth: ops.RequireProject(ops.ProjectReader),
	}, open)
}

// ---- update ---------------------------------------------------------------

// UpdateIn changes project details. A nil field is left alone.
type UpdateIn struct {
	ID          string  `path:"id" json:"-" desc:"Project id."`
	Title       *string `json:"title,omitempty" desc:"Up to 200 characters; cannot be empty."`
	Subtitle    *string `json:"subtitle,omitempty" desc:"Up to 200 characters."`
	AuthorName  *string `json:"author_name,omitempty" desc:"Up to 200 characters."`
	Genre       *string `json:"genre,omitempty" desc:"Up to 100 characters."`
	Language    *string `json:"language,omitempty" desc:"BCP 47 tag such as en or ga."`
	Status      *string `json:"status,omitempty" enum:"idea,outlining,drafting,revising,finished,published"`
	Description *string `json:"description,omitempty" desc:"Up to 5,000 characters."`
	WordGoal    *int    `json:"word_goal,omitempty" desc:"Target word count, 0 to 10,000,000; 0 for none."`
	Deadline    *string `json:"deadline,omitempty" desc:"YYYY-MM-DD; an empty string clears it."`
	CoverColor  *string `json:"cover_color,omitempty" desc:"One of the fixed cover palette colours."`
}

// ProjectRef lets the registry authorize by membership.
func (in UpdateIn) ProjectRef() string { return in.ID }

// Validate trims and checks every field that was sent.
func (in *UpdateIn) Validate() error {
	if err := line(&in.Title, "title", maxLine, true); err != nil {
		return err
	}
	if err := line(&in.Subtitle, "subtitle", maxLine, false); err != nil {
		return err
	}
	if err := line(&in.AuthorName, "author_name", maxLine, false); err != nil {
		return err
	}
	if err := line(&in.Genre, "genre", maxGenre, false); err != nil {
		return err
	}
	if in.Language != nil {
		l := strings.TrimSpace(*in.Language)
		if l == "" || len(l) > 16 || strings.ContainsAny(l, " <>\"'") {
			return ops.Invalid("language", "Use a language tag such as en or ga.")
		}
		in.Language = &l
	}
	if in.Status != nil && !contains(Statuses, *in.Status) {
		return ops.Invalid("status", "Status must be one of: "+strings.Join(Statuses, ", ")+".")
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if utf8.RuneCountInString(d) > maxDescription {
			return ops.Invalid("description", "The description is limited to 5,000 characters.")
		}
		in.Description = &d
	}
	if in.WordGoal != nil && (*in.WordGoal < 0 || *in.WordGoal > maxWordGoal) {
		return ops.Invalid("word_goal", "The word goal must be between 0 and 10,000,000.")
	}
	if in.Deadline != nil {
		d := strings.TrimSpace(*in.Deadline)
		if d != "" {
			t, err := time.Parse("2006-01-02", d)
			if err != nil || t.Format("2006-01-02") != d {
				return ops.Invalid("deadline", "Use a date as YYYY-MM-DD, or leave it empty for none.")
			}
		}
		in.Deadline = &d
	}
	if in.CoverColor != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.CoverColor))
		if !contains(CoverPalette, c) {
			return ops.Invalid("cover_color", "Pick a colour from the cover palette.")
		}
		in.CoverColor = &c
	}
	return nil
}

// line trims a single-line field and rejects control characters (a pasted
// newline in a title would break lists, filenames and the mirror).
func line(p **string, field string, max int, required bool) error {
	if *p == nil {
		return nil
	}
	v := strings.TrimSpace(**p)
	switch {
	case v == "" && required:
		return ops.Invalid(field, "This cannot be empty.")
	case utf8.RuneCountInString(v) > max:
		return ops.Invalid(field, "This is limited to "+itoa(max)+" characters.")
	case strings.IndexFunc(v, unicode.IsControl) >= 0:
		return ops.Invalid(field, "This cannot contain line breaks or control characters.")
	}
	*p = &v
	return nil
}

func update(c *ops.Ctx, in UpdateIn) (Item, error) {
	var out Project
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cur, err := loadProject(c, tx, c.P.UserID, in.ID)
		if err != nil {
			return err
		}
		sets, args, changed := []string{}, []any{}, []string{}
		set := func(field, column string, differs bool, value any) {
			if differs {
				sets = append(sets, column+" = ?")
				args = append(args, value)
				changed = append(changed, field)
			}
		}
		// The slug is deliberately never recomputed here: links, bookmarks and
		// the mirror's folder names keep working when a title is reworded.
		if in.Title != nil {
			set("title", "title", *in.Title != cur.Title, *in.Title)
		}
		if in.Subtitle != nil {
			set("subtitle", "subtitle", *in.Subtitle != cur.Subtitle, *in.Subtitle)
		}
		if in.AuthorName != nil {
			set("author_name", "author_name", *in.AuthorName != cur.AuthorName, *in.AuthorName)
		}
		if in.Genre != nil {
			set("genre", "genre", *in.Genre != cur.Genre, *in.Genre)
		}
		if in.Language != nil {
			set("language", "language", *in.Language != cur.Language, *in.Language)
		}
		if in.Status != nil {
			set("status", "status", *in.Status != cur.Status, *in.Status)
		}
		if in.Description != nil {
			set("description", "description", *in.Description != cur.Description, *in.Description)
		}
		if in.WordGoal != nil {
			set("word_goal", "word_goal", *in.WordGoal != cur.WordGoal, *in.WordGoal)
		}
		if in.Deadline != nil {
			var v any
			if *in.Deadline != "" {
				v = *in.Deadline
			}
			set("deadline", "deadline", *in.Deadline != cur.Deadline, v)
		}
		if in.CoverColor != nil {
			set("cover_color", "cover_color", *in.CoverColor != cur.CoverColor, *in.CoverColor)
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		now := db.Now()
		args = append(args, now, in.ID)
		if _, err := tx.Exec(`UPDATE projects SET `+strings.Join(sets, ", ")+`, updated_at = ? WHERE id = ?`, args...); err != nil {
			return err
		}
		// Field names only: a title or description never goes into an event.
		c.Emit(ops.Event{Type: "project.updated", ProjectID: in.ID, Payload: map[string]any{"fields": changed}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		out, err = loadProject(c, tx, c.P.UserID, in.ID)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return Item{Item: out}, nil
}

// ---- trash, restore, archive, unarchive ------------------------------------

// StateIn names one project for a state change.
type StateIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in StateIn) ProjectRef() string { return in.ID }

// transition sets (on) or clears a project's timestamp column. It is
// idempotent: repeating it changes nothing and emits no event. It leaves
// updated_at alone on purpose, so the library's "last edited" stays about
// writing, not about housekeeping.
func transition(action, column string, on bool, event string) func(*ops.Ctx, StateIn) (Item, error) {
	return func(c *ops.Ctx, in StateIn) (Item, error) {
		var out Project
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			if _, err := loadProject(c, tx, c.P.UserID, in.ID); err != nil {
				return err
			}
			var res sql.Result
			var err error
			if on {
				res, err = tx.Exec(`UPDATE projects SET `+column+` = ? WHERE id = ? AND `+column+` IS NULL`, db.Now(), in.ID)
			} else {
				res, err = tx.Exec(`UPDATE projects SET `+column+` = NULL WHERE id = ? AND `+column+` IS NOT NULL`, in.ID)
			}
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				c.Emit(ops.Event{Type: event, ProjectID: in.ID})
				c.Audit(ops.AuditEntry{Action: action, TargetType: "project", TargetID: in.ID})
				if err := c.FlushTo(tx); err != nil {
					return err
				}
			}
			out, err = loadProject(c, tx, c.P.UserID, in.ID)
			return err
		})
		if err != nil {
			return Item{}, err
		}
		return Item{Item: out}, nil
	}
}

// ---- purge -------------------------------------------------------------------

// PurgeIn permanently deletes a trashed project.
type PurgeIn struct {
	ID           string `path:"id" json:"-" desc:"Project id."`
	ConfirmTitle string `json:"confirm_title" desc:"The project's exact title, typed to confirm."`
}

// ProjectRef lets the registry authorize by membership.
func (in PurgeIn) ProjectRef() string { return in.ID }

// PurgeOut reports a purge.
type PurgeOut struct {
	Purged bool   `json:"purged"`
	ID     string `json:"id"`
}

// purge is the only hard delete in the application. The checks and the delete
// share one transaction, so a project cannot be restored between them.
// Foreign keys cascade to members, per-person state, documents, exports and
// (through documents) doc_text and its FTS rows. The audit row records the id
// and the title's length, never the title; the event carries no content.
func purge(c *ops.Ctx, in PurgeIn) (PurgeOut, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var title string
		var trashed sql.NullString
		err := tx.QueryRow(`SELECT title, trashed_at FROM projects WHERE id = ? AND deleted_at IS NULL`, in.ID).Scan(&title, &trashed)
		if err == sql.ErrNoRows {
			return ops.NotFound("project")
		}
		if err != nil {
			return err
		}
		if !trashed.Valid {
			return ops.Conflict("Move the project to the trash before deleting it permanently.")
		}
		if in.ConfirmTitle != title {
			return ops.Invalid("confirm_title", "Type the project's exact title to confirm.")
		}
		if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, in.ID); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "project.purged", ProjectID: in.ID})
		c.Audit(ops.AuditEntry{Action: "project.purge", TargetType: "project", TargetID: in.ID,
			Detail: map[string]any{"title_length": utf8.RuneCountInString(title)}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return PurgeOut{}, err
	}
	// What the project left on disk (conflict copies, the Markdown mirror) is
	// removed by the purge hooks: best effort, after the commit, never inside
	// the transaction.
	c.Env.ProjectPurged(c, in.ID)
	return PurgeOut{Purged: true, ID: in.ID}, nil
}

// removeConflicts is the purge hook for conflict copies of documents, which
// live under conflicts/<project id>/ in the data directory. The id passed the
// database lookup before the hook runs, so it is a real project id; the Base
// check keeps even a malformed one from naming a path outside conflicts/.
func removeConflicts(dataDir string) func(context.Context, string) {
	return func(_ context.Context, projectID string) {
		if db.ValidID(projectID) {
			_ = os.RemoveAll(filepath.Join(dataDir, "conflicts", projectID))
		}
	}
}

// ---- pin and open --------------------------------------------------------------

// PinIn pins or unpins a project for the caller.
type PinIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	Pinned bool   `json:"pinned" desc:"true to pin, false to unpin."`
}

// ProjectRef lets the registry authorize by membership.
func (in PinIn) ProjectRef() string { return in.ID }

// Both ops write only the caller's own user_project_state row. They do not
// touch the project, bump updated_at, or emit project events: what one person
// pins or opens is nobody else's business.
func pin(c *ops.Ctx, in PinIn) (Item, error) {
	var out Project
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := loadProject(c, tx, c.P.UserID, in.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO user_project_state(user_id, project_id, pinned) VALUES (?,?,?)
			ON CONFLICT(user_id, project_id) DO UPDATE SET pinned = excluded.pinned`, c.P.UserID, in.ID, in.Pinned); err != nil {
			return err
		}
		var err error
		out, err = loadProject(c, tx, c.P.UserID, in.ID)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return Item{Item: out}, nil
}

// OpenIn records that the caller opened a project.
type OpenIn struct {
	ID         string `path:"id" json:"-" desc:"Project id."`
	DocumentID string `json:"document_id,omitempty" desc:"The live document being opened; leave out to keep the last one."`
}

// ProjectRef lets the registry authorize by membership.
func (in OpenIn) ProjectRef() string { return in.ID }

func open(c *ops.Ctx, in OpenIn) (Item, error) {
	var out Project
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := loadProject(c, tx, c.P.UserID, in.ID); err != nil {
			return err
		}
		var doc any
		if in.DocumentID != "" {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
				in.DocumentID, in.ID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ops.Invalid("document_id", "That document is not in this project.")
			}
			doc = in.DocumentID
		}
		if _, err := tx.Exec(`INSERT INTO user_project_state(user_id, project_id, last_opened_at, last_document_id) VALUES (?,?,?,?)
			ON CONFLICT(user_id, project_id) DO UPDATE SET last_opened_at = excluded.last_opened_at,
				last_document_id = COALESCE(excluded.last_document_id, user_project_state.last_document_id)`,
			c.P.UserID, in.ID, db.Now(), doc); err != nil {
			return err
		}
		var err error
		out, err = loadProject(c, tx, c.P.UserID, in.ID)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return Item{Item: out}, nil
}
