package gitmirror_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

const token = "ghp_ThisIsAVerySecretTokenValue0123456789abcdef"

func TestRemoteCRUDAndWhoMayDoIt(t *testing.T) {
	h := newHarness(t)
	in := gitmirror.CreateIn{Audience: "private", ProjectID: h.pid, Name: "Public mirror", URL: "https://forge.example.org/mairead/salt-road.git", Token: token, Username: "mairead", Preset: "forgejo"}

	if _, err := h.call(h.reader, "git.remote.create", in); h.code(err) != ops.CodeForbidden {
		t.Errorf("reader create: %v", err)
	}
	if _, err := h.call(h.outside, "git.remote.create", in); h.code(err) != ops.CodeNotFound {
		t.Errorf("stranger create: %v", err)
	}
	if _, err := h.call(h.outside, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid}); h.code(err) != ops.CodeNotFound {
		t.Errorf("stranger list: %v", err)
	}
	if _, err := h.call(h.reader, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid}); h.code(err) != ops.CodeForbidden {
		t.Errorf("reader list: %v", err)
	}
	if _, err := h.call(h.editor, "git.remote.create", in); h.code(err) != ops.CodeForbidden {
		t.Errorf("editor create: %v", err)
	}
	rem := h.must(h.owner, "git.remote.create", in).(gitmirror.RemoteItem).Item
	if rem.AuthKind != "https_token" || !rem.HasSecret || rem.Host != "forge.example.org" || rem.Branch != "main" || rem.PushMode != "on_save" || !rem.IncludeNotes || rem.Preset != "forgejo" {
		t.Fatalf("remote = %+v", rem)
	}
	if rem.Author != "Scríobh <noreply@scriobh>" {
		t.Errorf("author = %q: the default identity names no one", rem.Author)
	}
	if len(rem.Setup) != 0 || rem.Status.State != "new" {
		t.Errorf("setup %v state %s", rem.Setup, rem.Status.State)
	}
	// A project may have several remotes (F-362), each with a name of its own.
	in2 := in
	in2.Name = "Second"
	second := h.must(h.owner, "git.remote.create", in2).(gitmirror.RemoteItem).Item
	if _, err := h.call(h.owner, "git.remote.create", in2); h.code(err) != ops.CodeConflict {
		t.Errorf("a second remote with the same name: %v", err)
	}
	list := h.must(h.owner, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid}).(gitmirror.RemoteList)
	if len(list.Items) != 2 || !list.Enabled {
		t.Fatalf("list = %+v", list)
	}
	if _, err := h.call(h.owner, "git.remote.get", gitmirror.RemoteQuery{ProjectID: h.pid}); h.code(err) != ops.CodeInvalid {
		t.Errorf("with two remotes an op that names none must say which: %v", err)
	}
	h.must(h.owner, "git.remote.delete", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: second.ID})

	// Update: only what is sent changes; the token is write-only.
	upd := h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Branch: ptr("backup"), PushMode: ptr("interval"), IntervalMinutes: ptr(15),
		IncludeNotes: ptr(false), AuthorName: ptr("Máire"), AuthorEmail: ptr("maire@example.org"), PathPrefix: ptr("/book/")}).(gitmirror.RemoteItem).Item
	if upd.Branch != "backup" || upd.PushMode != "interval" || upd.IntervalMinutes != 15 || upd.IncludeNotes || upd.PathPrefix != "book" ||
		upd.Author != "Máire <maire@example.org>" || upd.URL != rem.URL || !upd.HasSecret {
		t.Fatalf("updated = %+v", upd)
	}
	for _, bad := range []gitmirror.UpdateIn{
		{ProjectID: h.pid, Branch: ptr("a b")}, {ProjectID: h.pid, PathPrefix: ptr("../x")}, {ProjectID: h.pid, PushMode: ptr("hourly")},
		{ProjectID: h.pid, IntervalMinutes: ptr(1)}, {ProjectID: h.pid, URL: ptr("ssh://git@x.org/y.git")},
	} {
		if _, err := h.call(h.owner, "git.remote.update", bad); h.code(err) != ops.CodeInvalid {
			t.Errorf("update %+v: %v", bad, err)
		}
	}
	// Changing the address resets what is known about the remote.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr("https://forge.example.org/mairead/other.git")})
	if r := h.remote(); r.URL != "https://forge.example.org/mairead/other.git" || r.Status.State != "new" {
		t.Errorf("after a new address: %+v", r)
	}
	// Another project's remote id is not found.
	if _, err := h.call(h.owner, "git.remote.get", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: "grm_nope"}); h.code(err) != ops.CodeNotFound {
		t.Errorf("unknown remote: %v", err)
	}
	if _, err := h.call(h.owner, "git.remote.delete", gitmirror.RemoteQuery{ProjectID: h.pid}); h.code(err) != ops.CodeInvalid {
		t.Errorf("delete without naming the remote: %v", err)
	}
	if _, err := h.call(h.editor, "git.remote.delete", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: rem.ID}); h.code(err) != ops.CodeForbidden {
		t.Errorf("editor delete: %v", err)
	}
	h.must(h.owner, "git.remote.delete", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: rem.ID})
	if _, err := h.call(h.owner, "git.remote.get", gitmirror.RemoteQuery{ProjectID: h.pid}); h.code(err) != ops.CodeConflict {
		t.Errorf("a project with no remote: %v", err)
	}
	for _, op := range []string{"git.push", "git.remote.test", "git.remote.hostkey"} {
		var in any = gitmirror.RemoteBody{ProjectID: h.pid}
		if _, err := h.call(h.owner, op, in); h.code(err) != ops.CodeConflict {
			t.Errorf("%s with no remote: %v", op, err)
		}
	}
	// Presets are static data.
	ps := h.must(h.reader, "git.presets", struct{}{}).(gitmirror.PresetList)
	if len(ps.Items) != 3 || ps.Items[1].ID != "github" || ps.Items[1].DefaultHost != "github.com" {
		t.Errorf("presets = %+v", ps.Items)
	}
	for _, p := range ps.Items {
		if strings.Contains(strings.ToLower(p.Notes+p.SSHURL), "sha256:") {
			t.Errorf("preset %s carries a host key fingerprint it cannot verify", p.ID)
		}
	}
}

