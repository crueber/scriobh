package admin_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/admin"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/keys"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/share"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type world struct {
	t        *testing.T
	d        *db.DB
	reg      *ops.Registry
	dir      string
	ctx      context.Context
	ada, bob ops.Principal // two administrators
	wren     ops.Principal // a writer
	svc      *admin.Service
}

func newWorld(t *testing.T) *world {
	d := testutil.DB(t)
	dir := t.TempDir()
	reg := registry.Build(d, events.NewStore(d, nil), dir)
	w := &world{t: t, d: d, reg: reg, dir: dir, ctx: context.Background()}
	person := func(role, name string) ops.Principal {
		id := testutil.User(t, d, role, strings.ToLower(name)+".canary@example.org")
		if _, err := d.Writer().Exec(`UPDATE users SET display_name = ? WHERE id = ?`, name, id); err != nil {
			t.Fatal(err)
		}
		return ops.Principal{UserID: id, Role: role, Email: strings.ToLower(name) + ".canary@example.org", Name: name, Via: "session"}
	}
	w.ada, w.bob, w.wren = person(ops.RoleAdmin, "Ada"), person(ops.RoleAdmin, "Bob"), person(ops.RoleWriter, "Wren")
	w.svc = &admin.Service{Limits: attachments.Limits{MaxBytes: 25 << 20, UserBytes: 2 << 30, OwnerBytes: 1 << 30}, MaxKeysPerUser: 20, DataDir: dir, Version: "test"}
	reg.Env().SetService(admin.ServiceName, w.svc)
	return w
}

func (w *world) call(p ops.Principal, op string, in any) (any, error) {
	return w.reg.Call(w.ctx, p, op, in)
}
func (w *world) must(p ops.Principal, op string, in any) any {
	w.t.Helper()
	out, err := w.call(p, op, in)
	if err != nil {
		w.t.Fatalf("%s: %v", op, err)
	}
	return out
}
func (w *world) code(p ops.Principal, op string, in any) ops.Code {
	w.t.Helper()
	_, err := w.call(p, op, in)
	if err == nil {
		w.t.Fatalf("%s: expected an error", op)
	}
	return ops.AsError(err).Code
}

func TestOnlyAnAdministratorSeesPeople(t *testing.T) {
	w := newWorld(t)
	for _, op := range []string{"admin.user.list", "admin.quota.list", "admin.diagnostics", "admin.diagnostics.download"} {
		var in any = admin.ListIn{}
		switch op {
		case "admin.quota.list":
			in = admin.QuotaIn{}
		case "admin.diagnostics", "admin.diagnostics.download":
			in = admin.DiagnosticsIn{}
		}
		if c := w.code(w.wren, op, in); c != ops.CodeNotFound {
			t.Errorf("a writer calls %s: %s", op, c)
		}
		if _, err := w.call(w.ada, op, in); err != nil {
			t.Errorf("an administrator calls %s: %v", op, err)
		}
	}
	list := w.must(w.ada, "admin.user.list", admin.ListIn{}).(admin.ListOut)
	if list.Total != 3 || list.Admins != 2 || list.Items[0].Email == "" {
		t.Errorf("list: %+v", list)
	}
	found := w.must(w.ada, "admin.user.list", admin.ListIn{Q: "wren"}).(admin.ListOut)
	if found.Total != 1 || found.Items[0].Name != "Wren" {
		t.Errorf("search: %+v", found)
	}
	// a key cannot switch anyone off: the writes are for a browser session
	key := ops.Principal{UserID: w.ada.UserID, Role: ops.RoleAdmin, Via: "apikey", Scope: ops.ScopeAdmin}
	if c := w.code(key, "admin.user.disable", admin.UserRef{ID: w.wren.UserID}); c != ops.CodeForbidden {
		t.Errorf("a key disables someone: %s", c)
	}
}

