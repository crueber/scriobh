package server

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/domain/timeline"
)

// The Compendium tab's other two views: the events a scene tells and a quick note (F-1262, F-1263).

func (e lkEnv) doc(t *testing.T, title string) string {
	t.Helper()
	return e.call(t, "doc.create", docs.CreateIn{ProjectID: e.pid, Title: title, Kind: "scene"}).(docs.Item).Item.ID
}

func (e lkEnv) event(t *testing.T, title, date string) string {
	t.Helper()
	return e.call(t, "timeline.event.create", timeline.CreateIn{ID: e.cid, Title: title, StoryDate: date}).(timeline.EventItem).Item.ID
}

func (e lkEnv) link(t *testing.T, doc, ev string) {
	t.Helper()
	e.call(t, "timeline.scene.link", timeline.LinkIn{ID: doc, EventID: ev})
}

func (e lkEnv) scene(c caller, doc, extra string) (int, string) {
	return e.frag(c, "?view=events&doc="+doc+extra)
}

func TestSceneEventsListsTheEventsThisDocumentTellsAndNoOthers(t *testing.T) {
	e := newLkEnv(t)
	d1, d2 := e.doc(t, "The arrival"), e.doc(t, "The crossing")
	wed, fun := e.event(t, "The wedding", "1998-04-12"), e.event(t, "The funeral", "1999")
	e.event(t, "An event nobody tells", "2001")
	e.link(t, d1, wed)
	e.link(t, d2, fun)
	code, body := e.scene(e.owner, d1, "")
	if code != 200 || !strings.Contains(body, "The wedding") || strings.Contains(body, "The funeral") || strings.Contains(body, "An event nobody tells") {
		t.Fatalf("the events of the first scene: %d %.900s", code, body)
	}
	if !strings.Contains(body, "1998-04-12") || !strings.Contains(body, "/c/"+e.slug+"/timeline/events/"+wed) || !strings.Contains(body, "Scene: The arrival") {
		t.Errorf("the date, the link to the event and the scene's name: %.900s", body)
	}
	if _, none := e.scene(e.owner, e.doc(t, "Quiet"), ""); !strings.Contains(none, "None yet.") {
		t.Errorf("a scene that tells nothing: %.400s", none)
	}
	// The panel and the book's own timeline page say the same of the scene.
	rec := e.s.do(e.owner, "GET", "/p/"+e.pslug+"/timeline", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "The wedding") {
		t.Errorf("the timeline page: %d", rec.Code)
	}
}

func TestSceneEventsReadsAllTheEventsOfABusyCompendium(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	// More events told than one page of the op holds: none may be missed.
	for i := 0; i < lkEvPage+5; i++ {
		e.link(t, d, e.event(t, "Event "+strconv.Itoa(i), ""))
	}
	_, body := e.scene(e.owner, d, "")
	if n := strings.Count(body, `class="lk-ev"`); n != lkEvPage+5 {
		t.Errorf("events listed: %d, want %d", n, lkEvPage+5)
	}
}

// A search for an event to link names only events of Compendiums the caller can read. A Compendium the book references and the caller is not
// on has a matching event; the owner is offered it, and the caller is not, though it matches every word.
func TestSceneEventsSearchNeverNamesAnUnreadableCompendium(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	e.event(t, "The wedding at the quay", "1998-04-12")
	other := e.call(t, "compendium.create", compendiums.CreateIn{Name: "Other Bible"}).(compendiums.Item).Item.ID
	e.call(t, "project.compendium.ref.add", compendiums.RefAddIn{ID: e.pid, CompendiumID: other})
	e.call(t, "timeline.event.create", timeline.CreateIn{ID: other, Title: "The wedding abroad", StoryDate: "1999"})
	_, hits := e.scene(e.owner, d, "&part=hits&eq=wedding")
	if !strings.Contains(hits, "The wedding abroad") || !strings.Contains(hits, "The wedding at the quay") {
		t.Fatalf("the owner should be offered both: %.700s", hits)
	}
	_, hits = e.scene(e.peReader, d, "&part=hits&eq=wedding")
	if strings.Contains(hits, "The wedding abroad") {
		t.Errorf("an event of a Compendium the caller cannot read is named: %.700s", hits)
	}
	if !strings.Contains(hits, "The wedding at the quay") {
		t.Errorf("the caller's own Compendium is not searched: %.700s", hits)
	}
}

