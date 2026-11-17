// Package gitmirror pushes a project's Markdown mirror to a Git repository the
// writer owns, and imports a repository into a new project.
//
// Push-only is the contract. Scríobh writes; it never pulls, merges or rebases.
// The remote is a versioned copy, not a second writer:
//
//   - The commit is built from a consistent snapshot of the mirror (taken under
//     mirror.Hold and released before any network call) into a bare local
//     repository under data/git/<project>/<remote>/, which only ever receives
//     commits built that way.
//   - Before pushing, the remote's branch head is read (ls-remote). If it is the
//     head of the local history or one of its ancestors the push goes ahead; if
//     the remote has commits that are not ours the push is refused, recorded as
//     rejected with the commits that are not ours, and nothing is touched. The
//     only exits are a force push (typed confirmation, owner only, a local bundle
//     of the remote ref first) and an import.
//   - Credentials are sealed with the instance key (internal/secrets) and open
//     only for the length of one transport call. SSH host keys are pinned on an
//     explicit accept; a changed host key is a hard error, never a prompt.
//
// The work runs on a bounded background worker. Triggers are the mirror's
// write notifications (debounced per project), an optional interval and manual
// pushes; failures back off and never retry in a loop.
package gitmirror

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/webhook"
)

// ServiceName is the name the service is registered under in ops.Env.
const ServiceName = "git"

// Timing defaults.
const (
	DefaultDebounce    = 30 * time.Second
	DefaultMaxWait     = 5 * time.Minute
	DefaultPollEvery   = 30 * time.Second
	DefaultBackoffBase = time.Minute
	DefaultBackoffMax  = 30 * time.Minute
	DefaultWorkers     = 2
	pushTimeout        = 5 * time.Minute
	keepPushes         = 500
	jobKeepFor         = 24 * time.Hour
)

// DefaultMaxRepoMB is the default SCRIOBH_GIT_MAX_REPO_MB.
const DefaultMaxRepoMB = 512

// Source is the mirror, as the push sees it.
type Source interface {
	Hold(projectID string) (dir string, release func(), err error)
	Status(projectID string) mirror.Status
}

// Timer is what a Clock's AfterFunc returns.
type Timer = mirror.Timer

// Clock is the time source; tests inject a manual one.
type Clock = mirror.Clock

// A Clock may also implement PeriodicClock, to be told which of its timers is the service's periodic
// check. A manual clock in a test uses that to run a long jump's worth of checks once, which is what a
// real timer does in a process that was suspended; nothing else uses it, and a real clock need not.
type PeriodicClock interface {
	AfterFuncPeriodic(d time.Duration, f func()) Timer
}

type realClock struct{}

