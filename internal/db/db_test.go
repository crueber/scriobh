package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"modernc.org/sqlite"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestPragmasApplyToEveryConnection(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	for _, pool := range []struct {
		name string
		q    func(string) (string, error)
	}{
		{"read", func(p string) (string, error) {
			var v string
			err := d.Read().QueryRowContext(ctx, "PRAGMA "+p).Scan(&v)
			return v, err
		}},
		{"write", func(p string) (string, error) {
			var v string
			err := d.Writer().QueryRowContext(ctx, "PRAGMA "+p).Scan(&v)
			return v, err
		}},
	} {
		for p, want := range map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000"} {
			got, err := pool.q(p)
			if err != nil || got != want {
				t.Errorf("%s pool PRAGMA %s = %q, %v; want %q", pool.name, p, got, err, want)
			}
		}
	}
}

func TestMigrateAppliesOnceAndBacksUp(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	bk := filepath.Join(t.TempDir(), "bk")

	res, err := d.Migrate(ctx, bk)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) < 4 {
		t.Fatalf("expected the embedded migrations to apply, got %v", res.Applied)
	}
	if res.Backup != "" {
		t.Errorf("fresh database must not take a pre-flight backup, got %s", res.Backup)
	}
	res, err = d.Migrate(ctx, bk)
	if err != nil || len(res.Applied) != 0 {
		t.Fatalf("second run must be a no-op, got %v, %v", res.Applied, err)
	}

	// A later migration against a database that has data takes a backup first.
	all, _ := Migrations()
	all = append(all, Migration{Version: "9999", Name: "extra", File: "9999_extra.sql", SQL: "CREATE TABLE extra(x);", Checksum: "x"})
	res, err = d.migrateWith(ctx, all, bk)
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("got %v, %v", res.Applied, err)
	}
	if res.Backup == "" {
		t.Fatal("expected a pre-flight backup")
	}
	if _, err := os.Stat(res.Backup); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
}