func TestSwitchingSomeoneOffEndsEverythingTheyHadAndKeepsWhatTheyWrote(t *testing.T) {
	w := newWorld(t)
	cfg := testutil.Config(t, nil)
	key, _ := cfg.EnsureSecretKey()
	sess := &auth.Sessions{DB: w.d, Cfg: cfg, Key: key}
	keyring := &auth.Keys{DB: w.d}
	proj := w.must(w.wren, "project.create", projects.CreateIn{Title: "Wren's Book"}).(projects.Item).Item
	tok, _, err := sess.Create(w.ctx, w.wren.UserID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	made := w.must(w.wren, "key.create", keys.CreateIn{Name: "k"}).(keys.CreateOut)
	if _, _, err := sess.Lookup(w.ctx, tok); err != nil {
		t.Fatalf("session before: %v", err)
	}
	if _, _, err := keyring.Lookup(w.ctx, made.Secret); err != nil {
		t.Fatalf("key before: %v", err)
	}

	if c := w.code(w.ada, "admin.user.disable", admin.UserRef{ID: w.ada.UserID}); c != ops.CodeConflict {
		t.Errorf("switching yourself off: %s", c)
	}
	w.must(w.ada, "admin.user.disable", admin.UserRef{ID: w.wren.UserID})

	// every way in refuses: the session, the key, a live stream (which looks both up again), a new sign-in, being added to a project
	if _, _, err := sess.Lookup(w.ctx, tok); err == nil {
		t.Error("a switched-off person's session still works")
	}
	if _, _, err := sess.Peek(w.ctx, tok); err == nil {
		t.Error("a switched-off person's session still works for a stream")
	}
	if _, _, err := keyring.Lookup(w.ctx, made.Secret); err == nil {
		t.Error("a switched-off person's API key still works")
	}
	users := &auth.Users{DB: w.d, Cfg: cfg}
	_, err = users.SignIn(w.ctx, "test", auth.Claims{Subject: "sub-" + w.wren.UserID, Email: "wren.canary@example.org", EmailVerified: true, Name: "Wren", Groups: []string{cfg.WriterGroup}})
	var denied *auth.DeniedError
	if err == nil || !errorsAs(err, &denied) || !strings.Contains(denied.Reason, "switched off") {
		t.Errorf("signing in: %v", err)
	}
	if _, err := users.Get(w.ctx, w.wren.UserID); err == nil {
		t.Error("Users.Get returns a switched-off person")
	}
	if c := w.code(w.ada, "project.member.add", projects.MemberAddIn{ID: w.mustProject(w.ada), Email: "wren.canary@example.org", Role: "reader"}); c != ops.CodeNotFound {
		t.Errorf("adding a switched-off person to a project: %s", c)
	}
	// what they wrote and own is untouched
	if g, err := w.call(w.ada, "project.get", projects.GetIn{ID: proj.ID}); err != nil || g.(projects.Item).Item.Title != "Wren's Book" {
		t.Errorf("the project is not there: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM projects WHERE owner_id = ? AND deleted_at IS NULL`, w.wren.UserID); n != 1 {
		t.Errorf("projects owned: %d", n)
	}
	if n := w.count(`SELECT count(*) FROM sessions WHERE user_id = ? AND revoked_at IS NULL`, w.wren.UserID); n != 0 {
		t.Errorf("live sessions: %d", n)
	}
	// audited, with who did it
	if n := w.count(`SELECT count(*) FROM audit_log WHERE action = 'admin.user.disable' AND target_id = ? AND actor_id = ?`, w.wren.UserID, w.ada.UserID); n != 1 {
		t.Errorf("audit entries: %d", n)
	}
	// the list says so, and enabling brings everything back
	l := w.must(w.ada, "admin.user.list", admin.ListIn{Status: "disabled"}).(admin.ListOut)
	if l.Total != 1 || !l.Items[0].Disabled || l.Items[0].Sessions != 0 || l.Items[0].Projects != 1 {
		t.Errorf("disabled list: %+v", l)
	}
	w.must(w.ada, "admin.user.enable", admin.UserRef{ID: w.wren.UserID})
	if _, _, err := keyring.Lookup(w.ctx, made.Secret); err != nil {
		t.Errorf("the key after enabling: %v", err)
	}
	if _, err := users.SignIn(w.ctx, "test", auth.Claims{Subject: "sub-" + w.wren.UserID, Email: "wren.canary@example.org", EmailVerified: true, Name: "Wren", Groups: []string{cfg.WriterGroup}}); err != nil {
		t.Errorf("signing in after enabling: %v", err)
	}
	// switching off twice and on twice change nothing and are not errors
	w.must(w.ada, "admin.user.enable", admin.UserRef{ID: w.wren.UserID})
	if c := w.code(w.ada, "admin.user.disable", admin.UserRef{ID: "usr_00000000000000000000000000"}); c != ops.CodeNotFound {
		t.Errorf("someone who is not there: %s", c)
	}
}

func (w *world) mustProject(p ops.Principal) string {
	return w.must(p, "project.create", projects.CreateIn{Title: "Ada's Book"}).(projects.Item).Item.ID
}

func (w *world) count(q string, args ...any) int {
	var n int
	if err := w.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func errorsAs(err error, t **auth.DeniedError) bool {
	for err != nil {
		if d, ok := err.(*auth.DeniedError); ok {
			*t = d
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func TestTheLastAdministratorCannotBeSwitchedOffOrDemoted(t *testing.T) {
	w := newWorld(t)
	// two administrators: one may go
	w.must(w.ada, "admin.user.set_role", admin.RoleIn{ID: w.bob.UserID, Role: ops.RoleWriter})
	// Ada is now the only one
	if c := w.code(w.bob, "admin.user.disable", admin.UserRef{ID: w.ada.UserID}); c != ops.CodeForbidden && c != ops.CodeConflict {
		t.Errorf("Bob (not an administrator now) switches Ada off: %s", c)
	}
	if c := w.code(w.ada, "admin.user.set_role", admin.RoleIn{ID: w.ada.UserID, Role: ops.RoleWriter}); c != ops.CodeConflict {
		t.Errorf("demoting the last administrator: %s", c)
	}
	// a second administrator who is switched off does not count
	w.must(w.ada, "admin.user.set_role", admin.RoleIn{ID: w.bob.UserID, Role: ops.RoleAdmin})
	w.must(w.ada, "admin.user.disable", admin.UserRef{ID: w.bob.UserID})
	if c := w.code(w.ada, "admin.user.set_role", admin.RoleIn{ID: w.ada.UserID, Role: ops.RoleReader}); c != ops.CodeConflict {
		t.Errorf("demoting the only administrator who is on: %s", c)
	}
	if c := w.code(w.ada, "admin.user.set_role", admin.RoleIn{ID: w.bob.UserID, Role: "root"}); c != ops.CodeInvalid {
		t.Errorf("a role that is not one: %s", c)
	}
	// where the identity provider decides who administers, this is refused
	w.svc.AdminFromIdP = true
	if c := w.code(w.ada, "admin.user.set_role", admin.RoleIn{ID: w.wren.UserID, Role: ops.RoleAdmin}); c != ops.CodeConflict {
		t.Errorf("making an administrator where the IdP decides: %s", c)
	}
	if n := w.count(`SELECT count(*) FROM audit_log WHERE action = 'admin.user.set_role'`); n != 2 {
		t.Errorf("role changes audited: %d", n)
	}
}

func TestQuotasShowUseAgainstTheLimits(t *testing.T) {
	w := newWorld(t)
	if _, err := w.d.Writer().Exec(`INSERT INTO file_usage(scope, bytes, files) VALUES (?,?,1), ('instance', ?, 1)`, "user:"+w.wren.UserID, 3<<20, 5<<20); err != nil {
		t.Fatal(err)
	}
	w.must(w.wren, "key.create", keys.CreateIn{Name: "one"})
	q := w.must(w.ada, "admin.quota.list", admin.QuotaIn{}).(admin.QuotaOut)
	if q.Editable || !strings.Contains(q.Note, "configuration") {
		t.Errorf("quotas are read only: %+v", q)
	}
	if q.Instance[0].Used != 5<<20 || q.Instance[0].Limit != 0 || q.Instance[2].Limit != 25<<20 || q.Instance[3].Name != "Imports in progress (files held for review)" {
		t.Errorf("instance lines: %+v", q.Instance)
	}
	if q.Users[0].Name != "Wren" || q.Users[0].StorageBytes != 3<<20 || q.Users[0].StorageLimit != 2<<30 || q.Users[0].Keys != 1 || q.Users[0].KeyLimit != 20 {
		t.Errorf("user lines: %+v", q.Users)
	}
}

// Canaries in every place that is hidden: a person's name and email, a title, the text of a chapter, a comment, a
// share link and its token, a file's name on disk, a credential in a setting and in a Git address, and log lines that
// carry a token in a path, a bearer, a query and a key. Not one reaches the bundle, however it is read.
func TestDiagnosticsHoldNoneOfWhatIsPrivate(t *testing.T) {
	w := newWorld(t)
	proj := w.must(w.wren, "project.create", projects.CreateIn{Title: "CANARYTITLE The Salt Road"}).(projects.Item).Item
	doc := w.must(w.wren, "doc.create", docs.CreateIn{ProjectID: proj.ID, Title: "CANARYCHAPTER", Kind: "chapter", Content: "CANARYTEXT the tide came in. %%CANARYNOTE%%\n"}).(docs.Item).Item
	w.must(w.wren, "comment.create", review.CreateIn{ProjectID: proj.ID, DocumentID: doc.ID, Quote: "the tide", Body: "CANARYCOMMENT please cut"})
	// two private ("Only me") notes: the diagnostics bundle must not show that they exist (the comments row count stays that of the shared ones)
	w.must(w.wren, "comment.create", review.CreateIn{ProjectID: proj.ID, DocumentID: doc.ID, Body: "CANARYPRIVATE a note to myself", Visibility: "private"})
	w.must(w.wren, "comment.create", review.CreateIn{ProjectID: proj.ID, DocumentID: doc.ID, Body: "CANARYPRIVATE another", Visibility: "private"})
	sh := w.must(w.wren, "share.create", share.CreateIn{ProjectID: proj.ID, Label: "CANARYLABEL", Passphrase: "CANARYPASSPHRASE12"}).(share.CreateOut)
	k := w.must(w.wren, "key.create", keys.CreateIn{Name: "CANARYKEYNAME"}).(keys.CreateOut)
	if _, err := w.d.Writer().Exec(`INSERT INTO file_usage(scope, bytes, files) VALUES ('instance', 1000, 1)`); err != nil {
		t.Fatal(err)
	}
	canaryDir := filepath.Join(w.dir, "files", "projects", "CANARYPATH", "attachments")
	if err := os.MkdirAll(canaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canaryDir, "CANARYFILE.png"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a staged import (a file held while its review goes on): its name and its words are not in the bundle either
	stageDir := filepath.Join(w.dir, "imports", "stage", "imd_CANARYSTAGE")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "source"), []byte("CANARYSTAGEDTEXT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.dir, "CANARYTOPLEVEL.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.svc.Config = func() map[string]any {
		return map[string]any{"fine": 7, "listen": "CANARYADDR", "remote": "https://user:CANARYGITPASS@git.example.org/x.git", "key": "sk-CANARYSECRETKEY123"}
	}
	w.svc.Log = func() []admin.LogLine {
		return []admin.LogLine{
			{At: "2026-11-13T10:00:00Z", Level: "ERROR", Msg: "GET /s/" + sh.Token + " failed for wren.canary@example.org", Attrs: map[string]string{"route": "GET /s/{token}", "err": "Bearer CANARYBEARERVALUE then ?q=CANARYQUERY and shl_CANARYSHLTOKEN.abcdef and " + k.Secret + " and https://u:CANARYURLPASS@h/p"}},
			{At: "2026-11-13T10:01:00Z", Level: "WARN", Msg: "a long secret " + strings.Repeat("Z", 60), Attrs: map[string]string{"path": "/p/CANARYSLUG/read"}},
		}
	}
	b := w.must(w.ada, "admin.diagnostics", admin.DiagnosticsIn{}).(admin.Bundle)
	js, _ := json.Marshal(b)
	arch := w.must(w.ada, "admin.diagnostics.download", admin.DiagnosticsIn{}).(admin.Archive)
	zr, err := zip.NewReader(bytes.NewReader(arch.Body), int64(len(arch.Body)))
	if err != nil {
		t.Fatal(err)
	}
	all := string(js)
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		r, _ := f.Open()
		body, _ := io.ReadAll(r)
		r.Close()
		all += string(body)
	}
	if strings.Join(names, ",") != "README.txt,diagnostics.json" {
		t.Errorf("zip entries %v", names)
	}
	all += arch.Filename
	for _, secret := range []string{"CANARY", "canary", sh.Token, strings.Split(sh.Token, ".")[1], k.Secret, "wren.canary", "Wren", "Ada", "example.org", "ZZZZZZZZZZ", proj.ID, doc.ID, w.wren.UserID, w.dir} {
		if strings.Contains(all, secret) {
			i := strings.Index(all, secret)
			t.Errorf("the bundle holds %q: …%s…", secret, all[max(0, i-60):min(len(all), i+80)])
		}
	}
	// what it should hold is there
	for _, want := range []string{`"projects"`, `"documents"`, `"comments"`, `"users"`, `"fine": 7`, `"GET /s/{token}"`, "[token]", "[email]", "[url]"} {
		if !strings.Contains(all, want) && !strings.Contains(strings.ReplaceAll(all, `":7`, `": 7`), want) {
			t.Errorf("the bundle lacks %q", want)
		}
	}
	rows := map[string]int64{}
	for _, tc := range b.Tables {
		rows[tc.Name] = tc.Rows
	}
	if rows["projects"] != 1 || rows["documents"] < 1 || rows["comments"] != 1 || rows["users"] != 3 {
		t.Errorf("row counts: %v", rows)
	}
	var area = map[string]int{}
	for _, d := range b.Disk {
		area[d.Area] = d.Files
	}
	if area["files"] != 1 || area["other"] < 1 {
		t.Errorf("disk areas: %+v", b.Disk)
	}
	if b.Migrations.Pending != 0 || b.Migrations.Applied != b.Migrations.Known || b.Migrations.Latest == "" {
		t.Errorf("migrations: %+v", b.Migrations)
	}
	// the same state gives the same bytes
	again := w.must(w.ada, "admin.diagnostics.download", admin.DiagnosticsIn{}).(admin.Archive)
	if !bytes.Equal(again.Body, arch.Body) {
		t.Error("the bundle is not deterministic")
	}
}

func TestScrubCatchesTheShapesOfSecrets(t *testing.T) {
	for in, bad := range map[string]string{
		"GET /s/shl_01ABC.def failed":            "shl_01ABC",
		"Authorization: Bearer abcDEF123456":     "abcDEF123456",
		"clone https://me:hunter2@host/r.git":    "hunter2",
		"mail me@example.com now":                "me@example.com",
		"key sk-abcdefghijkl and ghp_abcdefgh12": "abcdefghij",
		"search ?q=private words&x=1":            "private words",
		"digest " + strings.Repeat("a1", 20):     strings.Repeat("a1", 20),
	} {
		if got := admin.Scrub(in); strings.Contains(got, bad) {
			t.Errorf("Scrub(%q) = %q still holds %q", in, got, bad)
		}
	}
	if got := admin.Scrub("GET /p/{slug}/read 200 in 5ms"); got != "GET /p/{slug}/read 200 in 5ms" {
		t.Errorf("an ordinary line was changed: %q", got)
	}
	if got := admin.ScrubLine(strings.Repeat("é ", 300), 20); len([]rune(got)) > 21 {
		t.Errorf("not bounded: %d", len([]rune(got)))
	}
}
