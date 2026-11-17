package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// Ops that name a child of an owner (an attachment, a research note, a tag) have
// a second path parameter. The sweeps substitute one id for every parameter of
// an op, so the child ids are filled from here: fixtures the sweep made, found
// again (or made again) before each request, since an op may delete or trash
// what it is swept with. The attachment is referenced by the note, so deleting
// it is a 409 rather than a 404 for a caller who is allowed to try.
var sweepChildren = map[string]string{}

// childFor fills the second parameter of an op path from the sweep's fixtures.
func childFor(name string) (string, bool) {
	v, ok := sweepChildren[name]
	return v, ok
}

// sweepFixtures (re)creates the research note, tag and attachment the sweeps
// use under one owner, as the owner. kind is "project" or "compendium".
func (s *Server) sweepFixtures(t *testing.T, owner caller, kind, id string) {
	t.Helper()
	base := "/api/v1/projects/" + id
	if kind == "compendium" {
		base = "/api/v1/compendiums/" + id
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		rec := s.do(owner, method, path, body)
		return rec
	}
	img := image.NewRGBA(image.Rect(0, 0, 6, 6))
	for x := 0; x < 6; x++ {
		img.Set(x, x, color.RGBA{200, 10, 10, 255})
	}
	var pb bytes.Buffer
	if err := png.Encode(&pb, img); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", base+"/attachments?filename=sweep.png", bytes.NewReader(pb.Bytes()))
	req.AddCookie(owner.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var up struct{ Item struct{ ID string } }
	if err := json.Unmarshal(rec.Body.Bytes(), &up); err != nil || up.Item.ID == "" {
		t.Fatalf("sweep attachment: %d %s", rec.Code, rec.Body)
	}
	// The tag.
	var tg struct{ Item struct{ ID string } }
	if rec := call("POST", base+"/tags", `{"name":"sweep"}`); rec.Code == 201 {
		_ = json.Unmarshal(rec.Body.Bytes(), &tg)
	} else {
		var l struct{ Items []struct{ ID, Name string } }
		_ = json.Unmarshal(call("GET", base+"/tags", "").Body.Bytes(), &l)
		for _, it := range l.Items {
			if it.Name == "sweep" {
				tg.Item.ID = it.ID
			}
		}
	}
	if tg.Item.ID == "" {
		t.Fatal("sweep tag")
	}
	// The note, restored if a sweep trashed it, pointing at the current attachment.
	var nl struct{ Items []struct{ ID, Title string } }
	_ = json.Unmarshal(call("GET", base+"/research", "").Body.Bytes(), &nl)
	var tl struct{ Items []struct{ ID, Title string } }
	_ = json.Unmarshal(call("GET", base+"/research?trash=true", "").Body.Bytes(), &tl)
	noteID := ""
	for _, it := range append(nl.Items, tl.Items...) {
		if it.Title == "Sweep note" {
			noteID = it.ID
		}
	}
	body := "![sweep](/files/" + up.Item.ID + ")"
	if noteID == "" {
		rec := call("POST", base+"/research", `{"title":"Sweep note","body":`+jsonString(body)+`,"tags":["sweep"]}`)
		var n struct{ Item struct{ ID string } }
		if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil || n.Item.ID == "" {
			t.Fatalf("sweep note: %d %s", rec.Code, rec.Body)
		}
		noteID = n.Item.ID
	} else {
		call("POST", base+"/research/"+noteID+"/restore", "")
		var n struct{ Item struct{ Revision int } }
		_ = json.Unmarshal(call("GET", base+"/research/"+noteID, "").Body.Bytes(), &n)
		if rec := call("PATCH", base+"/research/"+noteID, `{"base_revision":`+strconv.Itoa(n.Item.Revision)+`,"body":`+jsonString(body)+`}`); rec.Code >= 300 {
			t.Fatalf("sweep note update: %d %s", rec.Code, rec.Body)
		}
	}
	// preset_id: a built-in preset, which every project has and no op can delete
	sweepChildren = map[string]string{"att_id": up.Item.ID, "note_id": noteID, "tag_id": tg.Item.ID, "preset_id": "shunn-us"}
	if kind == "project" {
		s.reviewFixtures(t, owner, id)
		s.webhookFixtures(t, id)
		s.editionFixtures(t, id)
	}
	if kind == "compendium" {
		s.changeFixture(t, owner, id)
	}
}

// changeFixture makes one recorded split of the Compendium (once), so the ops that name a change have one to read.
func (s *Server) changeFixture(t *testing.T, owner caller, cid string) {
	t.Helper()
	var l struct{ Items []struct{ ID string } }
	_ = json.Unmarshal(s.do(owner, "GET", "/api/v1/compendiums/"+cid+"/changes", "").Body.Bytes(), &l)
	if len(l.Items) == 0 {
		rec := s.do(owner, "POST", "/api/v1/compendiums/"+cid+"/entities", `{"kind":"item","name":"Moved by the sweep"}`)
		var e struct{ Item struct{ ID string } }
		if rec.Code >= 300 || json.Unmarshal(rec.Body.Bytes(), &e) != nil {
			t.Fatalf("change fixture entity: %d %s", rec.Code, rec.Body)
		}
		body := `{"name":"Split by the sweep","entity_ids":["` + e.Item.ID + `"]}`
		var pv struct{ Plan struct{ Fingerprint string } }
		rec = s.do(owner, "POST", "/api/v1/compendiums/"+cid+"/split/preview", body)
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &pv) != nil {
			t.Fatalf("change fixture preview: %d %s", rec.Code, rec.Body)
		}
		rec = s.do(owner, "POST", "/api/v1/compendiums/"+cid+"/split", `{"name":"Split by the sweep","entity_ids":["`+e.Item.ID+`"],"fingerprint":"`+pv.Plan.Fingerprint+`"}`)
		if rec.Code != 201 {
			t.Fatalf("change fixture split: %d %s", rec.Code, rec.Body)
		}
		_ = json.Unmarshal(s.do(owner, "GET", "/api/v1/compendiums/"+cid+"/changes", "").Body.Bytes(), &l)
	}
	if len(l.Items) > 0 {
		sweepChildren["change_id"] = l.Items[0].ID
	}
}

