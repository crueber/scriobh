package search_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type fx struct {
	t        testing.TB
	d        *db.DB
	reg      *ops.Registry
	ctx      context.Context
	a, b     ops.Principal // owners of two projects
	reader   ops.Principal
	outsider ops.Principal
	pidA     string
	pidB     string
}

func newFx(t testing.TB) *fx {
	t.Helper()
	d := testutil.DB(t)
	f := &fx{t: t, d: d, ctx: context.Background(), reg: registry.Build(d, nil, t.TempDir(), slog.New(slog.NewTextHandler(testWriter{t}, nil)))}
	f.a = ops.Principal{UserID: testutil.User(t, d, "writer", "a@t.local"), Role: ops.RoleWriter}
	f.b = ops.Principal{UserID: testutil.User(t, d, "writer", "b@t.local"), Role: ops.RoleWriter}
	f.reader = ops.Principal{UserID: testutil.User(t, d, "reader", "r@t.local"), Role: ops.RoleReader}
	f.outsider = ops.Principal{UserID: testutil.User(t, d, "writer", "o@t.local"), Role: ops.RoleWriter}
	f.pidA = f.project(f.a, "Alpha")
	f.pidB = f.project(f.b, "Beta")
	if _, err := d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, f.pidA, f.reader.UserID, "reader", db.Now()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fx) project(p ops.Principal, title string) string {
	out, err := f.reg.Call(f.ctx, p, "project.create", projects.CreateIn{Title: title})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(projects.Item).Item.ID
}

func (f *fx) doc(p ops.Principal, pid string, in docs.CreateIn) docs.Doc {
	f.t.Helper()
	in.ProjectID = pid
	out, err := f.reg.Call(f.ctx, p, "doc.create", in)
	if err != nil {
		f.t.Fatalf("create %q: %v", in.Title, err)
	}
	return out.(docs.Item).Item
}

func (f *fx) search(p ops.Principal, pid, q string, limit, offset int) (search.QueryOut, error) {
	f.t.Helper()
	out, err := f.reg.Call(f.ctx, p, "search.query", search.QueryIn{ProjectID: pid, Q: q, Limit: limit, Offset: offset})
	if err != nil {
		return search.QueryOut{}, err
	}
	return out.(search.QueryOut), nil
}

func (f *fx) mustSearch(p ops.Principal, pid, q string) search.QueryOut {
	f.t.Helper()
	out, err := f.search(p, pid, q, 0, 0)
	if err != nil {
		f.t.Fatalf("search %q: %v", q, err)
	}
	return out
}

func titles(out search.QueryOut) []string {
	var ts []string
	for _, h := range out.Items {
		ts = append(ts, h.Title)
	}
	return ts
}

func TestRankTitleBeatsSynopsisBeatsBody(t *testing.T) {
	f := newFx(t)
	body := f.doc(f.a, f.pidA, docs.CreateIn{Title: "Notes", Content: "The lighthouse stood above the rocks and the lighthouse keeper slept."})
	syn := f.doc(f.a, f.pidA, docs.CreateIn{Title: "Interlude", SynopsisShort: "Where the lighthouse is first seen", Content: "Fog."})
	title := f.doc(f.a, f.pidA, docs.CreateIn{Title: "The Lighthouse", Content: "Nothing about it here."})
	out := f.mustSearch(f.a, f.pidA, "lighthouse")
	if got := titles(out); len(got) != 3 || got[0] != title.Title || got[1] != syn.Title || got[2] != body.Title {
		t.Fatalf("order = %v, want title, synopsis, body", got)
	}
	for i, want := range []string{"title", "synopsis", "body"} {
		if out.Items[i].MatchedIn != want {
			t.Errorf("item %d matched_in = %q, want %q", i, out.Items[i].MatchedIn, want)
		}
	}
	if out.Total != 3 || out.NextOffset != 0 || out.Degraded {
		t.Errorf("total %d next %d degraded %v", out.Total, out.NextOffset, out.Degraded)
	}
}

