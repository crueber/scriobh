package publish

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/publish/pdfw"
	"github.com/crueber/scriobh/internal/testutil"
)

// An animated GIF is embedded from its first frame: its other frames are never
// decoded, so a 120-frame animation costs one frame of memory.
func TestAnimatedGIFDecodesOneFrame(t *testing.T) {
	const side, frames = 2000, 120
	pal := color.Palette{color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}}
	frame := image.NewPaletted(image.Rect(0, 0, side, side), pal)
	for i := 0; i < side*side; i += 997 {
		frame.Pix[i] = 1
	}
	g := &gif.GIF{}
	for i := 0; i < frames; i++ {
		g.Image = append(g.Image, frame) // one buffer, referenced 120 times: the file is small
		g.Delay = append(g.Delay, 1)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	e := newImageEmbedder(pdfw.New())
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	im, why := e.embed(Image{Data: buf.Bytes(), Ext: "gif", Mime: "image/gif", Width: side, Height: side, Hash: "anim"})
	runtime.ReadMemStats(&after)
	if im == nil {
		t.Fatal(why)
	}
	// every frame decoded would be 120 x 4 MB of pixels at least
	if alloc := (after.TotalAlloc - before.TotalAlloc) >> 20; alloc > 200 {
		t.Errorf("embedding the GIF allocated %d MB: more than one frame was decoded", alloc)
	}
}

func amplified() *Book {
	var chapters []string
	for i := 0; i < 3; i++ {
		var md strings.Builder
		for j := 0; j < 300; j++ {
			md.WriteString("ref[^a] ")
		}
		md.WriteString("\n\n[^a]: " + strings.Repeat("note text that is long ", 700) + "\n")
		chapters = append(chapters, md.String())
	}
	return bookFrom("Amplified", chapters...)
}

// A layout that had to leave text out is not written, and says why; the
// misleading "note not set" count is not reported for it.
func TestTruncatedLayoutIsRefused(t *testing.T) {
	l, err := LayoutPDF(amplified(), print6x9())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Truncated()) == 0 {
		t.Fatal("the layout does not say it was cut")
	}
	if _, _, err := l.Render(); !errors.Is(err, ErrTruncated) {
		t.Errorf("Render: %v", err)
	}
	var said, miscount bool
	for _, w := range l.Warnings {
		if w.Code == "layout_limit" && strings.Contains(w.Message, "refused") {
			said = true
		}
		if w.Code == "note_not_set" {
			miscount = true
		}
	}
	if !said || miscount {
		t.Errorf("warnings %+v", l.Warnings)
	}
	// a book that fits is not truncated
	if l2 := mustLayout(t, bookOf(3000), print6x9()); len(l2.Truncated()) != 0 {
		t.Errorf("%v", l2.Truncated())
	}
}

// A cancelled context stops the layout of a long book at once.
func TestLayoutStopsWhenCancelled(t *testing.T) {
	b := longBook(1_500_000)
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	_, _, err := WritePDFContext(ctx, b, print6x9())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
	d := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 3*time.Second*timingFactor {
			t.Errorf("the layout ran %v after it was cancelled", d)
		}
	})
	// already cancelled: nothing is built
	if _, err := LayoutPDFContext(ctx, bookOf(5000), print6x9()); !errors.Is(err, context.Canceled) {
		t.Errorf("%v", err)
	}
}
