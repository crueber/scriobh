// Package testutil holds helpers shared by tests: a migrated temp database
// and a loopback configuration. It must not import the server (that would
// create an import cycle for server tests).
package testutil

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
)

// DB returns a migrated database in a temp directory, closed with the test.
//
// Applying the 85 migrations is a few dozen milliseconds, and about 1.7 s under the race
// detector (the pure-Go SQLite driver re-parses the schema after every DDL statement, and
// -race with checkptr slows that code about thirty times). With a hundred tests per package
// that was the largest cost of the race pass, so the migrated file is built once and each
// call copies it. The copy is the same file Migrate would have produced: the template is
// checkpointed and closed, and the copy is opened through db.Open like any other.
func DB(t testing.TB) *db.DB {
	t.Helper()
	return DBAt(t, filepath.Join(t.TempDir(), "scriobh.db"))
}

// DBAt is DB for a test that keeps its database at a path of its own (inside a data directory it
// builds). The file must not exist yet.
func DBAt(t testing.TB, path string) *db.DB {
	t.Helper()
	tmpl, err := migratedTemplate()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(tmpl, path); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	// Nothing is pending on a copy of the current schema; this proves it (and would apply a
	// migration the template somehow lacks rather than hand a test an old schema).
	if _, err := d.Migrate(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	return d
}

var (
	templateOnce sync.Once
	templateFile string
	templateErr  error
)

// migratedTemplate returns the path of a migrated, checkpointed database file. It is kept
// in a per-user directory of the system temp directory under a name that carries the hash of
// the migrations, so every test process (and every package of one `go test` run) shares it and
// a new migration makes a new file: the file is a function of its name. It is built in a private
// directory and renamed into place, so two processes racing to build it cannot leave a
// half-written file under the final name; an existing file is trusted only after it passes
// quick_check and holds every migration's row, once per process.
func migratedTemplate() (string, error) {
	templateOnce.Do(func() { templateFile, templateErr = buildTemplate() })
	return templateFile, templateErr
}

// templateDir returns the directory the templates live in: $TMPDIR/scriobh-testdb-<user>,
// created 0700 and checked to be a real directory (not a link another user planted), owned by
// this user, and closed to everyone else. Anything else gets a private directory of its own for
// this process, which only costs one build.
func templateDir() (string, error) {
	dir := filepath.Join(os.TempDir(), "scriobh-testdb-"+userKey())
	if err := os.MkdirAll(dir, 0o700); err == nil {
		if fi, err := os.Lstat(dir); err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 && privateToMe(fi) {
			return dir, nil
		}
	}
	return os.MkdirTemp("", "scriobh-testdb-private-")
}

// userKey names the current user in a directory name: the numeric id on Unix, the user name
// elsewhere (reduced to letters and digits).
func userKey() string {
	if uid := os.Getuid(); uid >= 0 {
		return strconv.Itoa(uid)
	}
	var b strings.Builder
	for _, r := range os.Getenv("USERNAME") {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "user"
	}
	return b.String()
}

// templateKey hashes the migrations into the file name.
func templateKey(ms []db.Migration) string {
	h := sha256.New()
	for _, m := range ms {
		h.Write([]byte(m.File + ":" + m.Checksum + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// validTemplate opens a cached template read-only (immutable: no -wal or -shm file is made next
// to it) and checks that it is whole: quick_check says ok and schema_migrations has one row per
// migration. A truncated or empty file, or one from a half-finished build, fails.
func validTemplate(path string, want int) error {
	d, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&immutable=1")
	if err != nil {
		return err
	}
	defer d.Close()
	var res string
	if err := d.QueryRow(`PRAGMA quick_check`).Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("quick_check: %s", res)
	}
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("schema_migrations has %d rows, want %d", n, want)
	}
	return nil
}

// tidyTemplates removes what earlier runs left: work directories of builds that died (older
// than an hour) and templates of other migration sets (older than a day). Best effort, errors
// ignored, and the current file is never touched.
func tidyTemplates(dir, current string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		age := time.Since(fi.ModTime())
		switch {
		case e.IsDir() && strings.HasPrefix(e.Name(), "build-") && age > time.Hour:
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		case !e.IsDir() && strings.HasPrefix(e.Name(), "migrated-") && strings.HasSuffix(e.Name(), ".db") && e.Name() != current && age > 24*time.Hour:
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func buildTemplate() (string, error) {
	ms, err := db.Migrations()
	if err != nil {
		return "", err
	}
	dir, err := templateDir()
	if err != nil {
		return "", err
	}
	name := "migrated-" + templateKey(ms) + ".db"
	final := filepath.Join(dir, name)
	tidyTemplates(dir, name)
	if _, err := os.Stat(final); err == nil {
		if validTemplate(final, len(ms)) == nil {
			return final, nil
		}
		// Present and broken (cut short, or from a build that died): build a new one over it.
	}
	work, err := os.MkdirTemp(dir, "build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	built := filepath.Join(work, "scriobh.db")
	d, err := db.Open(built)
	if err != nil {
		return "", err
	}
	if _, err := d.Migrate(context.Background(), ""); err != nil {
		d.Close()
		return "", err
	}
	if _, err := d.Writer().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		d.Close()
		return "", err
	}
	if err := d.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(built, final); err != nil {
		// Windows will not rename over a file; and another process may have built it first.
		_ = os.Remove(final)
		if err2 := os.Rename(built, final); err2 != nil {
			if validTemplate(final, len(ms)) == nil {
				return final, nil
			}
			return "", err2
		}
	}
	return final, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Config returns a loopback configuration using the development issuer and a
// temp data directory. env overrides individual variables.
func Config(t testing.TB, env map[string]string) *config.Config {
	t.Helper()
	base := map[string]string{
		"SCRIOBH_DATA_DIR":   t.TempDir(),
		"SCRIOBH_LISTEN":     "127.0.0.1:0",
		"SCRIOBH_BASE_URL":   "http://127.0.0.1:8080",
		"SCRIOBH_OIDC_STUB":  "1",
		"SCRIOBH_SECRET_KEY": "test-secret-key-test-secret-key-0123456789",
	}
	for k, v := range env {
		base[k] = v
	}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// User inserts a user row directly and returns its id. role is admin, writer
// or reader.
func User(t testing.TB, d *db.DB, role, email string) string {
	t.Helper()
	id := db.NewID("usr")
	now := db.Now()
	err := d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, role, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?)`, id, "test", "sub-"+id, email, email, role, now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Timing marks a test that asserts a wall-clock budget or measures throughput.
// It skips the test when SCRIOBH_TIMING is "off". The push gate sets that,
// because its runner is shared and slow and a budget there measures the
// machine; a laptop, and the nightly run on the runner with the machine to
// itself, leave it unset and run the test.
//
// Call it on the first line of a test that does nothing but measure, so the
// fixture is not built either. A test that asserts behaviour and time keeps its
// behaviour assertions outside, and puts the time assertion in a
// t.Run("time", ...) subtest whose first line is testutil.Timing(t). A test
// that only waits for something with a deadline is not a timing test and must
// not call it. The repository check in internal/repocheck fails when a test
// compares a measured duration with a budget and never calls Timing.
func Timing(t testing.TB) {
	t.Helper()
	if TimingOff() {
		t.Skip("timing test: skipped because SCRIOBH_TIMING=off (the nightly run and a plain `go test` run it)")
	}
}

// TimingOff reports whether SCRIOBH_TIMING is "off": the push gate. A test with
// a large fixture uses it to build a smaller one for its behaviour checks while
// the budget subtest, which Timing skips, is not being asserted anyway.
func TimingOff() bool { return os.Getenv("SCRIOBH_TIMING") == "off" }
