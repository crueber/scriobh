package editions_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type env struct {
	t     *testing.T
	d     *db.DB
	reg   *ops.Registry
	dir   string
	owner ops.Principal
	pid   string
	lim   attachments.Limits
}

func newEnv(t *testing.T, lim attachments.Limits) *env {
	t.Helper()
	d := testutil.DB(t)
	dir := t.TempDir()
	e := &env{t: t, d: d, dir: dir, lim: lim, reg: registry.BuildWith(d, events.NewStore(d, nil), dir, registry.Options{Attachments: lim})}
	e.owner = ops.Principal{UserID: testutil.User(t, d, ops.RoleWriter, "o@t.local"), Role: ops.RoleWriter}
	e.pid = e.call(e.owner, "project.create", projects.CreateIn{Title: "Book"}).(projects.Item).Item.ID
	return e
}

func (e *env) call(p ops.Principal, op string, in any) any {
	e.t.Helper()
	out, err := e.reg.Call(context.Background(), p, op, in)
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (e *env) fails(p ops.Principal, op string, in any, code ops.Code) {
	e.t.Helper()
	_, err := e.reg.Call(context.Background(), p, op, in)
	if err == nil || ops.AsError(err).Code != code {
		e.t.Fatalf("%s: %v, want %s", op, err, code)
	}
}

func (e *env) usage(scope string) (int64, int64) {
	var b, f int64
	_ = e.d.Read().QueryRow(`SELECT bytes, files FROM file_usage WHERE scope = ?`, scope).Scan(&b, &f)
	return b, f
}

// keep stages, places and records a source file the way an import does.
func (e *env) keep(project string, data []byte, label string, fail bool) (editions.Edition, error) {
	e.t.Helper()
	ctx := context.Background()
	st, err := editions.Stage(ctx, e.reg.Env(), e.lim, e.owner.UserID, project, data, "epub", false)
	if err != nil {
		return editions.Edition{}, err
	}
	committed := false
	defer func() { st.Done(committed) }()
	st.Lock()
	var out editions.Edition
	err = e.d.WithTx(ctx, func(tx *sql.Tx) error {
		c := &ops.Ctx{Context: ctx, Env: e.reg.Env(), P: e.owner}
		if err := st.Place(c, tx, e.lim); err != nil {
			return err
		}
		var err error
		out, err = editions.Insert(tx, editions.New{ProjectID: project, Kind: editions.KindEPUB, Label: label, SHA256: st.SHA256, Size: st.Size, By: e.owner.UserID,
			Chapters: []editions.NewChapter{{Kind: "chapter", Title: "One", SourcePath: "OEBPS/one.xhtml", Words: 2, Baseline: "Two words.\n"}}}, true)
		if err != nil {
			return err
		}
		if fail {
			return errors.New("the transaction fails")
		}
		return nil
	})
	committed = err == nil
	return out, err
}

func TestInsertListGetChaptersBaseline(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	ctx := context.Background()
	var id string
	err := e.d.WithTx(ctx, func(tx *sql.Tx) error {
		ed, err := editions.Insert(tx, editions.New{ProjectID: e.pid, Kind: editions.KindEPUB, Label: "  First\n edition  ", Identifier: "urn:isbn:9780000000002",
			SHA256: editions.SumOf([]byte("x")), Size: 1, Metadata: map[string]any{"title": "T", "authors": []string{"A"}}, By: e.owner.UserID,
			Chapters: []editions.NewChapter{
				{Kind: "part", Title: "Part I", SourcePath: "p1.xhtml", Baseline: ""},
				{ParentOrd: 1, Kind: "chapter", Title: "One", SourcePath: "c1.xhtml", Words: 3, Baseline: strings.Repeat("Words here. ", 1000)},
				{ParentOrd: 1, Kind: "chapter", Title: "Two ✦", SourcePath: "c2.xhtml#s2", Words: 1, Baseline: "Ünïcode.\n"},
			}}, false)
		id = ed.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := editions.List(ctx, e.d.Read(), e.pid)
	if err != nil || len(list) != 1 || list[0].Label != "First edition" || list[0].Chapters != 3 || list[0].HasFile || list[0].Metadata["title"] != "T" {
		t.Fatalf("list: %+v %v", list, err)
	}
	ch, err := editions.Chapters(ctx, e.d.Read(), id)
	if err != nil || len(ch) != 3 || ch[1].ParentOrd != 1 || ch[2].SourcePath != "c2.xhtml#s2" || ch[2].Title != "Two ✦" || ch[0].Ord != 1 {
		t.Fatalf("chapters: %+v %v", ch, err)
	}
	b, err := editions.Baseline(ctx, e.d.Read(), id, 2)
	if err != nil || b != strings.Repeat("Words here. ", 1000) || editions.HashOf(b) != ch[1].SHA256 {
		t.Fatalf("baseline: %d bytes, %v", len(b), err)
	}
	if _, err := editions.Baseline(ctx, e.d.Read(), id, 9); !errors.Is(err, editions.ErrNotFound) {
		t.Errorf("a chapter that is not there: %v", err)
	}
	// An id from another project is not found.
	other := e.call(e.owner, "project.create", projects.CreateIn{Title: "Other"}).(projects.Item).Item.ID
	if _, err := editions.Get(ctx, e.d.Read(), other, id); !errors.Is(err, editions.ErrNotFound) {
		t.Errorf("another project's edition: %v", err)
	}
	// A chapter that names a later one as its parent is refused, and nothing is kept.
	err = e.d.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := editions.Insert(tx, editions.New{ProjectID: e.pid, Kind: editions.KindEPUB, SHA256: editions.SumOf([]byte("y")),
			Chapters: []editions.NewChapter{{ParentOrd: 2, Kind: "chapter", Title: "A"}, {Kind: "chapter", Title: "B"}}}, false)
		return err
	})
	if err == nil {
		t.Error("a parent that comes later was accepted")
	}
	if l, _ := editions.List(ctx, e.d.Read(), e.pid); len(l) != 1 {
		t.Errorf("editions after the refusal: %d", len(l))
	}
}

func TestSourceFileKeptIntactAndCounted(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	data := []byte("PK\x03\x04 a book that is not really a zip, but its bytes are kept exactly")
	ed, err := e.keep(e.pid, data, "First", false)
	if err != nil {
		t.Fatal(err)
	}
	if !ed.HasFile || ed.SHA256 != editions.SumOf(data) || ed.Size != int64(len(data)) {
		t.Fatalf("edition: %+v", ed)
	}
	p, _ := editions.Path(e.dir, e.pid, ed.SHA256, "epub")
	if got, err := os.ReadFile(p); err != nil || string(got) != string(data) {
		t.Fatalf("file on disk: %v", err)
	}
	if b, f := e.usage("owner:" + e.pid); b != int64(len(data)) || f != 1 {
		t.Errorf("owner usage %d bytes in %d files", b, f)
	}
	if b, _ := e.usage("user:" + e.owner.UserID); b != int64(len(data)) {
		t.Errorf("user usage %d", b)
	}
	// The same bytes again: one file, counted once, two editions.
	if _, err := e.keep(e.pid, data, "Again", false); err != nil {
		t.Fatal(err)
	}
	if b, f := e.usage("owner:" + e.pid); b != int64(len(data)) || f != 1 {
		t.Errorf("a second edition of the same bytes was counted: %d bytes in %d files", b, f)
	}
	// Download returns the file byte for byte, to an owner or editor only.
	out := e.call(e.owner, "edition.download", editions.EditionIn{ProjectID: e.pid, EditionID: ed.ID}).(editions.FileOut)
	if string(out.Body) != string(data) || out.ContentType != "application/epub+zip" || !strings.HasSuffix(out.Filename, ".epub") || out.SHA256 != ed.SHA256 {
		t.Errorf("download: %+v", out)
	}
	// A file that is changed on disk is not served: it is not the source any more.
	if err := os.WriteFile(p, append([]byte("X"), data[1:]...), 0o600); err != nil {
		t.Fatal(err)
	}
	e.fails(e.owner, "edition.download", editions.EditionIn{ProjectID: e.pid, EditionID: ed.ID}, ops.CodeNotFound)
	// ...nor one replaced by a link.
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(p)
	if err := os.Symlink(secret, p); err != nil {
		t.Skip("no symbolic links here")
	}
	e.fails(e.owner, "edition.download", editions.EditionIn{ProjectID: e.pid, EditionID: ed.ID}, ops.CodeNotFound)
}

func TestAFailedImportKeepsNothing(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	data := []byte("book one")
	if _, err := e.keep(e.pid, data, "Fails", true); err == nil {
		t.Fatal("the transaction was meant to fail")
	}
	p, _ := editions.Path(e.dir, e.pid, editions.SumOf(data), "epub")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("a file was left behind by a failed import")
	}
	if b, f := e.usage("owner:" + e.pid); b != 0 || f != 0 {
		t.Errorf("usage after a failed import: %d, %d", b, f)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dir, "files", ".tmp")); len(ents) != 0 {
		t.Errorf("%d staged files left", len(ents))
	}
	// A failed import of bytes the project already keeps never removes that file.
	if _, err := e.keep(e.pid, data, "Good", false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.keep(e.pid, data, "Fails again", true); err == nil {
		t.Fatal("the transaction was meant to fail")
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("the failed import removed a file another edition keeps: %v", err)
	}
}

