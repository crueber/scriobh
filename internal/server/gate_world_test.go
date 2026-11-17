package server

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

// gateWorld is a protected project with a canary in every place the project keeps something a person wrote, and the
// people around it: the owner (one session that has unlocked it and one that has not), an editor who is a member and
// has not unlocked it, an administrator who is not a member, and a stranger.
type gateWorld struct {
	s                                      *Server
	owner, owner2, editor, admin, outsider caller
	pid, slug, docID, compID, entID        string
	pass                                   string
	needles                                []string
	wordCount                              *regexp.Regexp
}

const gateWords = 1313 // about; the real counts are read back

var gateCanaries = []string{
	"Zqtitle", "Zqbody", "Zqcomment", "Zqreply", "Zqlabel", "Zqstash", "Zqnote", "Zqtag", "zqfile", "Zqoverride", "Zqshare",
	"Zqchapter", "Zqprivate", "Zqsubtitle", "Zqdescription",
}

// sessionFor opens another browser session for a person who already exists.
func (s *Server) sessionFor(t *testing.T, name, userID string) caller {
	t.Helper()
	tok, _, err := s.Sess.Create(context.Background(), userID, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return caller{name: name, userID: userID, cookie: &http.Cookie{Name: auth.SessionCookie, Value: tok}}
}

func (w *gateWorld) post(t *testing.T, c caller, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return w.s.do(c, "POST", path, body)
}

func (w *gateWorld) must(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code >= 300 {
		t.Fatalf("%s: %d %s", what, rec.Code, rec.Body)
	}
}

func gateID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var v struct{ Item struct{ ID string } }
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.Item.ID == "" {
		t.Fatalf("no id in %d %s", rec.Code, rec.Body)
	}
	return v.Item.ID
}

