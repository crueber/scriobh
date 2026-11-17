package editiondiff_test

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/editiondiff"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// The fixtures are books made for these tests: a project is written with the ops, compiled to EPUB 3 by the exporter,
// and imported as another project, which keeps the file as an edition. Edits are made to the imported project with
// the ops a writer's screens use.

type fx struct {
	t   *testing.T
	d   *db.DB
	reg *ops.Registry
	dir string
	w   ops.Principal
}

func newFx(t *testing.T) *fx {
	t.Helper()
	d := testutil.DB(t)
	dir := t.TempDir()
	f := &fx{t: t, d: d, dir: dir, reg: registry.Build(d, nil, dir)}
	f.w = f.user("writer", ops.RoleWriter)
	return f
}

func (f *fx) user(name, role string) ops.Principal {
	return ops.Principal{UserID: testutil.User(f.t, f.d, role, name+"@test.local"), Role: role}
}

func (f *fx) call(p ops.Principal, op string, in any) (any, error) {
	return f.reg.Call(context.Background(), p, op, in)
}

func (f *fx) must(p ops.Principal, op string, in any) any {
	f.t.Helper()
	out, err := f.call(p, op, in)
	if err != nil {
		f.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (f *fx) code(p ops.Principal, op string, in any) ops.Code {
	f.t.Helper()
	_, err := f.call(p, op, in)
	if err == nil {
		f.t.Fatalf("%s succeeded, want an error", op)
	}
	return ops.AsError(err).Code
}

func (f *fx) member(project string, p ops.Principal, role string) {
	f.t.Helper()
	err := f.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, project, p.UserID, role, db.Now())
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// setRole changes a member's role on a project.
func (f *fx) setRole(project string, p ops.Principal, role string) {
	f.t.Helper()
	err := f.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE project_members SET role = ? WHERE project_id = ? AND user_id = ?`, role, project, p.UserID)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

var words = func() []string {
	r := rand.New(rand.NewSource(11))
	w := make([]string, 3000)
	for i := range w {
		b := make([]byte, 4+r.Intn(6))
		for j := range b {
			b[j] = byte('a' + r.Intn(26))
		}
		w[i] = string(b)
	}
	return w
}()

func para(r *rand.Rand, n int) string {
	p := make([]string, n)
	for i := range p {
		p[i] = words[r.Intn(len(words))]
	}
	return strings.Join(p, " ") + "."
}

type chap struct {
	title string
	paras []string
}

func (c chap) body() string { return strings.Join(c.paras, "\n\n") + "\n" }

func book(seed int64, chapters, paras, wordsPer int) []chap {
	r := rand.New(rand.NewSource(seed))
	out := make([]chap, chapters)
	for i := range out {
		out[i].title = fmt.Sprintf("The %s of %s", words[r.Intn(len(words))], words[r.Intn(len(words))])
		for p := 0; p < paras; p++ {
			out[i].paras = append(out[i].paras, para(r, wordsPer))
		}
	}
	return out
}

func tinyPNG(t testing.TB) []byte {
	m := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range m.Pix {
		m.Pix[i] = 200
	}
	m.Set(1, 1, color.RGBA{1, 2, 3, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// makeProject writes a book into a new project, trashing the starter chapter.
func (f *fx) makeProject(title string, ch []chap) string {
	f.t.Helper()
	out := f.must(f.w, "project.create", projects.CreateIn{Title: title, AuthorName: "A. Writer", Kind: "novel"}).(projects.Item)
	pid := out.Item.ID
	for _, it := range f.must(f.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut).Items {
		f.must(f.w, "doc.trash", docs.DocIn{ID: it.ID})
	}
	for _, c := range ch {
		f.must(f.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: c.title, Kind: "chapter", Content: c.body()})
	}
	return pid
}

func (f *fx) epub(pid string) []byte {
	f.t.Helper()
	return f.must(f.w, "export.compile", compile.CompileIn{ProjectID: pid, Preset: "epub3"}).(compile.Out).Body
}

// importBook imports an EPUB as a new project and returns the apply result.
func (f *fx) importBook(data []byte, label string, edits ...imports.Edit) imports.ApplyOut {
	f.t.Helper()
	pv := f.must(f.w, "import.preview", imports.PreviewIn{Name: "book.epub", Epub: data}).(imports.PreviewOut)
	return f.must(f.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "book.epub", Epub: data, EditionLabel: label, Edits: edits}).(imports.ApplyOut)
}

func (f *fx) preview(data []byte) imports.PreviewOut {
	f.t.Helper()
	return f.must(f.w, "import.preview", imports.PreviewIn{Name: "book.epub", Epub: data}).(imports.PreviewOut)
}

func keyOf(pv imports.PreviewOut, title string) string {
	for _, n := range pv.Nodes {
		if n.Title == title {
			return n.Key
		}
	}
	panic("no node " + title)
}

// docByTitle finds a live document of a project by its title.
func (f *fx) docByTitle(pid, title string) docs.Meta {
	f.t.Helper()
	for _, it := range f.must(f.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut).Items {
		if it.Title == title {
			return it
		}
	}
	f.t.Fatalf("no document %q", title)
	return docs.Meta{}
}

func (f *fx) doc(id string) docs.Doc {
	f.t.Helper()
	return f.must(f.w, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
}

func (f *fx) setBody(id, content string) {
	f.t.Helper()
	d := f.doc(id)
	f.must(f.w, "doc.update", docs.UpdateIn{ID: id, BaseRevision: d.Revision, Content: &content})
}

func (f *fx) diff(pid, eid, chapter string) editiondiff.Result {
	f.t.Helper()
	return f.must(f.w, "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid, Chapter: chapter}).(editiondiff.Result)
}

func (f *fx) edition(pid string) string {
	f.t.Helper()
	var id string
	if err := f.d.Read().QueryRow(`SELECT id FROM editions WHERE project_id = ? ORDER BY imported_at, id LIMIT 1`, pid).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func stepFor(t testing.TB, r editiondiff.Result, title string) editiondiff.Step {
	t.Helper()
	for _, s := range r.Steps {
		for _, b := range s.Base {
			if b.Title == title {
				return s
			}
		}
		for _, h := range s.Head {
			if h.Title == title {
				return s
			}
		}
	}
	var have []string
	for _, s := range r.Steps {
		have = append(have, s.Status)
	}
	t.Fatalf("no step for %q in %v", title, have)
	return editiondiff.Step{}
}

// A book with a picture and a footnote, a chapter of scenes, and two chapters the import merges: imported, and left
// alone, every chapter is as the edition has it. This is the test that fails when the project's text and the
// edition's baseline are read in different forms.
func TestAnImportedBookLeftAloneIsUnchanged(t *testing.T) {
	f := newFx(t)
	ch := book(1, 8, 5, 60)
	src := f.makeProject("The Source", nil)
	// a picture the exporter will carry
	ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(tinyPNG(t)), Length: int64(len(tinyPNG(t)))})
	it, err := f.reg.Call(ctx, f.w, "project.attachment.upload", attachments.UploadIn{ID: src, Filename: "map.png"})
	if err != nil {
		t.Fatal(err)
	}
	pic := "/files/" + it.(attachments.Item).Item.ID
	ch[1].paras[2] += "[^1]"
	ch[1].paras = append(ch[1].paras, "[^1]: A note on the road.")
	ch[2].paras[1] = "![A map of the marsh](" + pic + ")"
	ch[4].paras = []string{"The preface to the scenes.", "## Dusk\n\n" + ch[4].paras[0], "## Dawn\n\n" + ch[4].paras[1]}
	for _, c := range ch {
		f.must(f.w, "doc.create", docs.CreateIn{ProjectID: src, Title: c.title, Kind: "chapter", Content: c.body()})
	}
	data := f.epub(src)
	pv := f.preview(data)
	ap := f.importBook(data, "First edition",
		imports.Edit{Key: keyOf(pv, ch[6].title), MergeInto: keyOf(pv, ch[5].title)},
		imports.Edit{Key: keyOf(pv, ch[4].title), Split: ptr(true)})
	eid := f.edition(ap.ProjectID)
	if n := countDocs(t, f, ap.ProjectID, "scene"); n != 2 {
		t.Fatalf("the import made %d scenes, want 2", n)
	}
	r := f.diff(ap.ProjectID, eid, "")
	merged := 0
	for _, s := range r.Steps {
		if s.Words.Added != 0 || s.Words.Removed != 0 || s.Changes != 0 {
			t.Errorf("%s %v changed: %+v", s.Status, s.Base, s.Words)
		}
		switch s.Status {
		case editiondiff.StatusUnchanged:
		case editiondiff.StatusMerged:
			merged++
			if s.Compared != editiondiff.ComparedEqual || s.Match.Method != editiondiff.MethodRecorded || s.Match.Guess {
				t.Errorf("merged: %+v", s)
			}
		default:
			t.Errorf("an untouched import has a %s step: %+v", s.Status, s)
		}
		if s.Match != nil && s.Match.Method != editiondiff.MethodRecorded {
			t.Errorf("paired by %s: %+v", s.Match.Method, s.Match)
		}
	}
	if merged != 1 || len(r.Steps) != 7 || r.Summary.WordsAdded != 0 || r.TooDifferent || r.Partial {
		t.Errorf("%+v", r.Summary)
	}
	if r.Base.Source != "record" || r.Head.Kind != "project" || r.Base.Label != "First edition" {
		t.Errorf("sides: %+v %+v", r.Base, r.Head)
	}
	// the scenes' chapter says why a difference there cannot be offered one document at a time
	st := stepFor(t, r, ch[4].title)
	if st.Offer == nil || st.Offer.Suggest || st.Offer.Take {
		t.Errorf("a chapter made into scenes is offered: %+v", st.Offer)
	}
}

func ptr[T any](v T) *T { return &v }

func countDocs(t testing.TB, f *fx, pid, kind string) int {
	var n int
	if err := f.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE project_id = ? AND kind = ? AND deleted_at IS NULL`, pid, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// imported makes a plain book, imports it, and returns the importing project and edition.
func (f *fx) imported(seed int64, n int) (pid, eid string, ch []chap) {
	f.t.Helper()
	ch = book(seed, n, 6, 50)
	src := f.makeProject(fmt.Sprintf("Source %d", seed), ch)
	ap := f.importBook(f.epub(src), "First edition")
	return ap.ProjectID, f.edition(ap.ProjectID), ch
}

func TestImportEditDiffReportsExactlyThoseEdits(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(2, 9)
	d := func(i int) docs.Meta { return f.docByTitle(pid, ch[i].title) }

	// 1: a paragraph reworded: three words
	c1 := f.doc(d(1).ID)
	w := strings.Fields(ch[1].paras[3])
	w[2], w[3], w[4] = "qqone", "qqtwo", "qqthree"
	f.setBody(c1.ID, strings.Replace(c1.Content, ch[1].paras[3], strings.Join(w, " "), 1))
	// 2: a paragraph removed (50 words)
	c2 := f.doc(d(2).ID)
	f.setBody(c2.ID, strings.Replace(c2.Content, ch[2].paras[4]+"\n\n", "", 1))
	// 3: a paragraph added (7 words)
	c3 := f.doc(d(3).ID)
	f.setBody(c3.ID, c3.Content+"\nA new closing paragraph of seven words.\n")
	// 4: renamed
	f.must(f.w, "doc.update", docs.UpdateIn{ID: d(4).ID, BaseRevision: f.doc(d(4).ID).Revision, Title: ptr("A different name")})
	// 5: moved to the front
	f.must(f.w, "doc.move", docs.MoveIn{ID: d(5).ID, ParentID: "", After: ptr("")})
	// 6: left out of the project
	f.must(f.w, "doc.trash", docs.DocIn{ID: d(6).ID})
	// a new chapter
	f.must(f.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Brand new", Kind: "chapter", Content: "Ten words in the new chapter, written after the import was made.\n"})
	// 7: split in two where the writer cut it
	c7 := f.doc(d(7).ID)
	cut := strings.Index(c7.Content, ch[7].paras[3])
	f.must(f.w, "doc.split", docs.SplitIn{ID: c7.ID, BaseRevision: c7.Revision, Offset: cut, Title: "The second half"})

	r := f.diff(pid, eid, "")
	want := map[int]string{0: "unchanged", 1: "edited", 2: "edited", 3: "edited", 4: "edited", 5: "moved", 6: "removed", 7: "split", 8: "unchanged"}
	for i, wnt := range want {
		s := stepFor(t, r, ch[i].title)
		if s.Status != wnt {
			t.Errorf("chapter %d is %s, want %s: %+v", i+1, s.Status, wnt, s)
		}
	}
	if s := stepFor(t, r, ch[1].title); s.Words.Added != 3 || s.Words.Removed != 3 || s.Changes != 1 || s.Match.Method != editiondiff.MethodRecorded || s.Match.Score != 1 {
		t.Errorf("reworded: %+v", s)
	}
	if s := stepFor(t, r, ch[2].title); s.Words.Added != 0 || s.Words.Removed != 50 || s.Changes != 1 {
		t.Errorf("removed paragraph: %+v", s)
	}
	if s := stepFor(t, r, ch[3].title); s.Words.Added != 7 || s.Words.Removed != 0 {
		t.Errorf("added paragraph: %+v", s)
	}
	s4 := stepFor(t, r, ch[4].title)
	if !s4.Renamed || s4.Words.Added != 0 || s4.Words.Removed != 0 || s4.Head[0].Title != "A different name" {
		t.Errorf("renamed: %+v", s4)
	}
	s5 := stepFor(t, r, ch[5].title)
	if !s5.Moved || s5.Status != editiondiff.StatusMoved && s5.Status != editiondiff.StatusEdited {
		t.Errorf("moved: %+v", s5)
	}
	if s5.Status != editiondiff.StatusMoved {
		t.Errorf("a chapter moved with no edit is %s", s5.Status)
	}
	if s := stepFor(t, r, ch[6].title); s.Status != editiondiff.StatusRemoved || s.Words.Removed != 300 || s.Match != nil {
		t.Errorf("removed: %+v", s)
	}
	if s := stepFor(t, r, "Brand new"); s.Status != editiondiff.StatusAdded || s.Words.Added != 12 {
		t.Errorf("added: %+v", s)
	}
	s7 := stepFor(t, r, ch[7].title)
	// the first part is the import's record, the second half is paired by the text: the step is as sure as its weakest member
	if len(s7.Head) != 2 || s7.Words.Added != 0 || s7.Words.Removed != 0 || s7.Match.Method != editiondiff.MethodSimilarity || !s7.Match.Guess {
		t.Errorf("split: %+v", s7)
	}
	if r.Summary.Edited != 4 || r.Summary.Moved != 1 || r.Summary.Split != 1 || r.Summary.Added != 1 || r.Summary.Removed != 1 || r.Summary.Unchanged != 2 {
		t.Errorf("%+v", r.Summary)
	}
	// 3 + 7 + 12 added; 3 + 50 + 300 removed. A cut is not net-counted away.
	if r.Summary.WordsAdded != 3+7+12 || r.Summary.WordsRemoved != 3+50+300 {
		t.Errorf("words %+v", r.Summary)
	}
	// reading order of the project's text, a removed chapter after the one before it
	if r.Steps[0].Head[0].Title != ch[5].title {
		t.Errorf("the moved chapter is not first: %+v", r.Steps[0])
	}

	// the reviewer steps into one chapter and gets its differences, one by one, with the words
	s := f.diff(pid, eid, stepFor(t, r, ch[1].title).Key)
	var hs []editiondiff.Hunk
	for _, st := range s.Steps {
		if st.Key == stepFor(t, r, ch[1].title).Key {
			hs = st.Hunks
		}
	}
	if len(hs) != 1 || hs[0].Kind != editiondiff.HunkReplace || !strings.Contains(hs[0].Head, "qqone") || len(hs[0].Segments) < 3 || hs[0].Offer != "" {
		t.Errorf("hunks: %+v", hs)
	}
	if code := f.code(f.w, "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid, Chapter: "b99:hnope"}); code != ops.CodeNotFound {
		t.Errorf("an unknown chapter: %s", code)
	}
}

func TestSuggestionsFromTheDiffAreReviewedOneAtATime(t *testing.T) {
	f := newFx(t)
	ch := book(3, 4, 7, 40)
	ch[1].paras[2] = "A paragraph with [a link](https://example.com/page) in it that a later edit takes out."
	src := f.makeProject("Source", ch)
	ap := f.importBook(f.epub(src), "First edition")
	pid, eid := ap.ProjectID, f.edition(ap.ProjectID)
	d := f.docByTitle(pid, ch[1].title)
	doc := f.doc(d.ID)
	// reword paragraph 1; drop the paragraph with the link and a plain one; add one of our own
	w := strings.Fields(ch[1].paras[1])
	w[0] = "REWORDED"
	text := strings.Replace(doc.Content, ch[1].paras[1], strings.Join(w, " "), 1)
	text = strings.Replace(text, ch[1].paras[2]+"\n\n", "", 1)
	text = strings.Replace(text, ch[1].paras[5]+"\n\n", "", 1)
	text += "\nOur own closing words.\n"
	f.setBody(d.ID, text)
	doc = f.doc(d.ID)

	key := stepFor(t, f.diff(pid, eid, ""), ch[1].title).Key
	r := f.diff(pid, eid, key)
	st := stepFor(t, r, ch[1].title)
	if st.Offer == nil || !st.Offer.Suggest || len(st.Hunks) != 4 {
		t.Fatalf("%+v", st)
	}
	refused := map[string]string{}
	var offered []string
	for _, h := range st.Hunks {
		if h.Offer != "" {
			refused[h.Base+"|"+h.Head] = h.Offer
		} else {
			offered = append(offered, h.ID)
		}
	}
	if len(offered) != 2 || len(refused) != 2 {
		t.Fatalf("hunks %+v", st.Hunks)
	}
	// a revision that does not match is a conflict
	if code := f.code(f.w, "edition.suggest", editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: doc.Revision - 1}); code != ops.CodeConflict {
		t.Errorf("stale revision: %s", code)
	}
	if code := f.code(f.w, "edition.suggest", editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: doc.Revision, Hunks: []string{"nope"}}); code != ops.CodeInvalid {
		t.Errorf("an unknown difference: %s", code)
	}
	out := f.must(f.w, "edition.suggest", editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: doc.Revision}).(editiondiff.SuggestOut)
	if len(out.Created) != 2 || len(out.Skipped) != 2 {
		t.Fatalf("%+v", out)
	}
	for _, s := range out.Skipped {
		if s.Reason == "" {
			t.Errorf("%+v", s)
		}
	}
	// the text has not changed: the suggestions are rows beside it
	if after := f.doc(d.ID); after.Content != doc.Content || after.Revision != doc.Revision {
		t.Error("the document changed when suggestions were made")
	}
	list := f.must(f.w, "suggestion.list", review.SuggestionListIn{ProjectID: pid, DocumentID: d.ID}).(review.SuggestionListOut)
	if list.Total != 2 {
		t.Fatalf("%d suggestions", list.Total)
	}
	// accept the rewording alone: the paragraph is the edition's again, the rest stays as the writer wrote it
	var reword, restore string
	for _, c := range out.Created {
		for _, h := range st.Hunks {
			if h.ID == c.Hunk && h.Kind == editiondiff.HunkReplace {
				reword = c.SuggestionID
			} else if h.ID == c.Hunk {
				restore = c.SuggestionID
			}
		}
	}
	f.must(f.w, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: pid, SuggestionID: reword, BaseRevision: doc.Revision})
	got := f.doc(d.ID)
	if !strings.Contains(got.Content, ch[1].paras[1]) || strings.Contains(got.Content, "REWORDED") || !strings.Contains(got.Content, "Our own closing words.") || strings.Contains(got.Content, ch[1].paras[5]) {
		t.Errorf("after accepting one suggestion:\n%s", got.Content)
	}
	// the other is still waiting; accepting it puts the dropped paragraph back, between the paragraphs it was between
	if rest := f.must(f.w, "suggestion.list", review.SuggestionListIn{ProjectID: pid, DocumentID: d.ID}).(review.SuggestionListOut); rest.Total != 1 {
		t.Errorf("%d suggestions left", rest.Total)
	}
	f.must(f.w, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: pid, SuggestionID: restore, BaseRevision: got.Revision})
	got = f.doc(d.ID)
	if !strings.Contains(got.Content, ch[1].paras[4]+"\n\n"+ch[1].paras[5]+"\n\n"+ch[1].paras[6]) {
		t.Errorf("the paragraph is not where it was:\n%s", got.Content)
	}
	r = f.diff(pid, eid, "")
	if s := stepFor(t, r, ch[1].title); s.Changes != 2 { // the link paragraph and our own closing words remain
		t.Errorf("%+v", s)
	}
	// a reader cannot make suggestions from the diff
	reader := f.user("reader", ops.RoleWriter)
	f.member(pid, reader, ops.ProjectReader)
	if code := f.code(reader, "edition.suggest", editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: got.Revision}); code != ops.CodeForbidden {
		t.Errorf("a reader: %s", code)
	}
}

