package mirror_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/domain/stash"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// ---- harness ------------------------------------------------------------------

// fakeClock is a manual clock: timers fire only when the test advances it.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c       *fakeClock
	at      time.Time
	f       func()
	stopped bool
	fired   bool
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.stopped && !t.fired
	t.stopped = true
	return was
}

func newClock() *fakeClock { return &fakeClock{now: time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *fakeClock) AfterFunc(d time.Duration, f func()) mirror.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// Advance moves time forward and runs every timer that came due, in order.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.fired && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		next.fired = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// countFS counts writes and can be told to fail them.
type countFS struct {
	mirror.OSFS
	mu       sync.Mutex
	renames  []string // destination of every rename
	temps    int
	failTemp func(n int) (partial int, err error) // called per WriteTemp; partial bytes to leave behind
	failMove func(dst string) error
}

func (f *countFS) WriteTemp(dir string, data []byte) (string, error) {
	f.mu.Lock()
	f.temps++
	n := f.temps
	fail := f.failTemp
	f.mu.Unlock()
	if fail != nil {
		if partial, err := fail(n); err != nil {
			if partial > len(data) {
				partial = len(data)
			}
			// A crash part way through: a half-written temporary file stays behind.
			_ = os.MkdirAll(dir, 0o700)
			_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("w-partial-%d", n)), data[:partial], 0o600)
			return "", err
		}
	}
	return f.OSFS.WriteTemp(dir, data)
}

func (f *countFS) Rename(oldpath, newpath string) error {
	f.mu.Lock()
	fail := f.failMove
	f.mu.Unlock()
	if fail != nil {
		if err := fail(newpath); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.renames = append(f.renames, newpath)
	f.mu.Unlock()
	return f.OSFS.Rename(oldpath, newpath)
}

// writes counts files written into project folders (the state record is not one).
func (f *countFS) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.renames {
		if !strings.Contains(r, string(filepath.Separator)+".state"+string(filepath.Separator)) {
			n++
		}
	}
	return n
}

func (f *countFS) wrote(suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.renames {
		if strings.HasSuffix(r, suffix) {
			n++
		}
	}
	return n
}

func (f *countFS) reset() { f.mu.Lock(); f.renames = nil; f.mu.Unlock() }

// syncBuf is a goroutine-safe log buffer.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

type env struct {
	t     *testing.T
	d     *db.DB
	ev    *events.Store
	reg   *ops.Registry
	dir   string
	m     *mirror.Mirror
	clk   *fakeClock
	fs    *countFS
	owner ops.Principal
	ctx   context.Context
}

// newEnv builds a registry, an event store and a mirror on a fake clock. The
// mirror is not started: tests start it when they want it.
func newEnv(t *testing.T, tweak ...func(*mirror.Options)) *env {
	t.Helper()
	d := testutil.DB(t)
	e := &env{t: t, d: d, dir: t.TempDir(), clk: newClock(), fs: &countFS{}, ctx: context.Background()}
	e.ev = events.NewStore(d, nil)
	e.reg = registry.Build(d, e.ev, e.dir)
	e.owner = ops.Principal{UserID: testutil.User(t, d, ops.RoleWriter, "owner@t.local"), Role: ops.RoleWriter}
	o := mirror.Options{DataDir: e.dir, DB: d, Notifier: e.ev, Clock: e.clk, FS: e.fs}
	for _, f := range tweak {
		f(&o)
	}
	e.m = mirror.New(o)
	e.reg.Env().SetService(mirror.ServiceName, e.m)
	e.reg.Env().AddPurgeHook(e.m.ProjectPurged)
	e.reg.Env().AddCompendiumPurgeHook(e.m.CompendiumPurged)
	return e
}

func (e *env) start() {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.m.Start(ctx)
	e.t.Cleanup(func() { cancel(); e.m.Wait() })
	// Wait for the start-up check (reconcile and the first poll): a test that
	// creates a project right away would otherwise race it, and "settled" could
	// be read before the mirror has even noticed the project.
	select {
	case <-e.m.Started():
	case <-time.After(10 * time.Second):
		e.t.Fatal("the mirror did not finish starting")
	}
}

func (e *env) call(p ops.Principal, op string, in any) any {
	e.t.Helper()
	out, err := e.reg.Call(e.ctx, p, op, in)
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (e *env) project(title string) string {
	e.t.Helper()
	return e.call(e.owner, "project.create", projects.CreateIn{Title: title, Kind: "novel"}).(projects.Item).Item.ID
}

func (e *env) doc(pid, title, kind, content string) docs.Doc {
	e.t.Helper()
	return e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: pid, Title: title, Kind: kind, Content: content}).(docs.Item).Item
}

func (e *env) setContent(d docs.Doc, content string) docs.Doc {
	e.t.Helper()
	return e.call(e.owner, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, Content: &content}).(docs.Item).Item
}

