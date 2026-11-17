package server

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/ops"
)

// Search by meaning on its screens (F-170, F-205): the owner's page (a preview before anything is sent, a typed host for an
// endpoint that is not local, a typed title to delete), the By meaning mode for owners and editors only, the inspector's list for
// readers, and a page that never says text was sent when it was not. A stand-in embedding endpoint on loopback; no real model.

type semUI struct {
	s                                      *Server
	admin, owner, editor, reader, stranger caller
	pid, slug, provider                    string
	forge, harbour                         string
	asked                                  *atomic.Int64
}

func newSemUI(t *testing.T) semUI {
	t.Helper()
	s := newAuthzServer(t)
	asked := &atomic.Int64{}
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input []string }
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/embed" || json.Unmarshal(b, &in) != nil {
			http.NotFound(w, r)
			return
		}
		asked.Add(1)
		out := make([][]float64, len(in.Input))
		for i, text := range in.Input {
			v := make([]float64, 32)
			for _, word := range strings.Fields(strings.ToLower(text)) {
				h := fnv.New32a()
				h.Write([]byte(word))
				v[h.Sum32()%32]++
			}
			var n float64
			for _, x := range v {
				n += x * x
			}
			if n == 0 {
				v[0], n = 1, 1
			}
			for j := range v {
				v[j] /= math.Sqrt(n)
			}
			out[i] = v
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}))
	t.Cleanup(endpoint.Close)
	u := semUI{s: s, admin: s.newCaller(t, "admin", ops.RoleAdmin), owner: s.newCaller(t, "owner", ops.RoleWriter), editor: s.newCaller(t, "editor", ops.RoleWriter),
		reader: s.newCaller(t, "reader", ops.RoleWriter), stranger: s.newCaller(t, "stranger", ops.RoleWriter), asked: asked}
	u.provider = send(t, s, u.admin, "POST", "/api/v1/ai/providers", `{"name":"Local embeddings","kind":"ollama","scope":"instance","base_url":"`+endpoint.URL+`","local":true,"default_model":"embed-a"}`, 201)["item"].(map[string]any)["id"].(string)
	pj := send(t, s, u.owner, "POST", "/api/v1/projects", `{"title":"The Salt Road"}`, 201)["item"].(map[string]any)
	u.pid, u.slug = pj["id"].(string), pj["slug"].(string)
	s.addMember(t, u.pid, u.editor.userID, "editor")
	s.addMember(t, u.pid, u.reader.userID, "reader")
	send(t, s, u.owner, "PUT", "/api/v1/projects/"+u.pid+"/ai/policy", `{"mode":"ask"}`, 200)
	// in reading order, with a scene between the two that read alike: the neighbours of a scene are left out of its list
	for _, d := range [][2]string{{"The Forge", "forge anvil hammer iron sparks forge anvil hammer"}, {"The Mill", "grain flour stones wheel water mill"}, {"The Harbour", "harbour boats gulls tide rope harbour boats gulls"},
		{"The Orchard", "apples branches ladder basket orchard grass"}, {"The Quay", "harbour boats gulls tide quay rope harbour boats"}} {
		id := send(t, s, u.owner, "POST", "/api/v1/projects/"+u.pid+"/docs", fmt.Sprintf(`{"title":%q,"kind":"scene","content":%q}`, d[0], d[1]), 201)["item"].(map[string]any)["id"].(string)
		switch d[0] {
		case "The Forge":
			u.forge = id
		case "The Harbour":
			u.harbour = id
		}
	}
	return u
}

func (u semUI) form(t *testing.T, c caller, action string, v url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return u.s.postForm(t, c, "/ui/projects/"+u.pid+"/semantic/"+action, v)
}

