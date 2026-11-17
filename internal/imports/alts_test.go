package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/text"
)

const altsText = "The quiet house was empty, and the quiet stayed.\n\nShe waited by the old window. Nothing moved in the street.\n\nLast paragraph here.\n"

// The Markdown representation of alternatives survives an export and an
// import: the chosen text, every wording, the original, who and when are all
// there on the other side, under new ids.
func TestAlternativesSurviveExportAndImport(t *testing.T) {
	e := newEnv(t)
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Alts Book"}).(projects.Item).Item.ID
	d := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Chapter One", Content: altsText}).(docs.Item).Item
	mk := func(scope, quote string, variants ...string) docs.AltMut {
		cur := e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
		return e.must(e.w, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: cur.Revision, Scope: scope, Quote: quote, Occurrence: new(int), Variants: variants}).(docs.AltMut)
	}
	a := mk("word", "quiet", "still", "hushed -->")
	mk("sentence", "She waited by the old window.", "She stood at the window.")
	cur := e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
	e.must(e.w, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: a.Alternative.ID, VariantID: a.Alternative.Variants[1].ID})
	cur = e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
	e.must(e.w, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: cur.Revision, Ranges: []docs.Range{{Quote: "Last paragraph here."}}})
	cur = e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item

	// The Markdown export is the stored text, byte for byte, comment and tags included.
	exp := e.must(e.w, "export.markdown", export.MarkdownIn{ProjectID: pid, Mode: "bundle"}).(export.File)
	fs, err := imports.ReadZip(exp.Body)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range fs {
		if string(f.Data) == cur.Content {
			found = true
		}
	}
	if !found {
		t.Fatalf("the exported file is not the stored text:\n%q", cur.Content)
	}
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: fs, Name: "ignored"}).(imports.PreviewOut)
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: fs}).(imports.ApplyOut)
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	var got docs.Meta
	for _, m := range list.Items {
		if m.Title == "Chapter One" {
			got = m
		}
	}
	if got.ID == "" || got.ID == d.ID {
		t.Fatalf("imported document: %+v", got)
	}
	imp := e.must(e.w, "doc.get", docs.DocIn{ID: got.ID}).(docs.Item).Item
	if !strings.HasPrefix(imp.Content, "The still house was empty, and the quiet stayed.") {
		t.Fatalf("the chosen text came back: %q", imp.Content)
	}
	if imp.WordCount != cur.WordCount || !strings.Contains(imp.Content, "<scriobh-ghost>Last paragraph here.</scriobh-ghost>") {
		t.Errorf("word count %d vs %d; ghost tags kept", imp.WordCount, cur.WordCount)
	}
	src := e.must(e.w, "alternative.list", docs.AltListIn{ID: d.ID}).(docs.AltListOut)
	dst := e.must(e.w, "alternative.list", docs.AltListIn{ID: imp.ID}).(docs.AltListOut)
	if len(dst.Items) != 2 || dst.Active != 2 {
		t.Fatalf("alternatives after import: %+v", dst)
	}
	for i := range src.Items {
		s, g := src.Items[i], dst.Items[i]
		if s.Scope != g.Scope || s.Quote != g.Quote || s.Start != g.Start || s.End != g.End || len(s.Variants) != len(g.Variants) || s.ChosenPosition != g.ChosenPosition {
			t.Fatalf("alternative %d differs:\n%+v\n%+v", i, s, g)
		}
		if s.ID == g.ID {
			t.Error("ids are new")
		}
		for j := range s.Variants {
			sv, gv := s.Variants[j], g.Variants[j]
			if sv.Text != gv.Text || sv.Original != gv.Original || sv.Source != gv.Source || sv.CreatedAt != gv.CreatedAt || sv.ID == gv.ID {
				t.Fatalf("variant %d.%d differs:\n%+v\n%+v", i, j, sv, gv)
			}
		}
	}
	// And a second round trip is stable: the same Markdown but for the ids.
	norm := func(s string) string {
		items, err := alts.Parse(alts.Split(s).Line, alts.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		for i := range items {
			items[i].ID, items[i].Chosen = "x", "x"
			for j := range items[i].Variants {
				items[i].Variants[j].ID = "x"
			}
		}
		return alts.Split(s).Body + alts.Serialize(items)
	}
	if norm(imp.Content) != norm(cur.Content) {
		t.Errorf("the Markdown differs beyond ids:\n%s\n%s", imp.Content, cur.Content)
	}
}

