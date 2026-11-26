package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// A local repository with one commit on main, addressed the way the importer's in-process file
// transport reads it (the .git folder). The same helper the Git tests use.
func repoFixture(t *testing.T, files map[string]string) string {
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
	return "file://" + filepath.Join(dir, ".git")
}

// withFileGit swaps the server's Git service for one that may read file:// addresses and has its
// workers running, so the real import ops can be driven over REST against a local repository.
func withFileGit(t *testing.T, s *Server) {
	t.Helper()
	svc := gitmirror.New(gitmirror.Options{DataDir: s.Cfg.DataDir, DB: s.DB, Source: s.Mirror, Box: secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), Log: s.Log,
		MaxRepoBytes: 64 << 20, AllowFile: true, AllowPrivate: true, BundlesKeep: 5})
	s.Reg.Env().SetService(gitmirror.ServiceName, svc)
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	t.Cleanup(func() { cancel(); svc.Wait() })
	s.Git = svc
}

func restJSON(t *testing.T, s *Server, c caller, method, path, body string) (int, map[string]any) {
	t.Helper()
	rec := s.do(c, method, path, body)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The page: who may open it, what it offers, and that the list of unfinished imports is the caller's own.
func TestImportRepoPage(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	reader := s.newCaller(t, "reader", ops.RoleReader)
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)

	rec := s.do(e.owner, "GET", "/import/git", "")
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("import page: %d", rec.Code)
	}
	for _, need := range []string{`id="rg-form"`, `id="rg-url"`, `type="password"`, `js/import.`, `data-git`, `href="/import"`} {
		if !strings.Contains(body, need) {
			t.Errorf("the page is missing %s", need)
		}
	}
	if strings.Contains(body, "data-into") {
		t.Error("a repository import names an existing project")
	}
	if strings.Contains(body, `value="http`) {
		t.Error("a field of the form carries a preset value")
	}
	if rec := s.do(reader, "GET", "/import/git", ""); rec.Code != 403 {
		t.Errorf("an instance reader opened the page: %d", rec.Code)
	}
	if rec := s.do(caller{name: "anon"}, "GET", "/import/git", ""); rec.Code != 303 {
		t.Errorf("signed out: %d", rec.Code)
	}
	// The folder or zip page links to it, and the project page for adding files does not (nothing merges).
	if p := s.do(e.owner, "GET", "/import", "").Body.String(); !strings.Contains(p, `href="/import/git"`) {
		t.Error("the folder import has no way to the repository import")
	}
	if p := s.do(e.owner, "GET", "/p/"+e.slug+"/import", "").Body.String(); strings.Contains(p, "/import/git") {
		t.Error("adding files to a project offers a repository import")
	}

	// A remote on a project makes it a choice for its owner, and for no one else.
	s.Reg.Env().SetService(gitmirror.ServiceName, gitmirror.New(gitmirror.Options{DataDir: s.Cfg.DataDir, DB: s.DB, Source: s.Mirror, Box: secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), Log: s.Log, MaxRepoBytes: 64 << 20}))
	if code, out := restJSON(t, s, e.owner, "POST", "/api/v1/projects/"+e.pid+"/git/remotes", `{"name":"Backup","url":"https://git.example.org/wren/salt-road.git","auth_kind":"https_token","token":"tok_secret_value_123456"}`); code != 201 {
		t.Fatalf("create a remote: %d %v", code, out)
	}
	page := s.do(e.owner, "GET", "/import/git", "").Body.String()
	if !strings.Contains(page, "The Salt Road · Backup (git.example.org)") || strings.Contains(page, "tok_secret_value") {
		t.Errorf("the owner's page does not offer the remote (or shows the token)")
	}
	if p := s.do(editor, "GET", "/import/git", "").Body.String(); strings.Contains(p, "Backup (git.example.org)") {
		t.Error("an editor is offered the owner's remote")
	}
	// ?project= preselects the remote, for the owner only, and says nothing about projects it must not name.
	if p := s.do(e.owner, "GET", "/import/git?project="+e.slug, ""); p.Code != 200 || !strings.Contains(p.Body.String(), `value="remote" checked`) || !strings.Contains(p.Body.String(), "whose remote has commits") {
		t.Errorf("?project= by the owner: %d", p.Code)
	}
	if p := s.do(editor, "GET", "/import/git?project="+e.slug, ""); p.Code != 403 {
		t.Errorf("?project= by an editor: %d, want 403", p.Code)
	}
	if p := s.do(stranger, "GET", "/import/git?project="+e.slug, ""); p.Code != 404 {
		t.Errorf("?project= by a stranger: %d, want 404", p.Code)
	}
	if p := s.do(e.owner, "GET", "/import/git?project=no-such-project", ""); p.Code != 404 {
		t.Errorf("?project= of nothing: %d, want 404", p.Code)
	}
	// An editor may not start an import through the remote either: the op answers as if it were not there.
	var remoteID string
	if _, out := restJSON(t, s, e.owner, "GET", "/api/v1/projects/"+e.pid+"/git/remote", ""); out != nil {
		if it, ok := out["item"].(map[string]any); ok {
			remoteID, _ = it["id"].(string)
		}
	}
	if remoteID == "" {
		t.Fatal("no remote id")
	}
	if code, _ := restJSON(t, s, editor, "POST", "/api/v1/imports/git/preview", `{"remote_id":"`+remoteID+`"}`); code != 404 {
		t.Errorf("an editor imported through the owner's remote: %d, want 404", code)
	}
	if code, _ := restJSON(t, s, reader, "POST", "/api/v1/imports/git/preview", `{"url":"https://example.org/x.git"}`); code != 403 {
		t.Errorf("an instance reader started an import: %d, want 403", code)
	}
}