func TestRecencyBreaksTies(t *testing.T) {
	f := newFx(t)
	first := f.doc(f.a, f.pidA, docs.CreateIn{Title: "One", Content: "pilgrim"})
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Two", Content: "pilgrim"})
	third := f.doc(f.a, f.pidA, docs.CreateIn{Title: "Three", Content: "pilgrim"})
	// Touch the first so it is the most recently updated; same text, same rank.
	body := "pilgrim"
	if _, err := f.reg.Call(f.ctx, f.a, "doc.update", docs.UpdateIn{ID: first.ID, BaseRevision: 1, Title: ptr("One!"), Content: &body}); err != nil {
		t.Fatal(err)
	}
	got := titles(f.mustSearch(f.a, f.pidA, "pilgrim"))
	if len(got) != 3 || got[0] != "One!" || got[len(got)-1] == "One!" || !contains(got, third.Title) {
		t.Fatalf("order = %v, want the most recently updated first", got)
	}
}

func ptr[T any](v T) *T { return &v }

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestDiacriticsAndCaseAreFolded(t *testing.T) {
	f := newFx(t)
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "People", Content: "Máire met Seán at the Cúil."})
	for _, q := range []string{"maire", "MAIRE", "máire", "sean", "cuil", "Máire Seán"} {
		if out := f.mustSearch(f.a, f.pidA, q); out.Total != 1 {
			t.Errorf("%q found %d documents, want 1", q, out.Total)
		}
	}
}

func TestLastWordIsAPrefix(t *testing.T) {
	f := newFx(t)
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Docks", Content: "The harbour was quiet. A harbinger of rain."})
	if out := f.mustSearch(f.a, f.pidA, "harb"); out.Total != 1 {
		t.Errorf("a prefix found %d", out.Total)
	}
	// Only the last word is a prefix: an earlier fragment must be a whole word.
	if out := f.mustSearch(f.a, f.pidA, "harb quiet"); out.Total != 0 {
		t.Errorf("an earlier word matched as a prefix: %d", out.Total)
	}
	if out := f.mustSearch(f.a, f.pidA, "harbour quie"); out.Total != 1 {
		t.Errorf("whole word then prefix found %d", out.Total)
	}
}

func TestHostileInputNeverReachesFTSSyntax(t *testing.T) {
	f := newFx(t)
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "x marks", Content: "one two three near the title and not or and"})
	hostile := []string{
		`" OR * NOT ( ) ^ :`, `"`, `""`, `"""`, `a" OR "b`, `NEAR(a b)`, `NEAR(one two, 5)`, `title:x`, `{title body}: x`, `-title:x`, `^one`, `one NOT two`, `one AND`, `AND`, `OR`,
		`(one`, `one)`, `one*two`, `col:`, `:`, `rowid:1`, `x'; DROP TABLE doc_text; --`, "one\x00two", `\`, `\"`, `a**b`, `+one`, `one + two`,
		strings.Repeat("a ", 100), strings.Repeat("(", 500), `*`, `* * *`, "́", "👍 one",
	}
	for _, q := range hostile {
		out, err := f.search(f.a, f.pidA, q, 0, 0)
		if err != nil {
			// Nothing to search for is the one allowed refusal, and it is a clean 422.
			if e := ops.AsError(err); e.Code != ops.CodeInvalid {
				t.Errorf("%q: %v", q, err)
			}
			continue
		}
		if out.Degraded {
			t.Errorf("%q fell back to the substring search: user text reached the index syntax", q)
		}
	}
	// Words that look like operators or column names are just words.
	if out := f.mustSearch(f.a, f.pidA, "title:x"); out.Total != 1 {
		t.Errorf("`title:x` is the words title and x, which this document has: found %d", out.Total)
	}
	if out := f.mustSearch(f.a, f.pidA, "one NOT zzz"); out.Total != 0 {
		t.Errorf("NOT must not be an operator: found %d", out.Total)
	}
	if out := f.mustSearch(f.a, f.pidA, "and or not"); out.Total != 1 {
		t.Errorf("operator words are ordinary words: found %d", out.Total)
	}
	for _, q := range []string{"", "   ", "***", `" ( ) ^ :`, "́"} {
		if _, err := f.search(f.a, f.pidA, q, 0, 0); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%q should be refused as invalid, got %v", q, err)
		}
	}
	// A document and the index survive all of that.
	if out := f.mustSearch(f.a, f.pidA, "marks"); out.Total != 1 {
		t.Errorf("the index changed: %d", out.Total)
	}
}