func TestExportOptionForGhostedText(t *testing.T) {
	e := newEnv(t)
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Ghost Book"}).(projects.Item).Item.ID
	d := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "One", Content: altsText}).(docs.Item).Item
	cur := e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
	e.must(e.w, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: cur.Revision, Scope: "word", Quote: "window", Variants: []string{"pane"}})
	cur = e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
	e.must(e.w, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: cur.Revision, Ranges: []docs.Range{{Quote: "Nothing moved in the street."}, {Quote: "Last paragraph here."}}})
	cur = e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item

	read := func(in export.MarkdownIn) string {
		in.ProjectID = pid
		f := e.must(e.w, "export.markdown", in).(export.File)
		return string(f.Body)
	}
	keep := read(export.MarkdownIn{})
	if !strings.Contains(keep, "Nothing moved in the street.") || !strings.Contains(keep, "<scriobh-ghost>") || !strings.Contains(keep, alts.Split(cur.Content).Line) {
		t.Fatalf("ghosted text is exported by default, with the comment as stored:\n%s", keep)
	}
	omit := read(export.MarkdownIn{Ghosts: "omit"})
	if strings.Contains(omit, "Nothing moved") || strings.Contains(omit, "Last paragraph") || strings.Contains(omit, "scriobh-ghost") {
		t.Fatalf("omit leaves the ghosted text and its tags out:\n%s", omit)
	}
	if !strings.Contains(omit, "She waited by the old window. ") && !strings.Contains(omit, "She waited by the old window.\n") {
		t.Fatalf("the rest of the text stays:\n%s", omit)
	}
	line := alts.Split(strings.TrimSpace(omit[strings.Index(omit, "The quiet house"):])).Line
	items, err := alts.Parse(line, alts.Limits{})
	if err != nil || len(items) != 1 || items[0].Orphan {
		t.Fatalf("the comment is mended and its anchor still lands: %v %+v", err, items)
	}
	body := alts.Split(omit[strings.Index(omit, "The quiet house"):]).Body
	placed, _ := alts.Rebase("", body, items, nil, alts.Options{NewID: func(string) string { return "x" }})
	if placed[0].Orphan || placed[0].Start < 0 {
		t.Fatalf("the mended comment locates its text: %+v", placed)
	}
	if _, err := e.call(e.w, "export.markdown", export.MarkdownIn{ProjectID: pid, Ghosts: "maybe"}); err == nil {
		t.Error("an unknown option is refused")
	}
	// HTML: the tags are never shown; keep has the words, omit does not; the comment is never in it.
	h := e.must(e.w, "export.html", export.HTMLIn{ProjectID: pid}).(export.File)
	hs := string(h.Body)
	if strings.Contains(hs, "scriobh-ghost") || strings.Contains(hs, "scriobh:alts") || !strings.Contains(hs, "Nothing moved in the street.") {
		t.Fatalf("html keep: tags and comment must not show, words stay")
	}
	h = e.must(e.w, "export.html", export.HTMLIn{ProjectID: pid, Ghosts: "omit"}).(export.File)
	hs = string(h.Body)
	if strings.Contains(hs, "Nothing moved") || strings.Contains(hs, "scriobh-ghost") || !strings.Contains(hs, "She waited") {
		t.Fatal("html omit")
	}
	// Ghosted words are not in the counts the export reads.
	if text.WordCount(cur.Content) != cur.WordCount {
		t.Error("count")
	}
}

// A hand-written file imported from a folder: the comment is read into
// alternatives, and what is stored (text and tables) agree.
func TestImportingAHandWrittenComment(t *testing.T) {
	e := newEnv(t)
	short := "# Arrival\n\nThe quiet house was quiet.\n\n" + alts.Marker + `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["still","low"],"chosen":"quiet"}]} -->` + "\n"
	long := "# Second\n\nA quiet room.\n\n" + alts.Marker + `{"version":1,"items":[{"id":"alt_a","scope":"word","chosen":"v1","variants":[{"id":"v1","text":"quiet","original":true,"by":"usr_nobody"},{"id":"v2","text":"calm"}]}]} -->` + "\n"
	for i := 0; i < 2; i++ { // the same ids twice: a second project must not collide
		out := e.apply(files(map[string]string{"01-arrival.md": short, "02-second.md": long}), "", nil)
		list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
		seen := 0
		for _, m := range list.Items {
			d := e.must(e.w, "doc.get", docs.DocIn{ID: m.ID}).(docs.Item).Item
			if !strings.Contains(d.Content, alts.Marker) {
				continue
			}
			seen++
			var n int
			e.d.Read().QueryRow(`SELECT count(*) FROM alternatives WHERE document_id = ?`, d.ID).Scan(&n)
			if n != 1 {
				t.Errorf("%s: %d alternatives", m.Title, n)
			}
			list := e.must(e.w, "alternative.list", docs.AltListIn{ID: d.ID}).(docs.AltListOut)
			if len(list.Items) != 1 || list.Items[0].Status != "active" || len(list.Items[0].Variants) < 2 {
				t.Errorf("%s: %+v", m.Title, list)
			}
			// What is stored agrees with the tables: saving the text back changes nothing.
			cur := e.must(e.w, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, Content: &d.Content}).(docs.Item).Item
			if cur.Revision != d.Revision {
				t.Errorf("%s: the stored text is not canonical (saving it again wrote revision %d)", m.Title, cur.Revision)
			}
		}
		if seen != 2 {
			t.Fatalf("%d documents with alternatives", seen)
		}
	}
}

