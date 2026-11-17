package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
)

// The Phase 7 backup and restore rehearsal, through the real binary.
//
// One instance has every feature that can send something switched on, and something waiting to be sent: a Git
// remote (a token, a signing key, a trigger) that has been pushed to, a webhook with a delivery waiting, mail on with
// a Kindle edition in the outbox and a digest opt-in, a semantic index with embeddings (one of them stale), Pandoc
// enabled, a project with a passphrase, an imported EPUB with its source file, the presence preference on, and
// typography set. Every outside party is a stand-in on loopback that counts what reaches it, and each has been reached
// before the backup is taken, so "nothing was sent" below is a count that could have been more.
//
// The archive is restored into a directory of another path, and what it must do is checked row for row: every table
// but the ones a restore changes on purpose is identical, those changed are changed exactly as documented, and a
// server started on the copy sends nothing, in a quiet window longer than every worker's tick, and re-enables
// nothing. Then the failure paths: a restore without the secret key and a start with the original put back; and a
// restore in which the pauses of mail and of the semantic index fail, which must exit non-zero saying "NOT paused",
// still pause everything else, leave the markers, and keep both silent at every start until they can be paused.

const quietWindow = 9 * time.Second // longer than the mail dispatcher's tick (5 s) and the semantic worker's poll (2 s)

// tally counts what reaches a stand-in.
type tally struct {
	mu    sync.Mutex
	n     int
	paths []string
}

func (c *tally) add(p string) { c.mu.Lock(); c.n++; c.paths = append(c.paths, p); c.mu.Unlock() }
func (c *tally) count() int   { c.mu.Lock(); defer c.mu.Unlock(); return c.n }

// outside is every party a restored copy must not reach.
type outside struct {
	git, trigger, hook, embed, relay tally
	embedDown                        atomic.Bool
	pandocLog                        string
	trigURL, hookURL, embedURL       string
	relayPort, gitPort               int
}

func (o *outside) counts() map[string]int {
	n := 0
	if b, err := os.ReadFile(o.pandocLog); err == nil {
		n = strings.Count(string(b), "\n")
	}
	return map[string]int{"git": o.git.count(), "trigger": o.trigger.count(), "webhook": o.hook.count(), "embeddings": o.embed.count(), "mail relay": o.relay.count(), "pandoc runs": n}
}

func newOutside(t *testing.T) *outside {
	o := &outside{}
	srv := func(h http.HandlerFunc) *httptest.Server {
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		return s
	}
	o.trigURL = srv(func(w http.ResponseWriter, r *http.Request) { o.trigger.add(r.URL.Path); w.WriteHeader(204) }).URL
	o.hookURL = srv(func(w http.ResponseWriter, r *http.Request) { o.hook.add(r.URL.Path); w.WriteHeader(500) }).URL
	o.embedURL = srv(func(w http.ResponseWriter, r *http.Request) {
		o.embed.add(r.URL.Path)
		var in struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if o.embedDown.Load() || r.URL.Path != "/api/embed" {
			http.Error(w, "down", 500)
			return
		}
		out := make([][]float64, len(in.Input))
		for i, s := range in.Input {
			v := make([]float64, 8)
			for _, w := range strings.Fields(strings.ToLower(s)) {
				h := sha256.Sum256([]byte(w))
				v[int(h[0])%8]++
			}
			v[0] += 0.5 // never the zero vector
			out[i] = v
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}).URL
	// A listener that counts connections and closes them: a mail relay, and an https remote (the handshake that
	// arrives is the attempt).
	listen := func(c *tally) int {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				c.add("connect")
				conn.Close()
			}
		}()
		return ln.Addr().(*net.TCPAddr).Port
	}
	o.relayPort = listen(&o.relay)
	o.gitPort = listen(&o.git)
	// A stand-in Pandoc: it answers --version and records every run.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "pandoc")
	o.pandocLog = filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"$@\" >> \"" + o.pandocLog + "\"\ncase \"$1\" in --version) printf 'pandoc 3.2\\nFeatures: +server\\n'; exit 0;; esac\nprintf '# Chapter\\n\\nText.\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	o.pandocLog = filepath.Join(dir, "calls.log")
	t.Setenv("P7_PANDOC", bin)
	return o
}

func waitUntil(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// sqlQuery runs a read-only query against a stopped data directory's database and returns the rows as strings.
func sqlQuery(t *testing.T, dataDir, q string, args ...any) [][]string {
	t.Helper()
	d, err := db.Open(filepath.Join(dataDir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
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
			case []byte:
				row[i] = string(x)
			case nil:
				row[i] = ""
			default:
				row[i] = fmt.Sprint(x)
			}
		}
		out = append(out, row)
	}
	return out
}

func sqlOne(t *testing.T, dataDir, q string, args ...any) string {
	t.Helper()
	r := sqlQuery(t, dataDir, q, args...)
	if len(r) != 1 || len(r[0]) != 1 {
		t.Fatalf("%s: %v", q, r)
	}
	return r[0][0]
}

func sqlInt(t *testing.T, dataDir, q string, args ...any) int {
	t.Helper()
	var n int
	fmt.Sscan(sqlOne(t, dataDir, q, args...), &n)
	return n
}

// every feature, as the source of the rehearsal.
type everything struct {
	data, archive          string
	a                      *apiClient
	pA, pB, pC             string // the book, the protected project, the imported one
	epub                   []byte
	editionID              string
	staleDoc               string
	embedRowsBefore        int
	passphrase             string
	secrets                []string
	trigSecret             string
	hookSecret             string
	mailPass, token, hookP string
}

const p7Passphrase = "correct horse battery staple 7"

func p7Env(dir, key string) []string {
	e := []string{"SCRIOBH_DATA_DIR=" + dir, "SCRIOBH_WEBHOOK_ALLOW_PRIVATE=1", "SCRIOBH_GIT_ALLOW_PRIVATE=1", "SCRIOBH_MIRROR_DEBOUNCE_MS=50",
		"SCRIOBH_BACKUP_INTERVAL_HOURS=0", "SCRIOBH_WEBHOOK_DEBOUNCE_MS=50", "SCRIOBH_RATE_LIMIT=0"}
	if key != "" {
		e = append(e, "SCRIOBH_SECRET_KEY="+key)
	}
	return e
}

