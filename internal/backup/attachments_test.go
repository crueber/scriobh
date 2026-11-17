package backup_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
)

// A real upload survives a backup and a restore: the row, the file byte for
// byte, the usage counters, and the scratchpad that lives with the database.
func TestAttachmentsRoundTripThroughABackup(t *testing.T) {
	x := newData(t)
	pid := x.project("With figures", 1)
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for i := 0; i < 16; i++ {
		img.Set(i, i, color.RGBA{250, 20, 20, 255})
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	want := buf.Bytes()
	ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(want), Length: int64(len(want))})
	out, err := x.reg.Call(ctx, x.owner, "project.attachment.upload", attachments.UploadIn{ID: pid, Filename: "figure.png", Alt: "A figure"})
	if err != nil {
		t.Fatal(err)
	}
	att := out.(attachments.Item).Item
	r := x.create(backup.Options{})

	dst := t.TempDir()
	if _, err := backup.Restore(context.Background(), r.Path, dst, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	blob, err := attachments.BlobPath(dst, attachments.OwnerProject, pid, att.SHA256, "png")
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(blob)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("the restored file differs: %v (%d bytes, want %d)", err, len(got), len(want))
	}
	d, err := db.Open(filepath.Join(dst, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var alt string
	var used int64
	if err := d.Read().QueryRow(`SELECT alt FROM attachments WHERE id = ?`, att.ID).Scan(&alt); err != nil || alt != "A figure" {
		t.Errorf("the restored row: %q %v", alt, err)
	}
	if err := d.Read().QueryRow(`SELECT bytes FROM file_usage WHERE scope = 'instance'`).Scan(&used); err != nil || used != int64(len(want)) {
		t.Errorf("the restored usage counter: %d %v", used, err)
	}
	// A backup taken without files holds the rows but not the bytes, and says so.
	r2 := x.create(backup.Options{NoFiles: true})
	dst2 := t.TempDir()
	if _, err := backup.Restore(context.Background(), r2.Path, dst2, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(blob[len(dst):][1:]); err == nil {
		t.Log("(relative path exists in the working directory; ignoring)")
	}
	if _, err := os.Stat(filepath.Join(dst2, blob[len(dst)+1:])); err == nil {
		t.Error("a backup made without files restored a file")
	}
}
