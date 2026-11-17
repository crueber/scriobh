// Package backup writes and restores a consistent archive of a data directory.
//
// A backup is a zip: scriobh.db (a VACUUM INTO copy, taken without stopping
// the server), the files/, mirror/ and conflicts/ trees, and manifest.json,
// which lists every other entry with its size and SHA-256. Restore checks the
// manifest in both directions before it replaces anything.
//
// What a backup never holds: secret.key, SCRIOBH_SECRET_KEY, the lock file,
// other backups, or the mirror's scratch directories. It contains the whole
// manuscript in plain text and is not encrypted (see docs/OPERATIONS.md).
package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// Archive constants.
const (
	ManifestName    = "manifest.json"
	FormatName      = "scriobh-backup"
	ManifestVersion = 1
	DatabaseName    = "scriobh.db"
)

// Roots are the trees a backup may hold, besides the database. Anything else
// in a zip is refused on restore.
var roots = []string{"files", "mirror", "conflicts"}

// isDerivedCovers says rel (a folder under files/) is a project's folder of derived cover pictures,
// projects/<id>/covers. The layout is in the database and its pictures are attachments, which are archived; what is
// made from them is rebuilt the first time it is asked for (docs/COVERS.md).
func isDerivedCovers(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	return len(parts) == 3 && parts[0] == "projects" && parts[2] == "covers"
}

// Entry is one file in the archive.
type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Counts describe the database snapshot: what restore verifies afterwards.
type Counts struct {
	Projects  int `json:"projects"`
	Documents int `json:"documents"`
}

// Manifest is manifest.json.
type Manifest struct {
	Format         string   `json:"format"`
	Version        int      `json:"version"`
	CreatedAt      string   `json:"created_at"`
	ScriobhVersion string   `json:"scriobh_version"`
	Trigger        string   `json:"trigger"`
	Migrations     []string `json:"migrations" desc:"Versions of the migrations applied in the snapshotted database."`
	Counts         Counts   `json:"counts"`
	Includes       struct {
		Files     bool `json:"files"`
		Mirror    bool `json:"mirror"`
		Conflicts bool `json:"conflicts"`
	} `json:"includes"`
	Entries []Entry `json:"entries"`
}

// Options control Create.
type Options struct {
	// Out is the file to write. Empty means data/backups/<Prefix><timestamp>.zip.
	Out string
	// Prefix starts the generated name: "" (manual), "auto-" (scheduled) or
	// "pre-restore-". It also sets the manifest's trigger.
	Prefix string
	// NoFiles, NoMirror and NoConflicts leave a tree out.
	NoFiles, NoMirror, NoConflicts bool
	// Version is recorded in the manifest.
	Version string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Result describes a backup that was written.
type Result struct {
	Path      string   `json:"-"`
	Name      string   `json:"name"`
	Bytes     int64    `json:"bytes"`
	SHA256    string   `json:"sha256"`
	CreatedAt string   `json:"created_at"`
	Manifest  Manifest `json:"-"`
	// Skipped counts entries left out because they were not regular files.
	Skipped int `json:"skipped"`
}

func trigger(prefix string) string {
	switch prefix {
	case "auto-":
		return "scheduled"
	case "pre-restore-":
		return "pre-restore"
	}
	return "manual"
}

// stampLayout names a backup; it sorts as time does.
const stampLayout = "2006-01-02T15-04-05"

// Create writes a backup of the data directory at dataDir. The database is read
// from dbPath with VACUUM INTO on its own connection, so it works beside a
// running server and offline alike, and saves are not held up. The archive is
// written under a temporary name and renamed when complete, so a partial file
// never has a backup's name.
func Create(ctx context.Context, dbPath, dataDir string, o Options) (*Result, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	t := now().UTC()
	out := o.Out
	if out == "" {
		dir := filepath.Join(dataDir, "backups")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		name := o.Prefix + t.Format(stampLayout)
		out = filepath.Join(dir, name+".zip")
		for n := 2; exists(out); n++ {
			out = filepath.Join(dir, fmt.Sprintf("%s-%d.zip", name, n))
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return nil, err
		}
		if exists(out) {
			return nil, fmt.Errorf("%s already exists; choose another name or remove it", out)
		}
	}

	if err := os.MkdirAll(filepath.Join(dataDir, "backups"), 0o700); err != nil {
		return nil, err
	}
	sweepLeftovers(filepath.Join(dataDir, "backups"), now())
	scratch, err := os.MkdirTemp(filepath.Join(dataDir, "backups"), ".work-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	snap := filepath.Join(scratch, DatabaseName)
	if err := db.Snapshot(ctx, dbPath, snap); err != nil {
		return nil, err
	}
	m := Manifest{Format: FormatName, Version: ManifestVersion, CreatedAt: t.Format("2006-01-02T15:04:05.000Z"),
		ScriobhVersion: o.Version, Trigger: trigger(o.Prefix), Entries: []Entry{}}
	if m.Counts, m.Migrations, err = inspect(ctx, snap); err != nil {
		return nil, err
	}
	m.Includes.Files, m.Includes.Mirror, m.Includes.Conflicts = !o.NoFiles, !o.NoMirror, !o.NoConflicts

	type src struct{ arc, path string }
	srcs := []src{{DatabaseName, snap}}
	skipped := 0
	skip := map[string]bool{o.Out: true}
	for _, r := range roots {
		if (r == "files" && o.NoFiles) || (r == "mirror" && o.NoMirror) || (r == "conflicts" && o.NoConflicts) {
			continue
		}
		base := filepath.Join(dataDir, r)
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil // gone while we walked: a save or a purge
				}
				return err
			}
			rel, _ := filepath.Rel(base, p)
			if d.IsDir() {
				if r == "mirror" && (rel == ".tmp" || rel == ".state") {
					return filepath.SkipDir
				}
				if r == "files" && isDerivedCovers(rel) {
					return filepath.SkipDir // made from the layout and the attachments, on demand: never the source of truth
				}
				return nil
			}
			if !d.Type().IsRegular() || skip[p] {
				skipped++ // a symlink, socket or device is never archived
				return nil
			}
			arc := r + "/" + filepath.ToSlash(rel)
			if !validArchivePath(arc) {
				// A name restore would refuse (a backslash or a colon, legal on
				// Linux): left out and counted, so a written archive always restores.
				skipped++
				return nil
			}
			srcs = append(srcs, src{arc, p})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", r, err)
		}
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].arc < srcs[j].arc })

	partial := out + ".partial"
	f, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Result, error) {
		f.Close()
		os.Remove(partial)
		return nil, err
	}
	zw := zip.NewWriter(f)
	for _, s := range srcs {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		e, err := addFile(zw, s.arc, s.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // vanished between the walk and the read
		}
		if err != nil {
			return fail(fmt.Errorf("archiving %s: %w", s.arc, err))
		}
		m.Entries = append(m.Entries, e)
	}
	mj, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fail(err)
	}
	w, err := zw.CreateHeader(&zip.FileHeader{Name: ManifestName, Method: zip.Deflate})
	if err != nil {
		return fail(err)
	}
	if _, err := w.Write(append(mj, '\n')); err != nil {
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(partial)
		return nil, err
	}
	if err := os.Rename(partial, out); err != nil {
		os.Remove(partial)
		return nil, err
	}
	sum, size, err := hashFile(out)
	if err != nil {
		return nil, err
	}
	return &Result{Path: out, Name: filepath.Base(out), Bytes: size, SHA256: sum, CreatedAt: m.CreatedAt, Manifest: m, Skipped: skipped}, nil
}