func TestTrashedDocumentsNeverAppear(t *testing.T) {
	f := newFx(t)
	keep := f.doc(f.a, f.pidA, docs.CreateIn{Title: "Kept", Content: "needle in a haystack"})
	gone := f.doc(f.a, f.pidA, docs.CreateIn{Title: "Gone", Content: "needle in the bin"})
	folder := f.doc(f.a, f.pidA, docs.CreateIn{Title: "Folder", Kind: "part"})
	child := f.doc(f.a, f.pidA, docs.CreateIn{Title: "Child", ParentID: folder.ID, Content: "needle in a folder"})
	if out := f.mustSearch(f.a, f.pidA, "needle"); out.Total != 3 {
		t.Fatalf("before trash: %d", out.Total)
	}
	for _, id := range []string{gone.ID, folder.ID} {
		if _, err := f.reg.Call(f.ctx, f.a, "doc.trash", docs.DocIn{ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	out := f.mustSearch(f.a, f.pidA, "needle")
	if out.Total != 1 || out.Items[0].DocumentID != keep.ID {
		t.Fatalf("after trash: %v (child %s must go with its folder)", titles(out), child.ID)
	}
	if _, err := f.reg.Call(f.ctx, f.a, "doc.restore", docs.DocIn{ID: gone.ID}); err != nil {
		t.Fatal(err)
	}
	if out := f.mustSearch(f.a, f.pidA, "needle"); out.Total != 2 {
		t.Errorf("after restore: %d", out.Total)
	}
}

func TestOtherProjectsAreNeverSearched(t *testing.T) {
	f := newFx(t)
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Mine", Content: "a secret word: zeppelin"})
	f.doc(f.b, f.pidB, docs.CreateIn{Title: "Theirs", Content: "their zeppelin and their zeppelins"})
	for _, tc := range []struct {
		p       ops.Principal
		pid     string
		want    string
		wantTot int
	}{{f.a, f.pidA, "Mine", 1}, {f.b, f.pidB, "Theirs", 1}} {
		out := f.mustSearch(tc.p, tc.pid, "zeppelin")
		if out.Total != tc.wantTot || out.Items[0].Title != tc.want {
			t.Errorf("project %s: %v total %d", tc.pid, titles(out), out.Total)
		}
	}
}

func TestAccess(t *testing.T) {
	f := newFx(t)
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Open", Content: "readable text"})
	if out := f.mustSearch(f.reader, f.pidA, "readable"); out.Total != 1 {
		t.Errorf("a reader must be able to search: %d", out.Total)
	}
	if _, err := f.search(f.outsider, f.pidA, "readable", 0, 0); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("an outsider must get not_found, got %v", err)
	}
	if _, err := f.search(f.outsider, "prj_00000000000000000000000000", "readable", 0, 0); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a missing project must look the same, got %v", err)
	}
	if _, err := f.search(ops.Principal{}, f.pidA, "readable", 0, 0); ops.AsError(err).Code != ops.CodeUnauthenticated {
		t.Errorf("anonymous: %v", err)
	}
}

func TestSnippetsAreEscaped(t *testing.T) {
	f := newFx(t)
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "<b>Bold</b> title", SynopsisShort: "a <i>synopsis</i> & more",
		Content: "Before &lt;script&gt;alert(1)&lt;/script&gt; the marker shows &lt;img src=x onerror=alert(2)&gt; and \"quotes\" &amp; ampersands, <b>raw</b> <script>gone</script>."})
	out := f.mustSearch(f.a, f.pidA, "marker")
	if len(out.Items) != 1 {
		t.Fatal(titles(out))
	}
	h := out.Items[0]
	if strings.Contains(h.Snippet, "<b>") || strings.Contains(h.Snippet, "gone") && strings.Contains(h.Snippet, "<script>gone") {
		t.Errorf("raw HTML in the document is not text and must not be indexed: %q", h.Snippet)
	}
	for _, bad := range []string{"<script", "<img", "onerror=alert(2)>", "<b>", "<i>"} {
		if strings.Contains(h.SnippetHTML, bad) {
			t.Errorf("snippet_html holds markup %q: %s", bad, h.SnippetHTML)
		}
	}
	if !strings.Contains(h.SnippetHTML, "<mark>marker</mark>") {
		t.Errorf("the match is not marked: %s", h.SnippetHTML)
	}
	if !strings.Contains(h.SnippetHTML, "&lt;script&gt;") {
		t.Errorf("the script tag is not shown as text: %s", h.SnippetHTML)
	}
	if strings.Contains(h.Snippet, "<mark>") || strings.ContainsAny(h.Snippet, "\ue000\ue001") {
		t.Errorf("the plain snippet carries markup: %q", h.Snippet)
	}
	if !strings.Contains(h.Snippet, "marker") {
		t.Errorf("the plain snippet lost the match: %q", h.Snippet)
	}
	// A title that is markup is data too.
	out = f.mustSearch(f.a, f.pidA, "bold")
	if len(out.Items) != 1 || strings.Contains(out.Items[0].SnippetHTML, "<b>") || !strings.Contains(out.Items[0].SnippetHTML, "&lt;b&gt;") {
		t.Errorf("title snippet: %+v", out.Items)
	}
}

