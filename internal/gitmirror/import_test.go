package gitmirror_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// makeRepo writes files into a new repository with one commit on main and
// returns its file:// address.
func makeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "source")
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main")))
	wt, _ := repo.Worktree()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := wt.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := wt.Commit("A commit", &git.CommitOptions{Author: &object.Signature{Name: "A", Email: "a@example.org", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	return "file://" + filepath.Join(dir, ".git") // the .git folder: the in-process loader reads a bare layout
}

// waitJob polls the job until it stops running.
func (h *harness) waitJob(p ops.Principal, id string) gitmirror.ImportJob {
	h.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		j := h.must(p, "import.git_job", gitmirror.GitJobIn{JobID: id}).(gitmirror.GitJobOut).Item
		if j.Status != "running" {
			return j
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("the job is still running: %+v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) startImport(p ops.Principal, in gitmirror.GitPreviewIn) gitmirror.ImportJob {
	h.t.Helper()
	return h.must(p, "import.git_preview", in).(gitmirror.GitPreviewOut).Job
}

func TestAPushedProjectComesBackThroughAnImport(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	commit := head(t, dir, "main").String()

	job := h.startImport(h.owner, gitmirror.GitPreviewIn{URL: url})
	if job.Status != "running" || job.SourceURL != url {
		t.Fatalf("job = %+v", job)
	}
	job = h.waitJob(h.owner, job.ID)
	if job.Status != "preview" || job.CommitSHA != commit || job.Summary == nil || job.Summary.Layout != "scriobh" || job.Summary.Documents != 3 || job.Summary.Words != 10 {
		t.Fatalf("job = %+v summary %+v", job, job.Summary)
	}
	pv := h.must(h.owner, "import.git_preview", gitmirror.GitPreviewIn{JobID: job.ID}).(gitmirror.GitPreviewOut).Preview
	if pv == nil || pv.Layout != "scriobh" || pv.Documents != 3 || pv.Token != job.Summary.Token {
		t.Fatalf("preview = %+v", pv)
	}
	if pv.Project.Title != "The Salt Road" {
		t.Errorf("title = %q", pv.Project.Title)
	}

	// Another person cannot see or apply the job.
	if _, err := h.call(h.editor, "import.git_job", gitmirror.GitJobIn{JobID: job.ID}); h.code(err) != ops.CodeNotFound {
		t.Errorf("another person's job: %v", err)
	}
	// A wrong token is refused: the files are not the ones previewed.
	if _, err := h.call(h.owner, "import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: "imp_wrong"}); h.code(err) != ops.CodeConflict {
		t.Errorf("wrong token: %v", err)
	}

	out := h.must(h.owner, "import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: pv.Token}).(imports.ApplyOut)
	if out.Documents != 3 || out.Title != "The Salt Road" || out.ProjectID == h.pid {
		t.Fatalf("apply = %+v", out)
	}
	var kind, srcURL, sha, kept, meta string
	if err := h.d.Read().QueryRow(`SELECT kind, source_url, commit_sha, kept_copy_path, metadata_json FROM project_sources WHERE id = ?`, out.SourceID).Scan(&kind, &srcURL, &sha, &kept, &meta); err != nil {
		t.Fatal(err)
	}
	if kind != "git" || srcURL != url || sha != commit || kept != "imports/p-"+out.ProjectID || !strings.Contains(meta, `"branch"`) {
		t.Errorf("source = %s %s %s %s %s", kind, srcURL, sha, kept, meta)
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "imports", "p-"+out.ProjectID)); err != nil {
		t.Errorf("the provenance copy is not kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "imports", job.ID)); !os.IsNotExist(err) {
		t.Errorf("the job's clone is still in its first place: %v", err)
	}
	// One document source per file, with the hash of the bytes as read.
	rows, _ := h.d.Read().Query(`SELECT source_path, source_hash FROM document_sources WHERE source_id = ?`, out.SourceID)
	got := map[string]string{}
	for rows.Next() {
		var p, hh string
		_ = rows.Scan(&p, &hh)
		got[p] = hh
	}
	rows.Close()
	sum := sha256.Sum256([]byte("The quiet harbour.\n"))
	if len(got) != 3 || got["manuscript/01-the-harbour.md"] != hex.EncodeToString(sum[:]) {
		t.Errorf("document sources = %v", got)
	}
	var text string
	if err := h.d.Read().QueryRow(`SELECT d.title FROM documents d WHERE d.project_id = ? AND d.title = 'The Road'`, out.ProjectID).Scan(&text); err != nil {
		t.Errorf("the chapter did not come back: %v", err)
	}
	if j := h.must(h.owner, "import.git_job", gitmirror.GitJobIn{JobID: job.ID}).(gitmirror.GitJobOut).Item; j.Status != "done" || j.ProjectID != out.ProjectID {
		t.Errorf("job after apply = %+v", j)
	}
	if _, err := h.call(h.owner, "import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: pv.Token}); h.code(err) != ops.CodeConflict {
		t.Errorf("applying twice: %v", err)
	}
	// Purging the new project removes the kept copy.
	if _, err := h.d.Writer().Exec(`DELETE FROM projects WHERE id = ?`, out.ProjectID); err != nil {
		t.Fatal(err)
	}
	h.reg.Env().ProjectPurged(context.Background(), out.ProjectID)
	if _, err := os.Stat(filepath.Join(h.dataDir, "imports", "p-"+out.ProjectID)); !os.IsNotExist(err) {
		t.Errorf("the purged project's clone survived: %v", err)
	}
}

func TestImportOfAGenericMarkdownFolderAndTheEditsAreApplied(t *testing.T) {
	h := newHarness(t)
	h.start()
	url := makeRepo(t, map[string]string{
		"01-the-harbour.md": "# The Harbour\n\nThe quiet harbour.\n",
		"02-the-road.md":    "# The Road\n\nSalt on the wind.\n",
		"cover.png":         "not text",
		"README.md":         "About this repository.\n",
	})
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: url, Name: "Salt Road repo"}).ID)
	if job.Status != "preview" || job.Summary.Layout != "markdown-folder" {
		t.Fatalf("job = %+v", job)
	}
	pv := h.must(h.owner, "import.git_preview", gitmirror.GitPreviewIn{JobID: job.ID, Edits: []imports.Edit{{Key: "02-the-road.md", Skip: true}}}).(gitmirror.GitPreviewOut).Preview
	titles := map[string]bool{}
	for _, n := range pv.Nodes {
		titles[n.Title] = true
	}
	if titles["The Road"] || !titles["The Harbour"] {
		t.Errorf("nodes = %+v", pv.Nodes)
	}
	var skipped []string
	for _, s := range pv.Skipped {
		skipped = append(skipped, s.Path+": "+s.Reason)
	}
	if !strings.Contains(strings.Join(skipped, "|"), "cover.png: not a Markdown file") {
		t.Errorf("skipped = %v", skipped)
	}
	out := h.must(h.owner, "import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: pv.Token, Title: "Salt Road",
		Edits: []imports.Edit{{Key: "02-the-road.md", Skip: true}}}).(imports.ApplyOut)
	if out.Title != "Salt Road" || out.Documents != 2 { // the chapter and the README note
		t.Errorf("apply = %+v", out)
	}
}

func TestImportRefusals(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.MaxRepoBytes = 100 << 10 })
	h.start()
	// An ssh address needs a project remote's deploy key.
	if _, err := h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{URL: "ssh://git@forge.example.org/o/r.git"}); h.code(err) != ops.CodeInvalid {
		t.Errorf("ssh address: %v", err)
	}
	if _, err := h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{}); h.code(err) != ops.CodeInvalid {
		t.Errorf("nothing named: %v", err)
	}
	if _, err := h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{URL: "https://user:pw@forge.example.org/o/r.git"}); h.code(err) != ops.CodeInvalid {
		t.Errorf("credentials in the address: %v", err)
	}
	// A remote of a project you cannot edit is not found, whatever its id.
	url, _, _ := h.bareRemote()
	rem := h.addRemote(url)
	if _, err := h.call(h.outside, "import.git_preview", gitmirror.GitPreviewIn{RemoteID: rem.ID}); h.code(err) != ops.CodeNotFound {
		t.Errorf("someone else's remote: %v", err)
	}
	if _, err := h.call(h.reader, "import.git_preview", gitmirror.GitPreviewIn{RemoteID: rem.ID}); h.code(err) != ops.CodeForbidden {
		t.Errorf("a reader's use of a remote: %v", err)
	}
	// An empty repository.
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{RemoteID: rem.ID}).ID)
	if job.Status != "failed" || !strings.Contains(job.Error, "empty") {
		t.Errorf("empty repository: %+v", job)
	}
	// A repository over the size cap, refused with the limit named, and no clone left.
	big := map[string]string{}
	for i := 0; i < 4; i++ { // random text does not compress
		b := make([]byte, 60<<10)
		_, _ = rand.Read(b)
		big[string(rune('a'+i))+".md"] = hex.EncodeToString(b)
	}
	job = h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: makeRepo(t, big)}).ID)
	if job.Status != "failed" || !strings.Contains(job.Error, "limit") || !strings.Contains(job.Error, "SCRIOBH_GIT_MAX_REPO_MB") {
		t.Errorf("big repository: %+v", job)
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "imports", job.ID)); !os.IsNotExist(err) {
		t.Errorf("the clone of a refused repository is still there: %v", err)
	}
	// A repository with no Markdown at all.
	job = h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: makeRepo(t, map[string]string{"main.go": "package main\n"})}).ID)
	if job.Status != "failed" || !strings.Contains(job.Error, "no Markdown") {
		t.Errorf("no text: %+v", job)
	}
	// Discarding.
	good := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: makeRepo(t, map[string]string{"01-a.md": "Some text here.\n"})}).ID)
	if good.Status != "preview" {
		t.Fatalf("good = %+v", good)
	}
	h.must(h.owner, "import.git_discard", gitmirror.GitJobIn{JobID: good.ID})
	if _, err := os.Stat(filepath.Join(h.dataDir, "imports", good.ID)); !os.IsNotExist(err) {
		t.Error("a discarded clone is still there")
	}
	if _, err := h.call(h.owner, "import.git_job", gitmirror.GitJobIn{JobID: good.ID}); h.code(err) != ops.CodeNotFound {
		t.Errorf("a discarded job: %v", err)
	}
}

