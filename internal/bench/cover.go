package bench

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The cover's derivative budget (docs/COVERS.md, "Image processing"): p95 under 1.5 s to make the front of a 6x9 wrap
// that is 300 dpi, 3825 by 2775 pixels (12.75 by 9.25 in with the bleed), which is the biggest cover a writer is
// likely to send for a paperback: decode about 10.6 million pixels, cut the front, scale it to 2560 on the long side
// and write a JPEG. Every run changes the layout, so each one is a derivative that was not kept.
const coverWrapW, coverWrapH = 3825, 2775

// wrapJPEG is a wrap the way a designer sends one: a flat back and front with texture, a spine of another colour.
func wrapJPEG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, coverWrapW, coverWrapH))
	for y := 0; y < coverWrapH; y++ {
		for x := 0; x < coverWrapW; x++ {
			c := color.RGBA{uint8(40 + (x*y)%97), uint8(60 + (x/7+y/5)%120), uint8(90 + y%61), 255}
			if x > 1838 && x < 1988 {
				c = color.RGBA{30, 30, 30, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// measureCover times the making of the front of a 300-dpi 6x9 wrap (cover.derive).
func (e *env) measureCover(n int, repeatable func(string, int, bool, func(int) error) error) error {
	if _, ok := e.srv.Reg.Lookup("project.cover_layout.set"); !ok {
		return nil
	}
	out, err := e.call("project.create", projects.CreateIn{Title: "Cover bench"})
	if err != nil {
		return err
	}
	pid := out.(projects.Item).Item.ID
	pic, err := wrapJPEG()
	if err != nil {
		return err
	}
	ctx := ops.WithRawBody(e.ctx, ops.RawBody{R: bytes.NewReader(pic), Length: int64(len(pic))})
	up, err := e.srv.Reg.Call(ctx, e.who, "project.cover_layout.upload", coverlayout.UploadIn{ID: pid, Part: "wrap"})
	if err != nil {
		return err
	}
	att := up.(coverlayout.UploadOut).AttachmentID
	rev := 0
	return repeatable("cover.derive", max(3, n/10), false, func(i int) error {
		v, err := e.call("project.cover_layout.set", coverlayout.SetIn{ID: pid, Revision: rev, Shape: "wrap", Wrap: &coverlayout.Wrap{Attachment: att},
			Alt: fmt.Sprintf("A wrap, run %d", i)})
		if err != nil {
			return err
		}
		rev = v.(coverlayout.View).Revision
		f, err := e.call("project.cover", cover.In{ID: pid})
		if err != nil {
			return err
		}
		if f.(cover.File).ContentType != "image/jpeg" {
			return fmt.Errorf("the cover is %s, not a derivative", f.(cover.File).ContentType)
		}
		return nil
	})
}
