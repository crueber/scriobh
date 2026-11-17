package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// Service creates, lists and prunes the backups in data/backups. One backup runs
// at a time.
type Service struct {
	DB      *db.DB
	DataDir string
	Version string
	Log     *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu sync.Mutex
}

// idPattern is every file name the service lists, creates or serves: an
// optional prefix, a UTC timestamp, an optional collision counter. Nothing else
// in data/backups (the migrate/ and git/ folders, working files, anything a
// person put there) is listed or reachable by id.
var idPattern = regexp.MustCompile(`^(auto-|pre-restore-)?\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}(-\d+)?$`)

// Item is one backup in the listing.
type Item struct {
	ID        string `json:"id" desc:"The backup's id: its file name without .zip. Use it to download."`
	Kind      string `json:"kind" desc:"manual, scheduled or pre-restore." enum:"manual,scheduled,pre-restore"`
	CreatedAt string `json:"created_at"`
	Bytes     int64  `json:"bytes"`
}

func kindOf(id string) string {
	switch {
	case strings.HasPrefix(id, "auto-"):
		return "scheduled"
	case strings.HasPrefix(id, "pre-restore-"):
		return "pre-restore"
	}
	return "manual"
}

// ValidID reports whether id has the shape of a backup id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

func (s *Service) dir() string { return filepath.Join(s.DataDir, "backups") }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Create takes a backup. prefix is "" for a manual one and "auto-" for the
// scheduler's.
func (s *Service) Create(ctx context.Context, prefix string) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Create(ctx, s.DB.Path, s.DataDir, Options{Prefix: prefix, Version: s.Version, Now: s.now})
}

// List returns the backups, newest first.
func (s *Service) List() ([]Item, error) {
	entries, err := os.ReadDir(s.dir())
	if err != nil {
		if os.IsNotExist(err) {
			return []Item{}, nil
		}
		return nil, err
	}
	items := []Item{}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".zip")
		if !ok || !e.Type().IsRegular() || !ValidID(id) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, Item{ID: id, Kind: kindOf(id), CreatedAt: fi.ModTime().UTC().Format("2006-01-02T15:04:05.000Z"), Bytes: fi.Size()})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt != items[j].CreatedAt {
			return items[i].CreatedAt > items[j].CreatedAt
		}
		return items[i].ID > items[j].ID
	})
	return items, nil
}

// ErrNoBackup means no backup has that id.
var ErrNoBackup = errors.New("backup: no such backup")

// Path resolves an id from List to its file. It never accepts a path.
func (s *Service) Path(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("backup: %q is not a backup id", id)
	}
	p := filepath.Join(s.dir(), id+".zip")
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		return "", ErrNoBackup
	}
	return p, nil
}

// Prune removes scheduled backups beyond the newest keep. Manual and
// pre-restore backups are never removed. It returns the ids it removed.
func (s *Service) Prune(keep int) ([]string, error) {
	if keep < 1 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items, err := s.List()
	if err != nil {
		return nil, err
	}
	var sched []string
	for _, it := range items {
		if it.Kind == "scheduled" {
			sched = append(sched, it.ID)
		}
	}
	// The timestamp in the name orders them, whatever the file times say.
	sort.Sort(sort.Reverse(sort.StringSlice(sched)))
	var removed []string
	for i := keep; i < len(sched); i++ {
		if err := os.Remove(filepath.Join(s.dir(), sched[i]+".zip")); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		removed = append(removed, sched[i])
	}
	return removed, nil
}

// lastScheduled is when the newest scheduled backup was made (from its name),
// or the zero time.
func (s *Service) lastScheduled() time.Time {
	items, err := s.List()
	if err != nil {
		return time.Time{}
	}
	var best time.Time
	for _, it := range items {
		if it.Kind != "scheduled" {
			continue
		}
		stem := strings.TrimPrefix(it.ID, "auto-")
		if len(stem) > len("2006-01-02T15-04-05") {
			stem = stem[:len("2006-01-02T15-04-05")]
		}
		if t, err := time.Parse(stampLayout, stem); err == nil && t.After(best) {
			best = t
		}
	}
	return best
}
