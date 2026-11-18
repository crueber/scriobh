package compile_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/publish/check"
)

func coverPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{200, uint8(x * 255 / w), uint8(y * 255 / h), 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func (f *fx) uploadCover(part string, data []byte, trim, bleed string) coverlayout.UploadOut {
	f.t.Helper()
	ctx := ops.WithRawBody(f.ctx, ops.RawBody{R: bytes.NewReader(data), Length: int64(len(data))})
	out, err := f.reg.Call(ctx, f.owner, "project.cover_layout.upload", coverlayout.UploadIn{ID: f.pid, Part: part, Trim: trim, BleedIn: bleed})
	if err != nil {
		f.t.Fatalf("upload %s: %v", part, err)
	}
	return out.(coverlayout.UploadOut)
}

func (f *fx) setLayout(in coverlayout.SetIn) coverlayout.View {
	f.t.Helper()
	cur, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.get", coverlayout.ProjectIn{ID: f.pid})
	if err != nil {
		f.t.Fatal(err)
	}
	in.ID = f.pid
	if l := cur.(coverlayout.Item).Layout; l != nil {
		in.Revision = l.Revision
	}
	out, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.set", in)
	if err != nil {
		f.t.Fatalf("set: %v", err)
	}
	return out.(coverlayout.View)
}

func (f *fx) clearLayout() {
	f.t.Helper()
	if _, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.clear", coverlayout.ProjectIn{ID: f.pid}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) exportCover(in compile.CoverIn) (compile.Out, error) {
	in.ProjectID = f.pid
	out, err := f.reg.Call(f.ctx, f.owner, "export.cover", in)
	if err != nil {
		return compile.Out{}, err
	}
	return out.(compile.Out), nil
}

// coverDoc is every file export.cover makes of a cover read as one document, its pages in order: the one a printer takes, the
// front alone and, with proof, the proof.
func (f *fx) coverDoc(t *testing.T, in compile.CoverIn, proof bool) *check.PDFDoc {
	t.Helper()
	var all *check.PDFDoc
	parts := []string{"cover", "front"}
	if proof {
		parts = append(parts, "proof")
	}
	for _, part := range parts {
		in.Part = part
		out, err := f.exportCover(in)
		if err != nil {
			t.Fatalf("part %s: %v", part, err)
		}
		d := parse(t, out.Body)
		if len(d.Pages) != 1 {
			t.Fatalf("part %s has %d pages", part, len(d.Pages))
		}
		if all == nil {
			all = d
		} else {
			all.Pages = append(all.Pages, d.Pages...)
			all.Images += d.Images
		}
	}
	return all
}

func codesOf(ws []compile.Warning) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Code)
	}
	sort.Strings(out)
	return out
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func parse(t *testing.T, body []byte) *check.PDFDoc {
	t.Helper()
	d, probs := check.ParsePDF(body)
	for _, p := range probs {
		t.Error("the checker:", p)
	}
	return d
}

