package publish

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

// No page of a book that reports no problem ends with one line of a paragraph
// or begins with the last.
func TestNoWidowsOrOrphans(t *testing.T) {
	for _, o := range []Options{print6x9(), func() Options { o := print6x9(); o.PageSize = "5x8"; o.FontSize = 10.5; return o }()} {
		l := mustLayout(t, bookOf(40000), o)
		for _, w := range l.Warnings {
			if w.Code == "widow_orphan" {
				t.Fatalf("the layout reports a broken rule: %s", w.Message)
			}
		}
		for i, sh := range l.sheets {
			if len(sh.items) == 0 {
				continue
			}
			first, last := sh.items[0].it, sh.items[len(sh.items)-1].it
			if first.kind == vLine && first.par != nil && !first.first && len(first.par.lines) >= 2 && first.ln.to == first.par.lines[len(first.par.lines)-1].to && first.par.lines[len(first.par.lines)-1].from == first.ln.from {
				t.Errorf("page %d begins with the last line of a paragraph (a widow)", i+1)
			}
			if last.kind == vLine && last.par != nil && last.first && len(last.par.lines) >= 2 && last.par.lines[0].from == last.ln.from && last.h > 0 {
				// the line is alone only when it is the first and the paragraph goes on
				t.Errorf("page %d ends with the first line of a paragraph (an orphan)", i+1)
			}
		}
	}
}

// Everything a print preset says it prints is in some embedded font.
func TestPresetCharactersAreDrawable(t *testing.T) {
	ts := testFaces(t)
	o := print6x9()
	for _, txt := range []string{SceneBreakText(o), SceneBreakText(Options{SceneBreak: "stars"}), SceneBreakText(Options{SceneBreak: "hash"}), "THE END", "Contents", "Notes", "•", "…", "—", "©"} {
		for _, r := range txt {
			if r == ' ' {
				continue
			}
			if _, _, ok := ts.pick(r, 0); !ok {
				t.Errorf("no embedded font has %q (U+%04X)", r, r)
			}
		}
	}
}

func TestMissingGlyphsAndScriptsAreReported(t *testing.T) {
	b := bookFrom("Scripts", "Latin text, then 中文字符 and عربى and हिन्दी and 한국어, then a snowman ☃ and a fleuron ❦.")
	out, warns, err := WritePDF(b, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]string{}
	for _, w := range warns {
		codes[w.Code] = w.Message
	}
	if !strings.Contains(codes["script_unsupported"], "Han") || !strings.Contains(codes["script_unsupported"], "Arabic") || !strings.Contains(codes["script_unsupported"], "Devanagari") {
		t.Errorf("script_unsupported: %q", codes["script_unsupported"])
	}
	if probs := check.PDF(out); len(probs) > 0 {
		t.Error(probs[0])
	}
	doc, _ := check.ParsePDF(out)
	txt := doc.Text()
	if strings.Contains(txt, "中") || strings.Contains(txt, "ع") {
		t.Error("a character of an unsupported script was drawn")
	}
	if !strings.Contains(txt, "☃") || !strings.Contains(txt, "❦") {
		t.Errorf("a fallback glyph is missing: %q", txt)
	}
	// RTL languages are refused, never set backwards
	if _, err := LayoutPDF(b, func() Options { o := print6x9(); o.Language = "he"; return o }()); err != ErrRTL {
		t.Errorf("hebrew: %v", err)
	}
	// a font that is not embedded is said to be replaced
	_, warns, _ = WritePDF(b, func() Options { o := print6x9(); o.Font = "Garamond"; return o }())
	var subst bool
	for _, w := range warns {
		if w.Code == "font_substituted" {
			subst = true
		}
	}
	if !subst {
		t.Error("Garamond was replaced silently")
	}
}

func TestDropCapAndFirstParagraph(t *testing.T) {
	o := print6x9()
	o.DropCap = True()
	for _, md := range []string{"“Quoted opening,” she said, and went on for a while so that the paragraph has more than three lines of text in it, which it needs for a drop cap to be worth setting at all in this narrow measure of text on the page and a few more words.", "1 begins with a digit and goes on and on and on for several lines in the measure of a six by nine page so that it is long enough.", "Short."} {
		out, _, err := WritePDF(bookFrom("Drop", md+"\n\nSecond paragraph."), o)
		if err != nil {
			t.Fatal(err)
		}
		if probs := check.PDF(out); len(probs) > 0 {
			t.Error(probs[0])
		}
	}
	l := mustLayout(t, bookFrom("Drop", strings.Repeat("Alpha beta gamma delta epsilon zeta eta theta. ", 40)), o)
	var found bool
	for _, sh := range l.sheets {
		for _, p := range sh.items {
			if p.it.par != nil && p.it.par.dropCap != nil && p.it.first {
				found = true
			}
		}
	}
	if !found {
		t.Error("no drop cap")
	}
}