// Errors the page shows are the server's own words; these are the ones a person can run into when starting.
func TestImportRepoStartErrorsAreInWords(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	cases := []struct {
		body string
		code int
		want string
	}{
		{`{"url":"ssh://git@example.org/wren/x.git"}`, 422, "deploy key"},
		{`{"url":"http://example.org/x.git"}`, 422, ""},
		{`{"url":"https://user:pw@example.org/x.git"}`, 422, ""},
		{`{"url":"https://example.org/x.git?token=abc"}`, 422, ""},
		{`{}`, 422, "https address"},
	}
	for _, c := range cases {
		code, out := restJSON(t, s, e.owner, "POST", "/api/v1/imports/git/preview", c.body)
		msg := ""
		if er, ok := out["error"].(map[string]any); ok {
			msg, _ = er["message"].(string)
		}
		if code != c.code || (c.want != "" && !strings.Contains(msg, c.want)) || msg == "" {
			t.Errorf("%s: %d %q (want %d containing %q)", c.body, code, msg, c.code, c.want)
		}
		if strings.Contains(msg, "pw@") || strings.Contains(msg, "abc") {
			t.Errorf("%s: the message echoes a secret: %q", c.body, msg)
		}
	}
	// The token a person types is never echoed: not in the response, not in the job's address.
	withFileGit(t, s)
	if code, out := restJSON(t, s, e.owner, "POST", "/api/v1/imports/git/preview", `{"url":"https://example.org/x.git","path":"../etc"}`); code != 422 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "folder") {
		t.Errorf("a folder outside the repository: %d %v", code, out)
	}
	code, out := restJSON(t, s, e.owner, "POST", "/api/v1/imports/git/preview", `{"url":"https://127.0.0.1:1/none.git","token":"tok_do_not_echo_9876543210","username":"wren"}`)
	if code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "tok_do_not_echo") {
		t.Errorf("the start response echoes the token: %s", raw)
	}
}

