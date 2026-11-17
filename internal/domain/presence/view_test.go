package presence_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/presence"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

func (w *world) everyoneShares() {
	for _, p := range []*person{w.owner, w.editor, w.reviewer, w.reader} {
		w.share(p, "true")
	}
}

// Five roles, one project, each person on a different chapter. What each is told, by name and by document:
//
//	owner     editor, reviewer, reader with their chapters
//	editor    owner and reviewer with chapters; no reader
//	reviewer  owner and editor by name only; no reader; no chapters
//	reader    no one
func TestWhatEachRoleIsToldAboutTheOthers(t *testing.T) {
	w := newWorld(t)
	w.everyoneShares()
	w.must(w.beat(w.owner, w.docA))
	w.must(w.beat(w.editor, w.docB))
	w.must(w.beat(w.reviewer, w.docA))
	w.must(w.beat(w.reader, w.docB))

	own := w.must(w.list(w.owner))
	if !reflect.DeepEqual(names(own), []string{"Edgar", "Rhea", "Rory"}) {
		t.Fatalf("owner sees %v", names(own))
	}
	if d := docOf(own, "Edgar"); d == nil || d.Title != titleB || d.ID != w.docB {
		t.Errorf("owner is not told Edgar's chapter: %+v", d)
	}
	if d := docOf(own, "Rory"); d == nil || d.Title != titleB {
		t.Errorf("owner is not told the reader's chapter: %+v", d)
	}
	if !own.Names || !own.Chapters {
		t.Errorf("owner flags: %+v", own)
	}

	ed := w.must(w.list(w.editor))
	if !reflect.DeepEqual(names(ed), []string{"Olive", "Rhea"}) {
		t.Fatalf("editor sees %v (a reader is not told to an editor)", names(ed))
	}
	if d := docOf(ed, "Olive"); d == nil || d.Title != titleA {
		t.Errorf("editor is not told the owner's chapter: %+v", d)
	}

	rv := w.must(w.list(w.reviewer))
	if !reflect.DeepEqual(names(rv), []string{"Edgar", "Olive"}) {
		t.Fatalf("reviewer sees %v", names(rv))
	}
	if rv.Chapters || docOf(rv, "Olive") != nil || docOf(rv, "Edgar") != nil {
		t.Errorf("a reviewer is told who is here, not where: %s", raw(rv))
	}
	for _, c := range []string{titleA, titleB, w.docA, w.docB} {
		if strings.Contains(raw(rv), c) {
			t.Errorf("the reviewer's answer carries %q", c)
		}
	}

	rd := w.must(w.list(w.reader))
	if len(rd.Items) != 0 || rd.Names || !rd.Enabled {
		t.Fatalf("a reader is told no one: %s", raw(rd))
	}
	for _, c := range []string{"Olive", "Edgar", "Rhea", titleA, titleB, w.docA, w.docB} {
		if strings.Contains(raw(rd), c) {
			t.Errorf("the reader's answer carries %q", c)
		}
	}

	// the viewer is never in their own list, and every answer carries only the documented fields
	for who, v := range map[string]presence.View{"owner": own, "editor": ed, "reviewer": rv} {
		for _, it := range v.Items {
			if strings.Contains(strings.ToLower(it.Name), "@") {
				t.Errorf("%s: an address in a name: %q", who, it.Name)
			}
		}
	}
	if strings.Contains(raw(own), "Olive") {
		t.Error("the owner is listed to themselves")
	}
}

// A person who is not on the project is never told anything and never recorded, however they are authorized: an
// instance administrator acts as an owner everywhere else, but is not a member; a stranger gets the 404 a missing
// project gives.
func TestNonMembersAreNeitherShownNorTold(t *testing.T) {
	w := newWorld(t)
	w.everyoneShares()
	w.share(w.admin, "true")
	w.must(w.beat(w.editor, w.docA))
	before := w.svc.Len()
	v := w.must(w.beat(w.admin, w.docA))
	if v.Enabled || v.Reason != presence.ReasonNotMember || len(v.Items) != 0 {
		t.Fatalf("an administrator who is not a member: %s", raw(v))
	}
	if w.svc.Len() != before {
		t.Fatal("an administrator who is not a member was recorded")
	}
	if l := w.must(w.list(w.owner)); strings.Contains(raw(l), "Adam") {
		t.Fatal("an administrator who is not a member is shown")
	}
	if _, err := w.beat(w.stranger, w.docA); ops.AsError(err).Code != ops.CodeNotFound {
		t.Fatalf("a stranger: %v, want the 404 a missing project gives", err)
	}
	if _, err := w.list(w.stranger); ops.AsError(err).Code != ops.CodeNotFound {
		t.Fatalf("a stranger's list: %v", err)
	}
}

