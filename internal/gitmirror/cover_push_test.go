package gitmirror_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/server"
	"github.com/crueber/scriobh/internal/testutil"
)

// The project's own cover reaches a Git remote only as its include_cover says: a private remote carries it by default,
// a public one only after a person at a browser turns it on with the remote's name typed (F-861), and turning it off is
// always allowed. Never the alt text on a public remote.
func TestARemoteCarriesTheProjectsOwnCover(t *testing.T) {
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
	call("doc.create", docs.CreateIn{ProjectID: pid, Title: "Opening", Kind: "chapter", Content: "The harbour woke.\n"})
	pic := tinyPNG(7)
	c := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(pic), Length: int64(len(pic))})
	up, err := srv.Reg.Call(c, owner, "project.cover_layout.upload", coverlayout.UploadIn{ID: pid, Part: "front", Filename: "SECRET-NAME-CANARY"})
	if err != nil {
		t.Fatal(err)
	}
	front := coverlayout.Side{Mode: "page", Attachment: up.(coverlayout.UploadOut).AttachmentID, Position: "top"}
	call("project.cover_layout.set", coverlayout.SetIn{ID: pid, Shape: "sides", Front: &front, Alt: "COVER-ALT-CANARY a door"})
	waitMirrorCurrent(t, srv.Mirror, d, pid)

	key := ops.Principal{UserID: owner.UserID, Role: owner.Role, Via: "apikey", IsAgent: true, Scope: "admin", KeyID: "key_test"}
	push := func(name, audience string, mut func(*gitmirror.CreateIn)) (string, string) {
		t.Helper()
		dir := filepath.Join(t.TempDir(), "remote.git")
		url := initBare(t, dir)
		in := gitmirror.CreateIn{ProjectID: pid, Name: name, URL: url, PushMode: "manual", Audience: audience}
		if mut != nil {
			mut(&in)
		}
		rem := call("git.remote.create", in).(gitmirror.RemoteItem).Item
		out := call("git.push", gitmirror.RemoteBody{ProjectID: pid, RemoteID: rem.ID}).(gitmirror.PushOut)
		wctx, wcancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer wcancel()
		if err := svc.WaitIdle(wctx); err != nil {
			t.Fatal(err)
		}
		if p := call("git.push_get", gitmirror.PushGetIn{ProjectID: pid, PushID: out.Item.ID}).(gitmirror.PushItem).Item; p.Status != "ok" {
			t.Fatalf("push = %+v", p)
		}
		return dir, rem.ID
	}
	coverFiles := func(dir string) (pic string, others []string) {
		for _, f := range treeFilesOf(t, dir) {
			switch {
			case strings.HasPrefix(f, "cover/front-") && strings.HasSuffix(f, ".png"):
				pic = f
			case strings.HasPrefix(f, "cover/") || strings.HasPrefix(f, "attachments/"):
				others = append(others, f)
			}
		}
		return pic, others
	}

	// A public remote carries no cover until a person says so: no picture, no layout, no alt text.
	pubDir, pubID := push("Public", "public", nil)
	if pic, others := coverFiles(pubDir); pic != "" || len(others) != 0 {
		t.Fatalf("a public remote carries the cover by default: %v %v", pic, others)
	}
	if mf := gitShow(t, pubDir, "refs/heads/main:scriobh.json"); strings.Contains(mf, `"cover"`) || strings.Contains(everythingIn(t, pubDir), "COVER-ALT-CANARY") {
		t.Errorf("the public manifest says something of the cover:\n%s", mf)
	}
	if rem := call("git.remote.get", gitmirror.RemoteQuery{ProjectID: pid, RemoteID: pubID}).(gitmirror.RemoteItem).Item; rem.IncludeCover {
		t.Error("a public remote has include_cover on")
	}

	// Turning it on: refused for a key, refused without the name typed, allowed for a person with it; audited.
	on := true
	for name, in := range map[string]struct {
		who ops.Principal
		in  gitmirror.UpdateIn
	}{
		"a key":                 {key, gitmirror.UpdateIn{ProjectID: pid, RemoteID: pubID, IncludeCover: &on, ConfirmName: "Public"}},
		"a person with no name": {owner, gitmirror.UpdateIn{ProjectID: pid, RemoteID: pubID, IncludeCover: &on}},
		"a person, wrong name":  {owner, gitmirror.UpdateIn{ProjectID: pid, RemoteID: pubID, IncludeCover: &on, ConfirmName: "public"}},
	} {
		if _, err := srv.Reg.Call(context.Background(), in.who, "git.remote.update", in.in); err == nil {
			t.Errorf("%s turned the cover on for a public remote", name)
		} else if code := ops.AsError(err).Code; name == "a key" && code != ops.CodeForbidden || name != "a key" && code != ops.CodeConflict {
			t.Errorf("%s: %v", name, err)
		}
	}
	if rem := call("git.remote.get", gitmirror.RemoteQuery{ProjectID: pid, RemoteID: pubID}).(gitmirror.RemoteItem).Item; rem.IncludeCover {
		t.Fatal("a refused change was made")
	}
	call("git.remote.update", gitmirror.UpdateIn{ProjectID: pid, RemoteID: pubID, IncludeCover: &on, ConfirmName: "Public"})
	var audited int
	_ = d.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'git.remote.update' AND detail_json LIKE '%"include_cover":true%'`).Scan(&audited)
	if audited != 1 {
		t.Errorf("%d audit entries say the cover was turned on", audited)
	}
	out := call("git.push", gitmirror.RemoteBody{ProjectID: pid, RemoteID: pubID}).(gitmirror.PushOut)
	wctx, wcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer wcancel()
	if err := svc.WaitIdle(wctx); err != nil {
		t.Fatal(err)
	}
	if p := call("git.push_get", gitmirror.PushGetIn{ProjectID: pid, PushID: out.Item.ID}).(gitmirror.PushItem).Item; p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	picFile, others := coverFiles(pubDir)
	if picFile == "" || len(others) != 0 {
		t.Fatalf("the public remote with the cover on: %q %v (%v)", picFile, others, treeFilesOf(t, pubDir))
	}
	mf := gitShow(t, pubDir, "refs/heads/main:scriobh.json")
	var m struct {
		Cover *struct {
			Shape string                                `json:"shape"`
			Front struct{ Mode, File, Position string } `json:"front"`
		} `json:"cover"`
	}
	if err := json.Unmarshal([]byte(mf), &m); err != nil || m.Cover == nil || m.Cover.Shape != "sides" || m.Cover.Front.File != picFile || m.Cover.Front.Position != "top" {
		t.Errorf("the manifest's cover: %v %s", err, mf)
	}
	if all := everythingIn(t, pubDir); strings.Contains(all, "COVER-ALT-CANARY") || strings.Contains(all, "att_") || strings.Contains(all, "SECRET-NAME-CANARY") {
		t.Error("the public repository holds the alt text, an id or the upload's name")
	}
	blob, err := exec.Command("git", "-C", pubDir, "show", "refs/heads/main:"+picFile).Output()
	if err != nil || !bytes.HasPrefix(blob, []byte("\x89PNG")) {
		t.Errorf("the picture: %v", err)
	}
	// Turning it off is always allowed, for a key too, and starts the remote's history over.
	off := false
	if _, err := srv.Reg.Call(context.Background(), key, "git.remote.update", gitmirror.UpdateIn{ProjectID: pid, RemoteID: pubID, IncludeCover: &off}); err != nil {
		t.Errorf("a key turning the cover off: %v", err)
	}
	if rem := call("git.remote.get", gitmirror.RemoteQuery{ProjectID: pid, RemoteID: pubID}).(gitmirror.RemoteItem).Item; rem.IncludeCover || rem.Status.State != "new" {
		t.Errorf("after turning it off: cover %v, state %q", rem.IncludeCover, rem.Status.State)
	}

	// A private remote (a backup) carries the whole copy, the cover and its alt text included, by default.
	privDir, _ := push("Backup", "private", nil)
	if pic, _ := coverFiles(privDir); pic == "" {
		t.Errorf("a private remote has no cover: %v", treeFilesOf(t, privDir))
	}
	if !strings.Contains(gitShow(t, privDir, "refs/heads/main:scriobh.json"), `"cover"`) {
		t.Error("the private manifest has no cover")
	}
	// ... and a private remote may turn it off.
	noDir, _ := push("Backup without", "private", func(in *gitmirror.CreateIn) { f := false; in.IncludeCover = &f })
	if pic, _ := coverFiles(noDir); pic != "" || strings.Contains(gitShow(t, noDir, "refs/heads/main:scriobh.json"), `"cover"`) {
		t.Errorf("a private remote with the cover off carries it: %v", treeFilesOf(t, noDir))
	}
	// A public remote created with the cover on needs a person and the name.
	mkPublic := func(who ops.Principal, confirm string) error {
		_, err := srv.Reg.Call(context.Background(), who, "git.remote.create", gitmirror.CreateIn{ProjectID: pid, Name: "Public with cover", URL: initBare(t, filepath.Join(t.TempDir(), "r.git")),
			PushMode: "manual", Audience: "public", IncludeCover: &on, ConfirmName: confirm})
		return err
	}
	if err := mkPublic(key, "Public with cover"); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a key creating a public remote with the cover: %v", err)
	}
	if err := mkPublic(owner, ""); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("no name: %v", err)
	}
	if err := mkPublic(owner, "Public with cover"); err != nil {
		t.Errorf("a person creating it: %v", err)
	}
}

// addCoverToTheFakeMirror puts a cover the way the real mirror writes it: cover/<part>-<sha12>.png and the layout, without
// ids, as the manifest's cover object.
func (f *fakeSource) addCoverToTheFakeMirror(t *testing.T) string {
	t.Helper()
	pic := tinyPNG(11)
	sum := sha256.Sum256(pic)
	name := "cover/front-" + hex.EncodeToString(sum[:])[:12] + ".png"
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(f.dir, "cover"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, filepath.FromSlash(name)), pic, 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.dir, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["cover"] = map[string]any{"shape": "sides", "trim": "6x9", "alt": "COVER-ALT-CANARY", "front": map[string]any{"mode": "image", "file": name}, "back": map[string]any{"mode": "none"}}
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(f.dir, "project.json"), out, 0o644); err != nil {
		t.Fatal(err)
	}
	f.hash += "+cover"
	return name
}

// The file list of a plan and the Markdown bundle of a release are built by the same code as the push, so they obey the
// same switch: no cover while a public remote has it off, the cover once a person has turned it on.
func TestThePlanAndTheMarkdownBundleObeyTheCoverSwitch(t *testing.T) {
	h, f, rem, _ := setupRelease(t, "forgejo")
	acct := h.must(h.owner, "repo.account.list", struct{}{}).(gitmirror.AccountList).Items[0]
	h.src.WriteRich("The Salt Road", canaryDocs(), map[string][]byte{"keep.png": []byte("PNGKEEP"), "drop.png": []byte("PNGDROP")})
	name := h.src.addCoverToTheFakeMirror(t)
	hasCover := func(files []string) bool {
		for _, x := range files {
			if x == name {
				return true
			}
		}
		return false
	}
	planFiles := func(in gitmirror.RepoPlanIn) []string {
		var out []string
		for _, pf := range h.plan(in).Files {
			out = append(out, pf.Path)
		}
		return out
	}
	bundleNames := func(tag string) []string {
		if rel := h.release(rem.ID, tag, "md_bundle"); rel.Status != "ok" {
			t.Fatalf("release %+v", rel)
		}
		data := f.release("mairead", "salt-road", tag).Assets[0].Data
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, zf := range zr.File {
			out = append(out, zf.Name)
		}
		return out
	}
	if hasCover(planFiles(h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Name = "another" }))) {
		t.Error("the plan of a public remote lists the cover with the switch off")
	}
	if !hasCover(planFiles(h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Name = "another"; i.IncludeCover = true }))) {
		t.Error("the plan with include_cover lists no cover")
	}
	if pl := h.plan(h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Name = "another"; i.Audience = "private" })); !pl.IncludeCover || !hasCover(planFilesOf(pl)) {
		t.Errorf("the plan of a private remote carries no cover: %v", pl.IncludeCover)
	}
	if hasCover(bundleNames("v1")) {
		t.Error("the bundle of a public remote with the switch off holds the cover")
	}
	on := true
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: rem.ID, IncludeCover: &on, ConfirmName: rem.Name})
	if !hasCover(bundleNames("v2")) {
		t.Error("the bundle of a public remote with the switch on has no cover")
	}
}

func planFilesOf(pl gitmirror.RepoPlan) []string {
	var out []string
	for _, f := range pl.Files {
		out = append(out, f.Path)
	}
	return out
}

// Setting up the real mirror with a project that has a cover and a chapter, for the tests below.
func coverWorld(t *testing.T) (call func(op string, in any) any, as func(who ops.Principal, op string, in any) (any, error), owner ops.Principal, pid string, svc *gitmirror.Service, wait func(string, time.Duration) bool) {
	t.Helper()
	d := testutil.DB(t)
	cfg := testutil.Config(t, map[string]string{"SCRIOBH_MIRROR_DEBOUNCE_MS": "20"})
	srv, err := server.New(server.Deps{Cfg: cfg, DB: d})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.Mirror.Start(ctx)
	svc = gitmirror.New(gitmirror.Options{DataDir: cfg.DataDir, DB: d, Source: srv.Mirror, Box: secrets.New(cfg.SecretKey), AllowFile: true, AllowPrivate: true,
		Debounce: 50 * time.Millisecond, PollEvery: time.Hour})
	srv.Reg.Env().SetService(gitmirror.ServiceName, svc)
	svc.Start(ctx)
	t.Cleanup(func() { cancel(); svc.Wait(); srv.Mirror.Wait() })
	owner = ops.Principal{UserID: testutil.User(t, d, "writer", "mairead@test.local"), Role: "writer"}
	as = func(who ops.Principal, op string, in any) (any, error) {
		return srv.Reg.Call(context.Background(), who, op, in)
	}
	call = func(op string, in any) any {
		t.Helper()
		out, err := as(owner, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	pid = call("project.create", projects.CreateIn{Title: "The Salt Road"}).(projects.Item).Item.ID
	call("doc.create", docs.CreateIn{ProjectID: pid, Title: "Opening", Kind: "chapter", Content: "The harbour woke.\n"})
	pic := tinyPNG(7)
	c := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(pic), Length: int64(len(pic))})
	up, err := srv.Reg.Call(c, owner, "project.cover_layout.upload", coverlayout.UploadIn{ID: pid, Part: "front"})
	if err != nil {
		t.Fatal(err)
	}
	front := coverlayout.Side{Mode: "image", Attachment: up.(coverlayout.UploadOut).AttachmentID}
	call("project.cover_layout.set", coverlayout.SetIn{ID: pid, Shape: "sides", Front: &front, Alt: "A door"})
	waitMirrorCurrent(t, srv.Mirror, d, pid)
	wait = func(dir string, within time.Duration) bool { // the remote's repository holds the cover, waiting for an automatic push if need be
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if out, err := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", "main").Output(); err == nil && strings.Contains(string(out), "cover/front-") {
				return true
			}
			time.Sleep(50 * time.Millisecond)
		}
		return false
	}
	return
}

// Turning the cover on marks the remote behind, so that an on-save remote pushes it by itself, without waiting for the next
// edit; and a remote that is already as asked is left as it is when the page sends its audience again.
func TestTurningTheCoverOnPushesItAndTheAudienceFormDoesNotUndoIt(t *testing.T) {
	call, as, owner, pid, svc, wait := coverWorld(t)
	off, on := false, true
	dir := filepath.Join(t.TempDir(), "remote.git")
	rem := call("git.remote.create", gitmirror.CreateIn{ProjectID: pid, Name: "Backup", URL: initBare(t, dir), PushMode: "on_save", Audience: "private", IncludeCover: &off}).(gitmirror.RemoteItem).Item
	call("git.push", gitmirror.RemoteBody{ProjectID: pid, RemoteID: rem.ID})
	wctx, wcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer wcancel()
	if err := svc.WaitIdle(wctx); err != nil {
		t.Fatal(err)
	}
	if wait(dir, time.Second) {
		t.Fatal("a remote with the cover off pushed it")
	}
	// the audience form sends the audience it is showing: a private remote with the cover off stays so
	call("git.remote.update", gitmirror.UpdateIn{ProjectID: pid, RemoteID: rem.ID, Audience: ptr("private")})
	if got := call("git.remote.get", gitmirror.RemoteQuery{ProjectID: pid, RemoteID: rem.ID}).(gitmirror.RemoteItem).Item; got.IncludeCover {
		t.Error("sending the same audience turned the cover back on")
	}
	// on: the remote pushes it itself
	call("git.remote.update", gitmirror.UpdateIn{ProjectID: pid, RemoteID: rem.ID, IncludeCover: &on})
	if !wait(dir, 15*time.Second) {
		t.Error("the cover was not pushed after it was turned on (an on-save remote waited for the next edit)")
	}
	// a public remote with the cover confirmed on keeps it when its audience is sent again
	pubDir := filepath.Join(t.TempDir(), "pub.git")
	pub := call("git.remote.create", gitmirror.CreateIn{ProjectID: pid, Name: "Public", URL: initBare(t, pubDir), PushMode: "manual", Audience: "public", IncludeCover: &on, ConfirmName: "Public"}).(gitmirror.RemoteItem).Item
	if !pub.IncludeCover {
		t.Fatal("the public remote was made without the cover")
	}
	call("git.remote.update", gitmirror.UpdateIn{ProjectID: pid, RemoteID: pub.ID, Audience: ptr("public")})
	if got := call("git.remote.get", gitmirror.RemoteQuery{ProjectID: pid, RemoteID: pub.ID}).(gitmirror.RemoteItem).Item; !got.IncludeCover {
		t.Error("sending the same audience turned a confirmed cover off")
	}
	// and a real change of audience takes the new audience's default: public off, private on
	if got, err := as(owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: pid, RemoteID: rem.ID, Audience: ptr("public")}); err != nil || got.(gitmirror.RemoteItem).Item.IncludeCover {
		t.Errorf("a private remote made public: %v", err)
	}
	if got, err := as(owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: pid, RemoteID: rem.ID, Audience: ptr("private"), ConfirmName: "Backup"}); err != nil || !got.(gitmirror.RemoteItem).Item.IncludeCover {
		t.Errorf("a public remote made private: %v", err)
	}
}
