package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// A credential pasted into any text field of any form of the Phase 7 screens is never echoed back: not in the answer, not in
// the redirect, not in the page the redirect leads to. Every text field of every form of the pages is filled with an address
// that carries a password, one form at a time, and the canary is looked for everywhere the form's answer shows.

const sweepCanary = "SWEEPCANARY9f3a71"

var (
	reForm     = regexp.MustCompile(`(?is)<form\b[^>]*\bmethod="post"[^>]*>.*?</form>`)
	reFormAct  = regexp.MustCompile(`(?i)\baction="([^"]+)"`)
	reFormCtrl = regexp.MustCompile(`(?is)<(input|textarea|select)\b([^>]*)>`)
	reAttrName = regexp.MustCompile(`(?i)\bname="([^"]+)"`)
	reAttrType = regexp.MustCompile(`(?i)\btype="([^"]+)"`)
	reAttrVal  = regexp.MustCompile(`(?i)\bvalue="([^"]*)"`)
)

type sweepForm struct {
	action string
	fixed  url.Values // hidden fields, as the page has them
	text   []string   // the names of the fields a person types into
}

func scrapeForms(page string) []sweepForm {
	var out []sweepForm
	for _, f := range reForm.FindAllString(page, -1) {
		a := reFormAct.FindStringSubmatch(f)
		if a == nil {
			continue
		}
		sf := sweepForm{action: strings.SplitN(strings.ReplaceAll(a[1], "&amp;", "&"), "#", 2)[0], fixed: url.Values{}}
		for _, c := range reFormCtrl.FindAllStringSubmatch(f, -1) {
			nm := reAttrName.FindStringSubmatch(c[2])
			if nm == nil || nm[1] == "_csrf" {
				continue
			}
			typ := "text"
			if t := reAttrType.FindStringSubmatch(c[2]); t != nil {
				typ = strings.ToLower(t[1])
			}
			switch {
			case typ == "hidden":
				v := reAttrVal.FindStringSubmatch(c[2])
				if v != nil {
					sf.fixed.Set(nm[1], v[1])
				}
			case c[1] == "select", typ == "checkbox", typ == "radio", typ == "submit", typ == "button", typ == "file":
			default:
				sf.text = append(sf.text, nm[1])
			}
		}
		out = append(out, sf)
	}
	return out
}

func TestACredentialPastedIntoAnyFieldIsNeverEchoed(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.mailOn(t)
	cred := "https://writer:" + sweepCanary + "@example.com/o/r.git"
	slug := "the-salt-road"
	// something for every part of the Git page to show: a public remote with a signing key and a trigger
	pid := w.pid
	mk := func(vals url.Values) *http.Response { return nil }
	_ = mk
	create := w.s.postForm(t, w.owner, "/ui/projects/"+pid+"/git/create", url.Values{"name": {"Mirror"}, "url": {"ssh://git@forge.example.com/a/c.git"}, "audience": {"public"}})
	if create.Code != http.StatusSeeOther {
		t.Fatalf("a remote: %d", create.Code)
	}
	rid := reRemoteParam.FindStringSubmatch(create.Header().Get("Location"))[1]
	w.s.postForm(t, w.owner, "/ui/projects/"+pid+"/git/signing-generate", url.Values{"remote_id": {rid}})
	w.s.postForm(t, w.owner, "/ui/projects/"+pid+"/git/trigger-set", url.Values{"remote_id": {rid}, "trigger_url": {"https://hooks.example.org/x"}})
	w.s.do(w.owner, "GET", "/p/"+slug+"/git?remote="+rid, "") // the secret page is read once, here

	type page struct {
		who  caller
		path string
		sub  string // the forms of these pages are posted to this prefix only
	}
	pages := []page{
		{w.owner, "/p/" + slug + "/git?remote=" + rid, "/ui/projects/"},
		{w.owner, "/p/" + slug + "/git/publish", "/ui/projects/"},
		{w.owner, "/p/" + slug + "/git/release?remote=" + rid, "/ui/projects/"},
		{w.admin, "/admin/mail", "/ui/admin/"},
		{w.admin, "/admin/pandoc", "/ui/admin/"},
		{w.owner, "/settings", "/ui/settings/kindle"},
		{w.owner, "/p/" + slug + "/kindle", "/ui/projects/"},
	}
	// forms the pages only show once an account or a host exists: their fields, as the handlers read them
	extra := []struct {
		who    caller
		action string
		fields []string
	}{
		{w.owner, "/ui/projects/" + pid + "/publish/plan", []string{"account_id", "owner", "name", "description", "branch", "path_prefix"}},
		{w.owner, "/ui/projects/" + pid + "/publish/publish", []string{"account_id", "owner", "name", "description", "branch", "path_prefix", "plan_token", "confirm"}},
		{w.owner, "/ui/projects/" + pid + "/publish/connect", []string{"host", "label"}},
		{w.owner, "/ui/projects/" + pid + "/release/preview", []string{"tag"}},
		{w.owner, "/ui/projects/" + pid + "/release/start", []string{"tag"}},
	}
	seen := 0
	check := func(what string, who caller, rec interface {
		Header() http.Header
	}, status int, body string) {
	}
	_ = check
	run := func(who caller, action string, fixed url.Values, fields []string) {
		t.Helper()
		// all fields at once, then each field alone
		sets := [][]string{fields}
		for _, f := range fields {
			sets = append(sets, []string{f})
		}
		for _, set := range sets {
			vals := url.Values{}
			for k, v := range fixed {
				vals[k] = v
			}
			for _, f := range set {
				vals.Set(f, cred)
			}
			rec := w.s.postForm(t, who, action, vals)
			seen++
			loc := rec.Header().Get("Location")
			if strings.Contains(rec.Body.String(), sweepCanary) || strings.Contains(loc, sweepCanary) || strings.Contains(rec.Header().Get("Set-Cookie"), sweepCanary) {
				t.Errorf("%s with %v: the credential is in the answer or the redirect", action, set)
			}
			if rec.Code == http.StatusSeeOther {
				l, _, _ := strings.Cut(loc, "#")
				if next := w.s.do(who, "GET", l, ""); strings.Contains(next.Body.String(), sweepCanary) {
					t.Errorf("%s with %v: the credential is in the page the redirect leads to (%s)", action, set, l)
				}
			}
		}
	}
	for _, pg := range pages {
		code, body := w.s.getPage(t, pg.who, pg.path)
		if code != 200 {
			t.Fatalf("%s: %d", pg.path, code)
		}
		for _, f := range scrapeForms(body) {
			if !strings.HasPrefix(f.action, pg.sub) || len(f.text) == 0 {
				continue
			}
			run(pg.who, f.action, f.fixed, f.text)
		}
	}
	for _, x := range extra {
		run(x.who, x.action, url.Values{}, x.fields)
	}
	if seen < 40 {
		t.Errorf("only %d posts were made: is the scrape stale?", seen)
	}
}
