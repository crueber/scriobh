package server

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
)

type resFixture struct {
	*attFixture
	pbase, cbase string
}

func newResFixture(t *testing.T) *resFixture {
	t.Helper()
	f := &resFixture{attFixture: newAttFixture(t, nil)}
	f.pbase = "/api/v1/projects/" + f.pid
	f.cbase = "/api/v1/compendiums/" + f.cid
	// The reader is on the project but not the Compendium; the Compendium is the project's primary.
	if rec := f.s.do(f.owner, "PUT", f.pbase+"/compendium", `{"compendium_id":"`+f.cid+`"}`); rec.Code != 200 {
		t.Fatalf("set the primary: %d %s", rec.Code, rec.Body)
	}
	f.s.addCompendiumMember(t, f.cid, f.editor.userID, ops.ProjectEditor)
	return f
}

func (f *resFixture) call(c caller, method, path, body string) (int, map[string]any) {
	rec := f.s.do(c, method, path, body)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func (f *resFixture) mustNote(t *testing.T, c caller, base, body string) (id string, rev int) {
	t.Helper()
	code, m := f.call(c, "POST", base+"/research", body)
	if code != 201 {
		t.Fatalf("create %s: %d %v", body, code, m)
	}
	it := m["item"].(map[string]any)
	return it["id"].(string), int(it["revision"].(float64))
}

func (f *resFixture) titles(t *testing.T, c caller, path string) []string {
	t.Helper()
	code, m := f.call(c, "GET", path, "")
	if code != 200 {
		t.Fatalf("GET %s: %d %v", path, code, m)
	}
	var out []string
	for _, it := range m["items"].([]any) {
		out = append(out, it.(map[string]any)["title"].(string))
	}
	return out
}

func eq(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

func TestResearchNotesCRUDForProjectsAndCompendiums(t *testing.T) {
	f := newResFixture(t)
	for _, o := range []struct {
		name, base string
		who        caller
	}{{"project", f.pbase, f.editor}, {"compendium", f.cbase, f.editor}} {
		t.Run(o.name, func(t *testing.T) {
			folder, _ := f.mustNote(t, o.who, o.base, `{"title":"Sources","kind":"folder"}`)
			clip, crev := f.mustNote(t, o.who, o.base, `{"title":"Harbour history","kind":"clipping","parent_id":"`+folder+`","source_url":"https://example.org/harbour",
				"retrieved_on":"2026-03-14","quote":"The quay was rebuilt in 1881.","body":"Notes on the quay.","tags":["history","Harbour"],"status":"reading"}`)
			plain, _ := f.mustNote(t, o.who, o.base, `{"title":"A thought","body":"What if the tide table lies?"}`)
			code, m := f.call(o.who, "GET", o.base+"/research/"+clip, "")
			it := m["item"].(map[string]any)
			if code != 200 || it["source_url"] != "https://example.org/harbour" || it["retrieved_on"] != "2026-03-14" || it["quote"] != "The quay was rebuilt in 1881." ||
				it["status"] != "reading" || it["parent_id"] != folder || fmt.Sprint(it["tags"]) != "[Harbour history]" || it["word_count"].(float64) != 4 {
				t.Fatalf("get: %d %v", code, it)
			}
			// The tree, in reading order: the folder, what it holds, then the loose note.
			_, l := f.call(o.who, "GET", o.base+"/research", "")
			if fmt.Sprint(l["order"]) != fmt.Sprint([]string{folder, clip, plain}) {
				t.Errorf("order %v", l["order"])
			}
			// A stale save is refused with the current revision; a fresh one bumps it.
			if code, m := f.call(o.who, "PATCH", o.base+"/research/"+clip, `{"base_revision":`+fmt.Sprint(crev+5)+`,"title":"x"}`); code != 409 ||
				m["error"].(map[string]any)["data"].(map[string]any)["revision"].(float64) != float64(crev) {
				t.Errorf("stale save: %d %v", code, m)
			}
			code, m = f.call(o.who, "PATCH", o.base+"/research/"+clip, `{"base_revision":`+fmt.Sprint(crev)+`,"body":"New body words here now.","tags":["history"],"status":"useful"}`)
			it = m["item"].(map[string]any)
			if code != 200 || it["revision"].(float64) != float64(crev+1) || fmt.Sprint(it["tags"]) != "[history]" || it["status"] != "useful" || it["word_count"].(float64) != 5 {
				t.Errorf("update: %d %v", code, it)
			}
			// An update that changes nothing does not bump the revision.
			code, m = f.call(o.who, "PATCH", o.base+"/research/"+clip, `{"base_revision":`+fmt.Sprint(crev+1)+`,"title":"Harbour history"}`)
			if code != 200 || m["item"].(map[string]any)["revision"].(float64) != float64(crev+1) {
				t.Errorf("no-op update: %d %v", code, m)
			}
			// Move the loose note into the folder, first; then the folder cannot go into itself or its child.
			if code, _ := f.call(o.who, "POST", o.base+"/research/"+plain+"/move", `{"parent_id":"`+folder+`","after":""}`); code != 200 {
				t.Errorf("move: %d", code)
			}
			_, l = f.call(o.who, "GET", o.base+"/research", "")
			if fmt.Sprint(l["order"]) != fmt.Sprint([]string{folder, plain, clip}) {
				t.Errorf("order after the move %v", l["order"])
			}
			if code, _ := f.call(o.who, "POST", o.base+"/research/"+folder+"/move", `{"parent_id":"`+folder+`"}`); code != 422 {
				t.Errorf("a folder into itself: %d", code)
			}
			sub, _ := f.mustNote(t, o.who, o.base, `{"title":"Sub","kind":"folder","parent_id":"`+folder+`"}`)
			if code, _ := f.call(o.who, "POST", o.base+"/research/"+folder+"/move", `{"parent_id":"`+sub+`"}`); code != 422 {
				t.Errorf("a folder into its own child: %d", code)
			}
			if code, _ := f.call(o.who, "POST", o.base+"/research/"+plain+"/move", `{"parent_id":"`+clip+`"}`); code != 422 {
				t.Errorf("into a note that is not a folder: %d", code)
			}
			// Trash a folder: everything in it goes, and comes back with it.
			code, m = f.call(o.who, "POST", o.base+"/research/"+folder+"/trash", "")
			if code != 200 || m["count"].(float64) != 4 {
				t.Fatalf("trash: %d %v", code, m)
			}
			if got := f.titles(t, o.who, o.base+"/research"); len(got) != 0 {
				t.Errorf("live after trashing the folder: %v", got)
			}
			if got := f.titles(t, o.who, o.base+"/research?trash=true"); len(got) != 4 {
				t.Errorf("trash holds %v", got)
			}
			if code, _ := f.call(o.who, "PATCH", o.base+"/research/"+clip, `{"base_revision":`+fmt.Sprint(crev+1)+`,"title":"x"}`); code != 409 {
				t.Errorf("editing a trashed note: %d", code)
			}
			code, m = f.call(o.who, "POST", o.base+"/research/"+folder+"/restore", "")
			if code != 200 || m["count"].(float64) != 4 {
				t.Errorf("restore: %d %v", code, m)
			}
			if got := f.titles(t, o.who, o.base+"/research"); len(got) != 4 {
				t.Errorf("live after restoring: %v", got)
			}
			// A note trashed on its own, in a folder trashed later, comes back at the top level.
			f.call(o.who, "POST", o.base+"/research/"+plain+"/trash", "")
			f.call(o.who, "POST", o.base+"/research/"+folder+"/trash", "")
			f.call(o.who, "POST", o.base+"/research/"+plain+"/restore", "")
			if _, m := f.call(o.who, "GET", o.base+"/research/"+plain, ""); m["item"].(map[string]any)["parent_id"] != "" {
				t.Errorf("an orphaned note should surface at the top: %v", m)
			}
			f.call(o.who, "POST", o.base+"/research/"+folder+"/restore", "")
			// Filters.
			if got := f.titles(t, o.who, o.base+"/research?tag=HISTORY"); !eq(got, []string{"Harbour history"}) {
				t.Errorf("by tag: %v", got)
			}
			if got := f.titles(t, o.who, o.base+"/research?status=useful"); !eq(got, []string{"Harbour history"}) {
				t.Errorf("by status: %v", got)
			}
			if got := f.titles(t, o.who, o.base+"/research?kind=folder"); len(got) != 2 {
				t.Errorf("by kind: %v", got)
			}
		})
	}
	// Notes of one owner are not reachable through another's routes.
	pn, _ := f.mustNote(t, f.owner, f.pbase, `{"title":"Project only"}`)
	if code, _ := f.call(f.owner, "GET", f.cbase+"/research/"+pn, ""); code != 404 {
		t.Errorf("a project note through the Compendium: %d", code)
	}
	if got := f.titles(t, f.owner, f.pbase+"/research"); len(got) != 0 && !containsAll(got, "Project only") {
		t.Errorf("project list: %v", got)
	}
}

func containsAll(xs []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, x := range xs {
			found = found || x == w
		}
		if !found {
			return false
		}
	}
	return true
}

func TestResearchValidation(t *testing.T) {
	f := newResFixture(t)
	for _, tc := range []struct{ name, body string }{
		{"javascript address", `{"title":"x","kind":"clipping","source_url":"javascript:alert(1)"}`},
		{"data address", `{"title":"x","source_url":"data:text/html,<script>"}`},
		{"file address", `{"title":"x","source_url":"file:///etc/passwd"}`},
		{"address with a space", `{"title":"x","source_url":"https://a b.example"}`},
		{"hostless address", `{"title":"x","source_url":"https://"}`},
		{"bad date", `{"title":"x","retrieved_on":"14/03/2026"}`},
		{"impossible date", `{"title":"x","retrieved_on":"2026-02-31"}`},
		{"bad kind", `{"title":"x","kind":"video"}`},
		{"bad status", `{"title":"x","status":"done"}`},
		{"a folder with a source", `{"title":"x","kind":"folder","source_url":"https://example.org"}`},
		{"a missing folder", `{"title":"x","parent_id":"rsn_00000000000000000000000000"}`},
		{"too many tags", `{"title":"x","tags":[` + strings.TrimSuffix(strings.Repeat(`"t`+"1"+`",`, 0), ",") + manyTags(21) + `]}`},
		{"a long tag", `{"title":"x","tags":["` + strings.Repeat("a", 41) + `"]}`},
		{"unknown field", `{"title":"x","owner":"me"}`},
	} {
		if code, m := f.call(f.owner, "POST", f.pbase+"/research", tc.body); code != 422 {
			t.Errorf("%s: %d %v", tc.name, code, m)
		}
	}
	if code, _ := f.call(f.owner, "POST", f.pbase+"/research", `{"title":"x","body":"`+strings.Repeat("a", 1<<20+1)+`"}`); code != 422 {
		t.Errorf("a body over 1 MB: %d", code)
	}
	if code, _ := f.call(f.owner, "POST", f.pbase+"/research", `{"title":"fine","kind":"clipping","source_url":"https://example.org/a?b=c#d","retrieved_on":"2026-02-28"}`); code != 201 {
		t.Errorf("a good clipping: %d", code)
	}
	// No request leaves the server for an address: the clipping is stored as typed.
	_, l := f.call(f.owner, "GET", f.pbase+"/research?kind=clipping", "")
	if it := l["items"].([]any)[0].(map[string]any); it["source_url"] != "https://example.org/a?b=c#d" {
		t.Errorf("the address was altered: %v", it)
	}
}

func manyTags(n int) string {
	var xs []string
	for i := 0; i < n; i++ {
		xs = append(xs, fmt.Sprintf(`"tag%d"`, i))
	}
	return strings.Join(xs, ",")
}

func TestResearchAuthorizationAndCompendiumPrivacy(t *testing.T) {
	f := newResFixture(t)
	cn, _ := f.mustNote(t, f.owner, f.cbase, `{"title":"Bible secret","body":"The lighthouse keeper is the villain."}`)
	pn, _ := f.mustNote(t, f.owner, f.pbase, `{"title":"Book note","body":"The lighthouse is on the north point."}`)

	// Project research: the reader reads, cannot write; a stranger is told nothing.
	for _, tc := range []struct {
		who          caller
		method, path string
		body         string
		want         int
	}{
		{f.reader, "GET", f.pbase + "/research/" + pn, "", 200},
		{f.reader, "GET", f.pbase + "/research", "", 200},
		{f.reader, "POST", f.pbase + "/research", `{"title":"x"}`, 403},
		{f.reader, "PATCH", f.pbase + "/research/" + pn, `{"base_revision":1,"title":"x"}`, 403},
		{f.reader, "POST", f.pbase + "/research/" + pn + "/trash", "", 403},
		{f.reader, "POST", f.pbase + "/tags", `{"name":"x"}`, 403},
		{f.stranger, "GET", f.pbase + "/research", "", 404},
		{f.stranger, "GET", f.pbase + "/research/" + pn, "", 404},
		{f.stranger, "GET", f.pbase + "/research-board", "", 404},
		{f.stranger, "GET", f.pbase + "/tags", "", 404},
		// The Compendium's research: the project reader is not a member.
		{f.reader, "GET", f.cbase + "/research", "", 404},
		{f.reader, "GET", f.cbase + "/research/" + cn, "", 404},
		{f.reader, "GET", f.cbase + "/research-board", "", 404},
		{f.reader, "GET", f.cbase + "/tags", "", 404},
		{f.reader, "GET", f.cbase + "/scratchpad", "", 404},
		{f.reader, "GET", f.cbase + "/search/research?q=lighthouse", "", 404},
		{f.reader, "GET", f.cbase + "/search?q=lighthouse", "", 404},
		{f.stranger, "GET", f.cbase + "/research/" + cn, "", 404},
		{f.editor, "GET", f.cbase + "/research/" + cn, "", 200}, // the project's editor was made a Compendium member by the fixture
	} {
		if code, _ := f.call(tc.who, tc.method, tc.path, tc.body); code != tc.want {
			t.Errorf("%s %s as %s: %d, want %d", tc.method, tc.path, tc.who.name, code, tc.want)
		}
	}
	// A project's routes never return the Compendium's notes, with any id.
	if code, _ := f.call(f.owner, "GET", f.pbase+"/research/"+cn, ""); code != 404 {
		t.Errorf("a Compendium note through the project: %d", code)
	}
	if got := f.titles(t, f.reader, f.pbase+"/research"); !eq(got, []string{"Book note"}) {
		t.Errorf("the project's research: %v", got)
	}
	// Search: the project's research search finds the project's note only; the
	// manuscript search finds neither; the Compendium's finds its own.
	hits := func(c caller, path string) []string {
		code, m := f.call(c, "GET", path, "")
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, m)
		}
		var out []string
		items, _ := m["items"].([]any)
		for _, it := range items {
			mm := it.(map[string]any)
			if v, ok := mm["title"]; ok {
				out = append(out, v.(string))
			}
		}
		return out
	}
	if got := hits(f.reader, f.pbase+"/search/research?q=lighthouse"); !eq(got, []string{"Book note"}) {
		t.Errorf("project research search: %v", got)
	}
	code, all := f.call(f.reader, "GET", f.pbase+"/search?q=lighthouse", "")
	if code != 200 || all["total"] != float64(0) || strings.Contains(fmt.Sprint(all), "Bible secret") || strings.Contains(fmt.Sprint(all), "Book note") {
		t.Errorf("manuscript search: %d %v (it must answer, and find no research)", code, all)
	}
	if got := hits(f.editor, f.cbase+"/search/research?q=lighthouse"); !eq(got, []string{"Bible secret"}) {
		t.Errorf("Compendium research search: %v", got)
	}
	_, cs := f.call(f.editor, "GET", f.cbase+"/search?q=lighthouse%20keeper", "")
	rs := cs["research"].([]any)
	if len(rs) != 1 || rs[0].(map[string]any)["title"] != "Bible secret" || cs["research_total"].(float64) != 1 {
		t.Errorf("search.compendium research: %v", cs)
	}
	// Even the search of a project that has the Compendium as its primary does not see it.
	if got := hits(f.owner, f.pbase+"/search/research?q=villain"); len(got) != 0 {
		t.Errorf("the project's search found the Compendium's note: %v", got)
	}
	// Trashed notes drop out of search; edits are searchable at once.
	_, cur := f.call(f.owner, "GET", f.pbase+"/research/"+pn, "")
	rev := int(cur["item"].(map[string]any)["revision"].(float64))
	f.call(f.owner, "PATCH", f.pbase+"/research/"+pn, fmt.Sprintf(`{"base_revision":%d,"body":"Now it mentions a cormorant."}`, rev))
	if got := hits(f.reader, f.pbase+"/search/research?q=cormorant"); !eq(got, []string{"Book note"}) {
		t.Errorf("after an edit: %v", got)
	}
	if got := hits(f.reader, f.pbase+"/search/research?q=north"); len(got) != 0 {
		t.Errorf("the old text is still indexed: %v", got)
	}
	f.call(f.owner, "POST", f.pbase+"/research/"+pn+"/trash", "")
	if got := hits(f.reader, f.pbase+"/search/research?q=cormorant"); len(got) != 0 {
		t.Errorf("a trashed note is searchable: %v", got)
	}
	// Keys: a key pinned to the project reaches its research and not the Compendium's; one pinned to the Compendium the reverse.
	pk, _ := f.s.mintKey(t, f.owner, `{"name":"p","project_id":"`+f.pid+`"}`)
	ck, _ := f.s.mintKey(t, f.owner, `{"name":"c","compendium_id":"`+f.cid+`"}`)
	rk, _ := f.s.mintKey(t, f.owner, `{"name":"r","scope":"read"}`)
	for _, tc := range []struct {
		key, method, path, body string
		want                    int
	}{
		{pk, "GET", f.pbase + "/research", "", 200},
		{pk, "POST", f.pbase + "/research", `{"title":"by key"}`, 201},
		{pk, "GET", f.cbase + "/research", "", 200}, // a project key reaches the primary Compendium, as the owner can
		{ck, "GET", f.cbase + "/research", "", 200},
		{ck, "GET", f.pbase + "/research", "", 404},
		{rk, "GET", f.pbase + "/research", "", 200},
		{rk, "POST", f.pbase + "/research", `{"title":"by read key"}`, 403},
		{rk, "PUT", f.pbase + "/scratchpad", `{"content":"x","base_revision":0}`, 403},
	} {
		if rec := f.s.doKey(tc.key, tc.method, tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("key %s %s: %d, want %d: %s", tc.method, tc.path, rec.Code, tc.want, rec.Body)
		}
	}
}