func TestSceneEventsLinkSearchUnlinkAndNew(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	wed := e.event(t, "The wedding at the quay", "1998-04-12")
	e.event(t, "The storm", "1998-05")
	// The search offers events this scene does not tell yet, by the words of a title.
	_, hits := e.scene(e.owner, d, "&part=hits&eq=WEDDING+quay")
	if !strings.Contains(hits, "The wedding at the quay") || strings.Contains(hits, "The storm") || !strings.Contains(hits, `name="event_id" value="`+wed+`"`) {
		t.Fatalf("hits: %.700s", hits)
	}
	// Link: the scene tells it and the panel says so.
	code, body, _ := e.postFrag(t, e.owner, "event-link", url.Values{"doc": {d}, "event_id": {wed}})
	if code != 200 || !strings.Contains(body, "Linked.") || !strings.Contains(body, "The wedding at the quay") {
		t.Fatalf("link: %d %.600s", code, body)
	}
	if _, hits = e.scene(e.owner, d, "&part=hits&eq=wedding"); strings.Contains(hits, `name="event_id"`) {
		t.Errorf("an event already told is offered again: %.400s", hits)
	}
	// Unlink.
	code, body, _ = e.postFrag(t, e.owner, "event-unlink", url.Values{"doc": {d}, "event_id": {wed}})
	if code != 200 || !strings.Contains(body, "Unlinked.") || strings.Contains(body, `class="lk-ev"`) {
		t.Fatalf("unlink: %d %.600s", code, body)
	}
	// New: the event is the Compendium's and the scene tells it.
	code, body, _ = e.postFrag(t, e.owner, "event-new", url.Values{"doc": {d}, "title": {"The ferry sinks"}, "date": {"1998-06-01"}})
	if code != 200 || !strings.Contains(body, "The ferry sinks") || !strings.Contains(body, "1998-06-01") || !strings.Contains(body, "Added to Salt Bible and linked.") {
		t.Fatalf("new: %d %.700s", code, body)
	}
	list := e.call(t, "timeline.event.list", timeline.ListIn{ID: e.cid, Project: e.pid}).(timeline.ListOut)
	found := false
	for _, ev := range list.Items {
		if ev.Title == "The ferry sinks" && len(ev.Scenes) == 1 && ev.Scenes[0].ID == d {
			found = true
		}
	}
	if !found {
		t.Errorf("the new event is in the Compendium and told by the scene: %+v", list.Items)
	}
	// A date the timeline cannot read is said in words, and nothing is added.
	code, body, h := e.postFrag(t, e.owner, "event-new", url.Values{"doc": {d}, "title": {"Bad date"}, "date": {"last spring"}})
	// The refusal is said beside the date box, not at the top of the panel (U9.5).
	if code != 422 || !strings.Contains(body, "date") || h.Get("HX-Retarget") != "#lkev-date-err" || strings.Contains(body, "lk-status") {
		t.Errorf("a date that cannot be read: %d %q %q", code, body, h.Get("HX-Retarget"))
	}
}

func TestSceneEventsPermissionsFollowTheBookAndTheCompendium(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	ev := e.event(t, "The wedding", "1998")
	e.link(t, d, ev)
	// A project editor who only reads the Compendium: may link and unlink, may not add an event to it.
	_, body := e.scene(e.peReader, d, "")
	if !strings.Contains(body, "The wedding") || !strings.Contains(body, "/event-unlink") || strings.Contains(body, "/event-new") || !strings.Contains(body, "Only editors of this book&#39;s Compendium can add an event") {
		t.Errorf("a reader of the Compendium who edits the book: %.1200s", between(body, `id="lk-view"`, "</div>\n</div>"))
	}
	code, _, _ := e.postFrag(t, e.peReader, "event-new", url.Values{"doc": {d}, "title": {"Mine"}})
	if code == 200 {
		t.Errorf("a reader of the Compendium added an event")
	}
	// A project reader who edits the Compendium: reads the list as text, with the reason, and every change is refused.
	_, body = e.scene(e.ceProjReader, d, "")
	if !strings.Contains(body, "The wedding") || strings.Contains(body, "/event-unlink") || strings.Contains(body, "/event-link") || strings.Contains(body, "/event-new") || !strings.Contains(body, "Only editors of this book can change the events") {
		t.Errorf("a reader of the book: %.1200s", between(body, `id="lk-view"`, "</div>\n</div>"))
	}
	for _, c := range []caller{e.ceProjReader, e.reviewer} {
		for _, a := range []struct {
			act string
			f   url.Values
		}{{"event-unlink", url.Values{"doc": {d}, "event_id": {ev}}}, {"event-link", url.Values{"doc": {d}, "event_id": {ev}}}} {
			if code, _, _ = e.postFrag(t, c, a.act, a.f); code == 200 {
				t.Errorf("%s: %s was let through", c.userID, a.act)
			}
		}
	}
	if got := e.call(t, "timeline.event.list", timeline.ListIn{ID: e.cid, Project: e.pid}).(timeline.ListOut); len(got.Items) != 1 {
		t.Errorf("the book's events after the refused changes: %+v", got.Items)
	}
}

