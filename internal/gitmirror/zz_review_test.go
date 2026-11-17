package gitmirror_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/secrets"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// REVIEW F1: a public repository made by repo.publish (public audience, name typed) can be turned into a
// full-mirror remote by an API key: git.remote.update (audience, include_notes) and git.push_force are neither
// session-only nor re-checked against the repository's visibility.
func TestReviewAPublicRepositoryCanBeGivenTheFullMirrorByAnAPIKey(t *testing.T) {
	h, f, acct := setupPublish(t, "github")
	in := h.planIn(acct.ID, func(in *gitmirror.RepoPlanIn) { in.Visibility = "public" })
	out, p := h.publish(in, "salt-road")
	if p.Status != "ok" {
		t.Fatalf("publish: %+v", p)
	}
	if rp := f.repos["mairead/salt-road"]; rp == nil || rp.private {
		t.Fatalf("the repository should be public at the host: %+v", rp)
	}
	key := keyPrincipal(h, "write")
	if _, err := h.call(key, "repo.plan", in); h.code(err) == "none" {
		t.Fatalf("repo.plan must refuse a key")
	}
	if _, err := h.call(key, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: out.Remote.ID, Audience: ptr("private")}); err != nil {
		t.Skipf("the flip is refused: %v", err)
	}
	if _, err := h.call(key, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: out.Remote.ID, IncludeNotes: ptr(true)}); err != nil {
		t.Fatalf("include_notes: %v", err)
	}
	fo, err := h.call(key, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: out.Remote.ID, Confirm: out.Remote.Name})
	if err != nil {
		t.Fatalf("force: %v", err)
	}
	h.settle(fo.(gitmirror.PushOut).Item.ID)
	tree := treeOf(t, f, "mairead", "salt-road", "main")
	if _, ok := tree["notes/01-ideas.md"]; ok {
		t.Errorf("DEFECT: the notes are in a PUBLIC repository (private=%v), put there by an API key, with no session and no re-check: %v", f.repos["mairead/salt-road"].private, sortedKeys(tree))
	}
	_ = ops.CodeForbidden
}

