package server

import (
	"github.com/crueber/scriobh/internal/auth"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
)

// The project menu's line about the Markdown copy (the mirror), and its Rebuild button.
// The words are written for a person, not for the file system: whether the copy is
// current, when a file last changed, and what went wrong if something did.

type mirrorView struct {
	State      string // ok, pending, error, off, none
	Line       string
	Detail     string
	CanRebuild bool
}

// userZone is the profile's time zone, UTC when it is empty or unknown.
func (s *Server) userZone(r *http.Request) *time.Location {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "profile.get", account.EmptyIn{})
	if err != nil {
		return time.UTC
	}
	loc, err := time.LoadLocation(out.(account.ProfileItem).Item.Timezone)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}

func whenIn(ts string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ""
	}
	return t.In(loc).Format("2 Jan 2006, 15:04")
}

func fileCount(n int) string {
	if n == 1 {
		return "1 file"
	}
	return strconv.Itoa(n) + " files"
}

// mirrorSummary turns a status into a line a person can read.
func mirrorSummary(st mirror.Status, loc *time.Location, canEdit bool) mirrorView {
	v := mirrorView{CanRebuild: canEdit && st.Enabled}
	switch {
	case !st.Enabled:
		v.State = "off"
		v.Line = "The Markdown copy is switched off on this server."
	case st.LastError != "":
		v.State = "error"
		v.Line = "The Markdown copy could not be written."
		v.Detail = strings.TrimRight(st.LastError, ". ") + "."
		if at := whenIn(st.NextRetryAt, loc); at != "" {
			v.Detail += " Trying again at " + at + "."
		}
	case st.Pending:
		v.State = "pending"
		v.Line = "The Markdown copy is being brought up to date."
		if st.Files > 0 {
			v.Detail = fileCount(st.Files) + " so far."
		}
	case st.InSync:
		v.State = "ok"
		v.Line = "The Markdown copy is up to date."
		v.Detail = fileCount(st.Files)
		if at := whenIn(st.LastWriteAt, loc); at != "" {
			v.Detail += ", last changed " + at + "."
		} else {
			v.Detail += "."
		}
	default:
		v.State = "none"
		v.Line = "The Markdown copy has not been written yet."
		v.Detail = "It is written a few seconds after a change."
	}
	return v
}

// mirrorFor loads the status for a project's menu. It returns nil when the mirror is not
// part of this server at all (a build without it), so the menu simply omits the line.
func (s *Server) mirrorFor(r *http.Request, projectID string, canEdit bool) *mirrorView {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "mirror.status", mirror.StatusIn{ProjectID: projectID})
	if err != nil {
		return nil
	}
	v := mirrorSummary(out.(mirror.Item).Item, s.userZone(r), canEdit)
	return &v
}

// uiMirrorRebuild is POST /ui/projects/{id}/mirror/rebuild: rewrite the copy now, then show the menu again.
func (s *Server) uiMirrorRebuild(w http.ResponseWriter, r *http.Request) {
	pr, err := s.projectFor(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "mirror.rebuild", mirror.StatusIn{ProjectID: pr.ID}); err != nil {
		s.fail(w, r, err)
		return
	}
	// A page that has its own Rebuild button (the Git page) asks to come back to itself.
	if b := safeBack(r.PostFormValue("back")); b != "" && !isHTMX(r) {
		http.Redirect(w, r, b, http.StatusSeeOther)
		return
	}
	td := tileData{P: pr, State: libraryState(r), CSRF: authFrom(r.Context()).CSRF}
	td.Mirror = s.mirrorFor(r, pr.ID, pr.Role == ops.ProjectOwner || pr.Role == ops.ProjectEditor)
	if !isHTMX(r) {
		http.Redirect(w, r, "/ui/projects/"+pr.ID+"/menu?view="+urlQuery(td.State), http.StatusSeeOther)
		return
	}
	s.fragment(w, r, http.StatusOK, "project-menu", "project-menu", td)
}

// safeBack returns a same-origin path under /p/ that is safe to redirect to, or
// "". It goes through auth.SafeNext, refuses control characters, and refuses
// dot segments (http.Redirect would clean "/p/../x" to "/x", outside /p/).
func safeBack(b string) string {
	if !strings.HasPrefix(b, "/p/") || strings.Contains(b, "//") || strings.Contains(b, "..") {
		return ""
	}
	for _, r := range b {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	if auth.SafeNext(b) != b {
		return ""
	}
	return b
}
