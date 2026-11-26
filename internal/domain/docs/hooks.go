package docs

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
)

// The write path's side effects on history and the link graph live here, so
// the ops in docs.go stay short. Every function runs inside the caller's
// transaction.

const (
	maxSceneField = 200 // pov, location
	maxStoryDate  = 64
)

// cleanLine trims a one-line field and folds line breaks to spaces.
func cleanLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s))
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// prior is a document as it stood before a save replaced it.
type prior struct {
	revisions.HeadState
	Title string
}

// afterSave runs after doc.update has written the row. It records the previous
// head as a version (a body change): an automatic one when one is due, or, for a
// big cut, one labelled as such whatever the autosave interval says. It brings
// the document's outgoing links up to date (a body change), and re-resolves the
// links that pointed at its old title or now point at its new one (a rename).
func afterSave(tx *sql.Tx, p prior, projectID, id, title, content string, contentChanged, bigCut bool, now string) error {
	if contentChanged {
		if bigCut {
			if _, err := revisions.CaptureBefore(tx, p.HeadState, revisions.ReasonCut, now); err != nil {
				return err
			}
		} else if _, err := revisions.CaptureHead(tx, p.HeadState, now); err != nil {
			return err
		}
		if err := links.Sync(tx, links.SyncIn{ProjectID: projectID, DocumentID: id, Content: content, Now: now}); err != nil {
			return err
		}
		if err := mentions.Sync(tx, projectID, id); err != nil {
			return err
		}
		if err := attachments.SyncRefs(tx, attachments.RefDoc, id, attachments.OwnerProject, projectID, content); err != nil {
			return err
		}
	}
	if title != p.Title {
		return links.Reresolve(tx, projectID, p.Title, title)
	}
	return nil
}

// afterCreate links a new document's body and picks up links that were waiting
// for its title.
func afterCreate(tx *sql.Tx, projectID, id, title, content, now string) error {
	if err := links.Sync(tx, links.SyncIn{ProjectID: projectID, DocumentID: id, Content: content, Now: now}); err != nil {
		return err
	}
	if err := mentions.Sync(tx, projectID, id); err != nil {
		return err
	}
	if err := attachments.SyncRefs(tx, attachments.RefDoc, id, attachments.OwnerProject, projectID, content); err != nil {
		return err
	}
	return links.Reresolve(tx, projectID, title)
}

// titlesWhere returns the distinct titles of the documents a statement
// selects, for re-resolving links after a trash or restore.
func titlesWhere(tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// applyScene applies the scene-metadata fields of an update and reports whether
// any changed.
func applyScene(in UpdateIn, pov, location, storyDate, storyEnd *string) (bool, error) {
	changed := false
	for _, f := range []struct {
		name string
		in   *string
		cur  *string
		max  int
	}{
		{"pov", in.Pov, pov, maxSceneField},
		{"location", in.Location, location, maxSceneField},
		{"story_date", in.StoryDate, storyDate, maxStoryDate},
		{"story_date_end", in.StoryDateEnd, storyEnd, maxStoryDate},
	} {
		if f.in == nil {
			continue
		}
		v := cleanLine(*f.in)
		if utf8.RuneCountInString(v) > f.max {
			return false, ops.Invalid(f.name, fmt.Sprintf("This field is limited to %d characters.", f.max))
		}
		if v != *f.cur {
			changed = true
			*f.cur = v
		}
	}
	return changed, nil
}

// relinkBatch re-resolves the links to every document one trash took out of
// the tree.
func relinkBatch(tx *sql.Tx, projectID, batch string) error {
	titles, err := titlesWhere(tx, `SELECT DISTINCT title FROM documents WHERE project_id = ? AND trash_batch = ?`, projectID, batch)
	if err != nil {
		return err
	}
	return links.Reresolve(tx, projectID, titles...)
}

// afterSplit rewrites the links of both halves and picks up links waiting for
// the new document's title.
func afterSplit(tx *sql.Tx, projectID, headID, head, tailID, tailTitle, tail, now string) error {
	if err := links.Sync(tx, links.SyncIn{ProjectID: projectID, DocumentID: headID, Content: head, Now: now}); err != nil {
		return err
	}
	if err := mentions.Sync(tx, projectID, headID); err != nil {
		return err
	}
	if err := attachments.SyncRefs(tx, attachments.RefDoc, headID, attachments.OwnerProject, projectID, head); err != nil {
		return err
	}
	return afterCreate(tx, projectID, tailID, tailTitle, tail, now)
}
