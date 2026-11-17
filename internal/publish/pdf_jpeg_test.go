package publish

import (
	"bytes"
	"encoding/binary"
	"image/jpeg"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/publish/pdfw"
)

// exifSegment is an APP1 segment with a TIFF header, an orientation tag and a
// string the picture must not carry into a book.
func exifSegment(orient int, secret string) []byte {
	var t bytes.Buffer
	t.WriteString("MM")
	binary.Write(&t, binary.BigEndian, uint16(42))
	binary.Write(&t, binary.BigEndian, uint32(8))
	binary.Write(&t, binary.BigEndian, uint16(2)) // two entries
	// orientation
	binary.Write(&t, binary.BigEndian, uint16(0x0112))
	binary.Write(&t, binary.BigEndian, uint16(3))
	binary.Write(&t, binary.BigEndian, uint32(1))
	binary.Write(&t, binary.BigEndian, uint16(orient))
	binary.Write(&t, binary.BigEndian, uint16(0))
	// an ASCII tag (ImageDescription) with the secret after the IFD
	binary.Write(&t, binary.BigEndian, uint16(0x010E))
	binary.Write(&t, binary.BigEndian, uint16(2))
	binary.Write(&t, binary.BigEndian, uint32(len(secret)+1))
	binary.Write(&t, binary.BigEndian, uint32(8+2+24+4))
	binary.Write(&t, binary.BigEndian, uint32(0))
	t.WriteString(secret + "\x00")
	body := append([]byte("Exif\x00\x00"), t.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(body)+2))
	return append(seg, body...)
}

func segment(marker byte, body string) []byte {
	seg := []byte{0xFF, marker, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(body)+2))
	return append(seg, body...)
}

// withMetadata puts metadata segments after the SOI and junk after the EOI.
func withMetadata(raw []byte, orient int) []byte {
	var out []byte
	out = append(out, raw[:2]...)
	out = append(out, exifSegment(orient, "GPSSECRETLOCATION 53.27N 9.05W")...)
	out = append(out, segment(0xED, "Photoshop 3.0\x00IPTCSECRETBYLINE")...)
	out = append(out, segment(0xFE, "SECRETCOMMENT written by the camera")...)
	out = append(out, segment(0xE1, "http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta>XMPSECRET</x:xmpmeta>")...)
	out = append(out, raw[2:]...)
	return append(out, []byte("TRAILINGJUNKAFTEREOI")...)
}

func TestJPEGMetadataIsStrippedAndOrientationHonoured(t *testing.T) {
	defer uncompressed(t)()
	base := jpegImage(300, 200) // landscape as stored
	data := withMetadata(base.Data, 6)
	if JPEGOrientation(data) != 6 || JPEGOrientation(base.Data) != 1 {
		t.Fatalf("orientation: %d %d", JPEGOrientation(data), JPEGOrientation(base.Data))
	}
	img := Image{Data: data, Mime: "image/jpeg", Ext: "jpg", Width: 300, Height: 200, Hash: "exif1"}
	b := bookFrom("Photo", "![p](/files/att_photo000001)")
	b.Images = map[string]Image{"/files/att_photo000001": img}
	out, warns, err := WritePDF(b, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warns {
		if w.Code == "image_not_embedded" {
			t.Fatalf("not embedded: %s", w.Message)
		}
	}
	for _, secret := range []string{"GPSSECRET", "IPTCSECRET", "SECRETCOMMENT", "XMPSECRET", "TRAILINGJUNK", "Exif", "xmpmeta"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("the PDF contains %q from the JPEG's metadata", secret)
		}
	}
	if probs := check.PDF(out); len(probs) > 0 {
		t.Fatal(probs)
	}
	// upright: the picture was saved landscape, turned a quarter, so it is
	// placed taller than wide, with the matrix of orientation 6
	l := mustLayout(t, b, print6x9())
	var ip *imgPlace
	for _, sh := range l.sheets {
		for _, p := range sh.items {
			if p.it.img != nil {
				ip = p.it.img
			}
		}
	}
	if ip == nil || ip.w >= ip.h {
		t.Fatalf("the picture is %v wide and %v tall: not upright", ip.w, ip.h)
	}
	m := regexp.MustCompile(`q (\S+) (\S+) (\S+) (\S+) (\S+) (\S+) cm /Im1 Do Q`).FindStringSubmatch(string(out))
	if m == nil {
		t.Fatal("no placed image in the content")
	}
	if m[1] != "0" || m[4] != "0" || !strings.HasPrefix(m[2], "-") || strings.HasPrefix(m[3], "-") {
		t.Errorf("the matrix is %v: not a quarter turn clockwise", m[1:])
	}
	// the other orientations are placed by their own matrices
	for o := 1; o <= 8; o++ {
		img.Data = withMetadata(base.Data, o)
		img.Hash = "exif" + string(rune('0'+o))
		b.Images["/files/att_photo000001"] = img
		out, _, err := WritePDF(b, print6x9())
		if err != nil {
			t.Fatal(err)
		}
		if probs := check.PDF(out); len(probs) > 0 {
			t.Errorf("orientation %d: %v", o, probs[0])
		}
	}
	// a JPEG with bytes after its EOI is accepted; one cut short is not
	trail := Image{Data: append(append([]byte(nil), base.Data...), []byte("junk junk junk")...), Mime: "image/jpeg", Ext: "jpg", Width: 300, Height: 200, Hash: "trail"}
	if _, why := newImageEmbedder(pdfw.New()).jpeg(trail); why != "" {
		t.Errorf("trailing bytes after the end marker refused: %s", why)
	}
	cut := Image{Data: base.Data[:len(base.Data)/2], Mime: "image/jpeg", Ext: "jpg", Width: 300, Height: 200, Hash: "cut"}
	if _, why := newImageEmbedder(pdfw.New()).jpeg(cut); why == "" {
		t.Error("a truncated JPEG was accepted")
	}
}