func TestTakeAChapterIsAnUndoableRevision(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(4, 5)
	d := f.docByTitle(pid, ch[2].title)
	orig := f.doc(d.ID)
	f.setBody(d.ID, "Everything was rewritten.\n\nAnd again.\n")
	doc := f.doc(d.ID)
	r := f.diff(pid, eid, "")
	st := stepFor(t, r, ch[2].title)
	if st.Status != editiondiff.StatusEdited || st.Offer == nil || !st.Offer.Take {
		t.Fatalf("%+v", st)
	}
	in := editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: doc.Revision}
	// a preview changes nothing
	if _, err := f.reg.CallDry(context.Background(), f.w, "edition.take", in); err != nil {
		t.Fatal(err)
	}
	if f.doc(d.ID).Content != doc.Content {
		t.Fatal("a dry run changed the text")
	}
	stale := in
	stale.BaseRevision--
	if code := f.code(f.w, "edition.take", stale); code != ops.CodeConflict {
		t.Errorf("stale: %s", code)
	}
	beforeTake := f.credited()
	out := f.must(f.w, "edition.take", in).(editiondiff.TakeOut)
	// the words come back from the edition, written before: no goal credit
	if after := f.credited(); after != beforeTake {
		t.Errorf("taking a chapter credited %d words", after-beforeTake)
	}
	if out.RevisionID == "" || out.Unchanged {
		t.Fatalf("%+v", out)
	}
	got := f.doc(d.ID)
	if got.Content != orig.Content {
		t.Errorf("the chapter is not the edition's:\n%q\n%q", got.Content, orig.Content)
	}
	// the writer's text was kept first, and the new version is a restore: the history undoes it
	revs := f.must(f.w, "revision.list", revisions.ListIn{ID: d.ID}).(revisions.ListOut)
	var snapshot, restore *revisions.Revision
	for i := range revs.Items {
		switch revs.Items[i].Reason {
		case revisions.ReasonSnapshot:
			if snapshot == nil {
				snapshot = &revs.Items[i]
			}
		case revisions.ReasonRestore:
			restore = &revs.Items[i]
		}
	}
	if snapshot == nil || restore == nil || restore.ID != out.RevisionID {
		t.Fatalf("revisions: %+v", revs.Items)
	}
	back := f.must(f.w, "revision.get", revisions.GetIn{ID: d.ID, RevisionID: snapshot.ID}).(revisions.GetOut)
	if back.Content != doc.Content {
		t.Errorf("the text that was replaced was not kept: %q", back.Content)
	}
	// and the chapter is now unchanged
	if s := stepFor(t, f.diff(pid, eid, ""), ch[2].title); s.Status != editiondiff.StatusUnchanged {
		t.Errorf("%+v", s)
	}
	// taking it again changes nothing
	again := f.diff(pid, eid, "")
	if st := stepFor(t, again, ch[2].title); st.Offer == nil || st.Offer.Take {
		t.Errorf("an unchanged chapter is offered: %+v", st.Offer)
	}
	// a removed chapter and an added chapter cannot be taken
	f.must(f.w, "doc.trash", docs.DocIn{ID: f.docByTitle(pid, ch[0].title).ID})
	r = f.diff(pid, eid, "")
	rm := stepFor(t, r, ch[0].title)
	if code := f.code(f.w, "edition.take", editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: rm.Key, BaseRevision: 1}); code != ops.CodeConflict {
		t.Errorf("a removed chapter: %s", code)
	}
	// the control: the same text saved by hand is credited, so the check of the take above could have failed
	other := f.docByTitle(pid, ch[3].title)
	c0 := f.credited()
	f.setBody(other.ID, orig.Content+"\n"+strings.Repeat("word ", 40)+"\n")
	if f.credited() <= c0 {
		t.Errorf("a save credited nothing (%d to %d): the check of the take proves nothing", c0, f.credited())
	}
}

