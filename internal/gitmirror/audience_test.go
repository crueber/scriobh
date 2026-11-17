package gitmirror_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

// richDoc is a document of the fake mirror with everything the real manifest carries.
type richDoc struct {
	id, path, kind, title, status, synopsis, pov, body string
	exclude                                            bool // marked do not compile
}

// WriteRich writes a mirror the way the real one does: the manifest with the outline's details and entity
// references, notes, attachments under attachments/.
func (f *fakeSource) WriteRich(title string, docs []richDoc, atts map[string][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, _ := os.ReadDir(f.dir)
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(f.dir, e.Name()))
	}
	h := sha256.New()
	var list []map[string]any
	for _, d := range docs {
		full := filepath.Join(f.dir, filepath.FromSlash(d.path))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(d.body), 0o644); err != nil {
			panic(err)
		}
		sum := sha256.Sum256([]byte(d.body))
		m := map[string]any{"id": d.id, "path": d.path, "kind": d.kind, "title": d.title, "status": d.status, "synopsis_short": d.synopsis, "synopsis_long": d.synopsis,
			"pov": d.pov, "location": "LOCATION-CANARY", "story_date": "1998-04-12", "story_date_end": "", "target_words": 2000, "include_in_compile": !d.exclude,
			"word_count": text.WordCount(d.body), "sha256": hex.EncodeToString(sum[:])}
		if d.pov != "" {
			m["pov_entity"] = map[string]any{"id": "ent_" + d.id, "name": d.pov}
		}
		list = append(list, m)
		fmt.Fprintf(h, "%s %x\n", d.path, sum)
	}
	var atl []map[string]any
	for name, b := range atts {
		full := filepath.Join(f.dir, "attachments", name)
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, b, 0o644)
		sum := sha256.Sum256(b)
		atl = append(atl, map[string]any{"id": "att_" + strings.TrimSuffix(name, ".png"), "path": "attachments/" + name, "sha256": hex.EncodeToString(sum[:]), "bytes": len(b), "mime": "image/png", "filename": name, "alt": "ALT-TEXT-CANARY"})
		fmt.Fprintf(h, "%s %x\n", name, sum)
	}
	mf := map[string]any{"schema_version": 2, "format": "scriobh-mirror", "project": map[string]any{"id": "prj_x", "slug": "the-salt-road", "title": title, "subtitle": "", "author": "M. Reader", "kind": "novel", "language": "en", "genre": "GENRE-CANARY", "status": "STATUS-CANARY"},
		"state": "active", "includes_notes": true, "documents": list}
	if len(atl) > 0 {
		mf["attachments"] = atl
	}
	b, _ := json.MarshalIndent(mf, "", "  ")
	_ = os.WriteFile(filepath.Join(f.dir, "project.json"), append(b, '\n'), 0o644)
	_ = os.WriteFile(filepath.Join(f.dir, "README.md"), []byte("# A generated copy\n"), 0o644)
	fmt.Fprintf(h, "%s\n", title)
	f.hash = hex.EncodeToString(h.Sum(nil))
}

// keepAtt is where a public remote puts the picture "keep.png" (PNGKEEP): named by its hash, not by what it was uploaded as.
var keepAtt = func() string {
	sum := sha256.Sum256([]byte("PNGKEEP"))
	return "attachments/" + hex.EncodeToString(sum[:])[:16] + ".png"
}()

const altsLine = `<!-- scriobh:alts {"alt":"ALT-CANARY"} -->`

