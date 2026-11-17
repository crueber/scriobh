package cover

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// In names the project.
type In struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	Format string `query:"format" enum:"svg" desc:"svg: the generated typographic cover (SVG), even when the project has a cover layout of its own."`
	Size   string `query:"size" enum:"full,thumb" desc:"thumb: the front 480 px wide, for a list. Default full. A project with no cover layout has only the generated SVG."`
	Part   string `query:"part" enum:"front,back" desc:"back: the back cover, when the layout gives it a picture (404 otherwise). Default front."`
	V      string `query:"v" desc:"A cache-busting value (the layout's key); ignored."`
}

// ProjectRef lets the registry authorize by membership.
func (in In) ProjectRef() string { return in.ID }

// File is the cover. Over REST it is the image itself (the project's own cover picture as a JPEG, or the generated
// SVG), shown inline and cached by content hash (an ETag); elsewhere it is this JSON, with the image in body.
type File struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	ETag        string `json:"etag" desc:"Hash of everything the cover shows. The REST route answers 304 to a matching If-None-Match."`
	Body        []byte `json:"body" desc:"The image: a JPEG when the project has its own cover layout, else the SVG. The REST route returns it as the response body instead of JSON."`
}

// Download makes File an ops.Download.
func (f File) Download() (string, string, []byte) { return f.Filename, f.ContentType, f.Body }

// Inline makes File an ops.Inline: it is an image to show, not a file to save.
func (f File) Inline() string { return f.ETag }

// Register adds the cover op.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "project.cover", Method: "GET", Path: "/api/v1/projects/{id}/cover",
		Desc: "The project's cover as an image. With a cover layout of its own (project.cover_layout.get) it is the front as a JPEG, at most 2560 px on the long side (size thumb: 480 px wide; part back: the back, when it has one), cached by the layout's hash. " +
			"Without one, or with format svg, it is an SVG: a flat colour with the title, subtitle, author and kind, drawn from the project's own fields. " +
			"Deterministic, cached by content hash, safe to use as an image.",
		Auth: ops.RequireProject(ops.ProjectReader)}, get)
}

func get(c *ops.Ctx, in In) (File, error) {
	if in.Format != "" && in.Format != "svg" {
		return File{}, ops.Invalid("format", "format is svg.")
	}
	if in.Size != "" && in.Size != "full" && in.Size != "thumb" {
		return File{}, ops.Invalid("size", "size is full or thumb.")
	}
	if in.Part != "" && in.Part != "front" && in.Part != "back" {
		return File{}, ops.Invalid("part", "part is front or back.")
	}
	if in.Part == "back" && (in.Format == "svg" || in.Size == "thumb") {
		return File{}, ops.Invalid("part", "The back is a full-size JPEG: it has no svg or thumb form.")
	}
	var s Spec
	err := c.DB().Read().QueryRowContext(c, `SELECT title, subtitle, author_name, kind, cover_color FROM projects WHERE id = ? AND deleted_at IS NULL`, in.ID).
		Scan(&s.Title, &s.Subtitle, &s.Author, &s.Kind, &s.Color)
	if errors.Is(err, sql.ErrNoRows) {
		return File{}, ops.NotFound("project")
	}
	if err != nil {
		return File{}, err
	}
	if in.Format != "svg" {
		name := coverlayout.NameFront
		switch {
		case in.Part == "back":
			name = coverlayout.NameBack
		case in.Size == "thumb":
			name = coverlayout.NameThumb
		}
		d, _, err := coverlayout.Make(c, c.Env, in.ID, name, false)
		switch {
		case errors.Is(err, coverlayout.ErrNoPicture):
			d, err = nil, nil // the layout names a picture that is gone: the generated cover stands in
		case err != nil:
			return File{}, err
		}
		if d == nil && in.Part == "back" {
			return File{}, ops.NotFound("back cover")
		}
		if d != nil {
			sum := sha256.Sum256(d.Data)
			suffix := ""
			if name != coverlayout.NameFront {
				suffix = "-" + name
			}
			return File{Filename: projects.Slugify(s.Title) + "-cover" + suffix + ".jpg", ContentType: "image/jpeg", Bytes: len(d.Data),
				SHA256: hex.EncodeToString(sum[:]), ETag: d.ETag(), Body: d.Data}, nil
		}
	}
	key, svg := Cached(s)
	sum := sha256.Sum256(svg)
	return File{Filename: projects.Slugify(s.Title) + "-cover.svg", ContentType: "image/svg+xml", Bytes: len(svg),
		SHA256: hex.EncodeToString(sum[:]), ETag: `"` + key[:24] + `"`, Body: svg}, nil
}
