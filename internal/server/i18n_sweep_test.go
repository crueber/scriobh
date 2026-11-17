package server

import (
	"encoding/json"
	"html"
	"net/url"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

var (
	reSkipBlocks = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)>|<!--.*?-->`)
	reTagText    = regexp.MustCompile(`(?s)<[^>]*>`)
	reAttrText   = regexp.MustCompile(`(?i)\b(?:aria-label|title|placeholder|alt|data-arm|data-arm-ok|data-arm-cancel|data-skipped)="([^"]*)"`)
	reWordRun    = regexp.MustCompile(`[\p{L}]+`)
	reArticle    = regexp.MustCompile(`(?is)<article\b.*?</article>`)
	// what a person wrote, or quoted from the manuscript, shown beside the interface: a comment, the words it is on, a
	// suggestion's old and new words, an email address
	reTheirs = regexp.MustCompile(`(?is)<blockquote class="rv-quote".*?</blockquote>|<p class="rv-body">.*?</p>|<(ins|del) class="rv-(ins|del)">.*?</(ins|del)>|[\w.+-]+@[\w.-]+`)
)

// sweepAllowed is every plain word the pages may show in the pseudo-locale, and nothing else: the names the fixtures gave
// (the titles a person typed, the names of people), months as the server writes them in UTC, identifiers that are not
// words (the system's architecture, a queue's name), a unit, and the placeholder an untitled document is shown as on every
// screen of the application (displayTitle, an older shared function that is not in the catalogue yet).
var sweepAllowed = map[string]bool{}

func init() {
	for _, w := range strings.Fields("shared book chapter one two plot notes owner editor reviewer reader stranger admin ada wren rory another sam " +
		"utc jan feb mar apr may jun jul aug sep oct nov dec " +
		"arm darwin git imports jobs pushes runs bytes untitled " +
		"jpg") { // jpg: the extension of a file a person chose
		sweepAllowed[w] = true
	}
	// The platform the diagnostics page names (darwin and arm on a laptop, linux and amd on a CI runner): the words
	// of GOOS and of GOARCH with its digits dropped.
	sweepAllowed[runtime.GOOS] = true
	for _, w := range strings.FieldsFunc(runtime.GOARCH, func(r rune) bool { return r < 'a' || r > 'z' }) {
		sweepAllowed[w] = true
	}
}

// englishIn lists the plain-English words of a page's visible text and of the attributes a person hears or sees (labels,
// titles): in the pseudo-locale every letter the catalogue supplies is accented, so a word of ordinary letters is a string
// that did not come from it.
func englishIn(page string) []string {
	// the page's own content: the frame around it (the header, the project menu, the command palette) is older and shared by
	// every screen of the application, and is translated with them
	if i := strings.Index(page, "<main"); i >= 0 {
		if j := strings.Index(page[i:], "</main>"); j >= 0 {
			page = page[i : i+j]
		}
	}
	page = reArticle.ReplaceAllString(page, " ") // the manuscript's own words are the book's, not the interface's
	page = reTheirs.ReplaceAllString(page, " ")
	page = reSkipBlocks.ReplaceAllString(page, " ")
	var texts []string
	for _, m := range reAttrText.FindAllStringSubmatch(page, -1) {
		texts = append(texts, html.UnescapeString(m[1]))
	}
	texts = append(texts, html.UnescapeString(reTagText.ReplaceAllString(page, " ")))
	seen := map[string]bool{}
	var out []string
	for _, t := range texts {
		for _, w := range reWordRun.FindAllString(t, -1) {
			lw := strings.ToLower(w)
			if len(w) < 3 || seen[lw] || sweepAllowed[lw] {
				continue
			}
			ascii := true
			for _, r := range w {
				if r > 127 {
					ascii = false
				}
			}
			if ascii {
				seen[lw] = true
				out = append(out, w)
			}
		}
	}
	sort.Strings(out)
	return out
}

