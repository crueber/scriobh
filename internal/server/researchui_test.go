package server

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/ops"
)

// ---- fixtures for the template lint ---------------------------------------------------------------------

func resFixtureCompendium() compendiums.Compendium {
	return compendiums.Compendium{ID: "cmp_a", Slug: "the-salt-bible", Name: "The Salt Road Bible", Role: "owner"}
}

func resFixtureOwner(kind string, edit bool) resOwner {
	if kind == "c" {
		o := resCompendiumOwner(resFixtureCompendium())
		o.CanEdit = edit
		return o
	}
	return resProjectOwner(fixtureProjects()[0], edit)
}

func resFixtureTags() []resTagView {
	return resTagViews([]research.Tag{
		{ID: "tag_1", Name: "harbour", Color: "teal", Notes: 3}, {ID: "tag_2", Name: "weather <b>", Color: "clay", Notes: 1},
		{ID: "tag_3", Name: "people", Color: "plum", Notes: 0}})
}

func resFixtureRows(o resOwner, trash bool) []resRow {
	by := map[string]resTagView{}
	for _, t := range resFixtureTags() {
		by[strings.ToLower(t.Name)] = t
	}
	mk := func(id, parent, kind, title, status string, tags []string, level int, words int) resRow {
		e := research.Entry{ID: id, ParentID: parent, Kind: kind, Title: title, Status: status, Tags: tags, Revision: 2, WordCount: words, UpdatedAt: "2026-11-12T10:00:00.000Z", Trashed: trash}
		return resRow{Entry: e, Level: level, Folder: kind == "folder", KindLabel: resKindLabel[kind], StatusLabel: resStatusLabel[status], TagViews: resTagsFor(tags, by), When: "12 Nov 2026"}
	}
	f := mk("res_f", "", "folder", "Tides", "inbox", nil, 0, 0)
	f.Kids, f.CanDown = 2, true
	a := mk("res_a", "res_f", "note", "Tide tables <script>x</script>", "reading", []string{"harbour", "weather <b>"}, 1, 420)
	a.CanDown, a.CanOut = true, true
	b := mk("res_b", "res_f", "clipping", "", "useful", []string{"harbour"}, 1, 90)
	b.CanUp, b.CanOut = true, true
	c := mk("res_c", "", "note", "Loose note", "inbox", nil, 0, 12)
	c.CanUp, c.CanIn = true, true
	return []resRow{f, a, b, c}
}

func resFixtureData(kind string, edit bool, empty bool) resData {
	o := resFixtureOwner(kind, edit)
	d := resData{resOwner: o, CSRF: "tok_writer", View: "tree", Tags: resFixtureTags(), Values: map[string]string{}, Page: 1, Pages: 1, Zone: "UTC",
		TreeURL: o.UI + "/research/tree", Folders: []resFolder{{ID: "res_f", Label: "Tides"}}}
	if !empty {
		d.Rows = resFixtureRows(o, false)
		d.Total, d.From, d.To = len(d.Rows), 1, len(d.Rows)
	}
	return d
}

func resFixtureBoard(kind string, edit, empty bool, by string) resBoardData {
	o := resFixtureOwner(kind, edit)
	d := resBoardData{resOwner: o, CSRF: "tok_writer", By: by}
	if empty {
		return d
	}
	rows := resFixtureRows(o, false)
	card := func(i int) resCard {
		return resCard{Entry: rows[i].Entry, TagViews: rows[i].TagViews, KindLabel: rows[i].KindLabel}
	}
	key := func(a, b, c string) [3]string { return [3]string{a, b, c} }
	k := key("inbox", "reading", "useful")
	labels := [3]string{"Inbox", "Reading", "Useful"}
	if by == "tag" {
		k, labels = key("tag_1", "tag_2", "untagged"), [3]string{"harbour", "weather <b>", "Untagged"}
	}
	d.Columns = []resColumn{{Key: k[0], Label: labels[0], Count: 2, Cards: []resCard{card(3), card(2)}, Class: "cover-0"}, {Key: k[1], Label: labels[1], Count: 1, Cards: []resCard{card(1)}},
		{Key: k[2], Label: labels[2], Count: 60, Hidden: 20, More: o.link("/research/board", "all", k[2]), Cards: []resCard{card(0)}}, {Key: "archived", Label: "Archived", Count: 0}}
	d.Total = 63
	return d
}

func resFixtureNote(kind string, edit bool, clip bool, conflict bool) resNoteData {
	o := resFixtureOwner(kind, edit)
	n := research.Note{ID: "res_a", Kind: "note", Title: "Tide tables", Body: "Low water at **six**.\n\nSee [the file](/files/att_abc123).", Status: "reading", Tags: []string{"harbour"}, Revision: 3, WordCount: 9, UpdatedAt: "2026-11-12T10:00:00.000Z"}
	if clip {
		n.Kind, n.Title = "clipping", "A harbour master's diary"
		n.SourceURL, n.RetrievedOn, n.Quote = "https://example.org/diary?x=1&y=2", "2026-03-14", "The sea was <quiet>."
	}
	d := resNoteData{resOwner: o, CSRF: "tok_writer", Note: n, TagsText: "harbour", AllTags: resFixtureTags(), KindLabel: resKindLabel[n.Kind], Folder: "Tides",
		Statuses: resStatusOptions(n.Status), Preview: resPreview(n.Body), HasPreview: true, Words: 9, Updated: "12 Nov 2026", Source: n.SourceURL, FilesURL: o.Base + "/files", Zone: "UTC",
		Attachments: []resAttachment{{ID: "att_abc123", Filename: "tides.pdf", Kind: "pdf", Size: "12 KB", URL: "/files/att_abc123"}}, Values: map[string]string{}}
	if conflict {
		d.Conflict = &resConflict{Body: "Their text.", Title: "Their title", Revision: 4}
		d.Error = "This note was changed somewhere else after you opened it. Your text is kept below, and nothing was lost. Choose which to keep."
	}
	return d
}