func TestADocumentCannotForgeAHighlight(t *testing.T) {
	f := newFx(t)
	// Private-use characters in the text, in the title and in the synopsis.
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Sage \ue000\ue001 Title", SynopsisShort: "syn \ue000 x", Content: "x \ue000FORGED\ue001 y target z \ue000 \ue001"})
	out := f.mustSearch(f.a, f.pidA, "target")
	if len(out.Items) != 1 {
		t.Fatal(titles(out))
	}
	if n := strings.Count(out.Items[0].SnippetHTML, "<mark>"); n != 1 {
		t.Errorf("%d highlights in %q, want exactly the real one", n, out.Items[0].SnippetHTML)
	}
	if strings.Contains(out.Items[0].SnippetHTML, "<mark>FORGED") {
		t.Errorf("a forged highlight got through: %s", out.Items[0].SnippetHTML)
	}
	// A title with the characters in it is still a title match, shown as text.
	out = f.mustSearch(f.a, f.pidA, "sage")
	if len(out.Items) != 1 || out.Items[0].MatchedIn != "title" {
		t.Errorf("title search: %+v", out.Items)
	} else if strings.Count(out.Items[0].SnippetHTML, "<mark>") != 1 {
		t.Errorf("title snippet: %s", out.Items[0].SnippetHTML)
	}
}

func TestOffsetPointsAtTheFirstBodyMatch(t *testing.T) {
	f := newFx(t)
	body := "Intro line.\n\nMáire walked down to the quay where the boats waited."
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Walk", Content: body})
	out := f.mustSearch(f.a, f.pidA, "quay")
	if len(out.Items) != 1 {
		t.Fatal(titles(out))
	}
	plain := "Intro line.\nMáire walked down to the quay where the boats waited."
	want := len([]rune(plain[:strings.Index(plain, "quay")]))
	if out.Items[0].Offset != want {
		t.Errorf("offset = %d, want %d (in runes, in the plain text)", out.Items[0].Offset, want)
	}
	// Title-only matches have no body position.
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Zephyr", Content: "calm"})
	out = f.mustSearch(f.a, f.pidA, "zephyr")
	if len(out.Items) != 1 || out.Items[0].Offset != 0 || out.Items[0].MatchedIn != "title" {
		t.Errorf("%+v", out.Items)
	}
}

