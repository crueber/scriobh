package server

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// The export dialog. The form posts to a UI route (CSRF checked by
// uiRoute) that runs export.markdown and answers with the file. Script, when it
// is there, fetches the file instead so the page can say what was recorded;
// without it the browser simply downloads.

type exportData struct {
	P     projects.Project
	CSRF  string
	Error string
	Mode  string
	Notes bool
	// Format is markdown or html; PrivateNotes applies to html.
	Format       string
	PrivateNotes bool
	// Ghosts and Alternatives are keep or omit (Markdown only; empty is keep).
	Ghosts, Alternatives string
	// Excluded is how documents marked do not compile are treated: "" the default for the layout, "yes" or "no".
	Excluded string
	// Lang is the language of the interface, for the line the script shows after a download.
	Lang string
}

// SkippedText is the line the dialog shows after a download that left documents out; {n} is the count.
func (d exportData) SkippedText() string {
	if d.Lang == "" {
		d.Lang = "en"
	}
	return i18n.Default.T(d.Lang, "exp.skipped", "n", "{n}")
}

func urlQuery(s string) string { return url.QueryEscape(s) }

func (s *Server) uiExportForm(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	pr, err := s.projectFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if pr.Role != ops.ProjectOwner && pr.Role != ops.ProjectEditor {
		s.fail(w, r, ops.Forbidden("Only an owner or an editor can export a project."))
		return
	}
	d := exportData{P: pr, CSRF: authFrom(r.Context()).CSRF, Mode: "single", Lang: s.page(r, "", nil).Lang}
	if !isHTMX(r) {
		s.render(w, r, http.StatusOK, "project-export", s.page(r, "Export "+pr.Title, d))
		return
	}
	s.fragment(w, r, http.StatusOK, "project-export", "project-export", d)
}

func (s *Server) uiExport(w http.ResponseWriter, r *http.Request) {
	pr, err := s.projectFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	mode, notes := r.PostFormValue("mode"), r.PostFormValue("notes") == "true"
	ghosts, alts := r.PostFormValue("ghosts"), r.PostFormValue("alternatives")
	excl := r.PostFormValue("include_excluded")
	var mdExcl *bool
	if excl == "yes" || excl == "no" {
		v := excl == "yes"
		mdExcl = &v
	}
	var in any = export.MarkdownIn{ProjectID: pr.ID, Mode: mode, Notes: notes, Ghosts: ghosts, Alternatives: alts, IncludeExcluded: mdExcl}
	op := "export.markdown"
	if r.PostFormValue("format") == "html" {
		op = "export.html"
		in = export.HTMLIn{ProjectID: pr.ID, Mode: mode, Notes: notes, PrivateNotes: r.PostFormValue("private_notes") == "true", Ghosts: ghosts, IncludeExcluded: excl == "yes"}
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), op, in)
	if err != nil {
		e := ops.AsError(err)
		if e.Code == ops.CodeInvalid && !isFetch(r) {
			d := exportData{P: pr, CSRF: authFrom(r.Context()).CSRF, Error: e.Message, Mode: mode, Notes: notes, Format: r.PostFormValue("format"), PrivateNotes: r.PostFormValue("private_notes") == "true", Ghosts: ghosts, Alternatives: alts, Excluded: excl, Lang: s.page(r, "", nil).Lang}
			s.render(w, r, http.StatusUnprocessableEntity, "project-export", s.page(r, "Export "+pr.Title, d))
			return
		}
		s.fail(w, r, err)
		return
	}
	f := out.(export.File)
	w.Header().Set("X-Export-Id", f.ExportID)
	w.Header().Set("X-Export-Documents", strconv.Itoa(f.Documents))
	if f.Skipped > 0 { // documents marked do not compile: the dialog can say so
		w.Header().Set("X-Export-Skipped", strconv.Itoa(f.Skipped))
	}
	w.Header().Set("X-Export-Sha256", f.SHA256)
	writeDownload(w, f)
}