func TestAddressesThatAreRefused(t *testing.T) {
	for _, u := range []string{
		"http://forge.example.org/o/r.git",      // plain http would send a token in the clear
		"git://forge.example.org/o/r.git",       // unauthenticated
		"file:///etc",                           // a directory of the server
		"/var/lib/repo.git",                     // a local path
		"https://user:pw@forge.example.org/o/r", // credentials in the address
		"https://user@forge.example.org/o/r",
		"ssh://git:pw@forge.example.org/o/r.git",
		"ssh://-oProxyCommand=x/o/r.git", // an option, not a host
		"ssh://git@forge example.org/o/r.git",
		"https://forge.example.org/", // names no repository
		"",
		"ftp://forge.example.org/o/r.git",
	} {
		if _, _, err := gitmirror.ParseURL(u, false); err == nil {
			t.Errorf("%q was accepted", u)
		}
	}
	for u, want := range map[string]string{
		"ssh://git@github.com/o/r.git":         "github.com:22",
		"git@github.com:o/r.git":               "github.com:22",
		"ssh://git@forge.example.org:2222/o/r": "forge.example.org:2222",
		"https://forge.example.org/o/r.git":    "forge.example.org:443",
		"ssh://git@[2001:db8::1]:2200/o/r.git": "2001:db8::1:2200",
	} {
		host, port, err := gitmirror.ParseURL(u, false)
		if err != nil {
			t.Errorf("%q: %v", u, err)
			continue
		}
		if got := host + ":" + itoa(port); got != want {
			t.Errorf("%q = %s, want %s", u, got, want)
		}
	}
	if _, _, err := gitmirror.ParseURL("file:///tmp/x.git", true); err != nil {
		t.Errorf("the test seam: %v", err)
	}
	// Through the op, with the seam off, a file address is a 422.
	h := newHarness(t, func(o *gitmirror.Options) { o.AllowFile = false })
	if _, err := h.call(h.owner, "git.remote.create", gitmirror.CreateIn{Audience: "private", ProjectID: h.pid, Name: "x", URL: "file:///etc"}); h.code(err) != ops.CodeInvalid {
		t.Errorf("file address via the op: %v", err)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// anywhere reports the first place a needle appears: the log, the database
// (every column but the sealed secret), the audit log, the data directory.
func (h *harness) leaks(needles ...string) []string {
	h.t.Helper()
	var found []string
	check := func(where, text string) {
		for _, n := range needles {
			if n != "" && strings.Contains(text, n) {
				found = append(found, where+": "+n[:min(len(n), 12)]+"…")
			}
		}
	}
	check("log", h.logs.String())
	rows, err := h.d.Read().Query(`SELECT action, target_id, detail_json FROM audit_log`)
	if err == nil {
		for rows.Next() {
			var a, tid, d string
			_ = rows.Scan(&a, &tid, &d)
			check("audit "+a, a+tid+d)
		}
		rows.Close()
	}
	rows, _ = h.d.Read().Query(`SELECT id, name, preset, url, branch, path_prefix, auth_kind, username, public_key, known_hosts, last_error, last_error_code FROM git_remotes`)
	for rows.Next() {
		var cols [12]string
		_ = rows.Scan(&cols[0], &cols[1], &cols[2], &cols[3], &cols[4], &cols[5], &cols[6], &cols[7], &cols[8], &cols[9], &cols[10], &cols[11])
		check("remote row", strings.Join(cols[:], "|"))
	}
	rows.Close()
	rows, _ = h.d.Read().Query(`SELECT trigger, status, outcome, error_code, error_message, manifest_json, detail_json FROM git_pushes`)
	for rows.Next() {
		var cols [7]string
		_ = rows.Scan(&cols[0], &cols[1], &cols[2], &cols[3], &cols[4], &cols[5], &cols[6])
		check("push row", strings.Join(cols[:], "|"))
	}
	rows.Close()
	_ = filepath.WalkDir(h.dataDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, rerr := os.ReadFile(p); rerr == nil {
				check("file "+strings.TrimPrefix(p, h.dataDir), string(b))
			}
		}
		return nil
	})
	return found
}