func p7Serve(t *testing.T, bin, dir, key string) *proc {
	t.Helper()
	extra := p7Env(dir, key)[1:] // start() sets the data directory itself
	return start(t, bin, dir, extra...)
}

// populate turns everything on and gets something waiting at every outside party.
func populate(t *testing.T, bin string, o *outside) *everything {
	t.Helper()
	x := &everything{data: t.TempDir(), mailPass: mailSecret, token: gitSecret, passphrase: p7Passphrase}
	p := p7Serve(t, bin, x.data, rehearsalKey1)
	x.a = newAPI(t, p.base, "dev-admin")
	a := x.a
	type idOut struct {
		Item struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
			Slug     string `json:"slug"`
		} `json:"item"`
	}
	mk := func(title string) string {
		var r idOut
		a.call("POST", "/api/v1/projects", map[string]any{"title": title, "kind": "novel"}, &r, 201)
		return r.Item.ID
	}
	x.pA, x.pB = mk("The Salt Road"), mk("Lantern Hours")
	doc := func(pid, title, body string) idOut {
		var r idOut
		a.call("POST", "/api/v1/projects/"+pid+"/docs", map[string]any{"title": title, "kind": "scene", "content": body}, &r, 201)
		return r
	}
	for i, body := range []string{
		"The quiet harbour at dusk. Nora counted the boats, seven then eight, while the tide pulled at the rope.",
		"A road of salt and gorse, and a lantern swinging above the marsh water.",
		"The ferry left at four and the gulls followed it past the quay and the harbour wall.",
	} {
		d := doc(x.pA, fmt.Sprintf("Scene %d", i+1), body)
		if i == 2 {
			x.staleDoc = d.Item.ID
		}
	}
	doc(x.pB, "Private chapter", "The lamplighter walked the wall each evening, and no one but the owner may read it.")

	// A Git remote with a token and a signing key, pushed to by hand (the stand-in refuses, which is a request all
	// the same), and a trigger that has been called once.
	a.call("POST", "/api/v1/projects/"+x.pA+"/git/signing", map[string]any{}, nil, 200)
	a.call("POST", "/api/v1/projects/"+x.pA+"/git/remotes", map[string]any{"name": "Mirror", "url": fmt.Sprintf("https://127.0.0.1:%d/owner/salt-road.git", o.gitPort),
		"auth_kind": "https_token", "token": x.token, "push_mode": "manual", "audience": "private", "sign_commits": true}, nil, 201)
	var trig struct {
		Secret string `json:"secret"`
	}
	a.call("PUT", "/api/v1/projects/"+x.pA+"/git/trigger", map[string]any{"url": o.trigURL + "/after-push", "enabled": true}, &trig, 200)
	x.trigSecret = trig.Secret
	waitUntil(t, "the mirror", 30*time.Second, func() bool {
		code, b := a.do("GET", "/api/v1/projects/"+x.pA+"/mirror", nil, "")
		return code == 200 && strings.Contains(string(b), `"in_sync":true`)
	})
	var push struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
	}
	a.call("POST", "/api/v1/projects/"+x.pA+"/git/push", map[string]any{}, &push, 202)
	waitUntil(t, "a push attempt to reach the Git stand-in", 30*time.Second, func() bool {
		if o.git.count() > 0 {
			return true
		}
		return false
	})
	a.do("POST", "/api/v1/projects/"+x.pA+"/git/trigger/test", map[string]any{}, "")
	waitUntil(t, "the trigger's test call", 30*time.Second, func() bool { return o.trigger.count() > 0 })

	// A webhook whose receiver is failing, so a delivery is waiting to be retried.
	var hook struct {
		Item struct {
			ID string `json:"id"`
		} `json:"item"`
		Secret string `json:"secret"`
	}
	a.call("POST", "/api/v1/projects/"+x.pA+"/webhooks", map[string]any{"name": "Notify", "url": o.hookURL + "/hooks/" + hookPathTok, "events": []string{"comment.created"}}, &hook, 201)
	x.hookSecret = hook.Secret
	a.call("POST", "/api/v1/projects/"+x.pA+"/comments", map[string]any{"body": "A comment that makes an event."}, nil, 201)
	waitUntil(t, "a webhook delivery attempt", 30*time.Second, func() bool { return o.hook.count() > 0 })

	// Mail on, a Kindle edition waiting, a digest opt-in.
	a.call("PUT", "/api/v1/admin/mail", map[string]any{"host": "127.0.0.1", "port": o.relayPort, "security": "starttls", "allow_private": true, "username": "relay-user", "password": x.mailPass,
		"from_address": "scriobh@example.org", "from_name": "Scriobh", "enabled": true}, nil, 200)
	a.call("PUT", "/api/v1/me/kindle", map[string]any{"address": "rehearsal@kindle.com"}, nil, 200)
	var kpv struct {
		Token string `json:"token"`
	}
	a.call("POST", "/api/v1/projects/"+x.pA+"/kindle/preview", map[string]any{}, &kpv, 200)
	a.call("POST", "/api/v1/projects/"+x.pA+"/kindle/send", map[string]any{"token": kpv.Token}, nil, 200)
	a.call("PUT", "/api/v1/me/mail/digest", map[string]any{"enabled": true, "frequency": "daily"}, nil, 200)
	waitUntil(t, "the dispatcher to try the relay", 40*time.Second, func() bool { return o.relay.count() > 0 })

	// Pandoc enabled, and run once.
	pandoc := os.Getenv("P7_PANDOC")
	a.call("PUT", "/api/v1/admin/pandoc", map[string]any{"enabled": true, "path": pandoc}, nil, 200)
	a.do("POST", "/api/v1/admin/pandoc/test", map[string]any{}, "")

	// A semantic index with embeddings; then the endpoint goes down and a document is edited, so one embedding is stale.
	var prov idOut
	a.call("POST", "/api/v1/ai/providers", map[string]any{"name": "Local embeddings", "kind": "ollama", "scope": "instance", "base_url": o.embedURL, "local": true, "default_model": "embed-a"}, &prov, 201)
	a.call("PUT", "/api/v1/projects/"+x.pA+"/ai/policy", map[string]any{"mode": "ask"}, nil, 200)
	var pre struct {
		ConsentToken string `json:"consent_token"`
	}
	a.call("POST", "/api/v1/projects/"+x.pA+"/search/semantic/preview", map[string]any{"provider_id": prov.Item.ID, "model": "embed-a"}, &pre, 200)
	a.call("POST", "/api/v1/projects/"+x.pA+"/search/semantic/enable", map[string]any{"provider_id": prov.Item.ID, "model": "embed-a", "consent_token": pre.ConsentToken}, nil, 200)
	waitUntil(t, "the index to catch up", 120*time.Second, func() bool {
		var st struct {
			State string `json:"state"`
		}
		code, b := a.do("GET", "/api/v1/projects/"+x.pA+"/search/semantic/status", nil, "")
		return code == 200 && json.Unmarshal(b, &st) == nil && st.State == "ready"
	})
	o.embedDown.Store(true)
	var cur idOut
	a.call("GET", "/api/v1/docs/"+x.staleDoc, nil, &cur, 200)
	a.call("PATCH", "/api/v1/docs/"+x.staleDoc, map[string]any{"base_revision": cur.Item.Revision, "content": "The ferry left at five, and the gulls did not follow it."}, nil, 200)

	// Presence, typography.
	a.call("PUT", "/api/v1/me/prefs", map[string]any{"items": map[string]string{"presence.share": "true"}}, nil, 200)
	a.call("PUT", "/api/v1/projects/"+x.pA+"/typography", map[string]any{"font": "serif", "size": 110}, nil, 200)
	a.call("PUT", "/api/v1/me/typography", map[string]any{"font": "sans"}, nil, 200)

	// An imported book with its source file.
	x.epub = tinyEpub(t)
	var pv struct {
		Token string `json:"token"`
	}
	a.call("POST", "/api/v1/imports/preview", map[string]any{"name": "salt.epub", "epub": x.epub}, &pv, 200)
	var ap struct {
		ProjectID string `json:"project_id"`
	}
	a.call("POST", "/api/v1/imports", map[string]any{"token": pv.Token, "name": "salt.epub", "epub": x.epub, "edition_label": "First edition"}, &ap, 201)
	x.pC = ap.ProjectID

	// The passphrase goes on last: it locks this session out of the project, which is the point.
	a.call("PUT", "/api/v1/projects/"+x.pB+"/passphrase", map[string]any{"passphrase": x.passphrase}, nil, 200)

	x.secrets = []string{aiSecret, x.token, x.mailPass, x.hookSecret, hookPathTok, rehearsalKey1, x.passphrase, x.trigSecret}
	p.stop(t)
	x.embedRowsBefore = sqlInt(t, x.data, `SELECT count(*) FROM embed_docs`)
	return x
}

