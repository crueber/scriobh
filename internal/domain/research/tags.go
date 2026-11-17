package research

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Tags are labels a project or a Compendium keeps for its research notes. A
// name is unique among the owner's tags ignoring case and diacritics, the
// colour is one of a fixed list of tokens, and renaming, merging and deleting
// work on every note that uses the tag at once.

// Tag is one tag with the number of notes that carry it.
type Tag struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Color     string `json:"color" enum:"slate,clay,ochre,moss,teal,blue,plum,rose"`
	Notes     int    `json:"notes" desc:"Live research notes that carry it."`
	CreatedAt string `json:"created_at"`
}

// TagItem wraps one tag.
type TagItem struct {
	Item Tag `json:"item"`
}

// TagListIn lists an owner's tags.
type TagListIn struct {
	ID string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
}

func (in TagListIn) ProjectRef() string    { return in.ID }
func (in TagListIn) CompendiumRef() string { return in.ID }

// TagListOut lists tags by name.
type TagListOut struct {
	Items  []Tag    `json:"items"`
	Colors []string `json:"colors" desc:"The colour tokens a tag can have."`
}

// TagCreateIn creates a tag.
type TagCreateIn struct {
	ID    string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	Name  string `json:"name"`
	Color string `json:"color,omitempty" enum:"slate,clay,ochre,moss,teal,blue,plum,rose" desc:"Default slate."`
}

func (in TagCreateIn) ProjectRef() string    { return in.ID }
func (in TagCreateIn) CompendiumRef() string { return in.ID }

// TagIn names one tag of an owner.
type TagIn struct {
	ID    string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	TagID string `path:"tag_id" json:"-" desc:"Tag id." ref:"tag"`
}

func (in TagIn) ProjectRef() string    { return in.ID }
func (in TagIn) CompendiumRef() string { return in.ID }