func TestCredentialsNeverLeak(t *testing.T) {
	h := newHarness(t)
	h.start()
	// A token remote that cannot connect, so errors and logs are produced, and
	// a deploy key remote beside it (replacing the first: one per project).
	rem := h.addRemote("https://127.0.0.1:1/owner/repo.git", func(in *gitmirror.CreateIn) { in.Token = token; in.Username = "mairead"; in.PushMode = "on_save" })
	var sealed string
	_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, rem.ID).Scan(&sealed)
	if strings.Contains(sealed, token) || !strings.HasPrefix(sealed, "v1.") {
		t.Fatalf("the token is not sealed: %q", sealed)
	}
	p := h.push(h.owner)
	if p.Status != "error" {
		t.Fatalf("push = %+v", p)
	}
	test := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut)
	if test.OK {
		t.Fatalf("test = %+v", test)
	}
	var outputs []string
	for _, v := range []any{rem, h.remote(), p, test,
		h.must(h.owner, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid}),
		h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}),
		h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Token: ptr(token + "-rotated"), Username: ptr("mairead")}),
	} {
		b, _ := json.Marshal(v)
		outputs = append(outputs, string(b))
	}
	for _, o := range outputs {
		if strings.Contains(o, token) || strings.Contains(o, "v1.") {
			t.Errorf("an op result carries the credential or its ciphertext: %.200s", o)
		}
	}
	// A bad token is not echoed back in the error.
	_, err := h.call(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Token: ptr("has a space " + token)})
	if err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("error = %v", err)
	}
	if l := h.leaks(token, token+"-rotated"); len(l) > 0 {
		t.Errorf("the token leaked: %v", l)
	}

	// The deploy key: open the sealed private key with the instance key and look
	// for its body anywhere else.
	h.must(h.owner, "git.remote.delete", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: rem.ID})
	sr := h.addRemote("ssh://git@127.0.0.1:1/owner/repo.git", func(in *gitmirror.CreateIn) { in.PushMode = "on_save" })
	var cipher string
	_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, sr.ID).Scan(&cipher)
	pem, err := secrets.New([]byte("test-secret-key-test-secret-key-0123456789")).Open(cipher, gitmirror.SealContext(sr.ID, sr.URL))
	if err != nil || !strings.Contains(string(pem), "BEGIN OPENSSH PRIVATE KEY") {
		t.Fatalf("cannot open the sealed key: %v", err)
	}
	body := strings.Split(strings.TrimSpace(string(pem)), "\n")[1]
	if _, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}); err == nil {
		t.Error("a push before the host key is accepted should say what is missing")
	}
	h.must(h.owner, "git.remote.keygen", gitmirror.KeygenIn{ProjectID: h.pid, Replace: true})
	h.d.Writer().Exec(`UPDATE git_remotes SET known_hosts = 'x' WHERE id = ?`, sr.ID) // nothing to scan: the failure paths are what is under test
	b, _ := json.Marshal(h.must(h.owner, "git.remote.get", gitmirror.RemoteQuery{ProjectID: h.pid}))
	if strings.Contains(string(b), "PRIVATE") || strings.Contains(string(b), body) {
		t.Errorf("the private key is in a result: %.200s", b)
	}
	if l := h.leaks(body, "BEGIN OPENSSH PRIVATE KEY", "BEGIN PRIVATE KEY"); len(l) > 0 {
		t.Errorf("the private key leaked: %v", l)
	}
}

