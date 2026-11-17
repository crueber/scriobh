package site

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/crueber/scriobh/internal/db"
	sitegen "github.com/crueber/scriobh/internal/site"
)

// loadOptions reads a project's options over the defaults: a field the stored
// document lacks (an option added later) keeps its default.
func loadOptions(ctx context.Context, d *db.DB, projectID string) (sitegen.Options, string, error) {
	o := sitegen.Defaults()
	var raw, updated string
	err := d.Read().QueryRowContext(ctx, `SELECT options_json, updated_at FROM site_options WHERE project_id = ?`, projectID).Scan(&raw, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return o, "", nil
	}
	if err != nil {
		return o, "", err
	}
	if json.Unmarshal([]byte(raw), &o) != nil {
		return sitegen.Defaults(), updated, nil // a row nobody can read is no options
	}
	if o.Nav == nil {
		o.Nav = []sitegen.NavLink{}
	}
	return o, updated, nil
}

func loadRows(ctx context.Context, d *db.DB, projectID string) (map[string]docRow, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT document_id, role, slug, published_at, excerpt, tags_json, updated_at, pinned_slug FROM site_docs WHERE project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]docRow{}
	for rows.Next() {
		var id, tags string
		var r docRow
		if err := rows.Scan(&id, &r.Role, &r.Slug, &r.Published, &r.Excerpt, &tags, &r.UpdatedAt, &r.Pinned); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tags), &r.Tags)
		out[id] = r
	}
	return out, rows.Err()
}

func loadCreated(ctx context.Context, d *db.DB, projectID string) (map[string]time.Time, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT id, created_at FROM documents WHERE project_id = ? AND deleted_at IS NULL`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id, at string
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		if t, err := db.Parse(at); err == nil {
			out[id] = t.UTC()
		}
	}
	return out, rows.Err()
}

// State is the last build and the last publish of a project.
type State struct {
	Build   Stamp  `json:"build"`
	Publish Stamp  `json:"publish"`
	Dir     string `json:"-"`
}

// Stamp says when something last ran and what it produced.
type Stamp struct {
	At        string `json:"at,omitempty" desc:"When it ran; empty if it never has."`
	SiteHash  string `json:"site_hash,omitempty" desc:"A hash of every file and its path: the same site is the same hash."`
	InputHash string `json:"input_hash,omitempty" desc:"A hash of what the site was made from; compare it with the preview's to see whether anything changed since."`
	Posts     int    `json:"posts"`
	Files     int    `json:"files"`
	Bytes     int    `json:"bytes"`
}

func loadState(ctx context.Context, d *db.DB, projectID string) (State, error) {
	var s State
	err := d.Read().QueryRowContext(ctx, `SELECT build_at, build_site_hash, build_input_hash, build_posts, build_files, build_bytes,
		publish_at, publish_site_hash, publish_input_hash, publish_posts, publish_files, publish_bytes, publish_dir FROM site_state WHERE project_id = ?`, projectID).
		Scan(&s.Build.At, &s.Build.SiteHash, &s.Build.InputHash, &s.Build.Posts, &s.Build.Files, &s.Build.Bytes,
			&s.Publish.At, &s.Publish.SiteHash, &s.Publish.InputHash, &s.Publish.Posts, &s.Publish.Files, &s.Publish.Bytes, &s.Dir)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, nil
	}
	return s, err
}

func saveStamp(tx *sql.Tx, projectID, kind string, st Stamp, dir string) error {
	if _, err := tx.Exec(`INSERT INTO site_state(project_id) VALUES (?) ON CONFLICT(project_id) DO NOTHING`, projectID); err != nil {
		return err
	}
	switch kind {
	case "build":
		_, err := tx.Exec(`UPDATE site_state SET build_at=?, build_site_hash=?, build_input_hash=?, build_posts=?, build_files=?, build_bytes=? WHERE project_id=?`,
			st.At, st.SiteHash, st.InputHash, st.Posts, st.Files, st.Bytes, projectID)
		return err
	}
	_, err := tx.Exec(`UPDATE site_state SET publish_at=?, publish_site_hash=?, publish_input_hash=?, publish_posts=?, publish_files=?, publish_bytes=?, publish_dir=? WHERE project_id=?`,
		st.At, st.SiteHash, st.InputHash, st.Posts, st.Files, st.Bytes, dir, projectID)
	return err
}

// DocRow is a document's site settings as the ops show them.
type DocRow struct {
	DocumentID  string   `json:"document_id"`
	Role        string   `json:"role" enum:"auto,post,page,skip" desc:"auto: decided by the project's rule (chapters or top-level documents are posts, front and back matter are pages)."`
	Slug        string   `json:"slug" desc:"The address chosen for it (posts/<slug>.html); empty: made from the title."`
	PinnedSlug  string   `json:"pinned_slug" desc:"The address it was given the first time the site was built or published, kept so that it does not move when other documents arrive or the title changes; clear it with clear: [\"pinned_slug\"] to let it be made again."`
	PublishedAt string   `json:"published_at" desc:"The publication date (RFC 3339, UTC); empty: the date the document was created, with a warning."`
	Excerpt     string   `json:"excerpt" desc:"The summary shown in lists and feeds; empty: the first paragraph."`
	Tags        []string `json:"tags"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

func toRow(id string, r docRow) DocRow {
	if r.Role == "" {
		r.Role = "auto"
	}
	if r.Tags == nil {
		r.Tags = []string{}
	}
	return DocRow{DocumentID: id, Role: r.Role, Slug: r.Slug, PinnedSlug: r.Pinned, PublishedAt: r.Published, Excerpt: r.Excerpt, Tags: r.Tags, UpdatedAt: r.UpdatedAt}
}

func sortedRows(m map[string]docRow) []DocRow {
	out := make([]DocRow, 0, len(m))
	for id, r := range m {
		out = append(out, toRow(id, r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DocumentID < out[j].DocumentID })
	return out
}