// addFile copies one file into the zip, hashing the bytes as they are written.
func addFile(zw *zip.Writer, arc, path string) (Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Entry{}, err
	}
	if !fi.Mode().IsRegular() {
		return Entry{}, fs.ErrNotExist
	}
	h := &zip.FileHeader{Name: arc, Method: zip.Deflate, Modified: fi.ModTime().UTC()}
	h.SetMode(0o600)
	w, err := zw.CreateHeader(h)
	if err != nil {
		return Entry{}, err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, sum), f)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Path: arc, Size: n, SHA256: hex.EncodeToString(sum.Sum(nil))}, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// inspect reads the snapshot's migration versions and row counts.
func inspect(ctx context.Context, path string) (Counts, []string, error) {
	var c Counts
	conn, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&immutable=1")
	if err != nil {
		return c, nil, err
	}
	defer conn.Close()
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE deleted_at IS NULL`).Scan(&c.Projects); err != nil {
		return c, nil, fmt.Errorf("the database is not a Scríobh database or is not migrated: %w", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM documents WHERE deleted_at IS NULL`).Scan(&c.Documents); err != nil {
		return c, nil, err
	}
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return c, nil, err
	}
	defer rows.Close()
	var vers []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return c, nil, err
		}
		vers = append(vers, v)
	}
	if vers == nil {
		vers = []string{}
	}
	return c, vers, rows.Err()
}

// validArchivePath reports whether name is a path a backup may hold: relative,
// slash-separated, clean, and inside scriobh.db or one of the three trees.
func validArchivePath(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	if name == DatabaseName {
		return true
	}
	top, rest, ok := strings.Cut(name, "/")
	if !ok || rest == "" {
		return false
	}
	for _, r := range roots {
		if top == r {
			return true
		}
	}
	return false
}

func isRoot(name string) bool {
	for _, r := range roots {
		if name == r {
			return true
		}
	}
	return false
}

// sweepLeftovers removes the work directories and partial archives that a
// backup killed part way leaves behind, once they are old enough that no
// backup in another process can still be using them.
func sweepLeftovers(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if !(strings.HasPrefix(n, ".work-") || strings.HasSuffix(n, ".zip.partial")) {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > time.Hour {
			_ = os.RemoveAll(filepath.Join(dir, n))
		}
	}
}