func canaryDocs() []richDoc {
	return []richDoc{
		{id: "doc_a", path: "manuscript/01-opening.md", kind: "chapter", title: "Opening", status: "done", synopsis: "SYNOPSIS-CANARY", pov: "ENTITY-NAME-CANARY",
			body: "---\ntitle: FRONT-CANARY\n---\n\nThe harbour woke. %%PRIVATE-NOTE-CANARY%% It was cold <scriobh-ghost>GHOST-CANARY</scriobh-ghost>and grey.\n\n<!-- HTMLCOMMENT-CANARY -->\n\nSee ![a harbour](/files/att_keep).\n\n" + altsLine + "\n"},
		{id: "doc_b", path: "manuscript/02-draft.md", kind: "chapter", title: "NOCOMPILE-TITLE-CANARY", status: "draft", exclude: true, body: "NOCOMPILE-CANARY\n"},
		{id: "doc_p", path: "manuscript/03-secret-part/00-secret-part.md", kind: "part", title: "PART-TITLE-CANARY", status: "done", exclude: true, body: "PART-CANARY\n"},
		{id: "doc_s", path: "manuscript/03-secret-part/01-scene.md", kind: "scene", title: "CHILD-TITLE-CANARY", status: "done", body: "CHILD-CANARY ![gone](/files/att_drop)\n"},
		{id: "doc_c", path: "manuscript/04-closing.md", kind: "chapter", title: "Closing", status: "draft", body: "The end.\n"},
		{id: "doc_n", path: "notes/01-ideas.md", kind: "note", title: "NOTE-TITLE-CANARY", status: "idea", body: "NOTE-DOC-CANARY\n"},
	}
}

var canaries = []string{"SYNOPSIS-CANARY", "ENTITY-NAME-CANARY", "FRONT-CANARY", "PRIVATE-NOTE-CANARY", "GHOST-CANARY", "HTMLCOMMENT-CANARY", "ALT-CANARY",
	"NOCOMPILE-CANARY", "NOCOMPILE-TITLE-CANARY", "PART-CANARY", "PART-TITLE-CANARY", "CHILD-CANARY", "CHILD-TITLE-CANARY", "NOTE-DOC-CANARY", "NOTE-TITLE-CANARY",
	"LOCATION-CANARY", "GENRE-CANARY", "STATUS-CANARY", "ALT-TEXT-CANARY", "PNGDROP"}

// everythingIn returns every object of a bare repository, trees (with their file names) and commits (with their
// messages) included, as one string: what anyone who clones it can read.
func everythingIn(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "cat-file", "--batch-all-objects", "--batch")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("cat-file: %v", err)
	}
	return string(out)
}

func (h *harness) pushRemote(id string) gitmirror.Push {
	h.t.Helper()
	out := h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: id}).(gitmirror.PushOut)
	return h.settle(out.Item.ID)
}

