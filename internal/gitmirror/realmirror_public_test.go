package gitmirror_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/server"
	"github.com/crueber/scriobh/internal/testutil"
)

func tinyPNG(seed int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < 8; i++ {
		img.Set(i, i, color.RGBA{uint8(seed), 3, 4, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// The reader's view against the real mirror's manifest, folder layout and attachment records: a do-not-compile
// part with a chapter inside it, a private note, a ghost, an HTML comment, a scene whose point of view is a bible
// entity, an outline synopsis, a picture only the hidden chapter links to and one a pushed chapter links to.
func TestAPublicRemoteOfTheRealMirrorCarriesOnlyWhatAReaderSees(t *testing.T) {
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
	upload := func(seed int) string {
		b := tinyPNG(seed)
		c := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(b), Length: int64(len(b))})
		out, err := srv.Reg.Call(c, owner, "project.attachment.upload", attachments.UploadIn{ID: pid, Filename: "SECRET-NAME-CANARY.png", Alt: "ALT-TEXT-CANARY"})
		if err != nil {
			t.Fatal(err)
		}
		return out.(attachments.Item).Item.URL
	}
	keep, drop := upload(1), upload(2)

	cid := call("compendium.create", compendiums.CreateIn{Name: "The World"}).(compendiums.Item).Item.ID
	zed := call("entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Zedekiah Quillfeather", Summary: "ENTITY-SUMMARY-CANARY"}).(entities.Item).Item.ID
	call("project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	opening := call("doc.create", docs.CreateIn{ProjectID: pid, Title: "Opening", Kind: "chapter", SynopsisShort: "SYNOPSIS-CANARY",
		Content: "The harbour woke. %%PRIVATE-NOTE-CANARY%% It was <scriobh-ghost>GHOST-CANARY</scriobh-ghost>cold.\n\n<!-- HTMLCOMMENT-CANARY -->\n\n![a harbour](" + keep + ")\n"}).(docs.Item).Item
	call("scene.set_pov", scenes.SetIn{ID: opening.ID, Entity: zed})
	part := call("doc.create", docs.CreateIn{ProjectID: pid, Title: "PART-TITLE-CANARY", Kind: "part", Content: "PART-CANARY\n"}).(docs.Item).Item
	call("doc.create", docs.CreateIn{ProjectID: pid, Title: "CHILD-TITLE-CANARY", Kind: "chapter", ParentID: part.ID, Content: "CHILD-CANARY ![gone](" + drop + ")\n"})
	call("doc.create", docs.CreateIn{ProjectID: pid, Title: "NOTE-TITLE-CANARY", Kind: "note", Content: "NOTE-DOC-CANARY\n"})
	no := false
	call("doc.update", docs.UpdateIn{ID: part.ID, BaseRevision: part.Revision, IncludeInCompile: &no})
	wctx, wcancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer wcancel()
	waitMirrorCurrent(t, srv.Mirror, pid)

	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	url := initBare(t, remoteDir)
	// No audience asked for: the default is the strictest.
	rem := call("git.remote.create", gitmirror.CreateIn{ProjectID: pid, Name: "Public mirror", URL: url, PushMode: "manual"}).(gitmirror.RemoteItem).Item
	if rem.Audience != "public" {
		t.Fatalf("audience = %q", rem.Audience)
	}
	out := call("git.push", gitmirror.RemoteBody{ProjectID: pid}).(gitmirror.PushOut)
	if err := svc.WaitIdle(wctx); err != nil {
		t.Fatal(err)
	}
	if p := call("git.push_get", gitmirror.PushGetIn{ProjectID: pid, PushID: out.Item.ID}).(gitmirror.PushItem).Item; p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	all := everythingIn(t, remoteDir)
	for _, c := range []string{"SYNOPSIS-CANARY", "PRIVATE-NOTE-CANARY", "GHOST-CANARY", "HTMLCOMMENT-CANARY", "PART-CANARY", "PART-TITLE-CANARY", "CHILD-CANARY", "CHILD-TITLE-CANARY",
		"NOTE-DOC-CANARY", "NOTE-TITLE-CANARY", "SECRET-NAME-CANARY", "secret-name-canary", "Zedekiah", "Quillfeather", "ENTITY-SUMMARY-CANARY", "ALT-TEXT-CANARY", "compendium", "pov_entity", "synopsis"} {
		if strings.Contains(all, c) {
			t.Errorf("the public repository holds %q", c)
		}
	}
	if !strings.Contains(all, "The harbour woke.") || !strings.Contains(all, "cold.") {
		t.Errorf("the chapter is gone")
	}
	var atts []string
	for _, f := range treeFilesOf(t, remoteDir) {
		if strings.HasPrefix(f, "attachments/") {
			atts = append(atts, f)
		}
	}
	if len(atts) != 1 {
		t.Errorf("attachments pushed: %v (one picture is linked from a pushed chapter, one only from a hidden one)", atts)
	}
	if got := strings.Join(treeFilesOf(t, remoteDir), ","); !strings.Contains(got, "scriobh.json") || strings.Contains(got, "notes/") || strings.Contains(got, "part") {
		t.Errorf("tree = %s", got)
	}
}

