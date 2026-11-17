package server

import (
	"context"
	"net/http"
	"regexp"

	"github.com/crueber/scriobh/internal/ops"
)

// An instance administrator acts as the owner of every project and Compendium at the op layer (the API, the CLI):
// that is how an instance is looked after. The pages are for reading and writing one's own work, so on the UI layer
// an administrator who is not a member of a project or Compendium is a stranger to it: its fragments, forms and files
// answer 404 as its slug pages do, and nothing of a manuscript or a bible is shown to someone it was not shared with.
// (Administration has its own pages: settings, backups, people.)

var uiScoped = regexp.MustCompile(`^/(?:ui/)?(projects|compendiums|docs|entities|timeline/events|files)/([A-Za-z0-9_]+)(?:/|$)`)

// adminOutsider reports whether the request is an administrator's, for something addressed by id on a UI route that
// they are not a member of. Anything that does not exist is not an outsider's: the handler answers its own 404.
func (s *Server) adminOutsider(r *http.Request) bool {
	p := s.principal(r)
	if !p.Authenticated() || p.Role != ops.RoleAdmin {
		return false
	}
	m := uiScoped.FindStringSubmatch(r.URL.Path)
	if m == nil {
		return false
	}
	ctx := r.Context()
	var project, compendium string
	switch kind, id := m[1], m[2]; {
	case kind == "projects":
		project = id
	case kind == "compendiums":
		compendium = id
	case kind == "docs":
		project = s.lookup(ctx, `SELECT project_id FROM documents WHERE id = ?`, id)
		if project == "" {
			return false
		}
	case kind == "entities":
		compendium = s.lookup(ctx, `SELECT compendium_id FROM entities WHERE id = ?`, id)
		if compendium == "" {
			return false
		}
	case kind == "timeline/events":
		compendium = s.lookup(ctx, `SELECT compendium_id FROM timeline_events WHERE id = ?`, id)
		if compendium == "" {
			return false
		}
	case kind == "files": // /files/{id}
		row := s.DB.Read().QueryRowContext(ctx, `SELECT COALESCE(project_id,''), COALESCE(compendium_id,'') FROM attachments WHERE id = ?`, id)
		if row.Scan(&project, &compendium) != nil {
			return false
		}
	}
	var n int
	if project != "" {
		_ = s.DB.Read().QueryRowContext(ctx, `SELECT count(*) FROM project_members WHERE project_id = ? AND user_id = ?`, project, p.UserID).Scan(&n)
		if n == 0 {
			var exists int
			_ = s.DB.Read().QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE id = ?`, project).Scan(&exists)
			return exists > 0
		}
		return false
	}
	if compendium != "" {
		_ = s.DB.Read().QueryRowContext(ctx, `SELECT count(*) FROM compendium_members WHERE compendium_id = ? AND user_id = ?`, compendium, p.UserID).Scan(&n)
		if n == 0 {
			var exists int
			_ = s.DB.Read().QueryRowContext(ctx, `SELECT count(*) FROM compendiums WHERE id = ?`, compendium).Scan(&exists)
			return exists > 0
		}
	}
	return false
}

func (s *Server) lookup(ctx context.Context, query, id string) string {
	var v string
	_ = s.DB.Read().QueryRowContext(ctx, query, id).Scan(&v)
	return v
}
