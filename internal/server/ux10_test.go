package server

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// U10: "N of M documents" agrees with M, not with N. The import's documents are trashed down to one, which is left in the project.
func TestImportSourceCountAgreesWithItsDocumentCount(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	p := ops.Principal{UserID: w.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "Count", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	pr := out.(projects.Item).Item
	stage := func() string {
		rec := s.stageForm(t, w, "?into="+pr.Slug, [][2]string{{"fmt", "docx"}, {"kind", "docx"}, {"panel", "docx"}}, []stagePart{{"file", "two.docx", testDocx(t, "One", "Two")}}, false)
		if rec.Code != 303 {
			t.Fatalf("staging: %d %s", rec.Code, rec.Body.String())
		}
		return strings.TrimPrefix(rec.Header().Get("Location"), "/p/"+pr.Slug+"/import/d/")
	}
	id := stage()
	_, page := s.getPage(t, w, "/p/"+pr.Slug+"/import/d/"+id)
	tok := regexp.MustCompile(`name="token" value="([^"]+)"`).FindStringSubmatch(page)[1]
	if rec := s.postForm(t, w, "/ui/import/d/"+id+"/apply", url.Values{"rev": {"1"}, "token": {tok}, "slug": {pr.Slug}}); rec.Code != 303 {
		t.Fatalf("apply: %d %.300s", rec.Code, rec.Body.String())
	}
	lo, err := s.Reg.Call(context.Background(), p, "doc.list", docs.ListIn{ProjectID: pr.ID})
	if err != nil {
		t.Fatal(err)
	}
	order := lo.(docs.ListOut).Order
	if len(order) < 3 {
		t.Fatalf("the project holds %d documents, want its first and the import's two", len(order))
	}
	// The project's own first document is not the import's: trash one of the two the import made, leaving one of them.
	if _, err := s.Reg.Call(context.Background(), p, "doc.trash", docs.DocIn{ID: order[len(order)-1]}); err != nil {
		t.Fatal(err)
	}
	_, hist := s.getPage(t, w, "/p/"+pr.Slug+"/import")
	want := "1 of 2 documents are in the project."
	if !strings.Contains(hist, want) || strings.Contains(hist, "document is in the project") {
		t.Errorf("want %q; the page says: %s", want, tagStrip(hist))
	}
}

// U10: the continue card opens the chapter the writer left, and asks the writing view to restore the caret (#resume, js/resume.js).
func TestContinueWhereYouLeftOffGoesThroughTheCaretRestore(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	p := ops.Principal{UserID: w.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "Resume Book", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	pr := out.(projects.Item).Item
	dout, err := s.Reg.Call(context.Background(), p, "doc.create", docs.CreateIn{ProjectID: pr.ID, Title: "The Road", Kind: "chapter", Content: "Words here.\n"})
	if err != nil {
		t.Fatal(err)
	}
	did := dout.(docs.Item).Item.ID
	if _, err := s.Reg.Call(context.Background(), p, "project.open", projects.OpenIn{ID: pr.ID, DocumentID: did}); err != nil {
		t.Fatal(err)
	}
	_, shelf := s.getPage(t, w, "/")
	want := `class="continue" href="/p/` + pr.Slug + `/d/` + did + `#resume"`
	if !strings.Contains(shelf, want) {
		t.Errorf("the continue card does not restore the caret, want %s", want)
	}
}

// U10: a scene's header reads "Scene · Parent · 4 words" for the eye and for a screen reader, with no stray commas and no empty
// number between separators.
func TestSceneHeaderHasNoStrayPunctuation(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	p := ops.Principal{UserID: w.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "Scene Header", Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	pr := out.(projects.Item).Item
	ch, err := s.Reg.Call(context.Background(), p, "doc.create", docs.CreateIn{ProjectID: pr.ID, Title: "The Road", Kind: "chapter", Content: "Words.\n"})
	if err != nil {
		t.Fatal(err)
	}
	sc, err := s.Reg.Call(context.Background(), p, "doc.create", docs.CreateIn{ProjectID: pr.ID, Title: "The Quay", Kind: "scene", ParentID: ch.(docs.Item).Item.ID, Content: "A scene of words.\n"})
	if err != nil {
		t.Fatal(err)
	}
	_, body := s.getPage(t, w, "/p/"+pr.Slug+"/d/"+sc.(docs.Item).Item.ID)
	meta := regexp.MustCompile(`(?s)<p class="doc-meta eyebrow" id="doc-meta">.*?</p>`).FindString(body)
	if meta == "" {
		t.Fatal("no document header")
	}
	for _, bad := range []string{", in", ", </span>", ", <span", "· </span><span class=\"sr-only\">, ", "<span aria-hidden=\"true\"> · </span><span aria-hidden"} {
		if strings.Contains(meta, bad) {
			t.Errorf("the header has %q: %s", bad, meta)
		}
	}
	if !strings.Contains(meta, `<span class="sr-only">in </span><span class="doc-parent">The Road</span>`) || strings.Contains(meta, `id="doc-num"`) {
		t.Errorf("the header does not name its chapter as a scene's parent: %s", meta)
	}
}

// U10: the figures in the Compile options are labels, not a sample: the "About" and "Word count" choices name no number of words.
func TestCompileWordCountChoicesNameNoFigure(t *testing.T) {
	c, err := i18n.Load()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, k := range c.Keys() {
		if !strings.HasPrefix(k, "cmp.js.v_word_count_") {
			continue
		}
		n++
		if v := c.Raw("en", k); regexp.MustCompile(`\d`).MatchString(v) {
			t.Errorf("%s says %q: a figure that is not the book's", k, v)
		}
	}
	if n == 0 {
		t.Fatal("no word count choices in the catalogue")
	}
}

var _ = tagOf

// tagStrip is the text of a page, for a message.
func tagStrip(h string) string {
	return strings.Join(strings.Fields(regexp.MustCompile(`(?s)<[^>]*>`).ReplaceAllString(h, " ")), " ")
}
