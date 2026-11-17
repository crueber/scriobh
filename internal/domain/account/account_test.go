package account_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type fx struct {
	t   *testing.T
	d   *db.DB
	reg *ops.Registry
	ctx context.Context
	a   ops.Principal
	b   ops.Principal
}

func newFx(t *testing.T) *fx {
	t.Helper()
	d := testutil.DB(t)
	f := &fx{t: t, d: d, ctx: context.Background(), reg: registry.Build(d, events.NewStore(d, nil), t.TempDir())}
	f.a = ops.Principal{UserID: testutil.User(t, d, "writer", "ada@t.local"), Role: ops.RoleWriter, Via: "session"}
	f.b = ops.Principal{UserID: testutil.User(t, d, "reader", "bob@t.local"), Role: ops.RoleReader, Via: "session"}
	return f
}

func (f *fx) call(p ops.Principal, op string, in any) (any, error) {
	return f.reg.Call(f.ctx, p, op, in)
}

func (f *fx) must(p ops.Principal, op string, in any) any {
	f.t.Helper()
	out, err := f.call(p, op, in)
	if err != nil {
		f.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (f *fx) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func code(err error) ops.Code { return ops.AsError(err).Code }
func field(err error) string  { return ops.AsError(err).Field }
func sp(s string) *string     { return &s }

// session inserts a session row and returns its id.
func (f *fx) session(userID, ua string, created time.Time, mod func(*sessionRow)) string {
	f.t.Helper()
	id := db.NewID("ses")
	r := sessionRow{
		created: db.Format(created), seen: db.Format(created),
		expires: db.Format(created.Add(24 * time.Hour * 365 * 30)),
	}
	if mod != nil {
		mod(&r)
	}
	err := f.d.WithTx(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at, idle_expires_at, user_agent, revoked_at)
			VALUES (?,?,?,?,?,?,?,?,?)`, id, userID, "hash-"+id, r.created, r.seen, r.expires, r.idle, ua, r.revoked)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

type sessionRow struct {
	created, seen, expires string
	idle, revoked          any
}

func (f *fx) sessions(p ops.Principal) []account.Session {
	f.t.Helper()
	return f.must(p, "session.list", account.EmptyIn{}).(account.SessionsOut).Items
}

func ids(ss []account.Session) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.ID)
	}
	return out
}

// ---- profile ----------------------------------------------------------------

func TestProfileGetAndUpdate(t *testing.T) {
	f := newFx(t)
	p := f.must(f.a, "profile.get", account.EmptyIn{}).(account.ProfileItem).Item
	if p.ID != f.a.UserID || p.Email != "ada@t.local" || p.Role != "writer" || p.Timezone != "UTC" || p.Locale != "en" || p.AvatarColor == "" || p.Initials == "" {
		t.Errorf("profile %+v", p)
	}
	out := f.must(f.a, "profile.update", account.UpdateIn{DisplayName: sp("  Ada  Lovelace "), Timezone: sp("Europe/Dublin"), Locale: sp("ga")}).(account.ProfileItem).Item
	if out.DisplayName != "Ada  Lovelace" || out.Timezone != "Europe/Dublin" || out.Locale != "ga" || out.Initials != "AL" {
		t.Errorf("updated %+v", out)
	}
	// A partial update leaves the rest alone.
	out = f.must(f.a, "profile.update", account.UpdateIn{Locale: sp("fr")}).(account.ProfileItem).Item
	if out.DisplayName != "Ada  Lovelace" || out.Timezone != "Europe/Dublin" || out.Locale != "fr" {
		t.Errorf("partial update %+v", out)
	}
	// Another person's profile was not touched.
	if b := f.must(f.b, "profile.get", account.EmptyIn{}).(account.ProfileItem).Item; b.Locale != "en" || b.DisplayName != "bob@t.local" {
		t.Errorf("bob's profile changed: %+v", b)
	}
	// With no display name the initials come from the email.
	var blank = ops.Principal{UserID: testutil.User(t, f.d, "writer", "zed@t.local"), Role: ops.RoleWriter}
	if _, err := f.d.Writer().Exec(`UPDATE users SET display_name = '' WHERE id = ?`, blank.UserID); err != nil {
		t.Fatal(err)
	}
	if got := f.must(blank, "profile.get", account.EmptyIn{}).(account.ProfileItem).Item.Initials; got != "Z" {
		t.Errorf("initials from email = %q", got)
	}
}

func TestProfileValidation(t *testing.T) {
	f := newFx(t)
	cases := []struct {
		name  string
		in    account.UpdateIn
		field string
	}{
		{"empty name", account.UpdateIn{DisplayName: sp("  ")}, "display_name"},
		{"long name", account.UpdateIn{DisplayName: sp(strings.Repeat("é", 81))}, "display_name"},
		{"newline in name", account.UpdateIn{DisplayName: sp("a\nb")}, "display_name"},
		{"tab in name", account.UpdateIn{DisplayName: sp("a\tb")}, "display_name"},
		{"nul in name", account.UpdateIn{DisplayName: sp("a\x00b")}, "display_name"},
		{"empty zone", account.UpdateIn{Timezone: sp("")}, "timezone"},
		{"Local zone", account.UpdateIn{Timezone: sp("Local")}, "timezone"},
		{"made-up zone", account.UpdateIn{Timezone: sp("Mars/Olympus")}, "timezone"},
		{"zone path trick", account.UpdateIn{Timezone: sp("../../etc/passwd")}, "timezone"},
		{"unknown locale", account.UpdateIn{Locale: sp("xx")}, "locale"},
		{"empty locale", account.UpdateIn{Locale: sp("")}, "locale"},
		{"region locale", account.UpdateIn{Locale: sp("en-GB")}, "locale"},
	}
	for _, tc := range cases {
		if _, err := f.call(f.a, "profile.update", tc.in); code(err) != ops.CodeInvalid || field(err) != tc.field {
			t.Errorf("%s: %v, want invalid on %s", tc.name, err, tc.field)
		}
	}
	for _, l := range account.Locales {
		if _, err := f.call(f.a, "profile.update", account.UpdateIn{Locale: sp(l)}); err != nil {
			t.Errorf("locale %s: %v", l, err)
		}
	}
	for _, z := range []string{"UTC", "America/New_York", "Asia/Kolkata"} {
		if _, err := f.call(f.a, "profile.update", account.UpdateIn{Timezone: sp(z)}); err != nil {
			t.Errorf("zone %s: %v", z, err)
		}
	}
	if _, err := f.call(f.a, "profile.update", account.UpdateIn{DisplayName: sp(strings.Repeat("é", 80))}); err != nil {
		t.Errorf("80 characters is fine: %v", err)
	}
}

func TestProfileUpdateAuditsFieldsNotValues(t *testing.T) {
	f := newFx(t)
	f.must(f.a, "profile.update", account.UpdateIn{DisplayName: sp("Secret Name"), Locale: sp("de")})
	var detail, actor string
	if err := f.d.Read().QueryRow(`SELECT detail_json, actor_id FROM audit_log WHERE action = 'profile.update'`).Scan(&detail, &actor); err != nil {
		t.Fatal(err)
	}
	if actor != f.a.UserID || strings.Contains(detail, "Secret") || strings.Contains(detail, "de\"") || !strings.Contains(detail, "display_name") {
		t.Errorf("audit detail %s actor %s", detail, actor)
	}
	// A no-op update writes no audit row.
	f.must(f.a, "profile.update", account.UpdateIn{})
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action = 'profile.update'`); n != 1 {
		t.Errorf("%d audit rows after a no-op", n)
	}
}

// ---- prefs ------------------------------------------------------------------

func TestPrefsDefaultsAndSet(t *testing.T) {
	f := newFx(t)
	want := map[string]string{
		"theme": "system", "theme_light": "paper", "theme_dark": "ink", "editor_font": "serif", "editor_size": "19",
		"editor_measure": "66", "editor_line_height": "1.65", "reduced_motion": "system", "autosave_ms": "700",
		"show_word_count": "true", "density": "comfortable", "typewriter": "false", "focus_dim": "false", "language": "en",
		"presence.share": "false", // F-244: off for a new account
		"studio.nudges":  "true",  // F-508: the writing studio's gentle nudges, on and quiet
	}
	got := f.must(f.a, "prefs.get", account.EmptyIn{}).(account.PrefsOut).Items
	if len(got) != len(want) {
		t.Errorf("defaults %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("default %s = %q, want %q", k, got[k], v)
		}
	}
	if len(account.PrefKeys()) != len(want) {
		t.Errorf("PrefKeys = %v", account.PrefKeys())
	}

	out := f.must(f.a, "prefs.set", account.SetPrefsIn{Items: map[string]string{
		"theme": "dark", "editor_font": "mono", "editor_size": "022", "editor_measure": "full", "editor_line_height": "1.45", "typewriter": "true",
	}}).(account.PrefsOut).Items
	if out["theme"] != "dark" || out["editor_font"] != "mono" || out["editor_size"] != "22" || out["editor_measure"] != "full" ||
		out["editor_line_height"] != "1.45" || out["typewriter"] != "true" || out["density"] != "comfortable" {
		t.Errorf("after set: %v", out)
	}
	// What the server's page handlers read matches.
	if page := account.Prefs(f.ctx, f.d, f.a.UserID); page["editor_size"] != "22" || page["theme"] != "dark" || page["focus_dim"] != "false" {
		t.Errorf("Prefs() = %v", page)
	}
	// Setting again overwrites; keys not sent stay.
	out = f.must(f.a, "prefs.set", account.SetPrefsIn{Items: map[string]string{"theme": "light"}}).(account.PrefsOut).Items
	if out["theme"] != "light" || out["editor_font"] != "mono" {
		t.Errorf("second set: %v", out)
	}
	if n := f.count(`SELECT count(*) FROM user_prefs WHERE user_id = ? AND key = 'theme'`, f.a.UserID); n != 1 {
		t.Errorf("theme stored %d times", n)
	}
	// Stored as the canonical text.
	var raw string
	if err := f.d.Read().QueryRow(`SELECT value FROM user_prefs WHERE user_id = ? AND key = 'editor_size'`, f.a.UserID).Scan(&raw); err != nil || raw != "22" {
		t.Errorf("stored %q %v", raw, err)
	}
}

func TestPrefsAreIsolatedPerUser(t *testing.T) {
	f := newFx(t)
	f.must(f.a, "prefs.set", account.SetPrefsIn{Items: map[string]string{"theme": "dark", "autosave_ms": "2000"}})
	b := f.must(f.b, "prefs.get", account.EmptyIn{}).(account.PrefsOut).Items
	if b["theme"] != "system" || b["autosave_ms"] != "700" {
		t.Errorf("bob sees ada's prefs: %v", b)
	}
	f.must(f.b, "prefs.set", account.SetPrefsIn{Items: map[string]string{"theme": "light"}})
	if a := f.must(f.a, "prefs.get", account.EmptyIn{}).(account.PrefsOut).Items; a["theme"] != "dark" {
		t.Errorf("bob changed ada's prefs: %v", a)
	}
}

func TestPrefsValidationTable(t *testing.T) {
	f := newFx(t)
	good := map[string][]string{
		"theme":              {"system", "light", "dark"},
		"theme_light":        {"paper", "sepia"},
		"theme_dark":         {"ink", "slate", "moss"},
		"editor_font":        {"serif", "sans", "mono"},
		"editor_size":        {"16", "19", "24"},
		"editor_measure":     {"52", "66", "82", "full"},
		"editor_line_height": {"1.4", "1.45", "1.5", "1.65", "1.7", "1.9"},
		"reduced_motion":     {"system", "on", "off"},
		"autosave_ms":        {"300", "700", "5000"},
		"show_word_count":    {"true", "false"},
		"density":            {"comfortable", "compact", "spacious"},
		"typewriter":         {"true", "false"},
		"focus_dim":          {"true", "false"},
	}
	bad := map[string][]string{
		"theme":              {"", "auto", "Dark", "dark "},
		"theme_light":        {"ink", "slate", "Sepia", "neon", ""},
		"theme_dark":         {"midnight", "paper", "sepia", "Moss", ""},
		"editor_font":        {"comic", "Serif"},
		"editor_size":        {"15", "25", "", "abc", "19.5", "-19", "9999999999999999999999"},
		"editor_measure":     {"51", "83", "wide", "", "66ch"},
		"editor_line_height": {"1.35", "1.95", "1.66", "1.40", "1.450", "2", "", "x", "1,5"},
		"reduced_motion":     {"yes", "true"},
		"autosave_ms":        {"299", "5001", "fast", "0"},
		"show_word_count":    {"yes", "1", "True"},
		"density":            {"tight"},
		"typewriter":         {"on"},
		"focus_dim":          {"1"},
	}
	for k, vs := range good {
		for _, v := range vs {
			if _, err := f.call(f.a, "prefs.set", account.SetPrefsIn{Items: map[string]string{k: v}}); err != nil {
				t.Errorf("%s=%q: %v", k, v, err)
			}
		}
	}
	for k, vs := range bad {
		for _, v := range vs {
			if _, err := f.call(f.a, "prefs.set", account.SetPrefsIn{Items: map[string]string{k: v}}); code(err) != ops.CodeInvalid || field(err) != k {
				t.Errorf("%s=%q: %v, want invalid on %s", k, v, err, k)
			}
		}
	}
	// Every line height from 1.4 to 1.9 in 0.05 steps is accepted: 11 values.
	n := 0
	for h := 140; h <= 190; h += 5 {
		v := strings.TrimRight(strings.TrimRight(formatHundredths(h), "0"), ".")
		if _, err := f.call(f.a, "prefs.set", account.SetPrefsIn{Items: map[string]string{"editor_line_height": v}}); err != nil {
			t.Errorf("line height %s: %v", v, err)
		}
		n++
	}
	if n != 11 {
		t.Errorf("%d steps", n)
	}
	// An unknown key, and a shell of a request.
	if _, err := f.call(f.a, "prefs.set", account.SetPrefsIn{Items: map[string]string{"nonsense": "1"}}); code(err) != ops.CodeInvalid || field(err) != "nonsense" {
		t.Errorf("unknown key: %v", err)
	}
	if _, err := f.call(f.a, "prefs.set", account.SetPrefsIn{}); code(err) != ops.CodeInvalid {
		t.Errorf("empty set: %v", err)
	}
}

func formatHundredths(h int) string {
	return string(rune('0'+h/100)) + "." + string(rune('0'+h/10%10)) + string(rune('0'+h%10))
}

func TestPrefsSetIsAllOrNothing(t *testing.T) {
	f := newFx(t)
	f.must(f.a, "prefs.set", account.SetPrefsIn{Items: map[string]string{"theme": "dark"}})
	_, err := f.call(f.a, "prefs.set", account.SetPrefsIn{Items: map[string]string{"theme": "light", "editor_size": "99", "density": "compact"}})
	if code(err) != ops.CodeInvalid || field(err) != "editor_size" {
		t.Fatalf("got %v", err)
	}
	got := account.Prefs(f.ctx, f.d, f.a.UserID)
	if got["theme"] != "dark" || got["density"] != "comfortable" {
		t.Errorf("a failed call changed prefs: %v", got)
	}
}

func TestPrefsReadFallsBackFromStaleValues(t *testing.T) {
	f := newFx(t)
	for k, v := range map[string]string{"theme": "neon", "legacy_key": "x", "density": "compact"} {
		if _, err := f.d.Writer().Exec(`INSERT INTO user_prefs(user_id, key, value) VALUES (?,?,?)`, f.a.UserID, k, v); err != nil {
			t.Fatal(err)
		}
	}
	got := account.Prefs(f.ctx, f.d, f.a.UserID)
	if got["theme"] != "system" || got["density"] != "compact" {
		t.Errorf("stale value handling: %v", got)
	}
	if _, ok := got["legacy_key"]; ok {
		t.Error("an unknown stored key leaked into the map")
	}
	// Defaults() must not be shared mutable state.
	account.Defaults()["theme"] = "dark"
	if account.Defaults()["theme"] != "system" {
		t.Error("Defaults returns a shared map")
	}
}

// ---- sessions -----------------------------------------------------------------

func TestSessionListShowsLiveSessionsNewestFirstWithCurrent(t *testing.T) {
	f := newFx(t)
	now := time.Now().UTC()
	oldest := f.session(f.a.UserID, "Firefox", now.Add(-3*time.Hour), nil)
	middle := f.session(f.a.UserID, "Safari", now.Add(-2*time.Hour), nil)
	newest := f.session(f.a.UserID, "Chrome "+strings.Repeat("é", 200), now.Add(-1*time.Hour), nil)
	f.session(f.a.UserID, "revoked", now.Add(-30*time.Minute), func(r *sessionRow) { r.revoked = db.Format(now) })
	f.session(f.a.UserID, "expired", now.Add(-40*time.Minute), func(r *sessionRow) { r.expires = db.Format(now.Add(-time.Minute)) })
	f.session(f.a.UserID, "idle", now.Add(-50*time.Minute), func(r *sessionRow) { r.idle = db.Format(now.Add(-time.Minute)) })
	f.session(f.b.UserID, "bobs", now, nil)

	a := f.a
	a.SessionID = middle
	items := f.sessions(a)
	if got := ids(items); len(got) != 3 || got[0] != newest || got[1] != middle || got[2] != oldest {
		t.Fatalf("ids %v, want [%s %s %s]", got, newest, middle, oldest)
	}
	for _, s := range items {
		if s.Current != (s.ID == middle) {
			t.Errorf("current flag wrong on %s", s.ID)
		}
		if n := len([]rune(s.UserAgent)); n > 120 {
			t.Errorf("user agent %d characters", n)
		}
		if s.CreatedAt == "" || s.LastSeenAt == "" {
			t.Errorf("session %+v", s)
		}
	}
	if len([]rune(items[0].UserAgent)) != 120 || !strings.HasPrefix(items[0].UserAgent, "Chrome ") {
		t.Errorf("long agent not trimmed to 120 runes: %q", items[0].UserAgent)
	}
	// With no session of its own (an API key) nothing is current.
	for _, s := range f.sessions(f.a) {
		if s.Current {
			t.Error("current set without a session id")
		}
	}
}

func TestSessionListNeverLeaksSecrets(t *testing.T) {
	f := newFx(t)
	f.session(f.a.UserID, "Browser", time.Now(), nil)
	out := f.must(f.a, "session.list", account.EmptyIn{})
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "hash") || strings.Contains(string(raw), "token") {
		t.Errorf("secret material in %s", raw)
	}
	var generic map[string][]map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"id": true, "created_at": true, "last_seen_at": true, "user_agent": true, "current": true}
	for k := range generic["items"][0] {
		if !want[k] {
			t.Errorf("unexpected field %q", k)
		}
	}
}