// Item 13: a clean Markdown export is possible: the chosen text alone, no comment.
func TestExportWithoutTheAlternativesComment(t *testing.T) {
	e := newEnv(t)
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Clean Book"}).(projects.Item).Item.ID
	d := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "One", Content: altsText}).(docs.Item).Item
	cur := e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
	m := e.must(e.w, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: cur.Revision, Scope: "word", Quote: "window", Variants: []string{"pane", "rejected glass"}}).(docs.AltMut)
	cur = e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
	e.must(e.w, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, Step: "next"})
	cur = e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
	e.must(e.w, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: cur.Revision, Ranges: []docs.Range{{Quote: "Last paragraph here."}}})

	read := func(in export.MarkdownIn) string {
		in.ProjectID = pid
		return string(e.must(e.w, "export.markdown", in).(export.File).Body)
	}
	full := read(export.MarkdownIn{})
	if !strings.Contains(full, "rejected glass") || !strings.Contains(full, alts.Marker) {
		t.Fatalf("by default the comment, with every wording, is exported:\n%s", full)
	}
	clean := read(export.MarkdownIn{Alternatives: "omit"})
	if strings.Contains(clean, "rejected glass") || strings.Contains(clean, "scriobh:alts") || strings.Contains(clean, `"variants"`) {
		t.Fatalf("no comment, so no rejected wording:\n%s", clean)
	}
	if !strings.Contains(clean, "the old pane.") {
		t.Fatalf("the chosen wording stays:\n%s", clean)
	}
	if !strings.Contains(clean, "<scriobh-ghost>Last paragraph here.</scriobh-ghost>") {
		t.Fatalf("ghosts keep their own option:\n%s", clean)
	}
	both := read(export.MarkdownIn{Alternatives: "omit", Ghosts: "omit"})
	if strings.Contains(both, "Last paragraph") || strings.Contains(both, "scriobh") {
		t.Fatalf("both options:\n%s", both)
	}
	// The bundle too.
	bundle := e.must(e.w, "export.markdown", export.MarkdownIn{ProjectID: pid, Mode: "bundle", Alternatives: "omit"}).(export.File)
	fs, err := imports.ReadZip(bundle.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if strings.Contains(string(f.Data), "scriobh:alts") || strings.Contains(string(f.Data), "rejected glass") {
			t.Errorf("%s still carries the comment", f.Path)
		}
	}
	if _, err := e.call(e.w, "export.markdown", export.MarkdownIn{ProjectID: pid, Alternatives: "some"}); err == nil {
		t.Error("an unknown value is refused")
	}
}

// Item 1 through the export: ghosted text never leaks, whatever the comment is.
func TestOmitGhostedTextWithAnUnusableComment(t *testing.T) {
	e := newEnv(t)
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Leaky Book"}).(projects.Item).Item.ID
	body := "The quiet house. <scriobh-ghost>A secret ghosted line.</scriobh-ghost> After.\n\n"
	for name, line := range map[string]string{
		"unparseable": alts.Marker + `{nope} -->`,
		"short form":  alts.Marker + `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["still"]}]} -->`,
	} {
		e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: name, Content: body + line + "\n"})
	}
	out := string(e.must(e.w, "export.markdown", export.MarkdownIn{ProjectID: pid, Ghosts: "omit"}).(export.File).Body)
	if strings.Contains(out, "secret ghosted") || strings.Contains(out, "scriobh-ghost") {
		t.Fatalf("ghosted text leaked:\n%s", out)
	}
	if strings.Count(out, "The quiet house.") != 2 {
		t.Fatalf("the rest stays:\n%s", out)
	}
}

