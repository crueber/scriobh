package publish

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// writes a sheet of the eight orientations of a picture whose top left corner
// is red, for looking at (SCRIOBH_KEEP).
func TestOrientationSheet(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 240, 160))
	for y := 0; y < 160; y++ {
		for x := 0; x < 240; x++ {
			c := color.RGBA{230, 230, 230, 255}
			switch {
			case x < 40 && y < 40:
				c = color.RGBA{220, 0, 0, 255} // top left
			case x >= 200 && y < 40:
				c = color.RGBA{0, 0, 220, 255} // top right
			}
			m.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, m, &jpeg.Options{Quality: 90})
	b := bookFrom("Orientations")
	b.Images = map[string]Image{}
	var md string
	for o := 1; o <= 8; o++ {
		key := fmt.Sprintf("/files/att_orient%05d", o)
		b.Images[key] = Image{Data: withMetadata(buf.Bytes(), o), Mime: "image/jpeg", Ext: "jpg", Width: 240, Height: 160, Hash: fmt.Sprintf("o%d", o)}
		md += fmt.Sprintf("Orientation %d.\n\n![o](%s)\n\n", o, key)
	}
	b2 := bookFrom("Orientations", md)
	b2.Images = b.Images
	out, _, err := WritePDF(b2, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	keep(t, "orient.pdf", out)
}