func TestSessionRevokeIsPerUser(t *testing.T) {
	f := newFx(t)
	now := time.Now().UTC()
	mine := f.session(f.a.UserID, "mine", now, nil)
	current := f.session(f.a.UserID, "current", now, nil)
	bobs := f.session(f.b.UserID, "bobs", now, nil)
	a := f.a
	a.SessionID = current

	// Another person's session, and one that does not exist, are the same not_found.
	for _, id := range []string{bobs, "ses_00000000000000000000000000"} {
		if _, err := f.call(a, "session.revoke", account.RevokeIn{SID: id}); code(err) != ops.CodeNotFound {
			t.Errorf("revoke %s: %v, want not_found", id, err)
		}
	}
	if len(f.sessions(f.b)) != 1 {
		t.Error("ada revoked bob's session")
	}
	// Not shaped like a session id: invalid, before the database is touched.
	for _, id := range []string{"", "prj_x", "x' OR 1=1 --", strings.Repeat("ses_", 40)} {
		if _, err := f.call(a, "session.revoke", account.RevokeIn{SID: id}); code(err) != ops.CodeInvalid || field(err) != "sid" {
			t.Errorf("revoke %q: %v", id, err)
		}
	}

	out := f.must(a, "session.revoke", account.RevokeIn{SID: mine}).(account.RevokeOut)
	if !out.Revoked || out.Current || out.ID != mine {
		t.Errorf("out %+v", out)
	}
	if got := ids(f.sessions(a)); len(got) != 1 || got[0] != current {
		t.Errorf("after revoke: %v", got)
	}
	// Revoking again is quiet and writes no second audit row.
	f.must(a, "session.revoke", account.RevokeIn{SID: mine})
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action = 'session.revoke' AND target_id = ?`, mine); n != 1 {
		t.Errorf("%d audit rows", n)
	}
	var actor, detail string
	if err := f.d.Read().QueryRow(`SELECT actor_id, detail_json FROM audit_log WHERE action = 'session.revoke'`).Scan(&actor, &detail); err != nil || actor != f.a.UserID || detail != "{}" {
		t.Errorf("audit %q %q %v", actor, detail, err)
	}

	// Revoking the current session is allowed.
	out = f.must(a, "session.revoke", account.RevokeIn{SID: current}).(account.RevokeOut)
	if !out.Current || !out.Revoked {
		t.Errorf("out %+v", out)
	}
	if got := f.sessions(a); len(got) != 0 {
		t.Errorf("sessions after revoking current: %v", got)
	}
	// The revoked session is really dead: the auth lookup predicate agrees.
	if n := f.count(`SELECT count(*) FROM sessions WHERE id = ? AND revoked_at IS NOT NULL`, current); n != 1 {
		t.Error("the session row was not marked revoked")
	}
}

func TestSessionRevokeOthers(t *testing.T) {
	f := newFx(t)
	now := time.Now().UTC()
	keep := f.session(f.a.UserID, "this one", now, nil)
	f.session(f.a.UserID, "other 1", now, nil)
	f.session(f.a.UserID, "other 2", now, nil)
	f.session(f.a.UserID, "already revoked", now, func(r *sessionRow) { r.revoked = db.Format(now) })
	bobs := f.session(f.b.UserID, "bobs", now, nil)

	a := f.a
	a.SessionID = keep
	out := f.must(a, "session.revoke_others", account.EmptyIn{}).(account.RevokeOthersOut)
	if out.Revoked != 2 {
		t.Errorf("revoked %d, want 2 (already-revoked ones are not counted)", out.Revoked)
	}
	if got := ids(f.sessions(a)); len(got) != 1 || got[0] != keep {
		t.Errorf("remaining %v", got)
	}
	if got := ids(f.sessions(f.b)); len(got) != 1 || got[0] != bobs {
		t.Errorf("bob's sessions touched: %v", got)
	}
	var detail string
	if err := f.d.Read().QueryRow(`SELECT detail_json FROM audit_log WHERE action = 'session.revoke_others'`).Scan(&detail); err != nil || detail != `{"count":2}` {
		t.Errorf("audit %q %v", detail, err)
	}
	// A second call has nothing left to do.
	if out := f.must(a, "session.revoke_others", account.EmptyIn{}).(account.RevokeOthersOut); out.Revoked != 0 {
		t.Errorf("second call revoked %d", out.Revoked)
	}
	// A caller with no session of its own (an API key) has none to keep.
	f.session(f.a.UserID, "another", now, nil)
	if out := f.must(f.a, "session.revoke_others", account.EmptyIn{}).(account.RevokeOthersOut); out.Revoked != 2 {
		t.Errorf("sessionless caller revoked %d, want 2", out.Revoked)
	}
	if got := f.sessions(f.b); len(got) != 1 {
		t.Error("bob's session was revoked by ada's key")
	}
}

func TestAccountOpsNeedASignedInPrincipal(t *testing.T) {
	f := newFx(t)
	anon := ops.Principal{}
	for _, tc := range []struct {
		op string
		in any
	}{
		{"profile.get", account.EmptyIn{}}, {"profile.update", account.UpdateIn{}}, {"prefs.get", account.EmptyIn{}},
		{"prefs.set", account.SetPrefsIn{Items: map[string]string{"theme": "dark"}}}, {"session.list", account.EmptyIn{}},
		{"session.revoke", account.RevokeIn{SID: "ses_x"}}, {"session.revoke_others", account.EmptyIn{}},
	} {
		if _, err := f.call(anon, tc.op, tc.in); code(err) != ops.CodeUnauthenticated {
			t.Errorf("%s anonymous: %v", tc.op, err)
		}
		// A key pinned to one project cannot reach account ops.
		pinned := f.a
		pinned.ProjectID = "prj_x"
		if _, err := f.call(pinned, tc.op, tc.in); code(err) != ops.CodeForbidden {
			t.Errorf("%s pinned key: %v", tc.op, err)
		}
	}
}