// waitMirrorCurrent waits until the real mirror has written the project as the database holds it now. WaitIdle is not
// that: it looks only at the mirror's own dirty flags, and a change reaches the mirror as an event, a moment later.
func waitMirrorCurrent(t *testing.T, m *mirror.Mirror, projectID string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		cur, err := m.Current(context.Background(), projectID)
		if err != nil {
			t.Fatal(err)
		}
		if cur {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the mirror did not become current")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The real mirror knows when it is behind the database: a change the mirror was not told about is not current, and
// the public view of it leaves the changed document out rather than publish the old text.
func TestTheRealMirrorKnowsWhenItIsBehindAndAPublicPushWaits(t *testing.T) {
	d := testutil.DB(t)
	cfg := testutil.Config(t, map[string]string{"SCRIOBH_MIRROR_DEBOUNCE_MS": "20"})
	srv, err := server.New(server.Deps{Cfg: cfg, DB: d})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.Mirror.Start(ctx)
	svc := gitmirror.New(gitmirror.Options{DataDir: cfg.DataDir, DB: d, Source: srv.Mirror, Box: secrets.New(cfg.SecretKey), AllowFile: true, AllowPrivate: true, PollEvery: time.Hour})
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
	ch := call("doc.create", docs.CreateIn{ProjectID: pid, Title: "Opening", Kind: "chapter", Content: "The harbour woke.\n"}).(docs.Item).Item
	waitMirrorCurrent(t, srv.Mirror, pid)
	// A change the mirror is never told about (the database is edited under it).
	if _, err := d.Writer().Exec(`UPDATE documents SET content = ?, content_hash = 'changed-behind-its-back' WHERE id = ?`, "The harbour woke. %%PRIVATE-NOW%%\n", ch.ID); err != nil {
		t.Fatal(err)
	}
	if cur, _ := srv.Mirror.Current(context.Background(), pid); cur {
		t.Fatal("the mirror says it is current after the database changed")
	}
	url := initBare(t, filepath.Join(t.TempDir(), "remote.git"))
	call("git.remote.create", gitmirror.CreateIn{ProjectID: pid, Name: "Public", URL: url, PushMode: "manual"})
	out := call("git.push", gitmirror.RemoteBody{ProjectID: pid}).(gitmirror.PushOut)
	if err := svc.WaitIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := call("git.push_get", gitmirror.PushGetIn{ProjectID: pid, PushID: out.Item.ID}).(gitmirror.PushItem).Item; p.Status != "error" || p.ErrorCode != "mirror_behind" {
		t.Errorf("push = %+v", p)
	}
}
