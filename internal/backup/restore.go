package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// Restore limits.
const (
	// DefaultMaxBytes caps the bytes written when restoring, counted as they
	// are read, never from a header.
	DefaultMaxBytes = 64 << 30
	// MaxEntries caps the number of files in an archive.
	MaxEntries = 2_000_000
	// maxManifestBytes caps manifest.json itself.
	maxManifestBytes = 256 << 20
)

// PostRestore steps run after a successful restore, with the restored,
// migrated database open and the secret key of this installation (nil when
// there is none yet: a restore never creates one). The Git push package adds
// the step that clears remote credentials the key cannot read. A step returns
// a message for the person running the restore.
var PostRestore []func(ctx context.Context, d *db.DB, secretKey []byte) (message string, err error)

// PostInstall hooks run right after the restored files are in place and before anything opens the database. They
// are for what must hold even if the later steps cannot run: a hook writes a marker that the server honours at its
// next start (mail uses one to pause itself), so a restore that stops half way, or a step that fails, never leaves
// a copy that sends. A hook that fails fails the restore.
var PostInstall []func(dataDir string) error

// RestoreOptions control Restore.
type RestoreOptions struct {
	// Force allows restoring into a data directory that already holds data,
	// after taking a pre-restore backup of it.
	Force bool
	// MaxBytes overrides DefaultMaxBytes.
	MaxBytes int64
	// Version is recorded in the pre-restore backup's manifest.
	Version string
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// SecretKey is the installation's key when it comes from the environment
	// (SCRIOBH_SECRET_KEY). When empty, data/secret.key is read if it exists.
	SecretKey []byte
}

// RestoreResult reports a restore.
type RestoreResult struct {
	Manifest Manifest
	Counts   Counts
	// PreRestore is the backup taken of what was replaced; empty if there was
	// nothing to back up.
	PreRestore string
	// KeptAside is where the replaced files went when there was no database to
	// back up; empty otherwise.
	KeptAside string
	// Migrated lists migrations applied while restoring an older backup.
	Migrated []string
	// SecretKeyPresent reports whether the data directory has a secret.key.
	SecretKeyPresent bool
	// PreRestoreError says why the current data could not be archived first. It
	// is set when the current database is damaged or unreadable: the restore
	// goes on, and what was here is kept as it was in KeptAside.
	PreRestoreError string
	// Notes are messages from the post-restore steps.
	Notes []string
}

// items are the top-level names a restore can replace in the data directory.
var items = []string{DatabaseName, DatabaseName + "-wal", DatabaseName + "-shm", "files", "mirror", "conflicts"}

// carried are the items this archive holds: the database always, and the trees
// the manifest says it includes. Only these are moved aside and replaced; a tree
// the archive left out (--no-files) stays exactly as it is.
func carried(m Manifest) []string {
	out := []string{DatabaseName, DatabaseName + "-wal", DatabaseName + "-shm"}
	if m.Includes.Files {
		out = append(out, "files")
	}
	if m.Includes.Mirror {
		out = append(out, "mirror")
	}
	if m.Includes.Conflicts {
		out = append(out, "conflicts")
	}
	return out
}

