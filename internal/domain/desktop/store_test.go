package desktop

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/testutil"
)

var testBox = secrets.New([]byte("desktop-store-test-key-0123456789abcdef"))

func put(t *testing.T, d *db.DB, st State) {
	t.Helper()
	if err := d.WithTx(context.Background(), func(tx *sql.Tx) error { return putJSON(tx, KeyIdP, st) }); err != nil {
		t.Fatal(err)
	}
}

// bind makes userID the account of an identity.
func bind(t *testing.T, d *db.DB, userID, issuer, sub string) {
	t.Helper()
	if err := d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE users SET oidc_issuer = ?, oidc_sub = ? WHERE id = ?`, issuer, sub, userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func identityOf(t *testing.T, d *db.DB, userID string) (issuer, sub string) {
	t.Helper()
	var i, s sql.NullString
	if err := d.Read().QueryRow(`SELECT oidc_issuer, oidc_sub FROM users WHERE id = ?`, userID).Scan(&i, &s); err != nil {
		t.Fatal(err)
	}
	return i.String, s.String
}

func TestTheProfileIsMadeOnce(t *testing.T) {
	d := testutil.DB(t)
	ctx := context.Background()
	if _, ok, err := LoadProfile(ctx, d); ok || err != nil {
		t.Fatalf("a new instance has a profile: %v", err)
	}
	p1, created, err := CreateProfile(ctx, d, "Ada", "ada@example.org")
	if err != nil || !created || p1.Subject == "" || p1.Name != "Ada" {
		t.Fatalf("first profile: %+v %v %v", p1, created, err)
	}
	p2, created, err := CreateProfile(ctx, d, "Mallory", "")
	if err != nil || created || p2 != p1 {
		t.Fatalf("a second profile replaced the first: %+v %v %v", p2, created, err)
	}
	if got, ok, _ := LoadProfile(ctx, d); !ok || got != p1 {
		t.Errorf("loaded %+v", got)
	}
}

// TestThePendingSwitchMovesOnlyTheProvedAccount is the rule of F-390, at the place it is applied.
func TestThePendingSwitchMovesOnlyTheProvedAccount(t *testing.T) {
	ctx := context.Background()
	const extIss, extSub = "https://idp.example.org", "ext-123"
	setup := func(t *testing.T) (*db.DB, string, string, State) {
		d := testutil.DB(t)
		prof, _, _ := CreateProfile(ctx, d, "Ada", "ada@example.org")
		local := testutil.User(t, d, "admin", "ada@example.org")
		bind(t, d, local, LocalIdentity, prof.Subject)
		other := testutil.User(t, d, "writer", "other@example.org")
		sealed, _ := testBox.Seal([]byte("client-secret"), secretContext)
		x := &External{Issuer: extIss, ClientID: "scriobh", Secret: sealed}
		st := State{Mode: ModeEmbedded, External: x,
			Verified: &Verified{Identity: Identity{Issuer: extIss, Subject: extSub, Email: "ada@corp.example"}, UserID: local, Config: x.hash()},
			Pending:  &Pending{To: ModeExternal, By: local}}
		return d, local, other, st
	}

	t.Run("the switch moves the proved account and nothing else", func(t *testing.T) {
		d, local, other, st := setup(t)
		put(t, d, st)
		got, err := ApplyPending(ctx, d, testBox, nil)
		if err != nil || got.Mode != ModeExternal || got.Pending != nil || got.LastProblem != "" || got.Linked == nil || got.Linked.UserID != local {
			t.Fatalf("applied: %+v %v", got, err)
		}
		if iss, sub := identityOf(t, d, local); iss != extIss || sub != extSub {
			t.Errorf("the profile's account is bound to %s %s", iss, sub)
		}
		if iss, _ := identityOf(t, d, other); iss == extIss {
			t.Error("another account was moved")
		}
		c, err := ExternalConfig(got, testBox)
		if err != nil || c.ClientSecret != "client-secret" || c.Issuer != extIss {
			t.Errorf("the provider for the configuration: %+v %v", c, err)
		}
		var n int
		_ = d.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'desktop.idp.applied' AND actor_id = ?`, local).Scan(&n)
		if n != 1 {
			t.Errorf("%d audit rows for the switch", n)
		}
		// A second start changes nothing.
		again, _ := ApplyPending(ctx, d, testBox, nil)
		if again.Mode != ModeExternal || again.Linked == nil {
			t.Errorf("a second start: %+v", again)
		}

		// And back: the same account is the local profile again.
		again.Pending = &Pending{To: ModeEmbedded, By: other}
		put(t, d, again)
		back, err := ApplyPending(ctx, d, nil, nil)
		if err != nil || back.Mode != ModeEmbedded || back.Linked != nil || back.LastProblem != "" {
			t.Fatalf("back: %+v %v", back, err)
		}
		prof, _, _ := LoadProfile(ctx, d)
		if iss, sub := identityOf(t, d, local); iss != LocalIdentity || sub != prof.Subject {
			t.Errorf("after going back the account is bound to %s %s", iss, sub)
		}
		if iss, _ := identityOf(t, d, other); iss == LocalIdentity {
			t.Error("the administrator who asked took the profile although its own account exists")
		}
	})

	refused := map[string]func(t *testing.T, d *db.DB, local, other string, st *State){
		"no proof of sign-in": func(t *testing.T, d *db.DB, local, other string, st *State) { st.Verified = nil },
		"the settings changed after the proof": func(t *testing.T, d *db.DB, local, other string, st *State) {
			st.External.ClientID = "another"
		},
		"the proof is for another issuer": func(t *testing.T, d *db.DB, local, other string, st *State) {
			st.Verified.Issuer = "https://elsewhere.example"
		},
		"the identity is already another account's": func(t *testing.T, d *db.DB, local, other string, st *State) {
			bind(t, d, other, extIss, extSub)
		},
		"the proved account is not the local profile": func(t *testing.T, d *db.DB, local, other string, st *State) {
			st.Verified.UserID = other
		},
		"the account is gone": func(t *testing.T, d *db.DB, local, other string, st *State) {
			_ = d.WithTx(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, db.Now(), local)
				return err
			})
		},
		"the secret cannot be opened": func(t *testing.T, d *db.DB, local, other string, st *State) {
			st.External.Secret = "not-sealed-by-this-key"
			st.Verified.Config = st.External.hash()
		},
	}
	for name, breakIt := range refused {
		t.Run("refused: "+name, func(t *testing.T) {
			d, local, other, st := setup(t)
			breakIt(t, d, local, other, &st)
			put(t, d, st)
			before, _ := identityOf(t, d, local)
			got, err := ApplyPending(ctx, d, testBox, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.Mode != ModeEmbedded || got.Pending != nil || got.LastProblem == "" || got.Linked != nil {
				t.Errorf("the switch was made, or left pending, or not explained: %+v", got)
			}
			if iss, _ := identityOf(t, d, local); iss != before {
				t.Errorf("the profile's account moved from %q to %q", before, iss)
			}
			if stored, _ := LoadState(ctx, d.Read()); stored.Pending != nil || stored.LastProblem == "" {
				t.Errorf("the stored state: %+v", stored)
			}
		})
	}
}

