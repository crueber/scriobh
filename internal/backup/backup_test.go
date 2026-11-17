package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

const secretKey = "this-is-the-secret-key-0123456789abcdef-never-in-a-backup"

// data is a populated data directory: a migrated database with two projects,
// attachments, a mirror, conflict copies, and a secret key.
type data struct {
	t     *testing.T
	dir   string
	d     *db.DB
	reg   *ops.Registry
	owner ops.Principal
}

func newData(t *testing.T) *data {
	t.Helper()
	dir := t.TempDir()
	d := testutil.DBAt(t, filepath.Join(dir, "scriobh.db")) // migrated once per process, copied (see testutil.DB)
	x := &data{t: t, dir: dir, d: d, reg: registry.Build(d, events.NewStore(d, nil), dir)}
	x.owner = ops.Principal{UserID: testutil.User(t, d, ops.RoleWriter, "o@t.local"), Role: ops.RoleWriter}
	return x
}

func (x *data) project(title string, chapters int) string {
	x.t.Helper()
	out, err := x.reg.Call(context.Background(), x.owner, "project.create", projects.CreateIn{Title: title})
	if err != nil {
		x.t.Fatal(err)
	}
	id := out.(projects.Item).Item.ID
	for i := 0; i < chapters; i++ {
		if _, err := x.reg.Call(context.Background(), x.owner, "doc.create", docs.CreateIn{ProjectID: id, Title: fmt.Sprintf("Chapter %d", i), Kind: "chapter", Content: fmt.Sprintf("text of %s %d\n", title, i)}); err != nil {
			x.t.Fatal(err)
		}
	}
	return id
}

func (x *data) file(rel, content string) {
	x.t.Helper()
	p := filepath.Join(x.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		x.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		x.t.Fatal(err)
	}
}

// populate adds the trees a backup holds, and the things it must leave out.
func (x *data) populate() {
	x.file("files/projects/p1/attachments/abc.png", "PNGDATA")
	x.file("mirror/the-book--abc123/project.json", "{}")
	x.file("mirror/the-book--abc123/manuscript/01-one.md", "one\n")
	x.file("mirror/.state/prj_x.json", "state, regenerable")
	x.file("mirror/.tmp/w-1", "scratch")
	x.file("conflicts/p1/doc-1.md", "my lost text")
	x.file("secret.key", secretKey+"\n")
	x.file("backups/older.zip", "someone else's archive")
	x.file("backups/migrate/pre-migrate-1.db", "old db")
	x.file("scriobh.lock", "")
	if err := os.Symlink("/etc/passwd", filepath.Join(x.dir, "files", "link")); err != nil {
		x.t.Log("symlinks unavailable:", err)
	}
}

func (x *data) create(o backup.Options) *backup.Result {
	x.t.Helper()
	r, err := backup.Create(context.Background(), filepath.Join(x.dir, "scriobh.db"), x.dir, o)
	if err != nil {
		x.t.Fatal(err)
	}
	return r
}

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var out []string
	for _, f := range zr.File {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}

