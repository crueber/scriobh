package imports_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/imports"
)

// Re-review of the fix round: the import side.

func (e *env) rereviewSuggestions(projectID string) []review.SuggestionOut {
	e.t.Helper()
	out := e.must(e.w, "suggestion.list", review.SuggestionListIn{ProjectID: projectID, Status: "all", Limit: 200}).(review.SuggestionListOut)
	return out.Items
}

var rereviewN int

func rereviewTitle() string { rereviewN++; return fmt.Sprintf("Rereview %d", rereviewN) }

// The claim of the fix round: accepting the suggestions one at a time, in any order,
// gives what the "accept" import gives. With repeated text (a refrain, a repeated
// line of dialogue) it is not so. Here the two "cat" deletions have the same quote
// and the same 24 characters either side: the importer, which anchors with 60 bytes
// of context, finds each exactly once, but the stored context (24 runes a side)
// cannot tell them apart. The first suggestion accepted is applied by its stored
// position (the revision is unchanged); every later one is found again by context
// alone, exactly once. If the first accepted is the "dog " deletion, both "cat"s are
// still there when the second is looked for, and it is refused with "Reject it and
// ask for a new one". PlaceableAfterOthers asks only about the state with ALL the
// others applied, where one "cat" is left. Of the six orders, two fail. (A generator
// of changes 30 to 80 characters apart in repeated text found 1 to 5 such documents
// in every 1,500; in text that does not repeat none in 3,000.)
func TestRereviewImportedSuggestionsAcceptInAnyOrderWithRepeatedText(t *testing.T) {
	e := newEnv(t)
	X := "ebadcgfacaahecaadechfcadghbhhfgfgacbbhbeceacbhddahfa"
	Y := "cebadcgfacaahecaadechfcadghbhhfgfgacbbhbeceacbhddahfa"
	Z := "cebadcgfacaahecaadechfca"
	doc := body(para("Heading1", "One") + para2(del("Au3", rrDate0, "dog "), run(X), del("Au1", rrDate0, "cat"), run(Y), del("Au0", rrDate0, "cat"), run(Z)))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	// The three changes cannot all be suggestions: the two "cat" deletions have the same quote and the same context,
	// so each is found exactly once only if the other is already accepted. The importer takes one as accepted; what it
	// writes must be acceptable in every order.
	if pv.Suggestions < 1 || pv.Suggestions > 2 {
		t.Fatalf("the preview wrote %d suggestions, want one or two (the third change is taken as accepted)", pv.Suggestions)
	}
	pa := e.previewDocx(d, imports.PreviewIn{DocxChanges: "accept"})
	want := e.docContent(e.applyDocx(d, pa, imports.ApplyIn{DocxChanges: "accept", Title: rereviewTitle()}).ProjectID, "One")
	for _, order := range permutations(pv.Suggestions) {
		ap := e.applyDocx(d, pv, imports.ApplyIn{Title: rereviewTitle()})
		sugs := e.rereviewSuggestions(ap.ProjectID)
		for _, i := range order {
			if _, err := e.call(e.w, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: ap.ProjectID, SuggestionID: sugs[i].ID}); err != nil {
				t.Errorf("order %v: accepting suggestion %d: %v", order, i, err)
				break
			}
		}
		if got := e.docContent(ap.ProjectID, "One"); got != want && !t.Failed() {
			t.Errorf("order %v: accepted gives\n%q\nthe accept import gives\n%q", order, got, want)
		}
	}
}

const rrDate0 = "2024-03-05T10:21:00Z"

