package imports_test

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// The staged import: a file held on the server while it is reviewed, the draft that holds the review, and what becomes of both.

type denv struct {
	t       *testing.T
	d       *db.DB
	reg     *ops.Registry
	dataDir string
	lim     imports.StageLimits
	w       ops.Principal
}

func newDraftEnv(t *testing.T, lim imports.StageLimits) *denv {
	t.Helper()
	d := testutil.DB(t)
	dir := t.TempDir()
	e := &denv{t: t, d: d, dataDir: dir, lim: lim, reg: registry.BuildWith(d, nil, dir, registry.Options{ImportStage: lim})}
	e.w = e.user("writer", ops.RoleWriter)
	return e
}

func (e *denv) user(name, role string) ops.Principal {
	id := testutil.User(e.t, e.d, role, name+"@test.local")
	return ops.Principal{UserID: id, Role: role}
}

func (e *denv) call(p ops.Principal, op string, in any) (any, error) {
	return e.reg.Call(context.Background(), p, op, in)
}

func (e *denv) must(p ops.Principal, op string, in any) any {
	e.t.Helper()
	out, err := e.call(p, op, in)
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out
}

// stage sends bytes as the body of import.stage.
func (e *denv) stage(p ops.Principal, in imports.StageIn, body []byte) (imports.StageOut, error) {
	return e.stageR(p, in, bytes.NewReader(body), int64(len(body)))
}

func (e *denv) stageR(p ops.Principal, in imports.StageIn, r interface{ Read([]byte) (int, error) }, length int64) (imports.StageOut, error) {
	ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: r, ContentType: "application/octet-stream", Length: length})
	out, err := e.reg.Call(ctx, p, "import.stage", in)
	if err != nil {
		return imports.StageOut{}, err
	}
	return out.(imports.StageOut), nil
}

func (e *denv) mustStage(p ops.Principal, in imports.StageIn, body []byte) imports.StageOut {
	e.t.Helper()
	out, err := e.stage(p, in, body)
	if err != nil {
		e.t.Fatalf("import.stage: %v", err)
	}
	return out
}

func (e *denv) get(p ops.Principal, id string, preview bool) imports.DraftOut {
	e.t.Helper()
	return e.must(p, "import.draft.get", imports.DraftGetIn{ID: id, Preview: preview}).(imports.DraftOut)
}

func (e *denv) update(p ops.Principal, in imports.DraftUpdateIn) (imports.DraftUpdateOut, error) {
	out, err := e.call(p, "import.draft.update", in)
	if err != nil {
		return imports.DraftUpdateOut{}, err
	}
	return out.(imports.DraftUpdateOut), nil
}

func (e *denv) mustUpdate(p ops.Principal, in imports.DraftUpdateIn) imports.DraftUpdateOut {
	e.t.Helper()
	out, err := e.update(p, in)
	if err != nil {
		e.t.Fatalf("import.draft.update: %v", err)
	}
	return out
}

func (e *denv) stageDir(id string) string { return imports.StageDir(e.dataDir, id) }

func (e *denv) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// book is a Word document of three chapters, small and valid.
func book(t *testing.T, seed string) []byte {
	t.Helper()
	text := strings.Repeat(seed+" over the grey stones the gulls wheeled. ", 12)
	return miniDocx(t, map[string]string{"word/document.xml": body(
		para("Heading1", "The Start") + para("", text) + para("Heading1", "The Middle") + para("", text) + para("Heading1", "The End") + para("", text)),
		"word/styles.xml": headingStyles})
}

func code(err error) ops.Code {
	var oe *ops.Error
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

func reason(err error) string {
	var oe *ops.Error
	if errors.As(err, &oe) && oe.Data != nil {
		s, _ := oe.Data["reason"].(string)
		return s
	}
	return ""
}

func TestStageHoldsTheFileAndAnswersAnId(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	b := book(t, "tide")
	out := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "The Salt Road"}, b)
	if !strings.HasPrefix(out.StageID, "imd_") || out.Kind != "docx" || out.Bytes != int64(len(b)) || out.Revision != 1 || out.Existing || out.SHA256 == "" {
		t.Fatalf("stage: %+v", out)
	}
	got, err := os.ReadFile(filepath.Join(e.stageDir(out.StageID), "source"))
	if err != nil || !bytes.Equal(got, b) {
		t.Fatalf("the staged file: %v", err)
	}
	d := e.get(e.w, out.StageID, true)
	if d.Preview == nil || d.Preview.Layout != "docx" || len(d.Preview.Nodes) != 3 || d.Item.Status != "open" {
		t.Fatalf("draft: %+v", d)
	}
	if d.Preview.Nodes[0].Title != "The Start" {
		t.Errorf("the first chapter: %+v", d.Preview.Nodes[0])
	}
}

