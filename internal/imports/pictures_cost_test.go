package imports_test

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/testutil"
)

// rotatedJPEG is a JPEG whose EXIF says it is turned a quarter: cleaning it
// decodes, turns and writes it again. A blank one zips to almost nothing.
func rotatedJPEG(t testing.TB, w, h int) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, w, h)), &jpeg.Options{Quality: 50}); err != nil {
		t.Fatal(err)
	}
	j := b.Bytes()
	exif := []byte("Exif\x00\x00MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00")
	seg := []byte{0xff, 0xe1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}
	out := append([]byte{}, j[:2]...)
	out = append(out, seg...)
	out = append(out, exif...)
	return append(out, j[2:]...)
}

// The pictures of one import share a time budget: many large rotated JPEGs
// (cheap to send, each decoded and turned) do not hold a conversion for minutes.
func TestPictureCleaningIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	e := newEnv(t)
	img := rotatedJPEG(t, 4000, 4000)
	files := map[string][]byte{}
	var html strings.Builder
	const n = 60
	for i := 0; i < n; i++ {
		files[fmt.Sprintf("g/content/images/r/%d.jpg", i)] = append(append([]byte{}, img...), byte(i)) // distinct bytes
		fmt.Fprintf(&html, `<img src="__GHOST_URL__/content/images/r/%d.jpg">`, i)
	}
	files["g/export.json"] = []byte(`{"db":[{"meta":{"exported_on":1,"version":"5.0.0"},"data":{"posts":[{"id":"1","title":"R","slug":"r","status":"published","type":"post","html":` + jsonString(html.String()) + `}]}}]}`)
	z := zipFiles(t, files)
	t0 := time.Now()
	pv := e.previewConv(imports.PreviewIn{Name: "g", Zip: z})
	took := time.Since(t0)
	t.Logf("zip of %d bytes (one picture %d bytes): %d of %d pictures kept in %v", len(z), len(img), pv.Pictures, n, took)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 45*time.Second {
			t.Errorf("cleaning the pictures took %v", took)
		}
	})
}