func TestSceneEventsNeedALiveDocumentOfThisBook(t *testing.T) {
	e := newLkEnv(t)
	// A document of another book of the same person, one in the trash, and one that never was answer as an unknown document does.
	po := e.call(t, "project.create", projects.CreateIn{Title: "Another book"}).(projects.Item).Item
	foreign := e.call(t, "doc.create", docs.CreateIn{ProjectID: po.ID, Title: "Zqforeign", Kind: "scene"}).(docs.Item).Item.ID
	gone := e.doc(t, "Gone")
	e.call(t, "doc.trash", docs.DocIn{ID: gone})
	_, unknown := e.scene(e.owner, "doc_00000000000000000000000001", "")
	for name, d := range map[string]string{"another book's": foreign, "a trashed": gone, "none": ""} {
		code, body := e.scene(e.owner, d, "")
		if code != 404 || strings.Contains(body, "Zqforeign") || strings.Contains(body, "Gone") {
			t.Errorf("%s document: %d %.300s", name, code, body)
		}
	}
	if code, _ := e.scene(e.owner, "doc_00000000000000000000000001", ""); code != 404 || unknown == "" {
		t.Errorf("an unknown document: %d", code)
	}
	ev := e.event(t, "The wedding", "1998")
	for _, act := range []string{"event-link", "event-unlink"} {
		if code, _, _ := e.postFrag(t, e.owner, act, url.Values{"doc": {foreign}, "event_id": {ev}}); code != 404 {
			t.Errorf("%s with another book's document: %d", act, code)
		}
	}
	if got := e.call(t, "timeline.event.list", timeline.ListIn{ID: e.cid}).(timeline.ListOut); len(got.Items[0].Scenes) != 0 {
		t.Errorf("another book's scene was tied to an event through this book's panel: %+v", got.Items[0].Scenes)
	}
}

func TestSceneEventsEmptyStatesAreTheEntriesOwn(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	// A book with no Compendium: the same state as the entries view, and its owner's button.
	e.call(t, "project.compendium.set", compendiums.SetIn{ID: e.pid, CompendiumID: ""})
	_, body := e.scene(e.owner, d, "")
	if !strings.Contains(body, "This book has no Compendium yet.") || !strings.Contains(body, "/make") {
		t.Errorf("no Compendium: %.600s", body)
	}
}

// ---- notes ------------------------------------------------------------------------------------------------------------

func (e lkEnv) note(t *testing.T, title, body string) string {
	t.Helper()
	return e.call(t, "project.research.create", research.CreateIn{ID: e.pid, Kind: "note", Title: title, Body: body}).(research.Item).Item.ID
}

func TestNotesListTheRecentOnesAndSearchTheirTitles(t *testing.T) {
	e := newLkEnv(t)
	for i := 0; i < lkNotesN+3; i++ {
		e.note(t, "Note "+strconv.Itoa(i), "words")
	}
	e.note(t, "Ferry timetable", "The last boat is at nine.")
	_, body := e.frag(e.owner, "?view=notes")
	if n := strings.Count(body, `class="lk-pick"`); n != lkNotesN {
		t.Errorf("notes listed: %d, want %d", n, lkNotesN)
	}
	if !strings.Contains(body, "Ferry timetable") || strings.Contains(body, "Note 0<") {
		t.Errorf("the newest first: %.600s", between(body, `id="lk-nrows"`, "</ul>"))
	}
	if !strings.Contains(body, "/p/"+e.pslug+"/research") {
		t.Errorf("a way to all the notes: %.300s", body)
	}
	_, rows := e.frag(e.owner, "?view=notes&part=nrows&nq=TIMETABLE")
	if !strings.Contains(rows, "Ferry timetable") || strings.Count(rows, `class="lk-pick"`) != 1 || strings.Contains(rows, "<html") {
		t.Errorf("a search by title: %.500s", rows)
	}
	if _, rows = e.frag(e.owner, "?view=notes&part=nrows&nq=zzzz"); !strings.Contains(rows, "No note is called anything like") {
		t.Errorf("nothing: %.300s", rows)
	}
}