// TestGoingBackWhenTheMovedAccountIsGone: the administrator who asked becomes the local profile, and the recovery
// from the command line (which names nobody) gives it to the oldest administrator.
func TestGoingBackWhenTheMovedAccountIsGone(t *testing.T) {
	ctx := context.Background()
	d := testutil.DB(t)
	prof, _, _ := CreateProfile(ctx, d, "Ada", "")
	gone := testutil.User(t, d, "admin", "gone@example.org")
	bind(t, d, gone, "https://idp.example.org", "ext-1")
	_ = d.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, db.Now(), gone)
		return err
	})
	asker := testutil.User(t, d, "admin", "asker@example.org")
	bind(t, d, asker, "https://idp.example.org", "ext-2")
	st := State{Mode: ModeExternal, External: &External{Issuer: "https://idp.example.org", ClientID: "c"},
		Linked: &Linked{UserID: gone, Identity: Identity{Issuer: "https://idp.example.org", Subject: "ext-1"}}, Pending: &Pending{To: ModeEmbedded, By: asker}}
	put(t, d, st)
	got, err := ApplyPending(ctx, d, nil, nil)
	if err != nil || got.Mode != ModeEmbedded || got.LastProblem != "" {
		t.Fatalf("back: %+v %v", got, err)
	}
	if iss, sub := identityOf(t, d, asker); iss != LocalIdentity || sub != prof.Subject {
		t.Errorf("the administrator who asked is bound to %s %s", iss, sub)
	}

	// From the command line.
	d2 := testutil.DB(t)
	admin := testutil.User(t, d2, "admin", "a@example.org")
	bind(t, d2, admin, "https://idp.example.org", "ext-9")
	testutil.User(t, d2, "writer", "w@example.org")
	put(t, d2, State{Mode: ModeExternal, External: &External{Issuer: "https://idp.example.org", ClientID: "c"}})
	if changed, err := RequestEmbedded(ctx, d2); err != nil || !changed {
		t.Fatalf("RequestEmbedded: %v %v", changed, err)
	}
	got, err = ApplyPending(ctx, d2, nil, nil)
	if err != nil || got.Mode != ModeEmbedded || got.LastProblem != "" {
		t.Fatalf("recovery: %+v %v", got, err)
	}
	p2, ok, _ := LoadProfile(ctx, d2)
	if iss, sub := identityOf(t, d2, admin); !ok || iss != LocalIdentity || sub != p2.Subject || p2.Name == "" {
		t.Errorf("after the recovery the administrator is bound to %s %s, profile %+v", iss, sub, p2)
	}
	if changed, _ := RequestEmbedded(ctx, d2); changed {
		t.Error("the recovery changed an instance that is already embedded")
	}
	// With people but nobody to give the profile to (no administrator), the instance stays as it was and says why:
	// the local profile would sign in as a stranger.
	d3 := testutil.DB(t)
	testutil.User(t, d3, "writer", "only-a-writer@example.org")
	put(t, d3, State{Mode: ModeExternal, External: &External{Issuer: "https://idp.example.org", ClientID: "c"}, Pending: &Pending{To: ModeEmbedded}})
	if got, _ := ApplyPending(ctx, d3, nil, nil); got.Mode != ModeExternal || got.LastProblem == "" {
		t.Errorf("with no administrator: %+v", got)
	}
	// With nobody at all, there is nobody to strand: the instance goes back, and the first run makes the profile.
	d4 := testutil.DB(t)
	put(t, d4, State{Mode: ModeExternal, External: &External{Issuer: "https://idp.example.org", ClientID: "c"}, Pending: &Pending{To: ModeEmbedded}})
	if got, _ := ApplyPending(ctx, d4, nil, nil); got.Mode != ModeEmbedded || got.LastProblem != "" {
		t.Errorf("with no account at all: %+v", got)
	}
	if _, ok, _ := LoadProfile(ctx, d4); ok {
		t.Error("a profile was made for nobody")
	}
}