func TestImagesOfEveryKind(t *testing.T) {
	mk := func(w, h int, alpha bool, enc func(*bytes.Buffer, image.Image) error, pal bool) Image {
		var m image.Image
		if pal {
			p := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}, color.RGBA{0, 255, 0, 255}})
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					p.SetColorIndex(x, y, uint8((x+y)%3))
				}
			}
			m = p
		} else {
			r := image.NewNRGBA(image.Rect(0, 0, w, h))
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					a := uint8(255)
					if alpha {
						a = uint8(x * 255 / w)
					}
					r.SetNRGBA(x, y, color.NRGBA{uint8(x * 255 / w), uint8(y * 255 / h), 90, a})
				}
			}
			m = r
		}
		var buf bytes.Buffer
		if err := enc(&buf, m); err != nil {
			t.Fatal(err)
		}
		ext := "png"
		mime := "image/png"
		if bytes.HasPrefix(buf.Bytes(), []byte("GIF")) {
			ext, mime = "gif", "image/gif"
		}
		return Image{Data: buf.Bytes(), Mime: mime, Ext: ext, Width: w, Height: h, Hash: fmt.Sprintf("%s%d%v%v", ext, w, alpha, pal)}
	}
	pngEnc := func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) }
	gifEnc := func(b *bytes.Buffer, m image.Image) error {
		p := image.NewPaletted(m.Bounds(), color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}})
		return gif.Encode(b, p, nil)
	}
	book := bookFrom("Images", "![a](/files/att_opaque0001)\n\n![b](/files/att_alpha00002)\n\n![c](/files/att_palette003)\n\n![d](/files/att_gif0000004)\n\n![e](/files/att_webp000005)\n\n![f](/files/att_missing006)\n\n![g](/files/att_jpeg000007)")
	book.Images = map[string]Image{
		"/files/att_opaque0001": mk(300, 200, false, pngEnc, false),
		"/files/att_alpha00002": mk(300, 200, true, pngEnc, false),
		"/files/att_palette003": mk(120, 90, false, pngEnc, true),
		"/files/att_gif0000004": mk(100, 100, false, gifEnc, false),
		"/files/att_webp000005": {Data: []byte("RIFF....WEBP"), Mime: "image/webp", Ext: "webp", Width: 10, Height: 10, Hash: "w"},
		"/files/att_jpeg000007": jpegImage(500, 300),
	}
	out, warns, err := WritePDF(book, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	n := 0
	for _, p := range doc.Pages {
		n += p.Images
	}
	if n != 5 { // opaque, alpha, palette, gif, jpeg
		t.Errorf("%d images drawn, want 5", n)
	}
	var low bool
	for _, w := range warns {
		if w.Code == "image_low_resolution" {
			low = true
		}
	}
	if !low {
		t.Error("no low-resolution warning")
	}
	if !strings.Contains(doc.Text(), "not included") {
		t.Error("the unusable images are not marked")
	}
	// bytes not read (a preview or a dry run): laid out the same, drawn as a grey box with a warning
	l1 := mustLayout(t, book, print6x9())
	noBytes := *book
	noBytes.Images = map[string]Image{}
	for k, v := range book.Images {
		v.Data = nil
		noBytes.Images[k] = v
	}
	l2 := mustLayout(t, &noBytes, print6x9())
	if l1.PageCount() != l2.PageCount() {
		t.Errorf("the page count depends on whether image bytes were read: %d vs %d", l1.PageCount(), l2.PageCount())
	}
	out2, warns2, err := l2.Render()
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.PDF(out2); len(probs) > 0 {
		t.Error(probs[0])
	}
	var ne bool
	for _, w := range warns2 {
		if w.Code == "image_not_embedded" {
			ne = true
		}
	}
	if !ne {
		t.Error("no warning for images that could not be embedded")
	}
}

func TestCropMarksSetBoxes(t *testing.T) {
	o := print6x9()
	o.CropMarks = True()
	out, _, err := WritePDF(bookOf(3000), o)
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	pg := doc.Pages[0]
	if pg.W != 432+54 || pg.H != 648+54 || len(pg.Trim) != 4 || pg.Trim[0] != 27 || pg.Trim[2] != 27+432 {
		t.Errorf("media %vx%v trim %v", pg.W, pg.H, pg.Trim)
	}
}

func TestChapterStartVariants(t *testing.T) {
	b := bookOf(8000)
	for _, start := range []string{"recto", "new-page", "continuous"} {
		o := print6x9()
		o.ChapterStart = start
		l := mustLayout(t, b, o)
		odd := true
		for i, s := range b.Sections {
			if s.Kind != SecChapter || l.SectionPage(i) == 0 {
				continue
			}
			if l.SectionPage(i)%2 == 0 {
				odd = false
			}
		}
		if start == "recto" && !odd {
			t.Error("a chapter starts on a left-hand page in recto mode")
		}
		if start == "continuous" {
			out, _, _ := l.Render()
			if probs := check.PDF(out); len(probs) > 0 {
				t.Error(probs[0])
			}
		}
	}
}

func TestFolioPositionsAndNoHeadOnOpeners(t *testing.T) {
	l := mustLayout(t, bookOf(8000), print6x9())
	out, _, _ := l.Render()
	doc, _ := check.ParsePDF(out)
	for i, sh := range l.sheets {
		if sh.kind == pageBlank && doc.Pages[i].Text() != "" {
			t.Errorf("blank page %d has text", i+1)
		}
		if sh.noHead && sh.kind == pageBody {
			for _, ln := range doc.Pages[i].Lines {
				if ln.Y > doc.Pages[i].H-54 {
					t.Errorf("an opener page %d has a running head: %q", i+1, ln.Text)
				}
			}
		}
	}
}