// Item 3 and 10 for the importer: a comment that cannot be used is kept as
// text, with a warning, and the imported document saves.
func TestImportKeepsAnUnusableCommentAsTextWithAWarning(t *testing.T) {
	e := newEnv(t)
	bad := alts.Marker + `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["a\n\nb","still"]}]} -->`
	f := files(map[string]string{"01-odd.md": "# Odd\n\nThe quiet house.\n\n" + bad + "\n", "02-fine.md": "# Fine\n\nPlain.\n"})
	pv := e.preview(f, "")
	found := false
	for _, n := range pv.Nodes {
		for _, w := range n.Warnings {
			if strings.Contains(w, "alternatives comment") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the preview says the comment will not be used: %+v", pv.Nodes)
	}
	out := e.apply(f, "", nil)
	warned := false
	for _, w := range out.Warnings {
		if strings.Contains(w, "alternatives comment") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("the report says so too: %v", out.Warnings)
	}
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	for _, m := range list.Items {
		if m.Title != "Odd" {
			continue
		}
		d := e.must(e.w, "doc.get", docs.DocIn{ID: m.ID}).(docs.Item).Item
		if !strings.Contains(d.Content, `a\n\nb`) {
			t.Fatalf("kept as text: %q", d.Content)
		}
		c := strings.Replace(d.Content, "house", "house, house", 1)
		if _, err := e.call(e.w, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision, Content: &c}); err != nil {
			t.Fatalf("an imported document with such a comment must save: %v", err)
		}
	}
}

// A wording that is a phrase survives export and import as an active alternative.
func TestPhraseWordingSurvivesExportAndImport(t *testing.T) {
	e := newEnv(t)
	pid := e.must(e.w, "project.create", projects.CreateIn{Title: "Phrase Book"}).(projects.Item).Item.ID
	d := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "One", Content: altsText}).(docs.Item).Item
	cur := e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
	m := e.must(e.w, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: cur.Revision, Scope: "word", Quote: "window", Variants: []string{"big old window"}}).(docs.AltMut)
	cur = e.must(e.w, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item
	e.must(e.w, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, Step: "next"})
	exp := e.must(e.w, "export.markdown", export.MarkdownIn{ProjectID: pid, Mode: "bundle"}).(export.File)
	fs, err := imports.ReadZip(exp.Body)
	if err != nil {
		t.Fatal(err)
	}
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Files: fs, Name: "x"}).(imports.PreviewOut)
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Files: fs}).(imports.ApplyOut)
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	for _, mt := range list.Items {
		if mt.Title != "One" {
			continue
		}
		l := e.must(e.w, "alternative.list", docs.AltListIn{ID: mt.ID}).(docs.AltListOut)
		if l.Active != 1 || l.Orphaned != 0 || l.Items[0].Quote != "big old window" {
			t.Fatalf("a phrase wording after import: %+v", l)
		}
	}
}

// The configured limit decides what the importer reads, in the preview and in the apply.
func TestImportUsesTheConfiguredAlternativesLimit(t *testing.T) {
	e := newEnv(t)
	e.reg.Env().SetService(altstore.ServiceLimits, alts.Limits{PerDoc: 2})
	three := alts.Marker + `{"version":1,"items":[` +
		`{"scope":"word","quote":"one","variants":["1"]},{"scope":"word","quote":"two","variants":["2"]},{"scope":"word","quote":"three","variants":["3"]}]} -->`
	two := alts.Marker + `{"version":1,"items":[{"scope":"word","quote":"one","variants":["1"]},{"scope":"word","quote":"two","variants":["2"]}]} -->`
	f := files(map[string]string{"01-over.md": "# Over\n\none two three\n\n" + three + "\n", "02-within.md": "# Within\n\none two three\n\n" + two + "\n"})
	pv := e.preview(f, "")
	warned := map[string]bool{}
	for _, n := range pv.Nodes {
		for _, w := range n.Warnings {
			if strings.Contains(w, "alternatives comment") {
				warned[n.Title] = true
			}
		}
	}
	if !warned["Over"] || warned["Within"] {
		t.Fatalf("the preview judges by the configured limit: %v", warned)
	}
	out := e.apply(f, "", nil)
	list := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	counts := map[string]int{}
	for _, m := range list.Items {
		l := e.must(e.w, "alternative.list", docs.AltListIn{ID: m.ID}).(docs.AltListOut)
		counts[m.Title] = len(l.Items)
	}
	if counts["Over"] != 0 || counts["Within"] != 2 {
		t.Fatalf("alternatives made: %v", counts)
	}
}