func TestAPublicRemoteCarriesOnlyWhatAReaderSees(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.src.WriteRich("The Salt Road", canaryDocs(), map[string][]byte{"keep.png": []byte("PNGKEEP"), "drop.png": []byte("PNGDROP")})
	pubURL, _, pubDir := h.bareRemote()
	privURL, _, privDir := h.bareRemote()
	doneURL, _, doneDir := h.bareRemote()
	pub := h.addRemote(pubURL, func(in *gitmirror.CreateIn) { in.Name = "Public"; in.Audience = "" })
	priv := h.addRemote(privURL, func(in *gitmirror.CreateIn) { in.Name = "Backup"; in.Audience = "private" })
	done := h.addRemote(doneURL, func(in *gitmirror.CreateIn) { in.Name = "Finished"; in.Audience = ""; t := true; in.OnlyDone = &t })
	if pub.Audience != "public" || priv.Audience != "private" || pub.IncludeNotes || !priv.IncludeNotes || !done.OnlyDone {
		t.Fatalf("remotes: %+v %+v %+v", pub, priv, done)
	}
	for _, r := range []gitmirror.Remote{pub, priv, done} {
		if p := h.pushRemote(r.ID); p.Status != "ok" {
			t.Fatalf("push of %s = %+v", r.Name, p)
		}
	}

	// Public: not one canary anywhere in what a clone holds, a commit message, a file name or the manifest.
	all := everythingIn(t, pubDir)
	for _, c := range canaries {
		if strings.Contains(all, c) {
			t.Errorf("the public repository holds %s", c)
		}
	}
	for _, want := range []string{"The harbour woke.", "and grey.", "The end.", "PNGKEEP"} {
		if !strings.Contains(all, want) {
			t.Errorf("the public repository lost %q", want)
		}
	}
	tree := treeFilesOf(t, pubDir)
	if strings.Join(tree, ",") != keepAtt+",manuscript/01-opening.md,manuscript/04-closing.md,scriobh.json" {
		t.Errorf("public tree = %v", tree)
	}
	var mf struct {
		Project   map[string]any `json:"project"`
		Documents []map[string]any
	}
	if err := json.Unmarshal([]byte(gitShow(t, pubDir, "main:scriobh.json")), &mf); err != nil {
		t.Fatal(err)
	}
	if len(mf.Documents) != 2 || mf.Documents[0]["title"] != "Opening" {
		t.Errorf("public manifest documents = %v", mf.Documents)
	}
	for _, d := range mf.Documents {
		for k := range d {
			switch k {
			case "path", "kind", "title", "include_in_compile", "word_count", "sha256":
			default:
				t.Errorf("the public manifest carries %q", k)
			}
		}
	}
	clean := gitShow(t, pubDir, "main:manuscript/01-opening.md")
	if mf.Documents[0]["word_count"] != float64(text.WordCount(clean)) {
		t.Errorf("the word count %v is not of the text that was pushed (%d)", mf.Documents[0]["word_count"], text.WordCount(clean))
	}
	if _, ok := mf.Project["id"]; ok {
		t.Errorf("the public manifest names the project's id")
	}

	// Only what is done.
	if got := treeFilesOf(t, doneDir); strings.Join(got, ",") != keepAtt+",manuscript/01-opening.md,scriobh.json" {
		t.Errorf("only_done tree = %v", got)
	}
	if strings.Contains(everythingIn(t, doneDir), "The end.") {
		t.Errorf("a draft was pushed with only_done")
	}

	// Private is what it always was: the whole mirror.
	pall := everythingIn(t, privDir)
	for _, want := range []string{"NOTE-DOC-CANARY", "ALT-CANARY", "SYNOPSIS-CANARY", "NOCOMPILE-CANARY", "PRIVATE-NOTE-CANARY", "ENTITY-NAME-CANARY"} {
		if !strings.Contains(pall, want) {
			t.Errorf("the private remote lost %s", want)
		}
	}

	// The three remotes keep their own state: each has its own local repository and history.
	for _, r := range []gitmirror.Remote{pub, priv, done} {
		if got := h.must(h.owner, "git.remote.get", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: r.ID}).(gitmirror.RemoteItem).Item; got.Status.State != "synced" || got.Status.LocalSHA == "" {
			t.Errorf("%s: %+v", r.Name, got.Status)
		}
	}
	if pr, qr := h.remoteByID(pub.ID), h.remoteByID(priv.ID); pr.Status.LocalSHA == qr.Status.LocalSHA {
		t.Errorf("a public and a private remote hold the same commit")
	}
	// The push row records counts of what was withheld, never a title or a path.
	hist := h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid, RemoteID: pub.ID}).(gitmirror.PushHistoryOut)
	if b, _ := json.Marshal(hist); strings.Contains(string(b), "NOCOMPILE") || strings.Contains(string(b), "NOTE-TITLE") {
		t.Errorf("a withheld title is in the push history: %s", b)
	}
}

func (h *harness) remoteByID(id string) gitmirror.Remote {
	h.t.Helper()
	return h.must(h.owner, "git.remote.get", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: id}).(gitmirror.RemoteItem).Item
}

func treeFilesOf(t *testing.T, dir string) []string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files
}

func gitShow(t *testing.T, dir, spec string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "show", spec).Output()
	if err != nil {
		t.Fatalf("git show %s: %v", spec, err)
	}
	return string(out)
}

