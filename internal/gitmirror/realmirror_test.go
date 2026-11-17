package gitmirror_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/server"
	"github.com/crueber/scriobh/internal/testutil"
)

// The whole path with the real mirror: documents written through the ops, the
// mirror writing them, a push built from a held snapshot, and an import of
// what was pushed back into a new project. The mirror's manifest shape is
// checked against the importer here, not assumed.
func TestRealMirrorPushThenImportRoundTrip(t *testing.T) {
	d := testutil.DB(t)
	cfg := testutil.Config(t, map[string]string{"SCRIOBH_MIRROR_DEBOUNCE_MS": "20"})
	srv, err := server.New(server.Deps{Cfg: cfg, DB: d})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.Mirror.Start(ctx)
	svc := gitmirror.New(gitmirror.Options{DataDir: cfg.DataDir, DB: d, Source: srv.Mirror, Box: secrets.New(cfg.SecretKey), AllowFile: true,
		Debounce: 50 * time.Millisecond, PollEvery: time.Hour})
	srv.Mirror.OnWrite(svc.Notify)
	srv.Reg.Env().SetService(gitmirror.ServiceName, svc)
	svc.Start(ctx)
	t.Cleanup(func() { cancel(); svc.Wait(); srv.Mirror.Wait() })

	owner := ops.Principal{UserID: testutil.User(t, d, "writer", "mairead@test.local"), Role: "writer"}
	call := func(op string, in any) any {
		t.Helper()
		out, err := srv.Reg.Call(context.Background(), owner, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	pid := call("project.create", projects.CreateIn{Title: "The Salt Road"}).(projects.Item).Item.ID
	part := call("doc.create", docs.CreateIn{ProjectID: pid, Title: "Part One", Kind: "part"}).(docs.Item).Item.ID
	call("doc.create", docs.CreateIn{ProjectID: pid, Title: "The Harbour", Kind: "chapter", ParentID: part, Content: "The quiet harbour, where the boats wait.\n"})
	call("doc.create", docs.CreateIn{ProjectID: pid, Title: "The Road", Kind: "chapter", ParentID: part, Content: "Salt on the wind, and a long way to go.\n"})
	call("doc.create", docs.CreateIn{ProjectID: pid, Title: "Ideas", Kind: "note", Content: "Maybe a storm.\n"})
	wctx, wcancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer wcancel()
	if err := srv.Mirror.WaitIdle(wctx); err != nil {
		t.Fatal(err)
	}
	// The mirror's debounce can still be pending when WaitIdle returns: under
	// the race detector it wrote a three-document manifest and the first push
	// took that snapshot (run #528). Wait for project.json to list all five
	// documents (the four created here and the root the mirror adds) before the
	// remote exists.
	mirrorDir := filepath.Join(cfg.DataDir, "mirror")
	var matches []string
	manifestDeadline := time.Now().Add(15 * time.Second)
	lastCount := -1
	for {
		matches, _ = filepath.Glob(filepath.Join(mirrorDir, "the-salt-road--*", "project.json"))
		if len(matches) == 1 {
			if b, err := os.ReadFile(matches[0]); err == nil {
				var m struct {
					Documents []json.RawMessage `json:"documents"`
				}
				if json.Unmarshal(b, &m) == nil {
					lastCount = len(m.Documents)
					if lastCount == 5 {
						break
					}
				}
			}
		}
		if time.Now().After(manifestDeadline) {
			t.Fatalf("the mirror manifest never listed 5 documents (%d): %v", lastCount, matches)
		}
		time.Sleep(20 * time.Millisecond)
	}

	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	hh := &harness{t: t, d: d, reg: srv.Reg, svc: svc, owner: owner, pid: pid, dataDir: cfg.DataDir, logs: &safeBuf{}}
	repoURL := initBare(t, remoteDir)
	call("git.remote.create", gitmirror.CreateIn{Audience: "private", ProjectID: pid, Name: "Backup", URL: repoURL, PushMode: "on_save"})

	// A save reaches the remote by itself, within the debounce.
	deadline := time.Now().Add(15 * time.Second)
	for head(t, remoteDir, "main") == plumbing.ZeroHash {
		if time.Now().After(deadline) {
			t.Fatalf("nothing was pushed by the on-save trigger: %+v", call("git.remote.get", gitmirror.RemoteQuery{ProjectID: pid}).(gitmirror.RemoteItem).Item.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := svc.WaitIdle(wctx); err != nil {
		t.Fatal(err)
	}
	files := filesAt(t, remoteDir, commitsOf(t, remoteDir, "main")[0])
	if _, ok := files["scriobh.json"]; !ok {
		t.Fatalf("files = %v", sortedKeys(files))
	}
	for p := range files {
		if p != "scriobh.json" && !strings.HasPrefix(p, "manuscript/") && !strings.HasPrefix(p, "notes/") {
			t.Errorf("pushed %s", p)
		}
	}
	// Byte for byte what the mirror holds.
	if len(matches) != 1 {
		t.Fatalf("mirror manifests: %v", matches)
	}
	if b, _ := os.ReadFile(matches[0]); string(b) != files["scriobh.json"] {
		t.Error("scriobh.json is not the mirror's project.json")
	}

	// Import what was pushed, into a new project.
	job := hh.startImport(owner, gitmirror.GitPreviewIn{URL: repoURL})
	job = hh.waitJob(owner, job.ID)
	if job.Status != "preview" || job.Summary.Layout != "scriobh" || job.Summary.Documents != 5 {
		t.Fatalf("job = %+v %+v", job, job.Summary)
	}
	pv := call("import.git_preview", gitmirror.GitPreviewIn{JobID: job.ID}).(gitmirror.GitPreviewOut).Preview
	titles := map[string]string{}
	for _, n := range pv.Nodes {
		titles[n.Title] = n.Kind
	}
	if titles["Part One"] != "part" || titles["The Harbour"] != "chapter" || titles["Ideas"] != "note" {
		t.Errorf("nodes = %v", titles)
	}
	out := call("import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: pv.Token}).(imports.ApplyOut)
	var body string
	if err := d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = 'The Road'`, out.ProjectID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "Salt on the wind, and a long way to go.\n" {
		t.Errorf("the imported text = %q", body)
	}
}

// What the real mirror of a project with a Compendium holds, and what of it the
// push takes. The mirror has a compendium/ projection of the bible; the push
// must not publish it, nor scan it, nor name it. The one thing of the bible in
// the pushed repository is the manifest's per-scene POV and location entity.
func TestRealMirrorWithACompendiumPushesNoBibleFiles(t *testing.T) {
	d := testutil.DB(t)
	cfg := testutil.Config(t, map[string]string{"SCRIOBH_MIRROR_DEBOUNCE_MS": "20"})
	srv, err := server.New(server.Deps{Cfg: cfg, DB: d})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.Mirror.Start(ctx)
	svc := gitmirror.New(gitmirror.Options{DataDir: cfg.DataDir, DB: d, Source: srv.Mirror, Box: secrets.New(cfg.SecretKey), AllowFile: true, AllowPrivate: true,
		Debounce: 50 * time.Millisecond, PollEvery: time.Hour})
	srv.Mirror.OnWrite(svc.Notify)
	srv.Reg.Env().SetService(gitmirror.ServiceName, svc)
	svc.Start(ctx)
	t.Cleanup(func() { cancel(); svc.Wait(); srv.Mirror.Wait() })

	owner := ops.Principal{UserID: testutil.User(t, d, "writer", "mairead@test.local"), Role: "writer"}
	editor := ops.Principal{UserID: testutil.User(t, d, "writer", "editor@test.local"), Role: "writer"}
	call := func(p ops.Principal, op string, in any) any {
		t.Helper()
		out, err := srv.Reg.Call(context.Background(), p, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	pid := call(owner, "project.create", projects.CreateIn{Title: "The Salt Road"}).(projects.Item).Item.ID
	// The editor can edit the book but is not a member of the Compendium.
	if _, err := d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, editor.UserID, "editor", "2026-01-01T00:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	cid := call(owner, "compendium.create", compendiums.CreateIn{Name: "The World"}).(compendiums.Item).Item.ID
	const secretName = "Zedekiah Quillfeather"
	zed := call(owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: secretName, Summary: "-----BEGIN PRIVATE KEY-----\n" + keyBody}).(entities.Item).Item.ID
	call(owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	ch := call(owner, "doc.create", docs.CreateIn{ProjectID: pid, Title: "The Harbour", Kind: "chapter", Content: "The quiet harbour.\n"}).(docs.Item).Item.ID
	call(owner, "scene.set_pov", scenes.SetIn{ID: ch, Entity: zed})
	wctx, wcancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer wcancel()
	waitMirrorCurrent(t, srv.Mirror, d, pid)
	// The mirror really has the projection. It is written a moment after the project's own files, so it is waited
	// for (WaitIdle only says the mirror has nothing it knows of to do).
	found := false
	for deadline := time.Now().Add(60 * time.Second); !found && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		_ = filepath.WalkDir(filepath.Join(cfg.DataDir, "mirror"), func(p string, e os.DirEntry, err error) error {
			if err == nil && !e.IsDir() && strings.Contains(p, string(filepath.Separator)+"compendium"+string(filepath.Separator)) && strings.Contains(strings.ToLower(p), "quillfeather") {
				found = true
			}
			return nil
		})
	}
	if !found {
		t.Fatal("the mirror has no compendium/ file for the entity: the premise of this test is gone")
	}

	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	url := initBare(t, remoteDir)
	call(owner, "git.remote.create", gitmirror.CreateIn{Audience: "private", ProjectID: pid, Name: "Backup", URL: url, PushMode: "manual"})
	out := call(owner, "git.push", gitmirror.RemoteBody{ProjectID: pid}).(gitmirror.PushOut)
	if err := svc.WaitIdle(wctx); err != nil {
		t.Fatal(err)
	}
	p := call(editor, "git.push_get", gitmirror.PushGetIn{ProjectID: pid, PushID: out.Item.ID}).(gitmirror.PushItem).Item
	if p.Status != "ok" {
		t.Fatalf("a Compendium project could not be pushed: %+v", p)
	}
	files := filesAt(t, remoteDir, commitsOf(t, remoteDir, "main")[0])
	for name, content := range files {
		if strings.Contains(name, "compendium") || strings.Contains(strings.ToLower(name), "quillfeather") {
			t.Errorf("the push published %s", name)
		}
		if name != "scriobh.json" && (strings.Contains(content, secretName) || strings.Contains(content, "PRIVATE KEY")) {
			t.Errorf("%s carries bible text", name)
		}
	}
	// The one trace of the bible in the repository: the scene's POV entity in the manifest.
	if !strings.Contains(files["scriobh.json"], `"pov_entity"`) || !strings.Contains(files["scriobh.json"], secretName) {
		t.Errorf("the published manifest does not carry the POV entity (the documentation says it does):\n%s", files["scriobh.json"])
	}
	// The Git ops an editor can call carry no entity name and no bible path.
	for _, v := range []any{
		call(editor, "git.remote.get", gitmirror.RemoteQuery{ProjectID: pid}),
		call(editor, "git.push_history", gitmirror.PushHistoryIn{ProjectID: pid}),
		p,
	} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), secretName) || strings.Contains(strings.ToLower(string(b)), "quillfeather") || strings.Contains(string(b), "compendium/") {
			t.Errorf("a Git op shows the bible: %.300s", b)
		}
	}
}