// Restore replaces the contents of the data directory at dataDir with a backup.
//
// Order, so that a failure at any step leaves the live directory as it was:
// take the data-directory lock (refused while a server holds it); read and
// validate the manifest; extract into a staging directory beside the data,
// verifying every size and hash as the bytes are read; migrate and check the
// staged database (a backup from a newer build is refused here); back up what
// is there, if anything; swap the pieces in, undoing the swap if one fails.
// secret.key is never touched.
func Restore(ctx context.Context, zipPath, dataDir string, o RestoreOptions) (*RestoreResult, error) {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	if o.MaxBytes <= 0 {
		o.MaxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	release, err := db.Lock(dataDir)
	if err != nil {
		return nil, fmt.Errorf("%w: stop the server that is using this data directory before restoring", err)
	}
	defer release()

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("%s is not a readable backup: %w", zipPath, err)
	}
	defer zr.Close()
	m, byName, err := readManifest(zr)
	if err != nil {
		return nil, err
	}

	hasData := false
	for _, n := range items {
		if exists(filepath.Join(dataDir, n)) {
			hasData = true
			break
		}
	}
	if hasData && !o.Force {
		return nil, errors.New("the data directory already holds data. Restore into an empty directory, or pass --force: a backup of what is there is taken first")
	}

	stamp := now().UTC().Format(stampLayout)
	staging, err := os.MkdirTemp(dataDir, ".restore-"+stamp+"-*")
	if err != nil {
		return nil, err
	}
	cleanStaging := true
	defer func() {
		if cleanStaging {
			os.RemoveAll(staging)
		}
	}()
	if err := extract(ctx, m, byName, staging, o.MaxBytes); err != nil {
		return nil, err
	}

	// The staged database: refuse one this build cannot run, bring an older one
	// up to date, and check it is sound.
	counts, migrated, err := checkStaged(ctx, filepath.Join(staging, DatabaseName))
	if err != nil {
		return nil, err
	}
	if counts != m.Counts {
		return nil, fmt.Errorf("the restored database holds %d projects and %d documents; the manifest says %d and %d. The backup is damaged", counts.Projects, counts.Documents, m.Counts.Projects, m.Counts.Documents)
	}

	res := &RestoreResult{Manifest: m, Counts: counts, Migrated: migrated}

	// Pause everything that sends, in the staged database, before a single file reaches the data directory. The
	// files then arrive already paused: there is no moment, however the process ends afterwards (Ctrl-C, a kill, a
	// power cut), at which the installed copy has a remote, a trigger, a webhook, the Pandoc bridge, mail or an index
	// switched on. A cancelled context does not reach this phase: it is short, and skipping a pause is the one thing a
	// restore must not do. If it cannot be made in full the restore goes on (the data is wanted) with a marker that
	// every start honours, and says so.
	key := o.SecretKey
	if len(key) == 0 {
		key = readKeyFile(dataDir)
	}
	detail, _ := json.Marshal(map[string]any{"backup_created_at": m.CreatedAt, "projects": counts.Projects, "documents": counts.Documents, "forced": hasData})
	stagedErrs := pauseStaged(context.WithoutCancel(ctx), filepath.Join(staging, DatabaseName), key, string(detail), res)

	var aside string
	replaced := carried(m)
	if hasData {
		if exists(filepath.Join(dataDir, DatabaseName)) {
			r, err := Create(ctx, filepath.Join(dataDir, DatabaseName), dataDir, Options{Prefix: "pre-restore-", Version: o.Version, Now: now})
			if err != nil {
				// A damaged database is the usual reason to restore, and it is also why
				// it cannot be archived. The files are not lost by that: they are moved
				// aside whole (below) and kept, and the result says so.
				res.PreRestoreError = err.Error()
			} else {
				res.PreRestore = r.Path
			}
		}
		var err error
		if aside, err = os.MkdirTemp(dataDir, ".restore-old-"+stamp+"-*"); err != nil {
			return nil, err
		}
		for _, n := range replaced {
			if exists(filepath.Join(dataDir, n)) {
				if err := os.Rename(filepath.Join(dataDir, n), filepath.Join(aside, n)); err != nil {
					undo(dataDir, aside, replaced, nil)
					return nil, fmt.Errorf("moving %s aside: %w", n, err)
				}
			}
		}
	}
	// The generic marker goes in first when the staged pauses were not all made: whatever happens after that, the next
	// start (or `scriobh restore --finish`) pauses everything or refuses to run.
	wroteMarker := false
	if len(stagedErrs) > 0 {
		if err := WriteMarker(dataDir, "the pauses in the staged database were not all made"); err != nil {
			undo(dataDir, aside, replaced, nil)
			return nil, fmt.Errorf("the restore could not write its safety marker, and its pauses were not all made, so nothing was installed: %w", errors.Join(append(stagedErrs, err)...))
		}
		wroteMarker = true
	}
	var installed []string
	for _, n := range replaced {
		if !exists(filepath.Join(staging, n)) {
			continue
		}
		if err := os.Rename(filepath.Join(staging, n), filepath.Join(dataDir, n)); err != nil {
			undo(dataDir, aside, replaced, installed)
			if wroteMarker {
				_ = os.Remove(filepath.Join(dataDir, MarkerName))
			}
			return nil, fmt.Errorf("installing %s: %w", n, err)
		}
		installed = append(installed, n)
	}
	if hasData {
		if res.PreRestore != "" {
			_ = os.RemoveAll(aside) // the pre-restore backup holds everything that was here
		} else {
			res.KeptAside = aside
		}
	}
	_, err = os.Stat(filepath.Join(dataDir, "secret.key"))
	res.SecretKeyPresent = err == nil

	// Markers for what must pause, before anything can fail. A hook that cannot write its marker does not stop the
	// steps: they still run (they do the pausing themselves), and the failure is reported with theirs.
	// A hook that panics is a failed hook, not a restore that stops: the ones after it still run.
	var hookErrs []error
	for i, h := range PostInstall {
		if err := runInstallHook(h, dataDir); err != nil {
			hookErrs = append(hookErrs, fmt.Errorf("a safety marker could not be written (install hook %d): %w", i+1, err))
		}
	}

	// The same steps again on the installed database: they find nothing left to pause when the staged run made them,
	// remove the markers mail and the semantic index wrote, and are the second try for a step that failed in the
	// staging. An interrupted restore (a cancelled context) does not run them: what matters is already in the files,
	// and the marker makes the next start finish the rest.
	if ctx.Err() != nil {
		_ = WriteMarker(dataDir, "the restore was interrupted after the files were installed")
		return res, fmt.Errorf("the restore was interrupted after the files were installed (%v). Everything that sends was paused before the install%s. A marker (%s) in the data directory makes the next start finish the rest; `scriobh restore --finish` does it now%s", ctx.Err(), unpausedWords(stagedErrs), MarkerName, stagedDetail(stagedErrs))
	}
	// The database is opened twice if the first try fails (a busy file, a moment's trouble on a network disk).
	d, err := openRestored(filepath.Join(dataDir, DatabaseName))
	if err != nil {
		_ = WriteMarker(dataDir, "the restored database could not be reopened")
		return res, fmt.Errorf("restored, but the database could not be reopened to finish, so the follow-up pauses were not confirmed on the installed copy%s. A marker (%s) in the data directory makes the next start pause everything again or refuse to run. %s: %w", unpausedWords(stagedErrs), MarkerName, HandPauseText(), errors.Join(append(append(hookErrs, stagedErrs...), err)...))
	}
	defer d.Close()
	// Every step runs, whatever an earlier one did: a step that pauses what must not publish is not allowed to be
	// skipped because another step failed. The errors are reported together once the restore is complete.
	stepErrs := hookErrs
	nc := context.WithoutCancel(ctx)
	for i, step := range PostRestore {
		msg, err := runStep(nc, step, d, key)
		if err != nil {
			stepErrs = append(stepErrs, fmt.Errorf("restore step %d: %w", i+1, err))
		}
		res.addNote(msg)
	}
	if len(stepErrs) > 0 {
		_ = WriteMarker(dataDir, "a restore step failed")
		return res, fmt.Errorf("restored, but a follow-up step failed, so some senders may be NOT paused. A marker (%s) in the data directory makes every start retry the pauses and refuse to run until they are made; `scriobh restore --finish` retries them now. %s: %w", MarkerName, HandPauseText(), errors.Join(stepErrs...))
	}
	_ = os.Remove(filepath.Join(dataDir, MarkerName))
	return res, nil
}

