package mail

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/secrets"
)

// ServiceName is the key the server registers the Service under in ops.Env.
const ServiceName = "mail.service"

// Kinds of message in the outbox.
const (
	KindKindle = "kindle"
	KindDigest = "digest"
	// KindProgress is the weekly or monthly writing summary (F-509).
	KindProgress = "progress"
)

// Edition is a built file.
type Edition struct {
	Filename    string
	ContentType string
	Body        []byte
	SHA256      string
	InputHash   string
	Documents   int
}

// EditionRequest says what to build: the project, the preset and the options that override it. The options are
// chosen here, never by the caller of kindle.send.
type EditionRequest struct {
	ProjectID string
	Preset    string
	Options   publish.Options
}

// Compiler builds an edition as the principal. The server supplies one that calls export.compile, so the
// authorization, the limits and the export record are those of an export made by hand.
type Compiler func(ctx context.Context, p ops.Principal, req EditionRequest) (*Edition, error)

// Limits are the bounds of the outbox and the rates. Zero fields take the defaults.
type Limits struct {
	// PerPersonQueue and TotalQueue cap the messages waiting (pending or being sent); TotalQueueBytes caps the
	// attachments waiting.
	PerPersonQueue, TotalQueue int
	TotalQueueBytes            int64
	// KindlePerHour and DigestPerHour are how many messages of a kind one person may have made in an hour;
	// GlobalPerHour is the same for the instance.
	KindlePerHour, DigestPerHour, GlobalPerHour int
	// RatePerMin is how fast the dispatcher sends, whatever is waiting.
	RatePerMin int
	// MaxAttempts is how many times a message is tried before it is dead.
	MaxAttempts int
	// KeepDays is how long a finished row is kept (its text is gone at once).
	KeepDays int
	// KindleMaxAge and DigestMaxAge are how long a message may wait before it is too old to send.
	KindleMaxAge, DigestMaxAge time.Duration
	// PreviewTTL is how long a Kindle preview's token lasts.
	PreviewTTL time.Duration
	// ConfigHold is how long the dispatcher stops after a failure that is about the relay's settings.
	ConfigHold time.Duration
}

// DefaultLimits are the settings when the server gives none.
var DefaultLimits = Limits{
	PerPersonQueue: 10, TotalQueue: 500, TotalQueueBytes: 256 << 20,
	KindlePerHour: 6, DigestPerHour: 3, GlobalPerHour: 600, RatePerMin: 30,
	MaxAttempts: 6, KeepDays: 14, KindleMaxAge: 24 * time.Hour, DigestMaxAge: 6 * time.Hour,
	PreviewTTL: 15 * time.Minute, ConfigHold: 10 * time.Minute,
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits
	pi := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	pi(&l.PerPersonQueue, d.PerPersonQueue)
	pi(&l.TotalQueue, d.TotalQueue)
	pi(&l.KindlePerHour, d.KindlePerHour)
	pi(&l.DigestPerHour, d.DigestPerHour)
	pi(&l.GlobalPerHour, d.GlobalPerHour)
	pi(&l.RatePerMin, d.RatePerMin)
	pi(&l.MaxAttempts, d.MaxAttempts)
	pi(&l.KeepDays, d.KeepDays)
	if l.TotalQueueBytes <= 0 {
		l.TotalQueueBytes = d.TotalQueueBytes
	}
	pd := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	pd(&l.KindleMaxAge, d.KindleMaxAge)
	pd(&l.DigestMaxAge, d.DigestMaxAge)
	pd(&l.PreviewTTL, d.PreviewTTL)
	pd(&l.ConfigHold, d.ConfigHold)
	return l
}

