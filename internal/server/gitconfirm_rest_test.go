package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Every confirmation that guards a change letting more reach a public place is the op's, not the screen's: the same change
// over REST, with a session, is refused without the confirmation field, refused with the wrong one, and made with the right
// one. Changes that only narrow, and changes that never widen, need none.

type restRemote struct {
	Item struct {
		ID, Name, Audience, URL string
		OnlyDone                bool `json:"only_done"`
		IncludeNotes            bool `json:"include_notes"`
	}
}

func restCreate(t *testing.T, e writeEnv, body string) restRemote {
	t.Helper()
	rec := e.s.do(e.owner, "POST", "/api/v1/projects/"+e.pid+"/git/remotes", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", body, rec.Code, rec.Body)
	}
	var r restRemote
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	return r
}

func gitCode(t *testing.T, body []byte) string {
	t.Helper()
	var e struct {
		Error struct {
			Data map[string]any `json:"data"`
		}
	}
	_ = json.Unmarshal(body, &e)
	c, _ := e.Error.Data["git_code"].(string)
	return c
}

func TestWideningARemoteOverRESTNeedsItsNameTypedInTheOp(t *testing.T) {
	e := newWriteEnv(t)
	patch := func(body string) (int, string) {
		rec := e.s.do(e.owner, "PATCH", "/api/v1/projects/"+e.pid+"/git/remote", body)
		return rec.Code, gitCode(t, rec.Body.Bytes())
	}
	pub := restCreate(t, e, `{"name":"Mirror","url":"ssh://git@forge.example.com/a/mirror.git","audience":"public","only_done":true}`)
	id := pub.Item.ID
	for _, c := range []struct{ what, body string }{
		{"audience private", `{"remote_id":"` + id + `","audience":"private"}`},
		{"only_done off", `{"remote_id":"` + id + `","only_done":false}`},
		{"a new address", `{"remote_id":"` + id + `","url":"ssh://git@forge.example.com/a/elsewhere.git"}`},
	} {
		if code, gc := patch(c.body); code != http.StatusConflict || gc != "confirmation_required" {
			t.Errorf("%s with no confirmation: %d %q", c.what, code, gc)
		}
		wrong := strings.TrimSuffix(c.body, "}") + `,"confirm_name":"mirror"}`
		if code, gc := patch(wrong); code != http.StatusConflict || gc != "confirmation_required" {
			t.Errorf("%s with a wrong name: %d %q", c.what, code, gc)
		}
	}
	// nothing changed so far
	got := e.s.do(e.owner, "GET", "/api/v1/projects/"+e.pid+"/git/remote?remote_id="+id, "")
	var r restRemote
	_ = json.Unmarshal(got.Body.Bytes(), &r)
	if r.Item.Audience != "public" || !r.Item.OnlyDone || strings.Contains(r.Item.URL, "elsewhere") {
		t.Errorf("a refused change was made: %+v", r.Item)
	}
	// changes that do not widen need nothing
	if code, _ := patch(`{"remote_id":"` + id + `","name":"Mirror two","branch":"main"}`); code != http.StatusOK {
		t.Errorf("a rename: %d", code)
	}
	// the right name makes the change
	if code, gc := patch(`{"remote_id":"` + id + `","audience":"private","confirm_name":"Mirror two"}`); code != http.StatusOK {
		t.Errorf("widening with the name: %d %q", code, gc)
	}
	// a private remote that was never public: a full copy needs no name, and narrowing needs none either
	priv := restCreate(t, e, `{"name":"Backup","url":"ssh://git@forge.example.com/a/backup.git","audience":"private"}`)
	if code, gc := patch(`{"remote_id":"` + priv.Item.ID + `","include_notes":true}`); code != http.StatusOK {
		t.Errorf("notes on a private remote that was never public: %d %q", code, gc)
	}
}

