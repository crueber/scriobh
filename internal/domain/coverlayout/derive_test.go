package coverlayout_test

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

func (e *env) cover(in cover.In) cover.File {
	e.t.Helper()
	in.ID = e.pid
	return e.call("project.cover", in).(cover.File)
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func near(a, b color.Color, tol int) bool {
	ar, ag, ab, _ := a.RGBA()
	br, bg, bb, _ := b.RGBA()
	d := func(x, y uint32) int {
		v := int(x>>8) - int(y>>8)
		if v < 0 {
			v = -v
		}
		return v
	}
	return d(ar, br) <= tol && d(ag, bg) <= tol && d(ab, bb) <= tol
}

func (e *env) coversDir() string {
	d, _ := attachments.CoversDir(e.dir, e.pid)
	return d
}

func (e *env) kept() []string {
	ents, _ := os.ReadDir(e.coversDir())
	var out []string
	for _, x := range ents {
		out = append(out, x.Name())
	}
	return out
}

// An image side is filled and cropped from the centre, the front is at most 2560 px on the long side, the ETag follows
// the layout, and what is kept is swept when the layout changes.
func TestFrontFromAnImageSideIsCroppedKeptAndSwept(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(1600, 2560, color.RGBA{R: 200})) // 1:1.6, a little taller than a 6x9 page
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A door in the snow")
	in.ID = e.pid
	v := e.call("project.cover_layout.set", in).(coverlayout.View)
	var crop *coverlayout.Rect
	for _, p := range v.Pictures {
		crop = p.Crop
	}
	if crop == nil || *crop != (coverlayout.Rect{X0: 0, Y0: 80, X1: 1600, Y1: 2480}) {
		t.Errorf("the crop is shown before it is saved: %+v", v.Pictures)
	}
	if v.FrontURL == "" || v.ThumbURL == "" || v.BackURL != "" {
		t.Errorf("urls %q %q %q", v.FrontURL, v.ThumbURL, v.BackURL)
	}
	f := e.cover(cover.In{})
	if f.ContentType != "image/jpeg" || !strings.HasSuffix(f.Filename, "-cover.jpg") || f.ETag == "" {
		t.Fatalf("%s %s %q", f.ContentType, f.Filename, f.ETag)
	}
	img := decode(t, f.Body)
	if b := img.Bounds(); b.Dx() != 1600 || b.Dy() != 2400 {
		t.Errorf("the front is %v, want 1600x2400", b)
	}
	if again := e.cover(cover.In{}); again.ETag != f.ETag || !bytes.Equal(again.Body, f.Body) {
		t.Error("the same layout gives the same bytes and the same ETag")
	}
	// the front and the thumbnail were made when the layout was saved, so the first look does not wait for them
	oldKept := e.kept()
	if len(oldKept) != 2 || !strings.HasSuffix(oldKept[0], "-front.jpg") || !strings.HasSuffix(oldKept[1], "-thumb.jpg") {
		t.Errorf("kept %v", oldKept)
	}
	th := e.cover(cover.In{Size: "thumb"})
	if b := decode(t, th.Body).Bounds(); b.Dx() != 480 || b.Dy() != 720 || th.ETag == f.ETag {
		t.Errorf("the thumb is %v (%q, front %q)", b, th.ETag, f.ETag)
	}
	if len(e.kept()) != 2 {
		t.Errorf("kept %v", e.kept())
	}
	// ?format=svg is still the generated cover
	if svg := e.cover(cover.In{Format: "svg"}); svg.ContentType != "image/svg+xml" || !strings.HasPrefix(string(svg.Body), "<svg ") {
		t.Errorf("format=svg gave %s", svg.ContentType)
	}
	// no back picture: the back is a 404
	if _, err := e.callAs(e.owner, "project.cover", cover.In{ID: e.pid, Part: "back"}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("back: %v", err)
	}
	// changing the layout changes the ETag and sweeps what was kept
	in.Revision, in.Alt = 1, "A door, in snow"
	e.call("project.cover_layout.set", in)
	for _, k := range e.kept() {
		for _, old := range oldKept {
			if k == old {
				t.Errorf("a changed layout leaves %v", k)
			}
		}
	}
	if len(e.kept()) != 2 {
		t.Errorf("the changed layout has %v kept", e.kept())
	}
	if g := e.cover(cover.In{}); g.ETag == f.ETag {
		t.Error("the ETag did not follow the layout")
	}
	// a picture that is larger than 2560 is scaled down, never up
	big := e.upload("front", jpegOf(3000, 4800, color.RGBA{R: 9}))
	in2 := sides(coverlayout.Side{Mode: "image", Attachment: big.AttachmentID}, coverlayout.Side{}, "Bigger")
	in2.ID, in2.Revision = e.pid, 2
	e.call("project.cover_layout.set", in2)
	if b := decode(t, e.cover(cover.In{}).Body).Bounds(); b.Dx() != 1707 || b.Dy() != 2560 {
		t.Errorf("3000x4800 is cut to 3000x4500 then scaled to 1707x2560; got %v", b)
	}
}

