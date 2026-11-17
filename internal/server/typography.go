package server

import (
	"context"
	"html/template"

	"github.com/crueber/scriobh/internal/domain/typography"
)

// withTypography adds the project's typography, with the person's override over it, to the page's editor preferences
// (F-085). The three bundled faces go through the page's existing data-font attribute; everything else is data
// attributes that web/static/css/typography.css reads. Nothing is set when nothing is chosen, so a project that has
// no typography renders exactly as it did. Only the writing view and the reading view call it: exports, compile,
// print, share links and the static site never do.
func (s *Server) withTypography(ctx context.Context, userID, projectID string, pv map[string]string, e editorPrefs) editorPrefs {
	proj, person := typography.Load(ctx, s.DB, userID, projectID)
	eff := typography.Effective(proj, person, pv)
	if f := eff.EditorFont(); f != "" {
		e.Font = f
	}
	e.TypoAttrs = template.HTMLAttr(eff.HTMLAttrs()) //nolint:gosec // built only from the fixed lists
	return e
}
