package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// altWorld is a project with every kind of member, a document that carries an
// alternative and a ghost, and a stash item.
type altWorld struct {
	s                                         *Server
	owner, editor, reviewer, reader, stranger caller
	pid, did                                  string
	altID, v1, v2                             string
	itemID, trashedID                         string
}

func newAltWorld(t *testing.T) *altWorld {
	t.Helper()
	s := newAuthzServer(t)
	w := &altWorld{s: s}
	w.owner = s.newCaller(t, "owner", ops.RoleWriter)
	w.editor = s.newCaller(t, "editor", ops.RoleWriter)
	w.reviewer = s.newCaller(t, "reviewer", ops.RoleReader)
	w.reader = s.newCaller(t, "reader", ops.RoleReader)
	w.stranger = s.newCaller(t, "stranger", ops.RoleWriter)
	op := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(context.Background(), op, "project.create", projects.CreateIn{Title: "Alt Book"})
	if err != nil {
		t.Fatal(err)
	}
	w.pid = out.(projects.Item).Item.ID
	s.addMember(t, w.pid, w.editor.userID, ops.ProjectEditor)
	s.addMember(t, w.pid, w.reviewer.userID, ops.ProjectReviewer)
	s.addMember(t, w.pid, w.reader.userID, ops.ProjectReader)
	dout, err := s.Reg.Call(context.Background(), op, "doc.create", docs.CreateIn{ProjectID: w.pid, Title: "Chapter",
		Content: "The quiet house was empty, and the quiet stayed.\n\nShe waited by the old window.\n\nLast paragraph here.\n"})
	if err != nil {
		t.Fatal(err)
	}
	w.did = dout.(docs.Item).Item.ID
	// An alternative and a stash item, through REST as the owner.
	m := w.json(t, w.owner, "POST", "/api/v1/docs/"+w.did+"/alternatives", 201,
		`{"base_revision":`+strconv.Itoa(w.rev(t))+`,"scope":"word","quote":"quiet","occurrence":0,"variants":["still","hushed"]}`)
	alt := m["alternative"].(map[string]any)
	w.altID = alt["id"].(string)
	vs := alt["variants"].([]any)
	w.v1, w.v2 = vs[1].(map[string]any)["id"].(string), vs[2].(map[string]any)["id"].(string)
	it := w.json(t, w.owner, "POST", "/api/v1/projects/"+w.pid+"/stash", 201, `{"title":"Cut","content":"A cut paragraph."}`)
	w.itemID = it["item"].(map[string]any)["id"].(string)
	// A ghost to revive and a trashed stash item to restore or delete.
	w.json(t, w.owner, "POST", "/api/v1/docs/"+w.did+"/ghosts", 201,
		`{"base_revision":`+strconv.Itoa(w.rev(t))+`,"ranges":[{"quote":"Last paragraph here."}]}`)
	tr := w.json(t, w.owner, "POST", "/api/v1/projects/"+w.pid+"/stash", 201, `{"title":"Old","content":"An old cut."}`)
	w.trashedID = tr["item"].(map[string]any)["id"].(string)
	w.json(t, w.owner, "POST", "/api/v1/projects/"+w.pid+"/stash/trash", 200, `{"item_id":"`+w.trashedID+`"}`)
	return w
}