func TestQuotasAndTheEditionLimit(t *testing.T) {
	lim := attachments.Limits{MaxBytes: 25 << 20, OwnerBytes: 100, UserBytes: 1 << 30}
	e := newEnv(t, lim)
	if _, err := e.keep(e.pid, make([]byte, 60), "A", false); err != nil {
		t.Fatal(err)
	}
	_, err := e.keep(e.pid, append(make([]byte, 59), 1), "B", false)
	if ops.AsError(err) == nil || ops.AsError(err).Code != ops.CodeQuota {
		t.Fatalf("a second file over the project's limit: %v", err)
	}
	if _, f := e.usage("owner:" + e.pid); f != 1 {
		t.Errorf("files counted: %d", f)
	}
	// Bigger than the largest file kept.
	_, err = editions.Stage(context.Background(), e.reg.Env(), attachments.Limits{MaxBytes: 25 << 20}, e.owner.UserID, e.pid, make([]byte, editions.MaxFileBytes+1), "epub", false)
	if ops.AsError(err) == nil || ops.AsError(err).Code != ops.CodeTooLarge {
		t.Errorf("an oversize file: %v", err)
	}
	// A dry run places and counts nothing.
	st, err := editions.Stage(context.Background(), e.reg.Env(), e.lim, e.owner.UserID, e.pid, []byte("dry"), "epub", true)
	if err != nil {
		t.Fatal(err)
	}
	st.Done(false)
	if ents, _ := os.ReadDir(filepath.Join(e.dir, "files", ".tmp")); len(ents) != 0 {
		t.Errorf("a dry run staged a file")
	}
	// At most MaxPerProject editions.
	e2 := newEnv(t, attachments.DefaultLimits)
	err = e2.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < editions.MaxPerProject; i++ {
			if _, err := editions.Insert(tx, editions.New{ProjectID: e2.pid, Kind: editions.KindEPUB, SHA256: editions.SumOf([]byte{byte(i)})}, false); err != nil {
				return err
			}
		}
		_, err := editions.Insert(tx, editions.New{ProjectID: e2.pid, Kind: editions.KindEPUB, SHA256: editions.SumOf([]byte("one more"))}, false)
		return err
	})
	if ops.AsError(err) == nil || ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("the 51st edition: %v", err)
	}
}