func TestOldJobsAreSweptAndInterruptedOnesFailAtStart(t *testing.T) {
	h := newHarness(t)
	h.start()
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: makeRepo(t, map[string]string{"01-a.md": "Some text here.\n"})}).ID)
	if job.Status != "preview" {
		t.Fatalf("job = %+v", job)
	}
	h.clk.Advance(25 * time.Hour) // the periodic check sweeps jobs older than a day
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.svc.WaitIdle(ctx)
	if rowCount(t, h, `SELECT count(*) FROM import_jobs`) != 0 {
		t.Error("an old job was not removed")
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "imports", job.ID)); !os.IsNotExist(err) {
		t.Error("an old clone was not removed")
	}
	// A job left running by a stopped process fails at the next start.
	if _, err := h.d.Writer().Exec(`INSERT INTO import_jobs(id, owner_id, source_url, status, clone_path, created_at) VALUES ('imp_stuck', ?, 'https://x.org/a/b.git', 'running', 'imports/imp_stuck', ?)`, h.owner.UserID, h.clk.Now().UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
		t.Fatal(err)
	}
	svc := gitmirror.New(gitmirror.Options{DataDir: h.dataDir, DB: h.d, Source: h.src, Clock: h.clk})
	c2, cancel2 := context.WithCancel(context.Background())
	svc.Start(c2)
	cancel2()
	svc.Wait()
	if j := h.must(h.owner, "import.git_job", gitmirror.GitJobIn{JobID: "imp_stuck"}).(gitmirror.GitJobOut).Item; j.Status != "failed" || j.Error == "" {
		t.Errorf("stuck job = %+v", j)
	}
}