// TagUpdateIn renames a tag or changes its colour.
type TagUpdateIn struct {
	ID    string  `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	TagID string  `path:"tag_id" json:"-" desc:"Tag id." ref:"tag"`
	Name  *string `json:"name,omitempty"`
	Color *string `json:"color,omitempty" enum:"slate,clay,ochre,moss,teal,blue,plum,rose"`
}

func (in TagUpdateIn) ProjectRef() string    { return in.ID }
func (in TagUpdateIn) CompendiumRef() string { return in.ID }

// TagMergeIn merges a tag into another.
type TagMergeIn struct {
	ID     string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	TagID  string `path:"tag_id" json:"-" desc:"The tag to merge away." ref:"tag"`
	IntoID string `json:"into_id" desc:"The tag that keeps the notes." ref:"tag"`
}

func (in TagMergeIn) ProjectRef() string    { return in.ID }
func (in TagMergeIn) CompendiumRef() string { return in.ID }

// TagDeleteOut says what a delete or merge did.
type TagDeleteOut struct {
	Deleted bool `json:"deleted"`
	Notes   int  `json:"notes" desc:"Notes that carried the tag."`
}

func registerTags(r *ops.Registry, kind, prefix, base string, reader, editor ops.Auth, what string) {
	t := prefix + "tag."
	ops.Register(r, ops.Op{Name: t + "list", Method: "GET", Path: base + "/tags", Auth: reader,
		Desc: "List " + what + "'s tags with how many research notes use each."},
		func(c *ops.Ctx, in TagListIn) (TagListOut, error) { return tagList(c, owner{kind, in.ID}) })
	ops.Register(r, ops.Op{Name: t + "create", Method: "POST", Path: base + "/tags", Status: 201, Auth: editor,
		Desc: "Create a tag. Tags are also created when a note is saved with a new name."},
		func(c *ops.Ctx, in TagCreateIn) (TagItem, error) { return tagCreate(c, owner{kind, in.ID}, in) })
	ops.Register(r, ops.Op{Name: t + "update", Method: "PATCH", Path: base + "/tags/{tag_id}", Auth: editor,
		Desc: "Rename a tag or change its colour. Notes keep it."},
		func(c *ops.Ctx, in TagUpdateIn) (TagItem, error) { return tagUpdate(c, owner{kind, in.ID}, in) })
	ops.Register(r, ops.Op{Name: t + "merge", Permanent: true, Method: "POST", Path: base + "/tags/{tag_id}/merge", Auth: editor,
		Desc: "Merge a tag into another: its notes carry the other tag, and it is deleted."},
		func(c *ops.Ctx, in TagMergeIn) (TagDeleteOut, error) { return tagMerge(c, owner{kind, in.ID}, in) })
	ops.Register(r, ops.Op{Name: t + "delete", Permanent: true, Method: "DELETE", Path: base + "/tags/{tag_id}", Auth: editor,
		Desc: "Delete a tag. Notes lose it and are otherwise unchanged."},
		func(c *ops.Ctx, in TagIn) (TagDeleteOut, error) { return tagDelete(c, owner{kind, in.ID}, in) })
}

const tagCols = `t.id, t.name, t.color, t.created_at,
	(SELECT count(*) FROM research_tags rt JOIN research_notes n ON n.id = rt.note_id WHERE rt.tag_id = t.id AND n.deleted_at IS NULL)`

func tagList(c *ops.Ctx, o owner) (TagListOut, error) {
	rows, err := c.DB().Read().QueryContext(c, `SELECT `+tagCols+` FROM tags t WHERE t.`+o.col()+` = ? ORDER BY t.name_key, t.id`, o.id)
	if err != nil {
		return TagListOut{}, err
	}
	defer rows.Close()
	out := TagListOut{Items: []Tag{}, Colors: Colors}
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.Color, &t.CreatedAt, &t.Notes); err != nil {
			return TagListOut{}, err
		}
		out.Items = append(out.Items, t)
	}
	return out, rows.Err()
}

func loadTag(ctx context.Context, q queryer, o owner, id string) (Tag, error) {
	var t Tag
	err := q.QueryRowContext(ctx, `SELECT `+tagCols+` FROM tags t WHERE t.id = ? AND t.`+o.col()+` = ?`, id, o.id).Scan(&t.ID, &t.Name, &t.Color, &t.CreatedAt, &t.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return Tag{}, ops.NotFound("tag")
	}
	return t, err
}

func cleanTagName(s string) (string, string, error) {
	s = cleanLine(s)
	if s == "" {
		return "", "", ops.Invalid("name", "A tag needs a name.")
	}
	if utf8.RuneCountInString(s) > maxTagLen {
		return "", "", ops.Invalid("name", "A tag is limited to 40 characters.")
	}
	return s, text.FoldTitle(s), nil
}

func cleanColor(s string) (string, error) {
	if s == "" {
		return "slate", nil
	}
	if !oneOf(Colors, s) {
		return "", ops.Invalid("color", "Colour is one of: "+strings.Join(Colors, ", ")+".")
	}
	return s, nil
}

func tagCreate(c *ops.Ctx, o owner, in TagCreateIn) (TagItem, error) {
	name, key, err := cleanTagName(in.Name)
	if err != nil {
		return TagItem{}, err
	}
	color, err := cleanColor(in.Color)
	if err != nil {
		return TagItem{}, err
	}
	var out TagItem
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var exists string
		if err := tx.QueryRow(`SELECT id FROM tags WHERE `+o.col()+` = ? AND name_key = ?`, o.id, key).Scan(&exists); err == nil {
			return ops.Conflict("There is already a tag with that name.")
		}
		id := db.NewID("tag")
		var project, compendium any
		if o.kind == OwnerProject {
			project = o.id
		} else {
			compendium = o.id
		}
		if _, err := tx.Exec(`INSERT INTO tags(id, project_id, compendium_id, name, name_key, color, created_at) VALUES (?,?,?,?,?,?,?)`,
			id, project, compendium, name, key, color, db.Now()); err != nil {
			return err
		}
		t, err := loadTag(c, tx, o, id)
		if err != nil {
			return err
		}
		out = TagItem{Item: t}
		c.Emit(o.event("tag.created", map[string]any{"tag_id": id}))
		return c.FlushTo(tx)
	})
	return out, err
}

func tagUpdate(c *ops.Ctx, o owner, in TagUpdateIn) (TagItem, error) {
	var out TagItem
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		t, err := loadTag(c, tx, o, in.TagID)
		if err != nil {
			return err
		}
		name, key := t.Name, text.FoldTitle(t.Name)
		if in.Name != nil {
			if name, key, err = cleanTagName(*in.Name); err != nil {
				return err
			}
			var other string
			if err := tx.QueryRow(`SELECT id FROM tags WHERE `+o.col()+` = ? AND name_key = ? AND id != ?`, o.id, key, t.ID).Scan(&other); err == nil {
				return ops.Conflict("There is already a tag with that name. Merge them instead.")
			}
		}
		color := t.Color
		if in.Color != nil {
			if color, err = cleanColor(*in.Color); err != nil {
				return err
			}
		}
		if name == t.Name && color == t.Color {
			out = TagItem{Item: t}
			return nil
		}
		if _, err := tx.Exec(`UPDATE tags SET name = ?, name_key = ?, color = ? WHERE id = ?`, name, key, color, t.ID); err != nil {
			return err
		}
		t.Name, t.Color = name, color
		out = TagItem{Item: t}
		c.Emit(o.event("tag.updated", map[string]any{"tag_id": t.ID}))
		return c.FlushTo(tx)
	})
	return out, err
}

func tagMerge(c *ops.Ctx, o owner, in TagMergeIn) (TagDeleteOut, error) {
	if in.IntoID == "" {
		return TagDeleteOut{}, ops.Invalid("into_id", "Name the tag to merge into.")
	}
	if in.TagID == in.IntoID {
		return TagDeleteOut{}, ops.Invalid("into_id", "Choose a different tag to merge into.")
	}
	var out TagDeleteOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		from, err := loadTag(c, tx, o, in.TagID)
		if err != nil {
			return err
		}
		if _, err := loadTag(c, tx, o, in.IntoID); err != nil {
			return ops.Invalid("into_id", "That tag does not exist here.")
		}
		var n int
		_ = tx.QueryRow(`SELECT count(*) FROM research_tags WHERE tag_id = ?`, from.ID).Scan(&n)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO research_tags(note_id, tag_id) SELECT note_id, ? FROM research_tags WHERE tag_id = ?`, in.IntoID, from.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM tags WHERE id = ?`, from.ID); err != nil { // its research_tags rows cascade
			return err
		}
		out = TagDeleteOut{Deleted: true, Notes: n}
		c.Emit(o.event("tag.merged", map[string]any{"tag_id": from.ID, "target_id": in.IntoID, "count": n}))
		return c.FlushTo(tx)
	})
	return out, err
}

func tagDelete(c *ops.Ctx, o owner, in TagIn) (TagDeleteOut, error) {
	var out TagDeleteOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		t, err := loadTag(c, tx, o, in.TagID)
		if err != nil {
			return err
		}
		var n int
		_ = tx.QueryRow(`SELECT count(*) FROM research_tags WHERE tag_id = ?`, t.ID).Scan(&n)
		if _, err := tx.Exec(`DELETE FROM tags WHERE id = ?`, t.ID); err != nil {
			return err
		}
		out = TagDeleteOut{Deleted: true, Notes: n}
		c.Emit(o.event("tag.deleted", map[string]any{"tag_id": t.ID, "count": n}))
		return c.FlushTo(tx)
	})
	return out, err
}