func zipBytes(t *testing.T, path string) map[string][]byte {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

func has(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func count(t *testing.T, path, q string) int {
	t.Helper()
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.Read().QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ---- what a backup holds -------------------------------------------------------------

func TestBackupHoldsTheRightThingsAndNeverTheKey(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 3)
	x.project("Beta", 2)
	x.populate()
	r := x.create(backup.Options{Version: "test"})

	names := zipNames(t, r.Path)
	for _, want := range []string{"manifest.json", "scriobh.db", "files/projects/p1/attachments/abc.png",
		"mirror/the-book--abc123/project.json", "mirror/the-book--abc123/manuscript/01-one.md", "conflicts/p1/doc-1.md"} {
		if !has(names, want) {
			t.Errorf("the backup lacks %s; has %v", want, names)
		}
	}
	for _, n := range names {
		switch {
		case n == "secret.key", strings.HasPrefix(n, "backups/"), strings.HasPrefix(n, "mirror/.state"), strings.HasPrefix(n, "mirror/.tmp"),
			n == "scriobh.lock", n == "files/link", strings.HasSuffix(n, "-wal"):
			t.Errorf("the backup holds %s, which it must not", n)
		}
	}
	// The key bytes appear nowhere, not just under no entry's name.
	for n, b := range zipBytes(t, r.Path) {
		if bytes.Contains(b, []byte(secretKey)) {
			t.Errorf("the secret key's bytes are in %s", n)
		}
	}
	if fi, _ := os.Stat(r.Path); fi.Mode().Perm() != 0o600 {
		t.Errorf("the archive is %v, want 0600", fi.Mode().Perm())
	}
	if r.Manifest.Counts.Projects != 2 || r.Manifest.Counts.Documents != 2+3+2 /* seeded chapters + created */ {
		t.Errorf("counts %+v", r.Manifest.Counts)
	}
	if r.Manifest.Trigger != "manual" || r.Manifest.ScriobhVersion != "test" || len(r.Manifest.Migrations) == 0 {
		t.Errorf("manifest %+v", r.Manifest)
	}
	// No leftovers: no partial file, no work directory.
	left, _ := filepath.Glob(filepath.Join(x.dir, "backups", ".work-*"))
	partial, _ := filepath.Glob(filepath.Join(x.dir, "backups", "*.partial"))
	if len(left)+len(partial) != 0 {
		t.Errorf("leftovers: %v %v", left, partial)
	}

	// Trees can be left out.
	r2 := x.create(backup.Options{NoMirror: true, NoConflicts: true, NoFiles: true})
	for _, n := range zipNames(t, r2.Path) {
		if strings.HasPrefix(n, "mirror/") || strings.HasPrefix(n, "conflicts/") || strings.HasPrefix(n, "files/") {
			t.Errorf("--no-* flags ignored: %s", n)
		}
	}
	// Two backups in one second get two names.
	if r.Path == r2.Path {
		t.Error("a backup overwrote another")
	}
}

// ---- the round trip -----------------------------------------------------------------------

func TestRoundTrip(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 3)
	x.project("Beta", 2)
	x.populate()
	r := x.create(backup.Options{})
	wantDocs := count(t, x.d.Path, `SELECT count(*) FROM documents`)

	// Into an empty directory.
	dst := t.TempDir()
	res, err := backup.Restore(context.Background(), r.Path, dst, backup.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts.Projects != 2 || res.PreRestore != "" || res.SecretKeyPresent {
		t.Errorf("result %+v", res)
	}
	if got := count(t, filepath.Join(dst, "scriobh.db"), `SELECT count(*) FROM documents`); got != wantDocs {
		t.Errorf("documents %d, want %d", got, wantDocs)
	}
	for rel, want := range map[string]string{
		"files/projects/p1/attachments/abc.png":        "PNGDATA",
		"mirror/the-book--abc123/manuscript/01-one.md": "one\n",
		"conflicts/p1/doc-1.md":                        "my lost text",
	} {
		b, err := os.ReadFile(filepath.Join(dst, filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Errorf("%s: %q %v", rel, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "secret.key")); err == nil {
		t.Error("restore created a secret.key")
	}
	if _, err := os.Stat(filepath.Join(dst, "mirror", ".state")); err == nil {
		t.Error("restore invented mirror state")
	}
	if fi, _ := os.Stat(filepath.Join(dst, "scriobh.db")); fi.Mode().Perm() != 0o600 {
		t.Errorf("restored database is %v", fi.Mode().Perm())
	}
	// No staging leftovers, no log files next to the database.
	for _, g := range []string{".restore-*", ".restore-old-*", "scriobh.db-wal", "scriobh.db-shm"} {
		if m, _ := filepath.Glob(filepath.Join(dst, g)); len(m) != 0 {
			t.Errorf("leftover %v", m)
		}
	}

	// Into a directory that holds data: refused, then forced with a backup first.
	existing := newData(t)
	existing.project("Old", 1)
	existing.file("files/projects/old/attachments/x.png", "OLD")
	existing.file("secret.key", secretKey+"\n")
	existing.d.Close()
	if _, err := backup.Restore(context.Background(), r.Path, existing.dir, backup.RestoreOptions{}); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("restore over data without force: %v", err)
	}
	if got := count(t, filepath.Join(existing.dir, "scriobh.db"), `SELECT count(*) FROM projects`); got != 1 {
		t.Fatal("a refused restore changed the data")
	}
	res, err = backup.Restore(context.Background(), r.Path, existing.dir, backup.RestoreOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.PreRestore == "" || !strings.Contains(filepath.Base(res.PreRestore), "pre-restore-") {
		t.Fatalf("no pre-restore backup: %+v", res)
	}
	if !res.SecretKeyPresent {
		t.Error("the existing key should be reported as present")
	}
	if b, _ := os.ReadFile(filepath.Join(existing.dir, "secret.key")); !strings.Contains(string(b), secretKey) {
		t.Error("restore touched secret.key")
	}
	if got := count(t, filepath.Join(existing.dir, "scriobh.db"), `SELECT count(*) FROM projects`); got != 2 {
		t.Errorf("projects after forced restore: %d", got)
	}
	if _, err := os.Stat(filepath.Join(existing.dir, "files", "projects", "old")); err == nil {
		t.Error("the old files survived the restore")
	}
	// The pre-restore backup holds the old state and restores to it.
	back := t.TempDir()
	if _, err := backup.Restore(context.Background(), res.PreRestore, back, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := count(t, filepath.Join(back, "scriobh.db"), `SELECT count(*) FROM projects`); got != 1 {
		t.Errorf("the pre-restore backup holds %d projects, want the old 1", got)
	}
	if b, _ := os.ReadFile(filepath.Join(back, "files", "projects", "old", "attachments", "x.png")); string(b) != "OLD" {
		t.Error("the pre-restore backup lost the old files")
	}
	if m, _ := filepath.Glob(filepath.Join(existing.dir, ".restore-*")); len(m) != 0 {
		t.Errorf("leftover %v", m)
	}
	// The restore left an audit line in the restored database.
	if n := count(t, filepath.Join(existing.dir, "scriobh.db"), `SELECT count(*) FROM audit_log WHERE action = 'backup.restore'`); n != 1 {
		t.Errorf("audit lines for the restore: %d", n)
	}
}

// Restore refuses while the data-directory lock is held.
func TestRestoreRefusesWhileLocked(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	r := x.create(backup.Options{})
	dst := t.TempDir()
	release, err := db.Lock(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := backup.Restore(context.Background(), r.Path, dst, backup.RestoreOptions{}); err == nil || !strings.Contains(err.Error(), "another") {
		t.Fatalf("restore under a held lock: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "scriobh.db")); err == nil {
		t.Error("a refused restore wrote a database")
	}
}

// A save is never held up by a backup, and the backup is whole.
func TestBackupDoesNotStarveSaves(t *testing.T) {
	x := newData(t)
	pid := x.project("Big", 0)
	big := strings.Repeat("a long line of manuscript text, again and again. ", 4000) // ~200 KB
	var ids []docs.Doc
	for i := 0; i < 40; i++ {
		out, err := x.reg.Call(context.Background(), x.owner, "doc.create", docs.CreateIn{ProjectID: pid, Title: fmt.Sprintf("C%d", i), Kind: "chapter", Content: big})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, out.(docs.Item).Item)
	}
	stop := make(chan struct{})
	var saves, failures atomic.Int64
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			d := ids[i%len(ids)]
			c := fmt.Sprintf("%s save %d", big, i)
			out, err := x.reg.Call(context.Background(), x.owner, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, Content: &c})
			if err != nil {
				failures.Add(1)
				t.Errorf("a save failed during a backup: %v", err)
				return
			}
			ids[i%len(ids)] = out.(docs.Item).Item
			saves.Add(1)
		}
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	var r *backup.Result
	for i := 0; i < 3; i++ {
		r = x.create(backup.Options{})
	}
	took := time.Since(start)
	close(stop)
	wg.Wait()
	t.Logf("3 backups of a %d MB database in %v while %d saves went through", 8, took, saves.Load())
	if failures.Load() != 0 || saves.Load() == 0 {
		t.Fatalf("saves %d, failures %d", saves.Load(), failures.Load())
	}
	dst := t.TempDir()
	if _, err := backup.Restore(context.Background(), r.Path, dst, backup.RestoreOptions{}); err != nil {
		t.Fatalf("the backup taken beside live saves does not restore: %v", err)
	}
}

// ---- refusals ----------------------------------------------------------------------------------

type zent struct {
	name string
	data string
	mode fs.FileMode // zero means a regular file
}

func sum(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// craft writes a zip with the given entries and a manifest. tamper may edit
// the manifest before it is written.
func craft(t *testing.T, dbBytes []byte, counts backup.Counts, entries []zent, tamper func(*backup.Manifest)) string {
	t.Helper()
	m := backup.Manifest{Format: backup.FormatName, Version: backup.ManifestVersion, CreatedAt: "2026-11-12T10:00:00.000Z", Counts: counts, Entries: []backup.Entry{}}
	all := append([]zent{{name: "scriobh.db", data: string(dbBytes)}}, entries...)
	path := filepath.Join(t.TempDir(), "crafted.zip")
	f, _ := os.Create(path)
	zw := zip.NewWriter(f)
	for _, e := range all {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		w, _ := zw.CreateHeader(h)
		w.Write([]byte(e.data))
		m.Entries = append(m.Entries, backup.Entry{Path: e.name, Size: int64(len(e.data)), SHA256: sum(e.data)})
	}
	if tamper != nil {
		tamper(&m)
	}
	w, _ := zw.Create("manifest.json")
	mj, _ := json.Marshal(m)
	w.Write(mj)
	zw.Close()
	f.Close()
	return path
}

func TestRestoreRefusals(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	snap := filepath.Join(t.TempDir(), "s.db")
	if err := db.Snapshot(context.Background(), x.d.Path, snap); err != nil {
		t.Fatal(err)
	}
	dbBytes, _ := os.ReadFile(snap)
	counts := backup.Counts{Projects: 1, Documents: 2}
	good := func() []zent { return []zent{{name: "files/a.txt", data: "hello"}} }

	// Sanity: the crafted shape itself restores.
	ok := craft(t, dbBytes, counts, good(), nil)
	if _, err := backup.Restore(context.Background(), ok, t.TempDir(), backup.RestoreOptions{}); err != nil {
		t.Fatalf("the crafted baseline should restore: %v", err)
	}

	cases := []struct {
		name    string
		path    string
		opts    backup.RestoreOptions
		wantErr string
	}{
		{"zip slip", craft(t, dbBytes, counts, []zent{{name: "files/../../evil.txt", data: "x"}}, nil), backup.RestoreOptions{}, "never does"},
		{"zip slip at the root", craft(t, dbBytes, counts, []zent{{name: "../evil.txt", data: "x"}}, nil), backup.RestoreOptions{}, "never does"},
		{"absolute path", craft(t, dbBytes, counts, []zent{{name: "/etc/evil", data: "x"}}, nil), backup.RestoreOptions{}, "never does"},
		{"backslash path", craft(t, dbBytes, counts, []zent{{name: "files\\..\\evil", data: "x"}}, nil), backup.RestoreOptions{}, "never does"},
		{"a name outside the allowed trees", craft(t, dbBytes, counts, []zent{{name: "secret.key", data: "x"}}, nil), backup.RestoreOptions{}, "never does"},
		{"a symlink", craft(t, dbBytes, counts, []zent{{name: "files/link", data: "/etc/passwd", mode: fs.ModeSymlink | 0o777}}, nil), backup.RestoreOptions{}, "not a regular file"},
		{"a file the manifest does not list", craft(t, dbBytes, counts, good(), func(m *backup.Manifest) { m.Entries = m.Entries[:1] }), backup.RestoreOptions{}, "does not list"},
		{"a manifest entry with no file", craft(t, dbBytes, counts, good(), func(m *backup.Manifest) {
			m.Entries = append(m.Entries, backup.Entry{Path: "files/ghost.txt", Size: 1, SHA256: sum("x")})
		}), backup.RestoreOptions{}, "does not hold it"},
		{"a hash mismatch", craft(t, dbBytes, counts, good(), func(m *backup.Manifest) { m.Entries[1].SHA256 = sum("something else") }), backup.RestoreOptions{}, "recorded hash"},
		{"a size mismatch", craft(t, dbBytes, counts, good(), func(m *backup.Manifest) { m.Entries[1].Size = 2 }), backup.RestoreOptions{}, "manifest says"},
		{"an expansion past the cap", craft(t, dbBytes, counts, []zent{{name: "files/big.bin", data: strings.Repeat("0", 3<<20)}}, nil), backup.RestoreOptions{MaxBytes: 1 << 20}, "limit"},
		{"wrong counts", craft(t, dbBytes, backup.Counts{Projects: 9, Documents: 9}, good(), nil), backup.RestoreOptions{}, "damaged"},
		{"a wrong format", craft(t, dbBytes, counts, good(), func(m *backup.Manifest) { m.Format = "other" }), backup.RestoreOptions{}, "not a version"},
		{"no database", craft(t, nil, counts, good(), func(m *backup.Manifest) { m.Entries = m.Entries[1:] }), backup.RestoreOptions{}, ""},
	}
	// A database from a build with migrations this one does not know.
	newer := newData(t)
	newer.project("N", 1)
	if _, err := newer.d.Writer().Exec(`INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES ('9999','future','x','now')`); err != nil {
		t.Fatal(err)
	}
	nsnap := filepath.Join(t.TempDir(), "n.db")
	if err := db.Snapshot(context.Background(), newer.d.Path, nsnap); err != nil {
		t.Fatal(err)
	}
	nb, _ := os.ReadFile(nsnap)
	cases = append(cases, struct {
		name    string
		path    string
		opts    backup.RestoreOptions
		wantErr string
	}{"a database from a newer build", craft(t, nb, counts, good(), nil), backup.RestoreOptions{}, "cannot be used by this build"})
	// Not a zip at all.
	junk := filepath.Join(t.TempDir(), "junk.zip")
	os.WriteFile(junk, []byte("not a zip"), 0o600)
	cases = append(cases, struct {
		name    string
		path    string
		opts    backup.RestoreOptions
		wantErr string
	}{"not an archive", junk, backup.RestoreOptions{}, "not a readable backup"})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A directory with data, so "nothing was touched" is checkable.
			dir := newData(t)
			dir.project("Untouched", 1)
			dir.file("files/keep.txt", "keep")
			dir.d.Close()
			outside := filepath.Join(filepath.Dir(dir.dir), "evil.txt")
			tc.opts.Force = true
			_, err := backup.Restore(context.Background(), tc.path, dir.dir, tc.opts)
			if err == nil {
				t.Fatal("the restore should have been refused")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q lacks %q", err, tc.wantErr)
			}
			if got := count(t, filepath.Join(dir.dir, "scriobh.db"), `SELECT count(*) FROM projects`); got != 1 {
				t.Errorf("a refused restore changed the database (%d projects)", got)
			}
			if b, _ := os.ReadFile(filepath.Join(dir.dir, "files", "keep.txt")); string(b) != "keep" {
				t.Error("a refused restore changed the files")
			}
			if _, err := os.Stat(outside); err == nil {
				t.Error("a file was written outside the data directory")
			}
			for _, g := range []string{".restore-*", ".restore-old-*"} {
				if m, _ := filepath.Glob(filepath.Join(dir.dir, g)); len(m) != 0 {
					t.Errorf("leftover %v", m)
				}
			}
			if m, _ := filepath.Glob(filepath.Join(dir.dir, "backups", "pre-restore-*")); len(m) != 0 {
				t.Errorf("a refused restore took a pre-restore backup: %v", m)
			}
		})
	}
}

// A damaged byte inside the archive is caught by the hash.
func TestAFlippedByteIsCaught(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 2)
	x.populate()
	r := x.create(backup.Options{})
	zr, err := zip.OpenReader(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild the archive with one attachment byte changed and the manifest untouched.
	out := filepath.Join(t.TempDir(), "flipped.zip")
	f, _ := os.Create(out)
	zw := zip.NewWriter(f)
	for _, e := range zr.File {
		rc, _ := e.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		if e.Name == "files/projects/p1/attachments/abc.png" {
			b[0] ^= 0xff
		}
		w, _ := zw.Create(e.Name)
		w.Write(b)
	}
	zw.Close()
	f.Close()
	zr.Close()
	if _, err := backup.Restore(context.Background(), out, t.TempDir(), backup.RestoreOptions{}); err == nil || !strings.Contains(err.Error(), "recorded hash") {
		t.Fatalf("a flipped byte: %v", err)
	}
}

// ---- the service: ids, listing, pruning -----------------------------------------------------------

func TestServiceListsOnlyItsOwnBackupsAndNeverTakesAPath(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	x.populate()
	s := &backup.Service{DB: x.d, DataDir: x.dir}
	manual, err := s.Create(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), "auto-"); err != nil {
		t.Fatal(err)
	}
	x.file("backups/evil.zip", "x")
	x.file("backups/notes.txt", "x")
	os.MkdirAll(filepath.Join(x.dir, "backups", "2026-01-01T00-00-00.zip"), 0o700) // a directory with a backup's name

	items, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("listing = %+v (older.zip, evil.zip, migrate/ and the directory must not appear)", items)
	}
	kinds := map[string]bool{}
	for _, it := range items {
		kinds[it.Kind] = true
		if !backup.ValidID(it.ID) || it.Bytes == 0 {
			t.Errorf("item %+v", it)
		}
		if _, err := s.Path(it.ID); err != nil {
			t.Errorf("an id from the listing does not resolve: %v", err)
		}
	}
	if !kinds["manual"] || !kinds["scheduled"] {
		t.Errorf("kinds %v", kinds)
	}
	for _, bad := range []string{"../etc/passwd", "older", "evil", "backups/older", manual.Name, "migrate/pre-migrate-1", "", "2026-01-01T00-00-00", "auto-../x"} {
		if _, err := s.Path(bad); err == nil {
			t.Errorf("Path(%q) should fail", bad)
		}
	}
}

