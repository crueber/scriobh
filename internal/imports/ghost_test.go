package imports_test

import (
	"os"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/text"
)

func jsonFile(t testing.TB, name string) []imports.File {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return []imports.File{{Path: name, Data: b, Size: int64(len(b))}}
}

func TestGhost4(t *testing.T) {
	e := newEnv(t)
	files := jsonFile(t, "ghost-4.json")
	pv := e.previewConv(imports.PreviewIn{Name: "ghost.json", Files: files})
	if pv.Layout != imports.LayoutGhost {
		t.Fatalf("layout %q", pv.Layout)
	}
	if pv.Project.Title != "Salt Journal" || pv.Project.Kind != "blog" || pv.Project.Author != "Ada Fixture" {
		t.Errorf("project %+v", pv.Project)
	}
	// oldest first: the one with a millisecond date (2021) then the Markdown one (2022)
	eq(t, outline(pv.Nodes), []string{"chapter:HTML Only", "chapter:Markdown Post"})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "ghost.json", Files: files})
	got := e.docContent(ap.ProjectID, "Markdown Post")
	for _, want := range []string{"# Heading one", "Some *emphasis* and **strong**, with a [link](https://example.org/x) and `code`.", "- item one\n- item two", "> quoted", "```\ncode block\n```", "* * *", "*\\[image: alt text\\]*"} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown post lacks %q:\n%s", want, got)
		}
	}
	// Nothing in the card's Markdown is a Scríobh construct.
	if strings.HasPrefix(got, "---") || strings.Contains(got, "onclick") || strings.Contains(got, "alert(1)") || strings.Contains(got, "scriobh-alts") && !strings.Contains(got, `\<`) {
		t.Errorf("foreign Markdown got through:\n%s", got)
	}
	d := text.Structure(got, text.StructOptions{})
	plain := text.PlainText(d.Nodes)
	if !strings.Contains(plain, "%% secret %%") || !strings.Contains(plain, "[[Wiki Link]]") {
		t.Errorf("literal text changed: %q", plain)
	}
	if links := text.WikiLinks(got); len(links) != 0 {
		t.Errorf("a wiki link was made: %+v", links)
	}
	// the document has no alternatives or ghosts from the card
	var n int
	e.d.Read().QueryRow(`SELECT count(*) FROM alternatives WHERE project_id = ?`, ap.ProjectID).Scan(&n)
	if n != 0 {
		t.Errorf("alternatives made from a card: %d", n)
	}
	e.d.Read().QueryRow(`SELECT count(*) FROM ghosts WHERE project_id = ?`, ap.ProjectID).Scan(&n)
	if n != 0 {
		t.Errorf("ghosts made from a card: %d", n)
	}
	var syn, date string
	e.d.Read().QueryRow(`SELECT synopsis_short, story_date FROM documents WHERE project_id = ? AND title = 'Markdown Post'`, ap.ProjectID).Scan(&syn, &date)
	if syn != "A post written in Markdown." || date != "2022-05-03" {
		t.Errorf("synopsis %q date %q", syn, date)
	}
	e.d.Read().QueryRow(`SELECT story_date FROM documents WHERE project_id = ? AND title = 'HTML Only'`, ap.ProjectID).Scan(&date)
	if date != "2021-01-01" {
		t.Errorf("a millisecond date: %q", date)
	}
	h := e.docContent(ap.ProjectID, "HTML Only")
	if !strings.Contains(h, "From the **html** field. [Link](https://example.org/)") || strings.Contains(h, "v.example") || strings.Contains(h, "ignored") {
		t.Errorf("html post:\n%s", h)
	}
	// What must never be read
	var meta string
	e.d.Read().QueryRow(`SELECT metadata_json FROM project_sources WHERE project_id = ?`, ap.ProjectID).Scan(&meta)
	for _, bad := range []string{"private.example", "sk_live", "$2a$10"} {
		if strings.Contains(meta, bad) || strings.Contains(got+h, bad) {
			t.Errorf("%q was read", bad)
		}
	}
	// options
	all := e.previewConv(imports.PreviewIn{Name: "g.json", Files: files, GhostStatus: "all", GhostTypes: "both"})
	eq(t, outline(all.Nodes), []string{"chapter:HTML Only", "chapter:Markdown Post", "chapter:A Draft", "chapter:Scheduled", "part:Pages", "  chapter:About"})
}

func TestGhost5Lexical(t *testing.T) {
	e := newEnv(t)
	files := jsonFile(t, "ghost-5.json")
	pv := e.previewConv(imports.PreviewIn{Name: "ghost.json", Files: files})
	if pv.Layout != imports.LayoutGhost || pv.Documents != 2 {
		t.Fatalf("%q %d", pv.Layout, pv.Documents)
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "ghost.json", Files: files})
	got := e.docContent(ap.ProjectID, "Lexical Post")
	for _, want := range []string{
		"## Lexical heading",
		"Plain, **bold** *italic* ***both*** ~~struck~~ `code`. [a link](https://example.org/lex)\\\nafter a break \\%\\% not a note \\[\\[x\\]\\]",
		"> A lexical quote", "- first\n- second\n    1. nested one", "* * *", "```\nprint('hi')\n```", "*\\[image: Lexical image\\]*", "With a *caption*",
		"[A bookmark](https://example.org/bm)", "*\\[embed\\]*", "*\\[image: g1\\]*", "An html card", "Card **markdown** with", "Careful",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("lexical post lacks %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"x()", "mystery", "video.example", "product"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q in the text:\n%s", bad, got)
		}
	}
	if plain := text.PlainText(text.Structure(got, text.StructOptions{}).Nodes); strings.Contains(plain, "a note") && !strings.Contains(plain, "%% a note %%") {
		t.Errorf("a card's %%%% became a private note: %q", plain)
	}
	if links := text.WikiLinks(got); len(links) != 0 {
		t.Errorf("wiki links: %+v", links)
	}
	// an empty lexical falls back to the html field
	if h := e.docContent(ap.ProjectID, "Empty Lexical"); !strings.Contains(h, "Only the html has text.") {
		t.Errorf("fallback: %q", h)
	}
	if pv.Report["ghost"] == nil {
		t.Errorf("no report")
	}
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "mystery-card") {
		t.Errorf("an unknown card was not reported: %v", pv.Warnings)
	}
}