// newGateWorld seeds the project while it has no passphrase, then protects it.
func newGateWorld(t *testing.T, s *Server) *gateWorld {
	t.Helper()
	ctx := context.Background()
	w := &gateWorld{s: s, pass: "correct horse battery", wordCount: regexp.MustCompile(`$^`)}
	w.owner = s.newCaller(t, "owner", ops.RoleWriter)
	w.editor = s.newCaller(t, "editor", ops.RoleWriter)
	w.admin = s.newCaller(t, "admin", ops.RoleAdmin)
	w.outsider = s.newCaller(t, "outsider", ops.RoleWriter)
	w.owner2 = s.sessionFor(t, "owner2", w.owner.userID)
	ownerP := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "cli"} // the operator's shell: seeding is not what is under test

	rec := w.post(t, w.owner, "/api/v1/projects", `{"title":"Gate Book"}`)
	w.must(t, rec, "project")
	w.pid = gateID(t, rec)
	var pj struct{ Item struct{ Slug string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &pj)
	w.slug = pj.Item.Slug
	s.addMember(t, w.pid, w.editor.userID, ops.ProjectEditor)
	base := "/api/v1/projects/" + w.pid

	w.must(t, s.do(w.owner, "PATCH", base, `{"subtitle":"Zqsubtitle","description":"Zqdescription"}`), "project update")
	// the manuscript: a chapter with a distinctive title, words and a count
	body := "Zqbody Heroine walks home. " + strings.Repeat("word ", gateWords-5)
	rec = w.post(t, w.owner, base+"/docs", `{"title":"Zqtitle","kind":"chapter","content":`+jsonString(body)+`}`)
	w.must(t, rec, "doc")
	w.docID = gateID(t, rec)
	w.must(t, w.post(t, w.owner, base+"/docs", `{"title":"Zqchapter two","kind":"chapter","content":"Zqprivate words."}`), "doc 2")
	// a bible, an entity the text mentions, a book override and a timeline event told by the chapter
	comp, err := s.Reg.Call(ctx, ownerP, "compendium.create", compendiums.CreateIn{Name: "Gate Bible", ProjectID: w.pid})
	if err != nil {
		t.Fatal(err)
	}
	w.compID = comp.(compendiums.Item).Item.ID
	ent, err := s.Reg.Call(ctx, ownerP, "entity.create", entities.CreateIn{ID: w.compID, Kind: "character", Name: "Heroine"})
	if err != nil {
		t.Fatal(err)
	}
	w.entID = ent.(entities.Item).Item.ID
	ov := "Zqoverride"
	if _, err := s.Reg.Call(ctx, ownerP, "project.entity.override.set", entities.OverrideSetIn{ID: w.pid, EntityID: w.entID, Summary: &ov}); err != nil {
		t.Fatal(err)
	}
	// touch the chapter again so the index sees the entity
	w.must(t, s.do(w.owner, "PATCH", "/api/v1/docs/"+w.docID, `{"content":`+jsonString(body+" Heroine again.")+`,"base_revision":1}`), "doc update")
	evOut, err := s.Reg.Call(ctx, ownerP, "timeline.event.create", timeline.CreateIn{ID: w.compID, Title: "The crossing", StoryDate: "1990"})
	if err != nil {
		t.Fatal(err)
	}
	evID := evOut.(timeline.EventItem).Item.ID
	if _, err := s.Reg.Call(ctx, ownerP, "timeline.scene.link", timeline.LinkIn{ID: w.docID, EventID: evID}); err != nil {
		t.Fatal(err)
	}
	// review, history, stash, research, tags, a file, a share link
	rec = w.post(t, w.editor, base+"/comments", `{"body":"Zqcomment on the chapter","document_id":`+jsonString(w.docID)+`}`)
	w.must(t, rec, "comment")
	cid := gateID(t, rec)
	w.must(t, w.post(t, w.owner, base+"/comments", `{"body":"Zqreply to you","parent_id":`+jsonString(cid)+`}`), "reply (a notification for the editor)")
	w.must(t, w.post(t, w.owner, "/api/v1/docs/"+w.docID+"/revisions", `{"label":"Zqlabel"}`), "snapshot")
	w.must(t, w.post(t, w.owner, base+"/stash", `{"title":"Zqstash","content":"Zqstash words"}`), "stash")
	w.must(t, w.post(t, w.owner, base+"/research", `{"title":"Zqnote","body":"Zqnote body","tags":["Zqtag"]}`), "research")
	img := image.NewRGBA(image.Rect(0, 0, 6, 6))
	for x := 0; x < 6; x++ {
		img.Set(x, x, color.RGBA{200, 10, 10, 255})
	}
	var pb bytes.Buffer
	if err := png.Encode(&pb, img); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", base+"/attachments?filename=zqfile.png", bytes.NewReader(pb.Bytes()))
	req.AddCookie(w.owner.cookie)
	arec := httptest.NewRecorder()
	s.Handler().ServeHTTP(arec, req)
	w.must(t, arec, "attachment")
	w.must(t, w.post(t, w.owner, base+"/shares", `{"label":"Zqshare","scope":"read"}`), "share")

	// now protect it, as its owner; that session stays unlocked, the others do not
	rec = s.do(w.owner, "PUT", base+"/passphrase", `{"passphrase":`+jsonString(w.pass)+`}`)
	w.must(t, rec, "set passphrase")
	w.needles = gateCanaries
	// the counts a locked project must not show: the chapter's words and the project's, read from the project
	var pr struct {
		Item struct {
			WordCount     int `json:"word_count"`
			DocumentCount int `json:"document_count"`
		}
	}
	var dr struct {
		Item struct {
			WordCount int `json:"word_count"`
		}
	}
	_ = json.Unmarshal(s.do(w.owner, "GET", base, "").Body.Bytes(), &pr)
	_ = json.Unmarshal(s.do(w.owner, "GET", "/api/v1/docs/"+w.docID, "").Body.Bytes(), &dr)
	if pr.Item.WordCount < 1000 || dr.Item.WordCount < 1000 {
		t.Fatalf("the seeded text should have over a thousand words: project %d, chapter %d", pr.Item.WordCount, dr.Item.WordCount)
	}
	w.wordCount = regexp.MustCompile(`\b(` + strconv.Itoa(pr.Item.WordCount) + `|` + strconv.Itoa(dr.Item.WordCount) + `)\b`)
	return w
}

// leaks lists the canaries (and the word count) found in a response body.
func (w *gateWorld) leaks(body string) []string {
	var out []string
	for _, n := range w.needles {
		if strings.Contains(body, n) || strings.Contains(strings.ToLower(body), strings.ToLower(n)) {
			out = append(out, n)
		}
	}
	if w.wordCount.MatchString(body) {
		out = append(out, "a word count")
	}
	return out
}