func TestPruneKeepsTheNewestScheduledAndEverythingElse(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	s := &backup.Service{DB: x.d, DataDir: x.dir}
	base := time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		s.Now = func() time.Time { return base.Add(time.Duration(i) * 24 * time.Hour) }
		if _, err := s.Create(context.Background(), "auto-"); err != nil {
			t.Fatal(err)
		}
	}
	s.Now = func() time.Time { return base }
	if _, err := s.Create(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), "pre-restore-"); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Prune(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %v", removed)
	}
	items, _ := s.List()
	var auto []string
	manual, pre := 0, 0
	for _, it := range items {
		switch it.Kind {
		case "scheduled":
			auto = append(auto, it.ID)
		case "manual":
			manual++
		case "pre-restore":
			pre++
		}
	}
	sort.Strings(auto)
	want := []string{"auto-2026-06-04T03-00-00", "auto-2026-06-05T03-00-00", "auto-2026-06-06T03-00-00"}
	if fmt.Sprint(auto) != fmt.Sprint(want) || manual != 1 || pre != 1 {
		t.Errorf("after pruning: scheduled %v manual %d pre-restore %d", auto, manual, pre)
	}
}

// ---- the scheduler -------------------------------------------------------------------------------------

type fakeTime struct {
	mu    sync.Mutex
	now   time.Time
	waits []time.Duration
	ch    chan time.Time
}

