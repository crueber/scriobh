package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// testdata/v1.0.db is a database written by the v1.0 release's own development server, through its API, by the
// upgrade rehearsal (scripts/upgrade-rehearsal.sh with UPGRADE_FIXTURE_DIR=internal/db/testdata; cmd/scriobh
// upgrade_rich_test.go lists what it holds): three people with the three roles, five projects (a novel with parts,
// chapters, scenes, notes and front matter; a project made by an import), 32 documents with 20 revisions and
// snapshots, comments and suggestions, two share links (one revoked), a Compendium with entities, aliases,
// relationships, mentions and a timeline event, goals and writing sessions, three API keys (one revoked), a
// webhook, a static-site configuration, a compile preset, a compile and an export, an AI provider with a sealed
// key, a Git remote that was pushed to, and an idempotency key. Sessions and the addresses of the stand-ins it
// pointed at are stripped. The ids and the times differ every time it is made; the counts and the text do not,
// so the checks below name counts and sample values, never ids.

func copyFixture(t *testing.T, name string) (*DB, string) {
	t.Helper()
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
	t.Cleanup(func() { d.Close() })
	return d, dir
}

func tableNames(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out = append(out, s)
	}
	return out
}

func rowCount(t *testing.T, d *DB, table string) int {
	t.Helper()
	var n int
	if err := d.Read().QueryRow(`SELECT count(*) FROM "` + table + `"`).Scan(&n); err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	return n
}

// tableHash is a hash of every row of a table, in a stable order, over the named columns (all of them when none are named).
func tableHash(t *testing.T, d *DB, table string, cols ...string) string {
	t.Helper()
	sel := "*"
	if len(cols) > 0 {
		sel = strings.Join(cols, ", ")
	}
	rows, err := d.Read().Query(`SELECT ` + sel + ` FROM "` + table + `"`)
	if err != nil {
		t.Fatalf("%s: %v", table, err)
	}
	defer rows.Close()
	names, _ := rows.Columns()
	var lines []string
	for rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		var sb strings.Builder
		for _, v := range vals {
			fmt.Fprintf(&sb, "%T:%v\x1f", v, v)
		}
		lines = append(lines, sb.String())
	}
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\x1e")))
	return hex.EncodeToString(h[:])
}

func queryRows(t *testing.T, d *DB, q string, args ...any) [][]string {
	t.Helper()
	rows, err := d.Read().Query(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			switch x := v.(type) {
			case nil:
				row[i] = "NULL"
			case []byte:
				row[i] = string(x)
			default:
				row[i] = fmt.Sprint(x)
			}
		}
		out = append(out, row)
	}
	return out
}

func one(t *testing.T, d *DB, q string, args ...any) string {
	t.Helper()
	r := queryRows(t, d, q, args...)
	if len(r) != 1 || len(r[0]) != 1 {
		t.Fatalf("%s: %v", q, r)
	}
	return r[0][0]
}