// A cover made from sides, for each profile and two trims: the pages, the boxes, the pictures at their own resolution,
// and the checker's word on the file.
func TestPrintCoverFromSides(t *testing.T) {
	for _, tc := range []struct {
		preset, trim string
		w, h         float64
		fw, fh       int
	}{{"print-6x9", "6x9", 6, 9, 1900, 2900}, {"print-5.5x8.5", "5.5x8.5", 5.5, 8.5, 1760, 2660}} {
		for _, prof := range []string{"kdp", "ingram", "generic"} {
			t.Run(tc.trim+"/"+prof, func(t *testing.T) {
				f := newFx(t, "The Salt Road")
				f.novel()
				front := coverJPEG(tc.fw, tc.fh)
				fu := f.uploadCover("front", front, tc.trim, "")
				bu := f.uploadCover("back", coverPNG(900, 1300), tc.trim, "")
				view := f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: tc.trim, Alt: "A red door in the snow",
					Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID},
					Back:  &coverlayout.Side{Mode: "page", Attachment: bu.AttachmentID, Fit: "contain", Scale: 70, Position: "top", Background: "#2F5D50", MarginIn: 0.5}})
				in := compile.CoverIn{Preset: tc.preset, PageCount: 320, PageThicknessIn: 0.0025, Profile: prof, Barcode: true}
				out, err := f.exportCover(in)
				if err != nil {
					t.Fatal(err)
				}
				doc := f.coverDoc(t, in, true)
				if len(doc.Pages) != 3 {
					t.Fatalf("%d pages", len(doc.Pages))
				}
				if one := parse(t, out.Body); len(one.Pages) != 1 || !strings.HasSuffix(out.Filename, "-cover.pdf") {
					t.Errorf("the file for the printer is %d pages, %s", len(one.Pages), out.Filename)
				}
				spine := 320 * 0.0025
				wantW, wantH := (2*tc.w+0.25+spine)*72, (tc.h+0.25)*72
				p1 := doc.Pages[0]
				if !near(p1.W, wantW, 0.01) || !near(p1.H, wantH, 0.01) || len(p1.Trim) != 4 || !near(p1.Trim[0], 9, 0.001) || !near(p1.Trim[2], wantW-9, 0.01) {
					t.Errorf("page 1 %vx%v trim %v, want %vx%v", p1.W, p1.H, p1.Trim, wantW, wantH)
				}
				var jpg, pngd *check.PDFDraw
				for i := range p1.Draws {
					if p1.Draws[i].Image.Filter == "DCTDecode" {
						jpg = &p1.Draws[i]
					} else {
						pngd = &p1.Draws[i]
					}
				}
				if jpg == nil || pngd == nil {
					t.Fatalf("%+v", p1.Draws)
				}
				// the cleaned attachment is what is embedded, at its own pixel size
				if jpg.Image.W != tc.fw || jpg.Image.H != tc.fh || pngd.Image.W != 900 || pngd.Image.H != 1300 {
					t.Errorf("embedded %dx%d and %dx%d", jpg.Image.W, jpg.Image.H, pngd.Image.W, pngd.Image.H)
				}
				if !bytes.HasPrefix(jpg.Image.Data, []byte{0xFF, 0xD8}) || len(jpg.Image.Data) < 1000 {
					t.Error("the JPEG is not passed through")
				}
				// the front picture covers the front's trim plus a bleed on every side
				r := jpg.Rect()
				frontX0 := 9 + tc.w*72 + spine*72
				if r[0] > frontX0-9+0.01 && r[2] < wantW-0.01 || r[3]-r[1] < (tc.h+0.25)*72-0.01 && r[2]-r[0] < (tc.w+0.25)*72-0.01 {
					t.Errorf("the front picture %v does not cover the panel", r)
				}
				// the back is placed as the cover page's preview places it: coverlayout.PlacePage's answer for the picture and the
				// side as saved, mapped from the page's 2,560 pixels to the trim, to the hundredth of a point
				trim, _ := coverlayout.LookupTrim(tc.trim)
				pl, err := coverlayout.PlacePage(900, 1300, trim, *view.Back)
				if err != nil {
					t.Fatal(err)
				}
				kx, ky := tc.w*72/float64(pl.CanvasW), tc.h*72/float64(pl.CanvasH)
				wantDst := [4]float64{9 + float64(pl.Dst.Min.X)*kx, 9 + tc.h*72 - float64(pl.Dst.Max.Y)*ky, 9 + float64(pl.Dst.Max.X)*kx, 9 + tc.h*72 - float64(pl.Dst.Min.Y)*ky}
				wantClip := [4]float64{9 + float64(pl.Box.Min.X)*kx, 9 + tc.h*72 - float64(pl.Box.Max.Y)*ky, 9 + float64(pl.Box.Max.X)*kx, 9 + tc.h*72 - float64(pl.Box.Min.Y)*ky}
				for i, got := range pngd.Rect() {
					if !near(got, wantDst[i], 0.02) || pngd.Clip == nil || !near(pngd.Clip[i], wantClip[i], 0.02) {
						t.Errorf("the back is at %v clip %v, PlacePage says %v clip %v", pngd.Rect(), pngd.Clip, wantDst, wantClip)
						break
					}
				}
				// the proof is page 3 and says so
				if !strings.Contains(doc.Pages[2].Text(), "Proof") {
					t.Errorf("page 3: %q", doc.Pages[2].Text())
				}
				// the warnings: the printed resolution is checked and the picture of the back, small as it sits, is not low
				for _, w := range out.Warnings {
					if w.Code == "image_low_resolution" && strings.Contains(w.Message, "front") {
						t.Errorf("the front is 300 dpi or more: %s", w.Message)
					}
				}
				if out.Format != "pdf" || !strings.HasSuffix(out.Filename, "-cover.pdf") {
					t.Errorf("%s %s", out.Format, out.Filename)
				}
				// deterministic, and the hash follows the layout and the profile
				again, _ := f.exportCover(in)
				if !bytes.Equal(again.Body, out.Body) || again.InputHash != out.InputHash {
					t.Error("two covers differ")
				}
			})
		}
	}
}