func (r *RestoreResult) addNote(msg string) {
	if msg == "" {
		return
	}
	for _, n := range r.Notes {
		if n == msg {
			return
		}
	}
	r.Notes = append(r.Notes, msg)
}

// stagedDetail says which staged pauses failed and lists every sender as NOT paused, for the error of a restore that
// could not make them all.
func stagedDetail(stagedErrs []error) string {
	if len(stagedErrs) == 0 {
		return ""
	}
	return ". Some pauses could not be made before the install. " + HandPauseText() + ": " + errors.Join(stagedErrs...).Error()
}

func unpausedWords(stagedErrs []error) string {
	if len(stagedErrs) == 0 {
		return ""
	}
	return " in all but the steps that failed there"
}

// pauseStaged runs every post-restore step on the staged database and writes the audit line, then folds the log into
// the file. Notes go into res; the errors come back (and the restore goes on, with a marker, if there are any).
func pauseStaged(ctx context.Context, path string, key []byte, audit string, res *RestoreResult) []error {
	d, err := openRestored(path)
	if err != nil {
		return []error{fmt.Errorf("the staged database could not be opened to pause it: %w", err)}
	}
	var errs []error
	for i, step := range PostRestore {
		msg, err := runStep(ctx, step, d, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("staged step %d: %w", i+1, err))
		}
		res.addNote(msg)
	}
	_, _ = d.Writer().ExecContext(ctx, `INSERT INTO audit_log(id, at, action, target_type, target_id, detail_json) VALUES (?,?,?,?,?,?)`,
		db.NewID("aud"), db.Now(), "backup.restore", "instance", "", audit)
	if _, err := d.Writer().ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		errs = append(errs, err)
	}
	if err := d.Close(); err != nil {
		errs = append(errs, err)
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	return errs
}