// Options configure a Service.
type Options struct {
	DB      *db.DB
	Box     *secrets.Box
	Log     *slog.Logger
	DataDir string
	// BaseURL returns the app's own address, for the links in a digest. Empty, or an address that only works on
	// this machine, means the digest carries no link.
	BaseURL func() string
	Compile Compiler
	Client  *Client
	// VisibleProjects, when set, replaces the default answer of visibleProjectsFor (the live projects a person is
	// on): the server sets it to also leave out a project that is locked behind a passphrase.
	VisibleProjects func(ctx context.Context, userID string) (map[string]string, error)
	Limits          Limits
	// Now and Rand are the clock and the jitter source; tests inject them. Now defaults to db.NowTime.
	Now  func() time.Time
	Rand func() float64
	// AfterSend is a test seam: when it returns an error the result of the attempt is not recorded, as if the
	// process had died between the relay's answer and the write.
	AfterSend func(id string) error
}

// Service owns the outbox, its dispatcher and the digest scheduler.
type Service struct {
	o      Options
	lim    Limits
	client *Client
	rl     *rate.Limiter

	dmu     sync.Mutex // one dispatch pass at a time
	wake    chan struct{}
	started atomic.Bool
	latched atomic.Bool // a restore's marker is there and mail could not be paused
	wg      sync.WaitGroup

	mu        sync.Mutex
	holdUntil time.Time
	holdClass Class
	lastTest  time.Time
	warned    map[string]time.Time
}

// New returns a Service. It starts nothing: Start does.
func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = db.NowTime
	}
	if o.Rand == nil {
		o.Rand = randFloat
	}
	lim := o.Limits.withDefaults()
	cl := o.Client
	if cl == nil {
		cl = &Client{}
	}
	return &Service{o: o, lim: lim, client: cl, rl: rate.NewLimiter(rate.Limit(float64(lim.RatePerMin)/60), 5), wake: make(chan struct{}, 1), warned: map[string]time.Time{}}
}

func randFloat() float64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
}

// Limits returns the settings in force.
func (s *Service) Limits() Limits { return s.lim }

// Box is the sealing box for the relay's password.
func (s *Service) Box() *secrets.Box { return s.o.Box }

func (s *Service) now() time.Time { return s.o.Now().UTC() }

func (s *Service) stamp(t time.Time) string { return db.Format(t) }

// Wake asks the dispatcher to look at the outbox now.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// warnOnce logs a message at most once an hour per key.
func (s *Service) warnOnce(key, msg string, args ...any) {
	s.mu.Lock()
	last := s.warned[key]
	ok := s.now().Sub(last) > time.Hour
	if ok {
		s.warned[key] = s.now()
	}
	s.mu.Unlock()
	if ok {
		s.o.Log.Warn(msg, args...)
	}
}

// baseURL is the app's own address when it can go in a mail, "" otherwise.
func (s *Service) baseURL() string {
	if s.o.BaseURL == nil {
		return ""
	}
	return cleanBase(s.o.BaseURL())
}

func (s *Service) filesDir() string { return filepath.Join(s.o.DataDir, "mail", "outbox") }

func (s *Service) filePath(id string) string { return filepath.Join(s.filesDir(), id) }

func (s *Service) writeFile(id string, data []byte) error {
	if !db.ValidID(id) {
		return errors.New("bad id")
	}
	if err := os.MkdirAll(s.filesDir(), 0o700); err != nil {
		return err
	}
	tmp := s.filePath(id) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.filePath(id))
}

func (s *Service) removeFiles(ids []string) {
	if s == nil {
		return
	}
	for _, id := range ids {
		s.removeFile(id)
	}
}

func (s *Service) removeFile(id string) {
	if db.ValidID(id) {
		_ = os.Remove(s.filePath(id))
	}
}

