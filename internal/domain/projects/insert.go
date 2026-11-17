package projects

import (
	"database/sql"

	"github.com/crueber/scriobh/internal/ops"
)

// NewProject is what InsertTx writes. CreateIn is the validated input of the
// project.create op; the importer builds the same shape from a folder.
type NewProject struct {
	ID, OwnerID, Now string
	CreateIn
}

// InsertTx writes a project and its owner membership inside the caller's
// transaction and returns the slug it was given. It does not seed a first
// chapter: project.create does that itself, and an import brings its own
// documents. The input must already have passed CreateIn.Validate.
func InsertTx(tx *sql.Tx, p NewProject) (slug string, err error) {
	slug, err = freeSlug(tx, p.OwnerID, Slugify(p.Title))
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO projects
		(id, owner_id, title, slug, kind, subtitle, author_name, language, word_goal, cover_color, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.OwnerID, p.Title, slug, p.Kind, p.Subtitle, p.AuthorName, p.Language, p.WordGoal, p.CoverColor, p.Now, p.Now); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`,
		p.ID, p.OwnerID, ops.ProjectOwner, p.Now); err != nil {
		return "", err
	}
	return slug, nil
}