// The v1.0 database upgrades to this build with nothing lost, the exports rebuild (1895) intact, every new table
// empty and every new feature off, the old Git remote reading as private, and a second start a no-op.
func TestV10DatabaseUpgrades(t *testing.T) {
	ctx := context.Background()
	d, dir := copyFixture(t, "v1.0.db")
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}

	// ---- before ---------------------------------------------------------------------------------------------
	oldMigrations := queryRows(t, d, `SELECT version FROM schema_migrations ORDER BY version`)
	if len(oldMigrations) != 59 {
		t.Fatalf("the fixture holds %d migrations, want the 59 of v1.0", len(oldMigrations))
	}
	before := map[string]int{}
	for _, tb := range tableNames(t, d) {
		before[tb] = rowCount(t, d, tb)
	}
	// What the fixture must hold, so that "nothing was lost" means something.
	for table, want := range map[string]int{
		"users": 3, "projects": 5, "project_members": 7, "documents": 32, "document_revisions": 20, "comments": 4, "suggestions": 2, "share_links": 2,
		"entities": 4, "entity_aliases": 5, "entity_relations": 2, "timeline_events": 1, "goals": 4, "api_keys": 3, "webhooks": 1, "site_options": 1,
		"project_sources": 1, "compile_presets": 1, "exports": 2, "ai_providers": 1, "git_remotes": 1, "git_pushes": 1, "idempotency_keys": 1, "compendiums": 1,
	} {
		if before[table] != want {
			t.Errorf("the fixture's %s has %d rows, want %d", table, before[table], want)
		}
	}
	if before["entity_mentions"] == 0 || before["writing_sessions"] == 0 || before["daily_stats"] == 0 {
		t.Errorf("the fixture lacks mentions (%d), writing sessions (%d) or daily stats (%d)", before["entity_mentions"], before["writing_sessions"], before["daily_stats"])
	}
	// Tables whose rows must come through byte for byte.
	kept := []string{"users", "projects", "project_members", "documents", "document_revisions", "revision_blobs", "comments", "suggestions", "share_links",
		"entities", "entity_aliases", "entity_relations", "entity_mentions", "timeline_events", "goals", "writing_sessions", "daily_stats", "writing_session_docs",
		"api_keys", "webhooks", "webhook_state", "site_options", "project_sources", "document_sources", "compile_presets", "ai_providers", "idempotency_keys",
		"compendiums", "compendium_members", "git_pushes", "settings", "doc_text", "notifications"}
	// Compared over the columns v1.0 had: a later migration may add a column (1820 gives suggestions a label), and that
	// is not a change to what was stored.
	oldCols := map[string][]string{}
	hashBefore := map[string]string{}
	for _, tb := range kept {
		for _, r := range queryRows(t, d, `SELECT name FROM pragma_table_info('`+tb+`') ORDER BY cid`) {
			oldCols[tb] = append(oldCols[tb], `"`+r[0]+`"`)
		}
		hashBefore[tb] = tableHash(t, d, tb, oldCols[tb]...)
	}
	// The remote's columns that existed in v1.0 (1830 and 1835 add more).
	remoteCols := []string{"id", "project_id", "name", "preset", "url", "branch", "path_prefix", "auth_kind", "username", "secret_cipher", "public_key", "known_hosts",
		"push_mode", "interval_minutes", "include_notes", "author_name", "author_email", "enabled", "state", "last_push_at", "last_attempt_at", "last_commit_sha",
		"local_sha", "remote_sha", "mirror_hash", "last_error", "last_error_code", "fail_count", "next_attempt_at", "created_at", "updated_at"}
	remoteBefore := tableHash(t, d, "git_remotes", remoteCols...)
	// exports (1895 rebuilds it): the rows, the indexes and triggers, and the foreign keys.
	exportsBefore := tableHash(t, d, "exports")
	exportsAuxBefore := queryRows(t, d, `SELECT type, name, sql FROM sqlite_master WHERE tbl_name = 'exports' AND type <> 'table' ORDER BY name`)
	exportsFKBefore := queryRows(t, d, `SELECT "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list('exports') ORDER BY id`)
	if len(exportsAuxBefore) < 1 || len(exportsFKBefore) != 2 {
		t.Fatalf("the fixture's exports has %d indexes or triggers and %d foreign keys", len(exportsAuxBefore), len(exportsFKBefore))
	}
	// A foreign-key violation in the fixture itself would hide one the migration made.
	if r := queryRows(t, d, `PRAGMA foreign_key_check`); len(r) != 0 {
		t.Fatalf("the fixture has foreign key violations before the upgrade: %v", r)
	}

	// ---- upgrade ----------------------------------------------------------------------------------------------
	res, err := d.Migrate(ctx, filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != len(all)-59 || len(res.Applied) < 20 {
		t.Errorf("applied %d migrations, want every one after v1.0's 59 (%d known now; v1.1 added 20): %v", len(res.Applied), len(all), res.Applied)
	}
	if !slices.Contains(res.Applied, "1895") {
		t.Error("the exports rebuild (1895) did not run")
	}
	if res.Backup == "" {
		t.Fatal("no pre-migration backup was taken")
	}
	// The pre-migration copy is the v1.0 database as it was: 59 migrations, the same rows.
	bd, err := Open(res.Backup)
	if err != nil {
		t.Fatal(err)
	}
	if n := rowCount(t, bd, "schema_migrations"); n != 59 {
		t.Errorf("the pre-migration copy holds %d migrations", n)
	}
	for _, tb := range []string{"documents", "exports", "git_remotes", "share_links", "api_keys"} {
		if rowCount(t, bd, tb) != before[tb] {
			t.Errorf("the pre-migration copy's %s has %d rows, want %d", tb, rowCount(t, bd, tb), before[tb])
		}
	}
	if tableHash(t, bd, "exports") != exportsBefore {
		t.Error("the pre-migration copy's exports differ from the database it was taken of")
	}
	bd.Close()

	// ---- integrity ----------------------------------------------------------------------------------------------
	if got := one(t, d, `PRAGMA integrity_check`); got != "ok" {
		t.Errorf("integrity_check = %q", got)
	}
	if r := queryRows(t, d, `PRAGMA foreign_key_check`); len(r) != 0 {
		t.Errorf("foreign_key_check after the upgrade: %v", r)
	}

	// ---- nothing lost -----------------------------------------------------------------------------------------------
	changedOnPurpose := map[string]bool{"schema_migrations": true, "audit_log": true, "events": true}
	for tb, n := range before {
		if changedOnPurpose[tb] {
			continue
		}
		if got := rowCount(t, d, tb); got != n {
			t.Errorf("%s: %d rows before, %d after", tb, n, got)
		}
	}
	for _, tb := range kept {
		if got := tableHash(t, d, tb, oldCols[tb]...); got != hashBefore[tb] {
			t.Errorf("%s changed by the upgrade (its rows are not what v1.0 left)", tb)
		}
	}
	if n := rowCount(t, d, "schema_migrations"); n != len(all) {
		t.Errorf("schema_migrations holds %d rows, want %d", n, len(all))
	}
	// A sample of values, read as a person would.
	if got := queryRows(t, d, `SELECT email, role FROM users ORDER BY email`); fmt.Sprint(got) != "[[admin@dev.scriobh admin] [reader@dev.scriobh reader] [writer@dev.scriobh writer]]" {
		t.Errorf("people and roles: %v", got)
	}
	if got := one(t, d, `SELECT group_concat(title, '|') FROM (SELECT title FROM projects ORDER BY title)`); got != "Field Notes|Imported project|Lantern Hours|The Lantern Cycle|The Salt Road" {
		t.Errorf("projects: %s", got)
	}
	if got := one(t, d, `SELECT count(*) FROM documents WHERE kind = 'part'`); got < "2" {
		t.Errorf("parts: %s", got)
	}
	if got := one(t, d, `SELECT count(*) FROM documents d JOIN projects p ON p.id = d.project_id WHERE p.title = 'The Lantern Cycle' AND d.title = 'The Lamplighter' AND d.content LIKE '%counting the lamps%' AND d.revision = 4`); got != "1" {
		t.Errorf("a chapter of the novel, its text and its revision counter: %s", got)
	}
	if got := one(t, d, `SELECT count(*) FROM documents WHERE deleted_at IS NOT NULL`); got != "2" {
		t.Errorf("trashed documents: %s", got)
	}
	if got := one(t, d, `SELECT count(*) FROM document_revisions WHERE label LIKE 'Draft %'`); got != "10" {
		t.Errorf("named snapshots: %s", got)
	}
	if got := one(t, d, `SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'lantern'`); got == "0" {
		t.Error("full-text search finds nothing after the upgrade")
	}
	if got := one(t, d, `SELECT group_concat(name, '|') FROM (SELECT name FROM entities ORDER BY name)`); got != "Mara Quay|Nora Lantern|The Great Lantern|The Quay" {
		t.Errorf("entities: %s", got)
	}
	if got := one(t, d, `SELECT count(*) FROM share_links WHERE revoked_at IS NOT NULL`); got != "1" {
		t.Errorf("revoked share links: %s", got)
	}
	if got := one(t, d, `SELECT count(*) FROM api_keys WHERE revoked_at IS NOT NULL`); got != "1" {
		t.Errorf("revoked keys: %s", got)
	}
	if got := one(t, d, `SELECT count(*) FROM ai_providers WHERE api_key_cipher <> ''`); got != "1" {
		t.Errorf("the provider's sealed key: %s", got)
	}
	if got := one(t, d, `SELECT count(*) FROM webhooks WHERE secret_sealed <> '' AND enabled = 1`); got != "1" {
		t.Errorf("the webhook's sealed secret: %s", got)
	}
	if got := one(t, d, `SELECT json_extract(options_json, '$.footer_text') FROM site_options`); got != "Lit by the sea." {
		t.Errorf("the site configuration: %s", got)
	}

	// ---- exports (1895's rebuild) ------------------------------------------------------------------------------------
	if got := tableHash(t, d, "exports"); got != exportsBefore {
		t.Error("exports lost or changed a row in the rebuild")
	}
	if got := rowCount(t, d, "exports"); got != 2 {
		t.Errorf("exports has %d rows, want 2", got)
	}
	if got := queryRows(t, d, `SELECT type, name, sql FROM sqlite_master WHERE tbl_name = 'exports' AND type <> 'table' ORDER BY name`); fmt.Sprint(got) != fmt.Sprint(exportsAuxBefore) {
		t.Errorf("exports' indexes and triggers changed:\n  before %v\n  after  %v", exportsAuxBefore, got)
	}
	if got := queryRows(t, d, `SELECT "table", "from", "to", on_update, on_delete FROM pragma_foreign_key_list('exports') ORDER BY id`); fmt.Sprint(got) != fmt.Sprint(exportsFKBefore) {
		t.Errorf("exports' foreign keys changed:\n  before %v\n  after  %v", exportsFKBefore, got)
	}
	if got := one(t, d, `SELECT count(*) FROM sqlite_master WHERE name = 'exports_new'`); got != "0" {
		t.Error("the rebuild left exports_new behind")
	}
	// The keys still hold: a row for a project that is not there is refused, a project's purge removes its exports,
	// a format that is not on the list is refused, and the new ones are taken.
	var project, user string
	if err := d.Read().QueryRow(`SELECT project_id, user_id FROM exports ORDER BY id LIMIT 1`).Scan(&project, &user); err != nil {
		t.Fatal(err)
	}
	ins := func(id, proj, format string) error {
		_, err := d.Writer().Exec(`INSERT INTO exports(id, project_id, user_id, preset, format, filename, created_at) VALUES (?,?,?,'p',?,?,'t')`, id, proj, user, format, "f."+format)
		return err
	}
	if err := ins("x_nofk", "prj_missing", "md"); err == nil {
		t.Error("an export of a project that does not exist was accepted: the foreign key is gone")
	}
	if err := ins("x_bad", project, "exe"); err == nil {
		t.Error("an export in a format that is not on the list was accepted")
	}
	for _, f := range []string{"odt", "rtf", "latex", "rst", "mediawiki", "epub", "pdf", "html"} {
		if err := ins("x_"+f, project, f); err != nil {
			t.Errorf("%s was refused: %v", f, err)
		}
	}
	// An index is used where it should be (the rebuilt table keeps its lookup by project).
	var plan []string
	for _, r := range queryRows(t, d, `EXPLAIN QUERY PLAN SELECT * FROM exports WHERE project_id = 'x' ORDER BY created_at`) {
		plan = append(plan, r[len(r)-1])
	}
	if !strings.Contains(strings.Join(plan, ";"), "exports_project") {
		t.Errorf("a lookup of a project's exports does not use exports_project: %v", plan)
	}
	if _, err := d.Writer().Exec(`DELETE FROM exports WHERE id LIKE 'x_%'`); err != nil {
		t.Fatal(err)
	}
	tx, err := d.Writer().Begin()
	if err != nil {
		t.Fatal(err)
	}
	var cascaded int
	if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, project); err != nil {
		t.Fatal(err)
	}
	_ = tx.QueryRow(`SELECT count(*) FROM exports WHERE project_id = ?`, project).Scan(&cascaded)
	_ = tx.Rollback()
	if cascaded != 0 {
		t.Errorf("deleting a project left %d of its exports: the cascade is gone", cascaded)
	}

	// ---- the old Git remote reads as private --------------------------------------------------------------------------
	if got := tableHash(t, d, "git_remotes", remoteCols...); got != remoteBefore {
		t.Error("the remote's columns from v1.0 changed")
	}
	var audience, via string
	var onlyDone, sign, restorePaused, notes int
	if err := d.Read().QueryRow(`SELECT audience, only_done, sign_commits, created_via, include_notes, restore_paused FROM git_remotes`).Scan(&audience, &onlyDone, &sign, &via, &notes, &restorePaused); err != nil {
		t.Fatal(err)
	}
	if audience != "private" || onlyDone != 0 || sign != 0 || via != "" || notes != 1 || restorePaused != 0 {
		t.Errorf("the old remote reads as audience=%q only_done=%d sign=%d via=%q include_notes=%d restore_paused=%d: it must still be the full mirror, unsigned, not paused", audience, onlyDone, sign, via, notes, restorePaused)
	}
	if got := one(t, d, `SELECT count(*) FROM git_remotes WHERE enabled = 1 AND state = 'synced' AND secret_cipher <> '' AND known_hosts <> ''`); got != "1" {
		t.Errorf("the remote is no longer enabled, synced, sealed and pinned: %s", got)
	}

	// ---- every new table is empty, and every new feature is off -------------------------------------------------------
	var fresh []string
	for _, tb := range tableNames(t, d) {
		if _, old := before[tb]; !old {
			fresh = append(fresh, tb)
		}
	}
	if len(fresh) < 25 {
		t.Errorf("only %d new tables: %v", len(fresh), fresh)
	}
	for _, tb := range fresh {
		if strings.Contains(tb, "_fts") {
			continue // the search index's own tables
		}
		n := rowCount(t, d, tb)
		if tb == "mail_config" && n == 1 {
			if got := one(t, d, `SELECT enabled || host || password_sealed || restore_paused FROM mail_config`); got != "000" {
				t.Errorf("mail_config's seeded row is not off and empty: %q", got)
			}
			continue
		}
		if n != 0 {
			t.Errorf("the new table %s has %d rows after the upgrade", tb, n)
		}
	}
	for what, q := range map[string]string{
		"mail on":             `SELECT count(*) FROM mail_config WHERE enabled = 1 OR host <> ''`,
		"a mail message":      `SELECT count(*) FROM mail_outbox`,
		"a digest opt-in":     `SELECT count(*) FROM mail_digest`,
		"a Kindle address":    `SELECT count(*) FROM kindle_addresses`,
		"a semantic index":    `SELECT count(*) FROM embed_projects`,
		"an embedding":        `SELECT count(*) FROM embed_chunks`,
		"Pandoc":              `SELECT count(*) FROM settings WHERE key LIKE 'pandoc.%'`,
		"presence":            `SELECT count(*) FROM user_prefs WHERE key = 'presence.share'`,
		"a passphrase":        `SELECT count(*) FROM project_gates`,
		"a typography row":    `SELECT count(*) FROM project_typography`,
		"a person typography": `SELECT count(*) FROM user_typography`,
		"an edition":          `SELECT count(*) FROM editions`,
		"a signing key":       `SELECT count(*) FROM git_signing_keys`,
		"a trigger":           `SELECT count(*) FROM git_triggers`,
		"a Git account":       `SELECT count(*) FROM git_accounts`,
		"a release":           `SELECT count(*) FROM git_releases`,
	} {
		if got := one(t, d, q); got != "0" {
			t.Errorf("after the upgrade there is %s (%s = %s)", what, q, got)
		}
	}

	// ---- a second start is a no-op -------------------------------------------------------------------------------------
	d.Close()
	d2, err := Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	res2, err := d2.Migrate(ctx, filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Applied) != 0 || res2.Backup != "" {
		t.Errorf("a second start applied %v and took the copy %q", res2.Applied, res2.Backup)
	}
	if files, _ := filepath.Glob(filepath.Join(filepath.Dir(res.Backup), "*")); len(files) != 1 {
		t.Errorf("pre-migration copies after two starts: %v", files)
	}
	for _, tb := range kept {
		if got := tableHash(t, d2, tb, oldCols[tb]...); got != hashBefore[tb] {
			t.Errorf("%s changed by the second start", tb)
		}
	}
}