// A wrap is placed as the picture is, the whole of page 1, even a US Letter wrap at 300 dpi (18 million pixels, over the
// PDF writer's own cap for a picture it decodes: a JPEG is passed through). Changing the page count makes the wrap stale.
func TestPrintCoverFromAWrap(t *testing.T) {
	if testing.Short() {
		t.Skip("encodes an 18 megapixel picture")
	}
	f := newFx(t, "The Salt Road")
	f.novel()
	// Letter, 0.125 in bleed, a 0.5 in spine, 300 dpi: 17.75 x 11.25 in = 5325 x 3375 px
	wrap := coverJPEG(5325, 3375)
	up := f.uploadCover("wrap", wrap, "letter", "0.125")
	if up.Measured == nil || math.Abs(up.Measured.SpineIn-0.5) > 0.002 {
		t.Fatalf("measured %+v", up.Measured)
	}
	f.setLayout(coverlayout.SetIn{Shape: "wrap", Trim: "letter", Alt: "A wrap", Wrap: &coverlayout.Wrap{Attachment: up.AttachmentID}})
	// 200 pages of 0.0025 in paper is exactly the wrap's spine
	out, err := f.exportCover(compile.CoverIn{Preset: "proof-letter", PageCount: 200, PageThicknessIn: 0.0025, Profile: "kdp"})
	if err != nil {
		t.Fatal(err)
	}
	doc := f.coverDoc(t, compile.CoverIn{Preset: "proof-letter", PageCount: 200, PageThicknessIn: 0.0025, Profile: "kdp"}, true)
	if len(doc.Pages) != 3 {
		t.Fatalf("%d pages", len(doc.Pages))
	}
	p1 := doc.Pages[0]
	if !near(p1.W, 17.75*72, 0.2) || !near(p1.H, 11.25*72, 0.001) {
		t.Errorf("page 1 is %vx%v", p1.W, p1.H)
	}
	if len(p1.Draws) != 1 || len(p1.Fills) != 0 || p1.Strokes != 0 || len(p1.Lines) != 0 {
		t.Errorf("page 1: %d draws %d fills %d strokes %d lines", len(p1.Draws), len(p1.Fills), p1.Strokes, len(p1.Lines))
	}
	if d := p1.Draws[0]; d.Image.W != 5325 || d.Image.H != 3375 || d.Image.Filter != "DCTDecode" {
		t.Errorf("embedded %dx%d %s", d.Image.W, d.Image.H, d.Image.Filter)
	}
	for _, w := range out.Warnings {
		if w.Code == "cover_wrap_stale" || w.Code == "image_low_resolution" {
			t.Errorf("unexpected %s: %s", w.Code, w.Message)
		}
	}
	if out.Warnings == nil || !strings.Contains(strings.Join(codesOf(out.Warnings), ","), "cover_barcode_area") {
		t.Errorf("the note to leave the barcode area clear: %v", codesOf(out.Warnings))
	}
	// the page count changes: the wrap is stale, placed as it is
	stale, err := f.exportCover(compile.CoverIn{Preset: "proof-letter", PageCount: 260, PageThicknessIn: 0.0025, Profile: "kdp"})
	if err != nil {
		t.Fatal(err)
	}
	var msg string
	for _, w := range stale.Warnings {
		if w.Code == "cover_wrap_stale" {
			msg = w.Message
		}
	}
	if !strings.Contains(msg, "0.500") || !strings.Contains(msg, "0.650") {
		t.Errorf("stale: %q", msg)
	}
	sd := parse(t, stale.Body)
	if !near(sd.Pages[0].W, 17.75*72, 0.2) {
		t.Errorf("a stale wrap is stretched: %v", sd.Pages[0].W)
	}
	if stale.InputHash == out.InputHash {
		t.Error("the page count is not in the cover's hash")
	}
	// within a thirty-second of an inch is not stale: 200 pages of paper 0.0026 is 0.52 in
	near2, err := f.exportCover(compile.CoverIn{Preset: "proof-letter", PageCount: 200, PageThicknessIn: 0.0026, Profile: "kdp"})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range near2.Warnings {
		if w.Code == "cover_wrap_stale" {
			t.Errorf("0.02 in is under a thirty-second: %s", w.Message)
		}
	}
	// the spec says what the cover is
	sp, err := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", compile.CoverIn{ProjectID: f.pid, Preset: "proof-letter", PageCount: 260, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	s := sp.(compile.CoverSpecOut)
	if s.Layout == nil || !s.Layout.Stale || s.Layout.MeasuredSpineIn == nil || !near(*s.Layout.MeasuredSpineIn, 0.5, 0.002) || !near(s.Layout.WrapWidthIn, 17.75, 0.002) || s.SpineHasText || s.SpineTextWhy != "wrap" {
		t.Errorf("spec layout %+v text %v %q", s.Layout, s.SpineHasText, s.SpineTextWhy)
	}
}

// The layout's trim is the preset's, or the cover is refused with a sentence naming both; own_cover off is the generated
// cover; no layout is two pages as it was.
func TestPrintCoverTrimAndOwnCover(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	plain, err := f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(f.coverDoc(t, compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025}, false).Pages); n != 2 || len(parse(t, plain.Body).Pages) != 1 {
		t.Fatalf("no layout: %d pages", n)
	}
	fu := f.uploadCover("front", coverJPEG(1900, 2900), "6x9", "")
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID}})
	_, err = f.exportCover(compile.CoverIn{Preset: "print-5.5x8.5", PageCount: 300, PageThicknessIn: 0.0025})
	e := ops.AsError(err)
	if e == nil || e.Code != ops.CodeInvalid || e.Data["reason"] != "cover_trim_mismatch" || !strings.Contains(e.Message, "6x9") || !strings.Contains(e.Message, "5.5x8.5") {
		t.Fatalf("a wrong trim: %v", err)
	}
	_, err = f.exportCover(compile.CoverIn{Preset: "print-5.5x8.5", PageCount: 300, PageThicknessIn: 0.0025, Options: publish.Options{OwnCover: publish.False()}})
	if err != nil {
		t.Errorf("own_cover off, another trim: %v", err)
	}
	own, err := f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	if own.InputHash == plain.InputHash || len(f.coverDoc(t, compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025}, true).Pages) != 3 {
		t.Error("the layout is not in the cover")
	}
	off, err := f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025, Options: publish.Options{OwnCover: publish.False()}})
	if err != nil || off.InputHash == own.InputHash {
		t.Fatal(err)
	}
	if len(f.coverDoc(t, compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025, Options: publish.Options{OwnCover: publish.False()}}, false).Pages) != 2 {
		t.Error("own_cover off did not give the generated cover")
	}
	// a changed picture is a changed cover; the profile is in the hash
	fu2 := f.uploadCover("front", coverJPEG(1900, 2901), "6x9", "")
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: fu2.AttachmentID}})
	changed, _ := f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025})
	kdp, _ := f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025, Profile: "kdp"})
	if changed.InputHash == own.InputHash || kdp.InputHash == changed.InputHash {
		t.Error("a picture or a profile is not in the hash")
	}
	// the layout cleared: back to the generated cover
	f.clearLayout()
	back, err := f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025})
	if err != nil || len(parse(t, back.Body).Pages) != 1 {
		t.Errorf("cleared: %v", err)
	}
}

