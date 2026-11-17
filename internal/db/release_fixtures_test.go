package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// releaseFixtures are databases written by each release. phase0, v0.1 and v0.2
// were made when those were tagged and hold more kinds of rows (TestV02DatabaseUpgrades
// counts them); v0.3 to v0.5 were written by those releases' own binaries through
// their API, with TestUpgradeRehearsal in cmd/scriobh and SCRIOBH_UPGRADE_FIXTURE
// (scripts/upgrade-rehearsal.sh), and hold projects, documents, revisions and goals;
// v1.0 was made the same way and holds much more (see v10_upgrade_test.go).
// Every tag has one; add the next release's when it is tagged.
var releaseFixtures = []string{"phase0.db", "v0.1.db", "v0.2.db", "v0.3.db", "v0.3.5.db", "v0.4.db", "v0.5.db", "v1.0.db"}

// Every release's database keeps its projects, documents (text, revision
// counter, trash), revisions and goals through every later migration, and a
// pre-flight copy is taken first.
func TestEveryReleaseFixtureKeepsItsData(t *testing.T) {
	ctx := context.Background()
	queries := map[string]string{
		"projects":  `SELECT group_concat(id || ':' || title, ',') FROM (SELECT id, title FROM projects ORDER BY id)`,
		"documents": `SELECT group_concat(id || ':' || revision || ':' || content_hash || ':' || length(content) || ':' || COALESCE(deleted_at,''), ',') FROM (SELECT * FROM documents ORDER BY id)`,
		"content":   `SELECT group_concat(content, char(30)) FROM (SELECT content FROM documents ORDER BY id)`,
		"revisions": `SELECT group_concat(id || ':' || document_id || ':' || content_hash, ',') FROM (SELECT * FROM document_revisions ORDER BY id)`,
		"goals":     `SELECT group_concat(project_id || ':' || target_words, ',') FROM (SELECT * FROM goals ORDER BY id)`,
	}
	for _, name := range releaseFixtures[1:] {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "scriobh.db")
			if err := os.WriteFile(path, src, 0o600); err != nil {
				t.Fatal(err)
			}
			d, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			before := map[string]string{}
			for k, q := range queries {
				var v sql.NullString
				if err := d.Read().QueryRowContext(ctx, q).Scan(&v); err != nil {
					continue // the table is not in this release yet
				}
				before[k] = v.String
			}
			if before["documents"] == "" {
				t.Fatal("the fixture holds no documents")
			}
			res, err := d.Migrate(ctx, filepath.Join(dir, "backups"))
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Applied) == 0 || res.Backup == "" {
				t.Errorf("applied %d migrations, pre-flight copy %q", len(res.Applied), res.Backup)
			}
			for k, want := range before {
				var got sql.NullString
				if err := d.Read().QueryRowContext(ctx, queries[k]).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got.String != want {
					t.Errorf("%s changed by the upgrade:\n  before: %.300s\n  after:  %.300s", k, want, got.String)
				}
			}
			var check string
			if err := d.Read().QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
				t.Errorf("integrity_check = %q %v", check, err)
			}
		})
	}
}
