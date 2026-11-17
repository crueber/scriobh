package compile_test

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/publish/check"
)

func coverJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), 80, uint8(y * 255 / h), 255})
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	return b.Bytes()
}

func (f *fx) coverView() coverlayout.View {
	f.t.Helper()
	cur, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.get", coverlayout.ProjectIn{ID: f.pid})
	if err != nil {
		f.t.Fatal(err)
	}
	return *cur.(coverlayout.Item).Layout
}

func (f *fx) setCover(front []byte, alt string) coverlayout.View {
	f.t.Helper()
	ctx := ops.WithRawBody(f.ctx, ops.RawBody{R: bytes.NewReader(front), Length: int64(len(front))})
	up, err := f.reg.Call(ctx, f.owner, "project.cover_layout.upload", coverlayout.UploadIn{ID: f.pid, Part: "front"})
	if err != nil {
		f.t.Fatal(err)
	}
	side := coverlayout.Side{Mode: "image", Attachment: up.(coverlayout.UploadOut).AttachmentID}
	cur, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.get", coverlayout.ProjectIn{ID: f.pid})
	if err != nil {
		f.t.Fatal(err)
	}
	rev := 0
	if l := cur.(coverlayout.Item).Layout; l != nil {
		rev = l.Revision
	}
	out, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.set", coverlayout.SetIn{ID: f.pid, Revision: rev, Shape: "sides", Front: &side, Alt: alt})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(coverlayout.View)
}

// A project's own cover is the EPUB's cover-image, a JPEG, and its cover page is one image with the alt text; the
// file passes the EPUB checker, and its input hash follows the cover.
func TestEPUBUsesTheProjectsOwnCover(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	plain := f.compile("epub3", publish.Options{})
	manuscriptBefore := f.compile("shunn-us", publish.Options{}).InputHash
	pdfBefore := f.compile("print-6x9", publish.Options{}).InputHash
	bareBefore := f.compile("epub3", publish.Options{Cover: publish.False()}).InputHash
	if _, ok := unzip(t, plain.Body)["OEBPS/images/cover.svg"]; !ok {
		t.Fatal("the generated cover is not in the EPUB without a layout")
	}
	f.setCover(coverJPEG(1600, 2400), "A red door in the snow")
	out := f.compile("epub3", publish.Options{})
	if probs := check.EPUB(out.Body); len(probs) > 0 {
		t.Errorf("the checker: %v", probs)
	}
	z := unzip(t, out.Body)
	img, ok := z["OEBPS/images/cover.jpg"]
	if !ok || !strings.HasPrefix(img, "\xff\xd8") {
		t.Fatalf("no cover.jpg in the EPUB: %d bytes", len(img))
	}
	if cfg, err := jpeg.DecodeConfig(strings.NewReader(img)); err != nil || cfg.Width != 1600 || cfg.Height != 2400 {
		t.Errorf("the cover image: %v %+v", err, cfg)
	}
	if _, ok := z["OEBPS/images/cover.svg"]; ok {
		t.Error("the generated cover is still in the EPUB")
	}
	opf := z["OEBPS/content.opf"]
	if !strings.Contains(opf, `<item id="cover-image" href="images/cover.jpg" media-type="image/jpeg" properties="cover-image"/>`) || !strings.Contains(opf, `<meta name="cover" content="cover-image"/>`) {
		t.Errorf("the package:\n%s", opf)
	}
	page := z["OEBPS/text/cover.xhtml"]
	if !strings.Contains(page, `src="../images/cover.jpg" alt="A red door in the snow"`) || strings.Count(page, "<img") != 1 {
		t.Errorf("the cover page:\n%s", page)
	}
	// the input hash follows the cover, and the preview agrees with the export
	if out.InputHash == plain.InputHash {
		t.Error("a cover is not part of the input hash")
	}
	if pv := f.preview("epub3", publish.Options{}); pv.InputHash != out.InputHash {
		t.Errorf("preview %s, export %s", pv.InputHash, out.InputHash)
	}
	again := f.compile("epub3", publish.Options{})
	if again.InputHash != out.InputHash || !bytes.Equal(again.Body, out.Body) {
		t.Error("the same cover is not the same file")
	}
	f.setCover(coverJPEG(1200, 1800), "A red door in the snow")
	changed := f.compile("epub3", publish.Options{})
	if changed.InputHash == out.InputHash {
		t.Error("a changed picture did not change the input hash")
	}
	changedHash := changed.InputHash
	// the cover option off: no cover at all, the way a file for KDP is made
	no := publish.False()
	bare := f.compile("epub3", publish.Options{Cover: no})
	if _, ok := unzip(t, bare.Body)["OEBPS/images/cover.jpg"]; ok {
		t.Error("cover off still has the cover")
	}
	if probs := check.EPUB(bare.Body); len(probs) > 0 {
		t.Errorf("the checker, cover off: %v", probs)
	}
	if bare.InputHash == out.InputHash {
		t.Error("a file without the cover has the cover's hash")
	}
	// the back of the layout is not on the EPUB: changing it is the same file
	ctx := ops.WithRawBody(f.ctx, ops.RawBody{R: bytes.NewReader(coverJPEG(1000, 1500)), Length: int64(len(coverJPEG(1000, 1500)))})
	bk, err := f.reg.Call(ctx, f.owner, "project.cover_layout.upload", coverlayout.UploadIn{ID: f.pid, Part: "back"})
	if err != nil {
		t.Fatal(err)
	}
	cur := f.coverView()
	withBack := coverlayout.SetIn{ID: f.pid, Revision: cur.Revision, Shape: "sides", Front: cur.Front, Back: &coverlayout.Side{Mode: "image", Attachment: bk.(coverlayout.UploadOut).AttachmentID}, Alt: cur.Alt}
	if _, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.set", withBack); err != nil {
		t.Fatal(err)
	}
	if again := f.compile("epub3", publish.Options{}); again.InputHash != changedHash {
		t.Error("a back cover changed the EPUB's input hash")
	}
	// an export that does not show the cover has the hash it had: a manuscript, a PDF, an EPUB with the cover off
	m := f.compile("shunn-us", publish.Options{})
	if bytes.Contains(m.Body, []byte("\xff\xd8\xff")) {
		t.Error("the manuscript carries a picture")
	}
	if m.InputHash != manuscriptBefore {
		t.Error("a cover changed the manuscript's input hash")
	}
	if pdf := f.compile("print-6x9", publish.Options{}); pdf.InputHash != pdfBefore {
		t.Error("a cover changed the interior PDF's input hash")
	}
	if bare.InputHash != bareBefore {
		t.Error("a cover changed the hash of an EPUB with the cover off")
	}
}

