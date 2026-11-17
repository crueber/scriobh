// Package mirror writes a project to disk as plain Markdown, continuously.
//
// The database is canonical. The mirror is a projection of it: generated,
// never read back, never a second writer. It exists so that grep, git, rsync
// and a text editor always see the work, and so that a catastrophic bug is
// survivable.
//
// Layout, under data/mirror:
//
//	<slug>--<id suffix>/     one folder per project (see dirName)
//	  project.json           manifest: project fields, every document and its hash
//	  README.md              says the folder is generated
//	  manuscript/            numbered tree, the same one the Markdown bundle uses
//	  notes/
//	  compendium/            read-only projection of the Compendium material the project uses
//	compendium-<slug>--<id suffix>/  a Compendium no project uses as its primary (see flushCompendium)
//	.state/<project id>.json what was last written (outside every project folder)
//	.tmp/                    temporary files, on the same file system
//
// How it stays correct:
//
//   - Trigger. The mirror listens to the events the ops write (any event that
//     names a project), reading the events table from a cursor. It does not
//     wrap the event sink, so an op added later is covered the moment it emits
//     an event.
//   - Debounce. A project is rewritten Debounce after its last event, but never
//     later than MaxWait after its first unwritten one, so steady autosave still
//     reaches the disk.
//   - Atomic and quiet. Every file is written to .tmp and renamed into place,
//     so a reader sees the old file or the new one. A file whose bytes are
//     unchanged is not touched, so mtimes and Git diffs stay quiet.
//   - Never blocks a save. All of it happens on a background worker. A failure
//     is logged and retried with backoff; Status says what happened.
//   - Self-healing. At start every project's fingerprint (metadata only) is
//     compared with the one recorded when it was last written; a missing or
//     stale mirror is rebuilt. Rebuild forces a full comparison against the
//     bytes on disk.
package mirror

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/ops"
)

// Timing defaults.
const (
	DefaultDebounce = 2 * time.Second
	DefaultMaxWait  = 15 * time.Second
	retryBase       = 5 * time.Second
	retryMax        = 5 * time.Minute
	pollEvery       = 30 * time.Second // a fallback: a missed wake costs at most this
	drainBudget     = 10 * time.Second // how long shutdown may spend writing what is pending
)

// ErrNoMirror means the project has not been written yet.
var ErrNoMirror = errors.New("mirror: this project has no mirror yet")

// Timer is what a Clock's AfterFunc returns.
type Timer interface{ Stop() bool }

// Clock is the time source for debouncing; tests inject a fake one.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Timer
}

type realClock struct{}

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Notifier wakes the mirror when new events are committed (events.Store).
type Notifier interface {
	Subscribe() (<-chan struct{}, func())
}

// Options configure a Mirror.
type Options struct {
	DataDir  string
	DB       *db.DB
	Log      *slog.Logger
	Notifier Notifier
	// Disabled turns the mirror off: nothing is written, Status says so, and a
	// purge still removes whatever an earlier run left.
	Disabled bool
	Debounce time.Duration // default DefaultDebounce
	MaxWait  time.Duration // default DefaultMaxWait
	Clock    Clock         // default the real clock
	FS       FS            // default OSFS
}

// Mirror is the projection writer.
type Mirror struct {
	o                      Options
	root, stateDir, tmpDir string
	clk                    Clock
	fs                     FS
	log                    *slog.Logger
	debounce, maxWait      time.Duration

	mu        sync.Mutex
	ps        map[string]*project
	queue     []string
	ready     chan struct{}
	cursor    atomic.Int64 // newest event sequence number seen
	onWrite   []func(projectID string)
	running   bool
	done      chan struct{}
	started   chan struct{} // closed when the start-up check and the first poll are done
	startOnce sync.Once
}

// project is one project's scheduling state and last-written record.
type project struct {
	id string
	// rw: a flush holds the write lock, a snapshot reader (Hold) the read lock,
	// so a reader never sees a tree half way through a rewrite.
	rw sync.RWMutex

	// Guarded by Mirror.mu.
	dirty     bool
	first     time.Time // when the oldest unwritten change arrived
	timer     Timer
	queued    bool
	flushing  bool
	retries   int
	lastErr   string
	lastErrAt time.Time
	nextRetry time.Time
	loaded    bool
	st        *diskState
}

// diskState is what the mirror last wrote for a project. It lives in
// .state/, never inside the project's folder. Treated as immutable: a flush
// builds a new one and swaps it in.
type diskState struct {
	Dir         string            `json:"dir"`
	Fingerprint string            `json:"fingerprint"`
	Files       map[string]string `json:"files"` // path -> sha256 of what was written
	WrittenAt   string            `json:"written_at"`
	// Projection is the fingerprint of the compendium/ projection the files
	// under compendium/ were written from. While it is unchanged, a project's
	// flush carries those records forward instead of rendering every entity
	// again, so a chapter save never re-renders a 5,000-entity bible.
	Projection string `json:"projection,omitempty"`
}

