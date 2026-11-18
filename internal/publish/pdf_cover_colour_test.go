package publish

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

// iccProfile is a header-valid RGB profile (class mntr, the size it says, "acsp"): enough for a PDF to name as a colour
// space and for the checker to read; not a profile a colour engine could use.
func iccProfile(tag string) []byte {
	p := make([]byte, 160)
	copy(p[12:], "mntr")
	copy(p[16:], "RGB ")
	copy(p[36:], "acsp")
	copy(p[128:], tag)
	binary.BigEndian.PutUint32(p, uint32(len(p)))
	return p
}

// alphaPNG is a picture that is half transparent on its left half: colour (200, 0, 0) at alpha 128, then opaque blue.
func alphaPNG(t testing.TB, w, h int) *CoverImage {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x < w/2 {
				img.SetNRGBA(x, y, color.NRGBA{200, 0, 0, 128})
			} else {
				img.SetNRGBA(x, y, color.NRGBA{0, 0, 200, 255})
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return &CoverImage{Data: b.Bytes(), Ext: "png", Hash: "alpha", W: w, H: h}
}

// A picture's own profile is its colour space in the PDF, for a JPEG that is passed through, a PNG that is passed through and
// a PNG that has to be decoded; the profile is written once however many pages show the picture, a second profile is a
// second object, and a picture with none is DeviceRGB. The checker reads all of it.
func TestPicturesProfilesAreTheirColourSpaces(t *testing.T) {
	tc := coverTrims[0]
	cs := baseSpec(tc, "generic", 0.75, 300)
	p1, p2 := iccProfile("one"), iccProfile("two")
	front := testJPEG(t, 1900, 2850)
	front.ICC = p1
	plain := testPNG(t, 900, 1300) // no alpha: passed through
	plain.ICC = p1
	decoded := alphaPNG(t, 800, 1200) // an alpha channel: decoded
	decoded.ICC = p2
	cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelImage, Image: front},
		Back: CoverPanel{Mode: PanelPage, Image: decoded, Background: "#E8E1D5",
			Place: &CoverPlacement{CanvasW: 1707, CanvasH: 2560, Box: image.Rect(128, 128, 1579, 2432), Dst: image.Rect(213, 480, 1493, 1760)}}}
	doc, raw, _ := writeCover(t, cs)
	got := map[string]check.PDFImage{}
	for _, pg := range doc.Pages {
		for _, d := range pg.Draws {
			got[d.Image.Filter] = d.Image
			if d.Image.ColorSpace != "ICCBased" {
				t.Errorf("a %s picture's colour space is %s", d.Image.Filter, d.Image.ColorSpace)
			}
		}
	}
	if !bytes.Equal(got["DCTDecode"].ICC, p1) || !bytes.Equal(got["FlateDecode"].ICC, p2) {
		t.Error("a picture's profile is not its own in the file")
	}
	if n := strings.Count(string(raw), "/N 3 /Alternate /DeviceRGB"); n != 2 {
		t.Errorf("%d profile objects for two profiles over three pages", n)
	}
	// the passed-through PNG with the first profile shares the JPEG's object
	cs.Art.Back = CoverPanel{Mode: PanelImage, Image: plain}
	doc, raw, _ = writeCover(t, cs)
	if n := strings.Count(string(raw), "/N 3 /Alternate /DeviceRGB"); n != 1 {
		t.Errorf("%d profile objects for one profile on two pictures", n)
	}
	for _, d := range doc.Pages[0].Draws {
		if d.Image.ColorSpace != "ICCBased" || !bytes.Equal(d.Image.ICC, p1) {
			t.Errorf("%+v", d.Image.ColorSpace)
		}
	}
	// none: DeviceRGB
	front.ICC, plain.ICC = nil, nil
	doc, raw, _ = writeCover(t, cs)
	if strings.Contains(string(raw), "ICCBased") || doc.Pages[0].Draws[0].Image.ColorSpace != "DeviceRGB" {
		t.Error("a picture with no profile is not DeviceRGB")
	}
}

