package backup_test

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
)

// A project's own cover survives a backup and a restore: the layout, its pictures (attachments, byte for byte), and
// the hash; what was made from them for screens (files/projects/<id>/covers) is not archived and is made again on
// the restored server.
func TestACoverLayoutRoundTripsThroughABackup(t *testing.T) {
	x := newData(t)
	pid := x.project("With a cover", 1)
	img := image.NewRGBA(image.Rect(0, 0, 400, 600))
	for y := 0; y < 600; y++ {
		for px := 0; px < 400; px++ {
			img.Set(px, y, color.RGBA{uint8(px / 2), uint8(y / 3), 90, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(buf.Bytes()), Length: int64(buf.Len())})
	up, err := x.reg.Call(ctx, x.owner, "project.cover_layout.upload", coverlayout.UploadIn{ID: pid, Part: "front"})
	if err != nil {
		t.Fatal(err)
	}
	front := coverlayout.Side{Mode: "image", Attachment: up.(coverlayout.UploadOut).AttachmentID}
	v, err := x.reg.Call(context.Background(), x.owner, "project.cover_layout.set", coverlayout.SetIn{ID: pid, Shape: "sides", Front: &front, Alt: "A cover"})
	if err != nil {
		t.Fatal(err)
	}
	set := v.(coverlayout.View)
	if _, err := x.reg.Call(context.Background(), x.owner, "project.cover", cover.In{ID: pid}); err != nil { // makes the derivative
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(filepath.Join(x.dir, "files", "projects", pid, "covers")); len(ents) == 0 {
		t.Fatal("no derivative was made")
	}
	r := x.create(backup.Options{})
	zr, err := zip.OpenReader(r.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if strings.Contains(f.Name, "/covers/") {
			t.Errorf("the archive holds a derived picture: %s", f.Name)
		}
	}
	zr.Close()

	dst := t.TempDir()
	if _, err := backup.Restore(context.Background(), r.Path, dst, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(dst, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	reg2 := registry.Build(d, events.NewStore(d, nil), dst)
	got, err := reg2.Call(context.Background(), x.owner, "project.cover_layout.get", coverlayout.ProjectIn{ID: pid})
	if err != nil {
		t.Fatal(err)
	}
	l := got.(coverlayout.Item).Layout
	if l == nil || l.Hash != set.Hash || l.Revision != set.Revision || l.Alt != "A cover" || l.Front.Attachment != front.Attachment {
		t.Fatalf("the restored layout: %+v", l)
	}
	var refs, clean int
	if err := d.Read().QueryRow(`SELECT (SELECT count(*) FROM attachment_refs WHERE ref_kind = 'cover'), (SELECT cover_clean FROM attachments WHERE id = ?)`, front.Attachment).Scan(&refs, &clean); err != nil || refs != 1 || clean != 1 {
		t.Errorf("restored refs %d, cover_clean %d, %v", refs, clean, err)
	}
	f, err := reg2.Call(context.Background(), x.owner, "project.cover", cover.In{ID: pid})
	if err != nil {
		t.Fatalf("the restored cover: %v", err)
	}
	if ff := f.(cover.File); ff.ContentType != "image/jpeg" || len(ff.Body) == 0 {
		t.Errorf("the restored cover is %s", ff.ContentType)
	}
	if ents, _ := os.ReadDir(filepath.Join(dst, "files", "projects", pid, "covers")); len(ents) != 1 {
		t.Errorf("the derivative was not made again: %d files", len(ents))
	}
}