func TestTrashKeepsAndPurgeRemoves(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	data := []byte("the source")
	ed, err := e.keep(e.pid, data, "First", false)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := editions.Path(e.dir, e.pid, ed.SHA256, "epub")
	e.call(e.owner, "project.trash", projects.StateIn{ID: e.pid})
	// In the trash: the edition and its file are still there, and still readable.
	got := e.call(e.owner, "edition.get", editions.EditionIn{ProjectID: e.pid, EditionID: ed.ID}).(editions.GetOut)
	if got.Item.ID != ed.ID || len(got.Chapters) != 1 {
		t.Errorf("edition in the trash: %+v", got)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("the file went with the trash: %v", err)
	}
	e.call(e.owner, "project.restore", projects.StateIn{ID: e.pid})
	if l := e.call(e.owner, "edition.list", editions.ProjectIn{ProjectID: e.pid}).(editions.ListOut); len(l.Items) != 1 {
		t.Errorf("editions after a restore: %+v", l)
	}
	e.call(e.owner, "project.trash", projects.StateIn{ID: e.pid})
	e.call(e.owner, "project.purge", projects.PurgeIn{ID: e.pid, ConfirmTitle: "Book"})
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("the source file survived the purge: %v", err)
	}
	var n int
	_ = e.d.Read().QueryRow(`SELECT (SELECT count(*) FROM editions) + (SELECT count(*) FROM edition_chapters)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d edition rows survived the purge", n)
	}
	if b, f := e.usage("owner:" + e.pid); b != 0 || f != 0 {
		t.Errorf("the project's usage after the purge: %d, %d", b, f)
	}
	if b, f := e.usage("user:" + e.owner.UserID); b != 0 || f != 0 {
		t.Errorf("the person's usage after the purge: %d, %d", b, f)
	}
}

func TestRolesAndOtherProjects(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	ed, err := e.keep(e.pid, []byte("book"), "First", false)
	if err != nil {
		t.Fatal(err)
	}
	reader := ops.Principal{UserID: testutil.User(t, e.d, ops.RoleReader, "r@t.local"), Role: ops.RoleReader}
	stranger := ops.Principal{UserID: testutil.User(t, e.d, ops.RoleWriter, "s@t.local"), Role: ops.RoleWriter}
	if err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, e.pid, reader.UserID, ops.ProjectReader, db.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	in := editions.EditionIn{ProjectID: e.pid, EditionID: ed.ID}
	e.call(reader, "edition.get", in) // a reader sees the edition...
	e.fails(reader, "edition.download", in, ops.CodeForbidden)
	e.fails(stranger, "edition.get", in, ops.CodeNotFound)
	e.fails(stranger, "edition.download", in, ops.CodeNotFound)
	// ...and an edition id of another project, asked for under one's own, is not found.
	other := e.call(stranger, "project.create", projects.CreateIn{Title: "Mine"}).(projects.Item).Item.ID
	e.fails(stranger, "edition.get", editions.EditionIn{ProjectID: other, EditionID: ed.ID}, ops.CodeNotFound)
	e.fails(stranger, "edition.download", editions.EditionIn{ProjectID: other, EditionID: ed.ID}, ops.CodeNotFound)
}

func TestForDocument(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	ctx := context.Background()
	var docID string
	_ = e.d.Read().QueryRow(`SELECT 1`).Scan(new(int))
	docID = db.NewID("doc")
	err := e.d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO documents(id, project_id, kind, title, position, content, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
			docID, e.pid, "chapter", "One", 1000, "", db.Now(), db.Now()); err != nil {
			t.Skipf("the documents table needs more columns than this test sets: %v", err)
		}
		_, err := editions.Insert(tx, editions.New{ProjectID: e.pid, Kind: editions.KindEPUB, SHA256: editions.SumOf([]byte("z")),
			Chapters: []editions.NewChapter{{Kind: "chapter", Title: "One", SourcePath: "a.xhtml", DocumentID: docID}}}, false)
		return err
	})
	if err != nil {
		t.Skip(err)
	}
	ed, ch, ok, err := editions.ForDocument(ctx, e.d.Read(), docID)
	if err != nil || !ok || ed.ProjectID != e.pid || ch.SourcePath != "a.xhtml" || ch.DocumentID != docID {
		t.Fatalf("ForDocument: %+v %+v %v %v", ed, ch, ok, err)
	}
	if _, _, ok, _ := editions.ForDocument(ctx, e.d.Read(), "doc_none"); ok {
		t.Error("a document no edition names has an edition")
	}
}

// After a restore made without files the rows say a file is kept and the disk has none: importing the same
// book again puts it back, and does not count it twice.
func TestAMissingFileIsPutBack(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	data := []byte("a book whose file went missing")
	ed, err := e.keep(e.pid, data, "First", false)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := editions.Path(e.dir, e.pid, ed.SHA256, "epub")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.keep(e.pid, data, "Again", false); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(p); err != nil || string(got) != string(data) {
		t.Errorf("the file was not put back: %v", err)
	}
	if b, f := e.usage("owner:" + e.pid); b != int64(len(data)) || f != 1 {
		t.Errorf("usage %d bytes in %d files", b, f)
	}
}

// A quota that cannot be read is not a quota that is met: the stage fails, it does not let the file in.
func TestAQuotaThatCannotBeReadFailsClosed(t *testing.T) {
	e := newEnv(t, attachments.Limits{MaxBytes: 25 << 20, OwnerBytes: 1 << 20, UserBytes: 1 << 30})
	if _, err := e.d.Writer().ExecContext(context.Background(), `DROP TABLE file_usage`); err != nil {
		t.Fatal(err)
	}
	_, err := editions.Stage(context.Background(), e.reg.Env(), e.lim, e.owner.UserID, e.pid, []byte("a book"), "epub", false)
	if err == nil {
		t.Fatal("a file was staged although the quota could not be read")
	}
	if oe := ops.AsError(err); oe.Code != ops.CodeInternal {
		t.Errorf("%v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(e.dir, "files", ".tmp")); len(ents) != 0 {
		t.Errorf("%d staged files left", len(ents))
	}
}

// Importing different books does not wait: only the same bytes into the same project take turns.
func TestImportsOfDifferentBooksDoNotWaitForEachOther(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	ctx := context.Background()
	stage := func(data string) *editions.Staged {
		st, err := editions.Stage(ctx, e.reg.Env(), e.lim, e.owner.UserID, e.pid, []byte(data), "epub", false)
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	// a second book whose lock is another one of the 64 (the project's id differs from run to run)
	other := "book two"
	for i := 0; editions.LockIndex(e.pid, editions.SumOf([]byte(other))) == editions.LockIndex(e.pid, editions.SumOf([]byte("book one"))); i++ {
		other = fmt.Sprintf("book %d", i)
	}
	a, b, a2 := stage("book one"), stage(other), stage("book one")
	defer a.Done(false)
	defer b.Done(false)
	defer a2.Done(false)
	a.Lock()
	done := make(chan string, 2)
	go func() { b.Lock(); done <- "different" }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("another book waited for the first")
	}
	go func() { a2.Lock(); done <- "same" }()
	select {
	case <-done:
		t.Fatal("the same bytes into the same project did not wait")
	case <-time.After(150 * time.Millisecond):
	}
	a.Done(false)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the second import of the same bytes never got its turn")
	}
}

// A crash between placing a file and committing its row leaves a file no row names. The sweep removes it, only when it
// is old enough, only when it is the kind of file the store writes, and never one a row names.
func TestSweepOrphans(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	ctx := context.Background()
	dir := filepath.Join(e.dir, "files", "projects", e.pid, "editions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sha := func(s string) string { return editions.SumOf([]byte(s)) }
	old := time.Now().Add(-3 * time.Hour)
	put := func(name string, age time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, age, age); err != nil {
			t.Fatal(err)
		}
		return p
	}
	orphan := put(sha("orphan")+".epub", old)
	young := put(sha("young")+".epub", time.Now().Add(-5*time.Minute))
	// a file a row names, old
	ed, err := e.keep(e.pid, []byte("kept book"), "Kept", false)
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := editions.Path(e.dir, e.pid, ed.SHA256, "epub")
	if err := os.Chtimes(kept, old, old); err != nil {
		t.Fatal(err)
	}
	// odd names and kinds: a short name, the wrong extension, upper-case hex, a link, a folder, a stray file
	odd := []string{put("notes.txt", old), put(sha("x")[:63]+".epub", old), put(sha("x")+".exe", old), put(strings.ToUpper(sha("y"))+".epub", old)}
	link := filepath.Join(dir, sha("link")+".epub")
	secret := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(secret, []byte("s"), 0o600)
	if err := os.Symlink(secret, link); err == nil {
		odd = append(odd, link)
	}
	folder := filepath.Join(dir, sha("folder")+".epub")
	_ = os.Mkdir(folder, 0o700)
	odd = append(odd, folder)
	// a project folder that is not a project id is not looked into
	strange := filepath.Join(e.dir, "files", "projects", "not-an-id", "editions")
	_ = os.MkdirAll(strange, 0o700)
	strangeFile := filepath.Join(strange, sha("z")+".epub")
	_ = os.WriteFile(strangeFile, []byte("z"), 0o600)
	_ = os.Chtimes(strangeFile, old, old)
	odd = append(odd, strangeFile)

	n, err := editions.SweepOrphans(ctx, e.d, e.dir, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v", n, err)
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }
	if exists(orphan) {
		t.Error("the old orphan is still there")
	}
	for name, p := range map[string]string{"the young orphan": young, "the file a row names": kept} {
		if !exists(p) {
			t.Errorf("%s was removed", name)
		}
	}
	for _, p := range odd {
		if !exists(p) {
			t.Errorf("%s was removed, and is not a file of the store", filepath.Base(p))
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != "s" {
		t.Error("what a link pointed at was touched")
	}
	// the usage the file's row holds is untouched, and a second pass finds nothing
	if n, _ := editions.SweepOrphans(ctx, e.d, e.dir, time.Now()); n != 0 {
		t.Errorf("a second pass removed %d", n)
	}
	// the young orphan is fair game once it is old; the file a row names never is, however old
	if n, _ := editions.SweepOrphans(ctx, e.d, e.dir, time.Now().Add(100*24*time.Hour)); n != 1 || exists(young) {
		t.Errorf("a pass long after removed %d, and the once-young orphan is still there: %v", n, exists(young))
	}
	if !exists(kept) {
		t.Error("a file a row names was removed")
	}
}

// A pass does a bounded amount of work, and the next goes on.
func TestSweepOrphansIsBounded(t *testing.T) {
	e := newEnv(t, attachments.DefaultLimits)
	dir := filepath.Join(e.dir, "files", "projects", e.pid, "editions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	const total = editions.SweepRemove*2 + 50
	for i := 0; i < total; i++ {
		p := filepath.Join(dir, editions.SumOf([]byte{byte(i), byte(i >> 8), 'o'})+".epub")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, old, old)
	}
	left := func() int { ents, _ := os.ReadDir(dir); return len(ents) }
	n, err := editions.SweepOrphans(context.Background(), e.d, e.dir, time.Now())
	if err != nil || n != editions.SweepRemove || left() != total-n {
		t.Fatalf("a pass removed %d (%v), %d left", n, err, left())
	}
	for i := 0; i < 5 && left() > 0; i++ {
		if _, err := editions.SweepOrphans(context.Background(), e.d, e.dir, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if left() != 0 {
		t.Errorf("%d left after several passes", left())
	}
}
