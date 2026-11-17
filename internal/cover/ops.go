package cover

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// In names the project.
type In struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in In) ProjectRef() string { return in.ID }

// File is the cover. Over REST it is the SVG itself, shown inline and cached by
// content hash (an ETag); elsewhere it is this JSON, with the SVG in body.
type File struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	ETag        string `json:"etag" desc:"Hash of everything the cover shows. The REST route answers 304 to a matching If-None-Match."`
	Body        []byte `json:"body" desc:"The SVG. The REST route returns it as the response body instead of JSON."`
}

// Download makes File an ops.Download.
func (f File) Download() (string, string, []byte) { return f.Filename, f.ContentType, f.Body }

// Inline makes File an ops.Inline: it is an image to show, not a file to save.
func (f File) Inline() string { return f.ETag }

// Register adds the cover op.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "project.cover", Method: "GET", Path: "/api/v1/projects/{id}/cover",
		Desc: "The project's cover as an SVG: a flat colour with the title, subtitle, author and kind, drawn from the project's own fields. " +
			"Deterministic, cached by content hash, safe to use as an image.",
		Auth: ops.RequireProject(ops.ProjectReader)}, get)
}

func get(c *ops.Ctx, in In) (File, error) {
	var s Spec
	err := c.DB().Read().QueryRowContext(c, `SELECT title, subtitle, author_name, kind, cover_color FROM projects WHERE id = ? AND deleted_at IS NULL`, in.ID).
		Scan(&s.Title, &s.Subtitle, &s.Author, &s.Kind, &s.Color)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ops.NotFound("project")
	}
	if err != nil {
		return File{}, err
	}
	key, svg := Cached(s)
	sum := sha256.Sum256(svg)
	return File{Filename: projects.Slugify(s.Title) + "-cover.svg", ContentType: "image/svg+xml", Bytes: len(svg),
		SHA256: hex.EncodeToString(sum[:]), ETag: `"` + key[:24] + `"`, Body: svg}, nil
}