func resFixtureScratch(kind string, conflict bool) scratchData {
	o := resFixtureOwner(kind, true)
	d := scratchData{resOwner: o, CSRF: "tok_writer", Content: "Ask Mara about the ledger.", Revision: 2, Words: 5, Updated: "12 Nov 2026", DraftKey: "scriobh:scratch:" + kind + ":" + o.ID, MaxKB: 256, Zone: "UTC"}
	if conflict {
		d.Conflict = &scratchConflict{Theirs: "Saved from another tab.", Revision: 3}
		d.Error = "This pad was saved somewhere else after you opened it."
	}
	return d
}

func resFixtureTagsPage(kind string, edit bool) resTagsData {
	o := resFixtureOwner(kind, edit)
	d := resTagsData{resOwner: o, CSRF: "tok_writer", Tags: resFixtureTags(), Values: map[string]string{}}
	for _, c := range []string{"slate", "clay", "ochre", "moss", "teal", "blue", "plum", "rose"} {
		d.Colors = append(d.Colors, resColor{Token: c, Word: resColorWord[c], Class: resColorClass[c]})
	}
	return d
}

func researchPageFixtures(writer, reader Page) []fixture {
	with := func(p Page, title, path string, data any) Page { p.Title, p.Path, p.Data = title, path, data; return p }
	var out []fixture
	for _, kind := range []string{"p", "c"} {
		base := map[string]string{"p": "/p/the-salt-road", "c": "/c/the-salt-bible"}[kind]
		pg := func(p Page, name string) Page { return with(p, name, base+"/research", nil) }
		out = append(out,
			fixture{"research", with(pg(writer, "Research"), "Research", base+"/research", resFixtureData(kind, true, false))},
			fixture{"research", with(pg(writer, "Research"), "Research", base+"/research", resFixtureData(kind, true, true))},
			fixture{"research", with(pg(reader, "Research"), "Research", base+"/research", resFixtureData(kind, false, false))},
			fixture{"research", with(pg(reader, "Research"), "Research", base+"/research", resFixtureData(kind, false, true))},
			fixture{"research-board", with(writer, "Research board", base+"/research/board", resFixtureBoard(kind, true, false, "status"))},
			fixture{"research-board", with(writer, "Research board", base+"/research/board", resFixtureBoard(kind, true, false, "tag"))},
			fixture{"research-board", with(reader, "Research board", base+"/research/board", resFixtureBoard(kind, false, false, "status"))},
			fixture{"research-board", with(writer, "Research board", base+"/research/board", resFixtureBoard(kind, true, true, "status"))},
			fixture{"research-note", with(writer, "Tide tables", base+"/research/res_a", resFixtureNote(kind, true, false, false))},
			fixture{"research-note", with(writer, "Clipping", base+"/research/res_a", resFixtureNote(kind, true, true, false))},
			fixture{"research-note", with(writer, "Tide tables", base+"/research/res_a", resFixtureNote(kind, true, true, true))},
			fixture{"research-note", with(reader, "Clipping", base+"/research/res_a", resFixtureNote(kind, false, true, false))},
			fixture{"research-tags", with(writer, "Tags", base+"/research/tags", resFixtureTagsPage(kind, true))},
			fixture{"research-tags", with(reader, "Tags", base+"/research/tags", resFixtureTagsPage(kind, false))},
			fixture{"scratch", with(writer, "Scratchpad", base+"/scratch", resFixtureScratch(kind, false))},
			fixture{"scratch", with(writer, "Scratchpad", base+"/scratch", resFixtureScratch(kind, true))},
		)
	}
	trash := resFixtureData("p", true, false)
	trash.View, trash.Rows = "trash", resFixtureRows(trash.resOwner, true)
	out = append(out, fixture{"research", with(writer, "Research trash", "/p/the-salt-road/research?view=trash", trash)})
	hits := resFixtureData("p", true, false)
	hits.Q, hits.Searched, hits.HitTotal = "tide", true, 2
	hits.Hits = []resHit{{ID: "res_a", Title: "Tide tables", Kind: "note", MatchedIn: "text", Parts: []snipPart{{Text: "Low water at "}, {Text: "tide", Mark: true}}}}
	out = append(out, fixture{"research", with(writer, "Research search", "/p/the-salt-road/research?q=tide", hits)})
	return out
}

func researchFragmentFixtures() []fragmentFixture {
	return []fragmentFixture{
		{"research-tree", "research-tree", resFixtureData("p", true, false)},
		{"research-tree", "research-tree", resFixtureData("c", false, true)},
		{"research-board", "research-board", resFixtureBoard("p", true, false, "status")},
		{"research-nav", "res-subnav", map[string]any{"O": resFixtureOwner("p", true), "Active": "list"}},
		{"research-nav", "res-crumb", map[string]any{"O": resFixtureOwner("c", true), "Here": "Scratchpad"}},
	}
}

// ---- a real server: both owners, every role ---------------------------------------------------------------

type resUI struct {
	*resFixture
	creader caller // a reader on the Compendium
	pslug   string
	cslug   string
}