func (u semUI) enable(t *testing.T) {
	t.Helper()
	pv := u.form(t, u.owner, "preview", url.Values{"provider_id": {u.provider}, "model": {"embed-a"}})
	tok := regexp.MustCompile(`name="consent_token" value="([^"]+)"`).FindStringSubmatch(pv.Body.String())
	if tok == nil {
		t.Fatalf("no consent token: %d %s", pv.Code, alertText(pv.Body.String()))
	}
	rec := u.form(t, u.owner, "enable", url.Values{"provider_id": {u.provider}, "model": {"embed-a"}, "consent_token": {tok[1]}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("enable: %d %s", rec.Code, alertText(rec.Body.String()))
	}
	if _, err := u.s.DB.Writer().Exec(`UPDATE documents SET updated_at = '2020-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	svc, ok := semantic.ServiceOf(u.s.Reg.Env())
	if !ok {
		t.Fatal("no service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	svc.Settle(ctx)
}

func TestSemanticSettingsPageIsTheOwnersAndSendsNothingBeforeConfirmation(t *testing.T) {
	u := newSemUI(t)
	page := "/p/" + u.slug + "/semantic"
	for _, c := range []caller{u.editor, u.reader} {
		if rec := u.s.do(c, "GET", page, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d", c.name, rec.Code)
		}
		if rec := u.form(t, c, "preview", url.Values{"provider_id": {u.provider}, "model": {"embed-a"}}); rec.Code != http.StatusForbidden {
			t.Errorf("a preview as %s: %d", c.name, rec.Code)
		}
	}
	if rec := u.s.do(u.stranger, "GET", page, ""); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger: %d", rec.Code)
	}
	b := u.s.do(u.owner, "GET", page, "").Body.String()
	for _, want := range []string{"Search by meaning is off for this project.", "Nothing has been sent anywhere.", "Local embeddings", "See what would be sent", "The next step sends nothing"} {
		if !strings.Contains(b, want) {
			t.Errorf("the off page lacks %q", want)
		}
	}
	if strings.Contains(b, "/semantic/enable") {
		t.Error("the page offers to switch it on before any preview")
	}
	// the preview: what, where, what is never sent, and a button; and nothing went to the endpoint
	pv := u.form(t, u.owner, "preview", url.Values{"provider_id": {u.provider}, "model": {"embed-a"}})
	pb := pv.Body.String()
	for _, want := range []string{"What would be sent", "Pieces of text", "Private notes, ghosted text", "On this machine or its network", "None: the model is on this machine", `action="/ui/projects/` + u.pid + `/semantic/enable"`, "consent_token"} {
		if !strings.Contains(pb, want) {
			t.Errorf("the preview lacks %q", want)
		}
	}
	if strings.Contains(pb, "name=\"confirm_remote\"") {
		t.Error("a local endpoint asks for a typed host")
	}
	if u.asked.Load() != 0 {
		t.Errorf("the preview sent %d requests to the endpoint", u.asked.Load())
	}
	// a wrong token is refused and the preview is drawn again with a fresh one
	rec := u.form(t, u.owner, "enable", url.Values{"provider_id": {u.provider}, "model": {"embed-a"}, "consent_token": {"not-a-token"}})
	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusConflict && rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "consent_token") {
		t.Fatalf("a wrong token: %d %s", rec.Code, alertText(rec.Body.String()))
	}
	if st := send(t, u.s, u.owner, "GET", "/api/v1/projects/"+u.pid+"/search/semantic/status", "", 200); st["state"] != "off" {
		t.Errorf("a wrong token switched it on: %v", st)
	}
	u.enable(t)
	b = u.s.do(u.owner, "GET", page, "").Body.String()
	for _, want := range []string{"Up to date", "Local embeddings", "embed-a", "Pause", "Rebuild", "Delete the index", "each question you ask by meaning is sent to it too"} {
		if !strings.Contains(b, want) {
			t.Errorf("the running page lacks %q", want)
		}
	}
	if u.asked.Load() == 0 {
		t.Error("indexing sent nothing")
	}
}

func TestSemanticSwitchesAndTypedDelete(t *testing.T) {
	u := newSemUI(t)
	u.enable(t)
	page := "/p/" + u.slug + "/semantic"
	if rec := u.form(t, u.owner, "disable", url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("pause: %d", rec.Code)
	}
	b := u.s.do(u.owner, "GET", page+"?saved=disabled", "").Body.String()
	if !strings.Contains(b, "Paused. Nothing more is sent.") || !strings.Contains(b, "Switch it on again") || !strings.Contains(b, "Delete the index") {
		t.Errorf("the paused page:\n%s", alertText(b))
	}
	// delete needs the title typed
	rec := u.form(t, u.owner, "purge", url.Values{"confirm_title": {"the wrong title"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Nothing was deleted") {
		t.Fatalf("a wrong title: %d", rec.Code)
	}
	if st := send(t, u.s, u.owner, "GET", "/api/v1/projects/"+u.pid+"/search/semantic/status", "", 200); st["chunks"].(float64) == 0 {
		t.Fatal("a wrong title deleted the index")
	}
	if rec := u.form(t, u.owner, "purge", url.Values{"confirm_title": {"The Salt Road"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("purge: %d %s", rec.Code, alertText(rec.Body.String()))
	}
	if st := send(t, u.s, u.owner, "GET", "/api/v1/projects/"+u.pid+"/search/semantic/status", "", 200); st["chunks"].(float64) != 0 || st["state"] != "off" {
		t.Errorf("after purge: %v", st)
	}
	if rec := u.s.postForm(t, u.owner, "/ui/projects/"+u.pid+"/semantic/rebuild", url.Values{"_csrf": {"-"}}); rec.Code != http.StatusForbidden {
		t.Errorf("no token: %d", rec.Code)
	}
}

func TestByMeaningModeIsOfferedToOwnersAndEditorsOnly(t *testing.T) {
	u := newSemUI(t)
	search := "/p/" + u.slug + "/search"
	// off: nothing about it, for anyone
	for _, c := range []caller{u.owner, u.editor, u.reader} {
		if b := u.s.do(c, "GET", search, "").Body.String(); strings.Contains(b, "By meaning") || strings.Contains(b, "sem-modes") {
			t.Errorf("%s is told about a mode that is off", c.name)
		}
	}
	u.enable(t)
	for _, c := range []caller{u.owner, u.editor} {
		if b := u.s.do(c, "GET", search, "").Body.String(); !strings.Contains(b, "By meaning") || !strings.Contains(b, "By words") {
			t.Errorf("%s is not offered the mode", c.name)
		}
	}
	if b := u.s.do(u.reader, "GET", search, "").Body.String(); strings.Contains(b, "By meaning") {
		t.Error("a reader is told about it")
	}
	if rec := u.s.do(u.reader, "GET", search+"?mode=meaning&q=forge", ""); strings.Contains(rec.Body.String(), "sem-hit") || strings.Contains(rec.Body.String(), "sent to") {
		t.Error("a reader ran a search by meaning")
	}
	before := u.asked.Load()
	b := u.s.do(u.editor, "GET", search+"?mode=meaning", "").Body.String()
	if !strings.Contains(b, "Nothing is sent until you search.") || u.asked.Load() != before {
		t.Error("the empty mode page says or does something it should not")
	}
	// An address never sends a question: a link, a prefetch or another page's redirect cannot make the server call the endpoint.
	b = u.s.do(u.editor, "GET", search+"?mode=meaning&q=hammer+anvil+forge", "").Body.String()
	if u.asked.Load() != before || strings.Contains(b, "sem-hit") || strings.Contains(b, "Your question is sent") {
		t.Errorf("a GET with a question sent it (%d requests)", u.asked.Load()-before)
	}
	form := "/ui/projects/" + u.pid + "/search/meaning"
	if rec := u.s.post(t, u.editor, form, url.Values{"q": {"hammer anvil forge"}}, "-", false); rec.Code != http.StatusForbidden || u.asked.Load() != before {
		t.Errorf("a post with no token: %d, and %d requests", rec.Code, u.asked.Load()-before)
	}
	rec := u.s.postForm(t, u.editor, form, url.Values{"q": {"hammer anvil forge"}})
	b = rec.Body.String()
	if rec.Code != 200 || !strings.Contains(b, "The Forge") || !strings.Contains(b, "Your question is sent to Local embeddings, which is on this machine or its network.") {
		t.Fatalf("by meaning: %d\n%s", rec.Code, regexp.MustCompile(`(?s)<main.*</main>`).FindString(b))
	}
	if !regexp.MustCompile(`href="/p/` + u.slug + `/d/` + u.forge + `\?find=[^"]+"`).MatchString(b) {
		t.Error("the hit does not open the document at its passage")
	}
	if u.asked.Load() == before {
		t.Error("the question never reached the endpoint")
	}
	// a reader's post is refused whole, and sends nothing
	before = u.asked.Load()
	if rec := u.s.postForm(t, u.reader, form, url.Values{"q": {"forge"}}); rec.Code != http.StatusForbidden && rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "sem-hit") || u.asked.Load() != before {
		t.Errorf("a reader's search by meaning: %d, %d requests", rec.Code, u.asked.Load()-before)
	}
	// paused: said in words, and the question is not sent
	u.form(t, u.owner, "disable", url.Values{})
	before = u.asked.Load()
	b = u.s.postForm(t, u.editor, form, url.Values{"q": {"hammer"}}).Body.String()
	if strings.Contains(b, "sem-hit") || u.asked.Load() != before {
		t.Error("a paused index answered, or sent the question")
	}
}