func TestStageRefusesWhatIsNotWhatItSaysAndWhatIsTooBig(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	cases := []struct {
		name string
		in   imports.StageIn
		body []byte
		want string
	}{
		{"a pdf that is not", imports.StageIn{Kind: "pdf"}, book(t, "x"), "not a PDF"},
		{"a docx that is text", imports.StageIn{Kind: "docx"}, []byte("just some words, not a zip"), "not a Word document"},
		{"a zip that is a pdf", imports.StageIn{Kind: "zip"}, []byte("%PDF-1.4 ..."), "not a readable zip"},
		{"an epub that is text", imports.StageIn{Kind: "epub"}, []byte("hello hello hello"), "not an EPUB"},
		{"an export that is a picture", imports.StageIn{Kind: "file"}, []byte("\x89PNG\r\n\x1a\n"), "not a WordPress export"},
		{"an empty file", imports.StageIn{Kind: "docx"}, nil, ""},
		{"no kind", imports.StageIn{}, book(t, "x"), "kind"},
		{"an unknown kind", imports.StageIn{Kind: "doc"}, book(t, "x"), "kind"},
		{"a pandoc file with no format", imports.StageIn{Kind: "pandoc"}, []byte("{\\rtf1 hello}"), "format"},
		{"a bad option", imports.StageIn{Kind: "docx", DocxHeadings: "sections"}, book(t, "x"), "docx_headings"},
		{"parent without project", imports.StageIn{Kind: "docx", Parent: "doc_1"}, book(t, "x"), "parent"},
	}
	for _, c := range cases {
		_, err := e.stage(e.w, c.in, c.body)
		if err == nil {
			t.Errorf("%s: staged", c.name)
			continue
		}
		if code(err) != ops.CodeInvalid && c.want != "" {
			t.Errorf("%s: %v", c.name, err)
		}
		if c.want != "" && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %q does not say %q", c.name, err, c.want)
		}
	}
	// Too big: a declared length over the limit is refused before a byte is read, and a stream that runs over is cut.
	big := bytes.Repeat([]byte("PK\x03\x04"), (35<<20)/4)
	if _, err := e.stage(e.w, imports.StageIn{Kind: "docx"}, big); code(err) != ops.CodeTooLarge {
		t.Errorf("a docx over 34 MiB: %v", err)
	}
	if _, err := e.stageR(e.w, imports.StageIn{Kind: "docx"}, bytes.NewReader(big), -1); code(err) != ops.CodeTooLarge {
		t.Errorf("a stream of unknown length over 34 MiB: %v", err)
	}
	if _, err := e.stage(e.w, imports.StageIn{Kind: "pdf"}, append([]byte("%PDF-"), bytes.Repeat([]byte("x"), 33<<20)...)); code(err) != ops.CodeTooLarge {
		t.Errorf("a pdf over 32 MiB: %v", err)
	}
	// None of it left anything behind.
	if ents, _ := os.ReadDir(filepath.Join(e.dataDir, "imports", "stage")); len(ents) != 0 {
		t.Errorf("refused files left %d folders in stage/", len(ents))
	}
	var n int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM import_drafts`).Scan(&n)
	if n != 0 {
		t.Errorf("refused files left %d draft rows", n)
	}
}

func TestStageOfIdenticalBytesIsTheDraftAlreadyOpen(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	b := book(t, "tide")
	a := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, b)
	again := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, b)
	if again.StageID != a.StageID || !again.Existing {
		t.Fatalf("a second stage of the same bytes: %+v (first %+v)", again, a)
	}
	ents, _ := os.ReadDir(filepath.Join(e.dataDir, "imports", "stage"))
	if len(ents) != 1 {
		t.Errorf("%d folders in stage/, want 1", len(ents))
	}
	// Another person's identical file is theirs; another target is another draft.
	other := e.user("other", ops.RoleWriter)
	if o := e.mustStage(other, imports.StageIn{Kind: "docx"}, b); o.StageID == a.StageID {
		t.Error("two people share a draft")
	}
}

func TestStageQuotas(t *testing.T) {
	// Open drafts.
	e := newDraftEnv(t, imports.StageLimits{MaxOpen: 2})
	e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "b"))
	_, err := e.stage(e.w, imports.StageIn{Kind: "docx"}, book(t, "c"))
	if code(err) != ops.CodeConflict || reason(err) != "open_drafts" || !strings.Contains(err.Error(), "Discard one of your open imports first") {
		t.Fatalf("a third open draft: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dataDir, "imports", "stage")); len(ents) != 2 {
		t.Errorf("the refused file stayed on disk: %d folders", len(ents))
	}
	// One person's bytes: a zip of incompressible data over a megabyte.
	pad := func(seed byte, n int) []byte {
		b := make([]byte, n)
		_, _ = rand.New(rand.NewSource(int64(seed))).Read(b) // incompressible, so the zip is the size of its pad
		return miniDocxWith(t, b)
	}
	e = newDraftEnv(t, imports.StageLimits{UserMB: 1, TotalMB: 100})
	e.mustStage(e.w, imports.StageIn{Kind: "docx"}, pad(1, 700<<10))
	_, err = e.stage(e.w, imports.StageIn{Kind: "docx"}, pad(2, 700<<10))
	if code(err) != ops.CodeConflict || reason(err) != "user_quota" {
		t.Fatalf("past one person's share: %v", err)
	}
	// Another person has a share of their own, and the whole instance has a total.
	e = newDraftEnv(t, imports.StageLimits{UserMB: 100, TotalMB: 1})
	e.mustStage(e.w, imports.StageIn{Kind: "docx"}, pad(1, 700<<10))
	other := e.user("other", ops.RoleWriter)
	_, err = e.stage(other, imports.StageIn{Kind: "docx"}, pad(2, 700<<10))
	if code(err) != ops.CodeConflict || reason(err) != "total_quota" {
		t.Fatalf("past the instance's total: %v", err)
	}
	// Discarding frees the room.
	list := e.must(e.w, "import.draft.list", struct{}{}).(imports.DraftListOut)
	e.must(e.w, "import.draft.discard", imports.DraftIDIn{ID: list.Items[0].ID})
	if _, err := e.stage(other, imports.StageIn{Kind: "docx"}, pad(2, 700<<10)); err != nil {
		t.Errorf("after a discard: %v", err)
	}
}

func TestDraftIsTheOwnersAlone(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	a := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	other := e.user("other", ops.RoleWriter)
	for _, c := range []struct {
		op string
		in any
	}{
		{"import.draft.get", imports.DraftGetIn{ID: a.StageID, Preview: true}},
		{"import.draft.update", imports.DraftUpdateIn{ID: a.StageID, BaseRevision: 1, Options: map[string]string{"docx_headings": "parts"}}},
		{"import.draft.discard", imports.DraftIDIn{ID: a.StageID}},
		{"import.preview", imports.PreviewIn{StageID: a.StageID}},
		{"import.apply", imports.ApplyIn{StageID: a.StageID, Rev: 1, Token: "imp_x"}},
	} {
		if _, err := e.call(other, c.op, c.in); code(err) != ops.CodeNotFound {
			t.Errorf("%s by another writer: %v, want not found", c.op, err)
		}
	}
	// A draft that does not exist looks the same.
	_, miss := e.call(other, "import.draft.get", imports.DraftGetIn{ID: "imd_00000000000000000000000000"})
	_, theirs := e.call(other, "import.draft.get", imports.DraftGetIn{ID: a.StageID})
	if code(miss) != ops.CodeNotFound || miss.Error() != theirs.Error() {
		t.Errorf("a missing draft (%v) and another's (%v) differ", miss, theirs)
	}
	list := e.must(other, "import.draft.list", struct{}{}).(imports.DraftListOut)
	if len(list.Items) != 0 {
		t.Errorf("another person's list holds %d drafts", len(list.Items))
	}
	if _, err := e.call(e.w, "import.draft.get", imports.DraftGetIn{ID: "../../etc/passwd"}); code(err) != ops.CodeInvalid {
		t.Errorf("a path as an id: %v", err)
	}
}

func TestEditsAreMadeOnACopyAndSavedOnlyIfThePreviewAccepts(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	d := e.get(e.w, s.StageID, true)
	keys := make([]string, len(d.Preview.Nodes))
	for i, n := range d.Preview.Nodes {
		keys[i] = n.Key
	}
	title := "Opening"
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: keys[0], Title: &title}})
	if u.Item.Revision != 2 || u.Preview.Nodes[0].Title != "Opening" || !u.Item.CanUndo {
		t.Fatalf("a rename: rev %d, %+v", u.Item.Revision, u.Preview.Nodes[0])
	}
	// A join into itself is refused, with the reason, and nothing is stored.
	self := keys[1]
	_, err := e.update(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 2, Edit: &imports.DraftEdit{Key: keys[1], MergeInto: &self}})
	if code(err) != ops.CodeInvalid {
		t.Errorf("a join into itself: %v", err)
	}
	// A move into what is inside it is refused by the preview: nest a chapter under another and the other under it.
	under := keys[1]
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 2, Edit: &imports.DraftEdit{Key: keys[2], Parent: &under}})
	back := keys[2]
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 3, Edit: &imports.DraftEdit{Key: keys[1], Parent: &back}}); err == nil {
		t.Error("a move into what is inside it was accepted")
	}
	after := e.get(e.w, s.StageID, false)
	if after.Item.Revision != 3 {
		t.Errorf("a refused change moved the revision to %d", after.Item.Revision)
	}
	// Undo restores the state before the last change.
	un := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 3, Undo: true})
	if un.Item.Revision != 4 || len(un.Item.Edits.Edits) != 1 {
		t.Errorf("undo: rev %d, edits %v", un.Item.Revision, un.Item.Edits.Edits)
	}
	if un.Note == nil || un.Note.Code != "undone" {
		t.Errorf("undo's note: %+v", un.Note)
	}
}

func TestMovesJoinsAndLeavingOutKeepTheirWords(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	d := e.get(e.w, s.StageID, true)
	k := func(i int) string { return d.Preview.Nodes[i].Key }
	rev := 1
	next := func(in imports.DraftUpdateIn) imports.DraftUpdateOut {
		in.ID, in.BaseRevision = s.StageID, rev
		out := e.mustUpdate(e.w, in)
		rev = out.Item.Revision
		return out
	}
	u := next(imports.DraftUpdateIn{Move: &imports.DraftMove{Key: k(2), To: "up"}})
	if u.Note.Code != "moved" || u.Note.Pos != 2 || u.Note.Count != 3 || u.Note.Other != "The Middle" || !u.Note.Above {
		// The End moved above The Middle: now 2 of 3, and its neighbour below it is The Middle.
		t.Errorf("a move's note: %+v", u.Note)
	}
	if got := u.Preview.Nodes[1].Title; got != "The End" {
		t.Errorf("the order after a move: %s", got)
	}
	// Join the middle chapter into the first: it leaves the list and is remembered by name.
	into := k(0)
	u = next(imports.DraftUpdateIn{Edit: &imports.DraftEdit{Key: k(1), MergeInto: &into}})
	if u.Note.Code != "joined" || u.Preview.Documents != 2 {
		t.Errorf("a join: %+v, %d documents", u.Note, u.Preview.Documents)
	}
	g := u.Item.Edits.Gone[k(1)]
	if g.Title != "The Middle" && g.Title != "The End" || g.Into != "The Start" {
		t.Errorf("what was joined is not remembered: %+v", u.Item.Edits.Gone)
	}
	// What a chapter is joined into cannot be left out until it is put back.
	skip := true
	_, err := e.update(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: rev, Edit: &imports.DraftEdit{Key: k(0), Skip: &skip}})
	if code(err) != ops.CodeConflict || reason(err) != "joined_into" {
		t.Errorf("leaving out what holds a join: %v", err)
	}
	no := false
	u = next(imports.DraftUpdateIn{Edit: &imports.DraftEdit{Key: g2key(u), Skip: &no}})
	if u.Preview.Documents != 3 || len(u.Item.Edits.Gone) != 0 {
		t.Errorf("put back: %d documents, gone %v", u.Preview.Documents, u.Item.Edits.Gone)
	}
	u = next(imports.DraftUpdateIn{Edit: &imports.DraftEdit{Key: k(0), Skip: &skip}})
	if u.Preview.Documents != 2 || u.Item.Edits.Gone[k(0)].Title != "The Start" {
		t.Errorf("leave out: %+v", u.Item.Edits.Gone)
	}
}

func g2key(u imports.DraftUpdateOut) string {
	for k := range u.Item.Edits.Gone {
		return k
	}
	return ""
}

func TestChangingHowTheFileIsReadKeepsTheEditsOfChaptersThatRemainAndUndoTakesItBack(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	d := e.get(e.w, s.StageID, true)
	title := "Opening"
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &title}})
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 2, Options: map[string]string{"docx_headings": "parts"}})
	// a Word document's chapters are keyed by the paragraph they start at: the three headings are still there, and so is the rename
	if u.Item.Options.DocxHeadings != "parts" || len(u.Item.Edits.Edits) != 1 || u.Note == nil || u.Note.Code != "recut" || u.Note.Kept != 1 {
		t.Fatalf("after an option: %+v %+v", u.Item, u.Note)
	}
	// cutting at nothing takes every chapter away: the edit no longer applies, and is listed
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 3, Options: map[string]string{"docx_split": "none"}})
	if len(u.Item.Edits.Edits) != 0 || len(u.Note.Dropped) != 1 || u.Note.Dropped[0] != "Opening" {
		t.Fatalf("an edit with no chapter: %+v", u.Note)
	}
	un := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 4, Undo: true})
	if un.Item.Options.DocxSplit != "" || len(un.Item.Edits.Edits) != 1 {
		t.Errorf("undo of an option: %+v", un.Item)
	}
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 5, Options: map[string]string{"nonsense": "x"}}); code(err) != ops.CodeInvalid {
		t.Errorf("an option that is not one: %v", err)
	}
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 5, Options: map[string]string{"docx_headings": "sections"}}); code(err) != ops.CodeInvalid {
		t.Errorf("a value that is not one: %v", err)
	}
	// The project's own fields are saved without a new revision.
	p := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 5, Project: &imports.ProjectEdit{Title: "A Salt Road", Kind: "novella", Author: "W. Writer"}})
	if p.Item.Revision != 5 || p.Item.Edits.Project.Title != "A Salt Road" {
		t.Errorf("project fields: rev %d, %+v", p.Item.Revision, p.Item.Edits.Project)
	}
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 5, Project: &imports.ProjectEdit{Kind: "saga"}}); code(err) != ops.CodeInvalid {
		t.Errorf("a kind that is not one: %v", err)
	}
}

func TestTwoTabsTheStaleOneIsRefusedAndTheDraftIsOneState(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	d := e.get(e.w, s.StageID, true)
	t1, t2 := "One", "Two"
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &t1}})
	_, err := e.update(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[1].Key, Title: &t2}})
	if code(err) != ops.CodeConflict || reason(err) != "revision" {
		t.Fatalf("the stale tab: %v", err)
	}
	now := e.get(e.w, s.StageID, true)
	if now.Preview.Nodes[1].Title == "Two" || now.Preview.Nodes[0].Title != "One" || now.Item.Revision != 2 {
		t.Errorf("the draft after a stale change: rev %d, %+v", now.Item.Revision, now.Preview.Nodes)
	}
}

func applyStaged(e *denv, id string) (imports.ApplyOut, error) {
	d := e.get(e.w, id, true)
	out, err := e.call(e.w, "import.apply", imports.ApplyIn{StageID: id, Rev: d.Item.Revision, Token: d.Preview.Token, Title: "Applied"})
	if err != nil {
		return imports.ApplyOut{}, err
	}
	return out.(imports.ApplyOut), nil
}

func TestApplyDeletesTheStageAndKeepsOnlyCounts(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "The Salt Road"}, book(t, "a"))
	title := "Opening"
	d := e.get(e.w, s.StageID, true)
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &title}})
	out, err := applyStaged(e, s.StageID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Documents != 3 || out.Title != "Applied" {
		t.Fatalf("applied: %+v", out)
	}
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut)
	var titles []string
	for _, m := range lo.Items {
		titles = append(titles, m.Title)
	}
	if !strings.Contains(strings.Join(titles, "|"), "Opening") {
		t.Errorf("the writer's rename did not reach the project: %v", titles)
	}
	if e.exists(e.stageDir(s.StageID)) {
		t.Error("the staged file is still there after the apply")
	}
	row := e.get(e.w, s.StageID, true)
	if row.Item.Status != "done" || row.Item.Result == nil || row.Item.Result.ProjectID != out.ProjectID || row.Item.Result.Documents != 3 || row.Preview != nil {
		t.Errorf("the finished draft: %+v", row.Item)
	}
	var edits, prev string
	_ = e.d.Read().QueryRow(`SELECT edits_json, prev_json FROM import_drafts WHERE id = ?`, s.StageID).Scan(&edits, &prev)
	if edits != "{}" || prev != "" {
		t.Errorf("the finished row still holds the writer's edits: %s %s", edits, prev)
	}
	// The project's source is recorded as always: the hash and the name, not the file.
	var n int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM project_sources WHERE project_id = ? AND kind = 'docx'`, out.ProjectID).Scan(&n)
	if n != 1 {
		t.Errorf("project_sources rows: %d", n)
	}
	// A second apply, from a tab that did not know, is told so and makes nothing.
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{StageID: s.StageID, Rev: 2, Token: "imp_x"}); code(err) != ops.CodeConflict {
		t.Errorf("a second apply: %v", err)
	}
	var projectsN int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM projects WHERE title = 'Applied'`).Scan(&projectsN)
	if projectsN != 1 {
		t.Errorf("%d projects after a double apply", projectsN)
	}
}

func TestADoublePressMakesOneProject(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	d := e.get(e.w, s.StageID, true)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok, refused int
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.call(e.w, "import.apply", imports.ApplyIn{StageID: s.StageID, Rev: d.Item.Revision, Token: d.Preview.Token, Title: "Twice"})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
			} else if code(err) == ops.CodeConflict {
				refused++
			} else {
				t.Errorf("apply: %v", err)
			}
		}()
	}
	wg.Wait()
	var n int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM projects WHERE title = 'Twice'`).Scan(&n)
	if ok != 1 || n != 1 || refused != 3 {
		t.Errorf("%d applies succeeded, %d refused, %d projects", ok, refused, n)
	}
}