// The check, the spec and the export say the same things about a layout.
func TestCoverCheckAgreesWithTheExport(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	fu := f.uploadCover("front", coverJPEG(1500, 2300), "6x9", "") // 240 dpi: low
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID},
		Back: &coverlayout.Side{Mode: "page", Attachment: f.uploadCover("back", coverJPEG(1800, 1200), "6x9", "").AttachmentID, Fit: "cover", Position: "center", Background: "#2F5D50"}})
	for _, prof := range []string{"kdp", "ingram", "generic"} {
		for _, barcode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", prof, barcode), func(t *testing.T) {
				in := compile.CoverIn{PageCount: 60, PageThicknessIn: 0.005, Profile: prof, Barcode: barcode}
				out, err := f.exportCover(in)
				if err != nil {
					t.Fatal(err)
				}
				ck, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.check", coverlayout.CheckIn{ID: f.pid, Preset: "print-6x9", Profile: prof, PageCount: 60, PageThicknessIn: 0.005, Barcode: barcode})
				if err != nil {
					t.Fatal(err)
				}
				co := ck.(coverlayout.CheckOut)
				plan := map[string]bool{"image_low_resolution": true, "cover_margin_crossed": true, "cover_bleed_missing": true, "cover_wrap_stale": true, "cover_barcode_area": true, "spine_narrow": true, "spine_few_pages": true, "spine_rtl": true, "cover_wrap_bleed": true}
				var fromExport, fromCheck []string
				for _, w := range out.Warnings {
					if plan[w.Code] {
						fromExport = append(fromExport, w.Code+":"+w.Part)
					}
				}
				for _, w := range co.Warnings {
					if plan[w.Code] {
						fromCheck = append(fromCheck, w.Code+":"+w.Part)
					}
				}
				sort.Strings(fromExport)
				sort.Strings(fromCheck)
				if strings.Join(fromExport, ",") != strings.Join(fromCheck, ",") {
					t.Errorf("export %v, check %v", fromExport, fromCheck)
				}
				if len(fromCheck) == 0 {
					t.Error("nothing was checked")
				}
				for _, nc := range co.NotChecked {
					if nc == "text_margin" || nc == "barcode_box" || nc == "spine_text" {
						t.Errorf("%s is still not checked", nc)
					}
				}
				// the screen-size resolution finding is replaced by the printed one: a picture is reported once
				n := 0
				for _, w := range co.Warnings {
					if w.Code == "image_low_resolution" && w.Part == "front" || w.Code == "cover_low_resolution" && w.Part == "front" {
						n++
					}
				}
				if n != 1 {
					t.Errorf("the front's resolution is reported %d times", n)
				}
			})
		}
	}
	// without a page count the spine cannot be judged, and the check says so
	ck, _ := f.reg.Call(f.ctx, f.owner, "project.cover_layout.check", coverlayout.CheckIn{ID: f.pid, Profile: "kdp"})
	if nc := ck.(coverlayout.CheckOut).NotChecked; strings.Join(nc, ",") != "colour_profile,spine_text" {
		t.Errorf("not checked: %v", nc)
	}
}

// A right-to-left book: its front is on the left of the wrap, the page count must be given, a cover of pictures is made, and
// a generated cover is refused.
func TestPrintCoverRightToLeft(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	if _, err := f.d.Writer().Exec(`UPDATE projects SET language = 'ar' WHERE id = ?`, f.pid); err != nil {
		t.Fatal(err)
	}
	// a project with no layout has the generated cover it has always had, whatever its language, and its measurements
	if _, err := f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025}); err != nil {
		t.Fatalf("a generated cover for a right-to-left book: %v", err)
	}
	if sp, err := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025}); err != nil || sp.(compile.CoverSpecOut).Back.X0 > sp.(compile.CoverSpecOut).Front.X0 || !sp.(compile.CoverSpecOut).SpineHasText {
		t.Fatalf("the measurements of a generated cover for a right-to-left book: %v", err)
	}
	fu := f.uploadCover("front", coverJPEG(1900, 2900), "6x9", "")
	bu := f.uploadCover("back", coverJPEG(1900, 2901), "6x9", "")
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID}, Back: &coverlayout.Side{Mode: "image", Attachment: bu.AttachmentID}})
	if _, err := f.exportCover(compile.CoverIn{PageThicknessIn: 0.0025}); ops.AsError(err) == nil || ops.AsError(err).Field != "page_count" {
		t.Errorf("no page count for a right-to-left book: %v", err)
	}
	out, err := f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	p1 := parse(t, out.Body).Pages[0]
	var front, back *check.PDFDraw
	for i := range p1.Draws {
		if p1.Draws[i].Image.H == 2900 {
			front = &p1.Draws[i]
		} else {
			back = &p1.Draws[i]
		}
	}
	if front == nil || back == nil || front.Rect()[0] > back.Rect()[0] {
		t.Errorf("the front should be on the left: %+v", p1.Draws)
	}
	if len(p1.Lines) != 0 {
		t.Error("type on a right-to-left cover")
	}
	// the spec's panels follow the book: the front first, then the spine, then the back
	sp, err := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	s := sp.(compile.CoverSpecOut)
	if !(s.Front.X1 <= s.Spine.X0+0.01 && s.Spine.X1 <= s.Back.X0+0.01) || s.SpineTextWhy != "rtl" || s.SpineHasText {
		t.Errorf("panels %+v %+v %+v, spine type %v %q", s.Front, s.Spine, s.Back, s.SpineHasText, s.SpineTextWhy)
	}
	// and the barcode box is on the back, the right-hand one, near its lower right
	if s.BarcodeBox.X0 < s.Back.X0 || s.BarcodeBox.X1 > s.Back.X1 {
		t.Errorf("the barcode box %+v is not on the back %+v", s.BarcodeBox, s.Back)
	}
}

