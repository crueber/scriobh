package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The way back to writing names a book only to a person who can open it, and a book that is locked by its title alone (F-1264).

// A person on the Compendium who is on none of its books is shown no way back and no title; one who is on a single book is shown that
// book and never the other; and one who was taken off a book they had opened is shown nothing of it.
func TestAuthzCompBackNamesNoBookToAPersonWhoCannotOpenIt(t *testing.T) {
	b := newBackEnv(t)
	b.open(t, b.pid, b.docA)
	b.open(t, b.other, b.docB)
	// The reader of the Compendium is on neither book.
	_, body := b.page(b.reader, "/c/"+b.slug)
	if strings.Contains(body, "comp-back") || strings.Contains(body, "The Salt Road") || strings.Contains(body, "The Second Road") || strings.Contains(body, "The arrival") || strings.Contains(body, "The crossing") {
		t.Errorf("a person on the Compendium and on no book is shown a book: %s", backLine(body))
	}
	// The editor of the Compendium is on the second book only, and opened it.
	one := b.s.newCaller(t, "one-book", ops.RoleWriter)
	b.s.addCompendiumMember(t, b.cid, one.userID, ops.ProjectEditor)
	b.s.addMember(t, b.other, one.userID, ops.ProjectEditor)
	if _, err := b.s.Reg.Call(context.Background(), ops.Principal{UserID: one.userID, Role: ops.RoleWriter}, "project.open", projects.OpenIn{ID: b.other, DocumentID: b.docB}); err != nil {
		t.Fatal(err)
	}
	_, body = b.page(one, "/c/"+b.slug)
	line := backLine(body)
	if !strings.Contains(line, "Back to The Second Road, The crossing") || strings.Contains(body, "The Salt Road") || strings.Contains(body, "The arrival") {
		t.Errorf("a person on one of the two books: %q", line)
	}
	// Taken off the book, the person is shown nothing of it, though the page they read last is still in their own state.
	b.call(t, "project.member.remove", projects.MemberRemoveIn{ID: b.other, UserID: one.userID})
	_, body = b.page(one, "/c/"+b.slug)
	if strings.Contains(body, "comp-back") || strings.Contains(body, "The Second Road") || strings.Contains(body, "The crossing") {
		t.Errorf("a person taken off the book is still shown it: %q", backLine(body))
	}
	// A person who is not on the Compendium cannot read its pages at all, so there is no line to carry a title.
	if code, body := b.page(b.stranger, "/c/"+b.slug); code != 404 || strings.Contains(body, "Second Road") || strings.Contains(body, "comp-back") {
		t.Errorf("a stranger: %d %.200s", code, body)
	}
	// The tab: a member of the book who is not on the Compendium is sent to the wiring page, which names no Compendium.
	if rec := b.s.do(one, "GET", "/p/"+b.otherSlug+"/compendium/open", ""); rec.Code != 404 {
		t.Errorf("a person taken off the book opens its tab: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// A book with a passphrase that this session has not unlocked is named by its title and goes to its unlock page. Nothing of its chapters,
// its subtitle or its description is read, so none can be shown, and the Compendium pages still carry none of the canaries.
func TestAuthzCompBackAProtectedBookIsNamedByItsTitleAlone(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	// Opened while unlocked, by the person who owns it: that is their own state, which the other session shares.
	if _, err := s.Reg.Call(context.Background(), ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "cli"}, "project.open", projects.OpenIn{ID: w.pid, DocumentID: w.docID}); err != nil {
		t.Fatal(err)
	}
	co, err := s.Reg.Call(context.Background(), ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "cli"}, "compendium.get", compendiums.GetIn{ID: w.compID})
	if err != nil {
		t.Fatal(err)
	}
	slug := co.(compendiums.DetailItem).Item.Slug
	locked := s.do(w.owner2, "GET", "/c/"+slug, "")
	line := backLine(locked.Body.String())
	if locked.Code != 200 || !strings.Contains(line, "Back to Gate Book") || strings.Contains(line, ",") || !strings.Contains(line, `href="/p/`+w.slug+`"`) || strings.Contains(line, "#resume") {
		t.Errorf("locked: %d %q", locked.Code, line)
	}
	if l := w.leaks(locked.Body.String()); len(l) > 0 {
		t.Errorf("the Compendium page of a locked book leaks %v", l)
	}
	// The session that has unlocked it is taken to the chapter, by its title.
	open := s.do(w.owner, "GET", "/c/"+slug, "")
	if line := backLine(open.Body.String()); !strings.Contains(line, "Back to Gate Book, Zqtitle") || !strings.Contains(line, `/d/`+w.docID+`#resume`) {
		t.Errorf("unlocked: %q", line)
	}
	// With the state in place, the whole sweep of the Compendium pages is walked again as the people who have not unlocked it.
	if n := w.walkClass(t, "compendium", []caller{w.editor, w.owner2, w.admin}, 2, false); n < 30 {
		t.Errorf("walked only %d requests", n)
	}
}

// Of two books that use the Compendium, the one that is not locked is the way back even when the locked one was opened later: the link to a
// locked book asks for its passphrase first. Opened alone, the locked one is named by its title.
func TestAuthzCompBackPrefersABookThatIsNotLocked(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	ctx := context.Background()
	ownerP := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "cli"}
	po, err := s.Reg.Call(ctx, ownerP, "project.create", projects.CreateIn{Title: "Open Book"})
	if err != nil {
		t.Fatal(err)
	}
	open := po.(projects.Item).Item
	if _, err := s.Reg.Call(ctx, ownerP, "project.compendium.set", compendiums.SetIn{ID: open.ID, CompendiumID: w.compID}); err != nil {
		t.Fatal(err)
	}
	co, err := s.Reg.Call(ctx, ownerP, "compendium.get", compendiums.GetIn{ID: w.compID})
	if err != nil {
		t.Fatal(err)
	}
	slug := co.(compendiums.DetailItem).Item.Slug
	for _, id := range []string{open.ID, w.pid} { // the locked book is opened last
		if _, err := s.Reg.Call(ctx, ownerP, "project.open", projects.OpenIn{ID: id}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	rec := s.do(w.owner2, "GET", "/c/"+slug, "")
	if line := backLine(rec.Body.String()); !strings.Contains(line, "Back to Open Book") || strings.Contains(line, "Gate Book") {
		t.Errorf("the book that is not locked is the way back: %q", line)
	}
	if l := w.leaks(rec.Body.String()); len(l) > 0 {
		t.Errorf("leaks %v", l)
	}
	// With the unlocked book in the trash, the locked one is all there is.
	if _, err := s.Reg.Call(ctx, ownerP, "project.trash", projects.StateIn{ID: open.ID}); err != nil {
		t.Fatal(err)
	}
	rec = s.do(w.owner2, "GET", "/c/"+slug, "")
	if line := backLine(rec.Body.String()); !strings.Contains(line, "Back to Gate Book") {
		t.Errorf("only a locked book: %q", line)
	}
}

// The Compendium tab of a locked book sends a session that has not unlocked it to the unlock page and says nothing of the Compendium: not its
// address, not its name.
func TestAuthzCompendiumOpenOfALockedBookNamesNoCompendium(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	for _, c := range []caller{w.editor, w.owner2} {
		rec := s.do(c, "GET", "/p/"+w.slug+"/compendium/open", "")
		loc := rec.Header().Get("Location")
		if !(rec.Code == http.StatusSeeOther && strings.HasPrefix(loc, "/p/"+w.slug+"/unlock")) && !(rec.Code == http.StatusForbidden && lockedBody(rec.Body.String())) {
			t.Errorf("%s: %d %q", c.name, rec.Code, loc)
		}
		if strings.Contains(loc, "/c/") || strings.Contains(rec.Body.String(), "Gate Bible") || len(w.leaks(rec.Body.String())) > 0 {
			t.Errorf("%s: the unlock answer names the Compendium: %q %.200s", c.name, loc, rec.Body)
		}
	}
	// The session that has unlocked it is taken in.
	if rec := s.do(w.owner, "GET", "/p/"+w.slug+"/compendium/open", ""); rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/c/") {
		t.Errorf("the control: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
