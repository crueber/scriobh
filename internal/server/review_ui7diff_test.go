package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/editiondiff"
)

// The path of the edition in a redirect after an action is a path segment: a value that holds a slash, a question mark
// or a hash must not change where the 303 goes (review ui-7 diff, finding M1).
func TestDiffRedirectKeepsTheEditionInOneSegment(t *testing.T) {
	for _, edn := range []string{"../../evil", "a/b", "x?y=z", "x#frag", "a b"} {
		raw := diffURL("proj", edn, "b1:doc_1")
		u, err := url.Parse(raw)
		if err != nil {
			t.Errorf("%q: %v", edn, err)
			continue
		}
		if u.Fragment != "" || strings.Count(u.EscapedPath(), "/") != 5 || !strings.HasSuffix(u.Path, "/diff") {
			t.Errorf("an edition id of %q sends the redirect to %q (path %q): one segment was expected", edn, raw, u.EscapedPath())
		}
	}
}

// The button that offers suggestions acts on the differences the page lists and no others: a chapter of 350 differences is
// paged (50 to a page, every one reachable), the button says how many it offers, and the form carries only the ids on the
// page (finding H1; the page is the decided answer: no difference is offered that cannot be seen).
func TestSuggestAllActsOnlyOnTheDifferencesOnThePage(t *testing.T) {
	s := newWebServer(t)
	d := diffFixture("step")
	hs := make([]editiondiff.Hunk, 0, 350)
	for i := 1; i <= 350; i++ {
		hs = append(hs, editiondiff.Hunk{ID: fmt.Sprintf("hunk%03d", i), Index: i, Kind: "replace", Base: "old", Head: "new", Words: editiondiff.Words{Added: 1, Removed: 1}})
	}
	d.Res.Steps[0].Hunks = hs
	d.Hunks, d.OfferN, d.OfferIDs = nil, 0, nil
	d.selectStep(d.Res, "b2:d1", 3) // the third page: differences 101 to 150
	d.Revision = 4
	doc := renderPage(t, s, fixture{name: "editiondiff", page: Page{Title: "Changes", Path: "/p/p-a/editions/edn_1/diff", User: fixtureUser("writer"), CSRF: "tok_writer", Data: d}})
	for _, want := range []string{"Differences 101 to 150 of 350", "Page 3 of 7", "Suggest these 50 changes", `value="hunk101"`, `value="hunk150"`, `rel="prev"`, `rel="next"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("the third page lacks %q", want)
		}
	}
	for _, bad := range []string{"Suggest all of these changes", "more differences are not listed", `value="hunk100"`, `value="hunk151"`, "Difference 100 of", "Difference 151 of"} {
		if strings.Contains(doc, bad) {
			t.Errorf("the third page has %q: it must offer and show only differences 101 to 150", bad)
		}
	}
	if n := strings.Count(doc, `name="hunk" value="hunk1`); n != 100 { // 50 in the page's button, 50 in each difference's own form: hunk101..hunk150
		t.Errorf("hunk ids in forms: %d, want 100", n)
	}
}