func newResUI(t *testing.T) *resUI {
	t.Helper()
	u := &resUI{resFixture: newResFixture(t)}
	u.creader = u.s.newCaller(t, "creader", ops.RoleReader)
	u.s.addCompendiumMember(t, u.cid, u.creader.userID, "reader")
	_, pm := u.call(u.owner, "GET", "/api/v1/projects/"+u.pid, "")
	u.pslug = pm["item"].(map[string]any)["slug"].(string)
	_, cm := u.call(u.owner, "GET", "/api/v1/compendiums/"+u.cid, "")
	u.cslug = cm["item"].(map[string]any)["slug"].(string)
	return u
}

// side is one owner and who may do what on it.
type resSide struct {
	kind, base, ui, api, slug string
	// reads and writes name who can open the pages and who can change things; the rest get 404 (not members) or 403.
	reads, writes []caller
	notIn         []caller // sign-in is fine but there is no access: 404
}

func (u *resUI) sides() []resSide {
	return []resSide{
		{kind: "p", base: "/p/" + u.pslug, ui: "/ui/projects/" + u.pid, api: "/api/v1/projects/" + u.pid, slug: u.pslug,
			reads: []caller{u.owner, u.editor, u.reader}, writes: []caller{u.owner, u.editor}, notIn: []caller{u.stranger}},
		{kind: "c", base: "/c/" + u.cslug, ui: "/ui/compendiums/" + u.cid, api: "/api/v1/compendiums/" + u.cid, slug: u.cslug,
			reads: []caller{u.owner, u.editor, u.creader}, writes: []caller{u.owner, u.editor}, notIn: []caller{u.stranger, u.reader}},
	}
}

func (u *resUI) form(t *testing.T, c caller, path string, v url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return u.s.post(t, c, path, v, "", false)
}

func (u *resUI) page(c caller, path string) (int, string) {
	rec := u.s.do(c, "GET", path, "")
	return rec.Code, rec.Body.String()
}

func (u *resUI) makeNote(t *testing.T, sd resSide, body string) (id string, rev int) {
	t.Helper()
	return u.mustNote(t, u.owner, sd.api, body)
}

// ---- tests ---------------------------------------------------------------------------------------------------

func TestResearchPagesAreGuardedForBothOwners(t *testing.T) {
	u := newResUI(t)
	for _, sd := range u.sides() {
		id, _ := u.makeNote(t, sd, `{"title":"Guarded","body":"words here"}`)
		pages := []string{sd.base + "/research", sd.base + "/research/board", sd.base + "/research/tags", sd.base + "/research/" + id, sd.base + "/scratch",
			sd.ui + "/research/tree", sd.ui + "/research/board"}
		for _, p := range pages {
			if rec := u.s.do(caller{name: "anon"}, "GET", p, ""); rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnauthorized {
				t.Errorf("%s anonymous: %d", p, rec.Code)
			}
			for _, c := range sd.reads {
				if code, _ := u.page(c, p); code != http.StatusOK {
					t.Errorf("%s as %s: %d, want 200", p, c.name, code)
				}
			}
			for _, c := range sd.notIn {
				if code, body := u.page(c, p); code != http.StatusNotFound || strings.Contains(body, "Guarded") {
					t.Errorf("%s as %s (no access): %d, want 404 with nothing shown", p, c.name, code)
				}
			}
		}
		// A note id from the other owner is not found here, whoever asks.
		if code, _ := u.page(u.owner, sd.base+"/research/res_nothing"); code != http.StatusNotFound {
			t.Errorf("%s: an unknown note: %d", sd.base, code)
		}
		// The instance admin is not a member: the pages are found by the person's own memberships, so they are 404,
		// and nothing of the owner's shows either way.
		for _, p := range pages[:5] {
			if code, body := u.page(u.admin, p); code == http.StatusOK && strings.Contains(body, "Guarded") {
				t.Errorf("%s as admin showed a note the admin is not a member for", p)
			}
		}
	}
}