func TestPrivateNotesAndCodeAreNotIndexed(t *testing.T) {
	f := newFx(t)
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Draft", Content: "Plain words here.\n\n%%a hidden aside%%\n\n```\ncodeonly\n```\n\nSee [[Old Quay|the quay]]."})
	for q, want := range map[string]int{"plain": 1, "hidden": 0, "aside": 0, "codeonly": 0, "quay": 1, "Old": 0} {
		if out := f.mustSearch(f.a, f.pidA, q); out.Total != want {
			t.Errorf("%q: %d, want %d", q, out.Total, want)
		}
	}
}

func TestPaging(t *testing.T) {
	f := newFx(t)
	for i := 0; i < 7; i++ {
		f.doc(f.a, f.pidA, docs.CreateIn{Title: fmt.Sprintf("Doc %d", i), Content: "marmalade"})
	}
	seen := map[string]bool{}
	var offset, pages int
	for {
		out, err := f.search(f.a, f.pidA, "marmalade", 3, offset)
		if err != nil {
			t.Fatal(err)
		}
		if out.Total != 7 {
			t.Fatalf("total %d on page %d, want an honest 7 every time", out.Total, pages)
		}
		for _, h := range out.Items {
			if seen[h.DocumentID] {
				t.Errorf("%s appears on two pages", h.Title)
			}
			seen[h.DocumentID] = true
		}
		pages++
		if out.NextOffset == 0 {
			break
		}
		if out.NextOffset != offset+len(out.Items) {
			t.Fatalf("next_offset %d after offset %d with %d items", out.NextOffset, offset, len(out.Items))
		}
		offset = out.NextOffset
	}
	if pages != 3 || len(seen) != 7 {
		t.Errorf("%d pages, %d distinct documents", pages, len(seen))
	}
	if out, _ := f.search(f.a, f.pidA, "marmalade", 3, 50); len(out.Items) != 0 || out.Total != 7 || out.NextOffset != 0 {
		t.Errorf("past the end: %+v", out)
	}
	if _, err := f.search(f.a, f.pidA, "marmalade", 3, -1); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("negative offset: %v", err)
	}
	// The limit is clamped, not refused.
	for i := 0; i < 60; i++ {
		f.doc(f.a, f.pidA, docs.CreateIn{Title: fmt.Sprintf("More %d", i), Content: "marmalade"})
	}
	if out, _ := f.search(f.a, f.pidA, "marmalade", 500, 0); len(out.Items) != 50 || out.Total != 67 || out.NextOffset != 50 {
		t.Errorf("limit 500: %d items, total %d, next %d", len(out.Items), out.Total, out.NextOffset)
	}
	if out, _ := f.search(f.a, f.pidA, "marmalade", 0, 0); len(out.Items) != 20 {
		t.Errorf("default limit: %d items", len(out.Items))
	}
}