// A chapter a viewer is not allowed to read never appears. The canaries are titles and ids of: a document of another
// project, a document in the trash, and (for the reviewer and the reader) a real chapter of this one.
func TestCanaryNoChapterTheViewerCannotReadEverAppears(t *testing.T) {
	w := newWorld(t)
	w.everyoneShares()
	// a foreign document id and a trashed one are accepted without complaint and recorded as "no document"
	w.must(w.beat(w.editor, w.docForeign))
	w.must(w.beat(w.reviewer, w.docTrashed))
	w.must(w.beat(w.reader, w.docA))
	w.must(w.beat(w.owner, w.docB))
	for _, viewer := range []*person{w.owner, w.editor, w.reviewer, w.reader} {
		v := w.must(w.list(viewer))
		s := raw(v)
		for _, c := range []string{titleForeign, w.docForeign, titleTrashed, w.docTrashed} {
			if strings.Contains(s, c) {
				t.Errorf("%s is told about %q: %s", viewer.name, c, s)
			}
		}
	}
	if d := docOf(w.must(w.list(w.owner)), "Edgar"); d != nil {
		t.Errorf("a foreign document was recorded for the editor: %+v", d)
	}
	// a chapter put in the trash after someone opened it stops being named at once
	w.must(w.beat(w.editor, w.docB))
	if d := docOf(w.must(w.list(w.owner)), "Edgar"); d == nil || d.ID != w.docB {
		t.Fatalf("setup: %+v", d)
	}
	trashDoc(t, w, w.docB)
	v := w.must(w.list(w.owner))
	if d := docOf(v, "Edgar"); d != nil || strings.Contains(raw(v), titleB) {
		t.Fatalf("a chapter in the trash is still named: %s", raw(v))
	}
	// the answer to a malformed id is an error that says nothing about documents
	if _, err := w.reg.Call(context.Background(), w.editor.p, "presence.beat", presence.BeatIn{ID: w.pid, DocumentID: "not an id"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Fatalf("malformed document id: %v", err)
	}
}

// Sharing is symmetric: switched off, a person is not recorded, not shown and shown no one.
func TestPresenceShareIsSymmetric(t *testing.T) {
	w := newWorld(t)
	w.share(w.owner, "true")
	// the editor never switched it on: the account's default is off
	v := w.must(w.beat(w.editor, w.docA))
	if v.Enabled || v.Reason != presence.ReasonOff || len(v.Items) != 0 {
		t.Fatalf("a person who has not switched it on: %s", raw(v))
	}
	if l := w.must(w.beat(w.owner, w.docA)); len(l.Items) != 0 {
		t.Fatalf("the owner is shown someone who does not share: %s", raw(l))
	}
	if got := w.must(w.list(w.editor)); got.Enabled || len(got.Items) != 0 {
		t.Fatalf("the editor sees the owner while sharing nothing: %s", raw(got))
	}
	// on: both see each other
	w.share(w.editor, "true")
	w.must(w.beat(w.editor, w.docA))
	if !reflect.DeepEqual(names(w.must(w.list(w.owner))), []string{"Edgar"}) || !reflect.DeepEqual(names(w.must(w.list(w.editor))), []string{"Olive"}) {
		t.Fatal("two people who share do not see each other")
	}
	// off again: gone from the owner's list at once, and they see no one (the preferences op itself is tested in the server package)
	w.share(w.editor, "false")
	if len(w.must(w.list(w.owner)).Items) != 0 {
		t.Fatal("a person who switched it off is still shown")
	}
	if len(w.must(w.list(w.editor)).Items) != 0 {
		t.Fatal("a person who switched it off still sees others")
	}
}

// Expiry: entries leave 60 seconds after the last beat (the clock is injected), on leave, and when the session, the
// account, the membership or the switch that made them stops standing.
func TestEntriesStopBeingShown(t *testing.T) {
	w := newWorld(t)
	w.everyoneShares()
	now := time.Now()
	w.svc.SetClock(func() time.Time { return now })
	w.must(w.beat(w.editor, w.docA))
	w.must(w.beat(w.reviewer, w.docA))
	w.must(w.beat(w.owner, w.docA))
	shown := func() []string { return names(w.must(w.list(w.owner))) }
	if !reflect.DeepEqual(shown(), []string{"Edgar", "Rhea"}) {
		t.Fatalf("setup: %v", shown())
	}
	now = now.Add(59 * time.Second)
	w.must(w.beat(w.owner, w.docA))
	if len(shown()) != 2 {
		t.Fatal("gone before 60 seconds")
	}
	now = now.Add(2 * time.Second)
	if len(shown()) != 0 {
		t.Fatalf("still shown after 61 seconds: %v", shown())
	}

	// leave
	w.must(w.beat(w.editor, w.docA))
	if _, err := w.reg.Call(context.Background(), w.editor.p, "presence.leave", presence.LeaveIn{ID: w.pid}); err != nil {
		t.Fatal(err)
	}
	if len(shown()) != 0 {
		t.Fatal("still shown after leaving")
	}

	// sign-out: the session is revoked in the database (any path does that), and the entry is no longer shown
	w.must(w.beat(w.editor, w.docA))
	if _, err := w.d.Writer().Exec(`UPDATE sessions SET revoked_at = ? WHERE id = ?`, db.Now(), w.editor.p.SessionID); err != nil {
		t.Fatal(err)
	}
	if len(shown()) != 0 {
		t.Fatal("a person whose session was revoked is still shown")
	}
	// disabled account
	w.must(w.beat(w.reviewer, w.docA))
	if len(shown()) != 1 {
		t.Fatal("setup")
	}
	if _, err := w.d.Writer().Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, db.Now(), w.reviewer.p.UserID); err != nil {
		t.Fatal(err)
	}
	if len(shown()) != 0 {
		t.Fatal("a disabled person is still shown")
	}
	// removed from the project
	w.reviewer.p.SessionID = w.session(w.reviewer.p.UserID)
	if _, err := w.d.Writer().Exec(`UPDATE users SET disabled_at = NULL WHERE id = ?`, w.reviewer.p.UserID); err != nil {
		t.Fatal(err)
	}
	w.must(w.beat(w.reviewer, w.docA))
	if len(shown()) != 1 {
		t.Fatal("setup 2")
	}
	if _, err := w.d.Writer().Exec(`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, w.pid, w.reviewer.p.UserID); err != nil {
		t.Fatal(err)
	}
	if len(shown()) != 0 {
		t.Fatal("a person removed from the project is still shown")
	}
	if _, err := w.beat(w.reviewer, w.docA); ops.AsError(err).Code != ops.CodeNotFound {
		t.Fatalf("a removed member's beat: %v", err)
	}
}

// An API key neither sends nor sees presence, at any scope.
func TestKeysNeverSendOrSeePresence(t *testing.T) {
	w := newWorld(t)
	w.everyoneShares()
	for _, scope := range []string{ops.ScopeRead, ops.ScopeWrite, ops.ScopeAdmin} {
		k := w.owner.p
		k.Via, k.KeyID, k.SessionID, k.Scope = "apikey", "key_x", "", scope
		for _, op := range []string{"presence.beat", "presence.list", "presence.leave"} {
			var in any = presence.ListIn{ID: w.pid}
			switch op {
			case "presence.beat":
				in = presence.BeatIn{ID: w.pid}
			case "presence.leave":
				in = presence.LeaveIn{ID: w.pid}
			}
			if _, err := w.reg.Call(context.Background(), k, op, in); ops.AsError(err).Code != ops.CodeForbidden {
				t.Errorf("%s with a %s key: %v, want forbidden", op, scope, err)
			}
		}
	}
	// the operator's shell passes the session-only check but has no session: nothing is recorded for it
	cli := w.owner.p
	cli.Via, cli.SessionID = "cli", ""
	before := w.svc.Len()
	v, err := w.reg.Call(context.Background(), cli, "presence.beat", presence.BeatIn{ID: w.pid})
	if err != nil || v.(presence.View).Enabled || w.svc.Len() != before {
		t.Fatalf("a beat with no session recorded something: %v %+v", err, v)
	}
}

// A project with a passphrase: a session that has not unlocked it is refused, sends nothing and sees nothing; a person
// who unlocks, beats, and then locks is gone from the others' lists at once.
func TestLockedProjectShowsNothingAndRefusesBeats(t *testing.T) {
	w := newWorld(t)
	w.everyoneShares()
	ctx := context.Background()
	if _, err := w.reg.Call(ctx, w.owner.p, "project.passphrase.set", projectgate.SetIn{ID: w.pid, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	// the owner's session that set it is unlocked; the editor's is not
	if _, err := w.beat(w.editor, w.docA); !isLocked(err) {
		t.Fatalf("a locked session's beat: %v, want project_locked", err)
	}
	if _, err := w.list(w.editor); !isLocked(err) {
		t.Fatalf("a locked session's list: %v", err)
	}
	if w.svc.Len() != 0 {
		t.Fatal("a refused beat was recorded")
	}
	w.must(w.beat(w.owner, w.docA))
	if v := w.must(w.list(w.owner)); len(v.Items) != 0 {
		t.Fatalf("the owner sees %v", names(v))
	}
	// the editor unlocks and beats: the owner sees them
	if _, err := w.reg.Call(ctx, w.editor.p, "project.unlock", projectgate.UnlockIn{ID: w.pid, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	w.must(w.beat(w.editor, w.docA))
	if !reflect.DeepEqual(names(w.must(w.list(w.owner))), []string{"Edgar"}) {
		t.Fatal("an unlocked editor is not shown")
	}
	// the editor locks it: not shown, and the next beat is refused
	if _, err := w.reg.Call(ctx, w.editor.p, "project.lock", projectgate.ProjectIn{ID: w.pid}); err != nil {
		t.Fatal(err)
	}
	if v := w.must(w.list(w.owner)); len(v.Items) != 0 {
		t.Fatalf("a person who locked the project is still shown: %v", names(v))
	}
	if _, err := w.beat(w.editor, w.docA); !isLocked(err) {
		t.Fatalf("beat after lock: %v", err)
	}
	// a project locked by its owner for everyone (a new passphrase relocks every session but theirs)
	if _, err := w.reg.Call(ctx, w.editor.p, "project.unlock", projectgate.UnlockIn{ID: w.pid, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	w.must(w.beat(w.editor, w.docA))
	if _, err := w.reg.Call(ctx, w.owner.p, "project.passphrase.set", projectgate.SetIn{ID: w.pid, Passphrase: "another long one", Current: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	if v := w.must(w.list(w.owner)); len(v.Items) != 0 {
		t.Fatalf("a person relocked by a new passphrase is still shown: %v", names(v))
	}
}

func isLocked(err error) bool { _, ok := ops.IsLocked(err); return ok }

// A project in the trash shows nothing and takes no beats (a write is refused for a trashed project), and its entries
// are forgotten.
func TestTrashedProjectShowsNothing(t *testing.T) {
	w := newWorld(t)
	w.everyoneShares()
	w.must(w.beat(w.editor, w.docA))
	w.must(w.beat(w.owner, w.docA))
	if _, err := w.reg.Call(context.Background(), w.owner.p, "project.trash", projects.StateIn{ID: w.pid}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.beat(w.owner, w.docA); ops.AsError(err).Code != ops.CodeConflict {
		t.Fatalf("a beat on a trashed project: %v, want 409", err)
	}
	v := w.must(w.list(w.owner))
	if v.Enabled || v.Reason != presence.ReasonTrashed || len(v.Items) != 0 {
		t.Fatalf("a trashed project's list: %s", raw(v))
	}
	if w.svc.Len() != 0 {
		t.Fatalf("%d entries kept for a trashed project", w.svc.Len())
	}
	// the editor cannot reach a trashed project at all
	if _, err := w.list(w.editor); ops.AsError(err).Code != ops.CodeNotFound {
		t.Fatalf("an editor's list of a trashed project: %v", err)
	}
}

// Nothing a heartbeat does is written to the database: the whole database file's content is the same after a thousand
// beats, lists and leaves (compared through the tables a beat could touch).
func TestBeatsWriteNothing(t *testing.T) {
	w := newWorld(t)
	w.everyoneShares()
	snap := func() string {
		var sb strings.Builder
		for _, q := range []string{
			`SELECT group_concat(id || last_seen_at || coalesce(idle_expires_at,''), ',') FROM (SELECT * FROM sessions ORDER BY id)`,
			`SELECT group_concat(id || coalesce(last_seen_at,'') || updated_at, ',') FROM (SELECT * FROM users ORDER BY id)`,
			`SELECT count(*) FROM events`, `SELECT count(*) FROM audit_log`, `SELECT count(*) FROM idempotency_keys`,
			`SELECT group_concat(id || updated_at, ',') FROM (SELECT * FROM projects ORDER BY id)`,
		} {
			var s *string
			if err := w.d.Read().QueryRow(q).Scan(&s); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if s != nil {
				sb.WriteString(*s)
			}
			sb.WriteString("|")
		}
		return sb.String()
	}
	// make the sessions old enough that an ordinary request would refresh them
	if _, err := w.d.Writer().Exec(`UPDATE sessions SET last_seen_at = ?`, db.Format(time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	before := snap()
	var dataVersion0 int
	_ = w.d.Read().QueryRow(`PRAGMA data_version`).Scan(&dataVersion0)
	for i := 0; i < 200; i++ {
		for _, p := range []*person{w.owner, w.editor, w.reviewer, w.reader} {
			w.must(w.beat(p, w.docA))
			w.must(w.list(p))
		}
		if i%50 == 0 {
			if _, err := w.reg.Call(context.Background(), w.reader.p, "presence.leave", presence.LeaveIn{ID: w.pid}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if after := snap(); after != before {
		t.Fatalf("the database changed under heartbeats:\n%s\n%s", before, after)
	}
}

func trashDoc(t *testing.T, w *world, id string) {
	t.Helper()
	if _, err := w.reg.Call(context.Background(), w.owner.p, "doc.trash", docs.DocIn{ID: id}); err != nil {
		t.Fatal(err)
	}
}

// The gate's answer for one person is kept for GateTTL (5 s), so an unlock that idles out is noticed after that long and not
// before; a lock is seen at once (TestGatedProjectBeatCost and TestLockedProjectShowsNothingAndRefusesBeats), and an answer is not
// kept for a project that has no one shown.
func TestTheGateAnswerIsKeptForFiveSecondsOnly(t *testing.T) {
	w := newWorld(t)
	w.everyoneShares()
	ctx := context.Background()
	gate, _ := ops.Service[*projectgate.Service](w.reg.Env(), projectgate.ServiceName)
	gateNow := time.Now()
	gate.SetClock(func() time.Time { return gateNow })
	defer gate.SetClock(db.NowTime)
	svcNow := time.Now()
	w.svc.SetClock(func() time.Time { return svcNow })
	if _, err := w.reg.Call(ctx, w.owner.p, "project.passphrase.set", projectgate.SetIn{ID: w.pid, Passphrase: "a long enough one", IdleMinutes: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.reg.Call(ctx, w.editor.p, "project.unlock", projectgate.UnlockIn{ID: w.pid, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	w.must(w.beat(w.editor, w.docA))
	shown := func() int { return len(w.must(w.list(w.owner)).Items) }
	if shown() != 1 {
		t.Fatal("setup: an unlocked editor is not shown")
	}
	// the editor's unlock idles out (the gate's clock is a minute and a bit on); the presence clock is not
	gateNow = gateNow.Add(70 * time.Second)
	if _, err := w.reg.Call(ctx, w.owner.p, "project.unlock", projectgate.UnlockIn{ID: w.pid, Passphrase: "a long enough one"}); err != nil { // the owner's own unlock idled out too
		t.Fatal(err)
	}
	svcNow = svcNow.Add(3 * time.Second)
	if shown() != 1 {
		t.Fatal("the gate's answer was not kept for 3 seconds")
	}
	svcNow = svcNow.Add(3 * time.Second) // 6 s after the answer
	if shown() != 0 {
		t.Fatal("the gate's answer was kept for more than 5 seconds: an idled-out unlock is still shown")
	}
	// the cache follows the people shown: relocking an empty project leaves no version behind
	empty := presence.New()
	empty.InvalidateGate("prj_x")
	if empty.Version("prj_x") != 0 {
		t.Fatal("invalidating the gate of a project with no one shown left a version")
	}
}