func TestResearchActionsAreGuardedForBothOwners(t *testing.T) {
	u := newResUI(t)
	for _, sd := range u.sides() {
		id, rev := u.makeNote(t, sd, `{"title":"Guarded"}`)
		_, tm := u.call(u.owner, "POST", sd.api+"/tags", `{"name":"keep"}`)
		tagID := tm["item"].(map[string]any)["id"].(string)
		actions := map[string]url.Values{
			"create":     {"kind": {"note"}, "title": {"X"}},
			"move":       {"note_id": {id}, "act": {"down"}},
			"trash":      {"note_id": {id}},
			"restore":    {"note_id": {id}},
			"save":       {"note_id": {id}, "rev": {fmt.Sprint(rev)}, "title": {"X"}, "body": {"y"}},
			"board-move": {"note_id": {id}, "rev": {fmt.Sprint(rev)}, "to": {"reading"}, "from": {"inbox"}, "by": {"status"}},
			"tag-create": {"name": {"x"}, "color": {"teal"}},
			"tag-update": {"tag_id": {tagID}, "name": {"keep2"}, "color": {"teal"}},
			"tag-merge":  {"tag_id": {tagID}, "into_id": {tagID}},
			"tag-delete": {"tag_id": {tagID}},
		}
		for name, v := range actions {
			path := sd.ui + "/research/" + name
			anon := httptestPost(u.s, caller{name: "anon"}, path, v, "")
			if anon != http.StatusSeeOther && anon != http.StatusUnauthorized {
				t.Errorf("%s anonymous: %d", path, anon)
			}
			if rec := u.s.post(t, u.owner, path, v, "-", false); rec.Code != http.StatusForbidden {
				t.Errorf("%s without the token: %d, want 403", path, rec.Code)
			}
			if rec := u.s.post(t, u.owner, path, v, "wrong", false); rec.Code != http.StatusForbidden {
				t.Errorf("%s with a wrong token: %d, want 403", path, rec.Code)
			}
			for _, c := range sd.notIn {
				if rec := u.s.post(t, c, path, v, "", false); rec.Code != http.StatusNotFound {
					t.Errorf("%s as %s (no access): %d, want 404", path, c.name, rec.Code)
				}
			}
			for _, c := range sd.reads {
				if c.userID == u.owner.userID || c.userID == u.editor.userID {
					continue
				}
				if rec := u.s.post(t, c, path, v, "", false); rec.Code != http.StatusForbidden {
					t.Errorf("%s as reader %s: %d, want 403", path, c.name, rec.Code)
				}
			}
		}
		// Any member can preview text; it stores nothing.
		for _, c := range sd.reads {
			rec := u.s.post(t, c, sd.ui+"/research/preview", url.Values{"body": {"**hi** <script>alert(1)</script>"}}, "", false)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<strong>hi</strong>") || strings.Contains(rec.Body.String(), "<script>") {
				t.Errorf("preview as %s: %d %q", c.name, rec.Code, rec.Body.String())
			}
		}
		// And the scratch save needs a session and a token, and membership.
		sp := sd.ui + "/scratch"
		if rec := u.s.post(t, u.owner, sp, url.Values{"content": {"x"}}, "-", false); rec.Code != http.StatusForbidden {
			t.Errorf("%s without the token: %d", sp, rec.Code)
		}
		for _, c := range sd.notIn {
			if rec := u.s.post(t, c, sp, url.Values{"content": {"x"}}, "", false); rec.Code != http.StatusNotFound {
				t.Errorf("%s as %s: %d, want 404", sp, c.name, rec.Code)
			}
		}
	}
}

func httptestPost(s *Server, c caller, path string, v url.Values, token string) int {
	rec := s.do(c, "POST", path, v.Encode())
	return rec.Code
}

