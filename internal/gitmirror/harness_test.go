package gitmirror_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/testutil"
)

// ---- a manual clock ------------------------------------------------------------

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	firing *fakeTimer // the timer whose callback Advance is running, so AfterFunc can see a re-arm
}

type fakeTimer struct {
	c       *fakeClock
	at      time.Time
	f       func()
	stopped bool
	fired   bool
	tick    bool // the service's periodic check (AfterFuncPeriodic), not any timer
	rearm   bool // a tick armed from inside a tick's callback during this Advance
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

func (c *fakeClock) AfterFunc(d time.Duration, f func()) gitmirror.Timer { return c.arm(d, f, false) }

// AfterFuncPeriodic is what the service arms its periodic check with (gitmirror.PeriodicClock): the one
// timer Advance runs once per call however long the jump.
func (c *fakeClock) AfterFuncPeriodic(d time.Duration, f func()) gitmirror.Timer {
	return c.arm(d, f, true)
}

func (c *fakeClock) arm(d time.Duration, f func(), tick bool) gitmirror.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f, tick: tick, rearm: tick && c.firing != nil && c.firing.tick}
	c.timers = append(c.timers, t)
	return t
}

// Pending counts armed timers.
func (c *fakeClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

// Advance moves time forward and runs every timer that came due, in order. The service arms its periodic check
// through AfterFuncPeriodic (gitmirror.PeriodicClock), so the clock knows which timer that is rather than guessing
// from a delay (the debounce equals the poll interval, 30 s). It is the only timer treated specially: a tick that
// a tick's callback re-armed within this call does not run again at its own time, it runs once more at the end,
// when the clock reads the target, which is what a Go timer does in a process that was suspended and woke up. So
// a call runs the check at most twice (once when it was due, once at the target) however long the jump:
// Advance(48h) is two reads of the database, not 5,760, and each cost about 30 ms under -race.
//
// This is not free: a check can change state the next check acts on (it queues a retry whose backoff ended), so a
// test that must see each expiry advances in steps shorter than the wait (the backoff tests use 5 s and 35 s) or
// calls svc.Check itself. Every long jump in the tests is one whose passes in between would have found nothing
// new: the state did not change between them. Timers that are not periodic (a debounce, a delivery retry) fire at
// their own times, in order.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	for _, t := range c.timers {
		t.rearm = false
	}
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.stopped && !t.fired && !t.rearm && !t.at.After(target) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			// Only the deferred ticks are left (or nothing): the clock reads the target, and each tick runs once.
			var ticks []*fakeTimer
			for _, t := range c.timers {
				if !t.stopped && !t.fired && t.rearm && !t.at.After(target) {
					ticks = append(ticks, t)
				}
			}
			c.now = target
			if len(ticks) == 0 {
				c.mu.Unlock()
				return
			}
			for _, t := range ticks {
				t.fired = true
			}
			c.mu.Unlock()
			for _, t := range ticks {
				c.mu.Lock()
				c.firing = t
				c.mu.Unlock()
				t.f()
				c.mu.Lock()
				c.firing = nil
				c.mu.Unlock()
			}
			continue
		}
		next.fired = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.firing = next
		c.mu.Unlock()
		next.f()
		c.mu.Lock()
		c.firing = nil
		c.mu.Unlock()
	}
}

// ---- a stand-in for the mirror ---------------------------------------------------

// mdoc is one document of the fake mirror.
type mdoc struct {
	path, kind, title string
	words             int
	body              string
}

// fakeSource serves a folder laid out the way the mirror writes it.
type fakeSource struct {
	mu       sync.RWMutex
	dir      string
	hash     string
	noMirror bool
	behind   bool
	held     int
}

func newSource(t *testing.T) *fakeSource {
	return &fakeSource{dir: t.TempDir()}
}

func (f *fakeSource) Hold(string) (string, func(), error) {
	f.mu.RLock()
	if f.noMirror {
		f.mu.RUnlock()
		return "", nil, mirror.ErrNoMirror
	}
	return f.dir, f.mu.RUnlock, nil
}

func (f *fakeSource) Status(string) mirror.Status {
	return mirror.Status{Enabled: true, TreeHash: f.hash, InSync: true}
}