func TestScenesLikeThisOneForAReaderAndTheWritingPage(t *testing.T) {
	u := newSemUI(t)
	doc := "/p/" + u.slug + "/d/" + u.harbour
	if b := u.s.do(u.owner, "GET", doc, "").Body.String(); strings.Contains(b, "Scenes like this one") {
		t.Error("the inspector offers it with no index")
	}
	u.enable(t)
	for _, c := range []caller{u.owner, u.reader} {
		b := u.s.do(c, "GET", doc, "").Body.String()
		if !strings.Contains(b, "Scenes like this one") || !strings.Contains(b, `hx-get="/ui/docs/`+u.harbour+`/similar"`) {
			t.Errorf("%s: the inspector does not offer it", c.name)
		}
	}
	before := u.asked.Load()
	rec := u.s.do(u.reader, "GET", "/ui/docs/"+u.harbour+"/similar", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "The Quay") {
		t.Fatalf("the list: %d %s", rec.Code, rec.Body)
	}
	if u.asked.Load() != before {
		t.Error("scenes like this one sent something to the endpoint")
	}
	if rec := u.s.do(u.stranger, "GET", "/ui/docs/"+u.harbour+"/similar", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger: %d", rec.Code)
	}
	// a passage to scroll to loads the small script, and only then
	if b := u.s.do(u.owner, "GET", doc+"?find=harbour+boats", "").Body.String(); !strings.Contains(b, "js/jump") {
		t.Error("find does not load the script")
	}
	if b := u.s.do(u.owner, "GET", doc, "").Body.String(); strings.Contains(b, "js/jump") {
		t.Error("the script is loaded with no passage")
	}
}