func TestResearchTreeAndNoteFlow(t *testing.T) {
	u := newResUI(t)
	for _, sd := range u.sides() {
		t.Run(sd.kind, func(t *testing.T) {
			post := func(action string, v url.Values) *httptest.ResponseRecorder {
				return u.form(t, u.owner, sd.ui+"/research/"+action, v)
			}
			// Empty first: an honest sentence and the add form.
			code, body := u.page(u.owner, sd.base+"/research")
			if code != 200 || !strings.Contains(body, "No research in this") || !strings.Contains(body, `action="`+sd.ui+`/research/create"`) {
				t.Fatalf("empty page: %d", code)
			}
			if code, body = u.page(u.creaderOrReader(sd), sd.base+"/research"); code != 200 || strings.Contains(body, "/research/create") || !strings.Contains(body, "Nothing has been added yet") {
				t.Errorf("a reader sees the add form or no honest empty state: %d", code)
			}
			for _, kind := range []string{"folder", "note", "clipping"} {
				if rec := post("create", url.Values{"kind": {kind}, "title": {strings.ToUpper(kind[:1]) + kind[1:] + " one"}}); rec.Code != http.StatusSeeOther {
					t.Fatalf("create %s: %d", kind, rec.Code)
				}
			}
			_, body = u.page(u.owner, sd.base+"/research")
			for _, want := range []string{"Folder one", "Note one", "Clipping one", "3 entries", `aria-label="Move up Note one"`, `aria-label="Move into the folder above Note one"`} {
				if !strings.Contains(body, want) {
					t.Errorf("tree lacks %q", want)
				}
			}
			ids := idsByTitle(t, u, sd)
			// Indent the note under the folder above it, then outdent it.
			if rec := post("move", url.Values{"note_id": {ids["Note one"]}, "act": {"up"}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("move up: %d", rec.Code)
			}
			if rec := post("move", url.Values{"note_id": {ids["Note one"]}, "act": {"down"}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("move down: %d", rec.Code)
			}
			if rec := post("move", url.Values{"note_id": {ids["Note one"]}, "act": {"out"}}); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("outdent at the top level: %d, want 422", rec.Code)
			}
			if rec := post("move", url.Values{"note_id": {ids["Note one"]}, "act": {"in"}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("move in: %d", rec.Code)
			}
			_, body = u.page(u.owner, sd.base+"/research")
			if !strings.Contains(body, "1 inside") || !strings.Contains(body, `class="r-row lv1`) {
				t.Errorf("the note is not shown inside the folder")
			}
			if rec := post("move", url.Values{"note_id": {ids["Note one"]}, "act": {"out"}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("move out: %d", rec.Code)
			}
			// Trash the folder with a note in it, say what came with it, and bring it back.
			post("move", url.Values{"note_id": {ids["Note one"]}, "act": {"in"}})
			rec := post("trash", url.Values{"note_id": {ids["Folder one"]}})
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("trash: %d", rec.Code)
			}
			_, body = u.page(u.owner, sd.base+"/research?view=trash")
			if !strings.Contains(body, "Folder one") || !strings.Contains(body, "Note one") || !strings.Contains(body, "Restore") {
				t.Errorf("the trash lacks the folder and what was in it")
			}
			if rec := post("restore", url.Values{"note_id": {ids["Folder one"]}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("restore: %d", rec.Code)
			}
			_, body = u.page(u.owner, sd.base+"/research")
			if !strings.Contains(body, "Note one") || !strings.Contains(body, "Folder one") {
				t.Errorf("restore did not bring the folder and its note back")
			}

			// Tags: create, use, filter, rename, merge, delete.
			if rec := post("tag-create", url.Values{"name": {"harbour"}, "color": {"teal"}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("tag-create: %d", rec.Code)
			}
			if rec := post("tag-create", url.Values{"name": {"harbour"}, "color": {"teal"}}); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("a duplicate tag: %d, want 422", rec.Code)
			}
			if rec := post("tag-create", url.Values{"name": {"weather"}, "color": {"nonsense"}}); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("a colour that is not one of the eight: %d, want 422", rec.Code)
			}
			// Save the note with a tag (the form has the revision it was drawn from).
			code, nbody := u.page(u.owner, sd.base+"/research/"+ids["Note one"])
			if code != 200 {
				t.Fatalf("note page: %d", code)
			}
			rev := fieldValue(nbody, "rev")
			if rec := post("save", url.Values{"note_id": {ids["Note one"]}, "rev": {rev}, "title": {"Note one"}, "status": {"reading"}, "tags": {"harbour, Weather"}, "body": {"Low water at **six**."}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("save: %d", rec.Code)
			}
			_, body = u.page(u.owner, sd.base+"/research?tag=harbour")
			if !strings.Contains(body, "Note one") || strings.Contains(body, "Loose") || !strings.Contains(body, `class="tagdot cover-0"`) {
				t.Errorf("the tag filter or the swatch is wrong")
			}
			_, body = u.page(u.owner, sd.base+"/research/tags")
			for _, want := range []string{"harbour", "Weather", "Teal", `name="color" value="rose"`, "Delete the tag harbour"} {
				if !strings.Contains(body, want) {
					t.Errorf("tags page lacks %q", want)
				}
			}
			tags := tagIDs(t, u, sd)
			if rec := post("tag-update", url.Values{"tag_id": {tags["harbour"]}, "name": {"harbours"}, "color": {"blue"}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("tag-update: %d", rec.Code)
			}
			if rec := post("tag-merge", url.Values{"tag_id": {tags["weather"]}, "into_id": {tags["harbour"]}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("tag-merge: %d", rec.Code)
			}
			if rec := post("tag-delete", url.Values{"tag_id": {tags["harbour"]}}); rec.Code != http.StatusSeeOther {
				t.Fatalf("tag-delete: %d", rec.Code)
			}
			_, body = u.page(u.owner, sd.base+"/research")
			if !strings.Contains(body, "Note one") {
				t.Errorf("deleting a tag deleted a note")
			}

			// The board moves a card and says so when the revision is stale.
			_, bb := u.page(u.owner, sd.base+"/research/board")
			for _, want := range []string{"Inbox", "Reading", "Useful", "Archived", `class="r-moveto"`, `aria-live="polite"`} {
				if !strings.Contains(bb, want) {
					t.Errorf("board lacks %q", want)
				}
			}
			cur := noteRev(t, u, sd, ids["Note one"])
			if rec := post("board-move", url.Values{"note_id": {ids["Note one"]}, "rev": {fmt.Sprint(cur)}, "to": {"useful"}, "from": {"reading"}, "by": {"status"}}); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "#c-") {
				t.Errorf("board-move: %d %s", rec.Code, rec.Header().Get("Location"))
			}
			rec = post("board-move", url.Values{"note_id": {ids["Note one"]}, "rev": {fmt.Sprint(cur)}, "to": {"archived"}, "from": {"useful"}, "by": {"status"}})
			if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "e=stale") {
				t.Errorf("a stale board move: %d %s", rec.Code, rec.Header().Get("Location"))
			}
			if _, b := u.page(u.owner, sd.base+"/research/board?e=stale"); !strings.Contains(b, "changed somewhere else") {
				t.Errorf("the stale move is not explained")
			}
			if rec := post("board-move", url.Values{"note_id": {ids["Note one"]}, "rev": {fmt.Sprint(noteRev(t, u, sd, ids["Note one"]))}, "to": {"bogus"}, "from": {"useful"}, "by": {"status"}}); rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("a column that is not there: %d", rec.Code)
			}
		})
	}
}

func (u *resUI) creaderOrReader(sd resSide) caller {
	if sd.kind == "c" {
		return u.creader
	}
	return u.reader
}

var reIDRow = regexp.MustCompile(`(?s)id="n-([^"]+)".*?<(?:a class="r-name"[^>]*>|strong class="r-name">)([^<]*)<`)

func idsByTitle(t *testing.T, u *resUI, sd resSide) map[string]string {
	t.Helper()
	_, m := u.call(u.owner, "GET", sd.api+"/research", "")
	out := map[string]string{}
	for _, it := range m["items"].([]any) {
		x := it.(map[string]any)
		out[x["title"].(string)] = x["id"].(string)
	}
	return out
}

func tagIDs(t *testing.T, u *resUI, sd resSide) map[string]string {
	t.Helper()
	_, m := u.call(u.owner, "GET", sd.api+"/tags", "")
	out := map[string]string{}
	for _, it := range m["items"].([]any) {
		x := it.(map[string]any)
		out[strings.ToLower(x["name"].(string))] = x["id"].(string)
	}
	return out
}

func noteRev(t *testing.T, u *resUI, sd resSide, id string) int {
	t.Helper()
	_, m := u.call(u.owner, "GET", sd.api+"/research/"+id, "")
	return int(m["item"].(map[string]any)["revision"].(float64))
}

func fieldValue(html, name string) string {
	re := regexp.MustCompile(`name="` + name + `" value="([^"]*)"`)
	if m := re.FindStringSubmatch(html); m != nil {
		return m[1]
	}
	return ""
}

// A save from a stale page keeps the person's text, shows the other version, and a save with the new revision wins.
func TestResearchNoteConflictKeepsTheirText(t *testing.T) {
	u := newResUI(t)
	for _, sd := range u.sides() {
		id, rev := u.makeNote(t, sd, `{"title":"Shared","body":"first draft"}`)
		// Someone else saves.
		if code, _ := u.call(u.editor, "PATCH", sd.api+"/research/"+id, fmt.Sprintf(`{"base_revision":%d,"body":"the editor's text"}`, rev)); code != 200 {
			t.Fatalf("editor save: %d", code)
		}
		rec := u.s.post(t, u.owner, sd.ui+"/research/save", url.Values{"note_id": {id}, "rev": {fmt.Sprint(rev)}, "title": {"Shared"}, "status": {"inbox"}, "body": {"my careful rewrite"}}, "", false)
		body := rec.Body.String()
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s stale save: %d, want 409", sd.kind, rec.Code)
		}
		for _, want := range []string{"my careful rewrite", "the editor&#39;s text", `id="r-conflict"`, "open the note again", `value="` + fmt.Sprint(rev+1) + `"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s conflict page lacks %q", sd.kind, want)
			}
		}
		// Keep mine: the page's own form carries the new revision, so saving it wins and nothing else changed.
		if rec := u.s.post(t, u.owner, sd.ui+"/research/save", url.Values{"note_id": {id}, "rev": {fmt.Sprint(rev + 1)}, "title": {"Shared"}, "status": {"inbox"}, "body": {"my careful rewrite"}}, "", false); rec.Code != http.StatusSeeOther {
			t.Errorf("keep mine: %d", rec.Code)
		}
		_, m := u.call(u.owner, "GET", sd.api+"/research/"+id, "")
		if m["item"].(map[string]any)["body"] != "my careful rewrite" {
			t.Errorf("keep mine did not save")
		}
	}
}

// A clipping's address is text. Nothing links off the site, and opening it takes two deliberate steps in script.
func TestClippingAddressIsNeverALink(t *testing.T) {
	s := newWebServer(t)
	for _, kind := range []string{"p", "c"} {
		page := renderPage(t, s, fixture{"research-note", Page{Title: "Clipping", Path: "/x", User: fixtureUser("writer"), CSRF: "tok", Data: resFixtureNote(kind, true, true, false)}})
		if strings.Contains(page, `href="http`) || strings.Contains(page, `href='http`) {
			t.Errorf("%s: a clipping page links off the site", kind)
		}
		for _, want := range []string{`id="n-src"`, "https://example.org/diary?x=1&amp;y=2", "This opens another website.", "Scríobh never fetches it, and the site will see your visit.", `data-go`, `data-cancel`, `aria-expanded="false"`} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: clipping page lacks %q", kind, want)
			}
		}
		// Script turns the buttons on; without it there is no way to open the address by accident.
		for _, b := range []string{`data-copy="#n-src" hidden`, `data-open aria-controls="n-warn" aria-expanded="false" hidden`, `id="n-warn" class="r-warn" role="group" aria-labelledby="n-warn-h" hidden`} {
			if !strings.Contains(page, b) {
				t.Errorf("%s: %q is not hidden until script runs", kind, b)
			}
		}
		if strings.Contains(page, "The sea was <quiet>.") {
			t.Errorf("%s: a quote was not escaped", kind)
		}
	}
	js := readStatic(t, "js/pad.js")
	for _, want := range []string{"noopener,noreferrer", "^https?:"} {
		if !strings.Contains(js, want) {
			t.Errorf("pad.js lacks %q", want)
		}
	}
}