// Every page of Phase 5 (review, reading with comments, sharing, people, compile, export and the console), rendered in the
// pseudo-locale, holds no run of plain English: each word on it came from the message catalogue. What a person wrote
// (a title, a comment, a name) is theirs and is let through by name.
func TestThePseudoLocaleLeavesNoEnglishOnThePhase5Pages(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_OIDC_STUB": "1"})
	pid := w.pid
	admin := w.s.newCaller(t, "admin", ops.RoleAdmin)
	// something to show: a comment, a question with a reply, a suggestion, a link
	post := func(c caller, path, body string) *struct{ Item json.RawMessage } {
		rec := w.s.do(c, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("POST %s: %d %s", path, rec.Code, rec.Body)
		}
		var o struct{ Item json.RawMessage }
		_ = json.Unmarshal(rec.Body.Bytes(), &o)
		return &o
	}
	post(w.reviewer, "/api/v1/projects/"+pid+"/comments", `{"document_id":"`+w.doc1+`","quote":"quiet house","plain":true,"body":"Zebra first.","kind":"question"}`)
	th := post(w.editor, "/api/v1/projects/"+pid+"/comments", `{"document_id":"`+w.doc1+`","quote":"stood alone","plain":true,"body":"Marmot second."}`)
	var thread struct {
		ThreadID string `json:"thread_id"`
	}
	_ = json.Unmarshal(th.Item, &thread)
	post(w.reviewer, "/api/v1/projects/"+pid+"/comments", `{"parent_id":"`+thread.ThreadID+`","body":"Heron reply."}`)
	d := w.s.do(w.owner, "GET", "/api/v1/docs/"+w.doc1, "").Body.String()
	var doc struct{ Item struct{ Revision int } }
	_ = json.Unmarshal([]byte(d), &doc)
	post(w.reviewer, "/api/v1/projects/"+pid+"/suggestions", `{"document_id":"`+w.doc1+`","base_revision":`+strconv.Itoa(doc.Item.Revision)+`,"plain":true,"changes":[{"quote":"on the hill","text":"on the otter dune"}],"note":"Otter note."}`)
	_, tok := w.link(t, `{"scope":"comment","label":"Sam"}`)
	_, ptok := w.link(t, `{"scope":"read","passphrase":"correct horse battery"}`)
	for _, c := range []caller{w.owner, w.editor, w.reviewer, admin} {
		if rec := w.s.do(c, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
			t.Fatalf("language: %d", rec.Code)
		}
	}
	type pg struct {
		name string
		c    caller
		get  string
		post string
		form url.Values
	}
	rv := "/ui/projects/" + pid + "/review/"
	pages := []pg{
		{"review", w.owner, "/p/shared-book/review", "", nil},
		{"review of a chapter", w.owner, "/p/shared-book/review?doc=" + w.doc1 + "&kind=question", "", nil},
		{"review, nothing matches", w.owner, "/p/shared-book/review?kind=suggestion-note", "", nil},
		{"resolve plan", w.owner, "", rv + "resolve_all", url.Values{"step": {"preview"}}},
		{"accept plan", w.owner, "", rv + "accept_all", url.Values{"step": {"preview"}, "document_id": {w.doc1}, "suggestion_id": suggestionIDs(t, w, pid)}},
		{"reading, as the owner", w.owner, "/p/shared-book/read/" + w.doc1, "", nil},
		{"reading, as a reviewer", w.reviewer, "/p/shared-book/read/" + w.doc1, "", nil},
		{"share", w.owner, "/p/shared-book/share", "", nil},
		{"people", w.owner, "/p/shared-book/people", "", nil},
		{"people, as an editor", w.editor, "/p/shared-book/people", "", nil},
		{"compile", w.owner, "/p/shared-book/compile", "", nil},
		{"console people", admin, "/admin/users", "", nil},
		{"console people, filtered", admin, "/admin/users?q=ad&status=active", "", nil},
		{"console quotas", admin, "/admin/quotas", "", nil},
		{"console diagnostics", admin, "/admin/diagnostics", "", nil},
		{"console disable", admin, "/admin/users/" + w.stranger.userID + "/disable", "", nil},
	}
	// a built-in preset is named in the person's language where the catalogue has its name (the page hands its script the names)
	if body := w.s.do(w.owner, "GET", "/p/shared-book/compile", "").Body.String(); !strings.Contains(body, "preset_shunn-us") || !strings.Contains(html.UnescapeString(body), "⟦Ú") {
		t.Error("the Compile page does not hand its script the preset names")
	}
	// the page a stranger's link opens, in the language their browser asks for (the pseudo-locale in a development build)
	for name, tk := range map[string]string{"public page": tok, "public page with a passphrase": ptok} {
		rec := w.pub("GET", "/s/"+tk, "", "Accept", htmlAccept, "Accept-Language", "xx")
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, "⟦") {
			t.Errorf("%s: %d, not in the pseudo-locale", name, rec.Code)
		}
		if words := englishIn(body); len(words) > 0 {
			t.Errorf("%s has English that did not come from the catalogue: %s", name, strings.Join(words, " "))
		}
	}
	for _, p := range pages {
		var body string
		var code int
		if p.post != "" {
			rec := w.s.postForm(t, p.c, p.post, p.form)
			body, code = rec.Body.String(), rec.Code
		} else {
			rec := w.s.do(p.c, "GET", p.get, "")
			body, code = rec.Body.String(), rec.Code
		}
		if code != 200 {
			t.Errorf("%s: %d", p.name, code)
			continue
		}
		if !strings.Contains(body, "⟦") {
			t.Errorf("%s: nothing in the pseudo-locale at all", p.name)
		}
		if words := englishIn(body); len(words) > 0 {
			t.Errorf("%s has English that did not come from the catalogue: %s", p.name, strings.Join(words, " "))
		}
	}
}

func suggestionIDs(t *testing.T, w *shareWorld, pid string) []string {
	t.Helper()
	rec := w.s.do(w.owner, "GET", "/api/v1/projects/"+pid+"/suggestions?status=open", "")
	var out struct{ Items []struct{ ID string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	var ids []string
	for _, i := range out.Items {
		ids = append(ids, i.ID)
	}
	return ids
}
