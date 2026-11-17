package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/devgit"
)

// The Phase 6 upgrade rehearsal: data written by an older release's binary,
// through its own API, is opened by this build.
//
// SCRIOBH_UPGRADE_OLD_BIN names a development build of an older tag
// (scripts/upgrade-rehearsal.sh builds one from `git archive`). The old server
// creates projects, documents and saves, and snapshots and a goal where that
// release has them; every answer is recorded. Then the current binary starts on
// the same data directory and must: take the pre-migration copy, apply every
// migration, end with the schema of a fresh install, and answer with every
// field the old release answered, unchanged.
//
// SCRIOBH_UPGRADE_FIXTURE, when set, is where the old release's database is
// copied (checkpointed, before the upgrade): that is how the fixtures in
// internal/db/testdata are made.

func TestUpgradeRehearsal(t *testing.T) {
	oldBin := os.Getenv("SCRIOBH_UPGRADE_OLD_BIN")
	if oldBin == "" {
		t.Skip("set SCRIOBH_UPGRADE_OLD_BIN to an older release's development build (scripts/upgrade-rehearsal.sh does)")
	}
	tag := os.Getenv("SCRIOBH_UPGRADE_TAG")
	bin := rehearsalBinary(t)
	data := t.TempDir()
	key := "SCRIOBH_SECRET_KEY=" + rehearsalKey1
	common := []string{key, "SCRIOBH_MIRROR_DEBOUNCE_MS=50", "SCRIOBH_BACKUP_INTERVAL_HOURS=0", "SCRIOBH_WEBHOOK_ALLOW_PRIVATE=1", "SCRIOBH_GIT_ALLOW_PRIVATE=1", "SCRIOBH_RATE_LIMIT=0", "SCRIOBH_WEBHOOK_DEBOUNCE_MS=50"}

	// The stand-ins the rich data points at: a development Git server (the system git program is its server side),
	// a webhook receiver and an AI provider, all on loopback.
	rc := &rich{t: t, ops: map[string]richOp{}, wrote: map[string]int{}}
	rc.hook, rc.ai = &hookSink{}, &fakeAI{}
	rc.hookSrv, rc.aiSrv = httptest.NewServer(rc.hook), httptest.NewServer(rc.ai)
	defer rc.hookSrv.Close()
	defer rc.aiSrv.Close()
	if _, err := exec.LookPath("git"); err == nil {
		rc.gitRoot = t.TempDir()
		g, err := devgit.Start(devgit.Options{Root: rc.gitRoot})
		if err != nil {
			t.Fatal(err)
		}
		defer g.Stop()
		rc.git = g
		resp, err := http.Post(g.ControlURL()+"/repos", "application/json", strings.NewReader(`{"name":"book"}`))
		if err != nil || resp.StatusCode >= 300 {
			t.Fatalf("dev git server: create the repository: %v %v", err, resp)
		}
		resp.Body.Close()
	}

	// ---- the old release writes ----------------------------------------------
	old := start(t, oldBin, data, common...)
	a := newAPI(t, old.base, "dev-writer")
	type item struct {
		Item struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
		} `json:"item"`
	}
	var projects []string
	var docs []string
	has := map[string]bool{}
	for _, title := range []string{"The Salt Road", "Lantern Hours"} {
		var p item
		a.call("POST", "/api/v1/projects", map[string]any{"title": title, "kind": "novel"}, &p, 201)
		projects = append(projects, p.Item.ID)
		for i, ch := range []string{"Harbour", "The Road", "Gorse"} {
			var d item
			body := fmt.Sprintf("# %s\n\nThe quiet harbour, chapter %d of %s. Nóra counted *seven* boats.  \n", ch, i+1, title)
			a.call("POST", "/api/v1/projects/"+p.Item.ID+"/docs", map[string]any{"title": ch, "kind": "chapter", "content": body}, &d, 201)
			docs = append(docs, d.Item.ID)
			rev := d.Item.Revision
			for j := 0; j < 3; j++ {
				body += fmt.Sprintf("\nA line added on save %d.\n", j+1)
				var u item
				a.call("PATCH", "/api/v1/docs/"+d.Item.ID, map[string]any{"base_revision": rev, "content": body}, &u, 200)
				rev = u.Item.Revision
				// A named snapshot, where the release has them (v0.2 and later).
				if j == 1 {
					code, _ := a.do("POST", "/api/v1/docs/"+d.Item.ID+"/revisions", map[string]any{"label": "Draft " + ch}, "")
					has["revisions"] = has["revisions"] || code == 201
				}
			}
		}
		code, _ := a.do("PUT", "/api/v1/projects/"+p.Item.ID+"/goals", map[string]any{"kind": "daily", "target_words": 750}, "")
		has["goals"] = has["goals"] || code == 200
	}
	a.call("POST", "/api/v1/docs/"+docs[2]+"/trash", map[string]any{}, nil, 200)

	// The rich dataset: what a long-lived instance of that release holds.
	// The rich dataset is written through the old release's own operation table, which the first releases do not
	// have: from v0.5 on it is strict (a failure fails the rehearsal), before that it is skipped and says so.
	var rr *rich
	if richWanted(tag) {
		rr = newRich(t, old.base, func(sub string) *apiClient {
			if sub == "dev-writer" {
				return a
			}
			return newAPI(t, old.base, sub)
		})
	}
	if rr == nil {
		rr = &rich{t: t, off: true, ops: map[string]richOp{}, wrote: map[string]int{}}
		report(t, "%s: the rich dataset is skipped: this release has no operation table (_ops) to write it through; the small dataset above is what it is rehearsed with", tag)
	}
	rr.hook, rr.hookSrv, rr.ai, rr.aiSrv, rr.git, rr.gitRoot = rc.hook, rc.hookSrv, rc.ai, rc.aiSrv, rc.git, rc.gitRoot
	rr.write(old.base)

	capture := func(a *apiClient) map[string]any {
		out := map[string]any{}
		put := func(k string, b []byte) {
			var v any
			if err := json.Unmarshal(b, &v); err != nil {
				t.Fatalf("%s: %v", k, err)
			}
			out[k] = v
		}
		put("project.list", a.get("/api/v1/projects"))
		for _, pid := range projects {
			put(pid+" project.get", a.get("/api/v1/projects/"+pid))
			put(pid+" doc.list", a.get("/api/v1/projects/"+pid+"/docs"))
			put(pid+" search", a.get("/api/v1/projects/"+pid+"/search?q=harbour"))
			if has["goals"] {
				put(pid+" goal.list", a.get("/api/v1/projects/"+pid+"/goals"))
			}
		}
		for _, id := range docs {
			put(id+" doc.get", a.get("/api/v1/docs/"+id))
			if has["revisions"] {
				put(id+" revision.list", a.get("/api/v1/docs/"+id+"/revisions"))
			}
		}
		return out
	}
	before := capture(a)
	richBefore := rr.capture(a)
	if !rr.off {
		report(t, "%s: the rich dataset: %d kinds of write through the old API (%d operations the old release lacks were skipped); %d answers recorded", tag, len(rr.wrote), len(rr.skipped), len(richBefore))
	}
	old.stop(t)

	path := filepath.Join(data, "scriobh.db")
	oldMigrations := migrationNames(t, path)
	if out := os.Getenv("SCRIOBH_UPGRADE_FIXTURE"); out != "" {
		writeFixture(t, path, out)
	}

	// ---- this build opens it --------------------------------------------------
	cur := start(t, bin, data, common...)
	b := withJar(t, cur.base, a.c.Jar) // the session written by the old release still works
	if code, body := b.do("GET", "/api/v1/projects", nil, ""); code != 200 {
		t.Fatalf("the old release's session after the upgrade: %d %s\n%s", code, body, cur.logs)
	}
	after := capture(b)
	richAfter := rr.capture(b)
	rr.afterUpgrade(cur.base, b)
	if !rr.off {
		rr.newFeaturesAreOff(cur.base, rr.a.c.Jar)
	}
	cur.stop(t)

	// Every migration a fresh install has, and its schema.
	ctx := context.Background()
	fresh, err := db.Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.Migrate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	wantMig := migrationsOf(t, fresh)

	// The pre-migration copy: taken, and it is the old release's database. When the old release already holds every
	// migration this build has (a release that added none, as v2.0 added none to v1.2), nothing is pending and no copy
	// is taken: asking for one would be asking the server to copy a database it has no reason to touch.
	pending := strings.Join(oldMigrations, ",") != strings.Join(wantMig, ",")
	pre, _ := filepath.Glob(filepath.Join(data, "backups", "migrate", "pre-migrate-*.db"))
	if pending {
		if len(pre) != 1 {
			t.Fatalf("pre-migration copies: %v\n%s", pre, cur.logs)
		}
		if got := migrationNames(t, pre[0]); strings.Join(got, ",") != strings.Join(oldMigrations, ",") {
			t.Errorf("the pre-migration copy holds migrations %v, the old database %v", got, oldMigrations)
		}
	} else if len(pre) != 0 || strings.Contains(cur.logs.String(), "migrations applied") {
		t.Fatalf("nothing was pending, yet a pre-migration copy %v was taken or migrations were applied\n%s", pre, cur.logs)
	}
	upgraded := migrationNames(t, path)
	if strings.Join(upgraded, ",") != strings.Join(wantMig, ",") {
		t.Errorf("migrations after the upgrade:\n  got  %v\n  want %v", upgraded, wantMig)
	}
	want, err := db.DescribeSchema(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	ud, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.DescribeSchema(ctx, ud)
	ud.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffMaps(want, got) {
		t.Errorf("schema after the upgrade: %s", d)
	}
	dbState(t, path) // integrity_check and foreign_key_check

	// Nothing lost: every field the old release answered is answered the same.
	var lost []string
	for k, v := range before {
		lost = append(lost, subset(k, v, after[k])...)
	}
	sort.Strings(lost)
	for _, l := range lost {
		t.Errorf("changed by the upgrade: %s", l)
	}
	var richLost []string
	for k, v := range richBefore {
		richLost = append(richLost, subset(k, v, richAfter[k])...)
	}
	sort.Strings(richLost)
	for _, l := range richLost {
		t.Errorf("changed by the upgrade (rich data): %s", l)
	}
	if !rr.off {
		report(t, "%s: %d rich answers (people and roles, parts, chapters, scenes, notes, revisions, comments, suggestions, shares, keys, webhooks, site, compendium, mentions, goals, providers, Git remote) carry every old field unchanged", tag, len(richBefore))
	}
	if pending {
		report(t, "%s: %d migrations before, %d after (%d applied); pre-migration copy %s", tag, len(oldMigrations), len(upgraded), len(upgraded)-len(oldMigrations), filepath.Base(pre[0]))
	} else {
		report(t, "%s: %d migrations before and after (none pending: none applied, no pre-migration copy taken)", tag, len(upgraded))
	}
	report(t, "%s: schema equals a fresh install (%d tables, indexes and triggers compared, with columns, foreign keys and unique constraints)", tag, len(want))
	report(t, "%s: %d API answers (projects, documents%s%s, search) carry every old field unchanged; the old session still signs in", tag, len(before),
		map[bool]string{true: ", revisions"}[has["revisions"]], map[bool]string{true: ", goals"}[has["goals"]])

	// A second start of the upgraded build is a no-op: nothing to migrate, no second pre-migration copy.
	again2 := start(t, bin, data, common...)
	if code, _ := withJar(t, again2.base, a.c.Jar).do("GET", "/api/v1/projects", nil, ""); code != 200 {
		t.Errorf("the second start: %d", code)
	}
	again2.stop(t)
	if strings.Contains(again2.logs.String(), "migrations applied") {
		t.Errorf("the second start applied migrations:\n%s", again2.logs)
	}
	wantCopies := 1
	if !pending {
		wantCopies = 0
	}
	if pre2, _ := filepath.Glob(filepath.Join(data, "backups", "migrate", "*")); len(pre2) != wantCopies {
		t.Errorf("a second start made another pre-migration copy: %v", pre2)
	}
	if got := migrationNames(t, path); strings.Join(got, ",") != strings.Join(upgraded, ",") {
		t.Errorf("a second start changed the migrations: %v", got)
	}
	report(t, "%s: a second start of the upgraded build applies nothing and takes no second copy", tag)

	// The way back documented in OPERATIONS.md: the pre-migration copy over the
	// database, and the old binary starts on it with everything it wrote.
	// With nothing pending there is no copy to put back and nothing to put back it from: the old binary is started on the
	// database the upgraded build left, which has the migrations it knows, and must answer as before.
	if pending {
		for _, f := range []string{path + "-wal", path + "-shm"} {
			_ = os.Remove(f)
		}
		raw, err := os.ReadFile(pre[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	back := start(t, oldBin, data, common...)
	if !pending {
		// What the rehearsal wrote after the upgrade (rich.afterUpgrade) is in the database, so the old answers cannot be
		// compared with the ones recorded before it: the old release must start on the upgraded database and read it with
		// the session it made, and it did not need a copy to do so.
		bj := withJar(t, back.base, a.c.Jar)
		if code, body := bj.do("GET", "/api/v1/projects", nil, ""); code != 200 {
			t.Errorf("the old release on the upgraded database (no migration): %d %s\n%s", code, body, back.logs)
		}
		for _, pid := range projects {
			if code, body := bj.do("GET", "/api/v1/projects/"+pid+"/docs", nil, ""); code != 200 {
				t.Errorf("the old release lists the documents of %s on the upgraded database: %d %s", pid, code, body)
			}
		}
		back.stop(t)
		report(t, "%s: nothing to roll back (no migration): the old binary starts on the upgraded database, with its own session, and reads every project", tag)
		return
	}
	// The mirror and the local Git repository are files, not rows: they are not rolled back with the database, and
	// the edit made after the upgrade (rich.afterUpgrade) is in them. The old release writes the mirror again from
	// the database it has, and must say what it finds of the remote.
	rr.waitMirrorOn(withJar(t, back.base, a.c.Jar), rr.f.P1)
	rr.reportRemoteAfterRollback(withJar(t, back.base, a.c.Jar), tag)
	again := capture(withJar(t, back.base, a.c.Jar))
	richAgain := rr.capture(withJar(t, back.base, a.c.Jar))
	back.stop(t)
	var changed []string
	for k, v := range before {
		changed = append(changed, subset(k, v, again[k])...)
	}
	// Not compared after a rollback: whether the remote is in sync. The edit made after the upgrade was pushed, the
	// rollback took the edit out of the database, and the remote therefore holds a commit the old release has not
	// made: a next push, which with push mode manual is the owner's to start, sends the older text as a new commit.
	for k, v := range richBefore {
		changed = append(changed, subset(k, without(v, "in_sync"), without(richAgain[k], "in_sync"))...)
	}
	for _, l := range changed {
		t.Errorf("after rolling back to %s: %s", tag, l)
	}
	report(t, "%s: rolled back by copying the pre-migration file over scriobh.db; the old binary starts and answers as before", tag)
}

// subset reports where want (an old answer) is not contained in got (the new
// one): an object's fields must all be there with the same values (new fields
// are fine); arrays must have the same length, compared item by item.
func subset(at string, want, got any) []string {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s: was an object, now %T", at, got)}
		}
		var out []string
		for k, wv := range w {
			if volatileField(k) {
				continue
			}
			gv, ok := g[k]
			if !ok {
				out = append(out, at+"."+k+": missing")
				continue
			}
			out = append(out, subset(at+"."+k, wv, gv)...)
		}
		return out
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return []string{fmt.Sprintf("%s: %d items, now %v", at, len(w), lenOf(got))}
		}
		var out []string
		for i := range w {
			out = append(out, subset(fmt.Sprintf("%s[%d]", at, i), w[i], g[i])...)
		}
		return out
	default:
		if fmt.Sprint(want) != fmt.Sprint(got) {
			return []string{fmt.Sprintf("%s: %v, now %v", at, want, got)}
		}
		return nil
	}
}

