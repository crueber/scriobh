package gitmirror

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// Import from a repository.
//
// A repository import is the only path from a remote into Scríobh. It is
// stateful, because a clone is kept between the preview and the apply:
//
//  1. import.git_preview with an address starts a job. The clone runs on a
//     background worker (a request must not wait on a network transfer): a
//     shallow, single-branch, bare clone into data/imports/<job>/, never into
//     the mirror, stopped if it grows past SCRIOBH_GIT_MAX_REPO_MB.
//  2. import.git_job reports the job; when it is ready it carries a summary.
//  3. import.git_preview with the job id previews the mapping, with the
//     writer's edits, exactly as apply would create it. Only the two layouts the
//     importer knows are recognised (a Scríobh mirror and a generic Markdown
//     folder).
//  4. import.git_apply creates the project in one transaction, records the
//     address, the commit and a hash of every file as its source, and moves the
//     clone next to the project as its provenance copy.
//
// Files are read from the commit's tree, never from a working tree, so a link
// or a path in the repository cannot reach anything outside the clone. A job
// that is never applied is removed after a day.

const (
	jobTTL        = 24 * time.Hour
	maxOpenJobs   = 5
	cloneWorkers  = 2
	cloneTimeout  = 15 * time.Minute
	importsFolder = "imports"
)

// ImportSummary is what a ready job holds before any edits.
type ImportSummary struct {
	Layout       string   `json:"layout" desc:"markdown-folder or scriobh."`
	Documents    int      `json:"documents"`
	Words        int      `json:"words"`
	Files        int      `json:"files" desc:"Files in the repository (or the chosen folder)."`
	SkippedTotal int      `json:"skipped_total"`
	Token        string   `json:"token" desc:"The preview token of these files; send it to import.git_apply."`
	Warnings     []string `json:"warnings"`
}

// ImportJob is a repository import in progress.
type ImportJob struct {
	ID         string         `json:"id"`
	Status     string         `json:"status" enum:"running,preview,done,failed" desc:"running: cloning. preview: ready to preview and apply. done: a project was created. failed: see error."`
	SourceURL  string         `json:"source_url"`
	Branch     string         `json:"branch,omitempty"`
	Path       string         `json:"path,omitempty" desc:"The folder of the repository that is imported; empty for all of it."`
	CommitSHA  string         `json:"commit_sha,omitempty"`
	Error      string         `json:"error,omitempty"`
	ProjectID  string         `json:"project_id,omitempty"`
	Summary    *ImportSummary `json:"summary,omitempty"`
	CreatedAt  string         `json:"created_at"`
	FinishedAt string         `json:"finished_at,omitempty"`
}

type jobRow struct {
	ID, OwnerID, ProjectID, SourceURL, Branch, CommitSHA, Status, Token, ClonePath, Report, CreatedAt, FinishedAt, Error string
	Applying                                                                                                             bool
}

type jobReport struct {
	Path    string         `json:"path,omitempty"`
	Name    string         `json:"name,omitempty"`
	Summary *ImportSummary `json:"summary,omitempty"`
}

const jobCols = `id, owner_id, COALESCE(project_id,''), source_url, branch, commit_sha, status, token, clone_path, report_json, created_at, COALESCE(finished_at,''), error, applying`

func scanJob(sc scanner) (*jobRow, error) {
	var j jobRow
	var applying int
	err := sc.Scan(&j.ID, &j.OwnerID, &j.ProjectID, &j.SourceURL, &j.Branch, &j.CommitSHA, &j.Status, &j.Token, &j.ClonePath, &j.Report, &j.CreatedAt, &j.FinishedAt, &j.Error, &applying)
	j.Applying = applying != 0
	return &j, err
}

func (j *jobRow) view() ImportJob {
	var rep jobReport
	_ = json.Unmarshal([]byte(j.Report), &rep)
	return ImportJob{ID: j.ID, Status: j.Status, SourceURL: j.SourceURL, Branch: j.Branch, Path: rep.Path, CommitSHA: j.CommitSHA, Error: j.Error, ProjectID: j.ProjectID,
		Summary: rep.Summary, CreatedAt: j.CreatedAt, FinishedAt: j.FinishedAt}
}