func TestDegradedSearchWhenTheIndexIsGone(t *testing.T) {
	f := newFx(t)
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Alpha", Content: "A lantern in the &lt;window&gt; at night, 100% certain_ish."})
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Lantern Beta", Content: "Nothing relevant."})
	f.doc(f.b, f.pidB, docs.CreateIn{Title: "Other", Content: "another lantern"})
	gone := f.doc(f.a, f.pidA, docs.CreateIn{Title: "Trashed", Content: "lantern in the bin"})
	if _, err := f.reg.Call(f.ctx, f.a, "doc.trash", docs.DocIn{ID: gone.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Writer().Exec(`DROP TABLE doc_fts`); err != nil {
		t.Fatal(err)
	}
	out := f.mustSearch(f.a, f.pidA, "lantern")
	if !out.Degraded {
		t.Fatal("expected the degraded flag")
	}
	if out.Total != 2 || len(out.Items) != 2 {
		t.Fatalf("degraded results: %v total %d (trashed and other-project documents must stay out)", titles(out), out.Total)
	}
	for _, h := range out.Items {
		if h.MatchedIn == "" || h.Snippet == "" || !strings.Contains(h.SnippetHTML, "<mark>") {
			t.Errorf("degraded hit lacks a snippet: %+v", h)
		}
	}
	// Escaping holds on the fallback too, and LIKE wildcards are only text.
	out = f.mustSearch(f.a, f.pidA, "window")
	if len(out.Items) != 1 || strings.Contains(out.Items[0].SnippetHTML, "<window>") || !strings.Contains(out.Items[0].SnippetHTML, "&lt;<mark>window</mark>&gt;") {
		t.Errorf("degraded escaping: %+v", out.Items)
	}
	if out := f.mustSearch(f.a, f.pidA, "100% certain_ish"); !out.Degraded {
		t.Error("still degraded")
	}
	if out := f.mustSearch(f.a, f.pidA, "zzz"); out.Total != 0 || !out.Degraded {
		t.Errorf("no match: %+v", out)
	}
	// Access control does not depend on the index.
	if _, err := f.search(f.outsider, f.pidA, "lantern", 0, 0); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("outsider: %v", err)
	}
}

// Lower-casing changes the byte length of some letters (the Kelvin sign shrinks,
// a dotted capital I grows); the degraded search must still mark the right text.
func TestDegradedSearchMarksTheRightTextAroundOddCapitals(t *testing.T) {
	f := newFx(t)
	body := "KKK İstanbul İ İ İ and the lantern waited. A Lantern again."
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Odd", Content: body})
	if _, err := f.d.Writer().Exec(`DROP TABLE doc_fts`); err != nil {
		t.Fatal(err)
	}
	out := f.mustSearch(f.a, f.pidA, "LANTERN")
	if !out.Degraded || len(out.Items) != 1 {
		t.Fatalf("%+v", out)
	}
	h := out.Items[0]
	if !strings.Contains(h.SnippetHTML, "<mark>lantern</mark>") || strings.Count(h.SnippetHTML, "<mark>") != 1 {
		t.Errorf("snippet_html %q", h.SnippetHTML)
	}
	if !strings.Contains(h.Snippet, "İstanbul") {
		t.Errorf("snippet %q", h.Snippet)
	}
	want := len([]rune(body[:strings.Index(body, "lantern")]))
	if h.Offset != want {
		t.Errorf("offset %d, want %d", h.Offset, want)
	}
}

func TestRebuildRepairsAnIndexThatDrifted(t *testing.T) {
	f := newFx(t)
	one := f.doc(f.a, f.pidA, docs.CreateIn{Title: "One", Content: "alpha beta %%private%% gamma"})
	f.doc(f.b, f.pidB, docs.CreateIn{Title: "Two", Content: "delta"})
	// Damage the derived data: stale text, a missing row, an orphaned index entry.
	for _, q := range []string{
		`UPDATE doc_text SET body = 'stale words' WHERE document_id = '` + one.ID + `'`,
		`DELETE FROM doc_text WHERE project_id = '` + f.pidB + `'`,
	} {
		if _, err := f.d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if out := f.mustSearch(f.a, f.pidA, "alpha"); out.Total != 0 {
		t.Fatalf("setup: the stale text should hide the document (%d)", out.Total)
	}
	var reports []int
	if err := search.Rebuild(f.ctx, f.d, func(done, total int) { reports = append(reports, done, total) }); err != nil {
		t.Fatal(err)
	}
	if len(reports) == 0 || reports[len(reports)-1] != 4 {
		t.Errorf("progress %v: want a final report with the total of 4 documents (two plus the seeded first pages)", reports)
	}
	if out := f.mustSearch(f.a, f.pidA, "alpha"); out.Total != 1 {
		t.Errorf("after rebuild: %d", out.Total)
	}
	if out := f.mustSearch(f.a, f.pidA, "stale"); out.Total != 0 {
		t.Errorf("stale text survived: %d", out.Total)
	}
	if out := f.mustSearch(f.b, f.pidB, "delta"); out.Total != 1 {
		t.Errorf("the deleted row did not come back: %d", out.Total)
	}
	if out := f.mustSearch(f.a, f.pidA, "private"); out.Total != 0 {
		t.Errorf("a private note was indexed by the rebuild: %d", out.Total)
	}
	// The triggers are back: a normal save is searchable again straight away.
	f.doc(f.a, f.pidA, docs.CreateIn{Title: "Fresh", Content: "ultraviolet"})
	if out := f.mustSearch(f.a, f.pidA, "ultraviolet"); out.Total != 1 {
		t.Errorf("triggers were not restored: %d", out.Total)
	}
	var n int
	if err := f.d.Read().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'doc_text'`).Scan(&n); err != nil || n != 3 {
		t.Errorf("triggers on doc_text: %d %v", n, err)
	}
}

func TestRebuildExplainsAMissingIndex(t *testing.T) {
	f := newFx(t)
	if _, err := f.d.Writer().Exec(`DROP TABLE doc_fts`); err != nil {
		t.Fatal(err)
	}
	err := search.Rebuild(f.ctx, f.d, nil)
	if err == nil || !strings.Contains(err.Error(), "doc_fts") {
		t.Errorf("got %v", err)
	}
}

// testWriter sends log lines to the test log, so a fallback shows its reason.
type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func TestMoreThanTheWordLimitIsRefusedNotTruncated(t *testing.T) {
	f := newFx(t)
	if _, err := f.search(f.a, f.pidA, strings.Repeat("word ", 13), 10, 0); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a 13-word search must be refused so the prefix match never lands on the wrong word: %v", err)
	}
	if _, err := f.search(f.a, f.pidA, strings.Repeat("word ", 12), 10, 0); err != nil {
		t.Errorf("12 words are fine: %v", err)
	}
}

// A save that changes no searchable text (a status, a word target) leaves the search text and its index as they are, and a title change
// is found under the new title. The index is not rewritten for the saves that do not change it.
func TestSavesThatChangeNoSearchableTextKeepTheIndex(t *testing.T) {
	f := newFx(t)
	pid := f.project(f.a, "Book")
	d := f.doc(f.a, pid, docs.CreateIn{Title: "Arrival", Content: "The lantern swung over the harbour."})
	rev := d.Revision
	for _, in := range []docs.UpdateIn{{Status: ptr("revise")}, {TargetWords: ptr(500)}} {
		in.ID, in.BaseRevision = d.ID, rev
		out, err := f.reg.Call(f.ctx, f.a, "doc.update", in)
		if err != nil {
			t.Fatal(err)
		}
		rev = out.(docs.Item).Item.Revision
	}
	if got := titles(f.mustSearch(f.a, pid, "lantern harbour")); !contains(got, "Arrival") {
		t.Errorf("after a status and a target: the body is not found: %v", got)
	}
	if _, err := f.reg.Call(f.ctx, f.a, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: rev, Title: ptr("Renamed")}); err != nil {
		t.Fatal(err)
	}
	if got := titles(f.mustSearch(f.a, pid, "renamed")); !contains(got, "Renamed") {
		t.Errorf("a new title is not found: %v", got)
	}
	if got := titles(f.mustSearch(f.a, pid, "lantern")); !contains(got, "Renamed") {
		t.Errorf("the body is not found after a rename: %v", got)
	}
}