func (realClock) Now() time.Time                            { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Options configure a Service.
type Options struct {
	DataDir string
	DB      *db.DB
	Source  Source
	Box     *secrets.Box
	Log     *slog.Logger
	Clock   Clock

	Debounce    time.Duration // after the last mirror write, default 30 s
	MaxWait     time.Duration // never later than this after the first unpushed change, default 5 min
	PollEvery   time.Duration // how often interval, retry and catch-up checks run, default 30 s
	BackoffBase time.Duration // first retry delay after a failure, default 1 min
	BackoffMax  time.Duration // default 30 min
	Workers     int           // default 2

	// MaxRepoBytes caps what a clone, a scratch fetch or a bundle may hold
	// (SCRIOBH_GIT_MAX_REPO_MB). Zero means the default.
	MaxRepoBytes int64

	// Disk caps (see quota.go). For the two quotas, zero means the default and
	// a negative number means none. BundlesKeep zero means the default;
	// KeepImportDays zero means the default and negative means keep forever.
	UserQuotaBytes  int64
	TotalQuotaBytes int64
	BundlesKeep     int
	KeepImportDays  int

	// AllowPrivate lets Git sync connect to loopback, private and other
	// internal addresses (SCRIOBH_GIT_ALLOW_PRIVATE). Off by default: an editor
	// could otherwise point a remote at the server's own network.
	AllowPrivate bool

	// AllowFile permits file:// remotes and imports. It is a seam for tests: a
	// file address would let an editor read or write a directory of the server.
	AllowFile bool

	// TriggerSender sends the trigger deliveries (nil builds one from the webhook package). A test seam: a
	// loopback test server needs a sender that trusts its certificate.
	TriggerSender *webhook.Sender
	// ReleaseMaxBytes caps a release asset (64 MB). Zero means the default.
	ReleaseMaxBytes int64
	// Compile builds the edition a release publishes: the registry's export ops, run as the person who asked.
	// Set by the server (SetCompiler); a release is refused when it is nil.
	Compile Compiler
}

// Service is the Git push engine and the repository importer.
type Service struct {
	o   Options
	log *slog.Logger

	mu      sync.Mutex
	deb     map[string]*debounce // by project id
	queue   []*job
	active  map[string]*job // by remote id: queued or running
	ready   chan struct{}
	started bool
	ctx     context.Context
	done    chan struct{}
	wg      sync.WaitGroup
	impWG   sync.WaitGroup // clones of repository imports
	tick    Timer

	// Triggers (trigger.go) and releases (release.go).
	trigMu     sync.Mutex
	trigSender *webhook.Sender
	trigReady  chan struct{}
	deliverMu  sync.Mutex
	relMu      sync.Mutex
	relActive  map[string]bool // by remote id and tag
	relWG      sync.WaitGroup
}

// debounce is one project's pending automatic push.
type debounce struct {
	first time.Time
	timer Timer
}

// job is one push, from the claim of its row to its end.
type job struct {
	remoteID, projectID string
	trigger, by         string
	force               bool
	push                *pushRow // owned by the worker once the job runs
	snap                pushRow  // the row as it was claimed: what callers are given
	url, branch, name   string   // the remote as it was when the push was queued
	audience            string   // and what it was allowed to carry
	onlyDone            bool
	includeCover        bool

	// Guarded by Service.mu.
	again     bool // an automatic trigger arrived while it ran: push again afterwards
	cancel    context.CancelFunc
	cancelled bool
	started   bool
}

// New returns a Service. It does not touch the disk or the network until it runs.
func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Clock == nil {
		o.Clock = realClock{}
	}
	if o.Box == nil {
		o.Box = secrets.New(nil)
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
	if o.PollEvery <= 0 {
		o.PollEvery = DefaultPollEvery
	}
	if o.BackoffBase <= 0 {
		o.BackoffBase = DefaultBackoffBase
	}
	if o.BackoffMax <= 0 {
		o.BackoffMax = DefaultBackoffMax
	}
	if o.Workers <= 0 {
		o.Workers = DefaultWorkers
	}
	if o.MaxRepoBytes <= 0 {
		o.MaxRepoBytes = DefaultMaxRepoMB << 20
	}
	switch {
	case o.UserQuotaBytes == 0:
		o.UserQuotaBytes = DefaultUserQuotaMB << 20
	case o.UserQuotaBytes < 0:
		o.UserQuotaBytes = 0
	}
	switch {
	case o.TotalQuotaBytes == 0:
		o.TotalQuotaBytes = DefaultTotalQuotaMB << 20
	case o.TotalQuotaBytes < 0:
		o.TotalQuotaBytes = 0
	}
	if o.BundlesKeep == 0 {
		o.BundlesKeep = DefaultBundlesKeep
	}
	if o.KeepImportDays == 0 {
		o.KeepImportDays = DefaultKeepImportDay
	}
	configureNet(o.AllowPrivate, o.MaxRepoBytes, o.Log)
	return &Service{o: o, log: o.Log, deb: map[string]*debounce{}, active: map[string]*job{},
		ready: make(chan struct{}, 1), done: make(chan struct{}), trigReady: make(chan struct{}, 1), relActive: map[string]bool{}}
}

// Enabled reports whether credentials can be stored (an instance key exists).
func (s *Service) Enabled() bool { return s.o.Box.Enabled() }

// Start launches the workers and the periodic check, and returns at once. It
// stops when ctx is cancelled; Wait blocks until the workers have finished.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started, s.ctx = true, ctx
	s.mu.Unlock()
	s.recoverAtStart(ctx)
	for i := 0; i < s.o.Workers; i++ {
		s.wg.Add(1)
		go s.worker(ctx)
	}
	s.wg.Add(1)
	go s.triggerLoop(ctx)
	select { // deliveries a stopped server left waiting
	case s.trigReady <- struct{}{}:
	default:
	}
	s.mu.Lock()
	s.scheduleTick()
	s.mu.Unlock()
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		if s.tick != nil {
			s.tick.Stop()
		}
		for _, d := range s.deb {
			if d.timer != nil {
				d.timer.Stop()
			}
		}
		for _, j := range s.active {
			if j.cancel != nil {
				j.cancel()
			}
		}
		s.mu.Unlock()
		s.wg.Wait()
		s.impWG.Wait()
		close(s.done)
	}()
}