// Write replaces the folder's content with a mirror of the given documents,
// plus a README and a stray file, which the push must not publish.
func (f *fakeSource) Write(title string, includesNotes bool, docs []mdoc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries, _ := os.ReadDir(f.dir)
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(f.dir, e.Name()))
	}
	type md struct {
		ID        string `json:"id"`
		Path      string `json:"path"`
		Kind      string `json:"kind"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		WordCount int    `json:"word_count"`
		SHA256    string `json:"sha256"`
	}
	var list []md
	h := sha256.New()
	for i, d := range docs {
		full := filepath.Join(f.dir, filepath.FromSlash(d.path))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(d.body), 0o644); err != nil {
			panic(err)
		}
		sum := sha256.Sum256([]byte(d.body))
		list = append(list, md{ID: fmt.Sprintf("doc_%d", i), Path: d.path, Kind: d.kind, Title: d.title, Status: "draft", WordCount: d.words, SHA256: hex.EncodeToString(sum[:])})
		fmt.Fprintf(h, "%s %x\n", d.path, sum)
	}
	mf := map[string]any{
		"schema_version": 1, "format": "scriobh-mirror",
		"project": map[string]any{"id": "prj_x", "slug": "the-salt-road", "title": title},
		"state":   "active", "includes_notes": includesNotes, "documents": list,
	}
	b, _ := json.MarshalIndent(mf, "", "  ")
	_ = os.WriteFile(filepath.Join(f.dir, "project.json"), append(b, '\n'), 0o644)
	_ = os.WriteFile(filepath.Join(f.dir, "README.md"), []byte("# A generated copy\n"), 0o644)
	_ = os.WriteFile(filepath.Join(f.dir, "stray-secret.md"), []byte("not listed by the manifest\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(f.dir, ".state"), 0o755)
	_ = os.WriteFile(filepath.Join(f.dir, ".state", "x.json"), []byte("{}"), 0o644)
	fmt.Fprintf(h, "%s %v\n", title, includesNotes)
	f.hash = hex.EncodeToString(h.Sum(nil))
}

// ---- the harness -----------------------------------------------------------------

type harness struct {
	t       *testing.T
	d       *db.DB
	reg     *ops.Registry
	svc     *gitmirror.Service
	src     *fakeSource
	clk     *fakeClock
	logs    *safeBuf
	dataDir string
	owner   ops.Principal
	editor  ops.Principal
	reader  ops.Principal
	outside ops.Principal
	pid     string
	cancel  context.CancelFunc
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

var chapters = []mdoc{
	{"manuscript/01-the-harbour.md", "chapter", "The Harbour", 120, "The quiet harbour.\n"},
	{"manuscript/02-the-road.md", "chapter", "The Road", 80, "Salt on the wind.\n"},
	{"notes/01-ideas.md", "note", "Ideas", 5, "Maybe a storm.\n"},
}

// newHarness builds the services over a temp database, with the fake mirror, a
// manual clock and log capture, a project and four people: owner, editor,
// reader and an outsider.
func newHarness(t *testing.T, tune ...func(*gitmirror.Options)) *harness {
	t.Helper()
	d := testutil.DB(t)
	logs := &safeBuf{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dataDir := t.TempDir()
	reg := registry.Build(d, events.NewStore(d, log), dataDir, log)
	src := newSource(t)
	clk := newClock()
	opt := gitmirror.Options{DataDir: dataDir, DB: d, Source: src, Box: secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), Log: log, Clock: clk,
		AllowFile: true, AllowPrivate: true, BackoffBase: time.Minute, BackoffMax: 8 * time.Minute, PollEvery: 30 * time.Second}
	for _, f := range tune {
		f(&opt)
	}
	svc := gitmirror.New(opt)
	reg.Env().SetService(gitmirror.ServiceName, svc)
	reg.Env().AddPurgeHook(svc.ProjectPurged)

	mk := func(role, email string) ops.Principal {
		return ops.Principal{UserID: testutil.User(t, d, role, email), Role: role, Email: email}
	}
	h := &harness{t: t, d: d, reg: reg, svc: svc, src: src, clk: clk, logs: logs, dataDir: dataDir,
		owner: mk("writer", "mairead@test.local"), editor: mk("writer", "editor@test.local"), reader: mk("reader", "reader@test.local"), outside: mk("writer", "stranger@test.local")}
	out, err := reg.Call(context.Background(), h.owner, "project.create", projects.CreateIn{Title: "The Salt Road"})
	if err != nil {
		t.Fatal(err)
	}
	h.pid = out.(projects.Item).Item.ID
	for p, role := range map[string]string{h.editor.UserID: "editor", h.reader.UserID: "reader"} {
		err := d.WithTx(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, h.pid, p, role, db.Now())
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	src.Write("The Salt Road", true, chapters)
	return h
}

// start runs the service's workers until the test ends.
func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.svc.Start(ctx)
	h.t.Cleanup(func() { cancel(); h.svc.Wait() })
}

func (h *harness) call(p ops.Principal, op string, in any) (any, error) {
	h.t.Helper()
	return h.reg.Call(context.Background(), p, op, in)
}

func (h *harness) must(p ops.Principal, op string, in any) any {
	h.t.Helper()
	out, err := h.call(p, op, in)
	if err != nil {
		h.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (h *harness) code(err error) ops.Code {
	if err == nil {
		return "none"
	}
	return ops.AsError(err).Code
}

// bareRemote makes an empty bare repository and returns its file:// address
// and a handle on it.
func (h *harness) bareRemote() (string, *git.Repository, string) {
	h.t.Helper()
	dir := filepath.Join(h.t.TempDir(), "remote.git")
	url := initBare(h.t, dir)
	repo, _ := git.PlainOpen(dir)
	return url, repo, dir
}

// addRemote creates a remote with the given address.
func (h *harness) addRemote(url string, mut ...func(*gitmirror.CreateIn)) gitmirror.Remote {
	h.t.Helper()
	in := gitmirror.CreateIn{Audience: "private", ProjectID: h.pid, Name: "Backup", URL: url, PushMode: "manual"}
	for _, f := range mut {
		f(&in)
	}
	return h.must(h.owner, "git.remote.create", in).(gitmirror.RemoteItem).Item
}

// push pushes by hand and waits for the worker to finish.
func (h *harness) push(p ops.Principal) gitmirror.Push {
	h.t.Helper()
	out := h.must(p, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.PushOut)
	return h.settle(out.Item.ID)
}

func (h *harness) settle(pushID string) gitmirror.Push {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := h.svc.WaitIdle(ctx); err != nil {
		h.t.Fatalf("the push did not finish: %v\n%s", err, h.logs.String())
	}
	return h.must(h.owner, "git.push_get", gitmirror.PushGetIn{ProjectID: h.pid, PushID: pushID}).(gitmirror.PushItem).Item
}

func (h *harness) remote() gitmirror.Remote {
	h.t.Helper()
	return h.must(h.owner, "git.remote.get", gitmirror.RemoteQuery{ProjectID: h.pid}).(gitmirror.RemoteItem).Item
}

// head is the commit a bare repository's branch points at, or zero.
func head(t *testing.T, dir, branch string) plumbing.Hash {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return plumbing.ZeroHash
	}
	return ref.Hash()
}

func sortedKeys[V any](m map[string]V) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// initBare makes an empty bare repository whose head is main and returns its
// file:// address.
func initBare(t *testing.T, dir string) string {
	t.Helper()
	repo, err := git.PlainInit(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		t.Fatal(err)
	}
	return "file://" + dir
}

// useUnstartedService swaps in a second service over the same database that has
// not been started, so that pushes asked for stay queued until h.start().
func (h *harness) useUnstartedService() {
	h.t.Helper()
	svc := gitmirror.New(gitmirror.Options{DataDir: h.dataDir, DB: h.d, Source: h.src, Box: secrets.New([]byte("test-secret-key-test-secret-key-0123456789")),
		Log: slog.New(slog.NewTextHandler(h.logs, nil)), Clock: h.clk, AllowFile: true, AllowPrivate: true, BackoffBase: time.Minute, BackoffMax: 8 * time.Minute, PollEvery: time.Hour})
	h.reg.Env().SetService(gitmirror.ServiceName, svc)
	h.svc = svc
}

// newProject creates another project owned by the owner and returns its id.
func (h *harness) newProject(title string) string {
	h.t.Helper()
	out, err := h.reg.Call(context.Background(), h.owner, "project.create", projects.CreateIn{Title: title})
	if err != nil {
		h.t.Fatal(err)
	}
	return out.(projects.Item).Item.ID
}

// stampSources sets every import source's time to the manual clock's, which is
// what expiry compares against in a test (the row is written with the real clock).
func (h *harness) stampSources() {
	h.t.Helper()
	if _, err := h.d.Writer().Exec(`UPDATE project_sources SET imported_at = ?`, h.clk.Now().UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
		h.t.Fatal(err)
	}
}
