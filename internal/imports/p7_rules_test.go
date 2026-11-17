package imports_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
)

// The new sources keep the registry's rules: who may import, into what.
func TestPdfAndTrackedImportsKeepTheRegistryRules(t *testing.T) {
	e := newEnv(t)
	pdf := tinyPDF(t, "", pdftest.Opt{})
	d1 := "2024-03-05T10:21:00Z"
	docx := miniDocx(t, map[string]string{"word/document.xml": body(para("Heading1", "One") + para2(run("A line of text that is long enough to be found, "), ins("Ann", d1, "plus this, "), run("and it ends here."))), "word/styles.xml": headingStyles})
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Existing"}).(projects.Item).Item.ID

	// a reader (the global role) cannot import at all
	reader := e.user("reader", ops.RoleReader)
	for _, in := range []imports.PreviewIn{{Pdf: pdf}, {Docx: docx, DocxChanges: "suggest"}} {
		if _, err := e.call(reader, "import.preview", in); ops.AsError(err).Code != ops.CodeForbidden {
			t.Errorf("a reader previewed an import: %v", err)
		}
	}
	// a stranger gets "not found" for a project they are not in, a reviewer "forbidden" (it needs the editor role)
	stranger := e.user("stranger", ops.RoleWriter)
	if _, err := e.call(stranger, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Pdf: pdf}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a stranger: %v", err)
	}
	rev := e.user("reviewer", ops.RoleWriter)
	if _, err := e.d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, rev.UserID, "reviewer", db.Now()); err != nil {
		t.Fatal(err)
	}
	for _, in := range []imports.AppendPreviewIn{{ProjectID: pid, Pdf: pdf}, {ProjectID: pid, Docx: docx}} {
		if _, err := e.call(rev, "import.append_preview", in); ops.AsError(err).Code != ops.CodeForbidden {
			t.Errorf("a reviewer previewed an append: %v", err)
		}
	}
	if _, err := e.call(rev, "import.append", imports.AppendIn{ProjectID: pid, Token: "x", Docx: docx}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reviewer appended: %v", err)
	}
	// a trashed project refuses both sources, with the reason the screens read
	pv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Docx: docx, Name: "b.docx"}).(imports.PreviewOut)
	e.must(e.w, "project.trash", projects.StateIn{ID: pid})
	for name, call := range map[string]func() error{
		"pdf preview": func() error {
			_, err := e.call(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Pdf: pdf})
			return err
		},
		"docx preview": func() error {
			_, err := e.call(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Docx: docx})
			return err
		},
		"docx append": func() error {
			_, err := e.call(e.w, "import.append", imports.AppendIn{ProjectID: pid, Token: pv.Token, Docx: docx, Name: "b.docx"})
			return err
		},
	} {
		if oe := ops.AsError(call()); oe.Code != ops.CodeConflict || oe.Data["reason"] != ops.ReasonProjectTrashed {
			t.Errorf("%s into the trash: %v", name, oe)
		}
	}
	// the same file and the same preview, restored, work (and nothing was written meanwhile)
	e.must(e.w, "project.restore", projects.StateIn{ID: pid})
	out := e.must(e.w, "import.append", imports.AppendIn{ProjectID: pid, Token: pv.Token, Docx: docx, Name: "b.docx"}).(imports.ApplyOut)
	if out.Suggestions != 1 {
		t.Errorf("after the restore: %d suggestions", out.Suggestions)
	}
	_ = context.Background
}

// A Word author named like an account is stored as a label: "Guest: Bob" is how a
// guest of a share link is shown, so a label never begins that way.
func TestWordAuthorLabelsCannotLookLikeAGuest(t *testing.T) {
	e := newEnv(t)
	d1 := "2024-03-05T10:21:00Z"
	doc := body(para("Heading1", "One") + para2(run("A line of text that is long enough to be found, "), ins("Guest: Bob", d1, "plus this, "), run("and it ends here.")))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	for _, s := range e.suggestions(ap.ProjectID) {
		if s.Author.Kind != "label" || strings.HasPrefix(strings.ToLower(s.Author.Name), "guest:") || s.Author.Name != "Bob" {
			t.Errorf("author %+v", s.Author)
		}
	}
}

// Text from a Word file has no line or paragraph separators and no bidirectional
// override; the marks and the letters of a right-to-left script stay.
func TestWordTextHasNoOverridesAndKeepsRightToLeftLetters(t *testing.T) {
	e := newEnv(t)
	doc := body(para("Heading1", "On‮e") + para("", "Line one two ‪embedded‬ ⁦isolated⁩ and the Hebrew שלום with a mark‏ stays."))
	d := miniDocx(t, map[string]string{"word/document.xml": doc, "word/styles.xml": headingStyles})
	pv := e.previewDocx(d, imports.PreviewIn{})
	ap := e.applyDocx(d, pv, imports.ApplyIn{})
	title := pv.Nodes[0].Title
	text := e.docContent(ap.ProjectID, title)
	if strings.ContainsAny(title+text, "‮  ‪‬⁦⁩") {
		t.Errorf("overrides survive: %q %q", title, text)
	}
	if !strings.Contains(text, "שלום") || !strings.Contains(text, "‏") {
		t.Errorf("right-to-left text was damaged: %q", text)
	}
}