// The whole flow over the real ops against a local repository: start, follow, preview, apply; and what
// the page lists at each step.
func TestImportRepoFlowAndList(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	other := s.newCaller(t, "other", ops.RoleWriter)
	withFileGit(t, s)
	url := repoFixture(t, map[string]string{
		"chapter-one.md": "# Chapter One\n\nThe tide came in and the gulls wheeled over the quay.\n",
		"chapter-two.md": "# Chapter Two\n\nSalt on the wind, and a lamp that nobody lit.\n",
		"cover.png":      "not really a png",
	})
	code, out := restJSON(t, s, e.owner, "POST", "/api/v1/imports/git/preview", `{"url":"`+url+`"}`)
	if code != 200 {
		t.Fatalf("start: %d %v", code, out)
	}
	job := out["job"].(map[string]any)
	id := job["id"].(string)
	if job["status"] != "running" {
		t.Fatalf("job = %v", job)
	}
	// While it runs (or just after) the page lists it, for its owner only.
	if p := s.do(e.owner, "GET", "/import/git", "").Body.String(); !strings.Contains(p, `data-id="`+id+`"`) {
		t.Error("the unfinished import is not in the list")
	}
	if p := s.do(other, "GET", "/import/git", "").Body.String(); strings.Contains(p, id) {
		t.Error("another person's import is in the list")
	}
	deadline := time.Now().Add(20 * time.Second)
	var item map[string]any
	for {
		_, o := restJSON(t, s, e.owner, "GET", "/api/v1/imports/git?job_id="+id, "")
		item, _ = o["item"].(map[string]any)
		if item != nil && item["status"] != "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still running: %v", item)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if item["status"] != "preview" {
		t.Fatalf("job = %v", item)
	}
	if code, _ := restJSON(t, s, other, "GET", "/api/v1/imports/git?job_id="+id, ""); code != 404 {
		t.Errorf("another person read the job: %d", code)
	}
	if p := s.do(e.owner, "GET", "/import/git", "").Body.String(); !strings.Contains(p, `href="/import/d/imd_`) || !strings.Contains(p, `name="discard"`) || !strings.Contains(p, "2 documents") {
		t.Errorf("a ready import offers Continue, Throw away and its size")
	}
	code, pv := restJSON(t, s, e.owner, "POST", "/api/v1/imports/git/preview", `{"job_id":"`+id+`"}`)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	prev, _ := pv["preview"].(map[string]any)
	if prev == nil || prev["token"] == "" || int(prev["documents"].(float64)) != 2 || len(prev["nodes"].([]any)) != 2 {
		t.Fatalf("preview = %v", pv)
	}
	if sk, _ := prev["skipped"].([]any); len(sk) != 1 {
		t.Errorf("the png is not listed as skipped: %v", prev["skipped"])
	}
	code, ap := restJSON(t, s, e.owner, "POST", "/api/v1/imports/git", `{"job_id":"`+id+`","token":"`+prev["token"].(string)+`","title":"From the repository"}`)
	if code != 201 || ap["slug"] == "" {
		t.Fatalf("apply: %d %v", code, ap)
	}
	slug := ap["slug"].(string)
	// A finished import links to its project and offers no way to throw away the copy kept beside it.
	p := s.do(e.owner, "GET", "/import/git", "").Body.String()
	if !strings.Contains(p, `href="/p/`+slug+`"`) || strings.Contains(p, `name="discard"`) {
		t.Errorf("a finished import in the list: link or discard wrong")
	}
	// Applying again, or discarding the finished job, is not offered; the op also refuses to apply twice.
	if code, _ := restJSON(t, s, e.owner, "POST", "/api/v1/imports/git", `{"job_id":"`+id+`","token":"`+prev["token"].(string)+`"}`); code != 409 {
		t.Errorf("a second apply: %d, want 409", code)
	}
}

// A failed or ready job's text is shown as text, and the list holds only what the person may do with it.
func TestImportRepoListIsEscapedAndPerStatus(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	now := db.Now()
	ins := func(id, status, errText string) {
		_, err := s.DB.Writer().Exec(`INSERT INTO import_jobs(id, owner_id, kind, source_url, branch, status, clone_path, report_json, error, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, e.owner.userID, "git", "https://example.org/<b>x</b>.git", "main", status, "imports/"+id, `{}`, errText, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	ins("imp_run", "running", "")
	ins("imp_bad", "failed", "example.org could not be reached. <script>alert(1)</script>")
	body := s.do(e.owner, "GET", "/import/git", "").Body.String()
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "<b>x</b>") {
		t.Error("a job's text is not escaped")
	}
	if !strings.Contains(body, `/ui/import/git/imp_run/open`) || strings.Contains(body, `aria-label="Throw away the import of https://example.org/&lt;b&gt;x&lt;/b&gt;.git"`) && strings.Count(body, `name="discard"`) != 1 {
		t.Error("a running fetch should offer Check progress and no Throw away")
	}
	if !strings.Contains(body, `name="discard"`) || !strings.Contains(body, "could not be reached") {
		t.Error("a failed fetch should show why and offer Throw away")
	}
	// The op itself refuses to throw a running fetch away, in words.
	code, out := restJSON(t, s, e.owner, "DELETE", "/api/v1/imports/git?job_id=imp_run", "")
	if code == 200 {
		t.Errorf("a running fetch was discarded: %v", out)
	}
}