// MarkerName is the file that says a restore did not finish its pauses. It is written by the restore itself, atomically,
// and removed only once every pause has been made; every start of the server looks for it (FinishPending), and so
// does `scriobh restore --finish`.
const MarkerName = "restore.pending"

// WriteMarker writes the marker atomically.
func WriteMarker(dataDir, why string) error {
	tmp := filepath.Join(dataDir, MarkerName+".tmp")
	body := "a restore was made here and its pauses are not confirmed: " + why + "\nrun `scriobh restore --finish`, or start the server, which tries them and refuses to run until they are made\n"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, MarkerName))
}

// Pause names one thing that sends and how to pause it by hand.
type Pause struct {
	Name string
	SQL  string // run with sqlite3 against scriobh.db with the server stopped
}

// Pauses lists every sender, each registered by its own package. A restore that cannot say which of them were paused
// lists all of them under "NOT paused" with the statement that pauses each by hand.
var Pauses []Pause

// HandPauseText lists every registered sender and its statement.
func HandPauseText() string {
	var b strings.Builder
	b.WriteString("Treat each of these as NOT paused, and pause it by hand with sqlite3 against scriobh.db while the server is stopped:")
	for _, p := range Pauses {
		fmt.Fprintf(&b, "\n  %s: NOT paused; %s", p.Name, p.SQL)
	}
	return b.String()
}

// FinishPending is the second line of defence: when a restore left its marker, run every pause on the database now.
// It returns whether a marker was there, the notes of the steps and, when any step failed, an error that names what is
// NOT paused and how to pause it. The marker is removed only when every step succeeded. A server calls it before it
// starts anything and refuses to run when it fails.
//
// The steps are run with no secret key. The pauses do not need one; the one step that uses a key (the Git remotes'
// credential check, which clears credentials the key cannot read) would, run at a start with a new or another key, delete
// sealed credentials that the original key still opens. A restore itself runs it with the key it was given.
func FinishPending(ctx context.Context, d *db.DB, dataDir string) (pending bool, notes []string, err error) {
	if !exists(filepath.Join(dataDir, MarkerName)) {
		return false, nil, nil
	}
	nc := context.WithoutCancel(ctx)
	res := &RestoreResult{}
	var errs []error
	for i, step := range PostRestore {
		msg, e := runStep(nc, step, d, nil)
		if e != nil {
			errs = append(errs, fmt.Errorf("restore step %d: %w", i+1, e))
		}
		res.addNote(msg)
	}
	if len(errs) > 0 {
		return true, res.Notes, fmt.Errorf("a restore left its marker (%s) and its pauses could not all be made: %w\n%s", MarkerName, errors.Join(errs...), HandPauseText())
	}
	if e := os.Remove(filepath.Join(dataDir, MarkerName)); e != nil {
		return true, res.Notes, e
	}
	return true, res.Notes, nil
}

// openRestored opens the restored database, once more after a short wait when the first try fails. openDB is a
// seam for a test.
var openDB = db.Open

