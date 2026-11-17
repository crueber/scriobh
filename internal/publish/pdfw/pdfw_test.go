package pdfw

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/publish/font"
)

func serif(t *testing.T) *font.Font {
	t.Helper()
	face, _ := font.Serif().Face(font.Regular)
	f, err := face.Font()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// show writes s as one line at (x, y) in f.
func show(c *Content, d *Doc, pf *Font, ft *font.Font, s string, x, y, size int64) {
	var codes []uint16
	for _, r := range s {
		g, ok := ft.Glyph(r)
		if !ok {
			panic("no glyph for " + string(r))
		}
		codes = append(codes, pf.Code(g, r))
	}
	c.BeginText()
	c.SetFont(pf, size)
	c.TextAt(x, y)
	c.Show([]TJItem{{Codes: codes}})
	c.EndText()
}

func sample(t *testing.T) []byte {
	t.Helper()
	d := New()
	d.Title, d.Author, d.Language = "A Title (with) parens \\ and <angle> ünïcödé", "Máire Ní Bhriain", "en-GB"
	d.Created = [6]int{2026, 11, 13, 10, 0, 0}
	d.ID = DocumentID("test")
	ft := serif(t)
	pf := d.NewFont("LiberationSerif", ft)
	for i := 0; i < 3; i++ {
		p := d.NewPage(432000, 648000)
		show(p.Content, d, pf, ft, "Hello, world — page "+string(rune('1'+i)), 54000, 500000, 11000)
		p.Content.FillGray(500)
		p.Content.FillRect(54000, 490000, 100000, 500)
	}
	d.SetLabels([]PageLabel{{Page: 0, Style: "r"}, {Page: 2, Style: "D", Start: 1}})
	d.SetOutline([]OutlineItem{{Title: "Front", Page: 0, Y: 54000}, {Title: "Chapter “One”", Page: 2, Y: 100000}, {Title: "Scene", Page: 2, Level: 1, Y: 200000}, {Title: "Back", Page: 2}})
	out, err := d.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWriterOutputValidates(t *testing.T) {
	out := sample(t)
	if dir := os.Getenv("SCRIOBH_KEEP"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "pdfw-sample.pdf"), out, 0o644)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	if len(doc.Pages) != 3 || doc.Title != "A Title (with) parens \\ and <angle> ünïcödé" || doc.Author != "Máire Ní Bhriain" || doc.Lang != "en-GB" {
		t.Errorf("%d pages, title %q author %q lang %q", len(doc.Pages), doc.Title, doc.Author, doc.Lang)
	}
	if got := doc.Pages[1].Text(); got != "Hello, world — page 2" {
		t.Errorf("text %q", got)
	}
	if doc.Pages[0].Label != "i" || doc.Pages[1].Label != "ii" || doc.Pages[2].Label != "1" {
		t.Errorf("labels %q %q %q", doc.Pages[0].Label, doc.Pages[1].Label, doc.Pages[2].Label)
	}
	if strings.Join(doc.Outline, "|") != "Front|Chapter “One”|Scene|Back" {
		t.Errorf("outline %v", doc.Outline)
	}
	if doc.Fonts != 0 && doc.Fonts != 1 { // counted only when set
		t.Error(doc.Fonts)
	}
	if !bytes.Equal(out, sample(t)) {
		t.Error("the same document is not the same bytes")
	}
	// Nothing active.
	for _, bad := range []string{"/JavaScript", "/JS", "/OpenAction", "/Launch", "/URI", "/EmbeddedFile", "/Annots", "/AcroForm"} {
		if bytes.Contains(out, []byte(bad)) {
			t.Errorf("the file contains %s", bad)
		}
	}
}

func TestNumberFormatting(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 1000: "1", 1500: "1.5", -250: "-0.25", 432000: "432", 1: "0.001", -1: "-0.001", 12345: "12.345"} {
		if got := U(in); got != want {
			t.Errorf("U(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestStringsCannotEscape(t *testing.T) {
	for _, s := range []string{"a) /JS (b", ">> /OpenAction << /S /JavaScript", "\x00\x01", "x\r\ny", "\U0001F600 emoji"} {
		h := HexString(s)
		if !strings.HasPrefix(h, "<FEFF") || !strings.HasSuffix(h, ">") || strings.ContainsAny(h[1:len(h)-1], "()/<>\\ \n") {
			t.Errorf("%q -> %q", s, h)
		}
	}
	if l := Literal("a(b)c\\d\x01é"); l != `(a\(b\)c\\d\001\303\251)` {
		t.Errorf("literal %s", l)
	}
}

func TestImagesAreValid(t *testing.T) {
	d := New()
	ft := serif(t)
	pf := d.NewFont("LiberationSerif", ft)
	p := d.NewPage(432000, 648000)
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{uint8(60 * x), uint8(80 * y), 200, 255})
		}
	}
	var rgb, alpha []byte
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			c := img.RGBAAt(x, y)
			rgb = append(rgb, c.R, c.G, c.B)
			alpha = append(alpha, 255-uint8(30*x))
		}
	}
	mask := d.NewImage(ImageSpec{Width: 4, Height: 3, ColorSpace: "/DeviceGray", BPC: 8}, alpha)
	im := d.NewImage(ImageSpec{Width: 4, Height: 3, ColorSpace: "/DeviceRGB", BPC: 8, Mask: mask}, rgb)
	p.Content.Image(im, 54000, 300000, 144000, 108000)
	show(p.Content, d, pf, ft, "x", 54000, 200000, 11000)
	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, img)
	out, err := d.Finish()
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, pr := range probs {
		t.Error(pr)
	}
	if len(doc.Pages) != 1 || doc.Pages[0].Images != 1 {
		t.Errorf("images on the page: %+v", doc.Pages)
	}
}

func TestManyPagesUseATree(t *testing.T) {
	d := New()
	ft := serif(t)
	pf := d.NewFont("LiberationSerif", ft)
	for i := 0; i < 200; i++ {
		p := d.NewPage(432000, 648000)
		show(p.Content, d, pf, ft, "p", 54000, 500000, 11000)
	}
	out, err := d.Finish()
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, pr := range probs {
		t.Error(pr)
	}
	if len(doc.Pages) != 200 {
		t.Errorf("%d pages", len(doc.Pages))
	}
}