func TestMigrateRefusesEditedAndUnknown(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	all, _ := Migrations()
	edited := append([]Migration(nil), all...)
	edited[1].Checksum = "different"
	if _, err := d.migrateWith(ctx, edited, ""); err == nil || !strings.Contains(err.Error(), "edited after it was applied") {
		t.Errorf("edited migration must be refused, got %v", err)
	}
	if _, err := d.migrateWith(ctx, all[:len(all)-1], ""); err == nil || !strings.Contains(err.Error(), "does not know") {
		t.Errorf("older binary against a newer database must be refused, got %v", err)
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	bad := []Migration{{Version: "0001", Name: "bad", File: "0001_bad.sql", SQL: "CREATE TABLE ok(x); CREATE TABLE ok(x);", Checksum: "c"}}
	if _, err := d.migrateWith(ctx, bad, ""); err == nil {
		t.Fatal("expected failure")
	}
	var n int
	_ = d.Read().QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='ok'`).Scan(&n)
	if n != 0 {
		t.Error("a failed migration must leave nothing behind")
	}
}

func TestMigrationNaming(t *testing.T) {
	fsys := fstest.MapFS{
		"m/0001_a.sql": {Data: []byte("SELECT 1;")},
		"m/0001_b.sql": {Data: []byte("SELECT 1;")},
	}
	if _, err := loadMigrations(fsys, "m"); err == nil {
		t.Error("duplicate versions must be rejected")
	}
	fsys = fstest.MapFS{"m/1_bad.sql": {Data: []byte("")}}
	if _, err := loadMigrations(fsys, "m"); err == nil {
		t.Error("bad names must be rejected")
	}
}

func TestSchemaForeignKeysEnforced(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	_, err := d.Writer().ExecContext(ctx, `INSERT INTO sessions(id,user_id,token_hash,created_at,last_seen_at,expires_at) VALUES ('s','nobody','h','a','a','a')`)
	if err == nil {
		t.Error("foreign keys must be on")
	}
}

func TestIDsAreSortableAndUnique(t *testing.T) {
	base := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	restore := SetClock(func() time.Time { return base })
	defer restore()
	seen := map[string]bool{}
	prev := ""
	for i := 0; i < 5000; i++ {
		id := NewID("prj")
		if !strings.HasPrefix(id, "prj_") || len(id) != 4+26 {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		if id <= prev {
			t.Fatalf("not increasing: %s then %s", prev, id)
		}
		seen[id], prev = true, id
	}
	base = base.Add(time.Second)
	if later := NewID("prj"); later <= prev {
		t.Errorf("later time must sort later: %s <= %s", later, prev)
	}
}

func TestNowFormat(t *testing.T) {
	restore := SetClock(func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.FixedZone("x", 3600)) })
	defer restore()
	if got, want := Now(), "2026-01-02T02:04:05.006Z"; got != want {
		t.Errorf("Now() = %s, want %s", got, want)
	}
	if _, err := Parse(Now()); err != nil {
		t.Error(err)
	}
}

func TestPositions(t *testing.T) {
	if p := PositionAfter(0); p != PositionGap {
		t.Errorf("first position = %v", p)
	}
	p, re := PositionBetween(PositionGap, 2*PositionGap)
	if re || p <= PositionGap || p >= 2*PositionGap {
		t.Errorf("midpoint wrong: %v %v", p, re)
	}
	a, b := 1.0, 1.0001
	if _, re := PositionBetween(a, b); !re {
		t.Error("a tiny gap must ask for a rebalance")
	}
	if p, _ := PositionBetween(5, 0); p != 5+PositionGap {
		t.Errorf("append wrong: %v", p)
	}
	if p, _ := PositionBetween(-1, 100); p != 50 {
		t.Errorf("prepend wrong: %v", p)
	}
	r := Rebalanced(3)
	if r[0] != PositionGap || r[2] != 3*PositionGap {
		t.Errorf("rebalanced = %v", r)
	}
}

func TestNestedTxFailsInsteadOfDeadlocking(t *testing.T) {
	d := openTemp(t)
	old := acquireTimeout
	acquireTimeout = 150 * time.Millisecond
	defer func() { acquireTimeout = old }()
	ctx := context.Background()
	var inner error
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		inner = d.WithTx(ctx, func(*sql.Tx) error { return nil })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(inner, ErrWriterBusy) {
		t.Fatalf("a nested WithTx must fail with ErrWriterBusy, got %v", inner)
	}
	// The writer is usable afterwards.
	if err := d.WithTx(ctx, func(*sql.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// The Phase 0 database, saved at the phase-0-foundations tag, upgrades with its
// data intact and without touching any applied migration.
func TestPhase0DatabaseUpgrades(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile("testdata/phase0.db")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scriobh.db")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	res, err := d.Migrate(context.Background(), filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) == 0 || res.Backup == "" {
		t.Fatalf("expected new migrations and a pre-flight backup, got %+v", res)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM users`: 2, `SELECT count(*) FROM projects`: 2, `SELECT count(*) FROM project_members`: 3,
		`SELECT count(*) FROM sessions`: 1, `SELECT count(*) FROM events`: 1, `SELECT count(*) FROM audit_log`: 1,
		`SELECT count(*) FROM documents`: 0, `SELECT count(*) FROM projects WHERE archived_at IS NULL`: 2,
	} {
		var n int
		if err := d.Read().QueryRow(q).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d, %v; want %d", q, n, err, want)
		}
	}
	var title string
	if err := d.Read().QueryRow(`SELECT title FROM projects WHERE slug = 'the-salt-road'`).Scan(&title); err != nil || title != "The Salt Road" {
		t.Errorf("project data changed: %q %v", title, err)
	}
}