// cover_page puts the layout's front first in the print PDF and leaves the rest of the book alone; back_cover puts the back
// last in the EPUB, out of the reading order; own_cover off keeps the layout's pictures out of both, and out of their hashes.
func TestCoverPageAndBackCoverOptions(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	hashPDF := f.compile("print-6x9", publish.Options{}).InputHash
	hashEPUB := f.compile("epub3", publish.Options{}).InputHash
	pdfBase := f.compile("print-6x9", publish.Options{})
	baseDoc := parse(t, pdfBase.Body)
	fu := f.uploadCover("front", coverJPEG(1900, 2900), "6x9", "")
	bu := f.uploadCover("back", coverPNG(1000, 1500), "6x9", "")
	set := func(back bool, alt string) {
		in := coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: alt, Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID}}
		if back {
			in.Back = &coverlayout.Side{Mode: "image", Attachment: bu.AttachmentID}
		}
		f.setLayout(in)
	}
	set(true, "A red door")
	// with the options off a layout changes nothing of the interior or the EPUB's hashes it does not show
	if got := f.compile("print-6x9", publish.Options{}).InputHash; got != hashPDF {
		t.Error("a layout moved the interior PDF's hash with cover_page off")
	}
	// the PDF with the cover page
	on := publish.Options{CoverPage: publish.True()}
	out := f.compile("print-6x9", on)
	doc := parse(t, out.Body)
	if len(doc.Pages) != len(baseDoc.Pages)+2 || doc.Pages[0].Label != "Cover" || doc.Pages[2].Label != baseDoc.Pages[0].Label {
		t.Fatalf("%d pages (the book has %d), labels %q %q", len(doc.Pages), len(baseDoc.Pages), doc.Pages[0].Label, doc.Pages[2].Label)
	}
	if len(doc.Pages[0].Draws) != 1 || doc.Pages[0].Draws[0].Image.W != 1900 || len(doc.Pages[1].Draws) != 0 {
		t.Errorf("the cover page: %+v", doc.Pages[0].Draws)
	}
	if out.InputHash == hashPDF {
		t.Error("the cover page is not in the hash")
	}
	// the hash follows the front, not the back
	set(false, "A red door")
	noBack := f.compile("print-6x9", on)
	if noBack.InputHash != out.InputHash {
		t.Error("a change to the back alone changed the PDF's hash")
	}
	fu = f.uploadCover("front", coverJPEG(1900, 2901), "6x9", "")
	set(false, "A red door")
	if f.compile("print-6x9", on).InputHash == out.InputHash {
		t.Error("a new front did not change the PDF's hash")
	}
	// page count for the spine is the interior's alone
	sp, err := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", compile.CoverIn{ProjectID: f.pid, Options: on, PageThicknessIn: 0.002})
	if err != nil {
		t.Fatal(err)
	}
	if got := sp.(compile.CoverSpecOut).PageCount; got != len(baseDoc.Pages) {
		t.Errorf("the spine counts %d pages, the interior has %d", got, len(baseDoc.Pages))
	}
	// own_cover off: the generated front, and no picture of the layout in the file or the hash
	gen := f.compile("print-6x9", publish.Options{CoverPage: publish.True(), OwnCover: publish.False()})
	gd := parse(t, gen.Body)
	if len(gd.Pages) != len(baseDoc.Pages)+2 || len(gd.Pages[0].Draws) != 0 || !strings.Contains(gd.Pages[0].Text(), "The Salt Road") {
		t.Errorf("own_cover off: %d pages, %d draws, %q", len(gd.Pages), len(gd.Pages[0].Draws), gd.Pages[0].Text())
	}
	f.clearLayout()
	noLayout := f.compile("print-6x9", publish.Options{CoverPage: publish.True(), OwnCover: publish.False()})
	if noLayout.InputHash != gen.InputHash {
		t.Error("own_cover off still has the layout in the hash")
	}
	if g2 := f.compile("print-6x9", on); len(parse(t, g2.Body).Pages[0].Draws) != 0 {
		t.Error("no layout: the cover page is the generated front")
	}
	// a front for another page is refused
	set(false, "A red door")
	_, err = f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "print-5x8", Options: on})
	if e := ops.AsError(err); e == nil || e.Data["reason"] != "cover_trim_mismatch" {
		t.Errorf("a front for another trim: %v", err)
	}

	// ---- the EPUB's back cover
	set(true, "A red door")
	if got := f.compile("epub3", publish.Options{}).InputHash; got == hashEPUB { // the front is shown: the hash follows it
		t.Log("the front changed the EPUB's hash, as it should")
	}
	front := f.compile("epub3", publish.Options{})
	bc := f.compile("epub3", publish.Options{BackCover: publish.True()})
	if probs := check.EPUB(bc.Body); len(probs) > 0 {
		t.Errorf("the checker: %v", probs)
	}
	z := unzip(t, bc.Body)
	page := z["OEBPS/text/back-cover.xhtml"]
	if !strings.Contains(page, `src="../images/back-cover.jpg" alt="Back cover of The Salt Road"`) || strings.Count(page, "<img") != 1 {
		t.Errorf("the back cover page:\n%s", page)
	}
	opf := z["OEBPS/content.opf"]
	last := opf[strings.LastIndex(opf, "<itemref"):]
	if !strings.HasPrefix(last, `<itemref idref="back-cover" linear="no"/>`) {
		t.Errorf("the back cover is not last and out of the reading order:\n%s", opf[strings.Index(opf, "<spine"):])
	}
	if nav := z["OEBPS/nav.xhtml"]; !strings.Contains(nav, `href="text/back-cover.xhtml"`) {
		t.Errorf("the non-linear back cover is not reachable from the navigation document:\n%s", nav)
	}
	if strings.Count(opf, `linear="no"`) != 1 || !strings.HasPrefix(z["OEBPS/images/back-cover.jpg"], "\xff\xd8") {
		t.Error("the back cover's picture or the spine's other items")
	}
	if bc.InputHash == front.InputHash {
		t.Error("the back cover is not in the hash")
	}
	// the cover page option off: the back cover alone is still there; a front-only layout has none
	noCover := f.compile("epub3", publish.Options{BackCover: publish.True(), Cover: publish.False()})
	if _, ok := unzip(t, noCover.Body)["OEBPS/text/back-cover.xhtml"]; !ok {
		t.Error("no back cover with the cover off")
	}
	// own_cover off (the release switch): no picture of the layout anywhere, and the hash is the generated cover's
	rel := f.compile("epub3", publish.Options{BackCover: publish.True(), OwnCover: publish.False()})
	rz := unzip(t, rel.Body)
	if _, ok := rz["OEBPS/text/back-cover.xhtml"]; ok || rz["OEBPS/images/cover.jpg"] != "" {
		t.Error("own_cover off still has the layout's pictures in the EPUB")
	}
	plainRel := f.compile("epub3", publish.Options{OwnCover: publish.False()})
	if rel.InputHash == plainRel.InputHash { // options differ (back_cover is in the options json), but no cover hash is added
		t.Log("options alone make the hashes differ in other ways")
	}
	set(false, "A red door")
	if _, ok := unzip(t, f.compile("epub3", publish.Options{BackCover: publish.True()}).Body)["OEBPS/text/back-cover.xhtml"]; ok {
		t.Error("a layout with no back has a back cover page")
	}
	// the back of a wrap
	f.clearLayout()
	up := f.uploadCover("wrap", coverJPEG(3900, 2775), "6x9", "0.125")
	f.setLayout(coverlayout.SetIn{Shape: "wrap", Trim: "6x9", Alt: "wrap", Wrap: &coverlayout.Wrap{Attachment: up.AttachmentID}})
	wz := unzip(t, f.compile("epub3", publish.Options{BackCover: publish.True()}).Body)
	if img := wz["OEBPS/images/back-cover.jpg"]; !strings.HasPrefix(img, "\xff\xd8") {
		t.Error("a wrap's back is not in the EPUB")
	}
}