func TestWithoutAKeyNoCredentialIsStored(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.Box = secrets.New(nil) })
	_, err := h.call(h.owner, "git.remote.create", gitmirror.CreateIn{Audience: "private", ProjectID: h.pid, Name: "Backup", URL: "https://forge.example.org/o/r.git", Token: token})
	if h.code(err) != ops.CodeConflict || !strings.Contains(err.Error(), "secret key") || strings.Contains(err.Error(), token) {
		t.Fatalf("create without a key: %v", err)
	}
	if _, err := h.call(h.owner, "git.remote.create", gitmirror.CreateIn{Audience: "private", ProjectID: h.pid, Name: "Backup", URL: "ssh://git@forge.example.org/o/r.git"}); h.code(err) != ops.CodeConflict {
		t.Fatalf("a deploy key without a key: %v", err)
	}
	if rowCount(t, h, `SELECT count(*) FROM git_remotes`) != 0 {
		t.Error("a remote was stored without its credential")
	}
	list := h.must(h.owner, "git.remote.list", gitmirror.ProjectIn{ProjectID: h.pid}).(gitmirror.RemoteList)
	if list.Enabled || list.DisabledNotes == "" {
		t.Errorf("list = %+v", list)
	}
}

func TestCredentialsTheKeyCannotReadAreClearedAfterARestore(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("https://forge.example.org/o/r.git", func(in *gitmirror.CreateIn) { in.Token = token })
	if len(backup.PostRestore) == 0 {
		t.Fatal("no post-restore step is registered")
	}
	run := func(key []byte) string {
		var msgs []string
		for _, step := range backup.PostRestore {
			m, err := step(context.Background(), h.d, key)
			if err != nil {
				t.Fatal(err)
			}
			if m != "" { // other packages register steps too; only messages count
				msgs = append(msgs, m)
			}
		}
		return strings.Join(msgs, " ")
	}
	// The same key: the credential is fine and is kept, but a restore pauses every remote (a copy that was
	// restored must not push the old copy's text until its owner says so), and says so.
	if m := run([]byte("test-secret-key-test-secret-key-0123456789")); !strings.Contains(m, "1 remote(s)") || !strings.Contains(m, "were paused") ||
		h.remote().Enabled || !h.remote().HasSecret || h.remote().Status.LastErrorCode != "paused_by_restore" {
		t.Fatalf("same key: %q %+v", m, h.remote())
	}
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Enabled: ptr(true)}) // the owner switches it on again
	// No key at all: said so, nothing cleared.
	if m := run(nil); !strings.Contains(m, "no secret key") || !h.remote().HasSecret {
		t.Fatalf("no key: %q", m)
	}
	// A different key: cleared and switched off, with a message.
	m := run([]byte("a-different-secret-key-of-32-characters-or-more"))
	if !strings.Contains(m, "cleared") || !strings.Contains(m, "Enter them again") {
		t.Errorf("message = %q", m)
	}
	r := h.remote()
	if r.HasSecret || r.Enabled || r.Status.State != "blocked" || r.Status.LastErrorCode != "credentials_unreadable" || !strings.Contains(r.Status.LastError, "Enter them again") {
		t.Fatalf("remote = %+v", r)
	}
	var cipher string
	_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, rem.ID).Scan(&cipher)
	if cipher != "" {
		t.Error("the unreadable ciphertext was kept")
	}
	// It cannot be switched on again until the credential is entered; entering
	// it makes it work.
	if _, err := h.call(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Enabled: ptr(true)}); h.code(err) != ops.CodeConflict {
		t.Errorf("switching on without credentials: %v", err)
	}
	if _, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}); h.code(err) != ops.CodeConflict {
		t.Errorf("pushing a switched-off remote: %v", err)
	}
}

