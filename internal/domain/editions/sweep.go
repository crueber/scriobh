package editions

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// A source file is renamed into place inside the import's transaction, just before its row is inserted. A crash between
// the rename and the commit leaves a file with no row (and so no quota entry): an orphan. SweepOrphans removes them.
//
// It is careful about what it removes. Only a regular file (never a link) in <data>/files/projects/<project id>/editions/
// whose name is a SHA-256 and a known extension, inside a folder whose name is a valid project id, and only when no
// edition row of that project names those bytes and the file is older than MinOrphanAge, so the file of an import that is
// still running is never touched; and, under the lock an import holds while it places the file, the row is looked for
// again before the file goes. A pass looks at a bounded number of files and remembers where it stopped.

var orphanName = regexp.MustCompile(`^([0-9a-f]{64})\.(epub|docx|pdf|zip)$`)

const (
	// MinOrphanAge is how old a file with no row must be before it is removed.
	MinOrphanAge = time.Hour
	// SweepLook is how many edition files one pass looks at; the next pass goes on from there.
	SweepLook = 5000
	// SweepRemove is the most files one pass removes.
	SweepRemove = 500
)

var (
	sweepMu     sync.Mutex
	sweepCursor string // the project folder the last pass stopped before
)

// SweepOrphans removes the orphans it finds in one pass and returns how many. now is the clock (a test's).
func SweepOrphans(ctx context.Context, d *db.DB, dataDir string, now time.Time) (int, error) {
	if dataDir == "" {
		return 0, nil
	}
	sweepMu.Lock()
	defer sweepMu.Unlock()
	root := filepath.Join(dataDir, "files", "projects")
	ents, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() && db.ValidID(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	start := sort.SearchStrings(names, sweepCursor)
	looked, removed := 0, 0
	stoppedAt := ""
	for i := 0; i < len(names); i++ {
		project := names[(start+i)%len(names)]
		if looked >= SweepLook || removed >= SweepRemove {
			stoppedAt = project
			break
		}
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		editionsDir := filepath.Join(root, project, "editions")
		if li, err := os.Lstat(editionsDir); err != nil || !li.IsDir() { // a link to a folder is not the store's folder
			continue
		}
		files, err := os.ReadDir(editionsDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if looked++; looked > SweepLook || removed >= SweepRemove {
				break
			}
			m := orphanName.FindStringSubmatch(f.Name())
			if m == nil || !f.Type().IsRegular() { // a link, a folder, anything else is not ours to remove
				continue
			}
			info, err := f.Info()
			if err != nil || now.Sub(info.ModTime()) < MinOrphanAge {
				continue
			}
			ok, err := removeIfOrphan(ctx, d, filepath.Join(root, project, "editions", f.Name()), project, m[1])
			if err != nil {
				return removed, err
			}
			if ok {
				removed++
			}
		}
	}
	sweepCursor = stoppedAt
	return removed, nil
}

// removeIfOrphan removes the file when no edition of the project names its bytes, with the import lock held.
func removeIfOrphan(ctx context.Context, d *db.DB, path, project, sha string) (bool, error) {
	mu := lockFor(project, sha)
	mu.Lock()
	defer mu.Unlock()
	var n int
	if err := d.Read().QueryRowContext(ctx, `SELECT count(*) FROM editions WHERE project_id = ? AND sha256 = ?`, project, sha).Scan(&n); err != nil {
		return false, err // fail closed: a file whose row cannot be looked for stays
	}
	if n > 0 {
		return false, nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, nil
	}
	return true, nil
}
