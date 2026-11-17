package imports_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

func notesBook(t testing.TB) []byte {
	note := func(n, text string) string {
		return `<p>Text<a epub:type="noteref" id="r` + n + `" href="#n` + n + `">` + n + `</a></p><aside epub:type="footnote" id="n` + n + `"><p>` + text + `</p></aside>`
	}
	return miniBook(t, "Mapping", []mbChapter{
		{file: "c1.xhtml", title: "One", body: `<h1>One</h1>` + note("1", "Note A")},
		{file: "c2.xhtml", title: "Two", body: `<h1>Two</h1>` + note("1", "Note B") + note("2", "Note C")},
		{file: "c3.xhtml", title: "Three", body: `<h1>Three</h1><p>Plain.</p>`},
		{file: "c4.xhtml", title: "Four", body: `<h1>Four</h1><p>Last.</p>`},
	})
}

func keyOf(pv imports.PreviewOut, title string) string {
	for _, n := range pv.Nodes {
		if n.Title == title {
			return n.Key
		}
	}
	return ""
}

func TestEpubMappingEdits(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := notesBook(t)
	pv := e.previewConv(imports.PreviewIn{Name: "map.epub", Epub: book})
	eq(t, outline(pv.Nodes), []string{"chapter:One", "chapter:Two", "chapter:Three", "chapter:Four"})
	k1, k2, k3, k4 := keyOf(pv, "One"), keyOf(pv, "Two"), keyOf(pv, "Three"), keyOf(pv, "Four")
	str := func(s string) *string { return &s }
	edits := []imports.Edit{
		{Key: k2, MergeInto: k1}, // Two joins One: its notes are numbered after One's
		{Key: k4, Title: str("The Last"), Kind: str("back_matter")},
		{Key: k3, Parent: str(k1)}, // Three becomes a scene of One
	}
	pv2 := e.must(e.w, "import.preview", imports.PreviewIn{Name: "map.epub", Epub: book, Edits: edits}).(imports.PreviewOut)
	eq(t, outline(pv2.Nodes), []string{"chapter:One", "  chapter:Three", "back_matter:The Last"})
	if pv2.Documents != 3 {
		t.Errorf("documents: %d", pv2.Documents)
	}
	ap := e.applyConv(pv2, imports.ApplyIn{Name: "map.epub", Epub: book, Edits: edits})
	var body string
	if err := e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = 'One'`, ap.ProjectID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Text[^1]", "Text[^2]", "Text[^3]", "[^1]: Note A", "[^2]: Note B", "[^3]: Note C"} {
		if !strings.Contains(body, want) {
			t.Errorf("the merged text lacks %q:\n%s", want, body)
		}
	}
	// The edition maps every chapter of the book as it was read, merged ones to the document they went into.
	ed := e.must(e.w, "edition.list", editions.ProjectIn{ProjectID: ap.ProjectID}).(editions.ListOut).Items[0]
	got := e.must(e.w, "edition.get", editions.EditionIn{ProjectID: ap.ProjectID, EditionID: ed.ID}).(editions.GetOut)
	if len(got.Chapters) != 4 {
		t.Fatalf("chapters: %+v", got.Chapters)
	}
	docOf := func(title string) string {
		var id string
		_ = e.d.Read().QueryRow(`SELECT id FROM documents WHERE project_id = ? AND title = ?`, ap.ProjectID, title).Scan(&id)
		return id
	}
	if got.Chapters[0].DocumentID != docOf("One") || got.Chapters[1].DocumentID != docOf("One") || got.Chapters[2].DocumentID != docOf("Three") || got.Chapters[3].DocumentID != docOf("The Last") {
		t.Errorf("document ids: %+v", got.Chapters)
	}
	// The baseline is what was read, not what the writer made of it.
	if b, _ := editions.Baseline(t.Context(), e.d.Read(), ed.ID, 2); strings.Contains(b, "Note A") || !strings.Contains(b, "[^1]: Note B") {
		t.Errorf("the baseline of Two: %s", b)
	}
	if got.Chapters[3].Title != "Four" || got.Chapters[3].Kind != "chapter" {
		t.Errorf("the chapter list follows the book, not the edits: %+v", got.Chapters[3])
	}
	// A skipped chapter is a chapter of the edition with no document.
	ap2 := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "map.epub", Epub: book, Edits: []imports.Edit{{Key: k3, Skip: true}}}).(imports.ApplyOut)
	ed2 := e.must(e.w, "edition.list", editions.ProjectIn{ProjectID: ap2.ProjectID}).(editions.ListOut).Items[0]
	got2 := e.must(e.w, "edition.get", editions.EditionIn{ProjectID: ap2.ProjectID, EditionID: ed2.ID}).(editions.GetOut)
	if got2.Chapters[2].DocumentID != "" || got2.Chapters[2].Title != "Three" || got2.Chapters[1].DocumentID == "" {
		t.Errorf("skipped chapter: %+v", got2.Chapters)
	}
	_ = k4
	_ = docs.Kinds
}

func TestEpubMappingRefusals(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := notesBook(t)
	pv := e.previewConv(imports.PreviewIn{Name: "map.epub", Epub: book})
	k1, k2 := keyOf(pv, "One"), keyOf(pv, "Two")
	str := func(s string) *string { return &s }
	bad := map[string][]imports.Edit{
		"a key that is not in the book":       {{Key: "manuscript/99-elsewhere.md", Title: str("X")}},
		"a merge into a key that is not":      {{Key: k1, MergeInto: "nope"}},
		"a merge into itself":                 {{Key: k1, MergeInto: k1}},
		"a merge with another field":          {{Key: k1, MergeInto: k2, Title: str("Y")}},
		"a move to a key that is not":         {{Key: k1, Parent: str("nope")}},
		"a move into itself":                  {{Key: k1, Parent: str(k1)}},
		"a move into what is inside it":       {{Key: k1, Parent: str(k2)}, {Key: k2, Parent: str(k1)}},
		"a merge of what was merged":          {{Key: k2, MergeInto: k1}, {Key: k2, Title: str("Z")}},
		"a merge into what is left out":       {{Key: k2, Skip: true}, {Key: k1, MergeInto: k2}},
		"a title that is empty":               {{Key: k1, Title: str("")}},
		"a reorder with a key from elsewhere": nil,
	}
	for name, edits := range bad {
		if edits == nil {
			continue
		}
		if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "map.epub", Epub: book, Edits: edits}); err == nil {
			t.Errorf("%s was accepted by the preview", name)
		}
		if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "map.epub", Epub: book, Edits: edits}); err == nil {
			t.Errorf("%s was accepted by the apply", name)
		}
	}
	// A mapping made for another book does not apply to this one, whatever its keys happen to be.
	other := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: salt3(t)})
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: other.Token, Name: "map.epub", Epub: book}); err == nil || ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("another book's token: %v", err)
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "salt.epub", Epub: salt3(t), Edits: []imports.Edit{{Key: k1, Title: str("Mine")}}}); err == nil {
		t.Error("this book's keys applied to another book")
	}
	if n := count(t, e, `SELECT count(*) FROM projects`); n != 0 {
		t.Errorf("a refused mapping created %d projects", n)
	}
	// Reordering: the chapters' order is the writer's.
	k3 := keyOf(pv, "Three")
	rp := e.must(e.w, "import.preview", imports.PreviewIn{Name: "map.epub", Epub: book, Reorder: []imports.Reorder{{Keys: []string{k3, k2}}}}).(imports.PreviewOut)
	eq(t, outline(rp.Nodes), []string{"chapter:Three", "chapter:Two", "chapter:One", "chapter:Four"})
}

// permute calls f with every order of the edits.
func permute(edits []imports.Edit, f func([]imports.Edit)) {
	var rec func(a []imports.Edit, k int)
	rec = func(a []imports.Edit, k int) {
		if k == len(a) {
			f(append([]imports.Edit(nil), a...))
			return
		}
		for i := k; i < len(a); i++ {
			a[k], a[i] = a[i], a[k]
			rec(a, k+1)
			a[k], a[i] = a[i], a[k]
		}
	}
	rec(append([]imports.Edit(nil), edits...), 0)
}

// The mapping a set of edits makes does not depend on the order they are sent in: a merge into a chapter that is left
// out (itself, or by a part it is in) is refused whichever came first, a chain of merges is the same chain, and what
// is accepted gives the same chapters in the same order.
func TestEpubMappingDoesNotDependOnTheOrderOfTheEdits(t *testing.T) {
	e, _ := newEpubEnv(t)
	book := salt3(t)
	pv := e.previewConv(imports.PreviewIn{Name: "salt.epub", Epub: book})
	k := func(title string) string {
		key := keyOf(pv, title)
		if key == "" {
			t.Fatalf("no chapter %q in %v", title, outline(pv.Nodes))
		}
		return key
	}
	str := func(s string) *string { return &s }
	road, wall, part, ded, epi, ack := k("The Road"), k("The Wall"), k("Part One: The Marsh"), k("Dedication"), k("Epilogue"), k("Acknowledgments")
	cases := []struct {
		name  string
		edits []imports.Edit
		want  bool // accepted
	}{
		{"a merge into a chapter that is left out", []imports.Edit{{Key: wall, Skip: true}, {Key: road, MergeInto: wall}}, false},
		{"a merge of a chapter that is left out", []imports.Edit{{Key: road, Skip: true}, {Key: road, MergeInto: wall}}, false},
		{"a merge into a chapter in a part that is left out", []imports.Edit{{Key: part, Skip: true}, {Key: ded, MergeInto: wall}}, false},
		{"a merge into a part that is left out", []imports.Edit{{Key: part, Skip: true}, {Key: ded, MergeInto: part}}, false},
		{"a move into a chapter that is left out", []imports.Edit{{Key: wall, Skip: true}, {Key: ded, Parent: str(wall)}}, false},
		{"a move into a part that is left out, onto a chapter inside it", []imports.Edit{{Key: part, Skip: true}, {Key: ded, Parent: str(road)}}, false},
		{"a chain of merges, the last target left out", []imports.Edit{{Key: road, MergeInto: wall}, {Key: wall, MergeInto: epi}, {Key: epi, Skip: true}}, false},
		{"a loop of merges", []imports.Edit{{Key: road, MergeInto: wall}, {Key: wall, MergeInto: road}}, false},
		{"a chain of merges", []imports.Edit{{Key: road, MergeInto: wall}, {Key: wall, MergeInto: epi}}, true},
		{"a move into a chapter that is merged away", []imports.Edit{{Key: ack, Parent: str(epi)}, {Key: epi, MergeInto: road}}, true},
		{"a merge, a move and a rename", []imports.Edit{{Key: road, MergeInto: wall}, {Key: ack, Parent: str(wall)}, {Key: ded, Title: str("Dedicated")}}, true},
		{"a skip of a chapter nothing is merged into", []imports.Edit{{Key: epi, Skip: true}, {Key: road, MergeInto: wall}}, true},
	}
	for _, c := range cases {
		var first string
		n := 0
		permute(c.edits, func(edits []imports.Edit) {
			n++
			out, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "salt.epub", Epub: book, Edits: edits})
			if (err == nil) != c.want {
				t.Errorf("%s: accepted=%v in the order %v, want %v (%v)", c.name, err == nil, edits, c.want, err)
				return
			}
			if err != nil {
				return
			}
			got := strings.Join(outline(out.(imports.PreviewOut).Nodes), "|") + fmt.Sprint(out.(imports.PreviewOut).Documents, out.(imports.PreviewOut).Words)
			if first == "" {
				first = got
			} else if got != first {
				t.Errorf("%s: the order of the edits changed the result:\n%s\n%s", c.name, first, got)
			}
			// the apply makes what the preview said, and refuses what it refused
			if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "salt.epub", Epub: book, Edits: edits}); err != nil {
				t.Errorf("%s: the apply refused what the preview accepted: %v", c.name, err)
			}
		})
		if n < 2 {
			t.Errorf("%s: only %d order(s) tried", c.name, n)
		}
	}
}
