package compile_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/publish/check"
)

func pngBytes(seed uint8, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < w; i++ {
		img.Set(i, i%h, color.RGBA{seed, 3, 4, 255})
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func (f *fx) upload(projectID string, seed uint8) string {
	f.t.Helper()
	b := pngBytes(seed, 24, 16)
	ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(b), Length: int64(len(b))})
	out, err := f.reg.Call(ctx, f.owner, "project.attachment.upload", attachments.UploadIn{ID: projectID, Filename: "map.png", Alt: "A map"})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(attachments.Item).Item.ID
}

// A project's own images go into the file; another project's, a web address and
// a path never do, and nothing is fetched.
func TestImagesInACompile(t *testing.T) {
	f := newFx(t, "Pictures")
	other, err := f.reg.Call(f.ctx, f.owner, "project.create", projects.CreateIn{Title: "Elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	otherID := other.(projects.Item).Item.ID
	mine := f.upload(f.pid, 1)
	theirs := f.upload(otherID, 2)
	inline := "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	f.doc("One", "chapter", "", "Mine ![A map](/files/"+mine+") and theirs ![Stolen](/files/"+theirs+") and far ![Far](https://evil.example/a.png) and a path ![Path](../../etc/passwd) and inline ![Dot]("+inline+").\n")

	for _, preset := range []string{"shunn-us", "epub3"} {
		out := f.compile(preset, publish.Options{})
		parts := unzip(t, out.Body)
		var media []string
		for n := range parts {
			if strings.Contains(n, "media/") || strings.Contains(n, "images/img-") {
				media = append(media, n)
			}
		}
		if len(media) != 2 {
			t.Errorf("%s embeds %v, want mine and the inline one only", preset, media)
		}
		all := ""
		for _, b := range parts {
			all += b
		}
		if strings.Contains(all, "evil.example") || strings.Contains(all, "etc/passwd") {
			t.Errorf("%s holds a remote address", preset)
		}
		var codes []string
		for _, w := range out.Warnings {
			codes = append(codes, w.Code)
		}
		if !strings.Contains(strings.Join(codes, ","), "image_remote") || !strings.Contains(strings.Join(codes, ","), "image_missing") {
			t.Errorf("%s warnings: %v", preset, out.Warnings)
		}
	}
	// the bytes in the DOCX are the stored bytes
	d := f.compile("shunn-us", publish.Options{})
	if probs := check.DOCX(d.Body); len(probs) > 0 {
		t.Errorf("%v", probs)
	}
	if got := unzip(t, d.Body)["word/media/image1.png"]; !strings.HasPrefix(got, "\x89PNG") {
		t.Error("the embedded picture is not a PNG")
	}
	// a preview does not read the files and still counts them
	p := f.preview("epub3", publish.Options{})
	var codes []string
	for _, w := range p.Warnings {
		codes = append(codes, w.Code)
	}
	if !strings.Contains(strings.Join(codes, ","), "image_remote") {
		t.Errorf("preview warnings: %v", p.Warnings)
	}
}

// A dry run has no data directory: the images are left out with a warning, and
// nothing breaks.
func TestImagesInADryRun(t *testing.T) {
	f := newFx(t, "Dry")
	id := f.upload(f.pid, 3)
	f.doc("One", "chapter", "", "Mine ![A map](/files/"+id+").\n")
	res, err := f.reg.CallDry(f.ctx, f.owner, "export.compile", compileIn(f.pid, "shunn-us"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changes == 0 {
		t.Error("a dry run of an export should report the row it would record")
	}
	out := res.Out.(compile.Out)
	for name := range unzip(t, out.Body) {
		if strings.Contains(name, "media/") {
			t.Errorf("a dry run embedded %s", name)
		}
	}
	var codes []string
	for _, w := range out.Warnings {
		codes = append(codes, w.Code)
	}
	if !strings.Contains(strings.Join(codes, ","), "image_skipped") {
		t.Errorf("a dry run left out an image without saying so: %v", out.Warnings)
	}
	var n int
	f.d.Read().QueryRow(`SELECT count(*) FROM exports`).Scan(&n)
	if n != 0 {
		t.Errorf("the dry run recorded %d exports", n)
	}
}
