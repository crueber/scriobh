package server

import (
	"net/http"

	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// The import pages. The page itself only holds the chooser and the words around it; the
// script (js/import.js) reads the chosen folder or zip in the browser and talks to the
// import ops over REST (preview, then apply with the preview's token). Nothing here writes.

type importData struct {
	Into   *projects.Project // set when adding to an existing project
	Caps   importCaps
	Pandoc bool // the bridge is on and usable: the Pandoc formats are offered
}

type importCaps struct {
	Listed, Markdown, FileMiB, TotalMiB, BookMiB int
}

func caps() importCaps {
	return importCaps{Listed: imports.MaxListed, Markdown: imports.MaxMarkdown, FileMiB: imports.MaxFileBytes >> 20, TotalMiB: imports.MaxTotalBytes >> 20, BookMiB: editions.MaxFileBytes >> 20}
}

// importPage is GET /import: a new project from a folder or a zip. Instance writers only.
func (s *Server) importPage(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	if p.Role != ops.RoleAdmin && p.Role != ops.RoleWriter {
		s.fail(w, r, ops.Forbidden("Your account can read projects but not create them."))
		return
	}
	s.render(w, r, http.StatusOK, "import", s.page(r, "Import", importData{Caps: caps(), Pandoc: s.pandocUsable(r.Context())}))
}

// importIntoPage is GET /p/{slug}/import: add a folder to a project the person can edit.
func (s *Server) importIntoPage(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	pid, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, r.PathValue("slug"))
	if !ok {
		s.fail(w, r, ops.NotFound("project"))
		return
	}
	out, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: pid})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pr := out.(projects.Item).Item.Addressed()
	if pr.Trashed {
		s.fail(w, r, ops.NotFound("project"))
		return
	}
	if pr.Role != ops.ProjectOwner && pr.Role != ops.ProjectEditor {
		s.fail(w, r, ops.Forbidden("Only an owner or an editor can add files to a project."))
		return
	}
	s.render(w, r, http.StatusOK, "import", s.page(r, "Import into "+pr.Title, importData{Into: &pr, Caps: caps(), Pandoc: s.pandocUsable(r.Context())}))
}