// Start runs the dispatcher and the digest scheduler until ctx is cancelled. Call Wait afterwards. What a
// stopped process left is all in the database: a message that was being sent is either still to send (the relay
// had not been given the body) or given up on as delivery_unknown (it had).
func (s *Service) Start(ctx context.Context) {
	if !s.started.CompareAndSwap(false, true) {
		return
	}
	s.honourMarker(ctx)
	if err := s.recover(ctx); err != nil {
		s.o.Log.Warn("mail: recovering the outbox failed", "err", err)
	}
	s.checkPassword(ctx)
	s.wg.Add(2)
	go func() { defer s.wg.Done(); s.dispatchLoop(ctx) }()
	go func() { defer s.wg.Done(); s.digestLoop(ctx) }()
}

// Wait blocks until the loops have finished.
func (s *Service) Wait() { s.wg.Wait() }

// recover settles what a stopped process left claimed.
func (s *Service) recover(ctx context.Context) error {
	now := s.stamp(s.now())
	var dead []string
	err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM mail_outbox WHERE state = 'sending' AND data_started_at <> ''`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			dead = append(dead, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range dead {
			if err := finishTx(tx, id, "dead", ClassUnknown, now); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE mail_outbox SET state = 'pending', claimed_at = '' WHERE state = 'sending'`)
		return err
	})
	for _, id := range dead {
		s.removeFile(id)
	}
	return err
}

// checkPassword switches mail off, with a reason, when the stored password cannot be opened (the key changed).
func (s *Service) checkPassword(ctx context.Context) {
	cfg, err := loadConfig(ctx, s.o.DB.Read())
	if err != nil || cfg.PasswordSealed == "" {
		return
	}
	if s.o.Box.CanOpen(cfg.PasswordSealed, sealContext(cfg.Host, cfg.Port, cfg.Username)) {
		return
	}
	reason := "The relay's password cannot be read with this installation's secret key. Enter it again, then switch mail on."
	_ = s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE mail_config SET enabled = 0, disabled_reason = ?, updated_at = ? WHERE id = 1 AND enabled = 1`, reason, db.Now())
		return err
	})
}

func (s *Service) dispatchLoop(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	sweep := time.NewTicker(time.Hour)
	defer sweep.Stop()
	_ = s.Sweep(ctx)
	for {
		for s.DispatchOnce(ctx) > 0 && ctx.Err() == nil {
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-t.C:
		case <-sweep.C:
			if err := s.Sweep(ctx); err != nil {
				s.o.Log.Warn("mail: sweep failed", "err", err)
			}
		}
	}
}

// Sweep removes finished rows past their keep time, previews that expired, and files that no waiting row owns.
func (s *Service) Sweep(ctx context.Context) error {
	now := s.now()
	old := s.stamp(now.AddDate(0, 0, -s.lim.KeepDays))
	err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM mail_outbox WHERE state IN ('sent','dead') AND finished_at <> '' AND finished_at < ?`, old); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM kindle_previews WHERE expires_at < ?`, s.stamp(now))
		return err
	})
	if err != nil {
		return err
	}
	s.removeOrphanFiles(ctx)
	return nil
}

// removeOrphanFiles deletes attachment files that no pending or sending row owns and that are not brand new.
func (s *Service) removeOrphanFiles(ctx context.Context) { s.removeOrphans(ctx, 10*time.Minute) }

// removeOrphans is removeOrphanFiles with the age under which a file is left to the enqueue that wrote it.
func (s *Service) removeOrphans(ctx context.Context, grace time.Duration) {
	ents, err := os.ReadDir(s.filesDir())
	if err != nil || len(ents) == 0 {
		return
	}
	live := map[string]bool{}
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT id FROM mail_outbox WHERE state IN ('pending','sending') AND file_bytes > 0`)
	if err != nil {
		return
	}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			live[id] = true
		}
	}
	rows.Close()
	for _, e := range ents {
		if live[e.Name()] {
			continue
		}
		// A file is written just before its row; leave the new ones to the enqueue that is still running.
		if info, err := e.Info(); err == nil && grace > 0 && s.now().Sub(info.ModTime()) < grace {
			continue
		}
		_ = os.Remove(filepath.Join(s.filesDir(), e.Name()))
	}
}
