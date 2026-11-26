package server

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// The page that imports a Git repository as a new project. The page holds the form and the list of the person's unfinished imports.
// The form starts the clone (import.git_preview); the review is the draft's page, which follows the fetch (importshell.go).
// Nothing here writes but through the ops.

type repoRemoteOpt struct {
	ID, Label, Branch string
}

type repoJobView struct {
	ID, Status, URL, Branch, Path, Commit, Error, When string
	DraftID                                            string
	Slug                                               string // the project a finished import made, when it can be named
	Documents, Words                                   int
	Summary                                            bool
}

type repoImportData struct {
	Remotes     []repoRemoteOpt
	Pre         string // a remote chosen by ?project=
	PreProject  string
	Jobs        []repoJobView
	Unavailable string // why importing is off here, in words
	Caps        importCaps
	RepoMiB     int // the largest repository that is fetched (SCRIOBH_GIT_MAX_REPO_MB)
	Error, Hint string
	Vals        map[string]string // what the person typed, but never the token
	Remote      bool              // the remote radio was chosen
}

func (s *Server) registerImportRepo(m *http.ServeMux) {
	m.HandleFunc("GET /import/git", s.importRepoPage)
}

// importRepoPage is GET /import/git. Instance writers only. ?project=slug preselects that project's remote
// (its owner only), which is where the Git page sends a person whose remote has diverged.
func (s *Server) importRepoPage(w http.ResponseWriter, r *http.Request) {
	s.importGitPage(w, r, http.StatusOK, "", "")
}

// importGitPage draws the page, with the reason the last start did not work, and the advice for it, when there is one.
func (s *Server) importGitPage(w http.ResponseWriter, r *http.Request, status int, errMsg, code string) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	if p.Role != ops.RoleAdmin && p.Role != ops.RoleWriter {
		s.fail(w, r, ops.Forbidden("Your account can read projects but not create them."))
		return
	}
	d := repoImportData{Caps: caps(), RepoMiB: s.Cfg.GitMaxRepoMB, Error: errMsg, Vals: map[string]string{}}
	if errMsg != "" {
		d.Hint = s.importGitHint(r, code, errMsg)
		for _, k := range []string{"url", "branch", "path", "username", "remote_id", "split_scenes"} {
			d.Vals[k] = strings.TrimSpace(r.PostFormValue(k))
		}
		d.Remote = r.PostFormValue("rg-src") == "remote"
	}
	loc := s.userZone(r)
	jo, err := s.Reg.Call(r.Context(), p, "import.git_jobs", struct{}{})
	if err != nil {
		d.Unavailable = ops.AsError(err).Message
	} else {
		for _, j := range jo.(gitmirror.GitJobsOut).Items {
			v := repoJobView{ID: j.ID, Status: j.Status, URL: j.SourceURL, Branch: j.Branch, Path: j.Path, Commit: shortSHA(j.CommitSHA), Error: j.Error, When: whenIn(j.CreatedAt, loc), DraftID: j.DraftID}
			if j.Summary != nil {
				v.Summary, v.Documents, v.Words = true, j.Summary.Documents, j.Summary.Words
			}
			if j.Status == "done" && j.ProjectID != "" {
				if po, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: j.ProjectID}); err == nil {
					v.Slug = po.(projects.Item).Item.Addressed().Slug
				}
			}
			d.Jobs = append(d.Jobs, v)
		}
	}
	// The remotes of the projects this person owns: importing through one uses its stored credentials.
	if lo, err := s.Reg.Call(r.Context(), p, "project.list", projects.ListIn{}); err == nil {
		for _, pr := range addressedProjects(lo.(projects.ListOut).Items) {
			if pr.Role != ops.ProjectOwner {
				continue
			}
			ro, err := s.Reg.Call(r.Context(), p, "git.remote.list", gitmirror.ProjectIn{ProjectID: pr.ID})
			if err != nil {
				continue
			}
			for _, rm := range ro.(gitmirror.RemoteList).Items {
				d.Remotes = append(d.Remotes, repoRemoteOpt{ID: rm.ID, Branch: rm.Branch, Label: pr.Title + " · " + rm.Name + " (" + rm.Host + ")"})
			}
		}
	}
	if slug := r.URL.Query().Get("project"); slug != "" && r.Method == http.MethodGet {
		pid, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, slug)
		if !ok {
			s.fail(w, r, ops.NotFound("project"))
			return
		}
		po, err := s.Reg.Call(r.Context(), p, "project.get", projects.GetIn{ID: pid})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		pr := po.(projects.Item).Item.Addressed()
		if pr.Trashed {
			s.fail(w, r, ops.NotFound("project"))
			return
		}
		if pr.Role != ops.ProjectOwner {
			s.fail(w, r, ops.Forbidden("Only the project's owner can import through its remote."))
			return
		}
		ro, err := s.Reg.Call(r.Context(), p, "git.remote.list", gitmirror.ProjectIn{ProjectID: pid})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		items := ro.(gitmirror.RemoteList).Items
		if len(items) == 0 {
			s.fail(w, r, ops.NotFound("remote"))
			return
		}
		d.Pre, d.PreProject = items[0].ID, pr.Title
	}
	s.render(w, r, status, "import-repo", s.page(r, s.tr(r, "import.page_title_git"), d))
}

// importGitHint is the advice for what went wrong starting or following a fetch: by the error's own code, else by what it says.
func (s *Server) importGitHint(r *http.Request, code, msg string) string {
	known := map[string]bool{"address_blocked": true, "auth_failed": true, "repo_not_found": true, "unreachable": true, "timeout": true, "too_large": true, "host_key_unpinned": true,
		"host_key_changed": true, "credentials_missing": true, "credentials_unreadable": true, "empty": true, "layout": true, "invalid": true, "forbidden": true, "not_found": true}
	if known[code] {
		return s.tr(r, "import.hint_"+code)
	}
	for _, m := range []struct{ re, code string }{{"not one this server may connect to", "address_blocked"}, {"refused the (credentials|deploy key)", "auth_failed"}, {"does not exist, or that", "repo_not_found"},
		{"could not be reached", "unreachable"}, {"did not answer in time", "timeout"}, {"larger than the|sent more data", "too_large"}, {"repository is empty", "empty"},
		{"no Markdown text|no branch to import|head commit|files could not be read|is not in the repository|lists more than|totals more than", "layout"}} {
		if regexp.MustCompile(m.re).MatchString(msg) {
			return s.tr(r, "import.hint_"+m.code)
		}
	}
	return ""
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return strings.TrimSpace(s)
}