// Wait blocks until a started service has stopped; it returns at once if it
// was never started.
func (s *Service) Wait() {
	s.mu.Lock()
	st := s.started
	s.mu.Unlock()
	if st {
		<-s.done
	}
}

// recoverAtStart makes the database agree with a process that is not running
// any pushes: a row left 'running' by a crash would otherwise hold the remote's
// one-running-push slot forever. It also checks stored credentials against the
// current key and removes expired import clones.
func (s *Service) recoverAtStart(ctx context.Context) {
	now := stamp(s.o.Clock.Now())
	res, err := s.o.DB.Writer().ExecContext(ctx, `UPDATE git_pushes SET status = 'error', outcome = ?, error_code = ?, error_message = ?, finished_at = ?
		WHERE status = 'running'`, CodeInterrupted, CodeInterrupted, "The server stopped while this push was running. It is tried again with the next change or by hand.", now)
	if err != nil {
		s.log.Warn("git: could not close pushes left running by an earlier run", "err", cause(err))
	} else if n, _ := res.RowsAffected(); n > 0 {
		s.log.Info("git: closed pushes that were running when the server stopped", "count", n)
	}
	if _, err := s.o.DB.Writer().ExecContext(ctx, `UPDATE git_releases SET status = 'error', error_code = ?, error_message = ?, finished_at = ? WHERE status = 'running'`,
		CodeInterrupted, "The server stopped while this release was running. Run it again: an asset that is already there is not uploaded twice.", now); err != nil {
		s.log.Warn("git: could not close releases left running by an earlier run", "err", cause(err))
	}
	if msg, err := CheckCredentials(ctx, s.o.DB, s.o.Box, false); err != nil {
		s.log.Warn("git: could not check stored credentials", "err", cause(err))
	} else if msg != "" {
		s.log.Warn("git: " + msg)
	}
	s.recoverImports(ctx)
	s.sweepImports(ctx)
}

// ---- triggers ----------------------------------------------------------------

// Notify tells the service a project's mirror changed on disk. It is the
// function to give to Mirror.OnWrite, and it never blocks: it only moves a
// timer. When the timer fires, the project's on-save remotes are queued.
func (s *Service) Notify(projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		return
	}
	now := s.o.Clock.Now()
	d := s.deb[projectID]
	if d == nil {
		d = &debounce{first: now}
		s.deb[projectID] = d
	}
	wait := s.o.Debounce
	if left := s.o.MaxWait - now.Sub(d.first); left < wait {
		wait = max(left, 0)
	}
	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = s.o.Clock.AfterFunc(wait, func() { s.fire(projectID) })
}

// fire runs when a project's debounce ends.
func (s *Service) fire(projectID string) {
	s.mu.Lock()
	delete(s.deb, projectID)
	ctx := s.ctx
	s.mu.Unlock()
	if ctx == nil || ctx.Err() != nil {
		return
	}
	rs, err := s.remotesOf(ctx, projectID)
	if err != nil {
		return
	}
	for _, r := range rs {
		if r.Enabled && r.PushMode == ModeOnSave && s.autoEligible(r) {
			s.enqueueAuto(ctx, r, TriggerSave)
		}
	}
}