func TestAtStartUnreadableCredentialsAreSwitchedOffNotWiped(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("https://forge.example.org/o/r.git", func(in *gitmirror.CreateIn) { in.Token = token })
	wrong := secrets.New([]byte("a-different-secret-key-of-32-characters-or-more"))
	msg, err := gitmirror.CheckCredentials(context.Background(), h.d, wrong, false)
	if err != nil || !strings.Contains(msg, "switched off") {
		t.Fatalf("check = %q, %v", msg, err)
	}
	r := h.remote()
	if r.Enabled || r.Status.LastErrorCode != "credentials_unreadable" {
		t.Fatalf("remote = %+v", r)
	}
	var cipher string
	_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, rem.ID).Scan(&cipher)
	if cipher == "" {
		t.Fatal("the ciphertext was wiped; the right key could still read it")
	}
	// The original key is put back and the server restarts: it works again.
	msg, err = gitmirror.CheckCredentials(context.Background(), h.d, secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), false)
	if err != nil || !strings.Contains(msg, "switched on") {
		t.Fatalf("check = %q, %v", msg, err)
	}
	if r := h.remote(); !r.Enabled || r.Status.LastError != "" || r.Status.State != "new" {
		t.Fatalf("remote = %+v", r)
	}
}

func TestTheSecretScanner(t *testing.T) {
	pgp := "-----BEGIN PGP PRIVATE KEY BLOCK-----\nVersion: Test 1.0\n\n" + keyBody + "\n-----END PGP PRIVATE KEY BLOCK-----"
	hit := map[string]string{
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" + keyBody: "a private key",
		"-----BEGIN RSA PRIVATE KEY-----\n" + keyBody:     "a private key",
		pgp: "a private key",
		"> -----BEGIN PRIVATE KEY-----\n> " + keyBody:                "a private key", // quoted in Markdown
		"    -----BEGIN PRIVATE KEY-----\n    " + keyBody:            "a private key", // an indented block
		"my key is sk-abcdefghijklmnopqrstuvwxyz012345":              "an API key (sk-…)",
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz":                    "an API key (sk-…)",
		"export TOKEN=sk_live_abcdefghijklmnopqrstu":                 "an API key (sk_…)",
		"Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123":       "a bearer token",
		"ghp_abcdefghijklmnopqrstuvwxyz0123456789":                   "a GitHub token",
		"AKIAABCDEFGHIJKLMNOP":                                       "an AWS access key",
		"clone https://mairead:hunter2hunter2@forge.example.org/o/r": "a password in a URL",
	}
	for text, rule := range hit {
		got := gitmirror.ScanForSecrets(map[string][]byte{"manuscript/01.md": []byte(text)})
		if len(got) != 1 || got[0] != "manuscript/01.md|"+rule {
			t.Errorf("%q: %v, want %s", text, got, rule)
		}
	}
	for _, text := range []string{
		"The bearer of the token walked on.",
		"She wore a Bearer badge, and nothing else of note.",
		"\"-----BEGIN PRIVATE KEY-----\", it said, and then the page was torn.", // a quoted header and no key
		"A line that reads -----BEGIN RSA PRIVATE KEY----- in a thriller.",
		"sk-8 is a model; a task-based approach",
		"https://example.org/path/with/no/password",
		"AKIAIOSFODNN7EXAMPLE is the key in Amazon's documentation",
		"set OPENAI_API_KEY=sk-proj-xxxxxxxxxxxxxxxxxxxxxxxxxxxx first",
		"Authorization: Bearer xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"git clone https://user:password@example.org/o/r.git",
		"ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
		"Salt on the wind. " + strings.Repeat("word ", 200),
	} {
		if got := gitmirror.ScanForSecrets(map[string][]byte{"a.md": []byte(text)}); len(got) != 0 {
			t.Errorf("%q flagged: %v", text, got)
		}
	}
	got := gitmirror.ScanForSecrets(map[string][]byte{"z.md": []byte("-----BEGIN PRIVATE KEY-----\n" + keyBody), "a.md": []byte("-----BEGIN PRIVATE KEY-----\n" + keyBody)})
	if len(got) != 2 || !strings.HasPrefix(got[0], "a.md") {
		t.Errorf("findings are not in path order: %v", got)
	}
}