func TestFullTextSearchIsAvailable(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if _, err := d.Migrate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,role,created_at,updated_at) VALUES ('u','a@b.c','writer','t','t')`,
		`INSERT INTO projects(id,owner_id,title,slug,created_at,updated_at) VALUES ('p','u','P','p','t','t')`,
		`INSERT INTO documents(id,project_id,title,created_at,updated_at) VALUES ('d','p','Scríobh','t','t')`,
		`INSERT INTO doc_text(document_id,project_id,title,synopsis,body) VALUES ('d','p','Scríobh','','The harbour at low tide, Máire waited.')`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	var snip string
	if err := d.Read().QueryRow(`SELECT snippet(doc_fts, 2, '[', ']', '…', 8) FROM doc_fts WHERE doc_fts MATCH 'maire'`).Scan(&snip); err != nil || !strings.Contains(snip, "[Máire]") {
		t.Fatalf("snippet = %q, %v", snip, err)
	}
	// Updates and deletes keep the index in step.
	_, _ = d.Writer().Exec(`UPDATE doc_text SET body = 'nothing here' WHERE document_id = 'd'`)
	var n int
	_ = d.Read().QueryRow(`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'maire'`).Scan(&n)
	if n != 0 {
		t.Error("an update must remove the old terms from the index")
	}
	_, _ = d.Writer().Exec(`DELETE FROM documents WHERE id = 'd'`)
	_ = d.Read().QueryRow(`SELECT count(*) FROM doc_text`).Scan(&n)
	if n != 0 {
		t.Error("deleting a document must cascade to its search text")
	}
}

// The v0.1 database (real documents, a tree, a trashed branch, search rows) must
// open under every later schema with its data and search index intact. Regenerate
// the fixture only for a new release, never to make a failing migration pass.
func TestV01DatabaseUpgrades(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile("testdata/v0.1.db")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scriobh.db")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	res, err := d.Migrate(context.Background(), filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) > 0 && res.Backup == "" {
		t.Errorf("migrations were applied to a database with data but no pre-flight backup was taken: %+v", res)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM documents`:                                                 7,
		`SELECT count(*) FROM documents WHERE deleted_at IS NOT NULL`:                    1,
		`SELECT count(*) FROM doc_text`:                                                  7,
		`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'maire'`:                       1,
		`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'lighthouse'`:                  1,
		`SELECT count(*) FROM documents WHERE title = 'The Last Ferry' AND revision = 2`: 1,
	} {
		var n int
		if err := d.Read().QueryRow(q).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d, %v; want %d", q, n, err, want)
		}
	}
	var chk string
	if err := d.Read().QueryRow(`PRAGMA integrity_check`).Scan(&chk); err != nil || chk != "ok" {
		t.Errorf("integrity_check = %q, %v", chk, err)
	}
	rows, err := d.Read().Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("foreign_key_check reports violations after the upgrade")
	}
}

func TestValidIDHasTheShapeNewIDMakes(t *testing.T) {
	for _, p := range []string{"prj", "doc", "usr", "grm", "gps", "src", "imp"} {
		if id := NewID(p); !ValidID(id) {
			t.Errorf("%s is not valid", id)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "prj_", "prj_x", "PRJ_01J8Z0000000000000000000AA", "prj_01J8Z0000000000000000000A/", "prj_01J8Z0000000000000000000AIU", "/etc", "prj_01J8Z0000000000000000000A.."} {
		if ValidID(bad) {
			t.Errorf("%q is valid", bad)
		}
	}
}

// A connection's page cache holds every page it has touched until it is full, so
// its limit is what keeps the process from growing with the database (soak, 2026-11-14:
// a 64 MB writer cache cost about four times its own count in the driver's
// allocator and climbed for an hour). Fill a file several times the limit
// through the writer and read it back through the pool: no connection may hold
// much more than the limit.
func TestPageCacheStaysWithinItsLimit(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if _, err := d.Writer().ExecContext(ctx, `CREATE TABLE blobs (id INTEGER PRIMARY KEY, b BLOB NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	const rows, size = 6000, 8192 // about 48 MB, six times the 8 MB limit
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	for batch := 0; batch < rows; batch += 500 {
		if err := d.WithTx(ctx, func(tx *sql.Tx) error {
			for i := batch; i < batch+500; i++ {
				if _, err := tx.Exec(`INSERT INTO blobs (b) VALUES (?)`, payload); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Writer().ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	// Read everything back through the writer and through each reader.
	var n int64
	if err := d.Writer().QueryRowContext(ctx, `SELECT sum(length(b)) FROM blobs`).Scan(&n); err != nil || n != rows*size {
		t.Fatalf("writer read %d, %v", n, err)
	}
	var conns []*sql.Conn
	for i := 0; i < 4; i++ {
		c, err := d.Read().Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		if err := c.QueryRowContext(ctx, `SELECT sum(length(b)) FROM blobs`).Scan(&n); err != nil || n != rows*size {
			t.Fatalf("reader %d read %d, %v", i, n, err)
		}
	}
	w, err := d.Writer().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conns = append(conns, w)
	t.Cleanup(func() {
		for _, c := range conns {
			c.Close()
		}
	})
	const limit = 8 << 20
	for i, c := range conns {
		var used int
		err := c.Raw(func(dc any) error {
			cur, _, err := dc.(sqlite.DBStatus).Status(sqlite.DBStatusCacheUsed, false)
			used = cur
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if used > limit+limit/4 {
			t.Errorf("connection %d holds %d bytes of page cache, over its 8 MB limit", i, used)
		}
	}
}