// ---- op inputs ------------------------------------------------------------------

// GitPreviewIn starts an import job, or previews a ready one.
type GitPreviewIn struct {
	JobID       string            `json:"job_id,omitempty" desc:"A ready job: preview it (with edits) instead of starting a new one."`
	URL         string            `json:"url,omitempty" desc:"https://host/owner/repo.git. Public repositories need nothing more. An ssh address is not accepted here: import through a project's remote (remote_id), whose deploy key and host key are already set up."`
	RemoteID    string            `json:"remote_id,omitempty" desc:"Import the repository of one of your projects' remotes, with its stored credentials. You need to be the owner of that project: the clone uses its stored credentials."`
	Branch      string            `json:"branch,omitempty" desc:"Default: the remote's default branch (or the remote's configured branch with remote_id)."`
	Path        string            `json:"path,omitempty" desc:"Import only this folder of the repository, for a repository pushed with a folder prefix."`
	Username    string            `json:"username,omitempty" desc:"HTTPS user name, with token. Used for this clone only; never stored."`
	Token       string            `json:"token,omitempty" desc:"HTTPS token for a private repository. Write-only: used for this clone only, never stored or returned."`
	Name        string            `json:"name,omitempty" desc:"A name for the project when the repository says nothing better."`
	SplitScenes string            `json:"split_scenes,omitempty" enum:"none,h2"`
	Edits       []imports.Edit    `json:"edits,omitempty" desc:"With job_id: preview with these edits applied."`
	Reorder     []imports.Reorder `json:"reorder,omitempty" desc:"With job_id: preview with this reordering applied."`
}

// GitPreviewOut is a job and, for a ready job, its preview.
type GitPreviewOut struct {
	Job     ImportJob           `json:"job"`
	Preview *imports.PreviewOut `json:"preview,omitempty" desc:"Present when the job is ready and job_id was sent."`
}

// GitJobIn names a job.
type GitJobIn struct {
	JobID string `query:"job_id" json:"-" desc:"The import job."`
}

// GitJobOut wraps a job.
type GitJobOut struct {
	Item ImportJob `json:"item"`
}

// GitJobsOut lists the caller's jobs.
type GitJobsOut struct {
	Items []ImportJob `json:"items"`
}

// GitApplyIn creates the project from a ready job.
type GitApplyIn struct {
	JobID       string            `json:"job_id" desc:"The ready import job."`
	Token       string            `json:"token" desc:"The preview token (summary.token, or the preview's token) of the files in the clone."`
	Name        string            `json:"name,omitempty"`
	SplitScenes string            `json:"split_scenes,omitempty" enum:"none,h2"`
	Title       string            `json:"title,omitempty"`
	Kind        string            `json:"kind,omitempty" enum:"novel,novella,short,collection,nonfiction,essay,blog,report,blank"`
	Author      string            `json:"author,omitempty"`
	Language    string            `json:"language,omitempty"`
	Edits       []imports.Edit    `json:"edits,omitempty"`
	Reorder     []imports.Reorder `json:"reorder,omitempty"`
}

// GitDiscardOut says the job is gone.
type GitDiscardOut struct {
	Discarded bool `json:"discarded"`
}

func registerImport(r *ops.Registry) {
	ops.Register(r, ops.Op{NoDryRun: true, SecretInput: true, Name: "import.git_preview", Method: "POST", Path: "/api/v1/imports/git/preview",
		Desc: "Import a Git repository as a new project, step one. With url (https) or remote_id (a remote of a project you own) it starts a job: a shallow clone into data/imports/ on a background worker, capped by SCRIOBH_GIT_MAX_REPO_MB; poll import.git_job. With job_id of a ready job it previews the mapping with your edits (a Scríobh mirror via scriobh.json or project.json, or a generic Markdown folder). Writes nothing to any project.",
		Auth: ops.Writer()}, gitPreview)
	ops.Register(r, ops.Op{Name: "import.git_job", Method: "GET", Path: "/api/v1/imports/git",
		Desc: "One repository import job: running (cloning), preview (ready), done or failed, with a summary when ready. Only the person who started it can see it.",
		Auth: ops.Writer()}, gitJob)
	ops.Register(r, ops.Op{Name: "import.git_jobs", Method: "GET", Path: "/api/v1/imports/git/jobs",
		Desc: "Your repository import jobs, newest first. Jobs that were never applied are removed after a day.",
		Auth: ops.Writer()}, gitJobs)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "import.git_apply", Method: "POST", Path: "/api/v1/imports/git", Status: 201,
		Desc: "Import a Git repository as a new project, final step: create the project from a ready job in one transaction, record the address, the commit and the hash of every file as its source, and keep the clone beside the project as the provenance copy. Send the preview token.",
		Auth: ops.Writer()}, gitApply)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "import.git_discard", Permanent: true, Method: "DELETE", Path: "/api/v1/imports/git",
		Desc: "Throw away an import job and its clone. A job that is still cloning cannot be discarded yet.",
		Auth: ops.Writer()}, gitDiscard)
}