func TestTheReadersViewWithholdsWithTheRightReason(t *testing.T) {
	h := newHarness(t)
	h.src.WriteRich("The Salt Road", canaryDocs(), map[string][]byte{"keep.png": []byte("PNGKEEP"), "drop.png": []byte("PNGDROP")})
	files, withheld, err := h.svc.SnapshotFor(t.Context(), h.pid, "public", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, w := range withheld {
		got[w.Path] = w.Reason
	}
	want := map[string]string{"manuscript/02-draft.md": "do_not_compile", "manuscript/03-secret-part/00-secret-part.md": "do_not_compile",
		"manuscript/03-secret-part/01-scene.md": "do_not_compile", "notes/01-ideas.md": "note"}
	if len(got) != len(want) {
		t.Errorf("withheld = %v", got)
	}
	for p, r := range want {
		if got[p] != r {
			t.Errorf("%s withheld for %q, want %q", p, got[p], r)
		}
	}
	if _, ok := files["manuscript/01-opening.md"]; !ok || len(files) != 4 {
		t.Errorf("files = %v", sortedKeys(files))
	}
	// A prefix is applied to every path, the manifest included.
	files, _, err = h.svc.SnapshotFor(t.Context(), h.pid, "public", false, false, "book")
	if err != nil {
		t.Fatal(err)
	}
	for p := range files {
		if !strings.HasPrefix(p, "book/") {
			t.Errorf("%s is outside the prefix", p)
		}
	}
}

func TestTheDatabaseIsAskedToo(t *testing.T) {
	// The mirror is a few seconds behind the database. A chapter that was just marked do not compile, with a
	// scene inside it, is out of a public remote even though the mirror's manifest still says it is in.
	h := newHarness(t)
	mk := func(in docs.CreateIn) docs.Doc {
		return h.must(h.owner, "doc.create", in).(docs.Item).Item
	}
	ch := mk(docs.CreateIn{ProjectID: h.pid, Kind: "chapter", Title: "Hidden since a moment ago", Content: "LATE-CANARY chapter"})
	sc := mk(docs.CreateIn{ProjectID: h.pid, Kind: "scene", Title: "A scene in it", ParentID: ch.ID, Content: "LATE-CANARY scene"})
	ok := mk(docs.CreateIn{ProjectID: h.pid, Kind: "chapter", Title: "Fine", Content: "Fine text."})
	h.src.WriteRich("The Salt Road", []richDoc{
		{id: ch.ID, path: "manuscript/01-hidden/00-hidden.md", kind: "chapter", title: ch.Title, status: "draft", body: "LATE-CANARY chapter\n"},
		{id: sc.ID, path: "manuscript/01-hidden/01-a-scene.md", kind: "scene", title: sc.Title, status: "draft", body: "LATE-CANARY scene\n"},
		{id: ok.ID, path: "manuscript/02-fine.md", kind: "chapter", title: ok.Title, status: "draft", body: "Fine text."},
	}, nil)
	f := false
	h.must(h.owner, "doc.update", docs.UpdateIn{ID: ch.ID, BaseRevision: ch.Revision, IncludeInCompile: &f})
	files, withheld, err := h.svc.SnapshotFor(t.Context(), h.pid, "public", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for p, b := range files {
		if strings.Contains(string(b), "LATE-CANARY") {
			t.Errorf("%s holds text of a document the database says is out", p)
		}
	}
	if len(withheld) != 2 || len(files) != 2 {
		t.Errorf("withheld = %v files = %v", withheld, sortedKeys(files))
	}
}

func TestChangingWhatARemoteMayCarryStartsItsHistoryOver(t *testing.T) {
	h := newHarness(t)
	h.start()
	h.src.WriteRich("The Salt Road", canaryDocs(), nil)
	url, _, dir := h.bareRemote()
	r := h.addRemote(url, func(in *gitmirror.CreateIn) { in.Audience = "private" })
	if p := h.pushRemote(r.ID); p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	before := head(t, dir, "main")
	// Tightening the audience must not carry the old commits (which hold everything) forward in the same
	// repository: the history restarts, and the next push is refused as the remote has commits that are not ours.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r.ID, Audience: ptr("public")})
	if got := h.remoteByID(r.ID); got.Audience != "public" || got.IncludeNotes || got.Status.LocalSHA != "" || got.Status.State != "new" {
		t.Fatalf("after the change: %+v", got)
	}
	p := h.pushRemote(r.ID)
	if p.Status != "rejected" || head(t, dir, "main") != before {
		t.Fatalf("push = %+v; the remote moved", p)
	}
	// Notes cannot be switched on for a public remote.
	if _, err := h.call(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r.ID, IncludeNotes: ptr(true), ConfirmName: "Backup"}); h.code(err) != ops.CodeInvalid {
		t.Errorf("notes on a public remote: %v", err)
	}
	// The forced push is the exit, with its typed confirmation and bundle, and then the remote holds the reader's view only.
	h.must(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: r.ID, Confirm: "Backup"})
	if err := h.svc.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(treeFilesOf(t, dir), ","), "notes/") {
		t.Errorf("notes after the force push: %v", treeFilesOf(t, dir))
	}
}