func TestPageSideHasTheTrimsProportionsAndTheBackground(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", pngOf(400, 400, color.RGBA{R: 250, G: 250, B: 250, A: 255}))
	side := coverlayout.Side{Mode: "page", Attachment: up.AttachmentID, Fit: "contain", Position: "center", Scale: 100, MarginIn: 0.5}
	in := sides(side, coverlayout.Side{}, "A white square on a brown page")
	in.ID = e.pid
	e.call("project.cover_layout.set", in)
	img := decode(t, e.cover(cover.In{}).Body)
	if b := img.Bounds(); b.Dx() != 1707 || b.Dy() != 2560 {
		t.Fatalf("a 6x9 page is 1707x2560 here, got %v", b)
	}
	brown := color.RGBA{122, 75, 42, 255} // the project's cover colour, the default background
	if !near(img.At(5, 5), brown, 6) || !near(img.At(1700, 2550), brown, 6) || !near(img.At(100, 1280), brown, 6) {
		t.Errorf("the margin is not the background: %v %v", img.At(5, 5), img.At(100, 1280))
	}
	if !near(img.At(853, 1280), color.RGBA{250, 250, 250, 255}, 6) {
		t.Errorf("the middle is not the picture: %v", img.At(853, 1280))
	}
	// the 400 px picture is not enlarged to fill the margin's box: it sits at its own size in the middle, x 654 to 1054
	if !near(img.At(660, 1280), color.RGBA{250, 250, 250, 255}, 8) || !near(img.At(645, 1280), brown, 8) || !near(img.At(1060, 1280), brown, 8) {
		t.Errorf("the picture's edge: %v %v %v", img.At(645, 1280), img.At(660, 1280), img.At(1060, 1280))
	}
	v := e.get()
	small := false
	for _, w := range v.Warnings {
		small = small || w.Code == "cover_picture_small"
	}
	if !small {
		t.Errorf("a picture that was not enlarged is not warned about: %+v", v.Warnings)
	}
}