// bootMirror starts a second mirror on the same data (a server restart) and
// waits until its start-up check has finished and everything it queued is
// written. Every start-up test uses it: sleeping and then asking "idle?" is
// true before the check has run.
func (e *env) bootMirror(fs mirror.FS) (*mirror.Mirror, func()) {
	e.t.Helper()
	m := mirror.New(mirror.Options{DataDir: e.dir, DB: e.d, Notifier: e.ev, FS: fs, Debounce: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	stop := func() { cancel(); m.Wait() }
	select {
	case <-m.Started():
	case <-time.After(60 * time.Second):
		stop()
		e.t.Fatal("the mirror never finished its start-up check")
	}
	wctx, wcancel := context.WithTimeout(e.ctx, 60*time.Second)
	defer wcancel()
	if err := m.WaitIdle(wctx); err != nil {
		stop()
		e.t.Fatal(err)
	}
	return m, stop
}

// caughtUp waits until the mirror has taken notice of every event written so far.
func (e *env) caughtUp() {
	e.t.Helper()
	var max int64
	if err := e.d.Read().QueryRow(`SELECT COALESCE(max(seq), 0) FROM events`).Scan(&max); err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.m.Cursor() < max {
		if time.Now().After(deadline) {
			e.t.Fatalf("the mirror never caught up with event %d (at %d)", max, e.m.Cursor())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// settle waits for the mirror to take notice of every change so far, lets the
// debounce run out, and waits for the writes to finish.
func (e *env) settle(pid string) {
	e.t.Helper()
	e.caughtUp()
	e.flush()
}

func (e *env) flush() {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.clk.Advance(mirror.DefaultDebounce)
		ctx, cancel := context.WithTimeout(e.ctx, 300*time.Millisecond)
		err := e.m.WaitIdle(ctx)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			e.t.Fatal("the mirror did not go idle:", err)
		}
	}
}

// root is the project's folder in the mirror.
func (e *env) root(pid string) string {
	e.t.Helper()
	dir := e.m.Status(pid).Directory
	if dir == "" {
		e.t.Fatal("the project has no mirror folder")
	}
	return filepath.Join(e.dir, "mirror", dir)
}

// tree lists every file under a folder, slash-separated, sorted.
func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type manifestFile struct {
	SchemaVersion int `json:"schema_version"`
	Project       struct {
		ID, Slug, Title, Kind, Author string
	} `json:"project"`
	State         string `json:"state"`
	IncludesNotes bool   `json:"includes_notes"`
	Documents     []struct {
		ID, Path, Title, Status, SHA256 string
	} `json:"documents"`
}

func manifestOf(t *testing.T, root string) manifestFile {
	t.Helper()
	var m manifestFile
	if err := json.Unmarshal([]byte(read(t, filepath.Join(root, "project.json"))), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// ---- correctness ----------------------------------------------------------------

// A comment and an alternatives block are stored text; the mirror writes them
// byte for byte, and the file follows the document through every change.
func TestMirrorFollowsTheDocument(t *testing.T) {
	e := newEnv(t)
	e.start()
	alts := "<!-- scriobh:alts {\"version\":1,\"items\":[{\"scope\":\"word\",\"quote\":\"quiet\",\"variants\":[\"still\",\"low\"],\"chosen\":\"quiet\"}]} -->"
	body := "The quiet harbour.\r\n\r\nSecond  paragraph with trailing spaces  \n\n" + alts + "\n"

	pid := e.project("The Salt Road")
	ch := e.doc(pid, "The Harbour", "chapter", body)
	e.settle(pid)
	root := e.root(pid)

	files := tree(t, root)
	if !has(files, "project.json") || !has(files, "README.md") {
		t.Fatalf("missing manifest or README: %v", files)
	}
	var chPath string
	for _, f := range files {
		if strings.HasSuffix(f, "-the-harbour.md") {
			chPath = f
		}
	}
	if chPath == "" || !strings.HasPrefix(chPath, "manuscript/") {
		t.Fatalf("no chapter file in %v", files)
	}
	// The text is stored in its canonical form (a document with alternatives is
	// kept with LF line endings and the server's own comment line); the file is
	// byte for byte that.
	if got := read(t, filepath.Join(root, chPath)); got != ch.Content {
		t.Fatalf("the chapter is not byte for byte what is stored:\n got %q\nwant %q", got, ch.Content)
	}
	m := manifestOf(t, root)
	if m.SchemaVersion != 3 || m.Project.ID != pid || m.Project.Title != "The Salt Road" || m.Project.Kind != "novel" || m.State != "active" || !m.IncludesNotes {
		t.Fatalf("manifest = %+v", m)
	}
	if !strings.Contains(read(t, filepath.Join(root, "README.md")), "never reads it back") {
		t.Error("the README does not say the folder is generated")
	}
	// Permissions: the data directory's own: 0600 files, 0700 folders.
	if fi, _ := os.Stat(filepath.Join(root, chPath)); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(root, "manuscript")); fi.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v, want 0700", fi.Mode().Perm())
	}

	// Update.
	// A save of plain text keeps the document's alternatives, so the stored text
	// still ends with their comment; the file is the stored text.
	ch = e.setContent(ch, "New text.\n")
	e.settle(pid)
	if got := read(t, filepath.Join(root, chPath)); got != ch.Content || !strings.HasPrefix(got, "New text.\n\n<!-- scriobh:alts ") {
		t.Fatalf("after update: %q (stored %q)", got, ch.Content)
	}

	// Rename: the old file goes, the new one appears, nothing else changes.
	title := "Arrival"
	ch = e.call(e.owner, "doc.update", docs.UpdateIn{ID: ch.ID, BaseRevision: ch.Revision, Title: &title}).(docs.Item).Item
	e.settle(pid)
	files = tree(t, root)
	if has(files, chPath) {
		t.Errorf("the old file %s remains after a rename: %v", chPath, files)
	}
	var arrival string
	for _, f := range files {
		if strings.HasSuffix(f, "-arrival.md") {
			arrival = f
		}
	}
	if arrival == "" || read(t, filepath.Join(root, arrival)) != ch.Content {
		t.Fatalf("renamed chapter missing or wrong: %v", files)
	}

	// Move under a part: it becomes a folder, with the part's own text first.
	part := e.doc(pid, "Part One", "part", "Part text.\n")
	e.call(e.owner, "doc.move", docs.MoveIn{ID: ch.ID, ParentID: part.ID})
	e.settle(pid)
	files = tree(t, root)
	var moved string
	for _, f := range files {
		if strings.HasSuffix(f, "-arrival.md") {
			moved = f
		}
	}
	if !strings.Contains(moved, "part-one/") {
		t.Fatalf("the moved chapter is not inside the part folder: %v", files)
	}
	var partText string
	for _, f := range files {
		if strings.HasSuffix(f, "-part-one.md") {
			partText = f
		}
	}
	if partText == "" || read(t, filepath.Join(root, partText)) != "Part text.\n" {
		t.Errorf("the part's own text is missing: %v", files)
	}
	if has(files, arrival) && arrival != moved {
		t.Errorf("the chapter exists at its old path too: %v", files)
	}

	// Trash removes the file; restore brings it back with the same text.
	e.call(e.owner, "doc.trash", docs.DocIn{ID: ch.ID})
	e.settle(pid)
	for _, f := range tree(t, root) {
		if strings.HasSuffix(f, "-arrival.md") {
			t.Fatalf("a trashed document is still mirrored: %s", f)
		}
	}
	for _, d := range manifestOf(t, root).Documents {
		if d.ID == ch.ID {
			t.Error("a trashed document is still in project.json")
		}
	}
	e.call(e.owner, "doc.restore", docs.DocIn{ID: ch.ID})
	e.settle(pid)
	found := false
	for _, f := range tree(t, root) {
		if strings.HasSuffix(f, "-arrival.md") {
			found = read(t, filepath.Join(root, f)) == ch.Content
		}
	}
	if !found {
		t.Error("the restored document is not back with its text")
	}

	// Project fields reach the manifest.
	name := "Wrack Lines"
	e.call(e.owner, "project.update", projects.UpdateIn{ID: pid, Title: &name})
	e.settle(pid)
	if got := manifestOf(t, root).Project.Title; got != "Wrack Lines" {
		t.Errorf("manifest title = %q", got)
	}
	// A trashed project stays mirrored and says so.
	e.call(e.owner, "project.trash", projects.StateIn{ID: pid})
	e.settle(pid)
	if got := manifestOf(t, root).State; got != "trashed" {
		t.Errorf("state = %q", got)
	}
}

// Slugs are unique per owner, so two owners can both have "Same Title".
func TestTwoOwnersSameSlugGetDifferentFolders(t *testing.T) {
	e := newEnv(t)
	e.start()
	other := ops.Principal{UserID: testutil.User(t, e.d, ops.RoleWriter, "other@t.local"), Role: ops.RoleWriter}
	a := e.project("Same Title")
	b := e.call(other, "project.create", projects.CreateIn{Title: "Same Title"}).(projects.Item).Item.ID
	e.settle(a)
	e.settle(b)
	da, db2 := e.m.Status(a).Directory, e.m.Status(b).Directory
	if da == "" || da == db2 {
		t.Fatalf("folders %q and %q must differ", da, db2)
	}
	if !strings.HasPrefix(da, "same-title--") || !strings.HasPrefix(db2, "same-title--") {
		t.Errorf("folder names should lead with the slug: %q %q", da, db2)
	}
	if manifestOf(t, filepath.Join(e.dir, "mirror", da)).Project.ID != a || manifestOf(t, filepath.Join(e.dir, "mirror", db2)).Project.ID != b {
		t.Error("a folder holds the wrong project")
	}
}

// No op changes a slug today; if one does, the folder moves with it, files
// untouched.
func TestSlugChangeMovesTheFolder(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Old Name")
	e.doc(pid, "One", "chapter", "text\n")
	e.settle(pid)
	oldDir := e.m.Status(pid).Directory
	oldRoot := e.root(pid)
	var chFile string
	for _, f := range tree(t, oldRoot) {
		if strings.HasSuffix(f, "-one.md") {
			chFile = f
		}
	}
	before, _ := os.Stat(filepath.Join(oldRoot, chFile))
	time.Sleep(20 * time.Millisecond)

	if _, err := e.d.Writer().Exec(`UPDATE projects SET slug = 'new-name' WHERE id = ?`, pid); err != nil {
		t.Fatal(err)
	}
	e.m.Mark(pid)
	e.flush()

	newDir := e.m.Status(pid).Directory
	if newDir == oldDir || !strings.HasPrefix(newDir, "new-name--") {
		t.Fatalf("directory %q after the slug changed from %q", newDir, oldDir)
	}
	if _, err := os.Stat(oldRoot); !os.IsNotExist(err) {
		t.Errorf("the old folder remains: %v", err)
	}
	after, err := os.Stat(filepath.Join(e.dir, "mirror", newDir, chFile))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the file was rewritten instead of the folder being moved")
	}
	if got := manifestOf(t, filepath.Join(e.dir, "mirror", newDir)).Project.Slug; got != "new-name" {
		t.Errorf("manifest slug = %q", got)
	}
}

// Purging a project removes its folder and its state, through the hook.
func TestPurgeRemovesTheMirror(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Doomed")
	keep := e.project("Keeper")
	e.settle(pid)
	e.settle(keep)
	root := e.root(pid)
	keepRoot := e.root(keep)
	e.call(e.owner, "project.trash", projects.StateIn{ID: pid})
	e.call(e.owner, "project.purge", projects.PurgeIn{ID: pid, ConfirmTitle: "Doomed"})
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the purged project's folder remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "mirror", ".state", pid+".json")); !os.IsNotExist(err) {
		t.Errorf("its state file remains: %v", err)
	}
	if _, err := os.Stat(keepRoot); err != nil {
		t.Errorf("another project's folder was removed: %v", err)
	}
	// Give any in-flight flush time to (wrongly) recreate it.
	e.clk.Advance(time.Minute)
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	_ = e.m.WaitIdle(ctx)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("a late write recreated the purged folder: %v", err)
	}
}