func TestIssuerValidation(t *testing.T) {
	for issuer, want := range map[string]bool{
		"https://idp.example.org": true, "https://idp.example.org/realms/x": true, "https://idp.example.org/": true,
		"http://127.0.0.1:9000": true, "http://localhost:9000/x": true, "http://[::1]:9000": true,
		"http://idp.example.org": false, "http://192.168.1.5:9000": false, "http://localhost.evil.example": false,
		"ftp://idp.example.org": false, "idp.example.org": false, "": false, "https://": false,
		"https://user:pw@idp.example.org": false, "https://idp.example.org/?x=1": false, "https://idp.example.org/#f": false,
		" https://idp.example.org": false, "javascript:alert(1)": false,
	} {
		if got := validIssuer(issuer); got != want {
			t.Errorf("validIssuer(%q) = %v, want %v", issuer, got, want)
		}
	}
}

func addUser(t *testing.T, d *db.DB, id, role, email, issuer, sub string, extra string) {
	t.Helper()
	now := db.Now()
	var e, i, s any
	if email != "" {
		e = email
	}
	if issuer != "" {
		i, s = issuer, sub
	}
	if _, err := d.Writer().Exec(`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, avatar_color, role, created_at, updated_at, last_seen_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, id, i, s, e, "Name of "+id, "#000000", role, now, now, now); err != nil {
		t.Fatal(err)
	}
	if extra != "" {
		if _, err := d.Writer().Exec(`UPDATE users SET `+extra+` WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdoptAdminBindsOneNamedAdministrator(t *testing.T) {
	ctx := context.Background()
	d := testutil.DB(t)
	addUser(t, d, "usr_w", "writer", "writer@example.org", "https://idp.example.org", "w", "")
	addUser(t, d, "usr_a", "admin", "Ada@Example.org", "https://idp.example.org", "a", "")
	addUser(t, d, "usr_b", "admin", "bea@example.org", "https://idp.example.org", "b", "")
	addUser(t, d, "usr_off", "admin", "off@example.org", "https://idp.example.org", "o", "disabled_at = '2026-01-01T00:00:00Z'")
	addUser(t, d, "usr_gone", "admin", "gone@example.org", "https://idp.example.org", "g", "deleted_at = '2026-01-01T00:00:00Z'")
	addUser(t, d, "usr_bot", "admin", "bot@example.org", "https://idp.example.org", "t", "is_agent = 1")
	now := db.Now()
	if _, err := d.Writer().Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES ('ses_1','usr_a','h',?,?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}

	// Refused: nothing, a stranger, a writer, a switched-off account, a removed one, an agent, a name that is a fragment.
	for _, who := range []string{"", "nobody@example.org", "usr_nope", "usr_w", "writer@example.org", "usr_off", "usr_gone", "usr_bot", "example.org", "Ada"} {
		if _, err := AdoptAdmin(ctx, d, who); err == nil {
			t.Errorf("AdoptAdmin(%q) was accepted", who)
		}
	}
	for _, id := range []string{"usr_w", "usr_a", "usr_b", "usr_off", "usr_gone", "usr_bot"} {
		if i, _ := identityOf(t, d, id); i != "https://idp.example.org" {
			t.Errorf("a refused call moved %s to %q", id, i)
		}
	}

	// By email, exact up to case.
	got, err := AdoptAdmin(ctx, d, "ada@example.ORG")
	if err != nil || got.UserID != "usr_a" {
		t.Fatalf("adopt by email: %+v %v", got, err)
	}
	p, ok, _ := LoadProfile(ctx, d)
	if !ok || p.Subject == "" || p.Email != "ada@example.org" {
		t.Fatalf("the profile: %+v %v", p, ok)
	}
	if i, s := identityOf(t, d, "usr_a"); i != LocalIdentity || s != p.Subject {
		t.Errorf("the account's identity: %q %q", i, s)
	}
	if i, s := identityOf(t, d, "usr_b"); i != "https://idp.example.org" || s != "b" {
		t.Errorf("another administrator was touched: %q %q", i, s)
	}
	var revoked sql.NullString
	if err := d.Read().QueryRow(`SELECT revoked_at FROM sessions WHERE id = 'ses_1'`).Scan(&revoked); err != nil || !revoked.Valid {
		t.Errorf("the account's sessions were not ended: %v %v", revoked, err)
	}
	var n int
	if err := d.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'desktop.profile.adopt' AND target_id = 'usr_a' AND actor_id IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Errorf("audit rows: %d %v", n, err)
	}
	// Once the profile has an account, a second adoption is refused and moves nothing.
	if _, err := AdoptAdmin(ctx, d, "usr_b"); err == nil {
		t.Error("a second administrator was bound to the same profile")
	}
	if i, _ := identityOf(t, d, "usr_b"); i != "https://idp.example.org" {
		t.Errorf("the refused second call moved usr_b to %q", i)
	}
}

func TestAdoptAdminKeepsAnExistingProfileAndRefusesTheWrongMode(t *testing.T) {
	ctx := context.Background()
	d := testutil.DB(t)
	addUser(t, d, "usr_a", "admin", "", "https://idp.example.org", "a", "") // an account with no address
	made, _, err := CreateProfile(ctx, d, "Me", "me@example.org")           // the form the person filled in before being refused
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdoptAdmin(ctx, d, "usr_a"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := LoadProfile(ctx, d)
	if p.Subject != made.Subject || p.Name != "Me" || p.Email != "" {
		t.Errorf("an existing profile keeps its subject and name, and takes the account's address (none): %+v", p)
	}
	// A switch waiting, or an external provider in use: refused, pointing at the command that settles those.
	d2 := testutil.DB(t)
	addUser(t, d2, "usr_a", "admin", "a@example.org", "https://idp.example.org", "a", "")
	put(t, d2, State{Mode: ModeEmbedded, Pending: &Pending{To: ModeExternal}})
	if _, err := AdoptAdmin(ctx, d2, "usr_a"); err == nil {
		t.Error("adopted with a switch waiting")
	}
	put(t, d2, State{Mode: ModeExternal})
	if _, err := AdoptAdmin(ctx, d2, "usr_a"); err == nil {
		t.Error("adopted while the instance signs in through an external provider")
	}
	if i, _ := identityOf(t, d2, "usr_a"); i != "https://idp.example.org" {
		t.Errorf("a refused call moved the account to %q", i)
	}
}