// A private remote at an address that was ever public, made new or re-pointed there, needs the name too; and so does an ssh
// address, for which the host cannot be asked.
func TestAddressesThatWerePublicNeedTheNameWhateverTheRemoteIsNow(t *testing.T) {
	e := newWriteEnv(t)
	create := func(body string) (int, string) {
		rec := e.s.do(e.owner, "POST", "/api/v1/projects/"+e.pid+"/git/remotes", body)
		return rec.Code, gitCode(t, rec.Body.Bytes())
	}
	addr := "ssh://git@forge.example.com/a/once-public.git"
	pub := restCreate(t, e, `{"name":"Mirror","url":"`+addr+`","audience":"public"}`)
	// delete it and make a private one at the same address
	if rec := e.s.do(e.owner, "DELETE", "/api/v1/projects/"+e.pid+"/git/remote?remote_id="+pub.Item.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if code, gc := create(`{"name":"Backup","url":"` + addr + `","audience":"private"}`); code != http.StatusConflict || gc != "confirmation_required" {
		t.Errorf("a private remote at a public address with no name: %d %q", code, gc)
	}
	if code, gc := create(`{"name":"Backup","url":"` + addr + `","audience":"private","confirm_name":"backup"}`); code != http.StatusConflict || gc != "confirmation_required" {
		t.Errorf("with a wrong name: %d %q", code, gc)
	}
	if code, _ := create(`{"name":"Backup","url":"` + addr + `","audience":"private","confirm_name":"Backup"}`); code != http.StatusCreated {
		t.Errorf("with the name: %d", code)
	}
	// a private remote elsewhere, re-pointed at that address
	other := restCreate(t, e, `{"name":"Elsewhere","url":"ssh://git@forge.example.com/a/else.git","audience":"private"}`)
	patch := func(body string) (int, string) {
		rec := e.s.do(e.owner, "PATCH", "/api/v1/projects/"+e.pid+"/git/remote", body)
		return rec.Code, gitCode(t, rec.Body.Bytes())
	}
	if code, gc := patch(`{"remote_id":"` + other.Item.ID + `","url":"` + addr + `"}`); code != http.StatusConflict || gc != "confirmation_required" {
		t.Errorf("re-pointing at a public address with no name: %d %q", code, gc)
	}
	if code, gc := patch(`{"remote_id":"` + other.Item.ID + `","url":"` + addr + `","confirm_name":"Elsewhere"}`); code != http.StatusOK {
		t.Errorf("re-pointing with the name: %d %q", code, gc)
	}
	// re-pointing it at a fresh address needs nothing
	if code, gc := patch(`{"remote_id":"` + other.Item.ID + `","url":"ssh://git@forge.example.com/a/fresh.git"}`); code != http.StatusConflict {
		// this remote now sits at a public address, so leaving it is a change of address on an exposed remote
		t.Logf("leaving a public address: %d %q", code, gc)
	}
}

// Widening the Kindle domain list over REST needs the added domains named back, in the op; taking away needs nothing.
func TestWideningTheKindleDomainsOverRESTNeedsThemNamedInTheOp(t *testing.T) {
	w := newSiteWorld(t, nil)
	put := func(body string) (int, string) {
		rec := w.s.do(w.admin, "PUT", "/api/v1/admin/mail", body)
		var e struct {
			Error struct {
				Data map[string]any `json:"data"`
			}
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		c, _ := e.Error.Data["mail_code"].(string)
		return rec.Code, c
	}
	if code, mc := put(`{"kindle_domains":["kindle.com","free.kindle.com","evil.example"]}`); code != http.StatusConflict || mc != "confirmation_required" {
		t.Errorf("no confirmation: %d %q", code, mc)
	}
	if code, mc := put(`{"kindle_domains":["kindle.com","free.kindle.com","evil.example"],"confirm_domains":["other.example"]}`); code != http.StatusConflict || mc != "confirmation_required" {
		t.Errorf("the wrong domain: %d %q", code, mc)
	}
	if got := w.mailCfg(t).KindleDomains; len(got) != 2 {
		t.Errorf("a refused change was made: %v", got)
	}
	if code, mc := put(`{"kindle_domains":["kindle.com","free.kindle.com","evil.example"],"confirm_domains":["Evil.Example"]}`); code != http.StatusOK {
		t.Errorf("the right domain: %d %q", code, mc)
	}
	if code, _ := put(`{"kindle_domains":["kindle.com"]}`); code != http.StatusOK {
		t.Errorf("taking away: %d", code)
	}
	if code, _ := put(`{"kindle_domains":[]}`); code != http.StatusOK {
		t.Errorf("the default: %d", code)
	}
}
