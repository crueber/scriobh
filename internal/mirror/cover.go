package mirror

import (
	"context"
	"fmt"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
)

// The project's own cover (docs/COVERS.md) is projected as
//
//	cover/<part>-<sha12>.<ext>   each picture the layout uses (part: front, back or wrap), byte for byte
//
// and the layout itself, without ids, is the "cover" object of project.json: the shape, the trim, the alt text and,
// for each side, how it is placed and which file holds its picture. A project that uses the generated cover has
// neither, so it is the same bytes as before covers existed.

const coverDir = "cover"

// manifestCover is the layout in project.json. Field order is fixed, so the bytes are stable.
type manifestCover struct {
	Shape string        `json:"shape"`
	Trim  string        `json:"trim"`
	Alt   string        `json:"alt"`
	Front *manifestSide `json:"front,omitempty"`
	Back  *manifestSide `json:"back,omitempty"`
	Wrap  *manifestWrap `json:"wrap,omitempty"`
}

type manifestSide struct {
	Mode       string  `json:"mode"`
	File       string  `json:"file,omitempty"`
	Fit        string  `json:"fit,omitempty"`
	Scale      int     `json:"scale,omitempty"`
	Position   string  `json:"position,omitempty"`
	Background string  `json:"background,omitempty"`
	MarginIn   float64 `json:"margin_in,omitempty"`
}

type manifestWrap struct {
	File    string  `json:"file"`
	BleedIn float64 `json:"bleed_in"`
}

// loadCover reads a project's layout and the pictures it uses. It returns nil when the project has no layout.
func (m *Mirror) loadCover(ctx context.Context, projectID string) (map[string]extFile, *manifestCover, error) {
	l, err := coverlayout.Load(ctx, m.o.DB.Read(), projectID)
	if err != nil || l == nil {
		return nil, nil, err
	}
	ext := map[string]extFile{}
	file := func(part, attID string) string {
		if attID == "" {
			return ""
		}
		var sha, e string
		var n int64
		if err := m.o.DB.Read().QueryRowContext(ctx, `SELECT sha256, ext, bytes FROM attachments WHERE id = ? AND project_id = ?`, attID, projectID).Scan(&sha, &e, &n); err != nil {
			return "" // the picture is gone: the layout names a file that is not there, which the cover ops already show
		}
		src, err := attachments.BlobPath(m.o.DataDir, attachments.OwnerProject, projectID, sha, e)
		if err != nil {
			return ""
		}
		name := fmt.Sprintf("%s/%s-%s.%s", coverDir, part, sha[:12], e)
		ext[name] = extFile{Src: src, SHA: sha, Size: n}
		return name
	}
	c := &manifestCover{Shape: l.Shape, Trim: l.Trim, Alt: l.Alt}
	side := func(part string, s coverlayout.Side) *manifestSide {
		out := &manifestSide{Mode: s.Mode}
		if s.Mode == coverlayout.ModeImage || s.Mode == coverlayout.ModePage {
			out.File = file(part, s.Attachment)
		}
		if s.Mode == coverlayout.ModePage {
			out.Fit, out.Scale, out.Position, out.Background, out.MarginIn = s.Fit, s.Scale, s.Position, s.Background, s.MarginIn
		}
		return out
	}
	if l.Shape == coverlayout.ShapeWrap {
		c.Wrap = &manifestWrap{File: file("wrap", l.Wrap.Attachment), BleedIn: l.Wrap.Bleed()}
	} else {
		c.Front, c.Back = side("front", l.Front), side("back", l.Back)
	}
	return ext, c, nil
}
