package imagesafe

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func pngChunk(typ string, data []byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, uint32(len(data)))
	b.WriteString(typ)
	b.Write(data)
	c := crc32.NewIEEE()
	c.Write([]byte(typ))
	c.Write(data)
	_ = binary.Write(&b, binary.BigEndian, c.Sum32())
	return b.Bytes()
}

func plainPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 20), uint8(y * 20), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func none(t *testing.T, out []byte, canaries ...string) {
	t.Helper()
	for _, c := range canaries {
		if bytes.Contains(out, []byte(c)) {
			t.Errorf("the output keeps %q", c)
		}
	}
}

func TestPNGIsAnAllowList(t *testing.T) {
	p := plainPNG(t, 4, 3)
	iend := len(p) - 12
	var x []byte
	x = append(x, p[:iend]...)
	for _, c := range []string{"caBX", "prVt", "eXIf", "tEXt", "iTXt", "zTXt", "tIME", "zzZz"} {
		x = append(x, pngChunk(c, []byte("PNG_CANARY_"+c))...)
	}
	x = append(x, pngChunk("pHYs", make([]byte, 9))...) // kept
	x = append(x, p[iend:]...)
	x = append(x, []byte("TRAILING_AFTER_IEND_CANARY")...)
	out, info, err := Clean("png", x)
	if err != nil {
		t.Fatal(err)
	}
	none(t, out, "PNG_CANARY_", "TRAILING_AFTER_IEND_CANARY")
	if !bytes.Contains(out, []byte("pHYs")) || !bytes.Contains(out, []byte("IDAT")) {
		t.Error("an allowed chunk was dropped")
	}
	if info.Width != 4 || info.Height != 3 {
		t.Errorf("info %+v", info)
	}
	if _, err := png.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("the cleaned PNG does not decode: %v", err)
	}
	// A declared size past the cap is refused before anything is decoded.
	big := append([]byte(nil), p...)
	binary.BigEndian.PutUint32(big[16:], 50_000)
	binary.BigEndian.PutUint32(big[20:], 50_000)
	if _, _, err := Clean("png", big); err == nil {
		t.Error("a 2.5-gigapixel PNG was accepted")
	}
	if _, _, err := Clean("png", []byte("nope")); err == nil {
		t.Error("garbage accepted")
	}
}

func webpChunk(cc string, d []byte) []byte {
	var b bytes.Buffer
	b.WriteString(cc)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(d)))
	b.Write(d)
	if len(d)%2 == 1 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

func riff(body []byte) []byte {
	var f bytes.Buffer
	f.WriteString("RIFF")
	_ = binary.Write(&f, binary.LittleEndian, uint32(len(body)+4))
	f.WriteString("WEBP")
	f.Write(body)
	return f.Bytes()
}

func TestWebPIsAnAllowListWithDimensions(t *testing.T) {
	vp8x := make([]byte, 10)
	vp8x[0] = 0x08 | 0x04 | 0x20 // EXIF, XMP, ICC
	vp8x[4], vp8x[7] = 99, 49    // 100 x 50
	vp8l := append([]byte{0x2f}, 0, 0, 0, 0)
	body := webpChunk("VP8X", vp8x)
	body = append(body, webpChunk("ICCP", []byte("icc"))...)
	body = append(body, webpChunk("C2PA", []byte("WEBP_C2PA_CANARY"))...)
	body = append(body, webpChunk("XMP ", []byte("WEBP_XMP_CANARY"))...)
	body = append(body, webpChunk("EXIF", []byte("WEBP_EXIF_CANARY"))...)
	body = append(body, webpChunk("exif", []byte("WEBP_LOWER_EXIF_CANARY"))...)
	body = append(body, webpChunk("VP8L", vp8l)...)
	out, info, err := Clean("webp", riff(body))
	if err != nil {
		t.Fatal(err)
	}
	none(t, out, "WEBP_")
	if info.Width != 100 || info.Height != 50 {
		t.Errorf("dimensions %dx%d", info.Width, info.Height)
	}
	if out[12+8] != 0x20 {
		t.Errorf("VP8X flags %#x: EXIF and XMP must be cleared, ICC kept", out[12+8])
	}
	for _, k := range []string{"ICCP", "VP8L", "VP8X"} {
		if !bytes.Contains(out, []byte(k)) {
			t.Errorf("%s was dropped", k)
		}
	}
	// A canvas past the pixel cap is refused.
	huge := make([]byte, 10)
	huge[4], huge[5], huge[6] = 0xFF, 0xFF, 0x3F // 4,194,304
	huge[7], huge[8], huge[9] = 0xFF, 0xFF, 0x3F
	if _, _, err := Clean("webp", riff(webpChunk("VP8X", huge))); err == nil {
		t.Error("a 17-terapixel WebP was accepted")
	}
	// A frame of an animation is cleaned inside.
	frame := append(make([]byte, 16), webpChunk("VP8L", vp8l)...)
	frame = append(frame, webpChunk("XMP ", []byte("INNER_XMP_CANARY"))...)
	anim := append(webpChunk("VP8X", vp8x), webpChunk("ANMF", frame)...)
	out, _, err = Clean("webp", riff(anim))
	if err != nil {
		t.Fatal(err)
	}
	none(t, out, "INNER_XMP_CANARY")
	// Lossy and lossless headers give their size.
	vp8 := []byte{0, 0, 0, 0x9d, 0x01, 0x2a, 64, 0, 32, 0}
	_, info, err = Clean("webp", riff(webpChunk("VP8 ", vp8)))
	if err != nil || info.Width != 64 || info.Height != 32 {
		t.Errorf("VP8: %+v %v", info, err)
	}
	lossless := make([]byte, 5)
	lossless[0] = 0x2f
	binary.LittleEndian.PutUint32(lossless[1:], 15|7<<14)
	_, info, err = Clean("webp", riff(webpChunk("VP8L", lossless)))
	if err != nil || info.Width != 16 || info.Height != 8 {
		t.Errorf("VP8L: %+v %v", info, err)
	}
}

