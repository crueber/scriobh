package imports_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
)

// Accepting suggestions one at a time is what the review screen does. Changes of one paragraph that are within
// the stored context of each other cannot both be accepted one after the other (the second says "reject it and
// ask for a new one"), so the importer never writes such a pair as two suggestions: of one author they are one
// suggestion; of two authors the later is taken as accepted. Whatever it writes, every suggestion can be accepted
// alone, in every order, and the text is then what the accept import gives.
func TestReviewImportedSuggestionsCloseTogetherCanBeAcceptedOneAtATime(t *testing.T) {
	e := newEnv(t)
	d1, d2 := "2024-03-05T10:21:00Z", "2024-03-06T10:21:00Z"
	far := " The harbour smelled of salt and tar while the gulls wheeled over the quay, and nobody counted the boats. "
	cases := []struct {
		name string
		doc  string
		want int // suggestions written
	}{
		{"one author, one word apart", para2(run("The boat came in at "), del("Ann", d1, "dawn"), ins("Ann", d1, "first light"), run(" and "), ins("Ann", d1, "slowly "), run("spoke of the winter.")), 1},
		{"two authors, one word apart", para2(run("The boat came in at "), del("Ann", d1, "dawn"), ins("Ann", d1, "first light"), run(" and "), ins("Bo", d2, "slowly "), run("spoke of the winter.")), 1},
		{"one author, far apart", para2(run("The boat came in at "), ins("Ann", d1, "first light "), run("and was tied up."+far), ins("Ann", d1, "slowly "), run("spoke of the winter.")), 2},
		{"two authors, far apart", para2(run("The boat came in at "), ins("Ann", d1, "first light "), run("and was tied up."+far), ins("Bo", d2, "slowly "), run("spoke of the winter.")), 2},
	}
	for _, c := range cases {
		d := miniDocx(t, map[string]string{"word/document.xml": body(para("Heading1", "One") + c.doc), "word/styles.xml": headingStyles})
		pv := e.previewDocx(d, imports.PreviewIn{})
		if pv.Suggestions != c.want {
			t.Errorf("%s: %d suggestions, want %d (%v)", c.name, pv.Suggestions, c.want, pv.Warnings)
			continue
		}
		pa := e.previewDocx(d, imports.PreviewIn{DocxChanges: "accept"})
		want := e.docContent(e.applyDocx(d, pa, imports.ApplyIn{DocxChanges: "accept", Title: rereviewTitle()}).ProjectID, "One")
		for _, order := range permutations(c.want) {
			ap := e.applyDocx(d, pv, imports.ApplyIn{Title: rereviewTitle()})
			sugs := e.suggestions(ap.ProjectID)
			if len(sugs) != c.want {
				t.Fatalf("%s: %d suggestions listed, want %d", c.name, len(sugs), c.want)
			}
			for _, i := range order {
				if _, err := e.call(e.w, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: ap.ProjectID, SuggestionID: sugs[i].ID}); err != nil {
					t.Errorf("%s: order %v: accepting %q alone: %v", c.name, order, sugs[i].Changes[0].Text, err)
				}
			}
			if got := e.docContent(ap.ProjectID, "One"); got != want {
				t.Errorf("%s: order %v: accepted gives\n%q\nthe accept import gives\n%q", c.name, order, got, want)
			}
		}
	}
}

// Accepting all of them must give what the accept import gives. An insertion directly before a deletion of the
// same words is one replacement when one author made both, and when two did the later change is taken as
// accepted; neither is two suggestions that accept_all treats as overlapping.
func TestReviewAcceptAllGivesTheAcceptImportForAnInsertionBesideADeletion(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	rest := "cat dog dog mat cat ran far mat and cat a mat cat ran cat sat mat sat cat far ran the a mat sat far "
	for _, c := range []struct {
		name, insText, author2 string
		want                   int
	}{
		{"one author, the same words back (a no-op)", "cat ", "A0", 0},
		{"one author, other words", "far ", "A0", 1},
		{"two authors, the same words", "cat ", "A2", 1},
		{"two authors, other words", "far ", "A2", 1},
	} {
		doc := body(para("Heading1", "One") + para2(run("sat "), ins("A0", d1, c.insText), del(c.author2, d1, "cat "), run(rest)))
		d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
		pv := e.previewDocx(d, imports.PreviewIn{})
		ap := e.applyDocx(d, pv, imports.ApplyIn{Title: rereviewTitle()})
		sugs := e.suggestions(ap.ProjectID)
		if len(sugs) != c.want {
			t.Fatalf("%s: %d suggestions, want %d (%v)", c.name, len(sugs), c.want, pv.Warnings)
		}
		ids := []string{}
		for _, s := range sugs {
			ids = append(ids, s.ID)
		}
		var out review.SuggestionAcceptAllOut
		if len(sugs) > 0 {
			out = e.must(e.w, "suggestion.accept_all", review.SuggestionAcceptAllIn{ProjectID: ap.ProjectID, DocumentID: sugs[0].DocumentID, SuggestionIDs: ids}).(review.SuggestionAcceptAllOut)
		}
		pa := e.previewDocx(d, imports.PreviewIn{DocxChanges: "accept"})
		aa := e.applyDocx(d, pa, imports.ApplyIn{DocxChanges: "accept", Title: rereviewTitle()})
		if got, want := e.docContent(ap.ProjectID, "One"), e.docContent(aa.ProjectID, "One"); got != want || len(out.Skipped) != 0 {
			t.Errorf("%s: preview promised %d suggestions; accept_all skipped %d (%v); text\n got  %q\n want %q", c.name, pv.Suggestions, len(out.Skipped), out.Skipped, got, want)
		}
	}
}

// U+2028 and U+2029 survive pdfread's normalise into the stored Markdown; the
// project title and outline titles keep U+202E (pdfText/clean never strip it).
func TestReviewPdfTextHasNoLineSeparatorsAndTitlesNoBidiOverrides(t *testing.T) {
	e := newEnv(t)
	d := pdftest.New()
	tu := d.Stream("", []byte("/CIDInit /ProcSet findresource begin begincmap 1 begincodespacerange <00> <FF> endcodespacerange\n3 beginbfchar\n<01> <2028>\n<02> <2029>\n<03> <0041>\nendbfchar\nendcmap end"), "")
	hf := d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /FirstChar 0 /LastChar 3 /Widths [500 500 500 500] /ToUnicode %d 0 R >>", tu))
	tf := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	var b strings.Builder
	for l := 0; l < 8; l++ {
		fmt.Fprintf(&b, "BT /T 11 Tf 72 %d Td (Plain body text line %d with ordinary words.) Tj ET\n", 700-14*l, l)
	}
	fmt.Fprintf(&b, "BT /H 11 Tf 72 560 Td <0301030203> Tj ET\n")
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /T %d 0 R /H %d 0 R >>", tf, hf), b.String(), "")
	info := d.Add("<< /Title (\\357\\273\\277Ti\\342\\200\\256tle) >>")
	root := d.Pages([]int{pg}, "")
	data := d.Write(root, pdftest.Opt{Info: info})
	pv := e.previewPdf(data, imports.PreviewIn{})
	ap := e.applyPdf(data, pv, imports.ApplyIn{})
	if strings.ContainsRune(pv.Project.Title, 0x202e) {
		t.Errorf("project title %q keeps a bidirectional override", pv.Project.Title)
	}
	for _, n := range pv.Nodes {
		if txt := e.docContent(ap.ProjectID, n.Title); strings.ContainsAny(txt, "  ") {
			t.Errorf("document %q holds a line or paragraph separator: %q", n.Title, txt)
		}
	}
}