func TestAStaleRevisionCannotApplyWhatItIsNotLookingAt(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	d := e.get(e.w, s.StageID, true)
	title := "Changed"
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: s.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &title}})
	_, err := e.call(e.w, "import.apply", imports.ApplyIn{StageID: s.StageID, Rev: 1, Token: d.Preview.Token})
	if code(err) != ops.CodeConflict || reason(err) != "revision" {
		t.Fatalf("an apply of revision 1 on revision 2: %v", err)
	}
	if e.get(e.w, s.StageID, false).Item.Status != "open" {
		t.Error("a refused apply left the draft not open")
	}
}

func TestAFailedApplyKeepsTheDraftUsable(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	d := e.get(e.w, s.StageID, true)
	_, err := e.call(e.w, "import.apply", imports.ApplyIn{StageID: s.StageID, Rev: d.Item.Revision, Token: "imp_not_the_preview", Title: "Nope"})
	if code(err) != ops.CodeConflict {
		t.Fatalf("an apply with another file's token: %v", err)
	}
	if got := e.get(e.w, s.StageID, true); got.Item.Status != "open" || got.Preview == nil || !e.exists(e.stageDir(s.StageID)) {
		t.Fatalf("after a failed apply: %+v", got.Item)
	}
	if _, err := applyStaged(e, s.StageID); err != nil {
		t.Errorf("the next try: %v", err)
	}
}