// Notes saved in the same millisecond have the same UpdatedAt. The list keeps one fixed order for them: the later-made note first,
// since ids are made in time order, so two reloads show the same list.
func TestNotesWithTheSameTimeListTheLaterMadeFirst(t *testing.T) {
	e := newLkEnv(t)
	for i := 0; i < 4; i++ {
		e.note(t, "Same moment "+strconv.Itoa(i), "words")
	}
	if _, err := e.s.DB.Writer().Exec(`UPDATE research_notes SET updated_at = '2026-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	_, body := e.frag(e.owner, "?view=notes")
	at := make([]int, 4)
	for i := range at {
		at[i] = strings.Index(body, "Same moment "+strconv.Itoa(i)+"<")
		if at[i] < 0 {
			t.Fatalf("note %d is not listed: %.400s", i, between(body, `id="lk-nrows"`, "</ul>"))
		}
	}
	if !(at[3] < at[2] && at[2] < at[1] && at[1] < at[0]) {
		t.Errorf("notes with the same time, in the order listed (want the later made first): %v", at)
	}
}

func TestNotesNewOpenAndSaveWithTheRevisionRule(t *testing.T) {
	e := newLkEnv(t)
	code, body, _ := e.postFrag(t, e.owner, "note-new", url.Values{"title": {"Tide table"}, "body": {"High at six."}})
	if code != 200 || !strings.Contains(body, `id="lkn-body"`) || !strings.Contains(body, "High at six.") || !strings.Contains(body, "Added.") {
		t.Fatalf("new: %d %.700s", code, body)
	}
	list := e.call(t, "project.research.list", research.ListIn{ID: e.pid}).(research.ListOut).Items
	if len(list) != 1 || list[0].Title != "Tide table" {
		t.Fatalf("the note is the book's: %+v", list)
	}
	id := list[0].ID
	rev := list[0].Revision
	// Save: the new revision comes back for the next save.
	code, body, _ = e.postFrag(t, e.owner, "note-save", url.Values{"note_id": {id}, "base_revision": {strconv.Itoa(rev)}, "title": {"Tide table"}, "body": {"High at six, low at midnight."}})
	if code != 200 || !strings.Contains(body, "Saved.") || !strings.Contains(body, `value="`+strconv.Itoa(rev+1)+`"`) {
		t.Fatalf("save: %d %s", code, body)
	}
	got := e.call(t, "project.research.get", research.NoteIn{ID: e.pid, NoteID: id}).(research.Item).Item
	if got.Body != "High at six, low at midnight." {
		t.Errorf("saved body: %q", got.Body)
	}
	// A stale revision: the words are not saved, the box says so with Keep mine and Use theirs, and the other words are untouched.
	code, body, h := e.postFrag(t, e.owner, "note-save", url.Values{"note_id": {id}, "base_revision": {strconv.Itoa(rev)}, "title": {"Tide table"}, "body": {"Mine, from an old copy."}})
	if code != 422 || !strings.Contains(body, "changed since you opened it") || !strings.Contains(body, "Keep mine") || !strings.Contains(body, "Use theirs") || !strings.Contains(body, `{"base_revision":"`+strconv.Itoa(rev+1)+`"}`) {
		t.Errorf("stale: %d %s", code, body)
	}
	if h.Get("HX-Retarget") != "#lk-status" {
		t.Errorf("the refusal is said in the status line: %q", h.Get("HX-Retarget"))
	}
	if got = e.call(t, "project.research.get", research.NoteIn{ID: e.pid, NoteID: id}).(research.Item).Item; got.Body != "High at six, low at midnight." {
		t.Errorf("a stale save changed the note: %q", got.Body)
	}
	// Keep mine: the same words with the current revision win.
	code, body, _ = e.postFrag(t, e.owner, "note-save", url.Values{"note_id": {id}, "base_revision": {strconv.Itoa(rev + 1)}, "title": {"Tide table"}, "body": {"Mine, from an old copy."}})
	if code != 200 || !strings.Contains(body, "Saved.") {
		t.Errorf("keep mine: %d %s", code, body)
	}
}

func TestNotesOpenedNoteIsRenderedSafelyForAReader(t *testing.T) {
	e := newLkEnv(t)
	id := e.note(t, "Hostile", "**bold** <script>alert(1)</script> <img src=x onerror=alert(2)>")
	_, ro := e.frag(e.reviewer, "?view=notes&note="+id)
	if !strings.Contains(ro, "<strong>bold</strong>") || strings.Contains(ro, "<script>") || strings.Contains(ro, "<img") || strings.Contains(ro, `id="lk-nform"`) || !strings.Contains(ro, "Only editors of this book can change its notes.") {
		t.Errorf("a reader: %s", between(ro, `id="lk-view"`, "</div>"))
	}
	_, rw := e.frag(e.owner, "?view=notes&note="+id)
	if !strings.Contains(rw, `id="lk-nform"`) || strings.Contains(rw, "<script>alert") {
		t.Errorf("an editor: %.500s", rw)
	}
	for _, c := range []caller{e.reviewer, e.ceProjReader} {
		if code, _, _ := e.postFrag(t, c, "note-new", url.Values{"title": {"x"}}); code == 200 {
			t.Errorf("%s created a note", c.userID)
		}
		if code, _, _ := e.postFrag(t, c, "note-save", url.Values{"note_id": {id}, "base_revision": {"1"}, "body": {"x"}}); code == 200 {
			t.Errorf("%s saved a note", c.userID)
		}
	}
	if _, nf := e.frag(e.owner, "?view=notes&new=1"); !strings.Contains(nf, `id="lk-nform"`) || !strings.Contains(nf, `hx-post="`+e.base+`/note-new"`) || strings.Contains(nf, "lk-newform\" hx-post=\""+e.base+"/new") {
		t.Errorf("the form for a new note is shown to an editor: %.400s", nf)
	}
	if _, nf := e.frag(e.reviewer, "?view=notes&new=1"); strings.Contains(nf, `id="lk-nform"`) {
		t.Errorf("the form for a new note is shown to a reader")
	}
	if code, _ := e.frag(e.owner, "?view=notes&note=not-a-note"); code != 404 {
		t.Errorf("an unknown note: %d", code)
	}
}

// A search that comes back as another view (a Compendium that is gone, a note asked for) replaces the whole view and is never nested in the
// list it was asked for.
func TestSceneAndNotesPartsThatChangedViewRetargetTheWholePanel(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	n := e.note(t, "A note", "words")
	get := func(q string) (string, string, string) {
		rec := e.s.do(e.owner, "GET", e.base+q, "")
		return rec.Body.String(), rec.Header().Get("HX-Retarget"), rec.Header().Get("HX-Reswap")
	}
	if _, rt, _ := get("?view=events&doc=" + d + "&part=hits&eq=a"); rt != "" {
		t.Errorf("a search of hits that is still the events view is not retargeted: %q", rt)
	}
	if _, rt, _ := get("?view=notes&part=nrows&nq=a"); rt != "" {
		t.Errorf("a search of rows that is still the notes list is not retargeted: %q", rt)
	}
	if body, rt, rs := get("?view=notes&part=nrows&note=" + n); rt != "#lk-main" || rs != "innerHTML" || !strings.Contains(body, `id="lk-nform"`) {
		t.Errorf("a note asked for in the rows' request: retarget %q reswap %q", rt, rs)
	}
	if _, rt, _ := get("?view=notes&part=nrows&new=1"); rt != "#lk-main" {
		t.Errorf("a new note asked for in the rows' request: %q", rt)
	}
	e.call(t, "project.compendium.set", compendiums.SetIn{ID: e.pid, CompendiumID: ""})
	if body, rt, _ := get("?view=events&doc=" + d + "&part=hits&eq=a"); rt != "#lk-main" || !strings.Contains(body, "no Compendium") {
		t.Errorf("the Compendium is gone: retarget %q, %.200s", rt, body)
	}
}

// Only a live note of the book is saved through the panel.
func TestNotesSaveRefusesWhatIsNotALiveNote(t *testing.T) {
	e := newLkEnv(t)
	folder := e.call(t, "project.research.create", research.CreateIn{ID: e.pid, Kind: "folder", Title: "A folder"}).(research.Item).Item
	clip := e.call(t, "project.research.create", research.CreateIn{ID: e.pid, Kind: "clipping", Title: "A clipping", Body: "kept"}).(research.Item).Item
	gone := e.call(t, "project.research.create", research.CreateIn{ID: e.pid, Kind: "note", Title: "In the trash", Body: "kept"}).(research.Item).Item
	e.call(t, "project.research.trash", research.NoteIn{ID: e.pid, NoteID: gone.ID})
	_, unknown, _ := e.postFrag(t, e.owner, "note-save", url.Values{"note_id": {"rsn_00000000000000000000000001"}, "base_revision": {"1"}, "body": {"x"}})
	for name, it := range map[string]research.Note{"a folder": folder, "a clipping": clip, "a note in the trash": gone} {
		code, body, _ := e.postFrag(t, e.owner, "note-save", url.Values{"note_id": {it.ID}, "base_revision": {strconv.Itoa(it.Revision)}, "title": {"Mine"}, "body": {"Mine"}})
		if code != 404 || body != unknown {
			t.Errorf("%s: %d %.120s", name, code, body)
		}
		got := e.call(t, "project.research.get", research.NoteIn{ID: e.pid, NoteID: it.ID}).(research.Item).Item
		if got.Title == "Mine" || got.Body == "Mine" {
			t.Errorf("%s was changed through the panel", name)
		}
	}
}

// The forms that change something cannot be sent twice by a double click or a held Enter.
func TestSceneAndNotesFormsDropASecondSubmit(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	e.event(t, "The wedding", "1998")
	n := e.note(t, "A note", "words")
	for name, body := range map[string]string{
		"events":   func() string { _, b := e.scene(e.owner, d, "&eq=wedding&part=hits"); return b }() + func() string { _, b := e.scene(e.owner, d, ""); return b }(),
		"new note": func() string { _, b := e.frag(e.owner, "?view=notes&new=1"); return b }(),
		"a note":   func() string { _, b := e.frag(e.owner, "?view=notes&note="+n); return b }(),
	} {
		for _, f := range regexp.MustCompile(`<form[^>]*hx-post="[^"]*/(?:event-link|event-unlink|event-new|note-new|note-save)"[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(f, `hx-sync="this:drop"`) {
				t.Errorf("%s: a form that changes something can be sent twice: %s", name, f)
			}
		}
	}
}