// autoEligible says whether an automatic trigger may push this remote: set up,
// not parked waiting for a person, and not inside the wait that follows a
// failure. Every automatic path (the save debounce, the interval, the periodic
// check and the push-again after a push) goes through it, so a failing host is
// tried on the backoff schedule however busy the writing is. Manual pushes
// ignore all of it.
func (s *Service) autoEligible(r *remoteRow) bool {
	if len(setupMissing(r)) > 0 || r.State == StateRejected {
		return false
	}
	if r.State == StateBlocked && r.LastErrorCode != CodeSecretInTree {
		// Parked for a person. A refusal for a secret in the text is different:
		// the text is what changes, and the periodic check only queues a push
		// when the mirror's hash differs from the one that was refused.
		return false
	}
	if r.NextAttemptAt != "" && s.o.Clock.Now().Before(parseStamp(r.NextAttemptAt)) {
		return false
	}
	return true
}

// scheduleTick arms the next periodic check. Callers hold s.mu.
func (s *Service) scheduleTick() {
	if s.ctx == nil || s.ctx.Err() != nil {
		return
	}
	arm := s.o.Clock.AfterFunc
	if pc, ok := s.o.Clock.(PeriodicClock); ok {
		arm = pc.AfterFuncPeriodic
	}
	s.tick = arm(s.o.PollEvery, func() {
		s.check(s.ctx)
		s.mu.Lock()
		s.scheduleTick()
		s.mu.Unlock()
	})
}

