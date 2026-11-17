package backup

import (
	"context"
	"log/slog"
	"time"
)

// Scheduler takes a backup every Interval and keeps the newest Keep of its own.
// It is a ticker goroutine started by serve: the first backup is due Interval
// after the newest scheduled one (so a restart does not take another), or
// StartDelay after start when there is none yet. Everything that touches the
// clock or the wait is injectable.
type Scheduler struct {
	Svc      *Service
	Interval time.Duration
	Keep     int
	Log      *slog.Logger
	// StartDelay is the least wait before the first backup after start, so a
	// boot is not slowed by one. Default one minute.
	StartDelay time.Duration
	// RetryAfter is the wait after a failure. Default one hour.
	RetryAfter time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// After returns a channel that receives after d; nil means time.After.
	After func(d time.Duration) <-chan time.Time
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) after(d time.Duration) <-chan time.Time {
	if s.After != nil {
		return s.After(d)
	}
	return time.After(d)
}

// Run blocks until ctx is cancelled. With a zero Interval it returns at once:
// scheduled backups are off.
func (s *Scheduler) Run(ctx context.Context) {
	if s.Interval <= 0 {
		return
	}
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	if s.StartDelay <= 0 {
		s.StartDelay = time.Minute
	}
	if s.RetryAfter <= 0 {
		s.RetryAfter = time.Hour
	}
	// The first wait comes from the newest scheduled backup on disk; after
	// that it is simply the interval, so a name that cannot be parsed can
	// never turn this into a loop.
	wait := time.Duration(0)
	if last := s.Svc.lastScheduled(); !last.IsZero() {
		wait = last.Add(s.Interval).Sub(s.now())
	}
	if wait < s.StartDelay {
		wait = s.StartDelay
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.after(wait):
		}
		r, err := s.Svc.Create(ctx, "auto-")
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Error("scheduled backup failed; trying again later", "err", err, "retry_in", s.RetryAfter.String())
			wait = s.RetryAfter
			continue
		}
		wait = s.Interval
		log.Info("scheduled backup written", "name", r.Name, "bytes", r.Bytes)
		if removed, err := s.Svc.Prune(s.Keep); err != nil {
			log.Warn("pruning old scheduled backups failed", "err", err)
		} else if len(removed) > 0 {
			log.Info("old scheduled backups removed", "removed", removed)
		}
	}
}