// A project purged while the server was down leaves its folder behind; the
// next start removes it, and only folders the mirror recorded.
func TestStartupRemovesFoldersOfProjectsThatAreGone(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Gone")
	e.settle(pid)
	root := e.root(pid)
	stranger := filepath.Join(e.dir, "mirror", "not-ours")
	if err := os.MkdirAll(stranger, 0o700); err != nil {
		t.Fatal(err)
	}
	// Purge with a registry that has no mirror hook, as if the hook had failed.
	reg2 := registry.Build(e.d, e.ev, e.dir)
	if _, err := reg2.Call(e.ctx, e.owner, "project.trash", projects.StateIn{ID: pid}); err != nil {
		t.Fatal(err)
	}
	if _, err := reg2.Call(e.ctx, e.owner, "project.purge", projects.PurgeIn{ID: pid, ConfirmTitle: "Gone"}); err != nil {
		t.Fatal(err)
	}
	// With no hook, the purge event alone makes the running mirror remove it.
	e.settle(pid)
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("the folder of a purged project survived the purge event: %v", err)
	}

	// A fresh mirror on the same data removes a folder whose project no longer
	// exists: here a hand-made orphan with a state record.
	orphanID := "prj_00000000000000000000000000"
	orphan := filepath.Join(e.dir, "mirror", "orphan--000000")
	if err := os.MkdirAll(filepath.Join(orphan, "manuscript"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "project.json"), []byte(`{"project":{"id":"`+orphanID+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st := `{"dir":"orphan--000000","fingerprint":"x","files":{"project.json":"x"},"written_at":""}`
	if err := os.WriteFile(filepath.Join(e.dir, "mirror", ".state", orphanID+".json"), []byte(st), 0o600); err != nil {
		t.Fatal(err)
	}
	m2 := mirror.New(mirror.Options{DataDir: e.dir, DB: e.d, Notifier: e.ev, Clock: newClock(), FS: &countFS{}})
	ctx2, cancel2 := context.WithCancel(context.Background())
	m2.Start(ctx2)
	defer func() { cancel2(); m2.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(orphan); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the orphaned folder was not removed at start-up")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(stranger); err != nil {
		t.Errorf("a folder the mirror did not create was removed: %v", err)
	}
}

// ---- debounce -------------------------------------------------------------------

func TestDebounceCoalescesAndMaxWaitBounds(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Coalesce")
	e.doc(pid, "Chapter", "chapter", "one\n")
	e.settle(pid)
	e.fs.reset()

	// Five changes a second apart, each inside the 2 s window: nothing is
	// written until a quiet gap follows the last one.
	for i := 0; i < 5; i++ {
		e.m.Mark(pid)
		e.clk.Advance(time.Second)
	}
	time.Sleep(30 * time.Millisecond)
	if n := e.fs.writes(); n != 0 {
		t.Fatalf("%d writes while changes kept arriving inside the debounce window", n)
	}
	if !e.m.Status(pid).Pending {
		t.Fatal("the change is not pending")
	}
	e.flush()
	if n := e.fs.wrote("project.json"); n != 0 {
		// Nothing really changed, so even after the debounce no file is rewritten.
		t.Errorf("project.json rewritten %d times for an unchanged project", n)
	}

	// With the content really changing, five marks produce one write of the chapter.
	d := e.call(e.owner, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	var id string
	for _, m := range d.Items {
		if m.Title == "Chapter" {
			id = m.ID
		}
	}
	got := e.call(e.owner, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
	e.setContent(got, "two\n")
	e.fs.reset()
	for i := 0; i < 5; i++ {
		e.m.Mark(pid)
		e.clk.Advance(time.Second)
	}
	e.flush()
	if n := e.fs.wrote("-chapter.md"); n != 1 {
		t.Errorf("the chapter was written %d times, want 1", n)
	}

	// A steady stream, one change a second, must still reach the disk by MaxWait.
	got = e.call(e.owner, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
	e.setContent(got, "three\n")
	e.fs.reset()
	wrote := time.Duration(-1)
	for s := 1; s <= 30; s++ {
		e.m.Mark(pid)
		e.clk.Advance(time.Second)
		// The fake clock fires the mirror's timers inside Advance; the write they
		// start runs on the worker. Wait for that work before the next second, so
		// the second at which the chapter is written is exact, however long a write
		// takes in real time (fsync on Linux, the race detector).
		for deadline := time.Now().Add(30 * time.Second); e.m.WorkerBusy(pid); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("second %d: the worker is still busy after 30 s", s)
			}
		}
		if wrote < 0 && e.fs.wrote("-chapter.md") > 0 {
			wrote = time.Duration(s) * time.Second
		}
	}
	// Without a maximum wait the stream would never be written at all; with it the
	// chapter is written at the second the maximum wait runs out.
	if wrote < 0 || wrote > mirror.DefaultMaxWait+time.Second {
		t.Errorf("a steady stream of changes first reached the disk after %v; want within %v", wrote, mirror.DefaultMaxWait)
	}
}

// ---- atomicity and failure ---------------------------------------------------------

func TestFailedWritesNeverLeaveAPartialFileAndAreRetried(t *testing.T) {
	var logs syncBuf
	e := newEnv(t, func(o *mirror.Options) { o.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	e.start()
	pid := e.project("Atomic")
	ch := e.doc(pid, "Chapter", "chapter", "old text\n")
	e.settle(pid)
	root := e.root(pid)
	var file string
	for _, f := range tree(t, root) {
		if strings.HasSuffix(f, "-chapter.md") {
			file = filepath.Join(root, f)
		}
	}
	// waitFor runs the debounce and retry timers forward until cond holds.
	waitFor := func(what string, cond func(mirror.Status) bool) mirror.Status {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			e.clk.Advance(time.Minute)
			time.Sleep(5 * time.Millisecond)
			if st := e.m.Status(pid); cond(st) {
				return st
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s: %+v", what, e.m.Status(pid))
			}
		}
	}

	// 1. The write dies half way through the temporary file.
	e.fs.mu.Lock()
	e.fs.failTemp = func(int) (int, error) { return 3, errors.New("disk full") }
	e.fs.mu.Unlock()
	ch = e.setContent(ch, "new text, longer than before\n")
	e.caughtUp()
	st := waitFor("the failure", func(s mirror.Status) bool { return s.LastError != "" && !s.InSync })
	if got := read(t, file); got != "old text\n" {
		t.Fatalf("a failed write changed the visible file: %q", got)
	}
	if st.NextRetryAt == "" || !strings.Contains(st.LastError, "disk full") || st.Retries < 1 {
		t.Fatalf("status after a failure: %+v", st)
	}
	// The partial temporary file is outside the project folder.
	for _, f := range tree(t, root) {
		if strings.Contains(f, "w-partial") {
			t.Errorf("a temporary file is visible in the project folder: %s", f)
		}
	}
	// The failure is logged with the project and the reason, not with the
	// data directory's path; the status carries no path either.
	if l := logs.String(); !strings.Contains(l, "level=WARN") || !strings.Contains(l, "a write failed") || !strings.Contains(l, "project="+pid) || !strings.Contains(l, "disk full") {
		t.Errorf("the failure was not logged: %q", l)
	} else if strings.Contains(l, e.dir) || strings.Contains(st.LastError, e.dir) {
		t.Errorf("a path of the data directory reached the log or the status:\n%s\n%s", l, st.LastError)
	}
	// A change that arrives during the backoff does not cut it short.
	retries := st.Retries
	e.m.Mark(pid)
	e.clk.Advance(3 * time.Second) // past the 2 s debounce, inside the 5 s backoff
	time.Sleep(50 * time.Millisecond)
	if got := e.m.Status(pid).Retries; got != retries {
		t.Errorf("a new change retried at once (%d attempts, was %d): the backoff was lost", got, retries)
	}

	// 2. The write completes but the rename fails: still the old file.
	e.fs.mu.Lock()
	e.fs.failTemp = nil
	e.fs.failMove = func(dst string) error {
		if strings.HasSuffix(dst, "-chapter.md") {
			return errors.New("rename refused")
		}
		return nil
	}
	e.fs.mu.Unlock()
	waitFor("the rename failure", func(s mirror.Status) bool { return strings.Contains(s.LastError, "rename refused") })
	if got := read(t, file); got != "old text\n" {
		t.Fatalf("a failed rename changed the visible file: %q", got)
	}
	if e.m.Status(pid).Retries < 2 {
		t.Errorf("the retries should have accumulated: %+v", e.m.Status(pid))
	}

	// 3. The disk recovers: the retry writes the new text and clears the error.
	e.fs.mu.Lock()
	e.fs.failMove = nil
	e.fs.mu.Unlock()
	st = waitFor("recovery", func(s mirror.Status) bool { return s.InSync })
	if got := read(t, file); got != "new text, longer than before\n" {
		t.Fatalf("after recovery: %q", got)
	}
	if st.LastError != "" || st.Retries != 0 {
		t.Errorf("status after recovery: %+v", st)
	}
}

// A failed write never blocks the save that caused it.
func TestASaveNeverWaitsForTheMirror(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.fs.mu.Lock()
	e.fs.failTemp = func(int) (int, error) { return 0, errors.New("disk gone") }
	e.fs.mu.Unlock()
	pid := e.project("Quick")
	start := time.Now()
	ch := e.doc(pid, "Chapter", "chapter", "x\n")
	for i := 0; i < 20; i++ {
		ch = e.setContent(ch, fmt.Sprintf("save %d\n", i))
	}
	// The behaviour (every save succeeds while the mirror fails) is checked above in
	// every run; only the time bound is a timing test.
	d := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 5*time.Second {
			t.Errorf("20 saves took %v with a failing mirror", d)
		}
	})
}

// ---- quiet rewrites, rebuild, boot -------------------------------------------------

func mtimes(t *testing.T, root string) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	for _, f := range tree(t, root) {
		fi, err := os.Stat(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		out[f] = fi.ModTime()
	}
	return out
}

func TestRebuildIsIdempotentAndQuietAndRepairs(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Rebuild")
	e.doc(pid, "One", "chapter", "first\n")
	e.doc(pid, "Two", "chapter", "second\n")
	e.doc(pid, "A note", "note", "note text\n")
	e.settle(pid)
	root := e.root(pid)
	files := tree(t, root)
	if !has(files, "notes/01-a-note.md") {
		t.Fatalf("notes are not mirrored: %v", files)
	}
	before := mtimes(t, root)
	hash := e.m.Status(pid).TreeHash
	time.Sleep(20 * time.Millisecond)
	e.fs.reset()

	for i := 0; i < 2; i++ {
		st, err := e.m.Rebuild(e.ctx, pid)
		if err != nil {
			t.Fatal(err)
		}
		if st.TreeHash != hash || st.LastError != "" || !st.InSync {
			t.Fatalf("rebuild %d: %+v", i, st)
		}
	}
	if n := e.fs.writes(); n != 0 {
		t.Errorf("an unchanged project was rewritten: %d writes", n)
	}
	for f, mt := range mtimes(t, root) {
		if !mt.Equal(before[f]) {
			t.Errorf("%s: mtime changed on an idempotent rebuild", f)
		}
	}

	// Damage: a hand-edited file, a deleted file, and a stray Markdown file.
	one := ""
	for _, f := range files {
		if strings.HasSuffix(f, "-one.md") {
			one = f
		}
	}
	two := ""
	for _, f := range files {
		if strings.HasSuffix(f, "-two.md") {
			two = f
		}
	}
	if err := os.WriteFile(filepath.Join(root, one), []byte("EDITED\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, two)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "manuscript", "99-stray.md"), []byte("stray"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mine.txt"), []byte("not ours"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Rebuild(e.ctx, pid); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(root, one)) != "first\n" || read(t, filepath.Join(root, two)) != "second\n" {
		t.Error("rebuild did not repair the damaged files")
	}
	if _, err := os.Stat(filepath.Join(root, "manuscript", "99-stray.md")); !os.IsNotExist(err) {
		t.Error("rebuild left a stray Markdown file in manuscript/")
	}
	if _, err := os.Stat(filepath.Join(root, "mine.txt")); err != nil {
		t.Error("rebuild removed a file outside manuscript/ and notes/")
	}
	if got := e.m.Status(pid).TreeHash; got != hash {
		t.Errorf("tree hash %s after repair, want %s", got, hash)
	}
}

func TestStartupRebuildsMissingAndStaleMirrorsAndSkipsCurrentOnes(t *testing.T) {
	e := newEnv(t) // the mirror is not running while the data is made
	a := e.project("Alpha")
	b := e.project("Beta")
	chA := e.doc(a, "One", "chapter", "alpha one\n")
	e.doc(b, "One", "chapter", "beta one\n")

	run := func() (*mirror.Mirror, *countFS, func()) {
		fs := &countFS{}
		m, stop := e.bootMirror(fs)
		return m, fs, stop
	}
	settle := func(m *mirror.Mirror) {}

	// First start: every project is missing, so every project is written.
	m1, _, stop1 := run()
	settle(m1)
	for _, id := range []string{a, b} {
		if st := m1.Status(id); st.Files < 3 || !st.InSync {
			t.Fatalf("project %s after first start: %+v", id, st)
		}
	}
	rootA := filepath.Join(e.dir, "mirror", m1.Status(a).Directory)
	stop1()

	// Second start with nothing changed: not one file is written.
	m2, fs2, stop2 := run()
	settle(m2)
	if n := fs2.writes(); n != 0 {
		t.Errorf("a current mirror was rewritten at start-up: %d writes", n)
	}
	stop2()

	// A change made while the mirror was not running: only that project is redone.
	e.setContent(chA, "alpha one, revised\n")
	m3, fs3, stop3 := run()
	settle(m3)
	stop3()
	if fs3.wrote("-one.md") != 1 {
		t.Errorf("want exactly the changed chapter rewritten, got %v", fs3.renames)
	}
	var f string
	for _, x := range tree(t, rootA) {
		if strings.HasSuffix(x, "-one.md") {
			f = x
		}
	}
	if read(t, filepath.Join(rootA, f)) != "alpha one, revised\n" {
		t.Error("the stale chapter was not refreshed")
	}

	// A deleted mirror directory and lost state: rebuilt.
	if err := os.RemoveAll(filepath.Join(e.dir, "mirror")); err != nil {
		t.Fatal(err)
	}
	m4, _, stop4 := run()
	settle(m4)
	defer stop4()
	if !m4.Status(a).InSync || !m4.Status(b).InSync || read(t, filepath.Join(e.dir, "mirror", m4.Status(a).Directory, f)) != "alpha one, revised\n" {
		t.Error("a deleted mirror was not rebuilt")
	}
}

// ---- trigger coverage -----------------------------------------------------------------

// Every op that changes what the mirror shows must wake it. Mutating doc and
// project ops are listed either as mirrored (with the action that proves it) or
// as exempt with the reason; a new op fails this test until it is classified.
func TestEveryMutatingOpReachesTheMirror(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Coverage")
	ch := e.doc(pid, "Chapter", "chapter", "text\n")
	// Compendium material for the ops that change what a project's compendium/
	// folder shows.
	bibleA, bibleB, bibleC := e.compendium("Coverage A"), e.compendium("Coverage B"), e.compendium("Coverage C")
	ada, linked := e.entity(bibleA, "character", "Ada"), e.entity(bibleC, "place", "Harbour")
	e.settle(pid)

	body := func(c docs.Doc) docs.Doc {
		return e.call(e.owner, "doc.get", docs.DocIn{ID: c.ID}).(docs.Item).Item
	}
	// Research, tags and an attachment a chapter links to.
	note := e.call(e.owner, "project.research.create", research.CreateIn{ID: pid, Title: "Coverage note", Body: "Words.", Tags: []string{"one"}}).(research.Item).Item
	folder := e.call(e.owner, "project.research.create", research.CreateIn{ID: pid, Title: "Coverage folder", Kind: "folder"}).(research.Item).Item
	tag1 := e.call(e.owner, "project.tag.create", research.TagCreateIn{ID: pid, Name: "alpha"}).(research.TagItem).Item
	tag2 := e.call(e.owner, "project.tag.create", research.TagCreateIn{ID: pid, Name: "beta"}).(research.TagItem).Item
	att := e.upload(pid, "cover.png", 3)
	ch = e.setContent(ch, "![x]("+att.URL+")\n")
	// A picture made by the cover upload, for the layout ops; the layout is what wakes the mirror, so the picture
	// is stored and flushed first.
	coverCtx := ops.WithRawBody(e.ctx, ops.RawBody{R: bytes.NewReader(pngFile(9)), Length: -1})
	coverPic, cerr := e.reg.Call(coverCtx, e.owner, "project.cover_layout.upload", coverlayout.UploadIn{ID: pid, Part: "front"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	e.settle(pid)
	title := "Coverage 2"
	text := "changed\n"
	// Fixtures for the alternatives and stash ops: made and flushed to the mirror
	// first, so the op under test is what wakes it.
	altFixture := func() (docs.Doc, docs.AltOut) {
		d := e.doc(pid, "Alt fixture", "chapter", "The quiet house.\n")
		out := e.call(e.owner, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: body(d).Revision, Scope: "word", Quote: "quiet", Variants: []string{"still"}}).(docs.AltMut)
		e.settle(pid)
		return d, *out.Alternative
	}
	stashItem := func() stash.Item {
		s := e.call(e.owner, "stash.create", stash.CreateIn{ID: pid, Title: "Fixture", Content: "Fixture words."}).(stash.ItemOut).Item
		e.settle(pid)
		return s
	}
	mirrored := map[string]func(){
		"project.create":  func() { pid2 := e.project("Another"); pid = pid2 },
		"project.update":  func() { e.call(e.owner, "project.update", projects.UpdateIn{ID: pid, Title: &title}) },
		"project.trash":   func() { e.call(e.owner, "project.trash", projects.StateIn{ID: pid}) },
		"project.restore": func() { e.call(e.owner, "project.restore", projects.StateIn{ID: pid}) },
		"project.archive": func() { e.call(e.owner, "project.archive", projects.StateIn{ID: pid}) },
		"project.unarchive": func() {
			e.call(e.owner, "project.unarchive", projects.StateIn{ID: pid})
		},
		"doc.create": func() { e.doc(pid, "Second", "chapter", "two\n") },
		"doc.update": func() {
			cur := body(ch)
			e.call(e.owner, "doc.update", docs.UpdateIn{ID: ch.ID, BaseRevision: cur.Revision, Content: &text})
		},
		"doc.move":  func() { e.call(e.owner, "doc.move", docs.MoveIn{ID: ch.ID, ParentID: ""}) },
		"doc.trash": func() { e.call(e.owner, "doc.trash", docs.DocIn{ID: ch.ID}) },
		"doc.restore": func() {
			e.call(e.owner, "doc.restore", docs.DocIn{ID: ch.ID})
		},
		"doc.split": func() {
			cur := body(ch)
			e.call(e.owner, "doc.split", docs.SplitIn{ID: ch.ID, BaseRevision: cur.Revision, Offset: 3, Title: "Tail"})
		},
		"doc.resolve_link": func() {
			e.call(e.owner, "doc.resolve_link", docs.ResolveLinkIn{ProjectID: pid, Title: "A linked note", Create: true})
		},
		"project.compendium.set": func() {
			e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: bibleA})
		},
		"project.compendium.ref.add": func() {
			e.call(e.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: bibleB})
		},
		"project.compendium.create": func() {
			e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: ""})
			e.call(e.owner, "project.compendium.create", compendiums.ProjectCreateIn{ID: pid, Name: "Created for the project"})
		},
		"project.compendium.ref.remove": func() {
			e.call(e.owner, "project.compendium.ref.remove", compendiums.RefRemoveIn{ID: pid, CompendiumID: bibleB})
		},
		"project.entity.override.set": func() {
			n := "Ada Quill"
			e.call(e.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: ada, Name: &n})
		},
		"project.entity.override.clear": func() {
			e.call(e.owner, "project.entity.override.clear", entities.OverrideClearIn{ID: pid, EntityID: ada})
		},
		"project.entity.link.add": func() {
			e.call(e.owner, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: linked})
		},
		"project.entity.link.remove": func() {
			e.call(e.owner, "project.entity.link.remove", entities.LinkRemoveIn{ID: pid, EntityID: linked})
		},
		"project.research.create": func() {
			e.call(e.owner, "project.research.create", research.CreateIn{ID: pid, Title: "Another note", Body: "More."})
		},
		"project.research.update": func() {
			cur := e.call(e.owner, "project.research.get", research.NoteIn{ID: pid, NoteID: note.ID}).(research.Item).Item
			b := "Changed words."
			e.call(e.owner, "project.research.update", research.UpdateIn{ID: pid, NoteID: note.ID, BaseRevision: cur.Revision, Body: &b})
		},
		"project.research.move": func() {
			p := folder.ID
			e.call(e.owner, "project.research.move", research.MoveIn{ID: pid, NoteID: note.ID, ParentID: &p})
		},
		"project.research.trash": func() {
			e.call(e.owner, "project.research.trash", research.NoteIn{ID: pid, NoteID: note.ID})
		},
		"project.research.restore": func() {
			e.call(e.owner, "project.research.restore", research.NoteIn{ID: pid, NoteID: note.ID})
		},
		"project.tag.update": func() {
			n := "alpha2"
			e.call(e.owner, "project.tag.update", research.TagUpdateIn{ID: pid, TagID: tag1.ID, Name: &n})
		},
		"project.tag.merge": func() {
			e.call(e.owner, "project.tag.merge", research.TagMergeIn{ID: pid, TagID: tag2.ID, IntoID: tag1.ID})
		},
		"project.tag.delete": func() {
			e.call(e.owner, "project.tag.delete", research.TagIn{ID: pid, TagID: tag1.ID})
		},
		"project.attachment.update": func() {
			a := "A new alt"
			e.call(e.owner, "project.attachment.update", attachments.UpdateIn{ID: pid, AttID: att.ID, Alt: &a})
		},
		"project.attachment.delete": func() {
			e.call(e.owner, "project.attachment.delete", attachments.DeleteIn{ID: pid, AttID: att.ID, Force: true})
		},
		// The cover layout is projected as cover/ and the cover object of project.json.
		"project.cover_layout.set": func() {
			front := coverlayout.Side{Mode: "image", Attachment: coverPic.(coverlayout.UploadOut).AttachmentID}
			e.call(e.owner, "project.cover_layout.set", coverlayout.SetIn{ID: pid, Shape: "sides", Front: &front, Alt: "A cover"})
		},
		"project.cover_layout.clear": func() {
			e.call(e.owner, "project.cover_layout.clear", coverlayout.ProjectIn{ID: pid})
		},
		// Alternatives, ghosts and the stash change the stored text or the stash
		// folder: each wakes the mirror and leaves it in step.
		"alternative.create": func() {
			d := e.doc(pid, "Alt create", "chapter", "The quiet house.\n")
			e.settle(pid)
			e.call(e.owner, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: body(d).Revision, Scope: "word", Quote: "quiet", Variants: []string{"still"}})
		},
		"alternative.add_variant": func() {
			d, a := altFixture()
			e.call(e.owner, "alternative.add_variant", docs.AltVariantAddIn{ID: d.ID, BaseRevision: body(d).Revision, AlternativeID: a.ID, Text: "hushed"})
		},
		"alternative.edit_variant": func() {
			d, a := altFixture()
			e.call(e.owner, "alternative.edit_variant", docs.AltVariantEditIn{ID: d.ID, BaseRevision: body(d).Revision, AlternativeID: a.ID, VariantID: a.Variants[1].ID, Text: "stiller"})
		},
		"alternative.remove_variant": func() {
			d, a := altFixture()
			e.call(e.owner, "alternative.remove_variant", docs.AltVariantRemoveIn{ID: d.ID, BaseRevision: body(d).Revision, AlternativeID: a.ID, VariantID: a.Variants[1].ID})
		},
		"alternative.reorder_variant": func() {
			d, a := altFixture()
			e.call(e.owner, "alternative.reorder_variant", docs.AltVariantReorderIn{ID: d.ID, BaseRevision: body(d).Revision, AlternativeID: a.ID, VariantID: a.Variants[1].ID})
		},
		"alternative.choose": func() {
			d, a := altFixture()
			e.call(e.owner, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: body(d).Revision, AlternativeID: a.ID, Step: "next"})
		},
		"alternative.delete": func() {
			d, a := altFixture()
			e.call(e.owner, "alternative.delete", docs.AltDeleteIn{ID: d.ID, BaseRevision: body(d).Revision, AlternativeID: a.ID})
		},
		"alternative.reanchor": func() {
			d, a := altFixture()
			e.call(e.owner, "alternative.reanchor", docs.AltReanchorIn{ID: d.ID, BaseRevision: body(d).Revision, AlternativeID: a.ID, Quote: "house"})
		},
		"ghost.create": func() {
			d := e.doc(pid, "Ghost create", "chapter", "The quiet house.\n")
			e.settle(pid)
			e.call(e.owner, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: body(d).Revision, Ranges: []docs.Range{{Quote: "quiet"}}})
		},
		"ghost.revive": func() {
			d := e.doc(pid, "Ghost revive", "chapter", "The quiet house.\n")
			e.call(e.owner, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: body(d).Revision, Ranges: []docs.Range{{Quote: "quiet"}}})
			e.settle(pid)
			e.call(e.owner, "ghost.revive", docs.GhostReviveIn{ID: d.ID, BaseRevision: body(d).Revision, All: true})
		},
		"stash.create": func() {
			e.call(e.owner, "stash.create", stash.CreateIn{ID: pid, Title: "Mirrored", Content: "Stashed words."})
		},
		"stash.update": func() {
			s := stashItem()
			t := "Mirrored, edited"
			e.call(e.owner, "stash.update", stash.UpdateIn{ID: pid, ItemID: s.ID, BaseRevision: s.Revision, Title: &t})
		},
		"stash.trash": func() {
			s := stashItem()
			e.call(e.owner, "stash.trash", stash.ItemIn{ID: pid, ItemID: s.ID})
		},
		"stash.restore": func() {
			s := stashItem()
			e.call(e.owner, "stash.trash", stash.ItemIn{ID: pid, ItemID: s.ID})
			e.settle(pid)
			e.call(e.owner, "stash.restore", stash.ItemIn{ID: pid, ItemID: s.ID})
		},
		"stash.delete": func() {
			s := stashItem()
			e.call(e.owner, "stash.trash", stash.ItemIn{ID: pid, ItemID: s.ID})
			e.settle(pid)
			e.call(e.owner, "stash.delete", stash.ItemIn{ID: pid, ItemID: s.ID})
		},
		"stash.reorder": func() {
			a, b := stashItem(), stashItem()
			e.settle(pid)
			e.call(e.owner, "stash.reorder", stash.ReorderIn{ID: pid, ItemID: b.ID, AfterItemID: ""})
			_ = a
		},
		"stash.drag_back": func() {
			s := stashItem()
			d := e.doc(pid, "Drag target", "chapter", "Target text.\n")
			e.settle(pid)
			e.call(e.owner, "stash.drag_back", docs.DragBackIn{ID: pid, ItemID: s.ID, DocumentID: d.ID, BaseRevision: body(d).Revision})
		},
		"project.purge": func() {
			e.call(e.owner, "project.trash", projects.StateIn{ID: pid})
			e.call(e.owner, "project.purge", projects.PurgeIn{ID: pid, ConfirmTitle: "Another"})
		},
	}
	// Nothing the mirror shows changes: per-person state, leases, conflict
	// copies (which live in data/conflicts), exports, and the mirror's own ops.
	exempt := map[string]string{
		"project.pin": "per person", "project.open": "per person", "doc.lease": "a lock, not content",
		"doc.conflict": "writes data/conflicts, not the document", "export.markdown": "reads", "mirror.rebuild": "is the mirror",
		"project.member.add": "access, not content", "project.member.update": "access, not content", "project.member.remove": "access, not content",
		"project.passphrase.set": "access, not content: the lock is not in the projection", "project.passphrase.clear": "access, not content",
		"project.unlock": "a session's state, not content", "project.lock": "a session's state, not content",
		"project.typography.set": "how the pages are drawn on screen (F-085), kept in its own table; not content and not in the projection", "project.typography.reset": "how the pages are drawn on screen, not content",
		"project.tag.create":          "a tag no note carries is not in a file; carrying one is project.research.update",
		"project.scratch.set":         "a private scratchpad is never mirrored (TestScratchpadIsNotMirrored)",
		"project.attachment.upload":   "an attachment nothing links to is not mirrored; the document that links it is (doc.update)",
		"project.cover_layout.upload": "a cover picture no layout names is not mirrored; the layout that names it is (project.cover_layout.set)",
		"project.cover_layout.check":  "reads",
	}
	// The AI ops write providers, policies, budgets, never-send flags and the run
	// log: nothing the projection shows, and nothing in the manuscript (a run
	// returns a suggestion; accepting it is doc.update). TestNothingIsWrittenToTheManuscript
	// in internal/domain/ai proves it for runs. A new ai.* op has to be listed here.
	aiExempt := map[string]string{
		"ai.provider.create": "instance settings", "ai.provider.update": "instance settings", "ai.provider.delete": "instance settings",
		"ai.provider.test": "a request to a provider", "ai.provider.models.refresh": "a cached model list", "ai.provider.model.set": "a cached model list",
		"ai.provider.model.remove": "a cached model list", "ai.price.set": "instance settings", "ai.price.delete": "instance settings",
		"ai.budget.set": "instance settings", "ai.settings.set": "instance settings", "ai.policy.set": "a policy the projection does not show",
		"ai.exclude.set": "a flag the projection does not carry", "ai.context.preview": "writes nothing", "ai.run": "returns a suggestion; writes only the run log",
		"ai.run.cancel": "stops a request",
		// The recipes' layer. What is kept (reviews, marks, chats, jobs, held suggestions, style, chains) is
		// not in the projection. The accepts are the ones that change a document, and they do it through the
		// document's own save seam, so the mirror follows the doc.updated event they emit
		// (TestAcceptEmitsTheSaveEvent in internal/domain/aiwork).
		"ai.accept.edit": "writes the document through its save seam: the doc.updated event is what the mirror follows", "ai.accept.alternative": "writes alternatives through the document's save seam (alternative.updated, doc.updated)",
		"ai.accept.reject": "kept results only", "ai.lab.accept": "writes the document through its save seam (doc.updated)", "ai.lab.accept_all": "writes the document through its save seam (doc.updated)",
		"ai.summary.accept": "writes a synopsis through doc.update's own save (doc.updated)", "ai.extract.accept": "creates an entity through entities.Insert and emits entity.created, as entity.create does",
		"ai.extract.dismiss": "kept results only", "ai.review.note.dismiss": "kept results only", "ai.review.note.resolve": "kept results only", "ai.review.delete": "kept results only",
		"ai.lab.analyse": "kept results only", "ai.lab.dismiss": "kept results only", "ai.lab.preview": "writes nothing",
		"ai.summary.rolling.start": "a plan the projection does not show", "ai.summary.rolling.next": "reads", "ai.summary.rolling.skip": "a plan", "ai.summary.rolling.cancel": "a plan",
		"ai.chat.create": "a kept chat", "ai.chat.append": "a kept chat", "ai.chat.delete": "a kept chat", "ai.chat.preview": "writes nothing", "ai.chat.send": "returns a suggestion; writes only the run log and a kept chat",
		"ai.style.set": "the style guide is not in the projection", "ai.defaults.set": "default provider chains",
	}
	for _, en := range e.reg.Entries() {
		if n := en.Op.Name; strings.HasPrefix(n, "ai.") && en.Op.Method != "GET" && aiExempt[n] == "" {
			t.Errorf("%s changes data but is not classified here; does it touch anything the mirror shows?", n)
		}
	}
	for _, en := range e.reg.Entries() {
		n := en.Op.Name
		if !(strings.HasPrefix(n, "doc.") || strings.HasPrefix(n, "project.") || strings.HasPrefix(n, "alternative.") ||
			strings.HasPrefix(n, "ghost.") || strings.HasPrefix(n, "stash.")) || en.Op.Method == "GET" {
			continue
		}
		if mirrored[n] == nil && exempt[n] == "" {
			t.Errorf("%s changes data but is neither covered nor exempt in this test; does the mirror need to see it?", n)
		}
	}
	order := []string{"project.update", "project.research.create", "project.research.update", "project.research.move", "project.research.trash", "project.research.restore",
		"project.tag.update", "project.tag.merge", "project.tag.delete", "project.attachment.update", "project.attachment.delete",
		"project.cover_layout.set", "project.cover_layout.clear",
		"project.compendium.set", "project.compendium.ref.add", "project.entity.override.set", "project.entity.override.clear",
		"project.entity.link.add", "project.entity.link.remove", "project.compendium.ref.remove", "project.compendium.create", "doc.create", "doc.resolve_link", "doc.update", "doc.split", "doc.move", "doc.trash", "doc.restore",
		"alternative.create", "alternative.add_variant", "alternative.edit_variant", "alternative.remove_variant", "alternative.reorder_variant",
		"alternative.choose", "alternative.delete", "alternative.reanchor", "ghost.create", "ghost.revive",
		"stash.create", "stash.update", "stash.trash", "stash.restore", "stash.delete", "stash.reorder", "stash.drag_back",
		"project.archive", "project.unarchive", "project.trash", "project.restore", "project.create", "project.purge"}
	for _, n := range order {
		act := mirrored[n]
		if act == nil {
			t.Fatalf("no action for %s", n)
		}
		before := pid
		act()
		watch := pid
		if n == "project.create" {
			// A new project: wake-up for the new id.
		}
		_ = before
		deadline := time.Now().Add(5 * time.Second)
		for !e.m.Status(watch).Pending && n != "project.purge" {
			if time.Now().After(deadline) {
				t.Fatalf("%s did not wake the mirror", n)
			}
			time.Sleep(2 * time.Millisecond)
		}
		e.flush()
		if n == "project.purge" {
			continue
		}
		if st := e.m.Status(watch); !st.InSync {
			t.Errorf("%s: mirror not in sync afterwards: %+v", n, st)
		}
	}
}