// A suggestion is accepted or rejected whole (there is no per-change accept), and carries one label. So a
// suggestion must not hold the changes of several authors: the label cannot be cut to hide some of them, because
// there is only ever one. Thirty authors each change a word, a few words apart: every suggestion has one author
// and one author's changes, and what became of the other changes is counted and named.
func TestRereviewManyAuthorsOfOneSuggestionAreNotHiddenByTheLabelCap(t *testing.T) {
	e := newEnv(t)
	var parts []string
	for i := 0; i < 30; i++ {
		parts = append(parts, run(fmt.Sprintf("w%d ", i)), ins(fmt.Sprintf("Reviewer Number %02d", i), rrDate0, fmt.Sprintf("Y%d ", i)))
	}
	d := miniDocx(t, map[string]string{"word/document.xml": body(para("Heading1", "One") + para2(parts...)), "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	ap := e.applyDocx(d, pv, imports.ApplyIn{Title: rereviewTitle()})
	sugs := e.rereviewSuggestions(ap.ProjectID)
	if len(sugs) == 0 {
		t.Fatal("no suggestion was written")
	}
	for _, s := range sugs {
		if strings.Contains(s.Author.Name, " and ") || strings.Contains(s.Author.Name, ", ") || !strings.HasPrefix(s.Author.Name, "Reviewer Number ") {
			t.Errorf("a suggestion of %d changes is labelled %q: one author each", len(s.Changes), s.Author.Name)
		}
		for _, c := range s.Changes {
			// the inserted text Yn belongs to Reviewer Number n: every change in a suggestion is its label's
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(c.Text), "Y%d", &n); err != nil || s.Author.Name != fmt.Sprintf("Reviewer Number %02d", n) {
				t.Errorf("suggestion by %q holds the change %q", s.Author.Name, c.Text)
			}
		}
	}
	tr := pv.Report["docx"].(map[string]any)["tracked"].(map[string]any)
	if tr["author_conflicts"].(int) == 0 || len(tr["author_conflict_pairs"].([]string)) == 0 {
		t.Errorf("the changes of other authors taken as accepted are not counted and named: %v", tr)
	}
	if got := len(sugs) + tr["taken_as_accepted"].(int); got != 30 {
		t.Errorf("%d suggestions and %d taken as accepted: not the 30 changes", len(sugs), tr["taken_as_accepted"].(int))
	}
}

// Whatever the colon or the space, a Word author cannot be labelled as a guest of a share link.
func TestCleanLabelNeverLooksLikeAGuest(t *testing.T) {
	look := regexp.MustCompile(`(?i)^guest\s*[:\x{ff1a}\x{fe55}\x{a789}\x{2236}]`)
	for _, in := range []string{"Guest: Ann", "GUEST:Ann", "Guest : Ann", "Guest\u3000: Ann", "Guest\uff1a Ann", "\uff27uest: Ann", "Guest\ufe55 Ann", "Guest\ua789 Ann", "Guest: Guest: Ann", " guest :\tAnn", "Guest\u00a0: Ann", "\uff27\uff55\uff45\uff53\uff54\uff1a Ann"} {
		got := review.CleanLabel(in)
		if look.MatchString(got) || strings.Contains(strings.ToLower(got), "guest") {
			t.Errorf("CleanLabel(%q) = %q looks like a share-link guest", in, got)
		}
	}
	for in, want := range map[string]string{"Guesthouse Ann": "Guesthouse Ann", "Ann": "Ann", "": "Word", "Guest:": "Word"} {
		if got := review.CleanLabel(in); got != want {
			t.Errorf("CleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// CleanLabel trims "Guest:" so that a Word author cannot look like a guest of a
// share link, but not "Guest : Bob" or the full-width colon.
func TestRereviewWordAuthorCannotLookLikeAGuestWithAnotherColon(t *testing.T) {
	e := newEnv(t)
	look := regexp.MustCompile(`(?i)^guest\s*[:\x{ff1a}\x{fe55}]`)
	for i, name := range []string{"Guest : Bob", "Guest： Bob"} {
		doc := body(para("Heading1", "One") + para2(run(fmt.Sprintf("A line of text that is unique number %d, long enough to anchor. ", i)), ins(name, rrDate0, "added"), run(" and the rest of the line goes on.")))
		d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
		ap := e.applyDocx(d, e.previewDocx(d, imports.PreviewIn{}), imports.ApplyIn{Title: rereviewTitle()})
		for _, s := range e.rereviewSuggestions(ap.ProjectID) {
			if look.MatchString(s.Author.Name) {
				t.Errorf("the label %q still looks like a share-link guest", s.Author.Name)
			}
		}
	}
}