// New returns a Mirror. It does not touch the disk until it runs.
func New(o Options) *Mirror {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Debounce <= 0 {
		o.Debounce = DefaultDebounce
	}
	if o.MaxWait <= 0 {
		o.MaxWait = DefaultMaxWait
	}
	if o.MaxWait < o.Debounce {
		o.MaxWait = o.Debounce
	}
	m := &Mirror{o: o, log: o.Log, debounce: o.Debounce, maxWait: o.MaxWait,
		clk: o.Clock, fs: o.FS, ps: map[string]*project{}, ready: make(chan struct{}, 1), done: make(chan struct{}), started: make(chan struct{})}
	if m.clk == nil {
		m.clk = realClock{}
	}
	if m.fs == nil {
		m.fs = OSFS{}
	}
	m.root = filepath.Join(o.DataDir, "mirror")
	m.stateDir = filepath.Join(m.root, ".state")
	m.tmpDir = filepath.Join(m.root, ".tmp")
	return m
}

// Cursor is the newest event sequence number the mirror has taken notice of.
func (m *Mirror) Cursor() int64 { return m.cursor.Load() }

// Enabled reports whether the mirror writes anything.
func (m *Mirror) Enabled() bool { return !m.o.Disabled }

// Root is the mirror's directory inside the data directory.
func (m *Mirror) Root() string { return m.root }

// OnWrite registers a function called (on the worker, after the project's lock
// is released) each time a project's mirror changed on disk or was removed. The
// Git push uses it to schedule a push. It must not block.
func (m *Mirror) OnWrite(f func(projectID string)) {
	m.mu.Lock()
	m.onWrite = append(m.onWrite, f)
	m.mu.Unlock()
}

// Start launches the mirror in the background and returns at once. It stops
// when ctx is cancelled, after writing what is still pending (bounded by a few
// seconds). Wait blocks until that is done. Calling Start twice is a no-op.
func (m *Mirror) Start(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()
	go m.run(ctx)
}

// Started returns a channel that is closed once a started mirror has finished
// its start-up check (every stale project queued, the event cursor taken and
// the first poll made), or has stopped without one. Work it queued may still
// be pending: WaitIdle waits for that.
func (m *Mirror) Started() <-chan struct{} { return m.started }

func (m *Mirror) markStarted() { m.startOnce.Do(func() { close(m.started) }) }

// Wait blocks until a started mirror has stopped. It returns at once if the
// mirror was never started.
func (m *Mirror) Wait() {
	m.mu.Lock()
	r := m.running
	m.mu.Unlock()
	if r {
		<-m.done
	}
}

func (m *Mirror) run(ctx context.Context) {
	defer close(m.done)
	defer m.markStarted()
	if m.o.Disabled {
		return
	}
	for _, d := range []string{m.root, m.stateDir} {
		if err := m.fs.MkdirAll(d); err != nil {
			m.log.Error("mirror: cannot create its directory; the mirror is off until restart", "dir", d, "err", cause(err))
			return
		}
	}
	_ = m.fs.RemoveAll(m.tmpDir) // leftovers of an interrupted write

	var wg sync.WaitGroup
	wctx, stopWorker := context.WithCancel(ctx)
	wg.Add(1)
	go func() { defer wg.Done(); m.worker(wctx) }()

	// Subscribe first, then read the cursor, then reconcile, then poll once:
	// an event that lands at any point in between is seen by the reconcile, by
	// the poll, or by a wake-up, never by none of them.
	var wake <-chan struct{}
	if m.o.Notifier != nil {
		ch, cancel := m.o.Notifier.Subscribe()
		defer cancel()
		wake = ch
	}
	m.cursor.Store(m.maxSeq(ctx))
	m.reconcile(ctx)
	m.poll(ctx)
	m.markStarted()
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-wake:
			m.poll(ctx)
		case <-tick.C:
			m.poll(ctx)
		}
	}
	stopWorker()
	wg.Wait()
	m.drain()
}