func (w *altWorld) rev(t *testing.T) int {
	t.Helper()
	rec := w.s.do(w.owner, "GET", "/api/v1/docs/"+w.did, "")
	var d struct {
		Item struct {
			Revision int `json:"revision"`
		} `json:"item"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	return d.Item.Revision
}

func (w *altWorld) json(t *testing.T, c caller, method, path string, want int, body string) map[string]any {
	t.Helper()
	rec := w.s.do(c, method, path, body)
	if rec.Code != want {
		t.Fatalf("%s %s: %d (want %d) %s", method, path, rec.Code, want, rec.Body)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return m
}

// request describes one op with a valid body, for the matrix tests.
type altReq struct {
	op, method, path, body string
	role                   string // the lowest project role that may call it
	mutates                bool
}

func (w *altWorld) requests(t *testing.T) []altReq {
	rev := strconv.Itoa(w.rev(t))
	docPath := "/api/v1/docs/" + w.did
	proj := "/api/v1/projects/" + w.pid
	alt := `"alternative_id":"` + w.altID + `"`
	return []altReq{
		{"alternative.list", "GET", docPath + "/alternatives", "", ops.ProjectReader, false},
		{"alternative.create", "POST", docPath + "/alternatives", `{"base_revision":` + rev + `,"scope":"word","quote":"window"}`, ops.ProjectEditor, true},
		{"alternative.add_variant", "POST", docPath + "/alternatives/variants", `{"base_revision":` + rev + `,` + alt + `,"text":"calm"}`, ops.ProjectEditor, true},
		{"alternative.edit_variant", "PATCH", docPath + "/alternatives/variants", `{"base_revision":` + rev + `,` + alt + `,"variant_id":"` + w.v1 + `","text":"stiller"}`, ops.ProjectEditor, true},
		{"alternative.remove_variant", "POST", docPath + "/alternatives/variants/remove", `{"base_revision":` + rev + `,` + alt + `,"variant_id":"` + w.v2 + `"}`, ops.ProjectEditor, true},
		{"alternative.reorder_variant", "POST", docPath + "/alternatives/variants/reorder", `{"base_revision":` + rev + `,` + alt + `,"variant_id":"` + w.v2 + `"}`, ops.ProjectEditor, true},
		{"alternative.choose", "POST", docPath + "/alternatives/choose", `{"base_revision":` + rev + `,` + alt + `,"step":"next"}`, ops.ProjectEditor, true},
		{"alternative.reanchor", "POST", docPath + "/alternatives/reanchor", `{"base_revision":` + rev + `,` + alt + `,"quote":"house"}`, ops.ProjectEditor, true},
		{"alternative.delete", "POST", docPath + "/alternatives/delete", `{"base_revision":` + rev + `,` + alt + `}`, ops.ProjectEditor, true},
		{"ghost.list", "GET", docPath + "/ghosts", "", ops.ProjectReader, false},
		{"ghost.create", "POST", docPath + "/ghosts", `{"base_revision":` + rev + `,"ranges":[{"quote":"She waited by the old window."}]}`, ops.ProjectEditor, true},
		{"ghost.revive", "POST", docPath + "/ghosts/revive", `{"base_revision":` + rev + `,"all":true}`, ops.ProjectEditor, true},
		// A move trashes the item too: the preview must discard that row and its event.
		{"stash.drag_back", "POST", proj + "/stash/drag-back", `{"item_id":"` + w.itemID + `","document_id":"` + w.did + `","base_revision":` + rev + `,"mode":"move"}`, ops.ProjectEditor, true},
		{"stash.restore", "POST", proj + "/stash/restore", `{"item_id":"` + w.trashedID + `"}`, ops.ProjectEditor, true},
		{"stash.delete", "POST", proj + "/stash/delete", `{"item_id":"` + w.trashedID + `"}`, ops.ProjectEditor, true},
		{"stash.create", "POST", proj + "/stash", `{"title":"New","content":"Words."}`, ops.ProjectEditor, true},
		{"stash.list", "GET", proj + "/stash", "", ops.ProjectEditor, false},
		{"stash.get", "GET", proj + "/stash/item?item_id=" + w.itemID, "", ops.ProjectEditor, false},
		{"stash.update", "PATCH", proj + "/stash/item", `{"item_id":"` + w.itemID + `","base_revision":1,"title":"Cut 2"}`, ops.ProjectEditor, true},
		{"stash.trash", "POST", proj + "/stash/trash", `{"item_id":"` + w.itemID + `"}`, ops.ProjectEditor, true},
		{"stash.reorder", "POST", proj + "/stash/reorder", `{"item_id":"` + w.itemID + `"}`, ops.ProjectEditor, true},
		{"stash.drag_back", "POST", proj + "/stash/drag-back", `{"item_id":"` + w.itemID + `","document_id":"` + w.did + `","base_revision":` + rev + `}`, ops.ProjectEditor, true},
	}
}

var projRank = map[string]int{ops.ProjectReader: 1, ops.ProjectReviewer: 2, ops.ProjectEditor: 3, ops.ProjectOwner: 4}

// Every new op, by every kind of caller: the declared role decides, a stranger
// is told 404 whatever the op, and the stash is closed to readers and reviewers
// even for reading.
func TestAlternativeOpsAuthorizationMatrix(t *testing.T) {
	w := newAltWorld(t)
	people := []struct {
		c    caller
		rank int // 0: no access
	}{{w.owner, 4}, {w.editor, 3}, {w.reviewer, 2}, {w.reader, 1}, {w.stranger, 0}}
	for _, r := range w.requests(t) {
		for _, p := range people {
			// Dry runs change nothing, so every caller can be tried on the same state.
			rec := w.s.doDry(p.c, r.method, r.path, r.body)
			switch {
			case p.rank == 0:
				if rec.Code != http.StatusNotFound {
					t.Errorf("%s as a stranger: %d, want 404", r.op, rec.Code)
				}
			case p.rank < projRank[r.role]:
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s as %s: %d, want 403", r.op, p.c.name, rec.Code)
				}
			default:
				if rec.Code >= 300 {
					t.Errorf("%s as %s: %d %s", r.op, p.c.name, rec.Code, rec.Body)
				}
			}
		}
		// The nonexistent and the forbidden look the same to a stranger.
		bogus := strings.NewReplacer(w.did, "doc_00000000000000000000000000", w.pid, "prj_00000000000000000000000000").Replace(r.path)
		a, b := w.s.do(w.stranger, r.method, r.path, r.body), w.s.do(w.stranger, r.method, bogus, r.body)
		if a.Code != 404 || b.Code != 404 || a.Body.String() != b.Body.String() {
			t.Errorf("%s: existing-but-forbidden (%d) and nonexistent (%d) must be indistinguishable", r.op, a.Code, b.Code)
		}
	}
}

// A preview of each op reports what it would do and changes nothing.
func TestAlternativeOpsDryRun(t *testing.T) {
	w := newAltWorld(t)
	for _, r := range w.requests(t) {
		before := testutil.Dump(t, w.s.DB)
		dir := testutil.TreeDigest(t, w.s.Cfg.DataDir)
		rec := w.s.doDry(w.owner, r.method, r.path, r.body)
		if rec.Code >= 300 {
			t.Errorf("%s: %d %s", r.op, rec.Code, rec.Body)
			continue
		}
		m := decodeObj(t, rec)
		if m["dry_run"] != true {
			t.Errorf("%s: no dry_run in %s", r.op, rec.Body)
		}
		if r.mutates && m["would_change"] != true {
			t.Errorf("%s: a preview should report a change: %s", r.op, rec.Body)
		}
		if testutil.Dump(t, w.s.DB) != before {
			t.Errorf("%s: the dry run changed the database", r.op)
		}
		if testutil.TreeDigest(t, w.s.Cfg.DataDir) != dir {
			t.Errorf("%s: the dry run changed the data directory", r.op)
		}
	}
	// The preview of a move left the stash item where it was, and wrote no event.
	it := w.json(t, w.owner, "GET", "/api/v1/projects/"+w.pid+"/stash/item?item_id="+w.itemID, 200, "")["item"].(map[string]any)
	if it["trashed"] != false {
		t.Error("a dry run of drag_back with mode move trashed the item")
	}
	var n int
	_ = w.s.DB.Read().QueryRow(`SELECT count(*) FROM events WHERE type = 'stash.trashed'`).Scan(&n)
	if n != 1 { // the one made by the world's own setup
		t.Errorf("%d stash.trashed events: a preview must write none", n)
	}
	// Every op the phase added is in the matrix (a new one must be added).
	have := map[string]bool{}
	for _, r := range w.requests(t) {
		have[r.op] = true
	}
	for _, e := range w.s.Reg.Entries() {
		n := e.Op.Name
		if (strings.HasPrefix(n, "alternative.") || strings.HasPrefix(n, "ghost.") || strings.HasPrefix(n, "stash.")) && !have[n] {
			t.Errorf("%s is not in the request matrix", n)
		}
	}
}

// The whole life of the feature over REST: create, cycle, edit, ghost, stash,
// drag back; then every event it wrote carries ids and counts only.
func TestAlternativesOverREST(t *testing.T) {
	w := newAltWorld(t)
	doc := "/api/v1/docs/" + w.did
	proj := "/api/v1/projects/" + w.pid
	rev := func() string { return strconv.Itoa(w.rev(t)) }
	alt := `"alternative_id":"` + w.altID + `"`

	l := w.json(t, w.reader, "GET", doc+"/alternatives", 200, "")
	if l["active"].(float64) != 1 || len(l["items"].([]any)) != 1 {
		t.Fatalf("list: %v", l)
	}
	c := w.json(t, w.editor, "POST", doc+"/alternatives/choose", 200, `{"base_revision":`+rev()+`,`+alt+`,"variant_id":"`+w.v1+`","with_content":true}`)
	if !strings.HasPrefix(c["content"].(string), "The still house") || c["replaced"].(map[string]any)["text"] != "still" || !strings.HasPrefix(c["comment"].(string), "<!-- scriobh:alts ") {
		t.Fatalf("choose: %v", c)
	}
	// A stale revision is a 409 that names the current one.
	rec := w.s.do(w.editor, "POST", doc+"/alternatives/choose", `{"base_revision":1,`+alt+`,"step":"next"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "current_revision") {
		t.Errorf("stale: %d %s", rec.Code, rec.Body)
	}
	w.json(t, w.editor, "POST", doc+"/alternatives/choose", 200, `{"base_revision":`+rev()+`,`+alt+`,"step":"original"}`)
	w.json(t, w.editor, "PATCH", doc+"/alternatives/variants", 200, `{"base_revision":`+rev()+`,`+alt+`,"variant_id":"`+w.v2+`","text":"hushed, low"}`)
	g := w.json(t, w.editor, "POST", doc+"/ghosts", 201, `{"base_revision":`+rev()+`,"ranges":[{"quote":"She waited by the old window."}]}`)
	if g["ghosted_words"].(float64) != 9 || len(g["ghosts"].([]any)) != 2 { // this one and the one the world began with
		t.Fatalf("ghost: %v", g)
	}
	gl := w.json(t, w.reader, "GET", doc+"/ghosts", 200, "")
	gid := gl["items"].([]any)[0].(map[string]any)["id"].(string)
	w.json(t, w.editor, "POST", doc+"/ghosts/revive", 200, `{"base_revision":`+rev()+`,"ghost_ids":["`+gid+`"]}`)
	// The stash.
	w.json(t, w.editor, "POST", proj+"/stash", 201, `{"title":"Second","content":"Another cut line about harbours."}`)
	sr := w.json(t, w.owner, "GET", proj+"/stash?q=harbour", 200, "")
	if sr["total"].(float64) != 1 || !strings.Contains(sr["items"].([]any)[0].(map[string]any)["snippet_html"].(string), "<mark>") {
		t.Fatalf("stash search: %v", sr)
	}
	it := w.json(t, w.owner, "GET", proj+"/stash/item?item_id="+w.itemID, 200, "")["item"].(map[string]any)
	w.json(t, w.owner, "PATCH", proj+"/stash/item", 200, `{"item_id":"`+w.itemID+`","base_revision":`+strconv.Itoa(int(it["revision"].(float64)))+`,"content":"A longer cut paragraph."}`)
	d := w.json(t, w.owner, "POST", proj+"/stash/drag-back", 200, `{"item_id":"`+w.itemID+`","document_id":"`+w.did+`","base_revision":`+rev()+`,"mode":"move","with_content":true}`)
	if !strings.Contains(d["content"].(string), "A longer cut paragraph.\n") || d["moved"] != true {
		t.Fatalf("drag back: %v", d)
	}
	w.json(t, w.owner, "POST", proj+"/stash/restore", 200, `{"item_id":"`+w.itemID+`"}`)
	w.json(t, w.owner, "POST", proj+"/stash/trash", 200, `{"item_id":"`+w.itemID+`"}`)
	w.json(t, w.owner, "POST", proj+"/stash/delete", 200, `{"item_id":"`+w.itemID+`"}`)
	w.json(t, w.editor, "POST", doc+"/alternatives/delete", 200, `{"base_revision":`+rev()+`,`+alt+`}`)

	// Ids and counts only in every event, whatever the text said.
	rows, err := w.s.DB.Read().Query(`SELECT type, payload_json FROM events`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var typ, raw string
		if err := rows.Scan(&typ, &raw); err != nil {
			t.Fatal(err)
		}
		seen[typ] = true
		var p map[string]any
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("%s: %s", typ, raw)
		}
		for k, v := range p {
			if !events.ValidPayloadKey(k) || (!events.ValidValue(v) && v != "") {
				t.Errorf("%s: payload %q = %v is not allowed", typ, k, v)
			}
			if str, ok := v.(string); ok && strings.ContainsAny(str, " \t\n") {
				t.Errorf("%s: payload %q holds text %q", typ, k, str)
			}
		}
	}
	for _, typ := range []string{"alternative.updated", "ghost.updated", "stash.created", "stash.updated", "stash.trashed", "stash.restored", "stash.deleted", "doc.updated"} {
		if !seen[typ] {
			t.Errorf("no %s event was written", typ)
		}
	}
}

// A trashed project is the owner's alone: its alternatives and stash are not
// reachable by anyone else.
func TestAlternativeOpsOnATrashedProject(t *testing.T) {
	w := newAltWorld(t)
	op := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter}
	if _, err := w.s.Reg.Call(context.Background(), op, "project.trash", projects.StateIn{ID: w.pid}); err != nil {
		t.Fatal(err)
	}
	for _, r := range w.requests(t) {
		for _, c := range []caller{w.editor, w.reader, w.stranger} {
			if rec := w.s.do(c, r.method, r.path, r.body); rec.Code != http.StatusNotFound {
				t.Errorf("%s as %s on a trashed project: %d, want 404", r.op, c.name, rec.Code)
			}
		}
	}
	_ = httptest.NewRecorder
}
