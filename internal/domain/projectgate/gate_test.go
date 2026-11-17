package projectgate_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type world struct {
	d       *db.DB
	reg     *ops.Registry
	gate    *projectgate.Service
	owner   ops.Principal
	pid     string
	session string
}

func newWorld(t testing.TB) *world {
	d := testutil.DB(t)
	reg := registry.Build(d, nil, t.TempDir())
	uid := testutil.User(t, d, ops.RoleWriter, "gate@t.local")
	if err := d.WithTx(context.Background(), func(tx *sql.Tx) error {
		now := db.Now()
		_, err := tx.Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES ('ses_gate', ?, 'hash', ?, ?, ?)`, uid, now, now, db.Format(time.Now().Add(24*time.Hour)))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p := ops.Principal{UserID: uid, Role: ops.RoleWriter, Via: "session", SessionID: "ses_gate"}
	out, err := reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "Gate"})
	if err != nil {
		t.Fatal(err)
	}
	g, _ := ops.Service[*projectgate.Service](reg.Env(), projectgate.ServiceName)
	return &world{d: d, reg: reg, gate: g, owner: p, pid: out.(projects.Item).Item.ID, session: "ses_gate"}
}

func (w *world) protect(t testing.TB) {
	if _, err := w.reg.Call(context.Background(), w.owner, "project.passphrase.set", projectgate.SetIn{ID: w.pid, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
}

// The gate adds one indexed read to an op on a project with no passphrase and one joined read to one that has: the
// budget is 200 microseconds for either, measured below on this machine and stated in docs/p7-notes/passphrase.md.
func TestGateCheckBudget(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	per := func(n int) time.Duration {
		t0 := time.Now()
		for i := 0; i < n; i++ {
			if err := w.gate.Check(ctx, w.owner, w.pid); err != nil {
				t.Fatal(err)
			}
		}
		return time.Since(t0) / time.Duration(n)
	}
	ungated := per(3000)
	w.protect(t)
	unlocked := per(3000)
	t.Logf("a check with no passphrase: %v; with one, unlocked: %v", ungated, unlocked)
	w.gate.Flush(ctx)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		for name, got := range map[string]time.Duration{"ungated": ungated, "unlocked": unlocked} {
			if got > 400*time.Microsecond*timingFactor {
				t.Errorf("the %s check costs %v; the budget is 200 microseconds (twice that allowed for a loaded machine)", name, got)
			}
		}
	})
}

func BenchmarkCheckUngated(b *testing.B) {
	w := newWorld(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = w.gate.Check(context.Background(), w.owner, w.pid)
	}
}

func BenchmarkCheckUnlocked(b *testing.B) {
	w := newWorld(b)
	w.protect(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = w.gate.Check(context.Background(), w.owner, w.pid)
	}
	w.gate.Flush(context.Background())
}

// Setting a passphrase hashes it at the share links' cost, once: the budget is a second (about 20 ms measured).
func TestSetPassphraseCost(t *testing.T) {
	w := newWorld(t)
	t0 := time.Now()
	w.protect(t)
	d := time.Since(t0)
	t.Logf("set a passphrase: %v", d)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > time.Second*timingFactor {
			t.Errorf("setting a passphrase took %v", d)
		}
	})
}

// A key's approval at the moment it is made needs the minting session's unlock to be there, in the gate's epoch, in the
// same transaction: a passphrase changed after CheckMint looked leaves the key unapproved.
func TestApproveMintedNeedsTheSessionsUnlockInTheCurrentEpoch(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.protect(t)
	var uid string
	_ = w.d.Read().QueryRow(`SELECT id FROM users LIMIT 1`).Scan(&uid)
	if _, err := w.d.Writer().Exec(`INSERT INTO api_keys(id, user_id, name, label, prefix, secret_hash, scope, is_agent, created_at) VALUES ('key_x', ?, 'k', '', 'scr_x', 'h', 'write', 1, ?)`, uid, db.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Writer().Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES ('ses_other', ?, 'hash2', ?, ?, '2999-01-01T00:00:00.000Z')`, uid, db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	var epoch int
	_ = w.d.Read().QueryRow(`SELECT epoch FROM project_gates WHERE project_id = ?`, w.pid).Scan(&epoch)
	if _, err := w.d.Writer().Exec(`INSERT INTO project_unlocks(session_id, project_id, epoch, unlocked_at, last_used_at) VALUES ('ses_other', ?, ?, ?, ?)`, w.pid, epoch, db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	approve := func(session string) int {
		_ = w.d.WithTx(ctx, func(tx *sql.Tx) error { return projectgate.ApproveMinted(tx, []string{w.pid}, "key_x", uid, session) })
		var n int
		_ = w.d.Read().QueryRow(`SELECT count(*) FROM project_gate_key_ok WHERE key_id = 'key_x'`).Scan(&n)
		return n
	}
	if n := approve("ses_none"); n != 0 {
		t.Fatalf("a session with no unlock approved a key: %d", n)
	}
	// the owner rotates the passphrase: ses_other's unlock is void
	if _, err := w.reg.Call(ctx, w.owner, "project.passphrase.set", projectgate.SetIn{ID: w.pid, Passphrase: "a rotated one", Current: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	if n := approve("ses_other"); n != 0 {
		t.Errorf("a session whose unlock the rotation voided approved a key: %d", n)
	}
	if n := approve(w.session); n != 1 {
		t.Errorf("the session that rotated (and so has the project unlocked) could not approve: %d", n)
	}
}