func TestAuditNamesWhatChangedNeverTheValues(t *testing.T) {
	h := newHarness(t)
	h.addRemote("https://forge.example.org/o/r.git", func(in *gitmirror.CreateIn) { in.Token = token })
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Token: ptr(token + "2"), AuthorName: ptr("Zed"), AuthorEmail: ptr("zed@example.org")})
	var detail string
	if err := h.d.Read().QueryRow(`SELECT detail_json FROM audit_log WHERE action = 'git.remote.update'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, `"token"`) || strings.Contains(detail, token) || strings.Contains(detail, "zed@") {
		t.Errorf("audit detail = %s", detail)
	}
	_ = time.Second
}

func TestRestoreClearsCredentialsTheNewKeyCannotRead(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("https://forge.example.org/o/r.git", func(in *gitmirror.CreateIn) { in.Token = token })
	var dbPath string
	if err := h.d.Read().QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := backup.Create(context.Background(), dbPath, h.dataDir, backup.Options{Out: archive}); err != nil {
		t.Fatal(err)
	}
	restoredRemote := func(dir string) (cipher, publicKey string, enabled int, code string) {
		d, err := db.Open(filepath.Join(dir, "scriobh.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if err := d.Read().QueryRow(`SELECT secret_cipher, public_key, enabled, last_error_code FROM git_remotes WHERE id = ?`, rem.ID).Scan(&cipher, &publicKey, &enabled, &code); err != nil {
			t.Fatal(err)
		}
		return
	}

	// A data directory whose secret.key is not the one the backup was made with.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("ffffffffffffffffffffffffffffffffffffffffffffffff\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := backup.Restore(context.Background(), archive, dir, backup.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "cleared") || !strings.Contains(strings.Join(res.Notes, " "), "Enter them again") {
		t.Errorf("notes = %v", res.Notes)
	}
	if c, pk, en, code := restoredRemote(dir); c != "" || en != 0 || code != "credentials_unreadable" || pk != "" {
		t.Errorf("after restoring with another key: cipher=%q enabled=%d code=%q", c, en, code)
	}

	// The key from the environment (SCRIOBH_SECRET_KEY) is the one in use.
	dir2 := t.TempDir()
	res, err = backup.Restore(context.Background(), archive, dir2, backup.RestoreOptions{SecretKey: []byte("another-environment-key-of-more-than-32-chars")})
	if err != nil {
		t.Fatal(err)
	}
	if c, _, en, _ := restoredRemote(dir2); c != "" || en != 0 || !strings.Contains(strings.Join(res.Notes, " "), "cleared") {
		t.Errorf("environment key: cipher=%q enabled=%d notes=%v", c, en, res.Notes)
	}

	// The original key: nothing is cleared, and the remote is paused like every remote after a restore.
	dir3 := t.TempDir()
	res, err = backup.Restore(context.Background(), archive, dir3, backup.RestoreOptions{SecretKey: []byte("test-secret-key-test-secret-key-0123456789")})
	if err != nil {
		t.Fatal(err)
	}
	if c, _, en, code := restoredRemote(dir3); c == "" || en != 0 || code != "paused_by_restore" || !strings.Contains(strings.Join(res.Notes, " "), "1 remote(s)") || strings.Contains(strings.Join(res.Notes, " "), "cleared") {
		t.Errorf("original key: cipher=%q enabled=%d code=%q notes=%v", c, en, code, res.Notes)
	}

	// No key anywhere: said so, credentials kept for the original key.
	dir4 := t.TempDir()
	res, err = backup.Restore(context.Background(), archive, dir4, backup.RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c, _, _, _ := restoredRemote(dir4); c == "" || !strings.Contains(strings.Join(res.Notes, " "), "no secret key") {
		t.Errorf("no key: cipher=%q notes=%v", c, res.Notes)
	}
}
