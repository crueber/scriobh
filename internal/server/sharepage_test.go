package server

import (
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

const htmlAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

var reTokenShape = regexp.MustCompile(`shl_[0-9A-Z]{26}\.[A-Za-z0-9_-]{43}`)

func lockedDown(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	if h.Get("Set-Cookie") != "" {
		t.Errorf("%s sets a cookie: %s", what, h.Get("Set-Cookie"))
	}
	if h.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(h.Get("X-Robots-Tag"), "noindex") || !strings.Contains(h.Get("Cache-Control"), "no-store") {
		t.Errorf("%s headers: %v", what, h)
	}
	if !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || strings.Contains(h.Get("Content-Security-Policy"), "unsafe") {
		t.Errorf("%s CSP: %q", what, h.Get("Content-Security-Policy"))
	}
}

// A person with a link and no account sees one page that holds nothing of the book and no way into the application,
// and, for a link that opens nothing, one page that never says why. The address still answers JSON to a caller that
// does not ask for a page, exactly as before.
func TestAShareLinkOpensAPageThatHoldsNothingAndLeadsNowhere(t *testing.T) {
	w := newShareWorld(t, nil)
	_, open := w.link(t, `{"label":"SECRETLABEL","scope":"comment"}`)
	_, locked := w.link(t, `{"passphrase":"correct horse battery"}`)
	var shells []string
	for _, tok := range []string{open, locked} {
		rec := w.pub("GET", "/s/"+tok, "", "Accept", htmlAccept)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("the page: %d %v", rec.Code, rec.Header())
		}
		lockedDown(t, "the page", rec)
		body := rec.Body.String()
		shells = append(shells, body)
		noLeak(t, "the page", body)
		for _, bad := range []string{"Shared Book", "Chapter One", "Chapter Two", "SECRETLABEL", "quiet house", tok, "/settings", "Sign out", "csrf", `href="/"`, `href="/p/`, "palette", "manifest.webmanifest", "<form method=\"post\""} {
			if strings.Contains(body, bad) {
				t.Errorf("the page holds %q", bad)
			}
		}
		for _, tg := range tags(body) {
			if tg.name == "script" && !strings.HasPrefix(tg.attrs["src"], "/static/") {
				t.Errorf("a script from %v", tg.attrs)
			}
			if tg.name == "a" && tg.attrs["href"] != "" && !strings.HasPrefix(tg.attrs["href"], "#") {
				t.Errorf("a link out of the page: %v", tg.attrs)
			}
		}
		lint(t, "share page", body)
		if !strings.Contains(body, `<html lang="en">`) || !strings.Contains(body, `name="referrer" content="no-referrer"`) || !strings.Contains(body, "noindex") {
			t.Error("the page lacks its language, referrer or robots line")
		}
		// the page is not a view and not locked: it is the same for a link with a passphrase
		if rec := w.pub("GET", "/s/"+tok+"/api/manifest", ""); tok == locked && rec.Code != 401 {
			t.Errorf("the manifest of a passphrase link: %d", rec.Code)
		}
	}
	if shells[0] != shells[1] {
		t.Error("the page differs between a link with a passphrase and one without: it would tell a stranger which is which")
	}
	// the same address, asked for JSON, is the manifest as before
	if rec := w.pub("GET", "/s/"+open, ""); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || !strings.Contains(rec.Body.String(), "Shared Book") {
		t.Errorf("JSON callers changed: %d %v", rec.Code, rec.Header())
	}
	// nothing else navigates to a page: a wrong path, a method that is not GET and the API are not pages
	for _, p := range []string{"/s/" + open + "/settings", "/s/" + open + "/api/nothing", "/s/" + open + "/x/y", "/s/x/y", "/s/"} {
		rec := w.pub("GET", p, "", "Accept", htmlAccept)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), "This link is not available") {
			t.Errorf("%s: %d %.80s", p, rec.Code, rec.Body)
		}
		lockedDown(t, p, rec)
	}
	if rec := w.pub("POST", "/s/"+open, "", "Accept", htmlAccept); rec.Code != 404 {
		t.Errorf("POST to the page: %d", rec.Code)
	}
	// no token reaches a log line, whatever was asked of it
	if strings.Contains(w.logs.String(), strings.Split(open, ".")[1]) || strings.Contains(w.logs.String(), strings.Split(locked, ".")[1]) {
		t.Error("a token is in the log")
	}
}

func TestEveryLinkThatOpensNothingGetsTheSamePage(t *testing.T) {
	w := newShareWorld(t, nil)
	id, tok := w.link(t, `{}`)
	revID, revTok := w.link(t, `{}`)
	if rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/shares/"+revID+"/revoke", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	secret := tok[strings.Index(tok, ".")+1:]
	cases := map[string]string{"garbage": "garbage", "wrong secret": id + "." + strings.Repeat("A", len(secret)), "unknown id": "shl_00000000000000000000000000." + secret, "revoked": revTok, "unicode": "shl_é", "long": strings.Repeat("a", 3000)}
	var ref string
	for name, tk := range cases {
		for _, p := range []string{"", "/api/manifest", "/api/docs/" + w.doc1} {
			rec := w.pub("GET", "/s/"+tk+p, "", "Accept", htmlAccept)
			if rec.Code != 404 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
				t.Errorf("%s%s: %d %v", name, p, rec.Code, rec.Header())
			}
			if ref == "" {
				ref = rec.Body.String()
			}
			if rec.Body.String() != ref {
				t.Errorf("%s%s answers differently:\n%.200s", name, p, rec.Body)
			}
			lockedDown(t, name, rec)
		}
	}
	for _, bad := range []string{"revoked", "expired", "passphrase", "Shared Book", "exist"} {
		if strings.Contains(ref, bad) {
			t.Errorf("the page explains itself with %q", bad)
		}
	}
	lint(t, "gone page", ref)
}

