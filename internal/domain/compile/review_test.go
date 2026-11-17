package compile_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/publish"
)

func spp(s string) *string { return &s }

// What is inside front matter or back matter is front matter or back matter: it
// is left out with it when the preset switches it off, it comes first or last
// with it, and a back matter page is never glued to the last chapter.
func TestFrontAndBackMatterBringTheirContentsAlong(t *testing.T) {
	f := newFx(t, "Kids")
	fm := f.doc("Front", "front_matter", "", "")
	f.doc("Epigraph", "scene", fm.ID, "frontkidmark")
	f.doc("Ch", "chapter", "", "chmark")
	bm := f.doc("Back", "back_matter", "", "")
	f.doc("Ack", "scene", bm.ID, "backkidmark")

	off := false
	p := f.preview("plain-markdown", publish.Options{FrontMatter: &off, BackMatter: &off})
	if p.SwitchedOff != 4 || p.ExcludedTotal != 0 {
		t.Errorf("switched off %d, do-not-compile %d: %+v", p.SwitchedOff, p.ExcludedTotal, p.Excluded)
	}
	reasons := map[string]string{}
	for _, e := range p.Excluded {
		reasons[e.Title] = e.Reason
	}
	if !strings.Contains(reasons["Epigraph"], "inside front matter") || !strings.Contains(reasons["Front"], "front matter is switched off") {
		t.Errorf("reasons: %v", reasons)
	}
	for _, preset := range []string{"plain-markdown", "styled-docx", "epub3"} {
		out := f.compile(preset, publish.Options{FrontMatter: &off, BackMatter: &off})
		all := string(out.Body)
		if preset != "plain-markdown" {
			for _, b := range unzip(t, out.Body) {
				all += b
			}
		}
		if strings.Contains(all, "frontkidmark") || strings.Contains(all, "backkidmark") || !strings.Contains(all, "chmark") {
			t.Errorf("%s with front and back matter off: front child %v, back child %v", preset, strings.Contains(all, "frontkidmark"), strings.Contains(all, "backkidmark"))
		}
	}

	on := true
	out := f.compile("plain-markdown", publish.Options{FrontMatter: &on, BackMatter: &on})
	s := string(out.Body)
	idx := func(sub string) int { return strings.Index(s, sub) }
	if !(idx("## Front") >= 0 && idx("## Front") < idx("frontkidmark") && idx("frontkidmark") < idx("chmark") && idx("chmark") < idx("## Back") && idx("## Back") < idx("backkidmark")) {
		t.Errorf("order:\n%s", s)
	}
	// the back matter child is not part of the last chapter's section
	p = f.preview("plain-markdown", publish.Options{})
	var ch compile.OutlineItem
	for _, o := range p.Outline {
		if o.Kind == "chapter" {
			ch = o
		}
	}
	if len(ch.Documents) != 1 {
		t.Errorf("the chapter holds %v", ch.Documents)
	}
}

// The input hash covers what changes the file: renaming a chapter changes it.
func TestInputHashCoversWhatChangesTheFile(t *testing.T) {
	f := newFx(t, "Hash")
	ch := f.doc("Old Title", "chapter", "", "Body text.")
	f.setContact()
	base := f.preview("shunn-us", publish.Options{}).InputHash
	same := f.compile("shunn-us", publish.Options{})
	if same.InputHash != base {
		t.Fatal("the preview and the export disagree")
	}
	mustDiffer := func(what string, h string) {
		t.Helper()
		if h == base {
			t.Errorf("%s did not change the input hash", what)
		}
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.update", docs.UpdateIn{ID: ch.ID, BaseRevision: ch.Revision, Title: spp("New Title")}); err != nil {
		t.Fatal(err)
	}
	renamed := f.compile("shunn-us", publish.Options{})
	mustDiffer("renaming a chapter", renamed.InputHash)
	if renamed.SHA256 == same.SHA256 {
		t.Error("the file did not change either")
	}
	base = renamed.InputHash
	// the project: its colour is on the EPUB's cover
	for _, up := range []projects.UpdateIn{{ID: f.pid, CoverColor: spp("#7A4B2A")}} {
		before := f.compile("epub3", publish.Options{})
		if _, err := f.reg.Call(f.ctx, f.owner, "project.update", up); err != nil {
			t.Fatal(err)
		}
		after := f.compile("epub3", publish.Options{})
		if before.InputHash == after.InputHash {
			t.Errorf("%+v did not change the EPUB's input hash", up)
		}
	}
	// the bundle: a changed description is a changed hash
	sub := func() string {
		out, err := f.reg.Call(f.ctx, f.owner, "export.submission", compile.SubmissionIn{ProjectID: f.pid})
		if err != nil {
			t.Fatal(err)
		}
		return out.(compile.Out).InputHash
	}
	h1 := sub()
	if _, err := f.reg.Call(f.ctx, f.owner, "project.update", projects.UpdateIn{ID: f.pid, Description: spp("A new synopsis.")}); err != nil {
		t.Fatal(err)
	}
	h2 := sub()
	if h1 == h2 {
		t.Error("a new synopsis did not change the bundle's input hash")
	}
	// the genre is in the cover letter
	if _, err := f.reg.Call(f.ctx, f.owner, "project.update", projects.UpdateIn{ID: f.pid, Genre: spp("Fantasy")}); err != nil {
		t.Fatal(err)
	}
	if h3 := sub(); h3 == h2 {
		t.Error("a new genre did not change the bundle's input hash")
	}
	// a contact block change
	h := f.compile("shunn-us", publish.Options{}).InputHash
	e := "other@example.com"
	f.reg.Call(f.ctx, f.owner, "compile.contact.set", compile.ContactSetIn{ProjectID: f.pid, Email: &e})
	if f.compile("shunn-us", publish.Options{}).InputHash == h {
		t.Error("the contact block is not in the hash")
	}
}