// A picture with an alpha channel is laid on what is behind it and written with no soft mask: on the panel's background for
// a page side, on white for an image side and a wrap. The first sample is the left half's, half transparent.
func TestTransparencyIsFlattened(t *testing.T) {
	tc := coverTrims[0]
	inflate := func(b []byte) []byte {
		r, err := zlib.NewReader(bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(r)
		return out
	}
	mix := func(c, bg uint8) uint8 { return uint8((uint32(c)*128 + uint32(bg)*127 + 127) / 255) }
	for _, mode := range []string{PanelPage, PanelImage, "wrap"} {
		cs := baseSpec(tc, "generic", 0.75, 300)
		var bg = [3]uint8{0xE8, 0xE1, 0xD5}
		switch mode {
		case PanelPage:
			cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelPage, Image: alphaPNG(t, 800, 1200), Background: "#E8E1D5",
				Place: &CoverPlacement{CanvasW: 1707, CanvasH: 2560, Box: image.Rect(0, 0, 1707, 2560), Dst: image.Rect(100, 100, 900, 1300)}}}
		case PanelImage:
			bg = white
			cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelImage, Image: alphaPNG(t, 800, 1200)}}
		default:
			bg = white
			w := mkWrap(t, tc, 0.125, 0.75, 100, false)
			w.Image = *alphaPNG(t, w.Image.W, w.Image.H)
			cs.Art = &CoverArt{Shape: ArtWrap, Wrap: w}
		}
		doc, _, _ := writeCover(t, cs)
		var img check.PDFImage
		for _, d := range doc.Pages[0].Draws {
			img = d.Image
		}
		if img.Masked || img.ColorSpace != "DeviceRGB" {
			t.Errorf("%s: masked %v, %s", mode, img.Masked, img.ColorSpace)
			continue
		}
		px := inflate(img.Data)
		if len(px) != img.W*img.H*3 {
			t.Fatalf("%s: %d bytes of samples for %dx%d", mode, len(px), img.W, img.H)
		}
		if px[0] != mix(200, bg[0]) || px[1] != mix(0, bg[1]) || px[2] != mix(0, bg[2]) {
			t.Errorf("%s: the half-transparent pixel is %v, want %d %d %d over %v", mode, px[:3], mix(200, bg[0]), mix(0, bg[1]), mix(0, bg[2]), bg)
		}
		last := px[len(px)-3:]
		if last[0] != 0 || last[1] != 0 || last[2] != 200 {
			t.Errorf("%s: the opaque pixel is %v", mode, last)
		}
	}
}

// A zero spine is too narrow for type and says so, as the spec has always said.
func TestZeroSpineIsNarrow(t *testing.T) {
	cs := baseSpec(coverTrims[0], "generic", 0, 300)
	ws, err := CoverWarnings(cs)
	if err != nil {
		t.Fatal(err)
	}
	if !hasWarn(ws, "spine_narrow") {
		t.Errorf("%s", warnCodes(ws))
	}
}

// Print asks for 300 dpi; a picture made at 300 for the trim lands at about 288 once it covers the bleed, which is not
// reported, and one well under is, with the sentence that is true of an image side (it is scaled up to cover).
func TestResolutionThresholdAndWords(t *testing.T) {
	tc := coverTrims[0]
	plan := func(w, h int) []Warn {
		cs := baseSpec(tc, "generic", 0.75, 300)
		cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelImage, Image: &CoverImage{Ext: "jpg", Hash: "x", W: w, H: h}}, Back: CoverPanel{Mode: PanelNone}}
		ws, err := CoverWarnings(cs)
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	if ws := plan(1800, 2700); hasWarn(ws, "image_low_resolution") {
		t.Errorf("288 dpi: %s", warnCodes(ws))
	}
	ws := plan(1700, 2550)
	var msg string
	for _, w := range ws {
		if w.Code == "image_low_resolution" {
			msg = w.Message
		}
	}
	if msg == "" || !strings.Contains(msg, "scaled up") || strings.Contains(msg, "never enlarged") {
		t.Errorf("an image side: %q", msg)
	}
	// a picture cropped to fill: the share lost beyond the bleed is its own warning
	if ws := plan(2400, 2400); !hasWarn(ws, "cover_print_cropped") {
		t.Errorf("a square picture on a portrait panel: %s", warnCodes(ws))
	}
	if ws := plan(1875, 2775); hasWarn(ws, "cover_print_cropped") {
		t.Errorf("a picture in the panel's proportions: %s", warnCodes(ws))
	}
	// a page side keeps the old sentence
	cs := baseSpec(tc, "generic", 0.75, 300)
	cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelPage, Image: &CoverImage{Ext: "jpg", Hash: "y", W: 500, H: 700}, Background: "#2F5D50",
		Place: &CoverPlacement{CanvasW: 1707, CanvasH: 2560, Box: image.Rect(0, 0, 1707, 2560), Dst: image.Rect(100, 100, 600, 800)}}}
	pws, _ := CoverWarnings(cs)
	for _, w := range pws {
		if w.Code == "image_low_resolution" && !strings.Contains(w.Message, "never enlarged") {
			t.Errorf("a page side: %q", w.Message)
		}
	}
}