func (s *Service) loadJob(ctx context.Context, owner, id string) (*jobRow, error) {
	if id == "" {
		return nil, ops.Invalid("job_id", "Name the import job with job_id.")
	}
	j, err := scanJob(s.o.DB.Read().QueryRowContext(ctx, `SELECT `+jobCols+` FROM import_jobs WHERE id = ? AND owner_id = ?`, id, owner))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ops.NotFound("import")
	}
	if err != nil {
		return nil, ops.Internal(err)
	}
	return j, nil
}

func (s *Service) jobDir(id string) string { return filepath.Join(s.o.DataDir, importsFolder, id) }

func gitPreview(c *ops.Ctx, in GitPreviewIn) (GitPreviewOut, error) {
	s, err := service(c)
	if err != nil {
		return GitPreviewOut{}, err
	}
	if in.JobID != "" {
		j, err := s.loadJob(c, c.P.UserID, in.JobID)
		if err != nil {
			return GitPreviewOut{}, err
		}
		out := GitPreviewOut{Job: j.view()}
		switch j.Status {
		case "failed":
			return out, ops.Conflict("This import failed: " + j.Error)
		case "running":
			return out, nil
		}
		files, rep, err := s.jobFiles(j)
		if err != nil {
			return GitPreviewOut{}, err
		}
		pv, err := imports.PreviewFiles(files, pick(in.Name, rep.Name), in.SplitScenes, in.Edits, in.Reorder)
		if err != nil {
			return GitPreviewOut{}, err
		}
		out.Preview = &pv
		return out, nil
	}
	if in.URL == "" && in.RemoteID == "" {
		return GitPreviewOut{}, ops.Invalid("url", "Send the repository's https address, or remote_id of a project remote.")
	}
	url, dial, branch, auth, scrub, err := s.importSource(c, in)
	if err != nil {
		return GitPreviewOut{}, err
	}
	if s.ctxOrNil() == nil {
		return GitPreviewOut{}, ops.Conflict("Git sync is not running on this server.")
	}
	sub, e := cleanSubdir(in.Path)
	if e != nil {
		return GitPreviewOut{}, failure("path", e)
	}
	rep, _ := json.Marshal(jobReport{Path: sub, Name: strings.TrimSpace(in.Name)})
	j := &jobRow{ID: db.NewID("imp"), OwnerID: c.P.UserID, SourceURL: url, Branch: branch, Status: "running", Report: string(rep),
		CreatedAt: stamp(s.o.Clock.Now())}
	j.ClonePath = path.Join(importsFolder, j.ID)
	// The count of open jobs, the disk check and the insert are one transaction
	// on the single writer, so two requests cannot both pass a check that only
	// one of them fits. A clone still being made is counted at the full cap: it
	// has not yet grown to what it will take.
	userDisk := s.userImportUsage(c, c.P.UserID)
	totalDisk := s.totalUsed()
	err = s.o.DB.WithTx(c, func(tx *sql.Tx) error {
		var open, running int
		if err := tx.QueryRowContext(c, `SELECT count(*), COALESCE(SUM(status = 'running'), 0) FROM import_jobs WHERE owner_id = ? AND status IN ('running','preview')`, c.P.UserID).Scan(&open, &running); err != nil {
			return err
		}
		if open >= maxOpenJobs {
			return ops.Conflict(fmt.Sprintf("You have %d imports waiting. Apply or discard one first.", open))
		}
		reserve := int64(running+1) * s.o.MaxRepoBytes
		if q := s.o.UserQuotaBytes; q > 0 && userDisk+reserve > q {
			return ops.Conflict(fmt.Sprintf("Your imports would use more than your share of the space for Git copies (%d MB, SCRIOBH_GIT_USER_QUOTA_MB). Discard an import, or wait for a kept copy to expire.", q>>20))
		}
		if q := s.o.TotalQuotaBytes; q > 0 && totalDisk+int64(running+1)*s.o.MaxRepoBytes > q {
			return ops.Conflict(fmt.Sprintf("The space this server keeps for Git copies is full (%d MB, SCRIOBH_GIT_TOTAL_QUOTA_MB). Nothing was started.", q>>20))
		}
		_, err := tx.ExecContext(c, `INSERT INTO import_jobs(id, owner_id, kind, source_url, branch, status, clone_path, report_json, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			j.ID, j.OwnerID, "git", j.SourceURL, j.Branch, j.Status, j.ClonePath, j.Report, j.CreatedAt)
		return err
	})
	if err != nil {
		if ae, ok := err.(*ops.Error); ok {
			return GitPreviewOut{}, ae
		}
		return GitPreviewOut{}, ops.Internal(err)
	}
	s.impWG.Add(1)
	go func() {
		defer s.impWG.Done()
		s.runClone(*j, dial, auth, scrub, sub, strings.TrimSpace(in.Name))
	}()
	c.Audit(ops.AuditEntry{Action: "import.git_preview", TargetType: "import", TargetID: j.ID, Detail: map[string]any{"url": url, "remote": in.RemoteID != ""}})
	return GitPreviewOut{Job: j.view()}, nil
}

func (s *Service) ctxOrNil() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctx
}

// importSource works out what to clone and with what. Credentials given for
// this clone are used once and not kept.
func (s *Service) importSource(c *ops.Ctx, in GitPreviewIn) (url, dial, branch string, auth transport.AuthMethod, scrub []string, err error) {
	branch, e := cleanBranchOrEmpty(in.Branch)
	if e != nil {
		return "", "", "", nil, nil, failure("branch", e)
	}
	if in.RemoteID != "" {
		r, lerr := s.loadRemoteByID(c, in.RemoteID)
		if lerr != nil {
			return "", "", "", nil, nil, ops.NotFound("remote")
		}
		role, ok := "", false
		if c.Env.ProjectRole != nil {
			role, ok = c.Env.ProjectRole(c, c.P.UserID, r.ProjectID)
		}
		if !ok || role != ops.ProjectOwner {
			return "", "", "", nil, nil, ops.NotFound("remote")
		}
		ep, pe := parseURL(r.URL, s.o.AllowFile)
		if pe != nil {
			return "", "", "", nil, nil, failure("url", pe)
		}
		cr, cerr := s.credentialsFor(c, r, ep)
		if cerr != nil {
			return "", "", "", nil, nil, failure("", classify(cerr, ep.Host, r.Name))
		}
		if branch == "" {
			branch = r.Branch
		}
		return r.URL, cr.url, branch, cr.auth, cr.scrub, nil
	}
	ep, pe := parseURL(in.URL, s.o.AllowFile)
	if pe != nil {
		return "", "", "", nil, nil, failure("url", pe)
	}
	switch ep.Proto {
	case "ssh":
		return "", "", "", nil, nil, ops.Invalid("url", "An ssh address needs a deploy key. Import through a project's remote (remote_id), which has one.")
	case "https":
		if in.Token != "" {
			tok, terr := checkToken(in.Token)
			if terr != nil {
				return "", "", "", nil, nil, terr
			}
			user, uerr := checkUsername(in.Username)
			if uerr != nil {
				return "", "", "", nil, nil, uerr
			}
			if user == "" {
				user = "git"
			}
			u := strings.TrimSpace(in.URL)
			return u, u, branch, &githttp.BasicAuth{Username: user, Password: tok}, []string{tok}, nil
		}
	}
	u := strings.TrimSpace(in.URL)
	return u, u, branch, nil, nil, nil
}

func cleanBranchOrEmpty(b string) (string, *Error) {
	if strings.TrimSpace(b) == "" {
		return "", nil
	}
	return cleanBranch(b)
}

// cleanSubdir validates the folder of the repository to import.
func cleanSubdir(p string) (string, *Error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	if len(p) > 300 || p != path.Clean(p) || strings.Contains(p, "\\") || strings.HasPrefix(p, "..") || strings.Contains(p, "/../") {
		return "", gerr(CodeBadURL, "That is not a folder of the repository.")
	}
	return p, nil
}

// runClone is the background half of the first step.
func (s *Service) runClone(j jobRow, dial string, auth transport.AuthMethod, scrub []string, sub, name string) {
	ctx := s.ctxOrNil()
	if ctx == nil {
		return
	}
	select {
	case s.cloneSem() <- struct{}{}:
		defer func() { <-s.cloneSem() }()
	case <-ctx.Done():
		return
	}
	bg, cancelBg := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancelBg()
	fail := func(msg string) {
		_ = os.RemoveAll(s.jobDir(j.ID))
		_, _ = s.o.DB.Writer().ExecContext(bg, `UPDATE import_jobs SET status = 'failed', error = ?, finished_at = ? WHERE id = ?`, msg, stamp(s.o.Clock.Now()), j.ID)
	}
	cctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	dir := s.jobDir(j.ID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		fail("The import folder could not be created.")
		return
	}
	gctx, done := guardSize(cctx, dir, s.o.MaxRepoBytes)
	repo, err := s.cloneBare(gctx, dir, dial, j.Branch, auth)
	tooBig := errors.Is(context.Cause(gctx), errTooBig)
	done()
	if err == nil && dirSize(dir) > s.o.MaxRepoBytes { // the watch polls; a fast transfer can finish between two looks
		tooBig, err = true, errTooBig
	}
	if err != nil {
		switch {
		case tooBig:
			fail(fmt.Sprintf("The repository is larger than the %d MB limit (SCRIOBH_GIT_MAX_REPO_MB). Nothing was imported.", s.o.MaxRepoBytes>>20))
		case errors.Is(err, transport.ErrEmptyRemoteRepository):
			fail("The repository is empty: there is nothing to import.")
		default:
			host := ""
			if ep, pe := parseURL(j.SourceURL, true); pe == nil {
				host = ep.Host
			}
			fail(classify(err, host, "this import", scrub...).Message)
		}
		return
	}
	ref, err := repo.Head()
	if err != nil {
		fail("The repository has no branch to import.")
		return
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		fail("The repository's head commit could not be read.")
		return
	}
	files, err := treeFiles2(commit, sub)
	if err != nil {
		fail(err.Error())
		return
	}
	pv, err := imports.PreviewFiles(files, name, "", nil, nil)
	if err != nil {
		fail(ops.AsError(err).Message)
		return
	}
	if pv.Documents == 0 {
		fail("There is no Markdown text in the repository to import (looked for .md files and a Scríobh mirror).")
		return
	}
	sum := &ImportSummary{Layout: pv.Layout, Documents: pv.Documents, Words: pv.Words, Files: len(files), SkippedTotal: pv.SkippedTotal, Token: pv.Token, Warnings: pv.Warnings}
	rep, _ := json.Marshal(jobReport{Path: sub, Name: name, Summary: sum})
	_, _ = s.o.DB.Writer().ExecContext(bg, `UPDATE import_jobs SET status = 'preview', commit_sha = ?, token = ?, report_json = ?, finished_at = ? WHERE id = ?`,
		commit.Hash.String(), pv.Token, string(rep), stamp(s.o.Clock.Now()), j.ID)
}

var cloneSemaphore = make(chan struct{}, cloneWorkers)

func (s *Service) cloneSem() chan struct{} { return cloneSemaphore }

// cloneBare makes a shallow, single-branch, bare clone. A server that does not
// offer shallow clones gets a full one, under the same size guard.
func (s *Service) cloneBare(ctx context.Context, dir, url, branch string, auth transport.AuthMethod) (*git.Repository, error) {
	var last error
	for _, depth := range []int{1, 0} {
		_ = os.RemoveAll(dir)
		opts := &git.CloneOptions{URL: url, Auth: auth, Depth: depth, SingleBranch: true, Tags: git.NoTags}
		if branch != "" {
			opts.ReferenceName = plumbing.NewBranchReferenceName(branch)
		}
		repo, err := git.PlainCloneContext(ctx, dir, true, opts)
		if err == nil {
			return repo, nil
		}
		last = err
		if ctx.Err() != nil || depth == 0 || !strings.Contains(strings.ToLower(err.Error()), "shallow") {
			break
		}
	}
	return nil, last
}

// treeFiles2 lists the files of a commit's tree (or of a folder of it) for the
// importer. Blobs are read only for Markdown and the manifests; links are
// listed as links and everything else by size, so the importer reports them
// as skipped without the bytes being read.
func treeFiles2(c *object.Commit, sub string) ([]imports.File, error) {
	tree, err := c.Tree()
	if err != nil {
		return nil, errors.New("The repository's files could not be read.")
	}
	if sub != "" {
		if tree, err = tree.Tree(sub); err != nil {
			return nil, fmt.Errorf("The folder %q is not in the repository.", sub)
		}
	}
	var out []imports.File
	var total int64
	err = tree.Files().ForEach(func(f *object.File) error {
		if len(out) >= imports.MaxListed {
			return fmt.Errorf("The repository lists more than %d files; the limit is %d. Import a folder of it (path) instead.", imports.MaxListed, imports.MaxListed)
		}
		low := strings.ToLower(f.Name)
		isMD := strings.HasSuffix(low, ".md") || strings.HasSuffix(low, ".markdown")
		isManifest := low == "scriobh.json" || low == "project.json"
		switch {
		case f.Mode == filemode.Symlink:
			out = append(out, imports.File{Path: f.Name, Size: -1})
		case (isMD && f.Size <= imports.MaxFileBytes) || (isManifest && f.Size <= imports.MaxManifest):
			rd, err := f.Reader()
			if err != nil {
				return err
			}
			b := make([]byte, 0, f.Size)
			buf := make([]byte, 32<<10)
			for {
				n, rerr := rd.Read(buf)
				b = append(b, buf[:n]...)
				if rerr != nil {
					break
				}
			}
			_ = rd.Close()
			total += int64(len(b))
			if total > imports.MaxTotalBytes+2*imports.MaxManifest {
				return fmt.Errorf("The Markdown in the repository totals more than %d MB; the limit is %d MB. Import a folder of it (path) instead.", imports.MaxTotalBytes>>20, imports.MaxTotalBytes>>20)
			}
			out = append(out, imports.File{Path: f.Name, Data: b, Size: int64(len(b))})
		default:
			out = append(out, imports.File{Path: f.Name, Size: max(f.Size, 1)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// jobFiles re-reads a ready job's files from its clone.
func (s *Service) jobFiles(j *jobRow) ([]imports.File, jobReport, error) {
	var rep jobReport
	_ = json.Unmarshal([]byte(j.Report), &rep)
	repo, err := git.PlainOpen(s.cloneDir(j))
	if err != nil {
		return nil, rep, ops.Conflict("The clone of this import is gone. Start the import again.")
	}
	commit, err := repo.CommitObject(plumbing.NewHash(j.CommitSHA))
	if err != nil {
		return nil, rep, ops.Conflict("The clone of this import is damaged. Start the import again.")
	}
	files, err := treeFiles2(commit, rep.Path)
	if err != nil {
		return nil, rep, ops.Invalid("path", err.Error())
	}
	return files, rep, nil
}

// cloneDir resolves a job's clone_path, which is relative to the data
// directory and must stay under imports/.
func (s *Service) cloneDir(j *jobRow) string {
	rel := filepath.Clean(filepath.FromSlash(j.ClonePath))
	if !strings.HasPrefix(rel, importsFolder+string(filepath.Separator)) || strings.Contains(rel, "..") {
		return filepath.Join(s.o.DataDir, importsFolder, "invalid")
	}
	return filepath.Join(s.o.DataDir, rel)
}

func gitJob(c *ops.Ctx, in GitJobIn) (GitJobOut, error) {
	s, err := service(c)
	if err != nil {
		return GitJobOut{}, err
	}
	j, err := s.loadJob(c, c.P.UserID, in.JobID)
	if err != nil {
		return GitJobOut{}, err
	}
	return GitJobOut{Item: j.view()}, nil
}

func gitJobs(c *ops.Ctx, _ struct{}) (GitJobsOut, error) {
	s, err := service(c)
	if err != nil {
		return GitJobsOut{}, err
	}
	rows, err := s.o.DB.Read().QueryContext(c, `SELECT `+jobCols+` FROM import_jobs WHERE owner_id = ? ORDER BY id DESC LIMIT 20`, c.P.UserID)
	if err != nil {
		return GitJobsOut{}, ops.Internal(err)
	}
	defer rows.Close()
	out := GitJobsOut{Items: []ImportJob{}}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return GitJobsOut{}, ops.Internal(err)
		}
		out.Items = append(out.Items, j.view())
	}
	return out, nil
}

func gitApply(c *ops.Ctx, in GitApplyIn) (imports.ApplyOut, error) {
	s, err := service(c)
	if err != nil {
		return imports.ApplyOut{}, err
	}
	j, err := s.loadJob(c, c.P.UserID, in.JobID)
	if err != nil {
		return imports.ApplyOut{}, err
	}
	switch j.Status {
	case "running":
		return imports.ApplyOut{}, ops.Conflict("The clone is still running. Wait until the import is ready.")
	case "done":
		return imports.ApplyOut{}, ops.Conflict("This import already created a project.")
	case "failed":
		return imports.ApplyOut{}, ops.Conflict("This import failed: " + j.Error)
	}
	// Claim the job. Two requests for one job cannot both pass this: the second
	// finds it already being applied.
	claim, err := s.o.DB.Writer().ExecContext(c, `UPDATE import_jobs SET applying = 1 WHERE id = ? AND owner_id = ? AND status = 'preview' AND applying = 0`, j.ID, c.P.UserID)
	if err != nil {
		return imports.ApplyOut{}, ops.Internal(err)
	}
	if n, _ := claim.RowsAffected(); n == 0 {
		return imports.ApplyOut{}, ops.Conflict("This import is already being applied, or is no longer ready.")
	}
	applied := false
	defer func() {
		if !applied {
			_, _ = s.o.DB.Writer().ExecContext(context.WithoutCancel(c), `UPDATE import_jobs SET applying = 0 WHERE id = ?`, j.ID)
		}
	}()
	files, rep, err := s.jobFiles(j)
	if err != nil {
		return imports.ApplyOut{}, err
	}
	name := pick(in.Name, rep.Name)
	prov := imports.Provenance{Kind: "git", URL: j.SourceURL, Commit: j.CommitSHA, Path: repoName(j.SourceURL),
		Meta: map[string]any{"branch": j.Branch, "subdir": rep.Path, "job_id": j.ID}}
	out, err := imports.ApplyFiles(c, files, imports.ApplyIn{Token: in.Token, Name: name, SplitScenes: in.SplitScenes, Title: in.Title, Kind: in.Kind,
		Author: in.Author, Language: in.Language, Edits: in.Edits, Reorder: in.Reorder}, prov)
	if err != nil {
		return imports.ApplyOut{}, err
	}
	applied = true
	// Keep the clone beside the project as its provenance copy. The folder is
	// named for the project so a purge can remove it without a database row.
	kept := path.Join(importsFolder, importCopyName(out.ProjectID))
	now := stamp(s.o.Clock.Now())
	if rerr := os.Rename(s.cloneDir(j), filepath.Join(s.o.DataDir, filepath.FromSlash(kept))); rerr != nil {
		s.log.Warn("git: the clone of an import could not be kept", "err", cause(rerr))
		_ = os.RemoveAll(s.cloneDir(j))
		kept = ""
	}
	_, _ = s.o.DB.Writer().ExecContext(c, `UPDATE project_sources SET kept_copy_path = ? WHERE id = ?`, kept, out.SourceID)
	_, _ = s.o.DB.Writer().ExecContext(c, `UPDATE import_jobs SET status = 'done', applying = 0, project_id = ?, clone_path = ?, finished_at = ? WHERE id = ?`, out.ProjectID, kept, now, j.ID)
	return out, nil
}

// repoName is the repository's name as shown for a source: the last element of
// the address without .git.
func repoName(url string) string {
	u := strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	if len(u) > 200 {
		u = u[:200]
	}
	return u
}

func gitDiscard(c *ops.Ctx, in GitJobIn) (GitDiscardOut, error) {
	s, err := service(c)
	if err != nil {
		return GitDiscardOut{}, err
	}
	j, err := s.loadJob(c, c.P.UserID, in.JobID)
	if err != nil {
		return GitDiscardOut{}, err
	}
	if j.Status == "running" || j.Applying {
		return GitDiscardOut{}, ops.Conflict("The import is still being made or applied. Discard it when it has finished.")
	}
	// A finished import's clone is the provenance copy beside its project:
	// discarding the job discards that copy too, and the source record stops
	// pointing at it.
	if rel := j.ClonePath; rel != "" {
		_ = os.RemoveAll(s.cloneDir(j))
		_, _ = s.o.DB.Writer().ExecContext(c, `UPDATE project_sources SET kept_copy_path = '' WHERE kept_copy_path = ?`, rel)
	}
	if _, err := s.o.DB.Writer().ExecContext(c, `DELETE FROM import_jobs WHERE id = ? AND owner_id = ?`, j.ID, c.P.UserID); err != nil {
		return GitDiscardOut{}, ops.Internal(err)
	}
	return GitDiscardOut{Discarded: true}, nil
}

// recoverImports closes jobs a stopped process left cloning.
func (s *Service) recoverImports(ctx context.Context) {
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT `+jobCols+` FROM import_jobs WHERE status = 'running'`)
	if err != nil {
		return
	}
	var js []*jobRow
	for rows.Next() {
		if j, err := scanJob(rows); err == nil {
			js = append(js, j)
		}
	}
	rows.Close()
	for _, j := range js {
		_ = os.RemoveAll(s.cloneDir(j))
		_, _ = s.o.DB.Writer().ExecContext(ctx, `UPDATE import_jobs SET status = 'failed', error = ?, finished_at = ? WHERE id = ?`,
			"The server stopped while this was cloning. Start the import again.", stamp(s.o.Clock.Now()), j.ID)
	}
	// A job that was being applied when the server stopped: if its project was
	// created, the job is done; if not, it is ready again.
	arows, err := s.o.DB.Read().QueryContext(ctx, `SELECT id FROM import_jobs WHERE applying = 1 AND status = 'preview'`)
	if err != nil {
		return
	}
	var ids []string
	for arows.Next() {
		var id string
		if arows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	arows.Close()
	for _, id := range ids {
		var pid string
		_ = s.o.DB.Read().QueryRowContext(ctx, `SELECT project_id FROM project_sources WHERE kind = 'git' AND metadata_json LIKE ? LIMIT 1`, `%"job_id":"`+id+`"%`).Scan(&pid)
		if pid != "" {
			_, _ = s.o.DB.Writer().ExecContext(ctx, `UPDATE import_jobs SET status = 'done', applying = 0, project_id = ?, finished_at = ? WHERE id = ?`, pid, stamp(s.o.Clock.Now()), id)
		} else {
			_, _ = s.o.DB.Writer().ExecContext(ctx, `UPDATE import_jobs SET applying = 0 WHERE id = ?`, id)
		}
	}
}

// sweepImports removes jobs that were never applied after a day, with their clones.
func (s *Service) sweepImports(ctx context.Context) {
	cutoff := stamp(s.o.Clock.Now().Add(-jobTTL))
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT `+jobCols+` FROM import_jobs WHERE status IN ('preview','failed') AND applying = 0 AND created_at < ?`, cutoff)
	if err != nil {
		return
	}
	var js []*jobRow
	for rows.Next() {
		if j, err := scanJob(rows); err == nil {
			js = append(js, j)
		}
	}
	rows.Close()
	for _, j := range js {
		_ = os.RemoveAll(s.cloneDir(j))
		_, _ = s.o.DB.Writer().ExecContext(ctx, `DELETE FROM import_jobs WHERE id = ?`, j.ID)
	}
	s.expireKeptImports(ctx)
}

func importCopyName(projectID string) string { return "p-" + projectID }

// pick returns the first value that is not blank.
func pick(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