func (f *fakeTime) Now() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now }
func (f *fakeTime) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	f.waits = append(f.waits, d)
	f.mu.Unlock()
	return f.ch
}
func (f *fakeTime) tick(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	n := f.now
	f.mu.Unlock()
	f.ch <- n
}
func (f *fakeTime) waited() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.waits...)
}

func TestSchedulerWithAnInjectedClock(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	ft := &fakeTime{now: time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC), ch: make(chan time.Time)}
	svc := &backup.Service{DB: x.d, DataDir: x.dir, Now: ft.Now}
	sch := &backup.Scheduler{Svc: svc, Interval: 24 * time.Hour, Keep: 3, Now: ft.Now, After: ft.After, StartDelay: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()
	waitWaits := func(n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for len(ft.waited()) < n {
			if time.Now().After(deadline) {
				t.Fatalf("the scheduler did not wait for the %dth time: %v", n, ft.waited())
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitWaits(1)
	if w := ft.waited(); w[0] != time.Minute {
		t.Errorf("the first backup should wait the start delay, waited %v", w[0])
	}
	for i := 1; i <= 5; i++ {
		before := len(mustList(t, svc))
		if i == 1 {
			before = 0
		}
		ft.tick(24 * time.Hour)
		waitWaits(i + 1)
		if w := ft.waited(); w[i] != 24*time.Hour {
			t.Errorf("wait %d = %v, want one interval", i, w[i])
		}
		_ = before
	}
	cancel()
	<-done
	items := mustList(t, svc)
	if len(items) != 3 {
		t.Fatalf("5 scheduled backups with keep 3 left %d: %+v", len(items), items)
	}
	for _, it := range items {
		if it.Kind != "scheduled" {
			t.Errorf("%+v", it)
		}
	}

	// A restart soon after the last backup waits out the rest of the interval,
	// not the start delay and not a full new interval.
	ft2 := &fakeTime{now: ft.Now().Add(6 * time.Hour), ch: make(chan time.Time)}
	sch2 := &backup.Scheduler{Svc: svc, Interval: 24 * time.Hour, Keep: 3, Now: ft2.Now, After: ft2.After}
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { sch2.Run(ctx2); close(done2) }()
	deadline := time.Now().Add(5 * time.Second)
	for len(ft2.waited()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel2()
	<-done2
	if w := ft2.waited(); len(w) != 1 || w[0] != 18*time.Hour {
		t.Errorf("after a restart the wait was %v, want 18h", w)
	}

	// Off: returns at once.
	off := &backup.Scheduler{Svc: svc, Interval: 0}
	finished := make(chan struct{})
	go func() { off.Run(context.Background()); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("a scheduler with no interval should return at once")
	}
}

func mustList(t *testing.T, s *backup.Service) []backup.Item {
	t.Helper()
	l, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestSchedulerRetriesAfterAFailure(t *testing.T) {
	x := newData(t)
	ft := &fakeTime{now: time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC), ch: make(chan time.Time)}
	svc := &backup.Service{DB: &db.DB{Path: filepath.Join(x.dir, "missing.db")}, DataDir: x.dir, Now: ft.Now}
	sch := &backup.Scheduler{Svc: svc, Interval: 24 * time.Hour, Keep: 3, Now: ft.Now, After: ft.After, RetryAfter: 30 * time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()
	for _, n := range []int{1, 2} {
		deadline := time.Now().Add(5 * time.Second)
		for len(ft.waited()) < n && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(ft.waited()) < n {
			t.Fatal("no retry was scheduled")
		}
		ft.tick(time.Minute)
	}
	cancel()
	<-done
	if w := ft.waited(); w[1] != 30*time.Minute {
		t.Errorf("the retry waited %v, want 30m", w[1])
	}
}

// ---- the ops ------------------------------------------------------------------------------------------------

func TestBackupOps(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	x.reg.Env().SetService(backup.ServiceName, &backup.Service{DB: x.d, DataDir: x.dir})
	admin := ops.Principal{UserID: testutil.User(t, x.d, ops.RoleAdmin, "a@t.local"), Role: ops.RoleAdmin}
	ctx := context.Background()

	for _, op := range []string{"backup.list", "backup.create", "backup.download"} {
		if _, err := x.reg.Call(ctx, x.owner, op, map[string]any{}); ops.AsError(err).Code != ops.CodeForbidden {
			t.Errorf("%s as a writer: %v", op, err)
		}
	}
	out, err := x.reg.Call(ctx, admin, "backup.create", backup.CreateIn{})
	if err != nil {
		t.Fatal(err)
	}
	created := out.(backup.CreateOut)
	if created.Item.Kind != "manual" || created.Projects != 1 || created.SHA256 == "" || !backup.ValidID(created.Item.ID) {
		t.Fatalf("created %+v", created)
	}
	lo, err := x.reg.Call(ctx, admin, "backup.list", backup.ListIn{})
	if err != nil {
		t.Fatal(err)
	}
	if items := lo.(backup.ListOut).Items; len(items) != 1 || items[0].ID != created.Item.ID {
		t.Fatalf("listing %+v", items)
	}
	dl, err := x.reg.Call(ctx, admin, "backup.download", backup.DownloadIn{ID: created.Item.ID})
	if err != nil {
		t.Fatal(err)
	}
	file := dl.(backup.File)
	name, ct, body := file.Download()
	if name != created.Item.ID+".zip" || ct != "application/zip" || len(body) == 0 {
		t.Errorf("download %q %q %d bytes", name, ct, len(body))
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil || len(zr.File) < 2 {
		t.Errorf("the download is not the archive: %v", err)
	}
	for _, bad := range []string{"../../etc/passwd", "older", "/etc/passwd", "backups/x"} {
		if _, err := x.reg.Call(ctx, admin, "backup.download", backup.DownloadIn{ID: bad}); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("download of %q: %v", bad, err)
		}
	}
	if _, err := x.reg.Call(ctx, admin, "backup.download", backup.DownloadIn{ID: "2020-01-01T00-00-00"}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a well-formed id with no file: %v", err)
	}
}