// The release switch: with own_cover off, a project with a layout and a project without one give the same file and the same
// hash, for the EPUB with a back cover and the PDF with a cover page (a public remote must carry nothing of a cover that the
// remote was not told to carry).
func TestOwnCoverOffIsTheGeneratedCoverEverywhere(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	opts := []struct {
		preset string
		o      publish.Options
	}{
		{"epub3", publish.Options{BackCover: publish.True(), OwnCover: publish.False()}},
		{"epub3", publish.Options{OwnCover: publish.False()}},
		{"print-6x9", publish.Options{CoverPage: publish.True(), OwnCover: publish.False()}},
	}
	without := make([]compile.Out, len(opts))
	for i, c := range opts {
		without[i] = f.compile(c.preset, c.o)
	}
	fu := f.uploadCover("front", coverJPEG(1900, 2900), "6x9", "")
	bu := f.uploadCover("back", coverJPEG(1900, 2901), "6x9", "")
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID}, Back: &coverlayout.Side{Mode: "image", Attachment: bu.AttachmentID}})
	for i, c := range opts {
		with := f.compile(c.preset, c.o)
		if with.InputHash != without[i].InputHash || !bytes.Equal(with.Body, without[i].Body) {
			t.Errorf("%s %+v: the layout reached the file or its hash with own_cover off", c.preset, c.o)
		}
	}
}

// A wrap made without a bleed is placed with none, and an explicit bleed_in that disagrees with the picture's is told.
func TestWrapWithNoBleed(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	// 6 x 9, no bleed, a 0.5 in spine, 200 dpi: 12.5 x 9 in
	up := f.uploadCover("wrap", coverJPEG(2500, 1800), "6x9", "0")
	zero := 0.0
	f.setLayout(coverlayout.SetIn{Shape: "wrap", Trim: "6x9", Alt: "x", Wrap: &coverlayout.Wrap{Attachment: up.AttachmentID, BleedIn: &zero}})
	out, err := f.exportCover(compile.CoverIn{PageCount: 200, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	doc := parse(t, out.Body)
	if p := doc.Pages[0]; !near(p.W, 12.5*72, 0.2) || !near(p.H, 9*72, 0.001) || p.Trim != nil {
		t.Errorf("page 1 %vx%v trim %v", p.W, p.H, p.Trim)
	}
	for _, w := range out.Warnings {
		if w.Code == "cover_wrap_bleed" || w.Code == "cover_wrap_stale" {
			t.Errorf("%s: %s", w.Code, w.Message)
		}
	}
	half := 0.125
	out, err = f.exportCover(compile.CoverIn{PageCount: 200, PageThicknessIn: 0.0025, BleedIn: &half})
	if err != nil {
		t.Fatal(err)
	}
	var told bool
	for _, w := range out.Warnings {
		told = told || w.Code == "cover_wrap_bleed"
	}
	if !told {
		t.Errorf("a bleed_in that is not the wrap's was not told: %v", codesOf(out.Warnings))
	}
}

// A dry run reads no picture (it has no data directory): the cover and the cover page are made with grey boxes where the
// pictures go, and say so, instead of failing.
func TestDryRunsOfTheCoverAndTheCoverPage(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	fu := f.uploadCover("front", coverJPEG(1900, 2900), "6x9", "")
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID}})
	res, err := f.reg.CallDry(f.ctx, f.owner, "export.cover", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatalf("export.cover: %v", err)
	}
	out := res.Out.(compile.Out)
	if len(parse(t, out.Body).Pages) != 1 || !strings.Contains(strings.Join(codesOf(out.Warnings), ","), "image_not_embedded") {
		t.Errorf("a dry run of the cover: %v", codesOf(out.Warnings))
	}
	res, err = f.reg.CallDry(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "print-6x9", Options: publish.Options{CoverPage: publish.True()}})
	if err != nil {
		t.Fatalf("export.compile: %v", err)
	}
	out = res.Out.(compile.Out)
	doc := parse(t, out.Body)
	if len(doc.Pages[0].Draws) != 0 || len(doc.Pages[0].Fills) == 0 || !strings.Contains(strings.Join(codesOf(out.Warnings), ","), "image_not_embedded") {
		t.Errorf("a dry run of the cover page: %d draws, %v", len(doc.Pages[0].Draws), codesOf(out.Warnings))
	}
}