// The PDF copy of a JPEG is an allow-list: the segments decoding needs and nothing else. A comment that holds a
// copied EXIF block, a JPEGn segment (0xF0 to 0xFD), the reserved SOF-lookalike 0xC8, a reserved marker, an
// application segment of any number and an ICC profile (the PDF names DeviceRGB and does not use one) are all left out.
func TestPDFJPEGKeepsOnlyWhatDecodingNeeds(t *testing.T) {
	defer uncompressed(t)()
	base := jpegImage(300, 200)
	exifInComment := string(exifSegment(1, "COMMENTEXIFGPS 53.27N"))
	// Go's decoder reads a JFIF file's header only as far as the frame header, so what follows it (JPEGn segments,
	// a reserved marker) is not looked at by DecodeConfig, which is how such a file reaches the PDF writer at all.
	crafted := func(raw []byte) []byte {
		out := append([]byte(nil), raw[:2]...)
		out = append(out, segment(0xE0, "JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00JFIFTHUMBSECRET")...)
		out = append(out, segment(0xFE, exifInComment)...)
		out = append(out, segment(0xE5, "APP5SECRET")...)
		out = append(out, segment(0xE2, "ICC_PROFILE\x00\x01\x01ICCSECRETBYTES")...)
		sos := bytes.Index(raw, []byte{0xFF, 0xDA})
		out = append(out, raw[2:sos]...) // the tables and the frame header
		out = append(out, segment(0xF5, "JPG5SECRETPAYLOAD")...)
		out = append(out, segment(0xFD, "JPG13SECRETPAYLOAD")...)
		out = append(out, segment(0xF0, "JPG0SECRETPAYLOAD")...)
		out = append(out, segment(0xC8, "SOF-RESERVED-SECRET")...)
		out = append(out, segment(0x02, "RESERVEDMARKERSECRET")...)
		return append(out, raw[sos:]...)
	}
	data := crafted(base.Data)
	if _, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
		t.Fatalf("the crafted JPEG does not decode: %v", err)
	}
	img := Image{Data: data, Mime: "image/jpeg", Ext: "jpg", Width: 300, Height: 200, Hash: "allow1"}
	b := bookFrom("Photo", "![p](/files/att_photo000001)\n\nWords after.")
	b.Images = map[string]Image{"/files/att_photo000001": img}
	out, warns, err := WritePDF(b, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warns {
		if w.Code == "image_not_embedded" {
			t.Fatalf("not embedded: %s", w.Message)
		}
	}
	for _, secret := range []string{"COMMENTEXIFGPS", "JPG5SECRET", "JPG13SECRET", "JPG0SECRET", "SOF-RESERVED", "RESERVEDMARKER", "APP5SECRET", "ICCSECRET", "JFIFTHUMB", "Exif"} {
		if bytes.Contains(out, []byte(secret)) {
			t.Errorf("the PDF contains %q from a segment the picture does not need", secret)
		}
	}
	if probs := check.PDF(out); len(probs) > 0 {
		t.Fatal(probs)
	}
	// What is embedded is a whole, decodable picture of the same size, on the same number of pages as the clean one.
	m := regexp.MustCompile(`(?s)/Subtype /Image.*?stream\r?\n(.*?)\r?\nendstream`).FindSubmatch(out)
	if m == nil {
		t.Fatal("no image stream in the PDF")
	}
	got, err := jpeg.Decode(bytes.NewReader(m[1]))
	if err != nil {
		t.Fatalf("the embedded picture does not decode: %v", err)
	}
	if b := got.Bounds(); b.Dx() != 300 || b.Dy() != 200 {
		t.Errorf("the embedded picture is %dx%d, want 300x200", b.Dx(), b.Dy())
	}
	clean := bookFrom("Photo", "![p](/files/att_photo000001)\n\nWords after.")
	clean.Images = map[string]Image{"/files/att_photo000001": base}
	cleanOut, _, err := WritePDF(clean, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	pages := func(b []byte) int { return len(regexp.MustCompile(`/Type /Page\b`).FindAll(b, -1)) }
	if pages(out) != pages(cleanOut) || pages(out) == 0 {
		t.Errorf("%d pages with the crafted picture, %d with the clean one", pages(out), pages(cleanOut))
	}
}
