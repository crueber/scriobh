package pwa

import (
	"bytes"
	"image/color"
	"image/png"
	"testing"
)

func decode(t *testing.T, b []byte) (w int, at func(x, y int) color.NRGBA) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img.Bounds().Dx(), func(x, y int) color.NRGBA { return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA) }
}

func TestIconShape(t *testing.T) {
	for _, size := range []int{180, 192, 512} {
		w, at := decode(t, Icon(size, false))
		if w != size {
			t.Fatalf("width %d, want %d", w, size)
		}
		if c := at(0, 0); c.A != 0 {
			t.Errorf("%d: the corner of a rounded icon is not transparent: %v", size, c)
		}
		if c := at(size/2, size*5/32); c != moss { // between the strokes and the edge: flat moss
			t.Errorf("%d: background %v, want %v", size, c, moss)
		}
		if c := at(size/2, size*16/32); c != (color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}) { // the middle stroke
			t.Errorf("%d: stroke %v, want white", size, c)
		}
		// Flat: only moss, white, their blend at an edge, and transparency at the corners.
		_, at2 := decode(t, Icon(size, true))
		if c := at2(0, 0); c != moss {
			t.Errorf("%d: a maskable icon is not full bleed: %v", size, c)
		}
	}
}

func TestIconIsDeterministic(t *testing.T) {
	if !bytes.Equal(Icon(192, false), Icon(192, false)) || !bytes.Equal(Icon(192, true), Icon(192, true)) {
		t.Error("the same icon drew twice differently")
	}
	if bytes.Equal(Icon(192, false), Icon(192, true)) {
		t.Error("maskable and any are the same picture")
	}
	if len(Icon(512, false)) > 20<<10 {
		t.Errorf("the 512 icon is %d bytes", len(Icon(512, false)))
	}
}