func TestResearchBoardAndTags(t *testing.T) {
	f := newResFixture(t)
	a, _ := f.mustNote(t, f.owner, f.pbase, `{"title":"A","tags":["Weather","tides"],"status":"reading"}`)
	b, _ := f.mustNote(t, f.owner, f.pbase, `{"title":"B","tags":["weather"]}`)
	f.mustNote(t, f.owner, f.pbase, `{"title":"C"}`)
	f.mustNote(t, f.owner, f.pbase, `{"title":"Folder","kind":"folder"}`)
	_, board := f.call(f.reader, "GET", f.pbase+"/research-board", "")
	cols := board["columns"].([]any)
	if len(cols) != 4 || board["by"] != "status" {
		t.Fatalf("board: %v", board)
	}
	count := func(c any) float64 { return c.(map[string]any)["count"].(float64) }
	if count(cols[0]) != 2 || count(cols[1]) != 1 || count(cols[2]) != 0 { // inbox B and C, reading A; the folder is not a card
		t.Errorf("columns by status: %v", cols)
	}
	_, board = f.call(f.reader, "GET", f.pbase+"/research-board?by=tag", "")
	cols = board["columns"].([]any)
	labels := []string{}
	for _, c := range cols {
		labels = append(labels, fmt.Sprintf("%s:%v", c.(map[string]any)["label"], count(c)))
	}
	if !eq(labels, []string{"tides:1", "Weather:2", "Untagged:1"}) { // "Weather" is the first spelling used; names compare folded
		t.Errorf("columns by tag: %v", labels)
	}
	if code, _ := f.call(f.reader, "GET", f.pbase+"/research-board?by=colour", ""); code != 422 {
		t.Errorf("unknown grouping: %d", code)
	}
	// The tag manager.
	_, tl := f.call(f.reader, "GET", f.pbase+"/tags", "")
	items := tl["items"].([]any)
	if len(items) != 2 || tl["colors"] == nil {
		t.Fatalf("tags: %v", tl)
	}
	var weather, tides string
	for _, it := range items {
		m := it.(map[string]any)
		switch m["name"] {
		case "Weather":
			weather = m["id"].(string)
			if m["notes"].(float64) != 2 || m["color"] != "slate" {
				t.Errorf("weather: %v", m)
			}
		case "tides":
			tides = m["id"].(string)
		}
	}
	if code, _ := f.call(f.owner, "POST", f.pbase+"/tags", `{"name":"WEATHER"}`); code != 409 {
		t.Errorf("a duplicate name: %d", code)
	}
	if code, _ := f.call(f.owner, "POST", f.pbase+"/tags", `{"name":"x","color":"hotpink"}`); code != 422 {
		t.Errorf("a colour outside the tokens: %d", code)
	}
	if code, m := f.call(f.owner, "PATCH", f.pbase+"/tags/"+weather, `{"name":"Climate","color":"teal"}`); code != 200 || m["item"].(map[string]any)["color"] != "teal" {
		t.Errorf("rename: %d %v", code, m)
	}
	if code, _ := f.call(f.owner, "PATCH", f.pbase+"/tags/"+tides, `{"name":"climate"}`); code != 409 {
		t.Errorf("renaming onto another tag: %d", code)
	}
	if _, m := f.call(f.owner, "GET", f.pbase+"/research/"+b, ""); fmt.Sprint(m["item"].(map[string]any)["tags"]) != "[Climate]" {
		t.Errorf("notes follow a rename: %v", m)
	}
	// Merge tides into Climate: A has both, so it keeps one.
	if code, m := f.call(f.owner, "POST", f.pbase+"/tags/"+tides+"/merge", `{"into_id":"`+weather+`"}`); code != 200 || m["notes"].(float64) != 1 {
		t.Errorf("merge: %d %v", code, m)
	}
	if _, m := f.call(f.owner, "GET", f.pbase+"/research/"+a, ""); fmt.Sprint(m["item"].(map[string]any)["tags"]) != "[Climate]" {
		t.Errorf("after a merge: %v", m)
	}
	if code, _ := f.call(f.owner, "POST", f.pbase+"/tags/"+weather+"/merge", `{"into_id":"`+weather+`"}`); code != 422 {
		t.Errorf("merging into itself: %d", code)
	}
	if code, m := f.call(f.owner, "DELETE", f.pbase+"/tags/"+weather, ""); code != 200 || m["notes"].(float64) != 2 {
		t.Errorf("delete: %d %v", code, m)
	}
	if _, m := f.call(f.owner, "GET", f.pbase+"/research/"+a, ""); fmt.Sprint(m["item"].(map[string]any)["tags"]) != "[]" {
		t.Errorf("after a delete: %v", m)
	}
	// Tags are per owner: the Compendium's are its own, and a tag id from the project is not found there.
	if code, _ := f.call(f.owner, "DELETE", f.cbase+"/tags/"+tides, ""); code != 404 {
		t.Errorf("another owner's tag: %d", code)
	}
	if code, _ := f.call(f.owner, "POST", f.cbase+"/tags", `{"name":"Climate"}`); code != 201 {
		t.Errorf("the same name in another owner: %d", code)
	}
}