// maxSeq is the newest event sequence number.
func (m *Mirror) maxSeq(ctx context.Context) int64 {
	var n int64
	if err := m.o.DB.Read().QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) FROM events`).Scan(&n); err != nil {
		m.log.Warn("mirror: reading the event cursor failed", "err", err)
	}
	return n
}

// poll marks every project that has events past the cursor.
func (m *Mirror) poll(ctx context.Context) {
	tx, err := m.o.DB.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return
	}
	defer tx.Rollback()
	var upper int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) FROM events`).Scan(&upper); err != nil || upper <= m.cursor.Load() {
		return
	}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT project_id, compendium_id FROM events WHERE seq > ? AND seq <= ? AND (project_id IS NOT NULL OR compendium_id IS NOT NULL)
		AND type NOT LIKE 'comment.%' AND type NOT LIKE 'review.%' AND type NOT LIKE 'share.%' AND type NOT LIKE 'suggestion.%' AND type NOT LIKE 'export.%'`, m.cursor.Load(), upper)
	if err != nil {
		m.log.Warn("mirror: reading events failed", "err", err)
		return
	}
	seen := map[string]bool{}
	var ids, compendiums []string
	for rows.Next() {
		var pid, cid sql.NullString
		if err := rows.Scan(&pid, &cid); err != nil {
			continue
		}
		if pid.Valid && !seen[pid.String] {
			seen[pid.String] = true
			ids = append(ids, pid.String)
		}
		if cid.Valid && !seen[cid.String] {
			seen[cid.String] = true
			compendiums = append(compendiums, cid.String)
		}
	}
	rows.Close()
	// A change to a Compendium rewrites the standalone mirror of that
	// Compendium and the compendium/ folder of every project that uses it.
	for _, cid := range compendiums {
		ids = append(ids, cid)
		affected, err := entities.AffectedProjects(ctx, tx, cid)
		if err != nil {
			m.log.Warn("mirror: finding the projects of a changed Compendium failed", "err", err)
			continue
		}
		for _, pid := range affected {
			if !seen[pid] {
				seen[pid] = true
				ids = append(ids, pid)
			}
		}
		// A Compendium that references this one names it in its manifest.
		if refs, err := entities.ReferencingCompendiums(ctx, tx, cid); err == nil {
			for _, rid := range refs {
				if !seen[rid] {
					seen[rid] = true
					ids = append(ids, rid)
				}
			}
		}
	}
	for _, id := range ids {
		m.Mark(id)
	}
	// The cursor moves after the marks, so a reader that waits for it (a test, a
	// status page) never sees "caught up" while a change is not yet pending.
	m.cursor.Store(upper)
}

// reconcile queues every project whose mirror is missing or stale, and removes
// the mirror of a project that no longer exists. It reads metadata only.
func (m *Mirror) reconcile(ctx context.Context) {
	fps, _, err := fingerprints(ctx, m.o.DB.Read(), "")
	if err != nil {
		m.log.Warn("mirror: could not check the mirrors at start-up", "err", err)
		return
	}
	cfps, err := compendiumFingerprints(ctx, m.o.DB.Read(), "")
	if err != nil {
		m.log.Warn("mirror: could not check the Compendium mirrors at start-up", "err", err)
		return
	}
	for id, fp := range fps {
		st := m.loadState(m.proj(id))
		if st.Fingerprint != fp || st.Dir == "" || !m.exists(filepath.Join(m.root, st.Dir, ManifestName)) {
			m.enqueue(id)
		}
	}
	for id, fp := range cfps {
		fps[id] = fp
		st := m.loadState(m.proj(id))
		if st.Fingerprint != fp || st.Dir == "" || !m.exists(filepath.Join(m.root, st.Dir, CompendiumManifestName)) {
			m.enqueue(id)
		}
	}
	// A state file whose project is gone: it was purged while the server was
	// down, or the purge hook failed. Only folders this mirror recorded are removed.
	entries, err := m.fs.ReadDir(m.stateDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		if id == e.Name() || id == "" {
			continue
		}
		if _, ok := fps[id]; !ok {
			m.enqueue(id) // the flush finds the project gone and removes the folder
		}
	}
}

func (m *Mirror) exists(path string) bool {
	_, err := m.fs.Stat(path)
	return err == nil
}

// Mark records that a project changed. The write follows after the debounce.
func (m *Mirror) Mark(projectID string) {
	if m.o.Disabled || projectID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.projLocked(projectID)
	now := m.clk.Now()
	p.dirty = true
	if p.first.IsZero() {
		p.first = now
	}
	delay := m.debounce
	if left := m.maxWait - now.Sub(p.first); left < delay {
		delay = left
		if delay < 0 {
			delay = 0
		}
	}
	// A failing write waits out its backoff however much is being typed.
	if p.nextRetry.After(now) {
		if wait := p.nextRetry.Sub(now); wait > delay {
			delay = wait
		}
	}
	m.armLocked(p, delay)
}

// armLocked (re)starts the project's timer; when it fires the project is queued.
func (m *Mirror) armLocked(p *project, d time.Duration) {
	if p.timer != nil {
		p.timer.Stop()
	}
	id := p.id
	p.timer = m.clk.AfterFunc(d, func() { m.enqueue(id) })
}

func (m *Mirror) enqueue(id string) {
	m.mu.Lock()
	p := m.projLocked(id)
	if !p.queued {
		p.queued = true
		p.dirty = true
		m.queue = append(m.queue, id)
	}
	m.mu.Unlock()
	select {
	case m.ready <- struct{}{}:
	default:
	}
}

func (m *Mirror) proj(id string) *project {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.projLocked(id)
}

func (m *Mirror) projLocked(id string) *project {
	p := m.ps[id]
	if p == nil {
		p = &project{id: id}
		m.ps[id] = p
	}
	return p
}

func (m *Mirror) pop() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) == 0 {
		return "", false
	}
	id := m.queue[0]
	m.queue = m.queue[1:]
	return id, true
}

// worker writes queued projects one at a time, so a burst of changes cannot
// turn into a burst of disk work.
func (m *Mirror) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.ready:
		}
		for {
			if ctx.Err() != nil {
				return
			}
			id, ok := m.pop()
			if !ok {
				break
			}
			m.flushQueued(ctx, id)
		}
	}
}

// flushQueued runs one flush from the queue and schedules the retry on failure.
func (m *Mirror) flushQueued(ctx context.Context, id string) {
	m.mu.Lock()
	// Leaving the queue and starting the flush are one step, so a project is never
	// seen as neither queued nor being written while its flush is about to run.
	p := m.projLocked(id)
	p.queued, p.flushing = false, true
	m.mu.Unlock()
	_, err := m.flush(ctx, id, false)
	switch {
	case err != nil && ctx.Err() == nil:
		m.retryLater(id)
	case err != nil:
		// Interrupted by shutdown part way: the write is still owed, so the
		// final drain (which has a context of its own) must do it.
		m.mu.Lock()
		m.projLocked(id).dirty = true
		m.mu.Unlock()
	}
}

// retryLater schedules another attempt with exponential backoff.
func (m *Mirror) retryLater(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.projLocked(id)
	d := retryBase << min(p.retries-1, 6)
	if p.retries < 1 || d > retryMax {
		d = retryMax
	}
	p.dirty = true
	p.nextRetry = m.clk.Now().Add(d)
	m.armLocked(p, d)
	m.log.Warn("mirror: a write failed; it will be retried", "project", id, "attempt", p.retries, "retry_in", d.String(), "err", p.lastErr)
}

// cause is the error without the paths in it. A *PathError message carries the
// absolute path of the data directory and a file name made from a title; the
// operating system's own reason ("no space left on device") is what a log or a
// status needs.
func cause(err error) string {
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		return pe.Err.Error()
	case errors.As(err, &le):
		return le.Err.Error()
	}
	return err.Error()
}

// drain writes whatever is still pending, once, when the server stops.
func (m *Mirror) drain() {
	m.mu.Lock()
	var ids []string
	for id, p := range m.ps {
		if p.dirty || p.queued {
			ids = append(ids, id)
		}
		if p.timer != nil {
			p.timer.Stop()
		}
	}
	m.queue = nil
	m.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	sort.Strings(ids)
	ctx, cancel := context.WithTimeout(context.Background(), drainBudget)
	defer cancel()
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		if _, err := m.flush(ctx, id, false); err != nil {
			m.log.Warn("mirror: a pending write could not be finished at shutdown; it is redone at the next start", "project", id, "err", cause(err))
		}
	}
}

// WaitIdle blocks until no project is waiting, queued or being written, or ctx
// ends. A project waiting out a retry backoff counts as waiting. It is for
// tests and for the CLI.
func (m *Mirror) WaitIdle(ctx context.Context) error {
	for {
		m.mu.Lock()
		busy := false
		for _, p := range m.ps {
			if p.dirty || p.queued || p.flushing {
				busy = true
				break
			}
		}
		m.mu.Unlock()
		if !busy {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ---- writing ------------------------------------------------------------------

// Rebuild rewrites a project's mirror now, comparing every file against the
// bytes on disk (so a hand-edited or damaged file is repaired). Unchanged files
// are left alone. It returns the status afterwards; a failure is in Status, not
// returned, so a caller can show it.
func (m *Mirror) Rebuild(ctx context.Context, projectID string) (Status, error) {
	if m.o.Disabled {
		return Status{}, errors.New("the mirror is switched off")
	}
	var fps map[string]string
	var err error
	what := "project"
	if isCompendiumID(projectID) {
		what = "compendium"
		fps, err = compendiumFingerprints(ctx, m.o.DB.Read(), projectID)
	} else {
		fps, _, err = fingerprints(ctx, m.o.DB.Read(), projectID)
	}
	if err != nil {
		return Status{}, err
	}
	if _, ok := fps[projectID]; !ok {
		if isCompendiumID(projectID) {
			var n int
			_ = m.o.DB.Read().QueryRowContext(ctx, `SELECT count(*) FROM compendiums WHERE id = ? AND deleted_at IS NULL`, projectID).Scan(&n)
			if n > 0 {
				return Status{}, ops.Conflict("This Compendium is the primary of a project, so it has no folder of its own: its entities are in that project's compendium/ folder. Rebuild the project instead.")
			}
		}
		return Status{}, ops.NotFound(what)
	}
	_, err = m.flush(ctx, projectID, true)
	if err != nil && ctx.Err() == nil {
		m.retryLater(projectID)
	}
	return m.Status(projectID), nil
}

// flush brings one project's mirror in line with the database.
func (m *Mirror) flush(ctx context.Context, id string, force bool) (changed bool, err error) {
	p := m.proj(id)
	p.rw.Lock()
	m.mu.Lock()
	p.dirty, p.first, p.flushing = false, time.Time{}, true
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	m.mu.Unlock()
	var removed bool
	defer func() {
		m.mu.Lock()
		p.flushing = false
		if err != nil {
			p.retries++
			p.lastErr, p.lastErrAt = err.Error(), m.clk.Now()
		} else {
			p.retries, p.lastErr, p.lastErrAt, p.nextRetry = 0, "", time.Time{}, time.Time{}
		}
		hooks := m.onWrite
		m.mu.Unlock()
		p.rw.Unlock()
		if err == nil && changed {
			for _, h := range hooks {
				h(id)
			}
		}
		if removed {
			m.mu.Lock()
			if !p.dirty && !p.queued {
				delete(m.ps, id)
			}
			m.mu.Unlock()
		}
	}()

	st := m.loadState(p)
	if isCompendiumID(id) {
		changed, removed, err = m.flushCompendium(ctx, p, id, st, force)
		return changed, err
	}
	// Fingerprint first, snapshot second: a change between them leaves a
	// fingerprint older than the files, which only costs one more check later.
	fps, projs, err := fingerprints(ctx, m.o.DB.Read(), id)
	if err != nil {
		return false, err
	}
	fp, live := fps[id]
	if !live {
		removed = true
		return m.removeProject(p, st)
	}
	snap, err := export.LoadSnapshot(ctx, m.o.DB, id, includeNotes)
	if err != nil {
		if ae := ops.AsError(err); ae.Code == ops.CodeNotFound {
			removed = true
			return m.removeProject(p, st)
		}
		return false, err
	}
	exFiles, ext, metas, err := m.extras(ctx, id)
	if err != nil {
		return false, err
	}
	files, err := renderWith(snap, metas)
	if err != nil {
		return false, err
	}
	for n, b := range exFiles {
		files[n] = b
	}
	// The compendium/ projection. While its fingerprint is unchanged the
	// recorded files are carried forward without being rendered again.
	projFP := projs[id]
	dir := dirName(id, snap.Project.Slug)
	carry := !force && projFP != "" && st.Projection == projFP && st.Dir == dir && m.projectionPresent(st, dir)
	if projFP != "" && !carry {
		pf, perr := entities.Projection(ctx, m.o.DB, id)
		if perr != nil {
			return false, perr
		}
		for _, f := range pf {
			files[f.Path] = f.Content
		}
	}
	next, changedPaths, err := m.apply(id, st, dir, files, ext, carry, force)
	next.Fingerprint = fp
	next.Projection = projFP
	if err != nil {
		next.Fingerprint = "" // not known to be complete: the next start retries
		next.Projection = ""
	}
	if len(changedPaths) > 0 {
		next.WrittenAt = m.stamp()
	}
	if serr := m.saveState(p, id, next); serr != nil && err == nil {
		err = fmt.Errorf("recording what was written: %s", cause(serr))
	}
	if err != nil {
		return len(changedPaths) > 0, err
	}
	return len(changedPaths) > 0, nil
}

func (m *Mirror) stamp() string { return m.clk.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// apply writes the files into dir, rewriting only those whose bytes differ,
// and removes the files of documents that are gone. It returns the new state
// and the paths it changed. On error the state lists what is known to be right.
func (m *Mirror) apply(id string, st *diskState, dir string, files map[string][]byte, ext map[string]extFile, carry, force bool) (*diskState, []string, error) {
	next := &diskState{Dir: dir, Files: map[string]string{}, WrittenAt: st.WrittenAt}
	if carry {
		// The compendium/ files are not in files: what was recorded stays.
		for n, v := range st.Files {
			if carried(n) {
				next.Files[n] = v
			}
		}
	}
	var changed []string
	oldDir := st.Dir
	if oldDir == "" {
		oldDir = m.findDir(id) // state lost: adopt a folder that is plainly this project's
	}
	moved := false
	var leftover string
	if oldDir != "" && oldDir != dir {
		switch {
		case m.exists(filepath.Join(m.root, oldDir)) && !m.exists(filepath.Join(m.root, dir)):
			if err := m.fs.Rename(filepath.Join(m.root, oldDir), filepath.Join(m.root, dir)); err != nil {
				return next, changed, fmt.Errorf("moving the folder to its new name: %s", cause(err))
			}
			moved = true
			changed = append(changed, "(moved)")
		case m.exists(filepath.Join(m.root, dir)):
			leftover = oldDir // both exist: write the new one, then remove the old
			moved = true
		}
	}
	if st.Dir == "" || st.Dir != oldDir {
		force = true // nothing recorded to trust: compare against the disk
	}
	base := filepath.Join(m.root, dir)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		h := hashBytes(data)
		full := filepath.Join(base, filepath.FromSlash(name))
		if !force && st.Files[name] == h {
			if fi, err := m.fs.Stat(full); err == nil && fi.Mode().IsRegular() && fi.Size() == int64(len(data)) {
				next.Files[name] = h
				continue
			}
		}
		if cur, err := m.fs.ReadFile(full); err == nil && bytes.Equal(cur, data) {
			next.Files[name] = h
			continue
		}
		if err := writeFileAtomic(m.fs, m.tmpDir, full, data); err != nil {
			// Keep what is known to be right for the files not reached yet.
			for n, v := range st.Files {
				if _, done := next.Files[n]; !done {
					if _, want := files[n]; want {
						next.Files[n] = v
					}
				}
			}
			delete(next.Files, name)
			return next, changed, fmt.Errorf("writing %s: %s", name, cause(err))
		}
		next.Files[name] = h
		changed = append(changed, name)
	}
	// Attachments: binary files copied from data/files, by hash. A file whose
	// recorded hash and size still match is left alone; after a force it is
	// compared with the source; a source that is missing or damaged is skipped
	// (and logged), never a reason to stop writing the rest.
	extNames := make([]string, 0, len(ext))
	for n := range ext {
		extNames = append(extNames, n)
	}
	sort.Strings(extNames)
	for _, name := range extNames {
		e := ext[name]
		full := filepath.Join(base, filepath.FromSlash(name))
		if fi, err := m.fs.Stat(full); err == nil && fi.Mode().IsRegular() && fi.Size() == e.Size {
			if !force && st.Files[name] == e.SHA {
				next.Files[name] = e.SHA
				continue
			}
			if h, err := fileHash(full); err == nil && h == e.SHA {
				next.Files[name] = e.SHA
				continue
			}
		}
		if err := m.copyIn(e, full); err != nil {
			m.log.Warn("mirror: an attachment was not copied", "project", id, "err", cause(err))
			continue
		}
		next.Files[name] = e.SHA
		changed = append(changed, name)
	}
	// Remove the files of documents that are gone. Normally the old record
	// says which; after a force, a move or lost state, walk what is there.
	var stale []string
	if force || moved {
		want := make(map[string][]byte, len(files)+len(ext))
		for n, b := range files {
			want[n] = b
		}
		for n := range ext {
			want[n] = nil
		}
		stale = m.walkStale(base, want, staleRoots(id, carry))
	} else {
		for n := range st.Files {
			_, want := files[n]
			_, wantExt := ext[n]
			if !want && !wantExt && !(carry && carried(n)) {
				stale = append(stale, n)
			}
		}
	}
	sort.Strings(stale)
	for _, n := range stale {
		full := filepath.Join(base, filepath.FromSlash(n))
		if err := m.fs.Remove(full); err != nil && !os.IsNotExist(err) {
			return next, changed, fmt.Errorf("removing %s: %s", n, cause(err))
		}
		changed = append(changed, n)
		m.pruneEmpty(base, filepath.Dir(full))
	}
	if leftover != "" {
		_ = m.fs.RemoveAll(filepath.Join(m.root, leftover))
	}
	return next, changed, nil
}

// walkStale lists the Markdown files under manuscript/, notes/ and compendium/
// that the project no longer has (compendium/ only when its files were rendered
// this time). Nothing outside those folders is ours to remove.
func (m *Mirror) walkStale(base string, want map[string][]byte, roots []string) []string {
	var stale []string
	var walk func(rel string)
	walk = func(rel string) {
		entries, err := m.fs.ReadDir(filepath.Join(base, filepath.FromSlash(rel)))
		if err != nil {
			return
		}
		for _, e := range entries {
			r := rel + "/" + e.Name()
			if e.IsDir() {
				walk(r)
				continue
			}
			if _, ok := want[r]; !ok {
				stale = append(stale, r)
			}
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return stale
}

// projectionPresent reports whether every compendium/ file the record says was
// written is still there as a regular file. Files that are rendered again are
// compared against the disk; files that are carried forward are not rendered,
// so this is what keeps a deleted or damaged folder from being trusted: one
// missing file and the whole projection is written again.
func (m *Mirror) projectionPresent(st *diskState, dir string) bool {
	base := filepath.Join(m.root, dir)
	n := 0
	for name := range st.Files {
		if !carried(name) {
			continue
		}
		n++
		fi, err := m.fs.Stat(filepath.Join(base, filepath.FromSlash(name)))
		if err != nil || !fi.Mode().IsRegular() {
			return false
		}
	}
	return n > 0
}

// staleRoots are the folders whose unaccounted-for files are ours to remove: a
// project's manuscript, notes and (when it was rendered this time) compendium
// projection, or a standalone Compendium's entities and research.
func staleRoots(id string, carry bool) []string {
	if isCompendiumID(id) {
		return []string{"entities", researchDir, attachmentsDir}
	}
	if carry {
		return []string{"manuscript", "notes", researchDir, stashDir, attachmentsDir, strings.TrimSuffix(projectResearch, "/")}
	}
	return []string{"manuscript", "notes", researchDir, stashDir, attachmentsDir, "compendium"}
}

// pruneEmpty removes dir and its parents up to base while they are empty.
func (m *Mirror) pruneEmpty(base, dir string) {
	for dir != base && strings.HasPrefix(dir, base+string(filepath.Separator)) {
		if err := m.fs.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// removeProject deletes a project's mirror folder and its state: the project is
// gone. It only removes a folder this mirror recorded or can prove is the
// project's own.
func (m *Mirror) removeProject(p *project, st *diskState) (bool, error) {
	dir := st.Dir
	if dir == "" {
		dir = m.findDir(p.id)
	}
	if dir != "" {
		if err := m.fs.RemoveAll(filepath.Join(m.root, dir)); err != nil {
			return false, err
		}
	}
	_ = m.fs.Remove(m.statePath(p.id))
	m.mu.Lock()
	p.st, p.loaded = &diskState{}, true
	m.mu.Unlock()
	return dir != "", nil
}

// findDir looks for a folder that belongs to the project when no state says
// where it is: the name ends with the id suffix and project.json carries the id.
func (m *Mirror) findDir(id string) string {
	if isCompendiumID(id) {
		return m.findCompendiumDir(id)
	}
	entries, err := m.fs.ReadDir(m.root)
	if err != nil {
		return ""
	}
	suffix := "--" + idSuffix(id)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		b, err := m.fs.ReadFile(filepath.Join(m.root, e.Name(), ManifestName))
		if err != nil {
			continue
		}
		var mf struct {
			Project struct {
				ID string `json:"id"`
			} `json:"project"`
		}
		if json.Unmarshal(b, &mf) == nil && mf.Project.ID == id {
			return e.Name()
		}
	}
	return ""
}

// ProjectPurged is the purge hook: it cancels pending work and removes the
// project's folder. A flush that races it finds the project gone and removes
// the folder too, rather than recreating it.
func (m *Mirror) ProjectPurged(ctx context.Context, projectID string) {
	if !db.ValidID(projectID) {
		return
	}
	// Even with the mirror off, a folder from an earlier run is removed.
	m.mu.Lock()
	p := m.projLocked(projectID)
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.dirty, p.queued = false, false
	m.mu.Unlock()
	if _, err := m.flush(ctx, projectID, true); err != nil {
		m.log.Warn("mirror: could not remove the folder of a purged project", "project", projectID, "err", cause(err))
		m.retryLater(projectID)
	}
}

// ---- state --------------------------------------------------------------------

func (m *Mirror) statePath(id string) string { return filepath.Join(m.stateDir, id+".json") }

func (m *Mirror) loadState(p *project) *diskState {
	m.mu.Lock()
	if p.loaded {
		st := p.st
		m.mu.Unlock()
		return st
	}
	m.mu.Unlock()
	st := &diskState{}
	if b, err := m.fs.ReadFile(m.statePath(p.id)); err == nil {
		var s diskState
		if json.Unmarshal(b, &s) == nil && s.Files != nil && validDir(s.Dir) {
			st = &s
		}
	}
	m.mu.Lock()
	p.st, p.loaded = st, true
	m.mu.Unlock()
	return st
}

// validDir keeps a damaged or hostile state file from naming a path outside
// the mirror.
func validDir(d string) bool {
	return d != "" && d != "." && d != ".." && filepath.Base(d) == d && !strings.HasPrefix(d, ".")
}

// equal reports whether two records say the same.
func (a *diskState) equal(b *diskState) bool {
	if a == nil || b == nil || a.Dir != b.Dir || a.Fingerprint != b.Fingerprint || a.WrittenAt != b.WrittenAt || a.Projection != b.Projection || len(a.Files) != len(b.Files) {
		return false
	}
	for k, v := range a.Files {
		if b.Files[k] != v {
			return false
		}
	}
	return true
}

// saveState records what was written. An unchanged record is not rewritten.
func (m *Mirror) saveState(p *project, id string, st *diskState) error {
	m.mu.Lock()
	old, had := p.st, p.loaded
	p.st, p.loaded = st, true
	m.mu.Unlock()
	if had && st.equal(old) && m.exists(m.statePath(id)) {
		return nil
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(m.fs, m.tmpDir, m.statePath(id), b)
}

// ---- reading ------------------------------------------------------------------

// Status says how a project's mirror stands.
type Status struct {
	Enabled     bool   `json:"enabled" desc:"False when the mirror is switched off for the whole instance."`
	Pending     bool   `json:"pending" desc:"A change is waiting to be written, or a write is in progress."`
	InSync      bool   `json:"in_sync" desc:"The mirror was written from the current state and nothing is pending or failing."`
	Directory   string `json:"directory" desc:"The project's folder name under data/mirror; empty until the first write."`
	LastWriteAt string `json:"last_write_at" desc:"When a file in the mirror last changed; empty if never."`
	LastError   string `json:"last_error" desc:"The last write failure; empty when the last attempt succeeded."`
	LastErrorAt string `json:"last_error_at,omitempty"`
	Retries     int    `json:"retries" desc:"Consecutive failed attempts."`
	NextRetryAt string `json:"next_retry_at,omitempty" desc:"When the next attempt runs after a failure."`
	Files       int    `json:"files" desc:"Files in the mirror, project.json and README.md included."`
	TreeHash    string `json:"tree_hash" desc:"SHA-256 over every file's path and content hash; equal hashes mean equal mirrors."`
}

// Status reports on one project. It never touches the disk.
func (m *Mirror) Status(projectID string) Status {
	s := Status{Enabled: !m.o.Disabled}
	if m.o.Disabled {
		return s
	}
	p := m.proj(projectID)
	st := m.loadState(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	s.Pending = p.dirty || p.queued || p.flushing
	s.Directory = st.Dir
	s.LastWriteAt = st.WrittenAt
	s.Files = len(st.Files)
	if len(st.Files) > 0 {
		s.TreeHash = treeHash(st.Files)
	}
	s.LastError, s.Retries = p.lastErr, p.retries
	if !p.lastErrAt.IsZero() {
		s.LastErrorAt = p.lastErrAt.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	if !p.nextRetry.IsZero() {
		s.NextRetryAt = p.nextRetry.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	s.InSync = !s.Pending && p.lastErr == "" && st.Fingerprint != ""
	return s
}

// Current reports whether the project's files were written from the state the database is in now: nothing is
// pending or being written, and the fingerprint the files were written under (metadata and content hashes of every
// document) is the one the database gives today. A push to a repository people can read is built only from a
// mirror that is current. It reads the database, so it costs a query over the project's documents.
func (m *Mirror) Current(ctx context.Context, projectID string) (bool, error) {
	if m.o.Disabled {
		return false, errors.New("the mirror is switched off")
	}
	p := m.proj(projectID)
	st := m.loadState(p)
	m.mu.Lock()
	pending := p.dirty || p.queued || p.flushing
	m.mu.Unlock()
	if pending || st.Dir == "" || st.Fingerprint == "" {
		return false, nil
	}
	fps, _, err := fingerprints(ctx, m.o.DB.Read(), projectID)
	if err != nil {
		return false, err
	}
	fp, ok := fps[projectID]
	return ok && fp == st.Fingerprint, nil
}

// Hold gives a reader the project's mirror folder as a consistent tree: while
// the returned release function has not been called, no write to this project
// proceeds, so every file and the manifest belong to one state. Hold it only as
// long as it takes to copy or commit the tree. It fails with ErrNoMirror when
// the project has not been written yet.
func (m *Mirror) Hold(projectID string) (dir string, release func(), err error) {
	if m.o.Disabled {
		return "", nil, errors.New("the mirror is switched off")
	}
	p := m.proj(projectID)
	p.rw.RLock()
	st := m.loadState(p)
	if st.Dir == "" || st.Fingerprint == "" {
		p.rw.RUnlock()
		return "", nil, ErrNoMirror
	}
	return filepath.Join(m.root, st.Dir), p.rw.RUnlock, nil
}
