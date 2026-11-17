package research

import (
	"database/sql"
	"errors"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The scratchpad is a person's private freewriting pad, one per project and one
// per Compendium. The rules, all of which are tested:
//
//   - Every op reads and writes only the caller's own row; there is no way to
//     name another person's. An administrator's get returns the administrator's
//     own pad.
//   - It is not a document: it is not in the manuscript, the outline, the word
//     counts or the goals, is never searched, exported, mirrored, pushed to Git
//     or sent in an event, and no audit line carries its text.
//   - It needs only membership (a reader may keep notes on a book they read), and
//     it is hidden again the moment membership ends; the text is kept and comes
//     back if they are added again.
//   - It goes with the person, the project or the Compendium (foreign keys that
//     cascade), so a purge leaves nothing behind.
//   - A save carries the revision it started from, so two tabs cannot silently
//     overwrite each other.

const maxScratch = 256 << 10

// Scratch is the caller's pad.
type Scratch struct {
	Content   string `json:"content"`
	Revision  int    `json:"revision" desc:"0 for a pad that has never been saved. Send it as base_revision with the next save."`
	WordCount int    `json:"word_count"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// ScratchGetIn names the owner whose pad to read.
type ScratchGetIn struct {
	ID string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
}

func (in ScratchGetIn) ProjectRef() string    { return in.ID }
func (in ScratchGetIn) CompendiumRef() string { return in.ID }

// ScratchSetIn saves the caller's pad.
type ScratchSetIn struct {
	ID           string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	Content      string `json:"content"`
	BaseRevision int    `json:"base_revision" desc:"The revision you last saw (0 for a pad never saved); a stale one is a 409 with the current revision."`
}

func (in ScratchSetIn) ProjectRef() string    { return in.ID }
func (in ScratchSetIn) CompendiumRef() string { return in.ID }

// ScratchItem wraps the pad.
type ScratchItem struct {
	Item Scratch `json:"item"`
}

func registerScratch(r *ops.Registry, kind, prefix, base string, reader ops.Auth) {
	s := prefix + "scratch."
	what := map[string]string{OwnerProject: "this project", OwnerCompendium: "this Compendium"}[kind]
	ops.Register(r, ops.Op{Name: s + "get", Method: "GET", Path: base + "/scratchpad", Auth: reader.ForSessions(),
		Desc: "Read your private scratchpad for " + what + ". It is yours alone: nobody else, an administrator included, can read it through Scríobh, and an API key of yours cannot either (only a person signed in with a browser); it is never exported, searched, mirrored or pushed. A backup archive an administrator downloads does contain it, as it contains everything in the database."},
		func(c *ops.Ctx, in ScratchGetIn) (ScratchItem, error) { return scratchGet(c, owner{kind, in.ID}) })
	ops.Register(r, ops.Op{Name: s + "set", Method: "PUT", Path: base + "/scratchpad", Auth: reader.ForSessions(),
		Desc: "Save your private scratchpad for " + what + ". Send the revision you last saw; a stale revision is a 409. Only a person signed in with a browser can; an API key is refused."},
		func(c *ops.Ctx, in ScratchSetIn) (ScratchItem, error) { return scratchSet(c, owner{kind, in.ID}, in) })
}

func scratchGet(c *ops.Ctx, o owner) (ScratchItem, error) {
	var s Scratch
	err := c.DB().Read().QueryRowContext(c, `SELECT content, revision, updated_at FROM scratchpads WHERE user_id = ? AND `+o.col()+` = ?`, c.P.UserID, o.id).
		Scan(&s.Content, &s.Revision, &s.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ScratchItem{}, err
	}
	s.WordCount = text.WordCount(s.Content)
	return ScratchItem{Item: s}, nil
}

func scratchSet(c *ops.Ctx, o owner, in ScratchSetIn) (ScratchItem, error) {
	if len(in.Content) > maxScratch || !utf8.ValidString(in.Content) {
		return ScratchItem{}, ops.Invalid("content", "The scratchpad is limited to 256 KB of text.")
	}
	if in.BaseRevision < 0 {
		return ScratchItem{}, ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	var out ScratchItem
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var cur int
		err := tx.QueryRow(`SELECT revision FROM scratchpads WHERE user_id = ? AND `+o.col()+` = ?`, c.P.UserID, o.id).Scan(&cur)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if cur != in.BaseRevision {
			return ops.ConflictWith("Your scratchpad changed in another tab.", map[string]any{"revision": cur})
		}
		now := db.Now()
		if exists {
			_, err = tx.Exec(`UPDATE scratchpads SET content = ?, revision = revision + 1, updated_at = ? WHERE user_id = ? AND `+o.col()+` = ?`, in.Content, now, c.P.UserID, o.id)
		} else {
			var project, compendium any
			if o.kind == OwnerProject {
				project = o.id
			} else {
				compendium = o.id
			}
			_, err = tx.Exec(`INSERT INTO scratchpads(user_id, project_id, compendium_id, content, revision, updated_at) VALUES (?,?,?,?,1,?)`, c.P.UserID, project, compendium, in.Content, now)
		}
		if err != nil {
			return err
		}
		out = ScratchItem{Item: Scratch{Content: in.Content, Revision: cur + 1, WordCount: text.WordCount(in.Content), UpdatedAt: now}}
		return nil // deliberately no event and no audit entry: nothing about a scratchpad leaves it
	})
	return out, err
}