func TestApplyRefusesBytesAndEditsBesideAStageID(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	d := e.get(e.w, s.StageID, true)
	title := "x"
	cases := []struct {
		op string
		in any
	}{
		{"import.preview", imports.PreviewIn{StageID: s.StageID, Docx: book(t, "b")}},
		{"import.preview", imports.PreviewIn{StageID: s.StageID, Edits: []imports.Edit{{Key: "a", Title: &title}}}},
		{"import.preview", imports.PreviewIn{StageID: s.StageID, DocxHeadings: "parts"}},
		{"import.apply", imports.ApplyIn{StageID: s.StageID, Rev: 1, Token: d.Preview.Token, Docx: book(t, "b")}},
		{"import.apply", imports.ApplyIn{StageID: s.StageID, Rev: 1, Token: d.Preview.Token, Edits: []imports.Edit{{Key: "a", Title: &title}}}},
	}
	for _, c := range cases {
		_, err := e.call(e.w, c.op, c.in)
		if code(err) != ops.CodeInvalid || !strings.Contains(err.Error(), "not both") {
			t.Errorf("%s with a stage_id and a source: %v", c.op, err)
		}
	}
	// And the preview with only a stage_id is the draft's.
	pv := e.must(e.w, "import.preview", imports.PreviewIn{StageID: s.StageID}).(imports.PreviewOut)
	if pv.Documents != 3 || pv.Token != d.Preview.Token {
		t.Errorf("preview by stage_id: %d documents, token %s vs %s", pv.Documents, pv.Token, d.Preview.Token)
	}
}

