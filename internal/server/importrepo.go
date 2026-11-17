package server

import (
	"net/http"
	"strings"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// The page that imports a Git repository as a new project. The page holds the form and the list of
// the person's unfinished imports; the script (js/import-repo.js, with js/import.js for the preview)
// starts the clone job, polls it, and applies it, all over the import.git_* ops. Nothing here writes.

type repoRemoteOpt struct {
	ID, Label, Branch string
}

type repoJobView struct {
	ID, Status, URL, Branch, Path, Commit, Error, When string
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
}

func (s *Server) registerImportRepo(m *http.ServeMux) {
	m.HandleFunc("GET /import/git", s.importRepoPage)
}

// importRepoPage is GET /import/git. Instance writers only. ?project=slug preselects that project's remote
// (its owner only), which is where the Git page sends a person whose remote has diverged.
func (s *Server) importRepoPage(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	if p.Role != ops.RoleAdmin && p.Role != ops.RoleWriter {
		s.fail(w, r, ops.Forbidden("Your account can read projects but not create them."))
		return
	}
	d := repoImportData{Caps: caps(), RepoMiB: s.Cfg.GitMaxRepoMB}
	loc := s.userZone(r)
	jo, err := s.Reg.Call(r.Context(), p, "import.git_jobs", struct{}{})
	if err != nil {
		d.Unavailable = ops.AsError(err).Message
	} else {
		for _, j := range jo.(gitmirror.GitJobsOut).Items {
			v := repoJobView{ID: j.ID, Status: j.Status, URL: j.SourceURL, Branch: j.Branch, Path: j.Path, Commit: shortSHA(j.CommitSHA), Error: j.Error, When: whenIn(j.CreatedAt, loc)}
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
	if slug := r.URL.Query().Get("project"); slug != "" {
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
	s.render(w, r, http.StatusOK, "import-repo", s.page(r, "Import from a Git repository", d))
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return strings.TrimSpace(s)
}
