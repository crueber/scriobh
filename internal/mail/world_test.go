package mail

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	netmail "net/mail"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/testutil"
)

const (
	relayPW   = "relay-pw-CANARY-9f3k2"
	adminMail = "admin@example.net"
)

type fclock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fclock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fclock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type person struct {
	id    string
	email string
	p     ops.Principal
}

type world struct {
	t     *testing.T
	d     *db.DB
	box   *secrets.Box
	svc   *Service
	reg   *ops.Registry
	smtp  *fakeSMTP
	clk   *fclock
	logs  *bytes.Buffer
	store *events.Store
	dir   string

	admin, alice, bob, carol person
	project                  string

	compMu   sync.Mutex
	reqs     []EditionRequest
	editionN int    // changes the edition's hash
	bodySize int    // bytes of the fake edition
	title    string // the file name the fake compile gives
}

type wopt struct {
	lim       Limits
	afterSend func(string) error
	noSMTP    bool
	smtpMod   []func(*fakeSMTP)
	base      string
}

func newWorld(t *testing.T, mods ...func(*wopt)) *world {
	t.Helper()
	o := wopt{base: "https://scriobh.example.org"}
	for _, m := range mods {
		m(&o)
	}
	d := testutil.DB(t)
	w := &world{t: t, d: d, box: secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), logs: &bytes.Buffer{}, dir: t.TempDir(), bodySize: 5000, title: "My Book: Draft ✓.epub"}
	w.clk = &fclock{t: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}
	restore := db.SetClock(w.clk.Now)
	t.Cleanup(restore)
	w.store = events.NewStore(d, nil)
	w.smtp = newFakeSMTP(t, append([]func(*fakeSMTP){func(f *fakeSMTP) { f.AuthMechs = "PLAIN LOGIN"; f.Password = relayPW }}, o.smtpMod...)...)
	log := slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	w.svc = New(Options{DB: d, Box: w.box, Log: log, DataDir: w.dir, BaseURL: func() string { return o.base }, Compile: w.compile,
		Limits: o.lim, Now: w.clk.Now, Rand: func() float64 { return 0.5 }, AfterSend: o.afterSend,
		Client: &Client{Idle: 3 * time.Second, Overall: 10 * time.Second}})
	env := &ops.Env{DB: d, Sink: w.store, DataDir: w.dir, Log: log, ProjectRole: projects.RoleFunc(d)}
	env.ProjectTrashed = func(ctx context.Context, id string) bool {
		var n int
		_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM projects WHERE id = ? AND trashed_at IS NOT NULL`, id).Scan(&n)
		return n > 0
	}
	w.reg = ops.NewRegistry(env)
	Register(w.reg)
	env.SetService(ServiceName, w.svc)
	w.admin = w.user(ops.RoleAdmin, adminMail, "Ada Admin")
	w.alice = w.user(ops.RoleWriter, "alice@example.com", "Alice Author")
	w.bob = w.user(ops.RoleWriter, "bob@example.com", "Bob Editor")
	w.carol = w.user(ops.RoleWriter, "carol@example.com", "Carol Reader")
	w.project = w.addProject("Salt Road", w.alice, map[person]string{w.bob: "editor", w.carol: "reader"})
	return w
}

func (w *world) user(role, email, name string) person {
	id := testutil.User(w.t, w.d, role, email)
	if _, err := w.d.Writer().Exec(`UPDATE users SET display_name = ? WHERE id = ?`, name, id); err != nil {
		w.t.Fatal(err)
	}
	return person{id: id, email: email, p: ops.Principal{UserID: id, Role: role, Via: "session", Email: email}}
}

func (w *world) addProject(title string, owner person, members map[person]string) string {
	id := db.NewID("prj")
	now := db.Now()
	err := w.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES (?,?,?,?,?,?)`, id, owner.id, title, strings.ToLower(strings.ReplaceAll(title, " ", "-"))+"-"+id[len(id)-6:], now, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,'owner',?)`, id, owner.id, now); err != nil {
			return err
		}
		for m, role := range members {
			if _, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, id, m.id, role, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

// compile is the fake edition builder: it records what it was asked for and answers a file of a set size.
func (w *world) compile(_ context.Context, _ ops.Principal, req EditionRequest) (*Edition, error) {
	w.compMu.Lock()
	defer w.compMu.Unlock()
	w.reqs = append(w.reqs, req)
	body := bytes.Repeat([]byte{'E', 'P', 'U', 'B', byte(w.editionN)}, w.bodySize/5+1)[:w.bodySize]
	return &Edition{Filename: w.title, ContentType: epubType, Body: body, InputHash: fmt.Sprintf("hash-%d-%s", w.editionN, req.ProjectID), SHA256: "", Documents: 3}, nil
}

func (w *world) requests() []EditionRequest {
	w.compMu.Lock()
	defer w.compMu.Unlock()
	return append([]EditionRequest(nil), w.reqs...)
}

func (w *world) call(who person, op string, in any) (any, error) {
	return w.reg.Call(context.Background(), who.p, op, in)
}

func (w *world) must(who person, op string, in any) any {
	w.t.Helper()
	out, err := w.call(who, op, in)
	if err != nil {
		w.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func str(s string) *string { return &s }
func boolp(b bool) *bool   { return &b }
func intp(n int) *int      { return &n }

// configure sets the relay to the fake server (STARTTLS, its certificate trusted, a password) and switches mail on.
func (w *world) configure(mod ...func(*ConfigSetIn)) {
	w.t.Helper()
	in := ConfigSetIn{Enabled: boolp(true), Host: str("127.0.0.1"), Port: intp(w.smtp.Port()), Security: str(SecStartTLS), AllowPrivate: boolp(true), TLSCAPEM: str(string(w.smtp.CertPEM)),
		Username: str("relayuser"), Password: str(relayPW), FromAddress: str("scriobh@example.org"), FromName: str("Scríobh")}
	for _, m := range mod {
		m(&in)
	}
	w.must(w.admin, "mail.config.set", in)
}

func (w *world) pump() int {
	w.t.Helper()
	total := 0
	for i := 0; i < 20; i++ {
		n := w.svc.DispatchOnce(context.Background())
		if n == 0 {
			break
		}
		total += n
	}
	return total
}

type orow struct {
	ID, Kind, Person, State, Err, Recipient, Subject, Body, FileName, DataStarted string
	Attempts                                                                      int
	FileBytes                                                                     int64
	Next                                                                          string
}

func (w *world) rows() []orow {
	w.t.Helper()
	rs, err := w.d.Read().Query(`SELECT id, kind, person_id, state, last_error, recipient, subject, body, file_name, data_started_at, attempts, file_bytes, next_try_at FROM mail_outbox ORDER BY created_at, id`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rs.Close()
	var out []orow
	for rs.Next() {
		var r orow
		if err := rs.Scan(&r.ID, &r.Kind, &r.Person, &r.State, &r.Err, &r.Recipient, &r.Subject, &r.Body, &r.FileName, &r.DataStarted, &r.Attempts, &r.FileBytes, &r.Next); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func (w *world) onlyRow() orow {
	w.t.Helper()
	r := w.rows()
	if len(r) != 1 {
		w.t.Fatalf("outbox has %d rows: %+v", len(r), r)
	}
	return r[0]
}

// kindle stores the person's address and returns a preview token.
func (w *world) preview(who person) KindlePreviewOut {
	w.t.Helper()
	return w.must(who, "kindle.preview", KindleProjectIn{ProjectID: w.project}).(KindlePreviewOut)
}

func (w *world) setKindle(who person, addr string) {
	w.t.Helper()
	w.must(who, "kindle.address.set", KindleAddressSetIn{Address: addr})
}

func (w *world) enqueueRaw(j Job) error {
	return w.d.WithTx(context.Background(), func(tx *sql.Tx) error { return w.svc.EnqueueTx(context.Background(), tx, j) })
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (w *world) dump() string { return testutil.Dump(w.t, w.d) }

func secretsNone() *secrets.Box { return secrets.New(nil) }

func secretsOther() *secrets.Box {
	return secrets.New([]byte("another-secret-key-another-secret-key-99"))
}

func ops_asError(err error) *ops.Error { return ops.AsError(err) }

func mailReadFrom(raw string) (*netmail.Message, error) {
	return netmail.ReadMessage(strings.NewReader(raw))
}