func hasPause(out string, words ...string) []string {
	var missing []string
	for _, w := range words {
		if !strings.Contains(out, w) {
			missing = append(missing, w)
		}
	}
	return missing
}

// The restore of an instance with everything on.
func TestRestoreRehearsalEveryFeature(t *testing.T) {
	if testing.Short() {
		t.Skip("the restore rehearsal builds and runs the binary")
	}
	bin := rehearsalBinary(t)
	o := newOutside(t)
	x := populate(t, bin, o)

	// ---- what the source holds, so that the checks below could fail -------------------------------------
	src := x.data
	need := map[string]string{
		"a remote on":              `SELECT count(*) FROM git_remotes WHERE enabled = 1`,
		"a signing key":            `SELECT count(*) FROM git_signing_keys`,
		"a trigger on":             `SELECT count(*) FROM git_triggers WHERE enabled = 1`,
		"a webhook on":             `SELECT count(*) FROM webhooks WHERE enabled = 1`,
		"a delivery waiting":       `SELECT count(*) FROM webhook_deliveries`,
		"mail on":                  `SELECT count(*) FROM mail_config WHERE enabled = 1`,
		"a message in the outbox":  `SELECT count(*) FROM mail_outbox WHERE state IN ('pending','sending')`,
		"a digest opt-in":          `SELECT count(*) FROM mail_digest`,
		"a Kindle address":         `SELECT count(*) FROM kindle_addresses`,
		"an index on":              `SELECT count(*) FROM embed_projects WHERE enabled = 1`,
		"embeddings":               `SELECT count(*) FROM embed_chunks`,
		"Pandoc on":                `SELECT count(*) FROM settings WHERE key = 'pandoc.enabled' AND value = '1'`,
		"a passphrase":             `SELECT count(*) FROM project_gates`,
		"an edition with its file": `SELECT count(*) FROM editions WHERE has_file = 1`,
		"the presence preference":  `SELECT count(*) FROM user_prefs WHERE key = 'presence.share' AND value = 'true'`,
		"project typography":       `SELECT count(*) FROM project_typography`,
		"the person's typography":  `SELECT count(*) FROM user_typography`,
	}
	for what, q := range need {
		if sqlInt(t, src, q) < 1 {
			t.Errorf("the source has no %s (%s): the rehearsal would prove nothing", what, q)
		}
	}
	stale := sqlInt(t, src, `SELECT count(*) FROM embed_docs e JOIN documents d ON d.id = e.document_id WHERE d.content_hash <> e.content_hash`)
	if stale != 1 {
		t.Fatalf("expected exactly one stale embedding in the source, found %d", stale)
	}
	keptChunks := sqlInt(t, src, `SELECT count(*) FROM embed_chunks c JOIN embed_docs e ON e.document_id = c.document_id JOIN documents d ON d.id = e.document_id WHERE d.content_hash = e.content_hash`)
	pending := o.counts()
	report(t, "source: every feature on; stand-ins reached: %v", pending)
	for k, n := range pending {
		if n == 0 {
			t.Errorf("the %s stand-in was never reached before the backup; its silence later would mean nothing", k)
		}
	}

	archive := filepath.Join(t.TempDir(), "everything.zip")
	if out, errOut, code := runEnv(t, bin, p7Env(src, rehearsalKey1), "backup", "--out", archive); code != 0 {
		t.Fatalf("backup: %d %s %s", code, out, errOut)
	}
	x.archive = archive
	entries := readZip(t, archive)
	for _, s := range x.secrets {
		for name, b := range entries {
			if bytes.Contains(b, []byte(s)) {
				t.Errorf("a secret (%.6s...) is in the archive's %s", s, name)
			}
		}
	}
	srcDB := dbState(t, filepath.Join(src, "scriobh.db"))
	srcFiles := treeHashes(t, filepath.Join(src, "files"), ".tmp")
	editionFile := ""
	for name := range srcFiles {
		if strings.Contains(name, "/editions/") {
			editionFile = name
		}
	}
	if editionFile == "" {
		t.Fatalf("the source has no stored edition file among %d files", len(srcFiles))
	}
	srcSettings := sqlQuery(t, src, `SELECT key, value FROM settings WHERE key NOT LIKE 'pandoc.%' ORDER BY key`)

	// ---- restore on "another machine": a different path, the same key ----------------------------------------
	data2 := filepath.Join(t.TempDir(), "machine-b", "srv", "scriobh-data")
	out, errOut, code := runEnv(t, bin, p7Env(data2, rehearsalKey1), "restore", archive)
	if code != 0 {
		t.Fatalf("restore: %d\n%s\n%s", code, out, errOut)
	}
	if m := hasPause(out, "Git: 1 remote(s) and 1 trigger(s) were paused", "Webhooks: 1 webhook(s) were paused", "Mail: switched off", "Semantic search: 1 project(s)", "Pandoc: the bridge was switched on"); len(m) > 0 {
		t.Errorf("the restore output does not say %q:\n%s", m, out)
	}
	if !strings.Contains(out, "1 were dropped because their text differs") {
		t.Errorf("the restore output does not say one embedding was dropped:\n%s", out)
	}
	for _, f := range []string{"mail.restore-pause", "semantic.restore-pause"} {
		if _, err := os.Stat(filepath.Join(data2, f)); err == nil {
			t.Errorf("the restore left the marker %s after pausing", f)
		}
	}
	if _, err := os.Stat(filepath.Join(data2, "secret.key")); err == nil {
		t.Error("restore wrote a key file")
	}
	got := dbState(t, filepath.Join(data2, "scriobh.db"))
	changedOnPurpose := map[string]string{
		"audit_log": "the restore's line", "git_remotes": "paused", "git_triggers": "paused", "git_trigger_deliveries": "emptied", "webhooks": "paused",
		"webhook_deliveries": "emptied", "webhook_state": "parked", "mail_config": "paused", "mail_outbox": "emptied", "kindle_previews": "emptied",
		"embed_projects": "paused", "embed_docs": "stale dropped", "embed_chunks": "stale dropped", "settings": "Pandoc off", "events": "", "mail_digest": "",
	}
	var drift []string
	for name, w := range srcDB {
		if _, ok := changedOnPurpose[name]; ok {
			continue
		}
		if g := got[name]; g != w {
			drift = append(drift, fmt.Sprintf("%s: %d rows, %d before", name, g.Rows, w.Rows))
		}
	}
	sort.Strings(drift)
	for _, d := range drift {
		t.Errorf("a table a restore must not change was changed: %s", d)
	}
	// What a restore changes, exactly.
	chk := func(what string, want, got any) {
		t.Helper()
		if fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("%s: %v, want %v", what, got, want)
		}
	}
	chk("remotes still on", 0, sqlInt(t, data2, `SELECT count(*) FROM git_remotes WHERE enabled = 1`))
	chk("remotes marked as paused by the restore", sqlInt(t, src, `SELECT count(*) FROM git_remotes`), sqlInt(t, data2, `SELECT count(*) FROM git_remotes WHERE restore_paused = 1 AND state = 'blocked'`))
	chk("a remote's sealed token kept", sqlOne(t, src, `SELECT group_concat(secret_cipher) FROM git_remotes`), sqlOne(t, data2, `SELECT group_concat(secret_cipher) FROM git_remotes`))
	chk("triggers still on", 0, sqlInt(t, data2, `SELECT count(*) FROM git_triggers WHERE enabled = 1`))
	chk("trigger deliveries waiting", 0, sqlInt(t, data2, `SELECT count(*) FROM git_trigger_deliveries WHERE state = 'pending'`))
	chk("signing keys kept", sqlOne(t, src, `SELECT group_concat(project_id || public_key) FROM git_signing_keys`), sqlOne(t, data2, `SELECT group_concat(project_id || public_key) FROM git_signing_keys`))
	chk("webhooks still on", 0, sqlInt(t, data2, `SELECT count(*) FROM webhooks WHERE enabled = 1`))
	chk("webhook deliveries waiting", 0, sqlInt(t, data2, `SELECT count(*) FROM webhook_deliveries`))
	chk("webhook feeder parked", 1, sqlInt(t, data2, `SELECT count(*) FROM webhook_state WHERE cursor < 0`))
	chk("mail on", 0, sqlInt(t, data2, `SELECT count(*) FROM mail_config WHERE enabled = 1`))
	chk("mail marked as paused by the restore", 1, sqlInt(t, data2, `SELECT count(*) FROM mail_config WHERE restore_paused = 1`))
	chk("the relay's sealed password kept", sqlOne(t, src, `SELECT password_sealed FROM mail_config`), sqlOne(t, data2, `SELECT password_sealed FROM mail_config`))
	chk("messages waiting", 0, sqlInt(t, data2, `SELECT count(*) FROM mail_outbox WHERE state IN ('pending','sending')`))
	chk("Kindle previews waiting", 0, sqlInt(t, data2, `SELECT count(*) FROM kindle_previews`))
	chk("digest opt-ins kept", sqlInt(t, src, `SELECT count(*) FROM mail_digest`), sqlInt(t, data2, `SELECT count(*) FROM mail_digest`))
	chk("Kindle addresses kept", sqlOne(t, src, `SELECT group_concat(user_id || address) FROM kindle_addresses`), sqlOne(t, data2, `SELECT group_concat(user_id || address) FROM kindle_addresses`))
	chk("indexes still on", 0, sqlInt(t, data2, `SELECT count(*) FROM embed_projects WHERE enabled = 1`))
	chk("the index's pause reason", "restore", sqlOne(t, data2, `SELECT paused_reason FROM embed_projects`))
	chk("embedded documents kept", x.embedRowsBefore-stale, sqlInt(t, data2, `SELECT count(*) FROM embed_docs`))
	chk("the stale document's embedding dropped", 0, sqlInt(t, data2, `SELECT count(*) FROM embed_docs WHERE document_id = ?`, x.staleDoc)+sqlInt(t, data2, `SELECT count(*) FROM embed_chunks WHERE document_id = ?`, x.staleDoc))
	chk("the other embeddings' chunks kept", keptChunks, sqlInt(t, data2, `SELECT count(*) FROM embed_chunks`))
	chk("chunks with no document row", 0, sqlInt(t, data2, `SELECT count(*) FROM embed_chunks WHERE document_id NOT IN (SELECT document_id FROM embed_docs)`))
	chk("Pandoc switched off", "0", sqlOne(t, data2, `SELECT value FROM settings WHERE key = 'pandoc.enabled'`))
	chk("Pandoc's path kept", os.Getenv("P7_PANDOC"), sqlOne(t, data2, `SELECT value FROM settings WHERE key = 'pandoc.path'`))
	chk("the restore's mark for Pandoc", "1", sqlOne(t, data2, `SELECT value FROM settings WHERE key = 'pandoc.restore_paused'`))
	if g := sqlQuery(t, data2, `SELECT key, value FROM settings WHERE key NOT LIKE 'pandoc.%' ORDER BY key`); fmt.Sprint(g) != fmt.Sprint(srcSettings) {
		t.Errorf("settings other than Pandoc's changed:\n%v\n%v", srcSettings, g)
	}
	// The passphrase, the edition, the preference and typography are not touched.
	chk("the passphrase gate", sqlOne(t, src, `SELECT group_concat(project_id || pass_hash || epoch || idle_minutes || allow_keys) FROM project_gates`), sqlOne(t, data2, `SELECT group_concat(project_id || pass_hash || epoch || idle_minutes || allow_keys) FROM project_gates`))
	chk("the presence preference", "true", sqlOne(t, data2, `SELECT value FROM user_prefs WHERE key = 'presence.share'`))
	if d := diffMaps(srcFiles, treeHashes(t, filepath.Join(data2, "files"), ".tmp")); len(d) > 0 {
		t.Errorf("stored files differ after restore (the edition's source file is among them): %v", d)
	}
	// The copy holds nothing of the machine it came from.
	rawDB, err := os.ReadFile(filepath.Join(data2, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{src} {
		if bytes.Contains(rawDB, []byte(s)) {
			t.Errorf("the restored database holds the source's path %q", s)
		}
	}
	report(t, "restored on another path: %d tables; the only differences are the documented pauses (git remotes, triggers, webhooks, mail, semantic index, Pandoc) and their emptied queues; the passphrase, edition, digest opt-in, presence preference, typography and signing key are as they were", len(srcDB))

	// ---- the first start: nothing sends, nothing is switched on ------------------------------------------------
	base := o.counts()
	s2 := p7Serve(t, bin, data2, rehearsalKey1)
	b := newAPI(t, s2.base, "dev-admin")
	time.Sleep(quietWindow)
	var mc struct {
		Enabled         bool `json:"enabled"`
		PausedByRestore bool `json:"paused_by_restore"`
	}
	b.call("GET", "/api/v1/admin/mail", nil, &mc, 200)
	if mc.Enabled || !mc.PausedByRestore {
		t.Errorf("mail at the first start: %+v", mc)
	}
	var pc struct {
		Enabled bool `json:"enabled"`
	}
	b.call("GET", "/api/v1/admin/pandoc", nil, &pc, 200)
	if pc.Enabled {
		t.Error("Pandoc is on at the first start")
	}
	var ss struct {
		State string `json:"state"`
	}
	b.call("GET", "/api/v1/projects/"+x.pA+"/search/semantic/status", nil, &ss, 200)
	if ss.State == "ready" || ss.State == "indexing" {
		t.Errorf("the semantic index at the first start: %q", ss.State)
	}
	var rem struct {
		Item struct {
			Enabled bool `json:"enabled"`
		} `json:"item"`
	}
	b.call("GET", "/api/v1/projects/"+x.pA+"/git/remote", nil, &rem, 200)
	if rem.Item.Enabled {
		t.Error("the Git remote is on at the first start")
	}
	// A project with a passphrase is locked in a new session, and the passphrase still opens it.
	if code, _ := b.do("GET", "/api/v1/projects/"+x.pB+"/docs", nil, ""); code == 200 {
		t.Errorf("a protected project was served to a session that had not unlocked it: %d", code)
	}
	if code, body := b.do("POST", "/api/v1/projects/"+x.pB+"/unlock", map[string]any{"passphrase": "not it"}, ""); code == 200 {
		t.Errorf("a wrong passphrase unlocked the project: %s", body)
	}
	b.call("POST", "/api/v1/projects/"+x.pB+"/unlock", map[string]any{"passphrase": x.passphrase}, nil, 200)
	b.call("GET", "/api/v1/projects/"+x.pB+"/docs", nil, nil, 200)
	// The imported edition's source file is served byte for byte.
	var el struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	b.call("GET", "/api/v1/projects/"+x.pC+"/editions", nil, &el, 200)
	if len(el.Items) != 1 {
		t.Fatalf("editions after the restore: %+v", el)
	}
	code2, dl := b.do("GET", "/api/v1/projects/"+x.pC+"/editions/"+el.Items[0].ID+"/download", nil, "")
	if code2 != 200 || sha(dl) != sha(x.epub) {
		t.Errorf("the edition's source file after the restore: %d, sha %.12s, want %.12s", code2, sha(dl), sha(x.epub))
	}
	// Presence: the preference is on, and no one is shown as here, because presence lives in memory.
	var pr struct {
		Items []json.RawMessage `json:"items"`
	}
	b.call("GET", "/api/v1/projects/"+x.pA+"/presence", nil, &pr, 200)
	if len(pr.Items) != 0 {
		t.Errorf("presence shows %d people at the first start", len(pr.Items))
	}
	time.Sleep(2 * time.Second)
	s2.stop(t)
	if after := o.counts(); fmt.Sprint(after) != fmt.Sprint(base) {
		t.Errorf("a server started on the restored copy reached an outside party:\n  before %v\n  after  %v", base, after)
	}
	for _, q := range []string{`SELECT count(*) FROM git_remotes WHERE enabled = 1`, `SELECT count(*) FROM git_triggers WHERE enabled = 1`, `SELECT count(*) FROM webhooks WHERE enabled = 1`,
		`SELECT count(*) FROM mail_config WHERE enabled = 1`, `SELECT count(*) FROM embed_projects WHERE enabled = 1`, `SELECT count(*) FROM settings WHERE key = 'pandoc.enabled' AND value = '1'`,
		`SELECT count(*) FROM mail_outbox`, `SELECT count(*) FROM webhook_deliveries`} {
		if n := sqlInt(t, data2, q); n != 0 {
			t.Errorf("after the first start %s = %d", q, n)
		}
	}
	report(t, "first start on the copy: %s; nothing was switched on or queued; protected project locked and opened by its passphrase; edition file served unchanged (sha256 %.12s)", fmt.Sprint(o.counts()), sha(x.epub))
	if st := s2.logs.String(); true {
		for _, s := range x.secrets {
			if strings.Contains(st, s) {
				t.Errorf("a secret (%.6s...) reached the restored server's log", s)
			}
		}
	}

	// ---- a restore without the secret key, then the original put back at the start -----------------------------
	data3 := filepath.Join(t.TempDir(), "no-key")
	out, errOut, code = runEnv(t, bin, p7Env(data3, ""), "restore", archive)
	if code != 0 {
		t.Fatalf("restore without a key: %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "secret key: none found") || !strings.Contains(out, "Git remotes: no secret key was found") || !strings.Contains(out, "Mail: switched off") {
		t.Errorf("restore without a key:\n%s", out)
	}
	for _, q := range []string{`SELECT count(*) FROM git_remotes WHERE enabled = 1`, `SELECT count(*) FROM git_triggers WHERE enabled = 1`, `SELECT count(*) FROM webhooks WHERE enabled = 1`,
		`SELECT count(*) FROM mail_config WHERE enabled = 1`, `SELECT count(*) FROM embed_projects WHERE enabled = 1`, `SELECT count(*) FROM settings WHERE key = 'pandoc.enabled' AND value = '1'`, `SELECT count(*) FROM mail_outbox`} {
		if n := sqlInt(t, data3, q); n != 0 {
			t.Errorf("a restore without a key left %s = %d", q, n)
		}
	}
	base = o.counts()
	s3 := p7Serve(t, bin, data3, rehearsalKey1) // the original key is put back: every credential opens again
	time.Sleep(quietWindow)
	s3.stop(t)
	if sqlInt(t, data3, `SELECT count(*) FROM git_remotes WHERE enabled = 1`) != 0 {
		t.Error("starting with the original key put back switched a restored Git remote on again")
	}
	if after := o.counts(); fmt.Sprint(after) != fmt.Sprint(base) {
		t.Errorf("starting with the original key put back reached an outside party:\n  before %v\n  after  %v", base, after)
	}
	report(t, "restore without the key, start with the original put back: remotes stay off, nothing reached")

	// ---- the pauses of mail and of the semantic index fail -----------------------------------------------------
	// A copy of the source whose mail and index rows refuse to be changed: the restore of it cannot pause either.
	broken := t.TempDir()
	if err := copyTree(src, broken); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(broken, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TRIGGER no_mail_pause BEFORE UPDATE ON mail_config BEGIN SELECT RAISE(ABORT, 'database is locked'); END`,
		`CREATE TRIGGER no_index_pause BEFORE UPDATE ON embed_projects BEGIN SELECT RAISE(ABORT, 'database is locked'); END`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	brokenZip := filepath.Join(t.TempDir(), "broken.zip")
	if out, errOut, code := runEnv(t, bin, p7Env(broken, rehearsalKey1), "backup", "--out", brokenZip); code != 0 {
		t.Fatalf("backup of the copy: %d %s %s", code, out, errOut)
	}
	data4 := filepath.Join(t.TempDir(), "failing")
	out, errOut, code = runEnv(t, bin, p7Env(data4, rehearsalKey1), "restore", brokenZip)
	if code == 0 {
		t.Fatalf("a restore that could not pause mail and the index exited 0:\n%s", out)
	}
	if m := hasPause(errOut, "restore failed", "mail is NOT paused", "the semantic indexes are NOT paused", "mail.restore-pause", "semantic.restore-pause", "UPDATE mail_config SET enabled = 0", "UPDATE embed_projects SET enabled = 0"); len(m) > 0 {
		t.Errorf("stderr lacks %q:\n%s", m, errOut)
	}
	if !strings.Contains(out, "the data was restored, but:") {
		t.Errorf("stdout: %s", out)
	}
	// The failure of two steps did not stop the others.
	for _, q := range []string{`SELECT count(*) FROM git_remotes WHERE enabled = 1`, `SELECT count(*) FROM git_triggers WHERE enabled = 1`, `SELECT count(*) FROM webhooks WHERE enabled = 1`,
		`SELECT count(*) FROM settings WHERE key = 'pandoc.enabled' AND value = '1'`, `SELECT count(*) FROM webhook_deliveries`} {
		if n := sqlInt(t, data4, q); n != 0 {
			t.Errorf("a failed mail and index pause stopped the others: %s = %d", q, n)
		}
	}
	for _, f := range []string{"mail.restore-pause", "semantic.restore-pause", backup.MarkerName} {
		if _, err := os.Stat(filepath.Join(data4, f)); err != nil {
			t.Errorf("marker %s is gone although the pause failed: %v", f, err)
		}
	}
	// Every sender is named under NOT paused, not only the two that failed: the command cannot say which were reached.
	if m := hasPause(errOut, "Git remotes: NOT paused", "Git triggers: NOT paused", "Webhooks: NOT paused", "The Pandoc bridge: NOT paused", "Mail: NOT paused", "The semantic indexes: NOT paused"); len(m) > 0 {
		t.Errorf("stderr does not list every sender as NOT paused with its statement, lacking %q:\n%s", m, errOut)
	}
	// Started like this, the server refuses to run: the marker is honoured before anything starts, and it fails again.
	base = o.counts()
	if code, serr, started := tryServe(t, bin, data4, rehearsalKey1); started || code == 0 || !strings.Contains(serr, "refusing to start") || !strings.Contains(serr, "NOT paused") {
		t.Errorf("a start with the pauses failed: started %v, exit %d:\n%s", started, code, serr)
	}
	if after := o.counts(); fmt.Sprint(after) != fmt.Sprint(base) {
		t.Errorf("a start with the pause failed reached an outside party:\n  before %v\n  after  %v", base, after)
	}
	// `restore --finish` says the same while the cause stands.
	if _, ferr, fcode := runEnv(t, bin, p7Env(data4, rehearsalKey1), "restore", "--finish"); fcode == 0 || !strings.Contains(ferr, "NOT paused") {
		t.Errorf("restore --finish with the cause standing: exit %d\n%s", fcode, ferr)
	}
	// The cause is fixed (the triggers go); --finish makes the pauses and removes the marker, and the start runs.
	d, err = db.Open(filepath.Join(data4, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DROP TRIGGER no_mail_pause`, `DROP TRIGGER no_index_pause`} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()
	if fout, ferr, fcode := runEnv(t, bin, p7Env(data4, rehearsalKey1), "restore", "--finish"); fcode != 0 {
		t.Fatalf("restore --finish: %d\n%s\n%s", fcode, fout, ferr)
	}
	for _, f := range []string{"mail.restore-pause", "semantic.restore-pause", backup.MarkerName} {
		if _, err := os.Stat(filepath.Join(data4, f)); err == nil {
			t.Errorf("marker %s is still there after --finish", f)
		}
	}
	s5 := p7Serve(t, bin, data4, rehearsalKey1)
	time.Sleep(quietWindow)
	s5.stop(t)
	if sqlInt(t, data4, `SELECT count(*) FROM mail_config WHERE enabled = 1`) != 0 || sqlInt(t, data4, `SELECT count(*) FROM embed_projects WHERE enabled = 1`) != 0 {
		t.Error("after --finish mail and the index are not paused")
	}
	if after := o.counts(); fmt.Sprint(after) != fmt.Sprint(base) {
		t.Errorf("the start after --finish reached an outside party:\n  before %v\n  after  %v", base, after)
	}
	report(t, "a restore whose mail and index pauses fail: exit non-zero, every sender listed as NOT paused with its statement, every other pause made, the server refuses to start while the marker stands, --finish repeats the pauses and clears it")

	// ---- killed after the install, before any hook or step of the live phase ---------------------------------------------
	// A process that dies (kill -9, a power cut) after the files are installed. The first install hook ends its goroutine
	// without returning, which stops the restore exactly there. With the pauses made in the staged database the copy is
	// off already; with a staged pause failing, the marker is in place and the server refuses to start on it.
	killed := func(zip, dir string) {
		saved := backup.PostInstall
		backup.PostInstall = append([]func(string) error{func(string) error { runtime.Goexit(); return nil }}, saved...)
		defer func() { backup.PostInstall = saved }()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = backup.Restore(context.Background(), zip, dir, backup.RestoreOptions{SecretKey: []byte(rehearsalKey1)})
		}()
		<-done
	}
	allOff := func(dir string) {
		t.Helper()
		for _, q := range []string{`SELECT count(*) FROM git_remotes WHERE enabled = 1`, `SELECT count(*) FROM git_triggers WHERE enabled = 1`, `SELECT count(*) FROM webhooks WHERE enabled = 1`,
			`SELECT count(*) FROM settings WHERE key = 'pandoc.enabled' AND value = '1'`, `SELECT count(*) FROM webhook_deliveries`, `SELECT count(*) FROM mail_config WHERE enabled = 1`,
			`SELECT count(*) FROM mail_outbox`, `SELECT count(*) FROM embed_projects WHERE enabled = 1`} {
			if n := sqlInt(t, dir, q); n != 0 {
				t.Errorf("after a restore killed after the install, %s = %d", q, n)
			}
		}
	}
	data6 := filepath.Join(t.TempDir(), "killed")
	killed(archive, data6)
	allOff(data6)
	base = o.counts()
	s6 := p7Serve(t, bin, data6, rehearsalKey1)
	time.Sleep(quietWindow)
	s6.stop(t)
	if after := o.counts(); fmt.Sprint(after) != fmt.Sprint(base) {
		t.Errorf("a server started on a restore killed after the install reached an outside party:\n  before %v\n  after  %v", base, after)
	}
	allOff(data6)

	data7 := filepath.Join(t.TempDir(), "killed-broken")
	killed(brokenZip, data7)
	if _, err := os.Stat(filepath.Join(data7, backup.MarkerName)); err != nil {
		t.Fatalf("a restore killed with its pauses not all made left no marker: %v", err)
	}
	for _, q := range []string{`SELECT count(*) FROM git_remotes WHERE enabled = 1`, `SELECT count(*) FROM git_triggers WHERE enabled = 1`, `SELECT count(*) FROM webhooks WHERE enabled = 1`,
		`SELECT count(*) FROM settings WHERE key = 'pandoc.enabled' AND value = '1'`} {
		if n := sqlInt(t, data7, q); n != 0 {
			t.Errorf("a restore killed after the install left %s = %d although those pauses were made in the staging", q, n)
		}
	}
	base = o.counts()
	if code, serr, started := tryServe(t, bin, data7, rehearsalKey1); started || code == 0 || !strings.Contains(serr, "refusing to start") {
		t.Errorf("a start on a restore killed with a pause outstanding: started %v, exit %d:\n%s", started, code, serr)
	}
	// A second restore over the interrupted one (the good archive, --force) ends clean: nothing on, no marker left.
	if out, errOut, code := runEnv(t, bin, p7Env(data7, rehearsalKey1), "restore", "--force", archive); code != 0 {
		t.Fatalf("a second restore over the interrupted one: %d\n%s\n%s", code, out, errOut)
	}
	for _, f := range []string{"mail.restore-pause", "semantic.restore-pause", backup.MarkerName} {
		if _, err := os.Stat(filepath.Join(data7, f)); err == nil {
			t.Errorf("marker %s left after a clean second restore", f)
		}
	}
	allOff(data7)
	s7 := p7Serve(t, bin, data7, rehearsalKey1)
	time.Sleep(quietWindow)
	s7.stop(t)
	if after := o.counts(); fmt.Sprint(after) != fmt.Sprint(base) {
		t.Errorf("a server started after the second restore reached an outside party:\n  before %v\n  after  %v", base, after)
	}
	report(t, "killed after the install: with the pauses made in the staging the copy is off and silent; with one outstanding the marker makes the server refuse to start; a second restore over it ends clean")
}

// tryServe starts the server on dir and says whether it ran (it printed its LISTEN line) or refused (it exited without
// one), with its exit code and standard error. A server that runs is stopped.
func tryServe(t *testing.T, bin, dir, key string) (code int, stderr string, started bool) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "SCRIOBH_LISTEN=127.0.0.1:0", "SCRIOBH_OIDC_STUB=1"}, p7Env(dir, key)...)
	var se syncBuf
	cmd.Stderr = &se
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	listening := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "LISTEN ") {
				listening <- true
				break
			}
		}
		io.Copy(io.Discard, out)
		select {
		case listening <- false:
		default:
		}
	}()
	up := <-listening // true at LISTEN; false when the process ended its output without one
	if up {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	werr := make(chan error, 1)
	go func() { werr <- cmd.Wait() }()
	var err2 error
	select {
	case err2 = <-werr:
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		err2 = <-werr
	}
	if ee, ok := err2.(*exec.ExitError); ok && !up {
		return ee.ExitCode(), se.String(), up
	}
	return 0, se.String(), up
}