func readStatic(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(staticFS(t), name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Readers see everything and can change nothing.
func TestResearchReadersGetNoControls(t *testing.T) {
	s := newWebServer(t)
	for _, kind := range []string{"p", "c"} {
		d := resFixtureData(kind, false, false)
		page := renderPage(t, s, fixture{"research", Page{Title: "Research", Path: "/x", User: fixtureUser("reader"), CSRF: "tok", Data: d}})
		for _, bad := range []string{"/research/create", "/research/trash", "/research/move", `name="act"`, "/research/restore"} {
			if strings.Contains(page, bad) {
				t.Errorf("%s: a reader's list has %q", kind, bad)
			}
		}
		note := renderPage(t, s, fixture{"research-note", Page{Title: "Note", Path: "/x", User: fixtureUser("reader"), CSRF: "tok", Data: resFixtureNote(kind, false, true, false)}})
		for _, w := range []string{"/research/save", "/research/trash", `id="pad-state"`} {
			if strings.Contains(note, w) {
				t.Errorf("%s: a reader's note page has %q", kind, w)
			}
		}
		if !strings.Contains(note, "You can read this research but not change it.") || strings.Contains(note, "/research/save") || strings.Contains(note, "/research/trash") || strings.Contains(note, `id="pad-state"`) {
			t.Errorf("%s: a reader's note page has controls", kind)
		}
		if !strings.Contains(note, `data-can-edit="false"`) || strings.Count(note, " readonly") < 4 {
			t.Errorf("%s: a reader's note fields are not read only", kind)
		}
		tags := renderPage(t, s, fixture{"research-tags", Page{Title: "Tags", Path: "/x", User: fixtureUser("reader"), CSRF: "tok", Data: resFixtureTagsPage(kind, false)}})
		if strings.Contains(tags, "tag-create") || strings.Contains(tags, "tag-delete") {
			t.Errorf("%s: a reader's tag page has controls", kind)
		}
	}
}

// Every tag colour is a token with a word, a swatch class, and the name beside it.
func TestTagColoursHaveWordsAndSwatches(t *testing.T) {
	want := []string{"slate", "clay", "ochre", "moss", "teal", "blue", "plum", "rose"}
	for _, c := range want {
		if resColorClass[c] == "" || resColorWord[c] == "" {
			t.Errorf("colour %q has no swatch or word", c)
		}
	}
	if len(resColorClass) != len(want) || len(resColorWord) != len(want) {
		t.Errorf("the maps hold %d and %d colours, want %d", len(resColorClass), len(resColorWord), len(want))
	}
	s := newWebServer(t)
	page := renderPage(t, s, fixture{"research-tags", Page{Title: "Tags", Path: "/x", User: fixtureUser("writer"), CSRF: "tok", Data: resFixtureTagsPage("p", true)}})
	for _, c := range want {
		if !strings.Contains(page, `value="`+c+`"`) {
			t.Errorf("no radio for %s", c)
		}
	}
	if strings.Count(page, `class="tagdot`) < 8 || !strings.Contains(page, `aria-hidden="true"></span><span>Slate</span>`) {
		t.Errorf("a swatch is not shown with its word")
	}
}

// A long list is paged, not drawn whole.
func TestResearchTreeIsPaged(t *testing.T) {
	u := newResUI(t)
	sd := u.sides()[0]
	for i := 0; i < resPageSize+5; i++ {
		u.makeNote(t, sd, fmt.Sprintf(`{"title":"Note %03d","body":"some words"}`, i))
	}
	_, body := u.page(u.owner, sd.base+"/research")
	if n := strings.Count(body, `class="r-row`); n != resPageSize {
		t.Errorf("first page has %d rows, want %d", n, resPageSize)
	}
	if !strings.Contains(body, "Showing 1 to 100 of 105") || !strings.Contains(body, `rel="next"`) {
		t.Errorf("the page does not say it is partial")
	}
	_, body = u.page(u.owner, sd.base+"/research?p=2")
	if n := strings.Count(body, `class="r-row`); n != 5 || !strings.Contains(body, `rel="prev"`) {
		t.Errorf("second page: %d rows", n)
	}
	// The fragment the live refresh fetches answers the same list without the page around it.
	rec := u.s.do(u.owner, "GET", sd.ui+"/research/tree?p=2", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "<html") || !strings.Contains(rec.Body.String(), `id="r-tree"`) {
		t.Errorf("tree fragment: %d", rec.Code)
	}
}

func TestResearchSearchShowsMatchesAndSaysWhereOtherwise(t *testing.T) {
	u := newResUI(t)
	for _, sd := range u.sides() {
		u.makeNote(t, sd, `{"title":"Tide tables","body":"Low water at six, high water at noon."}`)
		u.makeNote(t, sd, `{"title":"Rope","body":"Hemp and tar."}`)
		_, body := u.page(u.owner, sd.base+"/research?q=hemp")
		if !strings.Contains(body, "Rope") || strings.Contains(body, "Tide tables") || !strings.Contains(body, "<mark>") {
			t.Errorf("%s: search for hemp: %s", sd.kind, between(body, `<section`, `</section>`))
		}
		_, body = u.page(u.owner, sd.base+"/research?q=zzzzqq")
		if !strings.Contains(body, "Nothing matches.") || !strings.Contains(body, "0 matches") {
			t.Errorf("%s: no honest empty search result", sd.kind)
		}
	}
}

// ---- the scratchpad ---------------------------------------------------------------------------------------

func TestScratchpadIsPrivateOnBothOwners(t *testing.T) {
	u := newResUI(t)
	for _, sd := range u.sides() {
		sp := sd.ui + "/scratch"
		rec := u.s.post(t, u.owner, sp, url.Values{"content": {"my private plan for the ending"}, "rev": {"0"}}, "", false)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("%s save: %d", sd.kind, rec.Code)
		}
		_, body := u.page(u.owner, sd.base+"/scratch")
		for _, want := range []string{"my private plan for the ending", "Private to you.", "an administrator included", "never searched, exported, mirrored, pushed", "not encrypted", "256 KB", `data-pad="scratch"`, `data-method="PUT"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s: the scratch page lacks %q", sd.kind, want)
			}
		}
		if rec := u.s.do(u.owner, "GET", sd.base+"/scratch", ""); !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
			t.Errorf("%s: the pad can be cached", sd.kind)
		}
		// Nobody else reads it: not an editor, not the instance admin, not a reader (who has a pad of their own).
		for _, c := range append(sd.reads[1:], u.admin) {
			code, other := u.page(c, sd.base+"/scratch")
			if code == http.StatusOK && strings.Contains(other, "private plan") {
				t.Errorf("%s: %s read the owner's pad", sd.kind, c.name)
			}
		}
		// A reader keeps a pad of their own.
		rd := u.creaderOrReader(sd)
		if rec := u.s.post(t, rd, sp, url.Values{"content": {"a reader's note"}, "rev": {"0"}}, "", false); rec.Code != http.StatusSeeOther {
			t.Errorf("%s: a reader saving a pad: %d", sd.kind, rec.Code)
		}
		if _, b := u.page(rd, sd.base+"/scratch"); !strings.Contains(b, "a reader&#39;s note") || strings.Contains(b, "private plan") {
			t.Errorf("%s: the reader's pad is wrong", sd.kind)
		}
		// The owner's pad is still the owner's.
		if _, b := u.page(u.owner, sd.base+"/scratch"); strings.Contains(b, "reader&#39;s note") {
			t.Errorf("%s: the reader's pad reached the owner", sd.kind)
		}
		// The pad is in no export, search or list: searching for its words finds nothing.
		_, found := u.page(u.owner, sd.base+"/research?q=ending")
		if strings.Contains(found, "private plan") {
			t.Errorf("%s: a pad turned up in search", sd.kind)
		}
		// A stale save keeps the person's text and shows the other version.
		rec = u.s.post(t, u.owner, sp, url.Values{"content": {"my tab's text"}, "rev": {"1"}}, "", false)
		_ = rec
		rec = u.s.post(t, u.owner, sp, url.Values{"content": {"a stale tab's text"}, "rev": {"0"}}, "", false)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "a stale tab&#39;s text") || !strings.Contains(rec.Body.String(), `id="r-conflict"`) {
			t.Errorf("%s: a stale pad save: %d", sd.kind, rec.Code)
		}
		// Too large says so in words.
		rec = u.s.post(t, u.owner, sp, url.Values{"content": {strings.Repeat("a", 262145)}, "rev": {"2"}}, "", false)
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: a pad over 256 KB: %d", sd.kind, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "256") {
			t.Errorf("%s: the limit is not named", sd.kind)
		}
	}
}

// Ending a Compendium membership, or a project's, closes the pad to that person.
func TestScratchpadClosesWithMembership(t *testing.T) {
	u := newResUI(t)
	for _, sd := range u.sides() {
		rd := u.creaderOrReader(sd)
		if rec := u.s.post(t, rd, sd.ui+"/scratch", url.Values{"content": {"kept while I belong"}, "rev": {"0"}}, "", false); rec.Code != http.StatusSeeOther {
			t.Fatalf("%s: %d", sd.kind, rec.Code)
		}
		table, col, owner := "project_members", "project_id", u.pid
		if sd.kind == "c" {
			table, col, owner = "compendium_members", "compendium_id", u.cid
		}
		err := u.s.DB.WithTx(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`DELETE FROM `+table+` WHERE `+col+`=? AND user_id=?`, owner, rd.userID)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if code, body := u.page(rd, sd.base+"/scratch"); code != http.StatusNotFound || strings.Contains(body, "kept while I belong") {
			t.Errorf("%s: after leaving, the pad page is %d", sd.kind, code)
		}
		if rec := u.s.post(t, rd, sd.ui+"/scratch", url.Values{"content": {"x"}, "rev": {"1"}}, "", false); rec.Code != http.StatusNotFound {
			t.Errorf("%s: after leaving, a save is %d", sd.kind, rec.Code)
		}
	}
}

// A project reader who is not on the Compendium reaches none of its research, board, tags or pad by any id.
func TestProjectReaderCannotReachCompendiumResearch(t *testing.T) {
	u := newResUI(t)
	sd := u.sides()[1]
	id, _ := u.makeNote(t, sd, `{"title":"Compendium secret","body":"hidden"}`)
	for _, p := range []string{sd.base + "/research", sd.base + "/research/board", sd.base + "/research/tags", sd.base + "/research/" + id, sd.base + "/scratch",
		sd.ui + "/research/tree", sd.ui + "/research/board"} {
		if code, body := u.page(u.reader, p); code != http.StatusNotFound || strings.Contains(body, "Compendium secret") {
			t.Errorf("%s as a project reader: %d", p, code)
		}
	}
	// And the project's own id never opens a Compendium note (nor the reverse).
	psd := u.sides()[0]
	if code, _ := u.page(u.owner, psd.base+"/research/"+id); code != http.StatusNotFound {
		t.Errorf("a Compendium note opened under a project: %d", code)
	}
}

// The new pages are dressed for all the screens and all the themes without anything that needs script to be safe.
func TestResearchPagesMarkup(t *testing.T) {
	s := newWebServer(t)
	for _, f := range researchPageFixtures(Page{User: fixtureUser("writer"), CSRF: "tok_writer", Path: "/"}, Page{User: fixtureUser("reader"), CSRF: "tok_reader", Path: "/"}) {
		doc := renderPage(t, s, f)
		label := f.name + "/" + f.page.Title
		for _, bad := range []string{"style=", "onclick=", "onchange=", "gradient"} {
			if strings.Contains(doc, bad) {
				t.Errorf("%s: contains %q", label, bad)
			}
		}
		if visible := reTags.ReplaceAllString(reScripts.ReplaceAllString(doc, ""), " "); strings.Contains(visible, "!") {
			t.Errorf("%s: the copy shouts", label)
		}
		if strings.Count(doc, `<h1`) != 1 {
			t.Errorf("%s: want one h1", label)
		}
		// Every form control inside the page has a label or an aria-label.
		for _, m := range regexp.MustCompile(`<(input|select|textarea)([^>]*)>`).FindAllStringSubmatch(doc, -1) {
			attrs := m[2]
			if strings.Contains(attrs, `type="hidden"`) || strings.Contains(attrs, `type="submit"`) || strings.Contains(attrs, `type="radio"`) {
				continue
			}
			id := regexp.MustCompile(`id="([^"]+)"`).FindStringSubmatch(attrs)
			if id == nil {
				t.Errorf("%s: a control without an id: %s", label, m[0])
				continue
			}
			if !strings.Contains(doc, `for="`+id[1]+`"`) && !strings.Contains(attrs, "aria-label") {
				t.Errorf("%s: control %s has no label", label, id[1])
			}
		}
	}
}

var (
	reTags    = regexp.MustCompile(`(?s)<[^>]*>`)
	reScripts = regexp.MustCompile(`(?s)<(script|!--).*?(</script>|-->)|<!doctype[^>]*>`)
)

var _ = template.HTML("")