// volatileField names answers that change by being looked at or by the day.
func volatileField(k string) bool {
	switch k {
	case "last_opened_at", "opened_at", "today", "progress", "streak", "words_today", "score", "snippet":
		return true
	}
	return false
}

func lenOf(v any) any {
	if a, ok := v.([]any); ok {
		return len(a)
	}
	return v
}

func migrationNames(t *testing.T, path string) []string {
	t.Helper()
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	return migrationsOf(t, d)
}

func migrationsOf(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.Read().Query(`SELECT version || '_' || name FROM schema_migrations ORDER BY version`)
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

// writeFixture copies a stopped database into one small file: the log folded
// in, then the copy compacted with 1 KiB pages (the schema alone is a page per
// table and index). The data directory under test is not changed.
func writeFixture(t *testing.T, path, out string) {
	t.Helper()
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	d.Close()
	in, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(f, in); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := sql.Open("sqlite", out)
	if err != nil {
		t.Fatal(err)
	}
	c.SetMaxOpenConns(1)
	// Nothing of the machine it was made on stays: the sessions, and the addresses of the stand-ins (ports) the
	// rows name. They are rewritten to fixed addresses that nothing listens on.
	for _, q := range fixtureCleanup {
		if _, err := c.Exec(q); err != nil && !strings.Contains(err.Error(), "no such table") && !strings.Contains(err.Error(), "no such column") {
			t.Fatal(q, err)
		}
	}
	for _, q := range []string{`PRAGMA journal_mode = DELETE`, `PRAGMA page_size = 1024`, `VACUUM`} {
		if _, err := c.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	checkFixture(t, out)
	report(t, "fixture written: %s", out)
}

// fixtureCleanup is run on a fixture's copy: login sessions and their unlocks, and the loopback ports of the
// stand-ins the rows name.
var fixtureCleanup = []string{
	`DELETE FROM sessions`,
	`DELETE FROM project_unlocks`,
	`UPDATE webhooks SET url = 'http://hooks.example.invalid/receive' WHERE url LIKE 'http://127.0.0.1:%'`,
	`UPDATE ai_providers SET base_url = 'http://models.example.invalid/v1' WHERE base_url LIKE 'http://127.0.0.1:%'`,
	`UPDATE git_remotes SET url = 'ssh://git@git.example.invalid/book.git' WHERE url LIKE 'ssh://git@127.0.0.1:%'`,
	`UPDATE git_remotes SET known_hosts = 'git.example.invalid ' || substr(known_hosts, instr(known_hosts, ' ') + 1) WHERE known_hosts LIKE '127.0.0.1:%'`,
	`UPDATE audit_log SET detail_json = json_set(detail_json, '$.host', 'git.example.invalid') WHERE json_valid(detail_json) AND json_extract(detail_json, '$.host') LIKE '127.0.0.1%'`,
}

// checkFixture refuses a fixture that holds anything of the machine it was made on, or that lacks a kind of data it
// is meant to carry (a table that came out empty would make every test of it vacuous).
func checkFixture(t *testing.T, out string) {
	t.Helper()
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for _, bad := range []string{os.TempDir(), "/private/", "/var/folders", "/Users/", "/home/", "localhost"} {
		if bad != "" && bytes.Contains(raw, []byte(bad)) {
			t.Errorf("the fixture holds %q, which belongs to the machine it was made on", bad)
		}
	}
	if home != "" && bytes.Contains(raw, []byte(home)) {
		t.Errorf("the fixture holds the home directory %q", home)
	}
	if loc := regexp.MustCompile(`127\.0\.0\.1:[0-9]+`).Find(raw); loc != nil {
		t.Errorf("the fixture holds a loopback address with a port: %s", loc)
	}
	if os.Getenv("SCRIOBH_UPGRADE_TAG") != "v1.0" {
		return
	}
	c, err := sql.Open("sqlite", out)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, table := range fixtureTables {
		var n int
		if err := c.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n == 0 {
			t.Errorf("the fixture's %s has %d rows (%v)", table, n, err)
		}
	}
}

// fixtureTables are the tables the v1.0 fixture must hold rows in: people, books and their trees, history, review,
// sharing, the bible, goals, keys, webhooks, a site, an import, a compile, an export, a provider, a remote, idempotency.
var fixtureTables = []string{"users", "projects", "project_members", "documents", "document_revisions", "comments", "suggestions", "share_links",
	"entities", "entity_aliases", "entity_relations", "entity_mentions", "timeline_events", "goals", "writing_sessions", "daily_stats", "api_keys", "webhooks",
	"site_options", "project_sources", "compile_presets", "exports", "ai_providers", "git_remotes", "git_pushes", "idempotency_keys", "compendiums"}

// without returns a copy of v with every field of the given name removed.
func without(v any, name string) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			if k != name {
				m[k] = without(e, name)
			}
		}
		return m
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = without(e, name)
		}
		return a
	}
	return v
}

// richWanted says whether a tag is new enough for the rich dataset: v0.5 and later, which have the operation table
// and the operations it uses (v0.1 to v0.4 are rehearsed with the small dataset only).
func richWanted(tag string) bool {
	var major, minor int
	if _, err := fmt.Sscanf(strings.TrimPrefix(tag, "v"), "%d.%d", &major, &minor); err != nil {
		return true // a tag that is not v<major>.<minor>: a development build; try everything
	}
	return major > 0 || minor >= 5
}