func seg(m byte, d []byte) []byte {
	b := []byte{0xFF, m}
	b = binary.BigEndian.AppendUint16(b, uint16(len(d)+2))
	return append(b, d...)
}

func exif(orientation int, extra string) []byte {
	var b bytes.Buffer
	b.WriteString("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08")
	_ = binary.Write(&b, binary.BigEndian, uint16(1))
	_ = binary.Write(&b, binary.BigEndian, uint16(0x0112))
	_ = binary.Write(&b, binary.BigEndian, uint16(3))
	_ = binary.Write(&b, binary.BigEndian, uint32(1))
	_ = binary.Write(&b, binary.BigEndian, uint16(orientation))
	b.Write([]byte{0, 0})
	_ = binary.Write(&b, binary.BigEndian, uint32(0))
	b.WriteString(extra)
	return b.Bytes()
}

// withEXIF puts an EXIF segment (and others) after the SOI of a real JPEG.
func withSegments(t *testing.T, j []byte, segs ...[]byte) []byte {
	t.Helper()
	out := append([]byte(nil), j[:2]...)
	for _, s := range segs {
		out = append(out, s...)
	}
	return append(out, j[2:]...)
}

func blocks(w, h int) image.Image {
	// left half red, right half blue, top rows brighter: a picture whose
	// orientation can be read back after a lossy encode.
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{220, 20, 20, 255}
			if x >= w/2 {
				c = color.RGBA{20, 20, 220, 255}
			}
			if y >= h/2 {
				c.G = 200
			}
			img.Set(x, y, c)
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestJPEGKeepsOnlyWhatDrawsIt(t *testing.T) {
	j := encodeJPEG(t, blocks(32, 16))
	jfif := append([]byte("JFIF\x00\x01\x01\x01\x00\x48\x00\x48\x02\x02"), []byte("THUMB_CANARY")...) // thumbnail 2x2
	icc := append([]byte("ICC_PROFILE\x00\x01\x01"), []byte("profile")...)
	dirty := withSegments(t, j,
		seg(0xE0, jfif),
		seg(0xE0, []byte("JFXX\x00APP0_CANARY")),
		seg(0xE1, exif(1, "EXIF_GPS_CANARY")),
		seg(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00XMP_CANARY")),
		seg(0xE2, icc),
		seg(0xE2, []byte("FPXR\x00FLASHPIX_CANARY")),
		seg(0xE2, []byte("ICC_PROFILE\x00\x00\x00BAD_ICC_CANARY")),
		seg(0xED, []byte("Photoshop 3.0\x00IPTC_CANARY")),
		seg(0xEE, []byte("Adobe\x00\x64\x00\x00\x00\x00\x01")),
		seg(0xEE, []byte("NotAdobe_CANARY")),
		seg(0xEF, []byte("APP15_CANARY")),
		seg(0xFE, []byte("COMMENT_CANARY")),
		seg(0xF5, []byte("JPG5_CANARY")),
	)
	dirty = append(dirty, []byte("TRAILER_CANARY")...)
	out, info, err := Clean("jpg", dirty)
	if err != nil {
		t.Fatal(err)
	}
	none(t, out, "APP0_CANARY", "EXIF_GPS_CANARY", "XMP_CANARY", "FLASHPIX_CANARY", "BAD_ICC_CANARY", "IPTC_CANARY", "NotAdobe_CANARY", "APP15_CANARY", "COMMENT_CANARY", "JPG5_CANARY", "TRAILER_CANARY", "Exif")
	if !bytes.Contains(out, []byte("profile")) || !bytes.Contains(out, []byte("Adobe")) {
		t.Error("a valid ICC profile or the Adobe marker was dropped")
	}
	k := bytes.Index(out, []byte("JFIF"))
	if k < 0 || out[k-2] != 0 || out[k-1] != 16 || out[k+12] != 0 || out[k+13] != 0 {
		t.Errorf("the JFIF header was not rebuilt without its thumbnail: % x", out[k-4:k+20])
	}
	none(t, out, "THUMB_CANARY")
	if info.Width != 32 || info.Height != 16 || info.Rotated {
		t.Errorf("info %+v", info)
	}
	if _, err := jpeg.Decode(bytes.NewReader(out)); err != nil {
		t.Errorf("the cleaned JPEG does not decode: %v", err)
	}
	if _, _, err := Clean("jpg", dirty[:40]); err == nil {
		t.Error("a truncated JPEG was accepted")
	}
}

func TestAnUprightJPEGIsNotReEncoded(t *testing.T) {
	j := encodeJPEG(t, blocks(32, 16))
	out, info, err := Clean("jpg", withSegments(t, j, seg(0xE1, exif(1, ""))))
	if err != nil || info.Rotated {
		t.Fatalf("%+v %v", info, err)
	}
	// Same scan data, byte for byte: nothing was decoded.
	if !bytes.Contains(out, j[len(j)/2:len(j)-2]) {
		t.Error("an upright photo was re-encoded")
	}
}

func TestOrientationIsAppliedBeforeTheEXIFGoes(t *testing.T) {
	src := blocks(32, 16) // wider than tall, red left, blue right, green on the bottom
	for _, o := range []int{2, 3, 4, 5, 6, 7, 8} {
		out, info, err := Clean("jpg", withSegments(t, encodeJPEG(t, src), seg(0xE1, exif(o, "GPS_CANARY_"+string(rune('0'+o))))))
		if err != nil {
			t.Fatalf("orientation %d: %v", o, err)
		}
		none(t, out, "GPS_CANARY", "Exif")
		wantW, wantH := 32, 16
		if o >= 5 {
			wantW, wantH = 16, 32
		}
		if !info.Rotated || info.Orientation != o || info.Width != wantW || info.Height != wantH {
			t.Errorf("orientation %d: %+v, want %dx%d rotated", o, info, wantW, wantH)
		}
		img, err := jpeg.Decode(bytes.NewReader(out))
		if err != nil || img.Bounds().Dx() != wantW || img.Bounds().Dy() != wantH {
			t.Fatalf("orientation %d: decoded %v %v", o, img.Bounds(), err)
		}
		// Where does the source's top-left (red, no green) land?
		corner := map[int][2]int{2: {wantW - 1, 0}, 3: {wantW - 1, wantH - 1}, 4: {0, wantH - 1}, 5: {0, 0}, 6: {wantW - 1, 0}, 7: {wantW - 1, wantH - 1}, 8: {0, wantH - 1}}[o]
		r, g, b, _ := img.At(corner[0], corner[1]).RGBA()
		if r>>8 < 150 || b>>8 > 90 || g>>8 > 90 {
			t.Errorf("orientation %d: the source's red top-left corner is not at %v: rgb %d %d %d", o, corner, r>>8, g>>8, b>>8)
		}
	}
	// A photo too large to turn is refused rather than published sideways.
	big := encodeJPEG(t, image.NewGray(image.Rect(0, 0, 6000, 5000)))
	if _, _, err := Clean("jpg", withSegments(t, big, seg(0xE1, exif(6, "")))); err == nil {
		t.Error("a 30-megapixel rotated photo was re-encoded")
	}
}

func TestGIFKeepsTheLoopNotTheComments(t *testing.T) {
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00" +
		"\x21\xFE\x05hello\x00" +
		"\x21\x01\x0cPLAINTEXT_CA\x00" +
		"\x21\xFF\x0bXMP DataXMP\x03abc\x00" +
		"\x21\xFF\x0bNETSCAPE2.0\x03\x01\x00\x00\x00" +
		"\x21\xF9\x04\x00\x0a\x00\x00\x00" +
		"\x2C\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02\x44\x01\x00" + "\x3B")
	out, info, err := Clean("gif", gif)
	if err != nil || info.Width != 1 || info.Height != 1 {
		t.Fatalf("%+v %v", info, err)
	}
	none(t, out, "hello", "PLAINTEXT_CA", "XMP Data")
	if !bytes.Contains(out, []byte("NETSCAPE2.0")) || !bytes.Contains(out, []byte{0x21, 0xF9}) {
		t.Error("the loop count or the frame timing was dropped")
	}
}

func TestSVGAndUnknownTypesAreRefused(t *testing.T) {
	for _, ext := range []string{"svg", "bmp", "", "tiff"} {
		if _, _, err := Clean(ext, []byte("<svg/>")); err == nil {
			t.Errorf("%q accepted", ext)
		}
	}
}
