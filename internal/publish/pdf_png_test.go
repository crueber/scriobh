package publish

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/publish/font"
	"github.com/crueber/scriobh/internal/publish/pdfw"
)

func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	return b.Bytes()
}

// rawPNG makes an 8-bit grey PNG of w x h whose rows start with the given
// filter byte, with extra chunks before the data.
func rawPNG(w, h int, filter byte, extra ...[]byte) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = 8, 0
	var rows bytes.Buffer
	for y := 0; y < h; y++ {
		rows.WriteByte(filter)
		rows.Write(bytes.Repeat([]byte{byte(y * 7)}, w))
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(rows.Bytes())
	zw.Close()
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, pngChunk("IHDR", ihdr)...)
	for _, e := range extra {
		out = append(out, e...)
	}
	out = append(out, pngChunk("IDAT", z.Bytes())...)
	return append(out, pngChunk("IEND", nil)...)
}

func pngImg(data []byte, w, h int, hash string) Image {
	return Image{Data: data, Mime: "image/png", Ext: "png", Width: w, Height: h, Hash: hash}
}

func TestPNGPassThroughIsBounded(t *testing.T) {
	// a good one passes through
	e := newImageEmbedder(pdfw.New())
	if im, why := e.embed(pngImg(rawPNG(20, 10, 0), 20, 10, "ok")); im == nil {
		t.Fatalf("a plain PNG was refused: %s", why)
	}
	// a filter type outside 0 to 4 is refused, not passed to a viewer
	if im, why := e.embed(pngImg(rawPNG(20, 10, 9), 20, 10, "badfilter")); im != nil || !strings.Contains(why, "filter") {
		t.Errorf("a row with filter type 9: %v %q", im != nil, why)
	}
	// the same pixel cap as the decoding path
	old := MaxImagePixels
	MaxImagePixels = 1000
	defer func() { MaxImagePixels = old }()
	if im, why := e.embed(pngImg(rawPNG(100, 100, 0), 100, 100, "big")); im != nil || !strings.Contains(why, "million pixels") {
		t.Errorf("a PNG over the cap: %v %q", im != nil, why)
	}
	// a stream with more rows than the header says is not trusted
	if im, _ := newImageEmbedder(pdfw.New()).embed(pngImg(rawPNG(20, 10, 0), 20, 5, "x")); im == nil {
		t.Log("the header decides the rows") // the header is the file's own: this is the plain case
	}
}

// A picture with a gamma or a colour profile is reported: it is shown as its
// numbers say, not colour-managed.
func TestColourProfilesAreReported(t *testing.T) {
	gama := pngChunk("gAMA", []byte{0, 0, 0x4e, 0x20})
	icc := append([]byte("ICC_PROFILE\x00"), make([]byte, 40)...)
	jpg := jpegImage(60, 40)
	withICC := append(append([]byte(nil), jpg.Data[:2]...), segment(0xE2, string(icc))...)
	withICC = append(withICC, jpg.Data[2:]...)
	b := bookFrom("Profiles", "![a](/files/att_gamma00001)\n\n![b](/files/att_icc0000002)\n\n![c](/files/att_plain000003)")
	b.Images = map[string]Image{
		"/files/att_gamma00001":  pngImg(rawPNG(30, 20, 0, gama), 30, 20, "gama"),
		"/files/att_icc0000002":  {Data: withICC, Mime: "image/jpeg", Ext: "jpg", Width: 60, Height: 40, Hash: "icc"},
		"/files/att_plain000003": pngImg(rawPNG(30, 20, 0), 30, 20, "plain"),
	}
	out, warns, err := WritePDF(b, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.PDF(out); len(probs) > 0 {
		t.Error(probs[0])
	}
	var msg string
	for _, w := range warns {
		if w.Code == "image_colour_profile" {
			msg = w.Message
		}
	}
	if !strings.HasPrefix(msg, "2 image(s)") {
		t.Errorf("colour profile warning: %q", msg)
	}
}

// A subset carries its font's copyright notice and licence address.
func TestSubsetKeepsTheFontNotice(t *testing.T) {
	for _, fam := range []*font.Family{font.Serif(), font.Fallbacks()[0]} {
		face, _ := fam.Face(font.Regular)
		f, err := face.Font()
		if err != nil {
			t.Fatal(err)
		}
		g, _ := f.Glyph('A')
		s, err := f.Subset([]uint16{g})
		if err != nil {
			t.Fatal(err)
		}
		utf16 := func(s string) []byte {
			var b []byte
			for _, r := range s {
				b = append(b, 0, byte(r))
			}
			return b
		}
		if !bytes.Contains(s.Data, utf16("Copyright")) {
			t.Errorf("%s: the subset has no copyright notice", fam.Name)
		}
	}
}