// The search to link matches a word in a story label, a place's name and a person's name as well as in a title (the event search reads
// those in the database; this is the panel's end of it).
func TestSceneEventsSearchMatchesLabelPlaceAndPerson(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	quay := e.call(t, "entity.create", entities.CreateIn{ID: e.cid, Kind: "place", Name: "The Quay"}).(entities.Item).Item.ID
	maire := e.call(t, "entity.create", entities.CreateIn{ID: e.cid, Kind: "character", Name: "Máire Ní"}).(entities.Item).Item.ID
	ev := e.call(t, "timeline.event.create", timeline.CreateIn{ID: e.cid, Title: "Arrival", StoryDate: "1998", Location: quay,
		Participants: []timeline.Participant{{Entity: maire}}}).(timeline.EventItem).Item.ID
	label := "Midsummer, Year 312"
	e.call(t, "timeline.event.update", timeline.UpdateIn{ID: ev, StoryLabel: &label})
	for _, q := range []string{"quay", "MÁIRE", "midsummer", "312"} {
		if _, hits := e.scene(e.owner, d, "&part=hits&eq="+url.QueryEscape(q)); !strings.Contains(hits, "Arrival") {
			t.Errorf("a search for %q does not offer the event: %.500s", q, hits)
		}
	}
	if _, hits := e.scene(e.owner, d, "&part=hits&eq=quay+zzz"); strings.Contains(hits, "Arrival") {
		t.Errorf("every word must match, yet the event is offered: %.500s", hits)
	}
}