// The notifier wiring end to end with the real clock and no test hooks: an op
// is followed by files on disk.
func TestRealClockEndToEnd(t *testing.T) {
	e := newEnv(t, func(o *mirror.Options) { o.Clock = nil; o.Debounce = 20 * time.Millisecond })
	e.start()
	pid := e.project("Live")
	e.doc(pid, "Chapter", "chapter", "live text\n")
	// The new project is mirrored first (with its seeded first page); wait for the
	// write that carries the chapter, not merely for any in-sync state.
	chapter := func() bool {
		if !e.m.Status(pid).InSync {
			return false
		}
		for _, f := range tree(t, e.root(pid)) {
			if strings.HasSuffix(f, "-chapter.md") && read(t, filepath.Join(e.root(pid), f)) == "live text\n" {
				return true
			}
		}
		return false
	}
	deadline := time.Now().Add(10 * time.Second)
	for !chapter() {
		if time.Now().After(deadline) {
			t.Fatalf("the chapter never reached the mirror: %+v", e.m.Status(pid))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- snapshot hold, disabled, ops --------------------------------------------------------

func TestHoldBlocksWritesUntilReleased(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Hold")
	ch := e.doc(pid, "Chapter", "chapter", "before\n")
	e.settle(pid)
	dir, release, err := e.m.Hold(pid)
	if err != nil {
		t.Fatal(err)
	}
	e.setContent(ch, "after\n")
	e.m.Mark(pid)
	done := make(chan struct{})
	go func() { e.clk.Advance(mirror.DefaultDebounce); _ = e.m.WaitIdle(e.ctx); close(done) }()
	select {
	case <-done:
		t.Fatal("a write went ahead while the tree was held")
	case <-time.After(150 * time.Millisecond):
	}
	for _, f := range tree(t, dir) {
		if strings.HasSuffix(f, "-chapter.md") && read(t, filepath.Join(dir, f)) != "before\n" {
			t.Error("the held tree changed")
		}
	}
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the write did not follow the release")
	}
	if _, _, err := e.m.Hold("prj_nothing"); !errors.Is(err, mirror.ErrNoMirror) {
		t.Errorf("hold on an unmirrored project: %v", err)
	}
}

func TestDisabledMirrorWritesNothingAndSaysSo(t *testing.T) {
	e := newEnv(t, func(o *mirror.Options) { o.Disabled = true })
	e.start()
	pid := e.project("Off")
	e.doc(pid, "Chapter", "chapter", "x\n")
	time.Sleep(50 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(e.dir, "mirror")); !os.IsNotExist(err) {
		t.Errorf("a disabled mirror created its directory: %v", err)
	}
	st := e.call(e.owner, "mirror.status", mirror.StatusIn{ProjectID: pid}).(mirror.Item).Item
	if st.Enabled || st.Pending {
		t.Errorf("status = %+v", st)
	}
	_, err := e.reg.Call(e.ctx, e.owner, "mirror.rebuild", mirror.StatusIn{ProjectID: pid})
	if ae := ops.AsError(err); err == nil || ae.Code != ops.CodeConflict {
		t.Errorf("rebuild with the mirror off: %v", err)
	}
}

func TestStatusAndRebuildOps(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Ops")
	e.doc(pid, "Chapter", "chapter", "x\n")
	e.settle(pid)
	reader := ops.Principal{UserID: testutil.User(t, e.d, ops.RoleReader, "r@t.local"), Role: ops.RoleReader}
	editor := ops.Principal{UserID: testutil.User(t, e.d, ops.RoleWriter, "e@t.local"), Role: ops.RoleWriter}
	for p, role := range map[*ops.Principal]string{&reader: ops.ProjectReader, &editor: ops.ProjectEditor} {
		uid := p.UserID
		if err := e.d.WithTx(e.ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, uid, role, db.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	st := e.call(reader, "mirror.status", mirror.StatusIn{ProjectID: pid}).(mirror.Item).Item
	if !st.Enabled || !st.InSync || st.Files < 3 || st.TreeHash == "" || st.Directory == "" || st.LastWriteAt == "" {
		t.Errorf("status = %+v", st)
	}
	if _, err := e.reg.Call(e.ctx, reader, "mirror.rebuild", mirror.StatusIn{ProjectID: pid}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reader may not rebuild: %v", err)
	}
	re := e.call(editor, "mirror.rebuild", mirror.StatusIn{ProjectID: pid}).(mirror.Item).Item
	if re.TreeHash != st.TreeHash {
		t.Error("rebuild changed the tree hash of an unchanged project")
	}
	if _, err := e.reg.Call(e.ctx, e.owner, "mirror.rebuild", mirror.StatusIn{ProjectID: "prj_00000000000000000000000000"}); err == nil {
		t.Error("rebuilding a project that does not exist should fail")
	}
}

// ---- size ------------------------------------------------------------------------------------

// A 60,000-word, 60-document project mirrors correctly, fast, and one more
// save costs one file.
func TestSixtyThousandWords(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Big Book")
	word := strings.Repeat("word ", 1000)
	want := map[string]string{}
	var last docs.Doc
	for i := 0; i < 60; i++ {
		body := fmt.Sprintf("# Chapter %d\n\n%s\n\n<!-- scriobh:alts {\"version\":1,\"n\":%d} -->\n", i, word, i)
		last = e.doc(pid, fmt.Sprintf("Chapter %02d", i), "chapter", body)
		want[last.ID] = last.Content // the canonical form: this comment is an alternatives line with no alternatives
	}
	e.m.Mark(pid)
	e.settle(pid)
	start := time.Now()
	st, err := e.m.Rebuild(e.ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rebuild of 60 documents / 60,000 words: %v", time.Since(start))
	root := e.root(pid)
	m := manifestOf(t, root)
	// 60 new chapters plus the seeded one.
	if len(m.Documents) != 61 || st.Files != 63 {
		t.Fatalf("%d documents, %d files in status", len(m.Documents), st.Files)
	}
	for _, d := range m.Documents {
		if body, ok := want[d.ID]; ok && read(t, filepath.Join(root, d.Path)) != body {
			t.Fatalf("%s is not what is stored", d.Path)
		}
	}
	// A forced rebuild of an unchanged project reads every file and writes none.
	e.fs.reset()
	start = time.Now()
	if _, err := e.m.Rebuild(e.ctx, pid); err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 2*time.Second {
			t.Errorf("a full compare of 60 documents took %v", d)
		}
	})
	if n := e.fs.writes(); n != 0 {
		t.Errorf("%d writes for nothing", n)
	}
	// One save, one chapter file (and the manifest that lists its hash).
	e.setContent(last, "short now\n")
	e.fs.reset()
	e.settle(pid)
	if n := e.fs.writes(); n != 2 {
		t.Errorf("one save caused %d file writes, want 2 (the chapter and project.json): %v", n, e.fs.renames)
	}
}

// Temporary files from an interrupted write are swept at start.
func TestStaleTemporaryFilesAreSwept(t *testing.T) {
	e := newEnv(t)
	tmp := filepath.Join(e.dir, "mirror", ".tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "w-123"), bytes.Repeat([]byte("x"), 10), 0o600); err != nil {
		t.Fatal(err)
	}
	e.start()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(tmp, "w-123")); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a stale temporary file survived start-up")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The purge hook turns an id into a path. "." and ".." pass a Base check, and
// ".." is the data directory.
func TestPurgeHookRefusesIdsThatAreNotIds(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Keeper")
	e.settle(pid)
	root := e.root(pid)
	for _, bad := range []string{".", "..", "", "../x", "prj_short", "/", "prj_" + strings.Repeat("A", 25) + "/"} {
		e.m.ProjectPurged(e.ctx, bad)
	}
	for _, p := range []string{e.dir, filepath.Join(e.dir, "mirror"), filepath.Join(e.dir, "mirror", ".state"), root, filepath.Join(e.dir, "scriobh.db")} {
		if _, err := os.Stat(p); err != nil {
			if p == filepath.Join(e.dir, "scriobh.db") {
				continue // the test database lives elsewhere
			}
			t.Errorf("%s was removed by a purge of a bad id: %v", p, err)
		}
	}
}