// An endpoint that is not declared local sends the whole manuscript off the network: the preview says so in its own box and
// switching it on needs its host typed; without it nothing is switched on and nothing is sent.
func TestRemoteEndpointNeedsTheHostTyped(t *testing.T) {
	u := newSemUI(t)
	rec := u.s.do(u.admin, "POST", "/api/v1/ai/providers", `{"name":"Hosted embeddings","kind":"openai_compatible","scope":"instance","base_url":"https://embed.example.test/v1","default_model":"embed-z"}`)
	if rec.Code != 201 {
		t.Skipf("a remote provider could not be made here: %d %s", rec.Code, rec.Body)
	}
	var pr struct{ Item struct{ ID string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &pr)
	pv := u.form(t, u.owner, "preview", url.Values{"provider_id": {pr.Item.ID}, "model": {"embed-z"}})
	b := pv.Body.String()
	if !strings.Contains(b, "This leaves your network.") || !strings.Contains(b, `name="confirm_remote"`) || !strings.Contains(b, "embed.example.test") {
		t.Fatalf("the remote preview: %d\n%s", pv.Code, regexp.MustCompile(`(?s)<main.*</main>`).FindString(b))
	}
	// the button waits for the host to be typed (the shell's typed-confirmation script), and the warning does not say the same thing three times
	if !strings.Contains(b, `data-confirm="embed.example.test"`) || !strings.Contains(b, "data-confirm-submit") {
		t.Error("the remote switch-on is not held until the host is typed")
	}
	if n := strings.Count(strings.NewReplacer("\u2068", "", "\u2069", "").Replace(b), "Not on this machine or its network"); n > 0 {
		t.Errorf("the remote warning still repeats the local/remote sentence %d times", n)
	}
	tok := regexp.MustCompile(`name="consent_token" value="([^"]+)"`).FindStringSubmatch(b)
	if tok == nil {
		t.Fatalf("no token: %s", alertText(b))
	}
	rec = u.form(t, u.owner, "enable", url.Values{"provider_id": {pr.Item.ID}, "model": {"embed-z"}, "consent_token": {tok[1]}, "confirm_remote": {"somewhere.else"}})
	if rec.Code == http.StatusSeeOther {
		t.Fatal("a wrong host switched it on")
	}
	if !strings.Contains(rec.Body.String(), `name="confirm_remote"`) {
		t.Error("the preview is not drawn again with the host box")
	}
	if st := send(t, u.s, u.owner, "GET", "/api/v1/projects/"+u.pid+"/search/semantic/status", "", 200); st["state"] != "off" {
		t.Errorf("a wrong host: %v", st)
	}
	if u.asked.Load() != 0 {
		t.Error("something was sent")
	}
}