func TestDiscardDeletesTheStage(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	if !e.exists(e.stageDir(s.StageID)) {
		t.Fatal("nothing staged")
	}
	e.must(e.w, "import.draft.discard", imports.DraftIDIn{ID: s.StageID})
	if e.exists(e.stageDir(s.StageID)) {
		t.Error("the staged file is still there after a discard")
	}
	if _, err := e.call(e.w, "import.draft.get", imports.DraftGetIn{ID: s.StageID}); code(err) != ops.CodeNotFound {
		t.Errorf("a discarded draft: %v", err)
	}
	// Nothing that was imported is touched: discard after an apply removes the row, not the project.
	s2 := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "b"))
	out, err := applyStaged(e, s2.StageID)
	if err != nil {
		t.Fatal(err)
	}
	e.must(e.w, "import.draft.discard", imports.DraftIDIn{ID: s2.StageID})
	if got := e.must(e.w, "doc.list", docs.ListIn{ProjectID: out.ProjectID}).(docs.ListOut); len(got.Items) != 3 {
		t.Errorf("discarding a finished draft changed the project: %d documents", len(got.Items))
	}
}

func TestExpiryRemovesTheFileAndTheRow(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{Hours: 72})
	fresh := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "fresh"))
	old := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "old"))
	n, err := imports.SweepDrafts(context.Background(), e.d, e.dataDir, e.lim, time.Now())
	if err != nil || n != 0 {
		t.Fatalf("a sweep of fresh drafts: %d, %v", n, err)
	}
	// 72 hours and a minute later, one has been touched and one has not.
	later := time.Now().Add(72*time.Hour + time.Minute)
	if _, err := e.d.Writer().Exec(`UPDATE import_drafts SET updated_at = ? WHERE id = ?`, db.Format(later.Add(-time.Hour)), fresh.StageID); err != nil {
		t.Fatal(err)
	}
	n, err = imports.SweepDrafts(context.Background(), e.d, e.dataDir, e.lim, later)
	if err != nil || n != 1 {
		t.Fatalf("the sweep: %d, %v", n, err)
	}
	if e.exists(e.stageDir(old.StageID)) {
		t.Error("an expired draft's file is still there")
	}
	if !e.exists(e.stageDir(fresh.StageID)) {
		t.Error("a draft in use was removed")
	}
	if _, err := e.call(e.w, "import.draft.get", imports.DraftGetIn{ID: old.StageID}); code(err) != ops.CodeNotFound {
		t.Errorf("an expired draft: %v", err)
	}
	// A finished draft's row goes after a week; an apply in progress that died goes with the idle limit.
	out, err := applyStaged(e, fresh.StageID)
	if err != nil {
		t.Fatal(err)
	}
	_ = out
	if n, _ := imports.SweepDrafts(context.Background(), e.d, e.dataDir, e.lim, time.Now().Add(6*24*time.Hour)); n != 0 {
		t.Errorf("a finished draft went before its week was up: %d", n)
	}
	if n, _ := imports.SweepDrafts(context.Background(), e.d, e.dataDir, e.lim, time.Now().Add(8*24*time.Hour)); n != 1 {
		t.Errorf("a finished draft stayed past its week: %d", n)
	}
}

