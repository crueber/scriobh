package export_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/ops"
)

func (f *fx) ownCover(alt string) {
	f.t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 800, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 800; x++ {
			img.Set(x, y, color.RGBA{uint8(x / 4), 90, uint8(y / 5), 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	ctx := ops.WithRawBody(f.ctx, ops.RawBody{R: bytes.NewReader(b.Bytes()), Length: int64(b.Len())})
	up, err := f.reg.Call(ctx, f.owner, "project.cover_layout.upload", coverlayout.UploadIn{ID: f.pid, Part: "front"})
	if err != nil {
		f.t.Fatal(err)
	}
	front := coverlayout.Side{Mode: "image", Attachment: up.(coverlayout.UploadOut).AttachmentID}
	if _, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.set", coverlayout.SetIn{ID: f.pid, Shape: "sides", Front: &front, Alt: alt}); err != nil {
		f.t.Fatal(err)
	}
}

// With a cover of its own the HTML export shows it instead of the generated one: a data URI in a single file, and a
// file of the zip (images/cover.jpg) in a bundle, whose title page may load it and whose other pages may not.
func TestHTMLExportUsesTheProjectsOwnCover(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.doc("Chapter", "chapter", "", "Words.")
	plain := f.mustHTML(export.HTMLIn{Mode: "single"})
	if !strings.Contains(string(plain.Body), "<figure class=\"cover\"><svg") {
		t.Fatal("no generated cover without a layout")
	}
	f.ownCover("A door in the snow")
	single := f.mustHTML(export.HTMLIn{Mode: "single"})
	html := string(single.Body)
	m := regexp.MustCompile(`<figure class="cover"><img src="data:image/jpeg;base64,([A-Za-z0-9+/=]+)" alt="A door in the snow" width="(\d+)" height="(\d+)"></figure>`).FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("the single file has no cover image:\n%.600s", html)
	}
	raw, err := base64.StdEncoding.DecodeString(m[1])
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width != 800 || cfg.Height != 1200 || m[2] != "800" || m[3] != "1200" {
		t.Errorf("%v %+v %s %s", err, cfg, m[2], m[3])
	}
	if strings.Contains(html, "<figure class=\"cover\"><svg") {
		t.Error("the generated cover is still on the page")
	}
	if !strings.Contains(html, "img-src data:;") || strings.Contains(html, "'self'") {
		t.Error("the single file's policy is no wider than inline images")
	}

	bundle := f.mustHTML(export.HTMLIn{Mode: "bundle"})
	zr, err := zip.NewReader(bytes.NewReader(bundle.Body), int64(len(bundle.Body)))
	if err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{}
	var img []byte
	for _, zf := range zr.File {
		rc, _ := zf.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		if zf.Name == "images/cover.jpg" {
			img = b
		}
		pages[zf.Name] = string(b)
	}
	if !bytes.Equal(img, raw) {
		t.Errorf("images/cover.jpg is %d bytes, the single file's picture %d", len(img), len(raw))
	}
	idx := pages["index.html"]
	if !strings.Contains(idx, `<img src="data:image/jpeg;base64,`+m[1]+`" alt="A door in the snow" width="800" height="1200">`) {
		t.Errorf("the index has no cover image:\n%.900s", idx)
	}
	// every page keeps the one policy: images from data: only, nothing from the page's own folder or the disk
	for name, p := range pages {
		if strings.HasSuffix(name, ".html") && (!strings.Contains(p, "img-src data:;") || strings.Contains(p, "&#39;self&#39;") || strings.Contains(p, "file:")) {
			t.Errorf("%s has a wider policy than inline images", name)
		}
		if strings.HasSuffix(name, ".html") && name != "index.html" && strings.Contains(p, "base64,") && strings.Contains(p, "cover") {
			t.Errorf("%s shows the cover", name)
		}
	}
	// the export's record carries the layout's hash: a changed cover is a changed export
	var manifest string
	if err := f.d.Read().QueryRow(`SELECT manifest_json FROM exports WHERE project_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, f.pid).Scan(&manifest); err != nil || !strings.Contains(manifest, `"cover_layout"`) {
		t.Errorf("the export record: %v %s", err, manifest)
	}
	// a picture that is gone does not stop the export: the generated cover stands in
	var sha, ext string
	if err := f.d.Read().QueryRow(`SELECT sha256, ext FROM attachments WHERE project_id = ? AND cover_clean = 1`, f.pid).Scan(&sha, &ext); err != nil {
		t.Fatal(err)
	}
	blob, _ := attachments.BlobPath(f.dir, attachments.OwnerProject, f.pid, sha, ext)
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.dir, "files", "projects", f.pid, "covers")); err != nil {
		t.Fatal(err)
	}
	if gone := f.mustHTML(export.HTMLIn{Mode: "single"}); !strings.Contains(string(gone.Body), "<figure class=\"cover\"><svg") {
		t.Error("the generated cover did not stand in for a picture that is gone")
	}
}