// check is the periodic pass: it queues interval pushes that are due, retries
// remotes whose backoff has ended, and catches changes a missed notification
// (or a restart) left unpushed.
func (s *Service) check(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	now := s.o.Clock.Now()
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT `+remoteCols+` FROM git_remotes WHERE enabled = 1`)
	if err != nil {
		return
	}
	var rs []*remoteRow
	for rows.Next() {
		if r, err := scanRemote(rows); err == nil {
			rs = append(rs, r)
		}
	}
	rows.Close()
	for _, r := range rs {
		if !s.autoEligible(r) {
			continue
		}
		if r.PushMode == ModeManual {
			continue
		}
		st := s.o.Source.Status(r.ProjectID)
		if st.Pending {
			continue // the mirror is about to notify
		}
		s.mu.Lock()
		_, pending := s.deb[r.ProjectID]
		_, busy := s.active[r.ID]
		s.mu.Unlock()
		if pending || busy {
			continue
		}
		retryDue := r.State == StateBehind && !now.Before(parseStamp(r.NextAttemptAt))
		behind := r.LocalSHA != "" && r.LocalSHA != r.LastCommitSHA
		changed := st.TreeHash != "" && st.TreeHash != r.MirrorHash
		switch r.PushMode {
		case ModeInterval:
			due := r.LastAttemptAt == "" || now.Sub(parseStamp(r.LastAttemptAt)) >= time.Duration(r.IntervalMinutes)*time.Minute
			if retryDue {
				s.enqueueAuto(ctx, r, TriggerRetry)
			} else if due && (changed || (behind && !now.Before(parseStamp(r.NextAttemptAt)))) {
				s.enqueueAuto(ctx, r, TriggerInterval)
			}
		case ModeOnSave:
			if retryDue {
				s.enqueueAuto(ctx, r, TriggerRetry)
			} else if changed && r.State != StateBehind {
				s.enqueueAuto(ctx, r, TriggerSave)
			}
		}
	}
	s.sweepImports(ctx)
	s.deliverDue(ctx)
}

// ---- the queue -----------------------------------------------------------------

// enqueueAuto queues an automatic push. If one is already queued or running
// for the remote it asks for another when that one ends, so the newest change
// is never left behind.
func (s *Service) enqueueAuto(ctx context.Context, r *remoteRow, trigger string) {
	if _, _, err := s.enqueue(ctx, r, trigger, "", false); err != nil && err != errBusy {
		s.log.Warn("git: could not queue a push", "remote", r.ID, "err", cause(err))
	}
}

// enqueue claims the remote's one running-push slot and queues the job. If the
// remote already has a push in flight it returns that push instead, and marks
// it to run again when it ends (for automatic triggers).
func (s *Service) enqueue(ctx context.Context, r *remoteRow, trigger, by string, force bool) (*pushRow, bool, error) {
	// A job stays in the active set for a moment after its row has been
	// settled. The row is the truth about whether a push is running: if the
	// active job's row is no longer running, wait for the worker to let go
	// rather than telling a person that a finished push is still in flight.
	for spins := 0; ; spins++ {
		s.mu.Lock()
		j := s.active[r.ID]
		if j == nil {
			s.mu.Unlock()
			break
		}
		cp := j.snap
		s.mu.Unlock()
		if s.runningPush(ctx, r.ID) != nil || spins >= 1500 {
			s.mu.Lock()
			if j2 := s.active[r.ID]; j2 == j && (trigger == TriggerSave || trigger == TriggerInterval || trigger == TriggerRetry) {
				j.again = true
			}
			s.mu.Unlock()
			return &cp, true, nil
		}
		time.Sleep(2 * time.Millisecond)
	}
	p, err := s.claimPush(ctx, r, trigger, by, force)
	if err != nil {
		if err == errBusy {
			if rp := s.runningPush(ctx, r.ID); rp != nil {
				return rp, true, nil
			}
		}
		return nil, false, err
	}
	j := &job{remoteID: r.ID, projectID: r.ProjectID, trigger: trigger, by: by, force: force, push: p, snap: *p, url: r.URL, branch: r.Branch, name: r.Name, audience: r.Audience, onlyDone: r.OnlyDone, includeCover: r.IncludeCover}
	s.mu.Lock()
	s.active[r.ID] = j
	s.queue = append(s.queue, j)
	s.mu.Unlock()
	select {
	case s.ready <- struct{}{}:
	default:
	}
	cp := j.snap
	return &cp, false, nil
}

func (s *Service) pop() *job {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.queue) > 0 {
		j := s.queue[0]
		s.queue = s.queue[1:]
		if j.cancelled {
			continue
		}
		j.started = true
		return j
	}
	return nil
}

func (s *Service) worker(ctx context.Context) {
	defer s.wg.Done()
	for {
		for {
			j := s.pop()
			if j == nil {
				break
			}
			if ctx.Err() != nil {
				s.abandon(j)
				continue
			}
			s.run(ctx, j)
			select {
			case s.ready <- struct{}{}: // there may be more
			default:
			}
		}
		select {
		case <-ctx.Done():
			// Whatever is still queued is marked cancelled.
			for j := s.pop(); j != nil; j = s.pop() {
				s.abandon(j)
			}
			return
		case <-s.ready:
		}
	}
}

// abandon ends a job that never started.
func (s *Service) abandon(j *job) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx()), 10*time.Second)
	defer cancel()
	j.push.Status, j.push.Outcome = "cancelled", "cancelled"
	j.push.ErrorCode, j.push.ErrorMessage = CodeInterrupted, "The server stopped before this push started."
	_ = s.finishPush(bg, j.push)
	s.mu.Lock()
	delete(s.active, j.remoteID)
	s.mu.Unlock()
}

// Cancel stops the remote's running or queued push. It reports whether there was one.
func (s *Service) Cancel(remoteID string) bool {
	s.mu.Lock()
	j := s.active[remoteID]
	if j == nil {
		s.mu.Unlock()
		return false
	}
	j.cancelled = true
	if j.cancel != nil {
		j.cancel()
	}
	started := j.started
	s.mu.Unlock()
	if !started {
		s.abandonCancelled(j)
	}
	return true
}

// baseCtx is the service's context, or a background one before Start.
func (s *Service) baseCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}

// cancelAndWait cancels the remote's push and waits (briefly) for it to leave
// the active set, so that whatever follows (removing its repository, writing a
// new address) cannot race with it.
func (s *Service) cancelAndWait(remoteID string) {
	if !s.Cancel(remoteID) {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, still := s.active[remoteID]
		s.mu.Unlock()
		if !still {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// abandonCancelled finishes a queued job the person cancelled.
func (s *Service) abandonCancelled(j *job) {
	bg, cancel := context.WithTimeout(context.WithoutCancel(s.baseCtx()), 10*time.Second)
	defer cancel()
	j.push.Status, j.push.Outcome, j.push.ErrorMessage = "cancelled", "cancelled", "Cancelled before it started."
	_ = s.finishPush(bg, j.push)
	s.mu.Lock()
	delete(s.active, j.remoteID)
	s.mu.Unlock()
}

// run executes one job and settles its row, whatever happens.
func (s *Service) run(parent context.Context, j *job) {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	j.cancel = cancel
	cancelled := j.cancelled
	s.mu.Unlock()
	if cancelled {
		cancel()
	}
	defer cancel()
	bg := context.WithoutCancel(parent) // the row and the status are written even when the push is cancelled
	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("git: push panicked", "remote", j.remoteID, "panic", fmt.Sprint(rec))
			wctx, wc := context.WithTimeout(bg, 10*time.Second)
			defer wc()
			j.push.Status, j.push.Outcome, j.push.ErrorCode, j.push.ErrorMessage = "error", CodeInternal, CodeInternal, "The push failed unexpectedly. It is in the server log."
			_ = s.finishPush(wctx, j.push)
		}
		s.mu.Lock()
		delete(s.active, j.remoteID)
		again := j.again && parent.Err() == nil
		s.mu.Unlock()
		if again {
			wctx, wc := context.WithTimeout(bg, 10*time.Second)
			defer wc()
			if r, err := s.loadRemoteByID(wctx, j.remoteID); err == nil && r.Enabled && s.autoEligible(r) {
				s.enqueueAuto(parent, r, TriggerSave)
			}
		}
	}()
	s.execute(ctx, bg, j)
}

// WaitIdle blocks until nothing is queued or running and no debounce is
// pending, or ctx ends. For tests and for shutdown.
func (s *Service) WaitIdle(ctx context.Context) error {
	for {
		s.mu.Lock()
		idle := len(s.active) == 0 && len(s.queue) == 0
		s.mu.Unlock()
		if idle {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// backoffFor is the delay before the next automatic attempt after the nth
// consecutive failure: base, 2x, 4x, … up to the maximum.
func (s *Service) backoffFor(n int) time.Duration {
	d := s.o.BackoffBase
	for i := 1; i < n && d < s.o.BackoffMax; i++ {
		d *= 2
	}
	return min(d, s.o.BackoffMax)
}

// ---- housekeeping ----------------------------------------------------------------

// ProjectPurged is the purge hook: it stops anything running for the project
// and removes its local repositories and import clones. The database rows are
// already gone (they cascade).
func (s *Service) ProjectPurged(_ context.Context, projectID string) {
	if !db.ValidID(projectID) {
		return
	}
	s.mu.Lock()
	if d := s.deb[projectID]; d != nil && d.timer != nil {
		d.timer.Stop()
	}
	delete(s.deb, projectID)
	for _, j := range s.active {
		if j.projectID == projectID {
			j.cancelled = true
			if j.cancel != nil {
				j.cancel()
			}
		}
	}
	s.mu.Unlock()
	for _, dir := range []string{
		filepath.Join(s.o.DataDir, "git", projectID),
		filepath.Join(s.o.DataDir, "backups", "git", projectID),
		filepath.Join(s.o.DataDir, "imports", importCopyName(projectID)),
	} {
		if err := os.RemoveAll(dir); err != nil {
			s.log.Warn("git: could not remove a purged project's repository", "project", projectID, "err", cause(err))
		}
	}
}

// RemoteRemoved removes a deleted remote's local repository.
func (s *Service) removeLocal(projectID, remoteID string) {
	if !db.ValidID(projectID) || !db.ValidID(remoteID) {
		return
	}
	_ = os.RemoveAll(s.repoDir(projectID, remoteID))
}

func cause(err error) string {
	if err == nil {
		return ""
	}
	if ae := ops.AsError(err); ae.Code != ops.CodeInternal {
		return ae.Message
	}
	return err.Error()
}