func TestStartUpRecoversBothWays(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	rowOnly := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	kept := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "b"))
	// A restored backup has the row and not the folder.
	if err := os.RemoveAll(e.stageDir(rowOnly.StageID)); err != nil {
		t.Fatal(err)
	}
	// A crash between writing the file and inserting the row left a folder with no row; one younger than an hour may be a stage in progress.
	orphan := e.stageDir("imd_0000000000000000000000ORPH")
	young := e.stageDir("imd_0000000000000000000000YNG0")
	for _, d := range []string{orphan, young} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(d, "source"), []byte("PK"), 0o600)
	}
	past := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(orphan, past, past)
	n, err := imports.SweepDrafts(context.Background(), e.d, e.dataDir, e.lim, time.Now())
	if err != nil || n != 2 {
		t.Fatalf("recovery removed %d, %v", n, err)
	}
	if _, err := e.call(e.w, "import.draft.get", imports.DraftGetIn{ID: rowOnly.StageID}); code(err) != ops.CodeNotFound {
		t.Errorf("a draft whose folder is gone: %v", err)
	}
	if e.exists(orphan) {
		t.Error("a folder with no row stayed")
	}
	if !e.exists(young) || !e.exists(e.stageDir(kept.StageID)) {
		t.Error("recovery took a stage in progress or a good draft")
	}
}

