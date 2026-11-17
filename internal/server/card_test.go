package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// newCardServer is a server for the card tests. rateLimit is SCRIOBH_SHARE_RATE_LIMIT, the per-address limit the public routes
// share with the share links (120 a minute by default): "0" switches it off, on purpose, for a test that counts requests and
// must not depend on it (a burst of 50 from one address would be limited otherwise).
func newCardServer(t *testing.T, rateLimit string) (*Server, *bytes.Buffer) {
	t.Helper()
	return newShareServer(t, map[string]string{"SCRIOBH_SHARE_RATE_LIMIT": rateLimit})
}

func cardGet(s *Server, path string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "198.51.100.9:4000"
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Cookie" {
			req.Header.Add("Cookie", hdr[i+1])
			continue
		}
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAProgressCardOverHTTP(t *testing.T) {
	s, logs := newCardServer(t, "0")
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	call := func(op string, in any) any {
		out, err := s.Reg.Call(t.Context(), p, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	pid := call("project.create", projects.CreateIn{Title: "CARDCANARY Book Title"}).(projects.Item).Item.ID
	did := call("doc.create", docs.CreateIn{ProjectID: pid, Title: "CARDCANARY Chapter", Content: "CARDCANARY manuscript words here"}).(docs.Item).Item.ID
	body := "CARDCANARY manuscript words here and many more words that are written today so that the day has some"
	doc := call("doc.get", docs.DocIn{ID: did}).(docs.Item).Item
	call("doc.update", docs.UpdateIn{ID: did, BaseRevision: doc.Revision, Content: &body})
	if rec := s.do(owner, "POST", "/api/v1/me/studio/goals", `{"period":"day","measure":"words","target":5}`); rec.Code != 201 {
		t.Fatalf("goal: %d %s", rec.Code, rec.Body)
	}
	rec := s.do(owner, "POST", "/api/v1/me/studio/cards", `{"label":"CARDCANARY note","expires_days":7}`)
	if rec.Code != 201 {
		t.Fatalf("card: %d %s", rec.Code, rec.Body)
	}
	var made struct {
		Item  struct{ ID, Token, Path string }
		Token string
		Path  string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	token := made.Item.Token
	if token == "" || made.Item.Path != "/card/"+token {
		t.Fatalf("card body: %s", rec.Body)
	}
	secret := strings.SplitN(token, ".", 2)[1]

	page := cardGet(s, "/card/"+token, "Accept", "text/html", "Cookie", owner.cookie.String())
	if page.Code != 200 {
		t.Fatalf("page: %d %.300s", page.Code, page.Body)
	}
	h := page.Header()
	if h.Get("X-Robots-Tag") != "noindex, nofollow, noarchive" || h.Get("Referrer-Policy") != "no-referrer" || h.Get("Cache-Control") != "no-store" ||
		h.Get("Content-Security-Policy") == "" || len(h.Values("Set-Cookie")) != 0 {
		t.Errorf("headers: %v", h)
	}
	txt := page.Body.String()
	for _, bad := range []string{"CARDCANARY", "<script", "http://", "https://", pid, did} {
		if strings.Contains(strings.ReplaceAll(txt, `xmlns="http://www.w3.org/2000/svg"`, ""), bad) {
			t.Errorf("the card page holds %q", bad)
		}
	}
	if !strings.Contains(txt, "<svg") || strings.Contains(txt, "Gradient") {
		t.Errorf("the card page: %.600s", txt)
	}
	svg := cardGet(s, "/card/"+token+"/card.svg")
	if svg.Code != 200 || !strings.HasPrefix(svg.Header().Get("Content-Type"), "image/svg+xml") || strings.Contains(svg.Body.String(), "CARDCANARY") || svg.Header().Get("X-Robots-Tag") == "" {
		t.Errorf("svg: %d %v", svg.Code, svg.Header())
	}
	js := cardGet(s, "/card/"+token+"/card.json")
	var view struct {
		Streak, WritingDays30 int
		GoalsActive           int `json:"goals_active"`
		Goals                 []struct{ Scope, Project string }
	}
	if js.Code != 200 || json.Unmarshal(js.Body.Bytes(), &view) != nil || view.GoalsActive != 1 || strings.Contains(js.Body.String(), "CARDCANARY") {
		t.Errorf("json: %d %s", js.Code, js.Body)
	}
	// Two page views were counted (the first, and now another); the picture and the data were not.
	cardGet(s, "/card/"+token, "Accept", "text/html")
	var list struct {
		Items []struct {
			Views     int
			ExpiresAt string `json:"expires_at"`
			Token     string
		}
	}
	rec = s.do(owner, "GET", "/api/v1/me/studio/cards", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].Views != 2 || list.Items[0].Token != "" || strings.Contains(rec.Body.String(), secret) {
		t.Errorf("list: %s", rec.Body)
	}
	// Every token that opens nothing is answered the same way: a wrong secret, a wrong shape, an unknown id, a revoked card.
	gone := map[string]string{}
	for name, path := range map[string]string{
		"wrong secret": "/card/" + made.Item.ID + "." + strings.Repeat("A", 43), "no secret": "/card/" + made.Item.ID, "unknown": "/card/crd_00000000000000000000000000." + secret,
		"shape": "/card/not-a-token", "sub path": "/card/" + token + "/other",
	} {
		r := cardGet(s, path, "Accept", "text/html")
		gone[name] = r.Body.String()
		if r.Code != 404 || len(r.Header().Values("Set-Cookie")) != 0 || r.Header().Get("X-Robots-Tag") == "" {
			t.Errorf("%s: %d %v", name, r.Code, r.Header())
		}
	}
	if rec := s.do(owner, "POST", "/api/v1/me/studio/cards/"+made.Item.ID+"/revoke", ""); rec.Code != 200 {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{"/card/" + token, "/card/" + token + "/card.svg", "/card/" + token + "/card.json"} {
		r := cardGet(s, path, "Accept", "text/html")
		if r.Code != 404 {
			t.Errorf("%s after revoking: %d", path, r.Code)
		}
		gone["revoked "+path] = r.Body.String()
	}
	var first string
	for name, b := range gone {
		if strings.HasSuffix(name, "card.svg") || strings.HasSuffix(name, "card.json") {
			continue // a script that did not ask for a page gets the JSON error
		}
		if first == "" {
			first = b
		}
		if b != first {
			t.Errorf("the answer for %q differs from the others", name)
		}
	}
	// The token is in no log line; the request log has the route and never the path.
	if strings.Contains(logs.String(), secret) || regexp.MustCompile(`/card/crd_`).MatchString(logs.String()) {
		t.Error("a card's token is in the log")
	}
	// The card is a link, not a person: the owner's own cookie opens nothing more, and no API route answers under /card/.
	if r := cardGet(s, "/card/"+made.Item.ID, "Cookie", owner.cookie.String()); r.Code != 404 {
		t.Errorf("a cookie opened a card: %d", r.Code)
	}
}

func TestACardOnlyRateLimitIsPerAddress(t *testing.T) {
	s, _ := newCardServer(t, "3")
	codes := map[int]int{}
	for i := 0; i < 40; i++ { // the bucket starts with a burst of ten
		codes[cardGet(s, "/card/crd_00000000000000000000000000."+strings.Repeat("A", 43)).Code]++
	}
	if codes[429] == 0 || codes[404] == 0 {
		t.Errorf("answers %v", codes)
	}
}

// A public page view writes nothing and a warm card never outlives its link: fifty page requests leave the database as it was
// (the views are counted in memory and shown in the owner's list), and a revoked link is the one 404 on the very next request.
func TestACardPageNeverWritesAndARevokedLinkStopsAtOnce(t *testing.T) {
	s, _ := newCardServer(t, "0")
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	rec := s.do(owner, "POST", "/api/v1/me/studio/cards", `{}`)
	var made struct{ Item struct{ ID, Token string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	for i := 0; i < 50; i++ {
		if r := cardGet(s, "/card/"+made.Item.Token, "Accept", "text/html"); r.Code != 200 {
			t.Fatalf("view %d: %d", i, r.Code)
		}
	}
	var views int
	var last string
	_ = s.DB.Read().QueryRow(`SELECT views, last_viewed_at FROM studio_cards WHERE id = ?`, made.Item.ID).Scan(&views, &last)
	if views != 0 || last != "" {
		t.Errorf("50 page requests wrote to the database: views %d, last viewed %q", views, last)
	}
	if n := s.cards.Computed(); n != 1 {
		t.Errorf("50 requests worked the card out %d times", n)
	}
	var list struct{ Items []struct{ Views int } }
	_ = json.Unmarshal(s.do(owner, "GET", "/api/v1/me/studio/cards", "").Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].Views != 50 {
		t.Errorf("the owner's list: %+v", list)
	}
	if err := s.cards.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	_ = s.DB.Read().QueryRow(`SELECT views FROM studio_cards WHERE id = ?`, made.Item.ID).Scan(&views)
	if views != 50 {
		t.Errorf("after the flush: %d", views)
	}
	// Warm, then revoked: the next request, page, picture or data, is the 404.
	if r := s.do(owner, "POST", "/api/v1/me/studio/cards/"+made.Item.ID+"/revoke", ""); r.Code != 200 {
		t.Fatal(r.Code)
	}
	for _, path := range []string{"/card/" + made.Item.Token, "/card/" + made.Item.Token + "/card.svg", "/card/" + made.Item.Token + "/card.json"} {
		if r := cardGet(s, path, "Accept", "text/html"); r.Code != 404 {
			t.Errorf("%s after revoking, warm: %d", path, r.Code)
		}
	}
}