// The interior's cover page cut from a wrap has no bleed on the spine side, and no more bleed on its outer edges than the wrap
// was made with; its low-resolution warning is about the front, not the whole wrap.
func TestCoverPageFromAWrap(t *testing.T) {
	tc := coverTrims[0]
	for _, wb := range []float64{0.125, 0.0625} {
		b := featureBook()
		o := print6x9()
		o.CoverPage, o.CropMarks = True(), True()
		w := mkWrap(t, tc, wb, 0.75, 120, true)
		b.PrintCover = &CoverSpec{Title: "Features", Author: "A. Writer", Color: "#2F5D50", Language: "en", Art: &CoverArt{Shape: ArtWrap, Wrap: w}}
		out, ws, err := WritePDF(b, o)
		if err != nil {
			t.Fatal(err)
		}
		doc, probs := check.ParsePDF(out)
		for _, p := range probs {
			t.Error(p)
		}
		c0 := doc.Pages[0]
		if len(c0.Draws) != 1 || c0.Draws[0].Clip == nil {
			t.Fatalf("%+v", c0.Draws)
		}
		clip := c0.Draws[0].Clip
		trim := c0.Trim
		off := 18.0 + 9                                                 // the slug and the bleed of the page: where the trim box starts
		if !near(trim[0], off, 0.01) || !near(clip[0], trim[0], 0.01) { // nothing on the spine side (the left, for a front on the right)
			t.Errorf("wrap bleed %v: clip %v trim %v: art on the spine side", wb, clip, trim)
		}
		if !near(clip[2], trim[2]+wb*72, 0.02) || !near(clip[1], trim[1]-wb*72, 0.02) || !near(clip[3], trim[3]+wb*72, 0.02) {
			t.Errorf("wrap bleed %v: clip %v trim %v", wb, clip, trim)
		}
		var low string
		for _, x := range ws {
			if x.Code == "image_low_resolution" {
				low = x.Message
			}
		}
		if !strings.Contains(low, "The front is cut from a wrap") {
			t.Errorf("the warning: %q", low)
		}
	}
}

// The EPUB checker knows a spine item outside the reading order must be linked from the navigation document.
func TestEPUBCheckerKnowsNonLinearItems(t *testing.T) {
	b := bookFrom("Back", feature, feature)
	b.BackCoverImage = testJPEG(t, 600, 900).Data
	out, err := WriteEPUB(b, Options{Format: "epub", BackCover: True(), TitlePage: True()})
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.EPUB(out); len(probs) > 0 {
		t.Fatalf("a back cover: %v", probs)
	}
	rewrite := func(edit func(name, body string) string) []byte {
		zr, _ := zip.NewReader(bytes.NewReader(out), int64(len(out)))
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, f := range zr.File {
			rc, _ := f.Open()
			data, _ := io.ReadAll(rc)
			rc.Close()
			body := data
			if f.Name != "mimetype" {
				body = []byte(edit(f.Name, string(data)))
			}
			w, _ := zw.CreateHeader(&zip.FileHeader{Name: f.Name, Method: f.Method})
			w.Write(body)
		}
		zw.Close()
		return buf.Bytes()
	}
	unlinked := rewrite(func(name, body string) string {
		if name == "OEBPS/nav.xhtml" {
			return strings.Replace(body, `<li><a epub:type="backmatter" href="text/back-cover.xhtml">Back cover</a></li>`, "", 1)
		}
		return body
	})
	probs := check.EPUB(unlinked)
	if len(probs) == 0 || !strings.Contains(strings.Join(probs, "|"), "linear=no") {
		t.Errorf("an unreachable non-linear item: %v", probs)
	}
	bad := rewrite(func(name, body string) string {
		if name == "OEBPS/content.opf" {
			return strings.Replace(body, `linear="no"`, `linear="maybe"`, 1)
		}
		return body
	})
	if probs := check.EPUB(bad); len(probs) == 0 {
		t.Error("linear=maybe passed")
	}
}