func openRestored(path string) (*db.DB, error) {
	d, err := openDB(path)
	if err == nil {
		return d, nil
	}
	time.Sleep(250 * time.Millisecond)
	d, err2 := openDB(path)
	if err2 != nil {
		return nil, fmt.Errorf("%w (and again after a short wait: %v)", err, err2)
	}
	return d, nil
}

// runInstallHook and runStep turn a panic into an error, so that one broken hook cannot skip the pauses after it.
func runInstallHook(h func(string) error, dataDir string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("it panicked: %v", r)
		}
	}()
	return h(dataDir)
}

func runStep(ctx context.Context, step func(context.Context, *db.DB, []byte) (string, error), d *db.DB, key []byte) (msg string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("it panicked, so what it pauses may NOT be paused: %v", r)
		}
	}()
	return step(ctx, d, key)
}

// readKeyFile reads data/secret.key the way config.EnsureSecretKey does,
// without ever creating it. A missing or short file gives nil.
func readKeyFile(dataDir string) []byte {
	b, err := os.ReadFile(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return nil
	}
	if k := strings.TrimSpace(string(b)); len(k) >= 32 {
		return []byte(k)
	}
	return nil
}

// undo puts the moved-aside pieces back and removes what was installed.
func undo(dataDir, aside string, replaced, installed []string) {
	for _, n := range installed {
		_ = os.RemoveAll(filepath.Join(dataDir, n))
	}
	if aside == "" {
		return
	}
	for _, n := range replaced {
		if exists(filepath.Join(aside, n)) {
			_ = os.Rename(filepath.Join(aside, n), filepath.Join(dataDir, n))
		}
	}
	_ = os.Remove(aside)
}

// readManifest reads and validates manifest.json and checks that the zip and
// the manifest list exactly the same files.
func readManifest(zr *zip.ReadCloser) (Manifest, map[string]*zip.File, error) {
	var m Manifest
	var mf *zip.File
	byName := map[string]*zip.File{}
	if len(zr.File) > MaxEntries {
		return m, nil, fmt.Errorf("the archive has %d entries; the limit is %d", len(zr.File), MaxEntries)
	}
	for _, f := range zr.File {
		if f.Name == ManifestName {
			if mf != nil {
				return m, nil, errors.New("the archive has two manifests")
			}
			mf = f
			continue
		}
		if strings.HasSuffix(f.Name, "/") {
			// A directory entry: ignored (directories are made as needed), but
			// only if it names a place a backup may hold.
			if t := strings.TrimSuffix(f.Name, "/"); !validArchivePath(t) && !isRoot(t) {
				return m, nil, fmt.Errorf("the archive holds %q, which a Scríobh backup never does; it is refused", f.Name)
			}
			continue
		}
		if !validArchivePath(f.Name) {
			return m, nil, fmt.Errorf("the archive holds %q, which a Scríobh backup never does; it is refused", f.Name)
		}
		if !f.Mode().IsRegular() {
			return m, nil, fmt.Errorf("the archive holds %q, which is not a regular file (a link or device); it is refused", f.Name)
		}
		if byName[f.Name] != nil {
			return m, nil, fmt.Errorf("the archive lists %q twice", f.Name)
		}
		byName[f.Name] = f
	}
	if mf == nil {
		return m, nil, errors.New("the archive has no manifest.json; it is not a Scríobh backup")
	}
	if mf.UncompressedSize64 > maxManifestBytes {
		return m, nil, errors.New("the manifest is implausibly large")
	}
	rc, err := mf.Open()
	if err != nil {
		return m, nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxManifestBytes+1))
	if err != nil {
		return m, nil, err
	}
	if len(b) > maxManifestBytes {
		return m, nil, errors.New("the manifest is implausibly large")
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, nil, fmt.Errorf("the manifest is not valid: %w", err)
	}
	if m.Format != FormatName || m.Version != ManifestVersion {
		return m, nil, fmt.Errorf("this is not a version %d Scríobh backup (format %q, version %d)", ManifestVersion, m.Format, m.Version)
	}
	listed := map[string]bool{}
	for _, e := range m.Entries {
		if !validArchivePath(e.Path) || e.Size < 0 || len(e.SHA256) != 64 {
			return m, nil, fmt.Errorf("the manifest has a bad entry %q", e.Path)
		}
		if listed[e.Path] {
			return m, nil, fmt.Errorf("the manifest lists %q twice", e.Path)
		}
		listed[e.Path] = true
		if byName[e.Path] == nil {
			return m, nil, fmt.Errorf("the manifest lists %q but the archive does not hold it", e.Path)
		}
	}
	var extra []string
	for n := range byName {
		if !listed[n] {
			extra = append(extra, n)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return m, nil, fmt.Errorf("the archive holds files the manifest does not list (first: %q); it was changed after it was made and is refused", extra[0])
	}
	if !listed[DatabaseName] {
		return m, nil, errors.New("the backup holds no database")
	}
	return m, byName, nil
}