// colouredWrap is a lossless wrap with the back, the spine and the front each a flat colour, a bleed strip round the
// outside, made at 150 dpi: 6x9 with a 0.5 in spine and no bleed is 1875 by 1350 pixels.
func colouredWrap(w, h, backEnd, frontStart int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{0, 160, 0, 255} // right panel
			switch {
			case x < backEnd:
				c = color.RGBA{200, 0, 0, 255} // left panel
			case x < frontStart:
				c = color.RGBA{0, 0, 200, 255} // spine
			}
			img.SetRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func TestWrapFrontIsCutAtTheTrimAndFollowsTheDirection(t *testing.T) {
	for _, c := range []struct {
		lang              string
		wantFront, wantBk color.RGBA
		onRight           string
	}{
		{"en", color.RGBA{0, 160, 0, 255}, color.RGBA{200, 0, 0, 255}, "right"},
		{"ar", color.RGBA{200, 0, 0, 255}, color.RGBA{0, 160, 0, 255}, "left"},
	} {
		t.Run(c.lang, func(t *testing.T) {
			e := newEnv(t)
			e.call("project.update", projects.UpdateIn{ID: e.pid, Language: &c.lang})
			// left panel [0,900), spine [900,975), right panel [975,1875): 6 in at 150 dpi is 900 px, the spine 75
			raw := colouredWrap(1875, 1350, 900, 975)
			up, err := e.uploadRaw(e.owner, e.pid, "wrap", map[string]string{"trim": "6x9", "bleed_in": "0"}, raw)
			if err != nil {
				t.Fatal(err)
			}
			zero := 0.0
			if up.Measured == nil || up.Measured.DPI != 150 || up.Measured.SpineIn != 0.5 || up.DPI != 150 {
				t.Errorf("measured at upload: %+v", up.Measured)
			}
			in := coverlayout.SetIn{ID: e.pid, Shape: "wrap", Wrap: &coverlayout.Wrap{Attachment: up.AttachmentID, BleedIn: &zero}, Alt: "A wrap in three colours"}
			v := e.call("project.cover_layout.set", in).(coverlayout.View)
			if v.Measured == nil || v.Measured.SpineIn != 0.5 || v.Measured.DPI != 150 || v.Measured.FrontOn != c.onRight {
				t.Fatalf("measured %+v", v.Measured)
			}
			f := decode(t, e.cover(cover.In{}).Body)
			if b := f.Bounds(); b.Dx() != 900 || b.Dy() != 1350 {
				t.Fatalf("the front is %v, a 6x9 page at 150 dpi is 900x1350", b)
			}
			for _, p := range []image.Point{{2, 2}, {450, 675}, {897, 1347}, {2, 1347}, {897, 2}} {
				if !near(f.At(p.X, p.Y), c.wantFront, 4) {
					t.Errorf("front at %v is %v, want %v (no spine and no back in it)", p, f.At(p.X, p.Y), c.wantFront)
				}
			}
			// the same cut by hand: the front's pixels are the picture's, exactly, up to the JPEG's own loss
			if th := e.cover(cover.In{Size: "thumb"}); decode(t, th.Body).Bounds().Dx() != 480 {
				t.Error("the thumb of a wrap is cut from the front")
			}
		})
	}
}

// The cut of the front is the picture's own pixels: no filtering when nothing is scaled.
func TestACutThatNeedsNoScalingIsExact(t *testing.T) {
	g, err := coverlayout.MeasureWrap(1875, 1350, mustTrim(t), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	src, err := coverlayout.Decode(colouredWrap(1875, 1350, 900, 975))
	if err != nil {
		t.Fatal(err)
	}
	out := coverlayout.RenderCrop(src, g.Front, coverlayout.MaxDerivedSide)
	for y := 0; y < g.Front.Dy(); y += 7 {
		for x := 0; x < g.Front.Dx(); x += 7 {
			if out.At(x, y) != src.Img.At(g.Front.Min.X+x, g.Front.Min.Y+y) {
				t.Fatalf("pixel %d,%d differs", x, y)
			}
		}
	}
}

func mustTrim(t *testing.T) coverlayout.TrimIn {
	tr, ok := coverlayout.LookupTrim("6x9")
	if !ok {
		t.Fatal("no 6x9")
	}
	return tr
}

func TestWrapThatIsNotAWrapIsRefusedAtUpload(t *testing.T) {
	e := newEnv(t)
	raw := colouredWrap(1875, 1350, 900, 975)
	zero := map[string]string{"bleed_in": "0"}
	for _, c := range []struct {
		name string
		opt  map[string]string
		body []byte
		code string
	}{
		{"made for a4, which is far taller: under 150 dpi", map[string]string{"trim": "a4", "bleed_in": "0"}, raw, "cover_wrap_low_dpi"},
		{"at the default bleed it is 146 dpi", nil, raw, "cover_wrap_low_dpi"},
		{"narrower than a back and a front", zero, colouredWrap(1700, 1350, 850, 850), "cover_wrap_spine_negative"},
		{"wider than any spine", zero, colouredWrap(1875+400, 1350, 900, 975), "cover_wrap_spine_wide"},
	} {
		_, err := e.uploadRaw(e.owner, e.pid, "wrap", c.opt, c.body)
		ae := ops.AsError(err)
		if err == nil || ae.Code != ops.CodeInvalid || ae.Data["reason"] != c.code {
			t.Errorf("%s: %v (%v)", c.name, err, ae.Data)
		}
	}
	var n int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM attachments WHERE project_id = ?`, e.pid).Scan(&n)
	if n != 0 {
		t.Errorf("%d attachments for refused wraps", n)
	}
}

func TestCoverLayoutSurvivesNothingOutsideTheDatabaseButTheFiles(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(600, 900, color.RGBA{R: 3}))
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A cover")
	in.ID = e.pid
	e.call("project.cover_layout.set", in)
	_ = e.cover(cover.In{})
	if len(e.kept()) == 0 {
		t.Fatal("nothing kept")
	}
	// the kept pictures are rebuilt on demand: remove them all and ask again
	if err := os.RemoveAll(e.coversDir()); err != nil {
		t.Fatal(err)
	}
	if f := e.cover(cover.In{}); f.ContentType != "image/jpeg" || len(e.kept()) != 1 {
		t.Errorf("not rebuilt: %s %v", f.ContentType, e.kept())
	}
	// a damaged kept file is not served: it is made again
	p := filepath.Join(e.coversDir(), e.kept()[0])
	if err := os.WriteFile(p, []byte("not a jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f := e.cover(cover.In{}); !bytes.HasPrefix(f.Body, []byte{0xFF, 0xD8}) {
		t.Error("a damaged derivative was served")
	}
}

// withProfile writes a colour profile into a JPEG as the one APP2 segment a profile of this size takes.
func withProfile(jpg []byte, desc string) []byte {
	return withICCSegments(jpg, profile(desc, "RGB ", true, 0))
}

// profile is a profile's bytes: a header with its colour space at byte 16 and, if sig, "acsp" at byte 36, then the
// description, padded to size when that is more.
func profile(desc, space string, sig bool, size int) []byte {
	return profileOf(desc, space, "mntr", sig, size, true)
}

// profileOf is profile with a class (bytes 12 to 15) and, when stated is false, a size field that is not the length.
func profileOf(desc, space, class string, sig bool, size int, stated bool) []byte {
	prof := make([]byte, 128)
	copy(prof[12:], class)
	copy(prof[16:], space)
	if sig {
		copy(prof[36:], "acsp")
	}
	prof = append(prof, []byte(desc)...)
	for len(prof) < size {
		prof = append(prof, 0)
	}
	n := uint32(len(prof))
	if !stated {
		n += 7
	}
	binary.BigEndian.PutUint32(prof, n)
	return prof
}

// withICCSegments writes a profile after the start of a JPEG in as many APP2 segments as it takes.
func withICCSegments(jpg, prof []byte) []byte {
	const piece = 60000
	n := (len(prof) + piece - 1) / piece
	out := []byte{0xFF, 0xD8}
	for i := 0; i < n; i++ {
		chunk := prof[i*piece : min((i+1)*piece, len(prof))]
		seg := append([]byte("ICC_PROFILE\x00"), byte(i+1), byte(n))
		seg = append(seg, chunk...)
		out = append(out, 0xFF, 0xE2, byte((len(seg)+2)>>8), byte(len(seg)+2))
		out = append(out, seg...)
	}
	return append(out, jpg[2:]...)
}

// A picture's colour profile goes with its derivatives, so the pixels and what they mean stay together, and the upload says
// when the profile is not sRGB.
func TestTheColourProfileGoesWithTheDerivatives(t *testing.T) {
	e := newEnv(t)
	plain := e.upload("front", jpegOf(600, 900, color.RGBA{R: 2}))
	for _, w := range plain.Warnings {
		if w.Code == "cover_profile_not_srgb" {
			t.Error("a picture with no profile is warned about")
		}
	}
	srgb := e.upload("front", withProfile(jpegOf(600, 900, color.RGBA{R: 3}), "sRGB IEC61966-2.1"))
	for _, w := range srgb.Warnings {
		if w.Code == "cover_profile_not_srgb" {
			t.Error("an sRGB profile is warned about")
		}
	}
	up := e.upload("front", withProfile(jpegOf(600, 900, color.RGBA{R: 4}), "Display P3"))
	warned := false
	for _, w := range up.Warnings {
		warned = warned || w.Code == "cover_profile_not_srgb" && strings.Contains(w.Message, "ignores profiles")
	}
	if !warned {
		t.Errorf("a Display P3 picture is not warned about: %+v", up.Warnings)
	}
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A cover")
	in.ID = e.pid
	e.call("project.cover_layout.set", in)
	for _, c := range []cover.In{{}, {Size: "thumb"}} {
		f := e.cover(c)
		if !bytes.Contains(f.Body, []byte("ICC_PROFILE\x00")) || !bytes.Contains(f.Body, []byte("Display P3")) {
			t.Errorf("%+v: the derivative has lost the profile", c)
		}
		if _, err := jpeg.Decode(bytes.NewReader(f.Body)); err != nil {
			t.Errorf("%+v: a derivative with a profile does not decode: %v", c, err)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A profile that is not one a derivative may carry (over 1 MB, no signature, not RGB) is left out, not cut down, and the upload
// says so; a front, a thumbnail and an EPUB never carry a huge one.
func TestAProfileThatMayNotBeCarriedIsLeftOut(t *testing.T) {
	e := newEnv(t)
	for name, prof := range map[string][]byte{
		"over 1 MB":  profile("sRGB huge", "RGB ", true, coverlayout.MaxProfileBytes+1),
		"no acsp":    profile("Display P3", "RGB ", false, 0),
		"a grey one": profile("Gray Gamma 2.2", "GRAY", true, 0),
		"an abstract profile (not a colour space a picture may name)": profileOf("Abstract", "RGB ", "abst", true, 0, true),
		"a size field that is not the length":                         profileOf("Display P3", "RGB ", "mntr", true, 0, false),
	} {
		base := jpegOf(600, 900, color.RGBA{R: uint8(len(name))})
		up := e.upload("front", withICCSegments(base, prof))
		dropped := false
		for _, w := range up.Warnings {
			dropped = dropped || w.Code == "cover_profile_dropped"
		}
		if !dropped {
			t.Errorf("%s: no warning that the profile is left out: %+v", name, up.Warnings)
		}
		in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A cover")
		in.ID = e.pid
		cur := e.get()
		if cur != nil {
			in.Revision = cur.Revision
		}
		e.call("project.cover_layout.set", in)
		for _, c := range []cover.In{{}, {Size: "thumb"}} {
			if f := e.cover(c); bytes.Contains(f.Body, []byte("ICC_PROFILE")) {
				t.Errorf("%s: a derivative (%+v) carries the profile, %d bytes", name, c, len(f.Body))
			}
		}
	}
	// a PNG whose profile inflates to more than the limit is dropped too, never truncated
	big := withPNGProfile(t, pngOf(60, 90, color.RGBA{A: 255}), profile("big", "RGB ", true, coverlayout.MaxProfileBytes+10))
	if up, err := e.uploadRaw(e.owner, e.pid, "front", nil, big); err != nil {
		t.Fatal(err)
	} else {
		dropped := false
		for _, w := range up.Warnings {
			dropped = dropped || w.Code == "cover_profile_dropped"
		}
		if !dropped {
			t.Errorf("a PNG with a huge profile: %+v", up.Warnings)
		}
	}
}

// withPNGProfile puts an iCCP chunk before the first IDAT.
func withPNGProfile(t *testing.T, p []byte, prof []byte) []byte {
	t.Helper()
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(prof)
	_ = zw.Close()
	data := append([]byte("icc\x00\x00"), z.Bytes()...)
	chunk := make([]byte, 0, len(data)+12)
	chunk = binary.BigEndian.AppendUint32(chunk, uint32(len(data)))
	chunk = append(chunk, "iCCP"...)
	chunk = append(chunk, data...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	i := bytes.Index(p, []byte("IDAT")) - 4
	return append(append(append([]byte{}, p[:i]...), chunk...), p[i:]...)
}
