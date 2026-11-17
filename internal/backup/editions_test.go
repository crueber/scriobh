package backup_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/ops"
)

// An edition (the source file of an imported book, its hash, its chapters and what the converter made of
// each) travels with a backup and comes back whole; a backup made without files holds the rows and not the
// book, and the restored edition then says the file is not there instead of serving something else.
func TestEditionsRoundTripThroughABackup(t *testing.T) {
	x := newData(t)
	ctx := context.Background()
	pid := x.project("An imported book", 2)
	book := []byte("PK the book, exactly as it was received, in bytes that are not really a zip")
	st, err := editions.Stage(ctx, x.reg.Env(), attachments.DefaultLimits, x.owner.UserID, pid, book, "epub", false)
	if err != nil {
		t.Fatal(err)
	}
	st.Lock()
	var ed editions.Edition
	err = x.d.WithTx(ctx, func(tx *sql.Tx) error {
		c := &ops.Ctx{Context: ctx, Env: x.reg.Env(), P: x.owner}
		if err := st.Place(c, tx, attachments.DefaultLimits); err != nil {
			return err
		}
		var err error
		ed, err = editions.Insert(tx, editions.New{ProjectID: pid, Kind: editions.KindEPUB, Label: "First edition", Identifier: "urn:isbn:1", SHA256: st.SHA256, Size: st.Size, By: x.owner.UserID,
			Metadata: map[string]any{"title": "An imported book"},
			Chapters: []editions.NewChapter{{Kind: "chapter", Title: "One", SourcePath: "OEBPS/one.xhtml", Words: 3, Baseline: "Three little words.\n"}}}, true)
		return err
	})
	st.Done(err == nil)
	if err != nil {
		t.Fatal(err)
	}
	r := x.create(backup.Options{})
	dst := t.TempDir()
	if _, err := backup.Restore(ctx, r.Path, dst, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dst, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, err := editions.Get(ctx, d.Read(), pid, ed.ID)
	if err != nil || got.Label != "First edition" || got.SHA256 != editions.SumOf(book) || !got.HasFile || got.Chapters != 1 || got.Metadata["title"] != "An imported book" {
		t.Fatalf("the restored edition: %+v %v", got, err)
	}
	if b, err := editions.Baseline(ctx, d.Read(), ed.ID, 1); err != nil || b != "Three little words.\n" {
		t.Errorf("the restored baseline: %q %v", b, err)
	}
	env := &ops.Env{DB: d, DataDir: dst}
	if b, err := editions.Open(env, got); err != nil || string(b) != string(book) {
		t.Errorf("the restored source file: %v", err)
	}
	var used int64
	if err := d.Read().QueryRow(`SELECT bytes FROM file_usage WHERE scope = ?`, "owner:"+pid).Scan(&used); err != nil || used != int64(len(book)) {
		t.Errorf("the restored usage counter: %d %v", used, err)
	}
	// Without files: the rows, not the book.
	r2 := x.create(backup.Options{NoFiles: true})
	dst2 := t.TempDir()
	if _, err := backup.Restore(ctx, r2.Path, dst2, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	d2, err := db.Open(filepath.Join(dst2, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	got2, err := editions.Get(ctx, d2.Read(), pid, ed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst2, "files", "projects", pid, "editions", got2.SHA256+".epub")); err == nil {
		t.Error("a backup made without files restored a file")
	}
	if _, err := editions.Open(&ops.Env{DB: d2, DataDir: dst2}, got2); err == nil {
		t.Error("an edition without its file opened")
	}
}