func TestCompareTwoExportedEditions(t *testing.T) {
	f := newFx(t)
	ch := book(5, 8, 5, 60)
	a := f.makeProject("The Book", ch)
	e1 := f.epub(a)
	ap1 := f.importBook(e1, "First edition")
	// the second edition: chapters 1 and 2 reworded, 3 dropped, 4 and 5 swapped, a new epilogue, a title changed
	second := make([]chap, 0, 9)
	for i, c := range ch {
		switch i {
		case 2:
			continue
		case 0:
			c.paras = append([]string(nil), c.paras...)
			c.paras[1] = "Rewritten in the second edition, entirely, in eleven words or so."
		case 1:
			c.title = "A better title"
		}
		second = append(second, c)
	}
	second[2], second[3] = second[3], second[2]
	second = append(second, chap{title: "Epilogue", paras: []string{"What happened after, in nine words that are all new."}})
	b := f.makeProject("The Book, revised", second)
	ap2 := f.importBook(f.epub(b), "Second edition")
	eid1, eid2 := f.edition(ap1.ProjectID), f.edition(ap2.ProjectID)

	for _, src := range []string{"file", "record", "auto"} {
		t.Run(src, func(t *testing.T) {
			r := f.must(f.w, "edition.compare", editiondiff.CompareIn{ProjectID: ap1.ProjectID, EditionID: eid1, WithProject: ap2.ProjectID, WithEdition: eid2, Source: src}).(editiondiff.Result)
			if r.Mode != "editions" || r.Base.Label != "First edition" || r.Head.Label != "Second edition" || r.Head.ProjectID != ap2.ProjectID {
				t.Errorf("%+v %+v", r.Base, r.Head)
			}
			wantSrc := src
			if src == "auto" {
				wantSrc = "file"
			}
			if r.Base.Source != wantSrc || r.Head.Source != wantSrc {
				t.Errorf("source %s %s", r.Base.Source, r.Head.Source)
			}
			if s := stepFor(t, r, ch[0].title); s.Status != editiondiff.StatusEdited || s.Words.Removed == 0 || s.Changes != 1 {
				t.Errorf("reworded: %+v", s)
			}
			s := stepFor(t, r, ch[1].title)
			if s.Status != editiondiff.StatusEdited || !s.Renamed || s.Words.Added != 0 || s.Words.Removed != 0 {
				t.Errorf("renamed: %+v", s)
			}
			if !s.Match.Guess {
				t.Errorf("a pairing between two editions is a guess: %+v", s.Match)
			}
			if s := stepFor(t, r, ch[2].title); s.Status != editiondiff.StatusRemoved || s.Words.Removed != 300 {
				t.Errorf("dropped: %+v", s)
			}
			if s := stepFor(t, r, "Epilogue"); s.Status != editiondiff.StatusAdded || s.Words.Added != 10 {
				t.Errorf("new: %+v", s)
			}
			moved := 0
			for _, s := range r.Steps {
				if s.Moved {
					moved++
				}
			}
			if moved != 1 {
				t.Errorf("%d chapters moved", moved)
			}
			// the comparison imported nothing
			if n := count(t, f, `SELECT count(*) FROM projects`); n != 4 {
				t.Errorf("%d projects", n)
			}
		})
	}
	// a person who cannot open the second project cannot compare against it, and cannot tell it exists
	stranger := f.user("stranger", ops.RoleWriter)
	f.member(ap1.ProjectID, stranger, ops.ProjectReader)
	in := editiondiff.CompareIn{ProjectID: ap1.ProjectID, EditionID: eid1, WithProject: ap2.ProjectID, WithEdition: eid2}
	// a comparison is the editors': a reader of the first project is refused before anything of the second is looked at
	if code := f.code(stranger, "edition.compare", in); code != ops.CodeForbidden {
		t.Errorf("a reader of the first project: %s", code)
	}
	f.setRole(ap1.ProjectID, stranger, ops.ProjectEditor)
	if code := f.code(stranger, "edition.compare", in); code != ops.CodeNotFound {
		t.Errorf("the second project: %s", code)
	}
	f.setRole(ap1.ProjectID, stranger, ops.ProjectReader)
	if code := f.code(f.w, "edition.compare", editiondiff.CompareIn{ProjectID: ap1.ProjectID, EditionID: eid1, WithProject: ap2.ProjectID, WithEdition: eid1}); code != ops.CodeNotFound {
		t.Errorf("an edition of another project: %s", code)
	}
	f.member(ap2.ProjectID, stranger, ops.ProjectReader)
	// a comparison shows the text of both books: editors of both
	if code := f.code(stranger, "edition.compare", in); code != ops.CodeForbidden {
		t.Errorf("a reader of both: %s", code)
	}
	f.setRole(ap2.ProjectID, stranger, ops.ProjectEditor)
	if code := f.code(stranger, "edition.compare", in); code != ops.CodeForbidden {
		t.Errorf("an editor of the second and a reader of the first: %s", code)
	}
	f.setRole(ap1.ProjectID, stranger, ops.ProjectEditor)
	f.must(stranger, "edition.compare", in)
	// a key pinned to the first project cannot reach the second
	pinned := ops.Principal{UserID: f.w.UserID, Role: ops.RoleWriter, Via: "apikey", ProjectID: ap1.ProjectID}
	if code := f.code(pinned, "edition.compare", in); code != ops.CodeNotFound {
		t.Errorf("a pinned key: %s", code)
	}
	// the source file gone: auto falls back to the records and says so; file refuses
	for _, p := range []string{ap2.ProjectID} {
		if err := f.d.WithTx(context.Background(), func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE editions SET has_file = 0 WHERE project_id = ?`, p)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	r := f.must(f.w, "edition.compare", in).(editiondiff.Result)
	if r.Head.Source != "record" || len(r.Notices) == 0 {
		t.Errorf("fallback: %+v %v", r.Head, r.Notices)
	}
	in.Source = "file"
	if code := f.code(f.w, "edition.compare", in); code != ops.CodeConflict {
		t.Errorf("file: %s", code)
	}
}

func count(t testing.TB, f *fx, q string) int {
	var n int
	if err := f.d.Read().QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAuthorizationAndTheTrash(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(6, 4)
	d := f.docByTitle(pid, ch[1].title)
	f.setBody(d.ID, "Rewritten.\n")
	st := stepFor(t, f.diff(pid, eid, ""), ch[1].title)
	doc := f.doc(d.ID)
	reader, outsider := f.user("reader", ops.RoleWriter), f.user("outsider", ops.RoleWriter)
	f.member(pid, reader, ops.ProjectReader)
	// a reader reads the diff and the comparison, and writes nothing
	f.must(reader, "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid})
	if code := f.code(reader, "edition.take", editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: doc.Revision}); code != ops.CodeForbidden {
		t.Errorf("take as a reader: %s", code)
	}
	for _, op := range []string{"edition.diff", "edition.compare"} {
		var in any = editiondiff.DiffIn{ProjectID: pid, EditionID: eid}
		if op == "edition.compare" {
			in = editiondiff.CompareIn{ProjectID: pid, EditionID: eid, WithEdition: eid}
		}
		if code := f.code(outsider, op, in); code != ops.CodeNotFound {
			t.Errorf("%s as an outsider: %s", op, code)
		}
	}
	// another project's edition is not found
	other, _, _ := f.imported(7, 3)
	if code := f.code(f.w, "edition.diff", editiondiff.DiffIn{ProjectID: other, EditionID: eid}); code != ops.CodeNotFound {
		t.Errorf("an edition of another project: %s", code)
	}
	// a project in the trash is read-only: the diff reads, the ops that write refuse
	f.must(f.w, "project.trash", projects.StateIn{ID: pid})
	f.must(f.w, "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid})
	for op, in := range map[string]any{
		"edition.take":    editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: doc.Revision},
		"edition.suggest": editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: doc.Revision},
	} {
		_, err := f.call(f.w, op, in)
		if err == nil || ops.AsError(err).Code != ops.CodeConflict || ops.AsError(err).Data["reason"] != "project_trashed" {
			t.Errorf("%s on a trashed project: %v", op, err)
		}
	}
	if f.doc(d.ID).Content != doc.Content {
		t.Error("the trashed project's text changed")
	}
}

// ---- budgets, at the level of the ops -------------------------------------------------------------------------------

func TestBudgetsOfThreeHundredPages(t *testing.T) {
	if testing.Short() {
		t.Skip("a 90,000-word book")
	}
	f := newFx(t)
	ch := book(8, 40, 38, 60) // 40 chapters of 2,280 words: about 91,000 words
	src := f.makeProject("Long Book", ch)
	e1 := f.epub(src)
	ap1 := f.importBook(e1, "First edition")
	eid1 := f.edition(ap1.ProjectID)
	// some edits in every chapter of the imported project
	r := rand.New(rand.NewSource(9))
	list := f.must(f.w, "doc.list", docs.ListIn{ProjectID: ap1.ProjectID}).(docs.ListOut)
	for _, it := range list.Items {
		d := f.doc(it.ID)
		text := d.Content
		for k := 0; k < 3; k++ {
			ps := strings.Split(text, "\n\n")
			i := r.Intn(len(ps))
			ps[i] = para(r, 60)
			text = strings.Join(ps, "\n\n")
		}
		f.setBody(it.ID, text)
	}
	start := time.Now()
	res := f.diff(ap1.ProjectID, eid1, "")
	el := time.Since(start)
	t.Logf("edition.diff of a 91,000-word, 40-chapter book against its own edition, 120 edits: %v", el)
	if res.Summary.Edited != 40 || res.Summary.Changes < 100 || res.Summary.Changes > 120 {
		t.Errorf("%+v", res.Summary)
	}
	diffTook := el
	// two editions of the book, from their files
	revised := make([]chap, len(ch))
	for i := range ch {
		revised[i] = chap{title: ch[i].title, paras: append([]string(nil), ch[i].paras...)}
		for k := 0; k < 6; k++ {
			revised[i].paras[r.Intn(len(revised[i].paras))] = para(r, 60)
		}
	}
	b := f.makeProject("Long Book, revised", revised)
	ap2 := f.importBook(f.epub(b), "Second edition")
	eid2 := f.edition(ap2.ProjectID)
	start = time.Now()
	cmp := f.must(f.w, "edition.compare", editiondiff.CompareIn{ProjectID: ap1.ProjectID, EditionID: eid1, WithProject: ap2.ProjectID, WithEdition: eid2, Source: "file"}).(editiondiff.Result)
	el = time.Since(start)
	t.Logf("edition.compare of two 91,000-word editions read from their files: %v", el)
	if cmp.Summary.Edited != 40 || cmp.Base.Source != "file" {
		t.Errorf("%+v", cmp.Summary)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		limit := 2 * time.Second
		if raceBuild {
			limit *= 8
		}
		if diffTook > limit {
			t.Errorf("edition.diff took %v", diffTook)
		}
		limit = 5 * time.Second
		if raceBuild {
			limit *= 8
		}
		if el > limit {
			t.Errorf("edition.compare took %v", el)
		}
	})
}

// ---- the text of an edition is the editors', and hostile editions ------------------------------------------------

func TestComparingEditionsIsForEditorsOfBoth(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(12, 4)
	d := f.docByTitle(pid, ch[1].title)
	f.setBody(d.ID, "Rewritten.\n")
	reader := f.user("reader", ops.RoleWriter)
	f.member(pid, reader, ops.ProjectReader)
	// a reader of the first project who owns a second cannot lift the first book's text through a comparison, with
	// chapter or without
	mine := f.makeProject("Mine", book(13, 3, 3, 20))
	data := f.epub(mine)
	other := f.user("other", ops.RoleWriter)
	f.member(pid, other, ops.ProjectReader)
	own := f.must(other, "project.create", projects.CreateIn{Title: "Own"}).(projects.Item).Item.ID
	e2 := f.storeEdition(own, data, "Own")
	in := editiondiff.CompareIn{ProjectID: pid, EditionID: eid, WithProject: own, WithEdition: e2, Source: "record"}
	for _, chapter := range []string{"", "b2:he2"} {
		in.Chapter = chapter
		if code := f.code(other, "edition.compare", in); code != ops.CodeForbidden {
			t.Errorf("a reader of one edition, chapter %q: %s", chapter, code)
		}
	}
}

// storeEdition records a source file as an edition of a project, with one chapter, and keeps the file.
func (f *fx) storeEdition(project string, data []byte, label string) string {
	f.t.Helper()
	sum := editions.SumOf(data)
	var id string
	err := f.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		e, err := editions.Insert(tx, editions.New{ProjectID: project, Kind: editions.KindEPUB, Label: label, SHA256: sum, Size: int64(len(data)),
			Chapters: []editions.NewChapter{{Kind: "chapter", Title: "One", SourcePath: "OEBPS/one.xhtml", Baseline: "Some text of the chapter that is long enough to be read.\n", Words: 11}}}, true)
		id = e.ID
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := editions.Path(f.dir, project, sum, "epub")
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		f.t.Fatal(err)
	}
	return id
}

func TestCompareRefusesAHostileSourceFile(t *testing.T) {
	f := newFx(t)
	pid, eid, _ := f.imported(14, 3)
	garbage := f.storeEdition(pid, []byte("this is not a zip file at all, only text"), "Garbage")
	in := editiondiff.CompareIn{ProjectID: pid, EditionID: eid, WithEdition: garbage, Source: "file"}
	if code := f.code(f.w, "edition.compare", in); code != ops.CodeInvalid {
		t.Errorf("a file that is not a book: %s", code)
	}
	// auto reads the files too, and refuses the same way: a book that cannot be read is not silently replaced
	in.Source = ""
	if code := f.code(f.w, "edition.compare", in); code != ops.CodeInvalid {
		t.Errorf("auto: %s", code)
	}
	if testing.Short() {
		return
	}
	// a zip that inflates to more than an import reads
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	m, err := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write([]byte("application/epub+zip")); err != nil {
		t.Fatal(err)
	}
	big, err := zw.CreateHeader(&zip.FileHeader{Name: "OEBPS/big.xhtml", Method: zip.Deflate})
	if err != nil {
		t.Fatal(err)
	}
	zero := make([]byte, 1<<20)
	for i := 0; i < 130; i++ {
		if _, err := big.Write(zero); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	bomb := f.storeEdition(pid, zb.Bytes(), "Bomb")
	in = editiondiff.CompareIn{ProjectID: pid, EditionID: eid, WithEdition: bomb, Source: "file"}
	start := time.Now()
	if code := f.code(f.w, "edition.compare", in); code != ops.CodeInvalid {
		t.Errorf("a zip bomb: %s", code)
	}
	el := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if el > 10*time.Second {
			t.Errorf("the bomb took %v", el)
		}
	})
}

func TestACompareThatIsTooLargeIsRefusedClearly(t *testing.T) {
	f := newFx(t)
	pid, eid, _ := f.imported(15, 4)
	defer editiondiff.ShrinkLimits(func(l *editiondiff.Limits) { l.MaxTotalBytes = 1000 })()
	_, err := f.call(f.w, "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid})
	if err == nil || ops.AsError(err).Code != ops.CodeInvalid || !strings.Contains(ops.AsError(err).Message, "too large") {
		t.Errorf("%v", err)
	}
}

// What the hostile inputs cost in memory, at the level of the ops: a project of the largest documents against an
// edition of the largest chapters.
func TestHostileSizesStayBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("large documents")
	}
	f := newFx(t)
	pid, eid, ch := f.imported(16, 3)
	// the biggest document a project holds is 2 MiB: fill each chapter with one-letter paragraphs
	var sb strings.Builder
	for sb.Len() < 2<<20-16 {
		sb.WriteString("a\n")
	}
	for _, c := range ch {
		f.setBody(f.docByTitle(pid, c.title).ID, sb.String())
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	r := f.diff(pid, eid, "")
	el := time.Since(start)
	runtime.ReadMemStats(&after)
	t.Logf("three 2 MiB documents of one-letter paragraphs against the edition: %v, %d MiB allocated, %+v", el, (after.TotalAlloc-before.TotalAlloc)>>20, r.Summary)
	if r.Summary.Edited != 3 {
		t.Errorf("%+v", r.Summary)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if el > 10*time.Second*time.Duration(1+7*b2i(raceBuild)) {
			t.Errorf("took %v", el)
		}
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- editions appended into one project ------------------------------------------------------------------------------

func (f *fx) appendBook(pid string, data []byte, label string) string {
	f.t.Helper()
	pv := f.must(f.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, Name: "book.epub", Epub: data}).(imports.PreviewOut)
	f.must(f.w, "import.append", imports.AppendIn{ProjectID: pid, Token: pv.Token, Name: "book.epub", Epub: data, EditionLabel: label})
	var id string
	if err := f.d.Read().QueryRow(`SELECT id FROM editions WHERE project_id = ? AND label = ?`, pid, label).Scan(&id); err != nil {
		f.t.Fatal(err)
	}
	return id
}

// A project that holds a large book and, appended to it, two editions of a smaller one: each edition is compared with
// the project as it stands, and with the other. The books are made by one tool, so their files have the same names.
func TestEditionsAppendedIntoOneProject(t *testing.T) {
	f := newFx(t)
	big := book(20, 12, 5, 60)
	bigImport := f.importBook(f.epub(f.makeProject("The Big Book", big)), "Big book")
	pid := bigImport.ProjectID
	small := book(21, 5, 5, 60)
	e1 := f.appendBook(pid, f.epub(f.makeProject("Small", small)), "Small, first")
	revised := make([]chap, len(small))
	for i, c := range small {
		revised[i] = chap{title: c.title, paras: append([]string(nil), c.paras...)}
	}
	revised[1].paras[2] = "Reworded in the second edition of the small book, a line of twelve or so words."
	e2 := f.appendBook(pid, f.epub(f.makeProject("Small, revised", revised)), "Small, second")

	// the small book's own chapters are unchanged, and the project's other chapters are added: the text of the other
	// edition, which is a second copy of most of them, is not a split of these chapters
	r := f.diff(pid, e1, "")
	if r.TooDifferent || r.Partial {
		t.Errorf("a small edition in a large project: %+v", r.Summary)
	}
	for i, c := range small {
		s := stepFor(t, r, c.title)
		if s.Status != editiondiff.StatusUnchanged || s.Match.Method != editiondiff.MethodRecorded || len(s.Head) != 1 {
			t.Errorf("small book chapter %d: %+v", i+1, s)
		}
	}
	if r.Summary.Split != 0 || r.Summary.Merged != 0 || r.Summary.Unchanged != 5 || r.Summary.Added != 12+5 {
		t.Errorf("%+v", r.Summary)
	}
	// against the second edition: its own chapters are unchanged, with the reworded one's own text
	r = f.diff(pid, e2, "")
	if r.Summary.Unchanged != 5 || r.Summary.Split != 0 || r.Summary.Edited != 0 || r.Summary.Added != 12+5 {
		t.Errorf("%+v", r.Summary)
	}
	// a chapter whose document was trashed is removed: another import's file of the same name is not its document
	f.must(f.w, "doc.trash", docs.DocIn{ID: f.docByTitle(pid, small[2].title).ID})
	r = f.diff(pid, e1, "")
	if s := stepFor(t, r, small[2].title); s.Status != editiondiff.StatusRemoved {
		// the second edition's copy of the chapter has the same text and title: by the title it is the chapter, a guess
		if s.Match == nil || !s.Match.Guess || s.Match.Method == editiondiff.MethodPath || s.Match.Method == editiondiff.MethodRecorded {
			t.Errorf("a trashed chapter: %+v", s)
		}
	}
	// the two editions against each other, from their files, inside one project
	cmp := f.must(f.w, "edition.compare", editiondiff.CompareIn{ProjectID: pid, EditionID: e1, WithEdition: e2, Source: "file"}).(editiondiff.Result)
	if s := stepFor(t, cmp, small[1].title); s.Status != editiondiff.StatusEdited || s.Words.Added == 0 || s.Changes != 1 {
		t.Errorf("%+v", s)
	}
	if cmp.Summary.Edited != 1 || cmp.Summary.Unchanged != 4 || cmp.Summary.Added != 0 || cmp.Summary.Removed != 0 {
		t.Errorf("%+v", cmp.Summary)
	}
}

// ---- the project passphrase gate -----------------------------------------------------------------------------------

func (f *fx) session(p ops.Principal, id string) ops.Principal {
	f.t.Helper()
	err := f.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		now := db.Now()
		_, err := tx.Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`, id, p.UserID, "hash-"+id, now, now, db.Format(time.Now().Add(24*time.Hour)))
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	p.Via, p.SessionID = "session", id
	return p
}

// A comparison names two projects, and the gate of each applies: a second project that is locked for the caller's
// session refuses (403 project_locked), not 404 (they are a member) and not the comparison.
func TestAComparisonAsksTheGateOfBothProjects(t *testing.T) {
	f := newFx(t)
	owner := f.session(f.w, "ses_owner")
	away := f.session(f.w, "ses_away") // the same person, another screen, nothing unlocked
	p1, e1, _ := f.imported(30, 3)
	p2, e2, _ := f.imported(31, 3)
	in := editiondiff.CompareIn{ProjectID: p1, EditionID: e1, WithProject: p2, WithEdition: e2, Source: "record"}
	f.must(away, "edition.compare", in)
	f.must(owner, "project.passphrase.set", projectgate.SetIn{ID: p2, Passphrase: "a long enough one"})
	// the screen that set it has it unlocked
	f.must(owner, "edition.compare", in)
	// the other screen cannot reach the locked second project through the first
	for _, chapter := range []string{"", "b1:he1"} {
		in.Chapter = chapter
		_, err := f.call(away, "edition.compare", in)
		if reason, ok := ops.IsLocked(err); !ok || reason != ops.ReasonProjectLocked {
			t.Errorf("chapter %q: a locked second project: %v", chapter, err)
		}
	}
	// and the locked project's own ops are refused as usual
	_, err := f.call(away, "edition.diff", editiondiff.DiffIn{ProjectID: p2, EditionID: e2})
	if _, ok := ops.IsLocked(err); !ok {
		t.Errorf("a locked first project: %v", err)
	}
	// a person who is no member of the locked project learns nothing of it: 404, not the lock
	stranger := f.user("gstranger", ops.RoleWriter)
	f.member(p1, stranger, ops.ProjectEditor) // an editor of the first project only
	stranger = f.session(stranger, "ses_stranger")
	in.Chapter = ""
	if code := f.code(stranger, "edition.compare", in); code != ops.CodeNotFound {
		t.Errorf("a stranger to the locked second project: %s", code)
	}
	// an API key is refused while the owner does not accept keys
	key := ops.Principal{UserID: f.w.UserID, Role: ops.RoleWriter, Via: "apikey", KeyID: "key_gatetest", Scope: ops.ScopeWrite}
	_, err = f.call(key, "edition.compare", in)
	if reason, ok := ops.IsLocked(err); !ok || reason != ops.ReasonProjectKeysRefused {
		t.Errorf("a key against a locked second project: %v", err)
	}
	// locked on the first side too
	f.must(owner, "project.passphrase.set", projectgate.SetIn{ID: p1, Passphrase: "another long enough one"})
	_, err = f.call(away, "edition.compare", editiondiff.CompareIn{ProjectID: p1, EditionID: e1, WithProject: p2, WithEdition: e2})
	if _, ok := ops.IsLocked(err); !ok {
		t.Errorf("both locked: %v", err)
	}
}