// A note whose reference is ghosted is not in the file, in any format.
func TestANoteOfAGhostedReferenceIsLeftOut(t *testing.T) {
	f := newFx(t, "Ghosts")
	f.doc("Ch", "chapter", "", "Body.[^1] <scriobh-ghost>ghost ref[^2]</scriobh-ghost>\n\n[^1]: one\n\n[^2]: GHOSTNOTE\n")
	md := f.compile("plain-markdown", publish.Options{})
	if strings.Contains(string(md.Body), "GHOSTNOTE") || !strings.Contains(string(md.Body), "[^1]: one") {
		t.Errorf("markdown:\n%s", md.Body)
	}
	z := unzip(t, f.compile("styled-docx", publish.Options{}).Body)
	if strings.Contains(z["word/footnotes.xml"], "GHOSTNOTE") {
		t.Error("the DOCX holds the ghosted reference's note")
	}
	kept := f.compile("plain-markdown", publish.Options{Ghosts: "keep"})
	if !strings.Contains(string(kept.Body), "GHOSTNOTE") {
		t.Error("with ghosts kept the note should be there")
	}
}

// An option can be cleared: in a saved preset and for one compile.
func TestOptionsCanBeCleared(t *testing.T) {
	f := newFx(t, "Clear")
	f.doc("Ch", "chapter", "", "Body.")
	out, err := f.reg.Call(f.ctx, f.owner, "compile.preset.create", compile.PresetCreateIn{ProjectID: f.pid, Name: "Pen", Base: "shunn-us", Options: publish.Options{Byline: "Pen Name", HeaderTitle: "SHORT"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(compile.PresetItem).Item.ID
	// for one compile
	pen := f.compile(id, publish.Options{})
	if !strings.Contains(unzip(t, pen.Body)["word/document.xml"], "by Pen Name") {
		t.Fatal("the preset's byline is not used")
	}
	cleared := f.compile(id, publish.Options{Clear: []string{"byline"}})
	doc := unzip(t, cleared.Body)["word/document.xml"]
	if strings.Contains(doc, "Pen Name") || !strings.Contains(doc, "by Máire Ní Bhriain") {
		t.Error("the byline was not cleared for this compile")
	}
	// in the preset
	up, err := f.reg.Call(f.ctx, f.owner, "compile.preset.update", compile.PresetUpdateIn{ProjectID: f.pid, PresetID: id, Options: publish.Options{Clear: []string{"byline", "header_title"}}})
	if err != nil {
		t.Fatal(err)
	}
	if o := up.(compile.PresetItem).Item.Options; o.Byline != "" || o.HeaderTitle != "" || o.Font != "Times New Roman" || len(o.Clear) != 0 {
		t.Errorf("after clearing: %+v", o)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Options: publish.Options{Clear: []string{"font"}}}); err == nil {
		t.Error("clearing an option that cannot be cleared was accepted")
	}
}

// contact: false leaves the block off the title page, and the word count stays.
func TestContactCanBeLeftOffTheTitlePage(t *testing.T) {
	f := newFx(t, "Private")
	f.doc("Ch", "chapter", "", "Body words here.")
	f.setContact()
	no := false
	out := f.compile("shunn-us", publish.Options{Contact: &no})
	doc := unzip(t, out.Body)["word/document.xml"]
	for _, pii := range []string{"maire@example.com", "Quay Street", "+353"} {
		if strings.Contains(doc, pii) {
			t.Errorf("the title page still has %q", pii)
		}
	}
	if !strings.Contains(doc, "about ") || !strings.Contains(doc, "The Salt Road") && !strings.Contains(doc, "Private") {
		t.Errorf("the rest of the title page is gone")
	}
	if p := f.preview("shunn-us", publish.Options{Contact: &no}); len(p.TitlePage.Contact) != 0 {
		t.Errorf("the preview shows the contact: %v", p.TitlePage.Contact)
	}
}
