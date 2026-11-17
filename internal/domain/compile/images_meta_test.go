package compile_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// phoneJPEG is a small photograph the way a phone saves it: an EXIF block with the camera's orientation and a GPS
// string, a comment and a trailing blob.
func phoneJPEG(t *testing.T) []byte {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, 24, 16))
	for x := 0; x < 24; x++ {
		for y := 0; y < 16; y++ {
			im.Set(x, y, color.RGBA{uint8(x * 10), uint8(y * 15), 90, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, im, nil); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a")
	binary.Write(&tiff, binary.BigEndian, uint32(8))
	binary.Write(&tiff, binary.BigEndian, uint16(2))
	// orientation 6 (turn a quarter), then a string tag that holds the canary
	binary.Write(&tiff, binary.BigEndian, uint16(0x0112))
	binary.Write(&tiff, binary.BigEndian, uint16(3))
	binary.Write(&tiff, binary.BigEndian, uint32(1))
	binary.Write(&tiff, binary.BigEndian, uint16(6))
	binary.Write(&tiff, binary.BigEndian, uint16(0))
	secret := "GPSCANARYLOCATION 53.27N 9.05W"
	binary.Write(&tiff, binary.BigEndian, uint16(0x010e))
	binary.Write(&tiff, binary.BigEndian, uint16(2))
	binary.Write(&tiff, binary.BigEndian, uint32(len(secret)+1))
	binary.Write(&tiff, binary.BigEndian, uint32(8+2+24+4))
	binary.Write(&tiff, binary.BigEndian, uint32(0))
	tiff.WriteString(secret + "\x00")
	body := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(body)+2))
	seg = append(seg, body...)
	com := []byte{0xFF, 0xFE, 0, 0}
	c := "COMMENTCANARY from the camera"
	binary.BigEndian.PutUint16(com[2:], uint16(len(c)+2))
	com = append(com, c...)
	out := append([]byte{}, raw[:2]...)
	out = append(out, seg...)
	out = append(out, com...)
	out = append(out, raw[2:]...)
	return append(out, []byte("TRAILINGCANARY")...)
}

// A picture with a location in it is stored as it came, and every file made from it (the DOCX, the EPUB and the
// print PDF) carries the picture without the location, the comment or anything after its end.
func TestNoCompileOutputCarriesAPhotographsMetadata(t *testing.T) {
	f := newFx(t, "Photographs")
	b := phoneJPEG(t)
	ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(b), Length: int64(len(b))})
	out, err := f.reg.Call(ctx, f.owner, "project.attachment.upload", attachments.UploadIn{ID: f.pid, Filename: "shore.jpg", Alt: "The shore"})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(attachments.Item).Item.ID
	f.doc("One", "chapter", "", "On the shore. ![The shore](/files/"+id+")\n")
	for _, preset := range []string{"shunn-us", "epub3", "print-6x9"} {
		res := f.compile(preset, publish.Options{})
		all := string(res.Body)
		if preset != "print-6x9" {
			all = ""
			for _, p := range unzip(t, res.Body) {
				all += p
			}
		}
		if !strings.Contains(all, "\xff\xd8") && preset != "print-6x9" {
			t.Errorf("%s: the picture is not in the file", preset)
		}
		if preset == "print-6x9" && !strings.Contains(all, "/DCTDecode") {
			t.Errorf("the PDF holds no picture")
		}
		for _, canary := range []string{"GPSCANARY", "COMMENTCANARY", "TRAILINGCANARY", "Exif"} {
			if strings.Contains(all, canary) {
				t.Errorf("%s: the output holds %q from the photograph", preset, canary)
			}
		}
	}
}