func TestAGoneFileIsSaidSoAndTheDraftCanBeDiscarded(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	_ = os.Remove(filepath.Join(e.stageDir(s.StageID), "source"))
	_, err := e.call(e.w, "import.draft.get", imports.DraftGetIn{ID: s.StageID, Preview: true})
	if code(err) != ops.CodeConflict || reason(err) != "gone" {
		t.Fatalf("a draft whose file is gone: %v", err)
	}
	e.must(e.w, "import.draft.discard", imports.DraftIDIn{ID: s.StageID})
}

func TestAFolderFormZipIsReadWithWhatWasNotSent(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	z := zipOf(t, map[string]string{
		"01-start.md":             "# The Start\n\n" + strings.Repeat("tide came in. ", 30),
		"part-two/01-crossing.md": "# The Crossing\n\n" + strings.Repeat("ferry went out. ", 30),
		".scriobh-unsent.json":    `[{"path":"cover.png","size":2048},{"path":".git/config","size":12}]`,
	})
	s := e.mustStage(e.w, imports.StageIn{Kind: "zip", Origin: "folder", Name: "salt-road"}, z)
	if s.Kind != "folder" {
		t.Fatalf("kind %s", s.Kind)
	}
	d := e.get(e.w, s.StageID, true)
	if d.Preview == nil || d.Preview.SkippedTotal != 2 {
		t.Fatalf("the folder's preview: %+v", d.Preview)
	}
	reasons := map[string]string{}
	for _, k := range d.Preview.Skipped {
		reasons[k.Path] = k.Reason
	}
	if reasons["cover.png"] != "not a Markdown file" || reasons[".git/config"] != "hidden file or folder" {
		t.Errorf("why files were left out: %v", reasons)
	}
	// A lone subfolder of the folder is a part, not a wrapper to strip (a zip's single top folder would be).
	var kinds []string
	for _, n := range d.Preview.Nodes {
		kinds = append(kinds, n.Kind)
	}
	if !strings.Contains(strings.Join(kinds, ","), "part") {
		t.Errorf("a subfolder of a folder is a part: %v", kinds)
	}
}