func TestResearchRefsKeepAttachments(t *testing.T) {
	f := newResFixture(t)
	a := decodeAtt(t, f.upload(f.owner, f.pid, "filename=figure.png", pngBytes(t, 9, 9, 31), ""))
	n, _ := f.mustNote(t, f.owner, f.pbase, `{"title":"Uses a figure","body":"See ![f](`+a.Item.URL+`)"}`)
	rec := f.get(f.owner, f.pbase+"/attachments/"+a.Item.ID+"/refs")
	if !strings.Contains(rec.Body.String(), n) || !strings.Contains(rec.Body.String(), `"kind":"research"`) {
		t.Errorf("refs: %s", rec.Body)
	}
	if rec := f.s.do(f.owner, "DELETE", f.pbase+"/attachments/"+a.Item.ID, ""); rec.Code != 409 {
		t.Errorf("a figure a note still shows: %d", rec.Code)
	}
}

func TestScratchpadIsPrivate(t *testing.T) {
	f := newResFixture(t)
	sp := f.pbase + "/scratchpad"
	if code, m := f.call(f.reader, "GET", sp, ""); code != 200 || m["item"].(map[string]any)["revision"].(float64) != 0 || m["item"].(map[string]any)["content"] != "" {
		t.Fatalf("empty pad: %d %v", code, m)
	}
	// Each person keeps their own, a reader included.
	if code, m := f.call(f.owner, "PUT", sp, `{"content":"OWNER'S PRIVATE PLOT TWIST","base_revision":0}`); code != 200 || m["item"].(map[string]any)["revision"].(float64) != 1 {
		t.Fatalf("owner save: %d %v", code, m)
	}
	if code, _ := f.call(f.reader, "PUT", sp, `{"content":"reader's own note","base_revision":0}`); code != 200 {
		t.Fatalf("a reader's save: %d", code)
	}
	for _, tc := range []struct {
		who  caller
		want string
	}{{f.owner, "OWNER'S PRIVATE PLOT TWIST"}, {f.reader, "reader's own note"}, {f.editor, ""}, {f.admin, ""}} {
		_, m := f.call(tc.who, "GET", sp, "")
		if got := m["item"].(map[string]any)["content"]; got != tc.want {
			t.Errorf("%s reads %q, want %q (nobody sees another person's pad, an administrator included)", tc.who.name, got, tc.want)
		}
	}
	if code, _ := f.call(f.stranger, "GET", sp, ""); code != 404 {
		t.Errorf("a stranger: %d", code)
	}
	// Revision safety.
	if code, m := f.call(f.owner, "PUT", sp, `{"content":"x","base_revision":0}`); code != 409 || m["error"].(map[string]any)["data"].(map[string]any)["revision"].(float64) != 1 {
		t.Errorf("a stale save: %d %v", code, m)
	}
	if code, _ := f.call(f.owner, "PUT", sp, `{"content":"second draft of the twist","base_revision":1}`); code != 200 {
		t.Errorf("a fresh save: %d", code)
	}
	// Not an event, not an audit line, not in search, not in exports.
	var n int
	_ = f.s.DB.Read().QueryRow(`SELECT count(*) FROM events WHERE payload_json LIKE '%scratch%' OR type LIKE 'scratch%'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d scratchpad events", n)
	}
	_ = f.s.DB.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE detail_json LIKE '%TWIST%' OR action LIKE '%scratch%'`).Scan(&n)
	if n != 0 {
		t.Errorf("%d scratchpad audit lines", n)
	}
	for _, q := range []string{f.pbase + "/search?q=twist", f.pbase + "/search/research?q=twist", f.cbase + "/search?q=twist"} {
		rec := f.s.do(f.owner, "GET", q, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":0`) || strings.Contains(strings.ToLower(rec.Body.String()), "plot twist") {
			t.Errorf("search %s: %d %s (it must answer, and find nothing)", q, rec.Code, rec.Body)
		}
	}
	rec := f.s.do(f.owner, "POST", f.pbase+"/export", `{"mode":"single"}`)
	if strings.Contains(rec.Body.String(), "twist") {
		t.Errorf("the export holds the pad")
	}
	// Membership ends: the pad is hidden, and still there for when they come back.
	if _, err := f.s.DB.Writer().Exec(`DELETE FROM project_members WHERE user_id = ?`, f.reader.userID); err != nil {
		t.Fatal(err)
	}
	if code, _ := f.call(f.reader, "GET", sp, ""); code != 404 {
		t.Errorf("a removed member's pad: %d", code)
	}
	f.s.addMember(t, f.pid, f.reader.userID, ops.ProjectReader)
	if _, m := f.call(f.reader, "GET", sp, ""); m["item"].(map[string]any)["content"] != "reader's own note" {
		t.Errorf("the pad after being re-added: %v", m)
	}
	// A Compendium's pad is separate from the project's.
	if code, _ := f.call(f.editor, "PUT", f.cbase+"/scratchpad", `{"content":"compendium pad","base_revision":0}`); code != 200 {
		t.Errorf("compendium pad: %d", code)
	}
	if _, m := f.call(f.editor, "GET", sp, ""); m["item"].(map[string]any)["content"] != "" {
		t.Errorf("the project pad shows the Compendium's: %v", m)
	}
	// Purges take them.
	f.call(f.owner, "POST", f.pbase+"/trash", "")
	if code, _ := f.call(f.owner, "DELETE", f.pbase, `{"confirm_title":"Attached Book"}`); code >= 300 {
		t.Fatalf("purge project: %d", code)
	}
	_ = f.s.DB.Read().QueryRow(`SELECT count(*) FROM scratchpads WHERE project_id = ?`, f.pid).Scan(&n)
	if n != 0 {
		t.Errorf("%d pads remain after the project's purge", n)
	}
	f.call(f.owner, "POST", f.cbase+"/trash", "")
	if code, _ := f.call(f.owner, "DELETE", f.cbase, `{"confirm_name":"Attached Bible"}`); code >= 300 {
		t.Fatalf("purge compendium: %d", code)
	}
	_ = f.s.DB.Read().QueryRow(`SELECT count(*) FROM scratchpads`).Scan(&n)
	_ = n
	var left int
	_ = f.s.DB.Read().QueryRow(`SELECT count(*) FROM scratchpads WHERE compendium_id = ?`, f.cid).Scan(&left)
	if left != 0 {
		t.Errorf("%d pads remain after the Compendium's purge", left)
	}
}