// copyTree copies a data directory (the database through SQLite, the rest as files).
func copyTree(from, to string) error {
	d, err := db.Open(filepath.Join(from, "scriobh.db"))
	if err != nil {
		return err
	}
	if _, err := d.Writer().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	d.Close()
	return filepath.WalkDir(from, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o700)
		}
		if strings.HasSuffix(p, "-wal") || strings.HasSuffix(p, "-shm") || strings.Contains(rel, "backups"+string(filepath.Separator)) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o600)
	})
}

// The steps a restore runs do not depend on each other: run in the opposite order on the same copy, they leave the
// same database. (The restore itself runs them in the order the packages register them, which is the order of
// package initialisation and not a thing anyone chose.)
func TestRestoreStepsLeaveTheSameDatabaseInAnyOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("the restore rehearsal builds and runs the binary")
	}
	bin := rehearsalBinary(t)
	o := newOutside(t)
	x := populate(t, bin, o)
	if len(backup.PostRestore) < 6 {
		t.Fatalf("only %d post-restore steps are registered in this binary", len(backup.PostRestore))
	}
	// The steps stamp their changes with the time; a fixed clock makes two runs comparable.
	restoreClock := db.SetClock(func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) })
	defer restoreClock()
	run := func(reverse bool) map[string]tableState {
		dir := t.TempDir()
		if err := copyTree(x.data, dir); err != nil {
			t.Fatal(err)
		}
		d, err := db.Open(filepath.Join(dir, "scriobh.db"))
		if err != nil {
			t.Fatal(err)
		}
		steps := append([]func(context.Context, *db.DB, []byte) (string, error)(nil), backup.PostRestore...)
		if reverse {
			for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
				steps[i], steps[j] = steps[j], steps[i]
			}
		}
		for _, s := range steps {
			if _, err := s(context.Background(), d, []byte(rehearsalKey1)); err != nil {
				t.Fatal(err)
			}
		}
		d.Close()
		st := dbState(t, filepath.Join(dir, "scriobh.db"))
		delete(st, "audit_log")
		return st
	}
	fwd, rev := run(false), run(true)
	for name, w := range fwd {
		if g := rev[name]; g != w {
			t.Errorf("table %s differs when the steps run in the opposite order: %d rows against %d", name, g.Rows, w.Rows)
		}
	}
	// Every pause is idempotent too: a second run of every step changes nothing.
	dir := t.TempDir()
	if err := copyTree(x.data, dir); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range backup.PostRestore {
		s(context.Background(), d, []byte(rehearsalKey1))
	}
	d.Close()
	once := dbState(t, filepath.Join(dir, "scriobh.db"))
	d, _ = db.Open(filepath.Join(dir, "scriobh.db"))
	for _, s := range backup.PostRestore {
		s(context.Background(), d, []byte(rehearsalKey1))
	}
	d.Close()
	twice := dbState(t, filepath.Join(dir, "scriobh.db"))
	delete(once, "audit_log")
	delete(twice, "audit_log")
	for name, w := range once {
		if name == "embed_projects" {
			continue // each pause bumps the index's generation, on purpose: it ends any work in flight
		}
		if g := twice[name]; g != w {
			t.Errorf("table %s changed when the steps ran a second time", name)
		}
	}
	report(t, "the %d post-restore steps leave the same database in the opposite order and when run twice (the index's generation counter apart)", len(backup.PostRestore))
}