func TestADraftOfAProjectIsGoneWhenTheProjectIsOrTheWriterCanNoLongerEditIt(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	pid := e.newProject(e.w, "Target")
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx", Project: pid}, book(t, "a"))
	if got := e.get(e.w, s.StageID, true); got.Preview == nil || got.Item.ProjectID != pid {
		t.Fatalf("a draft for a project: %+v", got.Item)
	}
	// A draft for a project one may not edit cannot be made, and says only not found.
	other := e.user("other", ops.RoleWriter)
	if _, err := e.stage(other, imports.StageIn{Kind: "docx", Project: pid}, book(t, "z")); code(err) != ops.CodeNotFound {
		t.Errorf("staging into another's project: %v", err)
	}
	// Previewing it through the append op names the same project; another project is not found.
	if _, err := e.call(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid, StageID: s.StageID}); err != nil {
		t.Errorf("the append preview of its own draft: %v", err)
	}
	pid2 := e.newProject(e.w, "Elsewhere")
	if _, err := e.call(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: pid2, StageID: s.StageID}); code(err) != ops.CodeNotFound {
		t.Errorf("a draft named for another project: %v", err)
	}
	// And the new-project preview is not for it.
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{StageID: s.StageID}); code(err) != ops.CodeNotFound {
		t.Errorf("a project's draft as a new project: %v", err)
	}
	d := e.get(e.w, s.StageID, true)
	out, err := e.call(e.w, "import.append", imports.AppendIn{ProjectID: pid, StageID: s.StageID, Rev: d.Item.Revision, Token: d.Preview.Token})
	if err != nil {
		t.Fatal(err)
	}
	if o := out.(imports.ApplyOut); o.ProjectID != pid || o.Documents != 3 {
		t.Errorf("added: %+v", o)
	}
	if e.exists(e.stageDir(s.StageID)) {
		t.Error("the stage stayed after adding to a project")
	}
}

func TestStageDirIsOutsideEverythingABackupHolds(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	s := e.mustStage(e.w, imports.StageIn{Kind: "docx"}, book(t, "a"))
	rel, err := filepath.Rel(e.dataDir, e.stageDir(s.StageID))
	if err != nil {
		t.Fatal(err)
	}
	for _, top := range []string{"files", "mirror", "conflicts"} {
		if rel == top || strings.HasPrefix(rel, top+string(filepath.Separator)) {
			t.Errorf("the stage is under %s/", top)
		}
	}
	if !strings.HasPrefix(rel, filepath.Join("imports", "stage")) {
		t.Errorf("the stage is at %s", rel)
	}
}

// miniDocxWith is a valid small Word document with a part of incompressible bytes in it, to make a file of a size.
func miniDocxWith(t *testing.T, pad []byte) []byte {
	t.Helper()
	return miniDocx(t, map[string]string{"word/document.xml": body(para("Heading1", "One") + para("", "text")), "word/styles.xml": headingStyles, "word/media/pad.bin": string(pad)})
}

func (e *denv) newProject(p ops.Principal, title string) string {
	e.t.Helper()
	return e.must(p, "project.create", projects.CreateIn{Title: title, Kind: "novel"}).(projects.Item).Item.ID
}
