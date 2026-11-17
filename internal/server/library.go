package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The library: a shelf of covers with progress, pin, archive and trash, the
// archived and trash views, and Continue where I left off. Actions are htmx
// forms that answer with the refreshed shelf; without JavaScript the same forms
// post normally and redirect back.

// libraryData is what the shelf renders.
type libraryData struct {
	Projects []projects.Project
	State    string // active, archived or trashed
	Q        string
	Continue *projects.Project
	CSRF     string
	IsWriter bool
	Counts   map[string]int // not loaded for every view; zero means "unknown"
	// Lock and Unlock are the words on the tile of a project that has a passphrase this session has not unlocked: only its
	// title is shown, never a count (F-252). Empty when no project in the list is locked.
	Lock, Unlock string
}

// tileData is one tile's context.
type tileData struct {
	P      projects.Project
	State  string
	CSRF   string
	Mirror *mirrorView // the project menu's line about the Markdown copy; nil when there is none to show
	// LockWord is the menu's "Lock now", for a project that has a passphrase this session has open (F-252).
	LockWord string
}

func libraryState(r *http.Request) string {
	switch s := r.FormValue("view"); s {
	case projects.StateArchived, projects.StateTrashed:
		return s
	}
	switch s := r.URL.Query().Get("state"); s {
	case projects.StateArchived, projects.StateTrashed:
		return s
	}
	return projects.StateActive
}

func (s *Server) libraryData(r *http.Request, state, q string) (libraryData, error) {
	p := s.principal(r)
	out, err := s.Reg.Call(r.Context(), p, "project.list", projects.ListIn{Q: q, State: state})
	if err != nil {
		return libraryData{}, err
	}
	d := libraryData{Projects: addressedProjects(out.(projects.ListOut).Items), State: state, Q: q, CSRF: authFrom(r.Context()).CSRF,
		IsWriter: p.Role == ops.RoleAdmin || p.Role == ops.RoleWriter}
	for _, pr := range d.Projects {
		if pr.Locked {
			d.Lock, d.Unlock = s.tr(r, "project.gate.tile"), s.tr(r, "project.gate.unlock")
			break
		}
	}
	if state == projects.StateActive && q == "" {
		best := -1
		for i, pr := range d.Projects {
			if pr.LastOpenedAt != "" && (best < 0 || pr.LastOpenedAt > d.Projects[best].LastOpenedAt) {
				best = i
			}
		}
		if best >= 0 {
			c := d.Projects[best]
			d.Continue = &c
		}
	}
	return d, nil
}