// The cover is one file for the printer; the front and the proof are files of their own, each recorded with its own hash.
func TestCoverPartsAreFilesOfTheirOwn(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	fu := f.uploadCover("front", coverJPEG(1900, 2900), "6x9", "")
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID}})
	in := compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025}
	hashes := map[string]bool{}
	for part, want := range map[string]string{"": "-cover.pdf", "cover": "-cover.pdf", "front": "-cover-front.pdf", "proof": "-cover-proof.pdf"} {
		in.Part = part
		out, err := f.exportCover(in)
		if err != nil {
			t.Fatalf("part %q: %v", part, err)
		}
		if !strings.HasSuffix(out.Filename, want) || len(parse(t, out.Body).Pages) != 1 {
			t.Errorf("part %q: %s, %d pages", part, out.Filename, len(parse(t, out.Body).Pages))
		}
		hashes[out.InputHash] = true
	}
	if len(hashes) != 3 {
		t.Errorf("%d hashes for a cover, a front and a proof", len(hashes))
	}
	rows, err := f.d.Read().Query(`SELECT filename, manifest_json FROM exports WHERE preset LIKE 'cover/%' ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n, parts := 0, map[string]bool{}
	for rows.Next() {
		var name, manifest string
		_ = rows.Scan(&name, &manifest)
		n++
		for _, p := range []string{"cover", "front", "proof"} {
			if strings.Contains(manifest, `"part":"`+p+`"`) {
				parts[p] = true
			}
		}
	}
	if n != 4 || len(parts) != 3 {
		t.Errorf("%d exports recorded for four calls, parts %v", n, parts)
	}
	in.Part = "spine"
	if _, err := f.exportCover(in); ops.AsError(err) == nil || ops.AsError(err).Field != "part" {
		t.Errorf("an unknown part: %v", err)
	}
}

// What compile.cover_spec reports of the barcode box and the text margin is where and what the PDF draws, for a cover with no
// layout and no profile named (its own place), with a profile, and with a layout.
func TestSpecDescribesWhatTheCoverDraws(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	check1 := func(name string, in compile.CoverIn, doc func() *check.PDFDoc) {
		in.ProjectID = f.pid
		in.Barcode = true
		sp, err := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", in)
		if err != nil {
			t.Fatal(err)
		}
		bb := sp.(compile.CoverSpecOut).BarcodeBox
		var found bool
		for _, fl := range doc().Pages[0].Fills {
			if near(fl.Rect[2]-fl.Rect[0], bb.X1-bb.X0, 0.02) && near(fl.Rect[3]-fl.Rect[1], bb.Y1-bb.Y0, 0.02) && near(fl.Rect[0], bb.X0, 0.02) && near(fl.Rect[1], bb.Y0, 0.02) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: the spec says the barcode box is %+v; the file draws %+v", name, bb, doc().Pages[0].Fills)
		}
	}
	draw := func(in compile.CoverIn) func() *check.PDFDoc {
		return func() *check.PDFDoc {
			in.ProjectID, in.Barcode = f.pid, true
			out, err := f.exportCover(in)
			if err != nil {
				t.Fatal(err)
			}
			return parse(t, out.Body)
		}
	}
	base := compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025}
	check1("no layout, no profile", base, draw(base))
	if sp, _ := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025}); sp.(compile.CoverSpecOut).TextMarginIn != 0.125 {
		t.Errorf("a generated cover's guides keep 0.125 in: %v", sp.(compile.CoverSpecOut).TextMarginIn)
	}
	for _, prof := range []string{"kdp", "ingram"} {
		in := base
		in.Profile = prof
		check1("no layout, "+prof, in, draw(in))
	}
	fu := f.uploadCover("front", coverJPEG(1900, 2900), "6x9", "")
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID}})
	check1("layout", base, draw(base))
	in := base
	in.Profile = "ingram"
	check1("layout, ingram", in, draw(in))
}

// A layout with a generated side cannot be made for a right-to-left book, but compile.cover_spec still measures it (and
// says so); only export.cover refuses.
func TestRightToLeftLayoutWithAGeneratedSide(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	if _, err := f.d.Writer().Exec(`UPDATE projects SET language = 'he' WHERE id = ?`, f.pid); err != nil {
		t.Fatal(err)
	}
	bu := f.uploadCover("back", coverJPEG(1900, 2900), "6x9", "")
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "generated"}, Back: &coverlayout.Side{Mode: "image", Attachment: bu.AttachmentID}})
	sp, err := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", compile.CoverIn{ProjectID: f.pid, PageCount: 300, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatalf("the measurements are refused: %v", err)
	}
	s := sp.(compile.CoverSpecOut)
	if s.WrapWidthIn == 0 || s.Front.X1 > s.Spine.X0+0.01 || !strings.Contains(strings.Join(codesOf(s.Warnings), ","), "cover_rtl_text") {
		t.Errorf("%+v %v", s.Front, codesOf(s.Warnings))
	}
	_, err = f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025})
	if e := ops.AsError(err); e == nil || e.Data["reason"] != "cover_rtl_text" {
		t.Errorf("export.cover: %v", err)
	}
	ck, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.check", coverlayout.CheckIn{ID: f.pid, Profile: "kdp"})
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, w := range ck.(coverlayout.CheckOut).Warnings {
		seen = seen || w.Code == "cover_rtl_text"
	}
	if !seen {
		t.Error("the check does not say it")
	}
}

// Cover warnings say which part of the cover they are about, in export.cover, compile.cover_spec and the check alike; other
// compile warnings have none.
func TestCoverWarningsCarryTheirPart(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	fu := f.uploadCover("front", coverJPEG(1500, 2300), "6x9", "")
	bu := f.uploadCover("back", coverJPEG(1500, 2300), "6x9", "")
	f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: fu.AttachmentID}, Back: &coverlayout.Side{Mode: "image", Attachment: bu.AttachmentID}})
	in := compile.CoverIn{PageCount: 60, PageThicknessIn: 0.005, Profile: "kdp"}
	want := map[string]bool{"image_low_resolution:front": true, "image_low_resolution:back": true, "spine_few_pages:spine": true, "cover_barcode_area:back": true}
	got := func(ws []compile.Warning) map[string]bool {
		m := map[string]bool{}
		for _, w := range ws {
			m[w.Code+":"+w.Part] = true
		}
		return m
	}
	out, err := f.exportCover(in)
	if err != nil {
		t.Fatal(err)
	}
	in.ProjectID = f.pid
	sp, err := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", in)
	if err != nil {
		t.Fatal(err)
	}
	ck, err := f.reg.Call(f.ctx, f.owner, "project.cover_layout.check", coverlayout.CheckIn{ID: f.pid, Profile: "kdp", PageCount: 60, PageThicknessIn: 0.005})
	if err != nil {
		t.Fatal(err)
	}
	for name, m := range map[string]map[string]bool{"export": got(out.Warnings), "spec": got(sp.(compile.CoverSpecOut).Warnings)} {
		for k := range want {
			if !m[k] {
				t.Errorf("%s lacks %s: %v", name, k, m)
			}
		}
	}
	cm := map[string]bool{}
	for _, w := range ck.(coverlayout.CheckOut).Warnings {
		cm[w.Code+":"+w.Part] = true
	}
	for k := range want {
		if !cm[k] {
			t.Errorf("the check lacks %s: %v", k, cm)
		}
	}
	for _, w := range out.Warnings {
		if w.Code == "odd_page_count" || w.Code == "few_pages" {
			if w.Part != "" {
				t.Errorf("%s has part %q", w.Code, w.Part)
			}
		}
	}
	// a wrap's are about the wrap
	f.clearLayout()
	up := f.uploadCover("wrap", coverJPEG(2000, 1500), "6x9", "0.125")
	f.setLayout(coverlayout.SetIn{Shape: "wrap", Trim: "6x9", Alt: "x", Wrap: &coverlayout.Wrap{Attachment: up.AttachmentID}})
	wout, err := f.exportCover(compile.CoverIn{PageCount: 300, PageThicknessIn: 0.0025})
	if err != nil {
		t.Fatal(err)
	}
	if m := got(wout.Warnings); !m["cover_barcode_area:wrap"] || !m["cover_wrap_stale:wrap"] {
		t.Errorf("a wrap's warnings: %v", m)
	}
}

// The refusals of the cover ops carry a reason code, so a page can say them in its own words.
func TestCoverRefusalsCarryReasons(t *testing.T) {
	f := newFx(t, "The Salt Road")
	f.novel()
	call := func(in compile.CoverIn) *ops.Error {
		in.ProjectID = f.pid
		_, err := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", in)
		return ops.AsError(err)
	}
	ok := compile.CoverIn{PageCount: 100, PageThicknessIn: 0.0025}
	for _, c := range []struct {
		name, reason string
		in           func(compile.CoverIn) compile.CoverIn
	}{
		{"pages out of range", "cover_page_count_range", func(i compile.CoverIn) compile.CoverIn { i.PageCount = 2000; return i }},
		{"thickness", "cover_thickness_range", func(i compile.CoverIn) compile.CoverIn { i.PageThicknessIn = 0; return i }},
		{"bleed", "cover_bleed_range", func(i compile.CoverIn) compile.CoverIn { b := 2.0; i.BleedIn = &b; return i }},
		{"part", "cover_part", func(i compile.CoverIn) compile.CoverIn { i.Part = "x"; return i }},
		{"profile", "cover_profile", func(i compile.CoverIn) compile.CoverIn { i.Profile = "x"; return i }},
		{"not a print preset", "cover_preset_not_print", func(i compile.CoverIn) compile.CoverIn { i.Preset = "epub3"; return i }},
	} {
		if e := call(c.in(ok)); e == nil || e.Data["reason"] != c.reason {
			t.Errorf("%s: %v", c.name, e)
		}
	}
	// a book with nothing in it has no page count to take
	f = newFx(t, "Empty")
	empty := compile.CoverIn{PageThicknessIn: 0.0025}
	if e := call(empty); e == nil || e.Data["reason"] != "cover_page_count_needed" || e.Field != "page_count" {
		t.Errorf("an empty book: %v", e)
	}
}