// reviewFixtures (re)makes a comment, an open suggestion and a live share link in a
// project, as its owner, and adds their ids to sweepChildren. An op the sweep drives
// may delete, resolve, decide or revoke what it is swept with.
func (s *Server) reviewFixtures(t *testing.T, owner caller, pid string) {
	t.Helper()
	base := "/api/v1/projects/" + pid
	get := func(path string, v any) {
		rec := s.do(owner, "GET", path, "")
		if rec.Code != 200 {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
		_ = json.Unmarshal(rec.Body.Bytes(), v)
	}
	post := func(path, body string, v any) {
		rec := s.do(owner, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("POST %s: %d %s", path, rec.Code, rec.Body)
		}
		_ = json.Unmarshal(rec.Body.Bytes(), v)
	}
	var dl struct{ Items []struct{ ID, Title string } }
	get(base+"/docs", &dl)
	doc := ""
	for _, it := range dl.Items {
		if it.Title == "Sweep text" {
			doc = it.ID
		}
	}
	if doc == "" {
		var d struct{ Item struct{ ID string } }
		post(base+"/docs", `{"title":"Sweep text","kind":"chapter","content":"Sweep text for the suggestions."}`, &d)
		doc = d.Item.ID
	}
	var cl struct{ Items []struct{ ID string } }
	get(base+"/comments?q=sweepcomment", &cl)
	comment := ""
	if len(cl.Items) > 0 {
		comment = cl.Items[0].ID
	} else {
		var c struct{ Item struct{ ID string } }
		post(base+"/comments", `{"body":"sweepcomment"}`, &c)
		comment = c.Item.ID
	}
	var sl struct{ Items []struct{ ID string } }
	get(base+"/suggestions?document_id="+doc, &sl)
	sug := ""
	if len(sl.Items) > 0 {
		sug = sl.Items[0].ID
	} else {
		var d struct {
			Item struct {
				Content  string
				Revision int
			}
		}
		get("/api/v1/docs/"+doc, &d)
		from, to := "Sweep text", "Sweep copy"
		if !strings.Contains(d.Item.Content, from) {
			from, to = to, from
		}
		var sg struct{ Item struct{ ID string } }
		post(base+"/suggestions", `{"document_id":`+jsonString(doc)+`,"base_revision":`+strconv.Itoa(d.Item.Revision)+`,"changes":[{"quote":`+jsonString(from)+`,"text":`+jsonString(to)+`}]}`, &sg)
		sug = sg.Item.ID
	}
	var shl struct {
		Items []struct{ ID, Label, Status string }
	}
	get(base+"/shares", &shl)
	shr := ""
	for _, it := range shl.Items {
		if it.Label == "sweep" && it.Status == "active" {
			shr = it.ID
		}
	}
	if shr == "" {
		var sh struct{ Item struct{ ID string } }
		post(base+"/shares", `{"label":"sweep","scope":"comment"}`, &sh)
		shr = sh.Item.ID
	}
	sweepChildren["comment_id"], sweepChildren["suggestion_id"], sweepChildren["share_id"] = comment, sug, shr
	sweepChildren["document_id"] = doc // the site settings ops name a document of the project
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
