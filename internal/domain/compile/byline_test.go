package compile_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/publish/check"
)

// The name in the contact block is for a manuscript's title page: a print file,
// a cover and a book's metadata never take it, and contact: false keeps it out
// of everything.
func TestContactNameStaysOutOfPrint(t *testing.T) {
	f := newFx(t, "No Author")
	f.doc("One", "chapter", "", "Words of the chapter, ch1mark.")
	empty := ""
	if _, err := f.reg.Call(f.ctx, f.owner, "project.update", projects.UpdateIn{ID: f.pid, AuthorName: &empty}); err != nil {
		t.Fatal(err)
	}
	name, surname := "Private Person", "Person"
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.contact.set", compile.ContactSetIn{ProjectID: f.pid, Name: &name, Surname: &surname}); err != nil {
		t.Fatal(err)
	}
	out := f.compile("print-6x9", publish.Options{})
	doc, probs := check.ParsePDF(out.Body)
	for _, p := range probs {
		t.Error(p)
	}
	if doc.Author != "" || strings.Contains(doc.Text(), "Private") || strings.Contains(string(out.Body), "Private") {
		t.Errorf("the contact name reached the print file: author %q", doc.Author)
	}
	var noAuthor bool
	for _, w := range out.Warnings {
		if w.Code == "no_author" {
			noAuthor = true
		}
	}
	if !noAuthor {
		t.Errorf("no no_author warning: %+v", out.Warnings)
	}
	// the print file does not depend on the contact block, and its hash says so
	name2 := "Someone Else"
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.contact.set", compile.ContactSetIn{ProjectID: f.pid, Name: &name2}); err != nil {
		t.Fatal(err)
	}
	if again := f.compile("print-6x9", publish.Options{}); again.InputHash != out.InputHash {
		t.Error("a print file's hash changed with the contact block")
	}
	// the cover
	cv, err := f.reg.Call(f.ctx, f.owner, "export.cover", compile.CoverIn{ProjectID: f.pid, PageCount: 200, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	cd, _ := check.ParsePDF(cv.(compile.Out).Body)
	if cd.Author != "" || strings.Contains(cd.Text(), "Someone") || strings.Contains(cd.Text(), "Private") {
		t.Errorf("the cover has a contact name: %q %q", cd.Author, cd.Text())
	}
	// the manuscript keeps its fallback, unless contact is off
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.contact.set", compile.ContactSetIn{ProjectID: f.pid, Name: &name}); err != nil {
		t.Fatal(err)
	}
	docx := unzip(t, f.compile("shunn-us", publish.Options{}).Body)["word/document.xml"]
	if !strings.Contains(docx, "Private Person") {
		t.Error("the manuscript lost its contact name")
	}
	no := false
	docx = unzip(t, f.compile("shunn-us", publish.Options{Contact: &no}).Body)["word/document.xml"]
	hdr := ""
	for k, v := range unzip(t, f.compile("shunn-us", publish.Options{Contact: &no}).Body) {
		if strings.HasPrefix(k, "word/header") || strings.HasPrefix(k, "word/footer") {
			hdr += v
		}
	}
	if strings.Contains(docx, "Private") || strings.Contains(hdr, "Private") || strings.Contains(hdr, "Person") {
		t.Error("contact: false still put the contact name in the manuscript")
	}
}