func TestAnUnreadableTextIsWithheldNotPushed(t *testing.T) {
	// text.CleanMarkdown fails closed on text it cannot place; such a document is left out of a public remote.
	h := newHarness(t)
	deep := strings.Repeat("[", 200000) + "x" // a pathological paragraph: the parser has to sanitise it
	h.src.WriteRich("The Salt Road", []richDoc{
		{id: "doc_1", path: "manuscript/01-fine.md", kind: "chapter", title: "Fine", status: "done", body: "Fine.\n"},
		{id: "doc_2", path: "manuscript/02-bad.md", kind: "chapter", title: "Bad", status: "done", body: "UNREADABLE-CANARY " + deep + "\n"},
	}, nil)
	files, withheld, err := h.svc.SnapshotFor(t.Context(), h.pid, "public", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["manuscript/02-bad.md"]; ok || files["manuscript/01-fine.md"] == nil {
		t.Errorf("files = %v", sortedKeys(files))
	}
	for p, b := range files {
		if strings.Contains(string(b), "UNREADABLE-CANARY") {
			t.Errorf("%s was pushed although it could not be cleaned", p)
		}
	}
	if len(withheld) != 1 || withheld[0].Path != "manuscript/02-bad.md" || withheld[0].Reason != gitmirror.WithheldUnreadable {
		t.Errorf("withheld = %+v", withheld)
	}
}

func TestPublicViewPerformance(t *testing.T) {
	// Budget: the reader's view of 1,000 documents of 5 KB (a 1,000,000-word novel is about this) in under 5 s,
	// on a loaded test machine; the number measured is in docs/p7-notes/gitpub.md.
	h := newHarness(t)
	var ds []richDoc
	para := "The harbour woke slowly, %%a private note%% and the gulls rose over the water <scriobh-ghost>ghost</scriobh-ghost> while the boats waited. "
	for i := 0; i < 1000; i++ {
		ds = append(ds, richDoc{id: fmt.Sprintf("doc_%d", i), path: fmt.Sprintf("manuscript/%04d-chapter.md", i), kind: "chapter", title: "C", status: "done", body: strings.Repeat(para, 36) + "\n"})
	}
	h.src.WriteRich("Big", ds, nil)
	start := time.Now()
	files, _, err := h.svc.SnapshotFor(t.Context(), h.pid, "public", false, false, "")
	took := time.Since(start)
	if err != nil || len(files) != 1001 {
		t.Fatalf("files %d err %v", len(files), err)
	}
	t.Logf("reader's view of 1000 documents of ~5 KB: %v", took)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second*timingFactor {
			t.Errorf("took %v", took)
		}
	})
}