func TestShareLinksPageIsTheOwnersAndShowsALinkOnce(t *testing.T) {
	w := newShareWorld(t, nil)
	page := "/p/shared-book/share"
	reader := w.s.newCaller(t, "reader", ops.RoleReader)
	w.s.addMember(t, w.pid, reader.userID, ops.ProjectReader)
	for _, c := range []struct {
		who  caller
		want int
	}{{w.owner, 200}, {w.editor, 403}, {w.reviewer, 403}, {reader, 403}, {w.stranger, 404}, {caller{name: "anonymous"}, 303}} {
		if rec := w.s.do(c.who, "GET", page, ""); rec.Code != c.want {
			t.Errorf("%s opens the share links page: %d, want %d", c.who.name, rec.Code, c.want)
		}
	}
	base := "/ui/projects/" + w.pid + "/share/"
	if rec := w.s.postForm(t, w.editor, base+"create", url.Values{"scope": {"read"}}); rec.Code != 403 {
		t.Errorf("an editor makes a link: %d", rec.Code)
	}
	if rec := w.s.postForm(t, w.stranger, base+"create", url.Values{"scope": {"read"}}); rec.Code != 404 {
		t.Errorf("a stranger makes a link: %d", rec.Code)
	}
	if rec := w.s.postForm(t, w.owner, base+"create", url.Values{"scope": {"read"}, "_csrf": {"nope"}}); rec.Code != 403 {
		t.Errorf("no token: %d", rec.Code)
	}
	// a short passphrase is refused on the page, with what was typed kept and the passphrase not
	bad := w.s.postForm(t, w.owner, base+"create", url.Values{"scope": {"comment"}, "label": {"Sam"}, "passphrase": {"short"}})
	if bad.Code != 422 || !strings.Contains(bad.Body.String(), `value="Sam"`) || strings.Contains(bad.Body.String(), "short\"") {
		t.Errorf("a short passphrase: %d", bad.Code)
	}
	// the link is made: shown on the answer, with a way to copy it, and never again
	rec := w.s.postForm(t, w.owner, base+"create", url.Values{"scope": {"comment"}, "label": {"Sam"}, "contents": {"chosen"}, "doc": {w.doc2}, "expires": {"2099-01-01"}, "passphrase": {"correct horse battery"}, "download": {"on"}})
	// the answer to the POST is a redirect (a reload asks for nothing and makes no second link), and the page it
	// goes to shows the link once
	if rec.Code != 303 || reTokenShape.MatchString(rec.Body.String()) || rec.Header().Get("Location") != page {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec = w.s.do(w.owner, "GET", page, "")
	tok := reTokenShape.FindString(rec.Body.String())
	var made int
	if err := w.s.DB.Read().QueryRow(`SELECT count(*) FROM share_links WHERE label = 'Sam'`).Scan(&made); err != nil || made != 1 {
		t.Errorf("%d links were made (%v)", made, err)
	}
	if tok == "" || !strings.Contains(rec.Body.String(), `data-path="/s/`) || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("the link is not shown once: %.400s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "correct horse battery") {
		t.Error("the passphrase is on the page")
	}
	for _, again := range []*httptest.ResponseRecorder{w.s.do(w.owner, "GET", page, ""), w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/shares", "")} {
		if reTokenShape.MatchString(again.Body.String()) || strings.Contains(again.Body.String(), tok) {
			t.Errorf("a token is shown again: %.300s", again.Body)
		}
	}
	list := w.s.do(w.owner, "GET", page, "").Body.String()
	for _, want := range []string{"Sam", "Read and comment", "One chosen part: Chapter Two", "Has a passphrase", "Download allowed", "wrong passphrases tried"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	// the link works as made: it shows only the chosen part, and wants its passphrase
	if rec := w.pub("GET", "/s/"+tok+"/api/manifest", ""); rec.Code != 401 {
		t.Errorf("a passphrase link without its proof: %d", rec.Code)
	}
	// change it, then revoke it: the address then answers the page that says nothing
	id := regexp.MustCompile(`id="link-(shl_[0-9A-Z]{26})"`).FindStringSubmatch(list)[1]
	if rec := w.s.postForm(t, w.owner, base+"update", url.Values{"share_id": {id}, "label": {"Sam B"}, "scope": {"read"}, "ghosts": {"omit"}, "remove_passphrase": {"on"}, "clear_expiry": {"on"}}); rec.Code != 303 {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if rec := w.pub("GET", "/s/"+tok+"/api/manifest", ""); rec.Code != 200 {
		t.Errorf("a link whose passphrase was removed: %d", rec.Code)
	}
	if rec := w.s.postForm(t, w.owner, base+"revoke", url.Values{"share_id": {id}}); rec.Code != 303 {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec := w.pub("GET", "/s/"+tok, "", "Accept", htmlAccept); rec.Code != 404 || !strings.Contains(rec.Body.String(), "This link is not available") {
		t.Errorf("a revoked link: %d", rec.Code)
	}
	if !strings.Contains(w.s.do(w.owner, "GET", page, "").Body.String(), "Revoked") {
		t.Error("the list does not say it is revoked")
	}
}