// REVIEW F2: the restore pause is undone by the start-up credential check.
func TestReviewARestorePauseSurvivesTheKeyComingAndGoing(t *testing.T) {
	h := newHarness(t)
	h.start()
	r := h.addRemote("https://forge.example.org/o/r.git", func(in *gitmirror.CreateIn) { in.Name = "B"; in.Token = "tok_abcdefghijklmnopqrstuvwxyz" })
	// A restore on a machine that has no secret key yet: nothing is cleared, every remote is paused.
	for _, step := range backup.PostRestore {
		if _, err := step(t.Context(), h.d, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.remoteByID(r.ID); got.Enabled || got.Status.LastErrorCode != "paused_by_restore" {
		t.Fatalf("not paused: %+v", got.Status)
	}
	// First start: a new key is made; the check (clear=false) cannot read the token.
	other := secrets.New([]byte("another-secret-key-another-secret-key-0123456"))
	if _, err := gitmirror.CheckCredentials(t.Context(), h.d, other, false); err != nil {
		t.Fatal(err)
	}
	// The operator puts the original secret.key back and restarts.
	orig := secrets.New([]byte("test-secret-key-test-secret-key-0123456789"))
	if _, err := gitmirror.CheckCredentials(t.Context(), h.d, orig, false); err != nil {
		t.Fatal(err)
	}
	if got := h.remoteByID(r.ID); got.Enabled {
		t.Errorf("DEFECT: the remote is switched on again with no one switching it on (state %s, code %q)", got.Status.State, got.Status.LastErrorCode)
	}
}

// REVIEW F4: the database's second opinion is lost the moment the document is trashed, while the mirror is still behind.
func TestReviewTheSecondOpinionSurvivesTrashingADoNotCompileDocument(t *testing.T) {
	h := newHarness(t)
	mk := func(in docs.CreateIn) docs.Doc { return h.must(h.owner, "doc.create", in).(docs.Item).Item }
	ch := mk(docs.CreateIn{ProjectID: h.pid, Kind: "chapter", Title: "Hidden", Content: "LATE-CANARY chapter"})
	sc := mk(docs.CreateIn{ProjectID: h.pid, Kind: "scene", Title: "A scene in it", ParentID: ch.ID, Content: "LATE-CANARY scene"})
	ok := mk(docs.CreateIn{ProjectID: h.pid, Kind: "chapter", Title: "Fine", Content: "Fine text."})
	// the mirror still has the old state: both documents included
	h.src.WriteRich("The Salt Road", []richDoc{
		{id: ch.ID, path: "manuscript/01-hidden/00-hidden.md", kind: "chapter", title: ch.Title, status: "draft", body: "LATE-CANARY chapter\n"},
		{id: sc.ID, path: "manuscript/01-hidden/01-a-scene.md", kind: "scene", title: sc.Title, status: "draft", body: "LATE-CANARY scene\n"},
		{id: ok.ID, path: "manuscript/02-fine.md", kind: "chapter", title: ok.Title, status: "draft", body: "Fine text.\n"},
	}, nil)
	f := false
	ch2 := h.must(h.owner, "doc.update", docs.UpdateIn{ID: ch.ID, BaseRevision: ch.Revision, IncludeInCompile: &f}).(docs.Item).Item
	_ = ch2
	h.must(h.owner, "doc.trash", docs.DocIn{ID: ch.ID})
	files, _, err := h.svc.SnapshotFor(t.Context(), h.pid, "public", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for p, b := range files {
		if strings.Contains(string(b), "LATE-CANARY") {
			t.Errorf("DEFECT: %s holds text of a document marked do not compile and then trashed", p)
		}
	}
}

// REVIEW F5: the public manifest's attachment entries carry the uploader's file name.
func TestReviewAnAttachmentsUploadedFileNameIsNotPublished(t *testing.T) {
	h := newHarness(t)
	h.src.WriteRich("The Salt Road", canaryDocs(), map[string][]byte{"keep.png": []byte("PNGKEEP")})
	mp := filepath.Join(h.src.dir, "project.json")
	b, _ := os.ReadFile(mp)
	b = []byte(strings.Replace(string(b), `"filename": "keep.png"`, `"filename": "FILENAME-CANARY-divorce-papers.png"`, 1))
	if err := os.WriteFile(mp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	files, _, err := h.svc.SnapshotFor(t.Context(), h.pid, "public", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(files["scriobh.json"]), "FILENAME-CANARY") {
		t.Errorf("DEFECT: scriobh.json of a public remote carries the uploaded file name:\n%s", files["scriobh.json"])
	}
}

// REVIEW F4b: text the database now holds as a private note is public while the mirror is behind: the push never
// compares the mirror's text with the database (documents.content_hash vs the manifest's sha256) and a manual push
// does not wait for the mirror to be current.
func TestReviewTextJustMadePrivateIsNotPublishedFromAStaleMirror(t *testing.T) {
	h := newHarness(t)
	d := h.must(h.owner, "doc.create", docs.CreateIn{ProjectID: h.pid, Kind: "chapter", Title: "One", Content: "Open. %%LATE-PRIVATE-CANARY%% Close."}).(docs.Item).Item
	h.src.WriteRich("The Salt Road", []richDoc{{id: d.ID, path: "manuscript/01-one.md", kind: "chapter", title: "One", status: "done", body: "Open. LATE-PRIVATE-CANARY Close.\n"}}, nil)
	files, _, err := h.svc.SnapshotFor(t.Context(), h.pid, "public", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for p, b := range files {
		if strings.Contains(string(b), "LATE-PRIVATE-CANARY") {
			t.Errorf("DEFECT: %s publishes text the database holds as a private note (stale mirror)", p)
		}
	}
}