// extract writes every entry under staging, checking size and hash as the
// bytes are read. The size limit is on bytes actually read.
func extract(ctx context.Context, m Manifest, byName map[string]*zip.File, staging string, maxBytes int64) error {
	// Refuse up front when the manifest itself declares more than the cap, so a
	// crafted size can never be written first and checked after.
	var declared int64
	for _, e := range m.Entries {
		declared += e.Size
		if declared > maxBytes || declared < 0 {
			return fmt.Errorf("the manifest lists more than the %d GiB limit; refused", maxBytes>>30)
		}
	}
	var total int64
	for _, e := range m.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		zf := byName[e.Path]
		dest := filepath.Join(staging, filepath.FromSlash(e.Path))
		if !strings.HasPrefix(dest, staging+string(filepath.Separator)) {
			return fmt.Errorf("%q would be written outside the data directory; refused", e.Path)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			rc.Close()
			return err
		}
		h := sha256.New()
		// Never read past the manifest's size, nor past what is left of the cap.
		limit := e.Size
		if left := maxBytes - total; left < limit {
			limit = left
		}
		n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(rc, limit+1))
		rc.Close()
		cerr := out.Close()
		if err != nil {
			return fmt.Errorf("reading %s: %w", e.Path, err)
		}
		if cerr != nil {
			return cerr
		}
		total += n
		if total > maxBytes {
			return fmt.Errorf("the archive expands past the %d GiB limit; refused", maxBytes>>30)
		}
		if n != e.Size {
			return fmt.Errorf("%s is %d bytes (or more); the manifest says %d. The archive is damaged", e.Path, n, e.Size)
		}
		if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
			return fmt.Errorf("%s does not match its recorded hash. The archive is damaged or was changed after it was made", e.Path)
		}
	}
	return nil
}

// checkStaged opens the staged database, applies pending migrations (refusing a
// database from a newer build or one whose applied migrations were edited),
// checks integrity, and returns the row counts.
func checkStaged(ctx context.Context, path string) (Counts, []string, error) {
	var c Counts
	d, err := db.Open(path)
	if err != nil {
		return c, nil, fmt.Errorf("the backup's database cannot be opened: %w", err)
	}
	// Count before migrating: a later data migration must not make a good
	// archive look damaged.
	if err := d.Read().QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE deleted_at IS NULL`).Scan(&c.Projects); err != nil {
		d.Close()
		return c, nil, fmt.Errorf("the backup's database is not a Scríobh database: %w", err)
	}
	if err := d.Read().QueryRowContext(ctx, `SELECT count(*) FROM documents WHERE deleted_at IS NULL`).Scan(&c.Documents); err != nil {
		d.Close()
		return c, nil, err
	}
	res, err := d.Migrate(ctx, "")
	if err != nil {
		d.Close()
		return c, nil, fmt.Errorf("the backup's database cannot be used by this build: %w", err)
	}
	var check string
	if err := d.Read().QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		d.Close()
		return c, nil, fmt.Errorf("the backup's database failed its integrity check (%v %s)", err, check)
	}
	// Fold the log into the file so the staged database is one file.
	if _, err := d.Writer().ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		d.Close()
		return c, nil, err
	}
	if err := d.Close(); err != nil {
		return c, nil, err
	}
	if fi, err := os.Stat(path + "-wal"); err == nil && fi.Size() > 0 {
		return c, nil, errors.New("the restored database still has an unmerged log; it was not installed")
	}
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	return c, res.Applied, nil
}