// Every research, tag and attachment event carries ids, types and counts only.
func TestResearchEventPayloadsCarryNoText(t *testing.T) {
	f := newResFixture(t)
	n, rev := f.mustNote(t, f.owner, f.pbase, `{"title":"Secret title","body":"Secret prose.","tags":["Secret tag"],"kind":"clipping","source_url":"https://example.org/secret","quote":"Secret quote"}`)
	f.call(f.owner, "PATCH", f.pbase+"/research/"+n, fmt.Sprintf(`{"base_revision":%d,"title":"Another secret title","body":"More secret prose."}`, rev))
	f.call(f.owner, "POST", f.pbase+"/research/"+n+"/trash", "")
	f.call(f.owner, "POST", f.pbase+"/research/"+n+"/restore", "")
	f.call(f.owner, "POST", f.cbase+"/research", `{"title":"Secret bible note","body":"Secret."}`)
	f.call(f.owner, "POST", f.pbase+"/tags", `{"name":"Secret name"}`)
	f.upload(f.owner, f.pid, "filename=secret-name.png", pngBytes(t, 8, 8, 40), "")
	rows, err := f.s.DB.Read().Query(`SELECT type, payload_json FROM events WHERE type LIKE 'research.%' OR type LIKE 'tag.%' OR type LIKE 'attachment.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var typ, raw string
		_ = rows.Scan(&typ, &raw)
		seen++
		if strings.Contains(strings.ToLower(raw), "secret") {
			t.Errorf("%s carries text: %s", typ, raw)
		}
		var p map[string]any
		_ = json.Unmarshal([]byte(raw), &p)
		for k, v := range p {
			if !events.ValidPayloadKey(k) || !events.ValidValue(v) {
				t.Errorf("%s: payload key %q = %v is not an id, a count or a token", typ, k, v)
			}
		}
	}
	if seen < 6 {
		t.Errorf("only %d events seen", seen)
	}
	_ = httptest.NewRecorder
}