// A cover picture that is gone (a restore made without files) does not stop an export: the generated cover stands in.
func TestEPUBWithALayoutWhosePictureIsGoneUsesTheGeneratedCover(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	v := f.setCover(coverJPEG(1200, 1800), "A door")
	var sha, ext string
	if err := f.d.Read().QueryRow(`SELECT sha256, ext FROM attachments WHERE id = ?`, v.Front.Attachment).Scan(&sha, &ext); err != nil {
		t.Fatal(err)
	}
	blob, err := attachments.BlobPath(f.dir, attachments.OwnerProject, f.pid, sha, ext)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(f.dir, "files", "projects", f.pid, "covers")); err != nil { // the pictures made at save time go too (a restore keeps none)
		t.Fatal(err)
	}
	out := f.compile("epub3", publish.Options{})
	z := unzip(t, out.Body)
	if _, ok := z["OEBPS/images/cover.svg"]; !ok {
		t.Errorf("the generated cover is not in the EPUB: %v", len(z))
	}
	if _, ok := z["OEBPS/images/cover.jpg"]; ok {
		t.Error("a picture that is gone is in the EPUB")
	}
	if probs := check.EPUB(out.Body); len(probs) > 0 {
		t.Errorf("the checker: %v", probs)
	}
}

// A wrap's front is on the right of a book read left to right and on the left of one read right to left; a compile that
// gives a language of its own turns it the way that language reads, not only the project's.
func TestEPUBWrapFrontFollowsTheCompilesLanguage(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	// 6x9 at 150 dpi with no bleed and a half-inch spine: 1875 by 1350; red on the left, green on the right
	img := image.NewRGBA(image.Rect(0, 0, 1875, 1350))
	for y := 0; y < 1350; y++ {
		for x := 0; x < 1875; x++ {
			c := color.RGBA{0, 160, 0, 255}
			switch {
			case x < 900:
				c = color.RGBA{200, 0, 0, 255}
			case x < 975:
				c = color.RGBA{0, 0, 200, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	ctx := ops.WithRawBody(f.ctx, ops.RawBody{R: bytes.NewReader(buf.Bytes()), Length: int64(buf.Len())})
	up, err := f.reg.Call(ctx, f.owner, "project.cover_layout.upload", coverlayout.UploadIn{ID: f.pid, Part: "wrap", BleedIn: "0"})
	if err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	if _, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.set", coverlayout.SetIn{ID: f.pid, Shape: "wrap", Wrap: &coverlayout.Wrap{Attachment: up.(coverlayout.UploadOut).AttachmentID, BleedIn: &zero}, Alt: "A wrap"}); err != nil {
		t.Fatal(err)
	}
	centre := func(out compile.Out) color.Color {
		c, err := jpeg.Decode(strings.NewReader(unzip(t, out.Body)["OEBPS/images/cover.jpg"]))
		if err != nil {
			t.Fatal(err)
		}
		return c.At(450, 675)
	}
	ltr := f.compile("epub3", publish.Options{})
	rtl := f.compile("epub3", publish.Options{Language: "ar"})
	if r, g, _, _ := centre(ltr).RGBA(); g>>8 < 120 || r>>8 > 60 {
		t.Errorf("the front of an English book is not the right-hand side: %v", centre(ltr))
	}
	if r, g, _, _ := centre(rtl).RGBA(); r>>8 < 150 || g>>8 > 60 {
		t.Errorf("the front of a compile in Arabic is not the left-hand side: %v", centre(rtl))
	}
	if ltr.InputHash == rtl.InputHash {
		t.Error("the language option does not change the hash of an EPUB whose cover turns with it")
	}
}

// own_cover false (what a release to a public remote without include_cover sends) puts the generated cover in the EPUB, and
// the project's own cover is then not part of its input hash.
func TestEPUBWithOwnCoverOffUsesTheGeneratedCover(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	no := false
	before := f.compile("epub3", publish.Options{OwnCover: &no})
	f.setCover(coverJPEG(1200, 1800), "A door")
	out := f.compile("epub3", publish.Options{OwnCover: &no})
	z := unzip(t, out.Body)
	if _, ok := z["OEBPS/images/cover.svg"]; !ok {
		t.Error("the generated cover is not in the EPUB")
	}
	if _, ok := z["OEBPS/images/cover.jpg"]; ok {
		t.Error("the project's own cover is in an EPUB made with own_cover off")
	}
	if out.InputHash != before.InputHash {
		t.Error("the project's own cover changed the hash of an EPUB that does not show it")
	}
	if own := f.compile("epub3", publish.Options{}); own.InputHash == out.InputHash {
		t.Error("the default EPUB does not differ from one made with own_cover off")
	}
}