// shelf answers an action: the refreshed shelf for htmx, a redirect otherwise.
func (s *Server) shelf(w http.ResponseWriter, r *http.Request, state string, trigger map[string]any) {
	if !isHTMX(r) {
		dest := "/"
		if state != projects.StateActive {
			dest = "/?state=" + url.QueryEscape(state)
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
		return
	}
	d, err := s.libraryData(r, state, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(trigger) > 0 {
		b, _ := json.Marshal(trigger)
		w.Header().Set("HX-Trigger", asciiJSON(b))
	}
	s.fragment(w, r, http.StatusOK, "shelf", "shelf", d)
}

func toast(message, undo string) map[string]any {
	t := map[string]any{"message": message}
	if undo != "" {
		t["undo"] = undo
	}
	return map[string]any{"scriobh:toast": t, "scriobh:close": true}
}

// project loads a project through project.get so access is checked as usual.
func (s *Server) projectFor(r *http.Request) (projects.Project, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.get", projects.GetIn{ID: r.PathValue("id")})
	if err != nil {
		return projects.Project{}, err
	}
	return out.(projects.Item).Item.Addressed(), nil
}

func (s *Server) uiProjectMenu(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	pr, err := s.projectFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	td := tileData{P: pr, State: libraryState(r), CSRF: authFrom(r.Context()).CSRF}
	if pr.Protected && !pr.Locked {
		td.LockWord = s.tr(r, "project.gate.lock_now")
	}
	if !pr.Trashed {
		td.Mirror = s.mirrorFor(r, pr.ID, pr.Role == ops.ProjectOwner || pr.Role == ops.ProjectEditor)
	}
	if !isHTMX(r) {
		s.render(w, r, http.StatusOK, "project-actions", s.page(r, pr.Title, td))
		return
	}
	s.fragment(w, r, http.StatusOK, "project-menu", "project-menu", td)
}

func (s *Server) uiProjectPin(w http.ResponseWriter, r *http.Request) {
	pr, err := s.projectFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	want := r.PostFormValue("pinned") == "true"
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "project.pin", projects.PinIn{ID: pr.ID, Pinned: want}); err != nil {
		s.fail(w, r, err)
		return
	}
	s.shelf(w, r, libraryState(r), nil)
}

// uiProjectMove runs archive, unarchive, trash or restore.
func (s *Server) uiProjectMove(op, verb string, undo string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pr, err := s.projectFor(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if _, err := s.Reg.Call(r.Context(), s.principal(r), op, projects.StateIn{ID: pr.ID}); err != nil {
			s.fail(w, r, err)
			return
		}
		state := libraryState(r)
		var t map[string]any
		if undo != "" {
			t = toast(verb+" “"+pr.Title+"”.", "/ui/projects/"+pr.ID+"/"+undo+"?view="+url.QueryEscape(state))
		} else {
			t = toast(verb+" “"+pr.Title+"”.", "")
		}
		s.shelf(w, r, state, t)
	}
}

// The purge confirmation: a typed title, because this one cannot be undone.
type purgeData struct {
	P     projects.Project
	CSRF  string
	Error string
	Typed string
}

func (s *Server) uiProjectPurgeForm(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	pr, err := s.projectFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if pr.Role != ops.ProjectOwner {
		s.fail(w, r, ops.Forbidden("Only the owner can delete a project for good."))
		return
	}
	pd := purgeData{P: pr, CSRF: authFrom(r.Context()).CSRF}
	if !isHTMX(r) {
		s.render(w, r, http.StatusOK, "project-purge", s.page(r, "Delete "+pr.Title, pd))
		return
	}
	s.fragment(w, r, http.StatusOK, "project-menu", "project-purge", pd)
}

func (s *Server) uiProjectPurge(w http.ResponseWriter, r *http.Request) {
	pr, err := s.projectFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	typed := r.PostFormValue("confirm_title")
	_, err = s.Reg.Call(r.Context(), s.principal(r), "project.purge", projects.PurgeIn{ID: pr.ID, ConfirmTitle: typed})
	if err != nil {
		e := ops.AsError(err)
		if e.Code == ops.CodeInvalid || e.Code == ops.CodeConflict {
			pd := purgeData{P: pr, CSRF: authFrom(r.Context()).CSRF, Error: e.Message, Typed: typed}
			if !isHTMX(r) {
				s.render(w, r, http.StatusUnprocessableEntity, "project-purge", s.page(r, "Delete "+pr.Title, pd))
				return
			}
			s.fragment(w, r, http.StatusUnprocessableEntity, "project-menu", "project-purge", pd)
			return
		}
		s.fail(w, r, err)
		return
	}
	dest := "/?state=" + projects.StateTrashed
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", dest)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// libraryTitle is the page heading for a view.
func libraryTitle(state string) string {
	switch state {
	case projects.StateArchived:
		return "Archived"
	case projects.StateTrashed:
		return "Trash"
	}
	return "Library"
}

func trimQ(r *http.Request) string { return strings.TrimSpace(r.URL.Query().Get("q")) }

// asciiJSON escapes every non-ASCII character as \uXXXX: HTTP header values are
// Latin-1 at best, and a title with a curly quote or an accent would arrive as mojibake.
func asciiJSON(b []byte) string {
	var sb strings.Builder
	for _, r := range string(b) {
		switch {
		case r < 0x7f:
			sb.WriteRune(r)
		case r > 0xffff:
			r -= 0x10000
			sb.WriteString(fmt.Sprintf("\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff)))
		default:
			sb.WriteString(fmt.Sprintf("\\u%04x", r))
		}
	}
	return sb.String()
}

// addressedProjects gives every project its address as its Slug, so the pages built from the list link to the right one
// when two projects the person can read share a slug.
func addressedProjects(items []projects.Project) []projects.Project {
	out := make([]projects.Project, len(items))
	for i, p := range items {
		out[i] = p.Addressed()
	}
	return out
}
