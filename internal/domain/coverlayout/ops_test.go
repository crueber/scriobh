package coverlayout_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type env struct {
	t     *testing.T
	d     *db.DB
	reg   *ops.Registry
	dir   string
	owner ops.Principal
	pid   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testutil.DB(t)
	dir := t.TempDir()
	e := &env{t: t, d: d, dir: dir, reg: registry.BuildWith(d, events.NewStore(d, nil), dir, registry.Options{Attachments: attachments.DefaultLimits})}
	e.owner = ops.Principal{UserID: testutil.User(t, d, ops.RoleWriter, "o@t.local"), Role: ops.RoleWriter}
	e.pid = e.call("project.create", projects.CreateIn{Title: "Covers", CoverColor: "#7A4B2A"}).(projects.Item).Item.ID
	return e
}

func (e *env) callAs(who ops.Principal, op string, in any) (any, error) {
	return e.reg.Call(context.Background(), who, op, in)
}

func (e *env) call(op string, in any) any {
	e.t.Helper()
	out, err := e.callAs(e.owner, op, in)
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (e *env) fails(op string, in any, code ops.Code) *ops.Error {
	e.t.Helper()
	_, err := e.callAs(e.owner, op, in)
	if err == nil || ops.AsError(err).Code != code {
		e.t.Fatalf("%s: %v, want %s", op, err, code)
	}
	return ops.AsError(err)
}

// uploadRaw uploads a picture; opt may carry trim and bleed_in.
func (e *env) uploadRaw(who ops.Principal, pid, part string, opt map[string]string, body []byte) (coverlayout.UploadOut, error) {
	ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(body), Length: int64(len(body))})
	in := coverlayout.UploadIn{ID: pid, Part: part, Trim: opt["trim"], BleedIn: opt["bleed_in"]}
	out, err := e.reg.Call(ctx, who, "project.cover_layout.upload", in)
	if err != nil {
		return coverlayout.UploadOut{}, err
	}
	return out.(coverlayout.UploadOut), nil
}

func (e *env) upload(part string, body []byte) coverlayout.UploadOut {
	e.t.Helper()
	out, err := e.uploadRaw(e.owner, e.pid, part, nil, body)
	if err != nil {
		e.t.Fatalf("upload %s: %v", part, err)
	}
	return out
}

func (e *env) get() *coverlayout.View {
	return e.call("project.cover_layout.get", coverlayout.ProjectIn{ID: e.pid}).(coverlayout.Item).Layout
}

func (e *env) events(typ string) int {
	var n int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM events WHERE project_id = ? AND type = ?`, e.pid, typ).Scan(&n)
	return n
}

func (e *env) coverRefs() int {
	var n int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM attachment_refs WHERE ref_kind = 'cover' AND ref_id = ?`, e.pid).Scan(&n)
	return n
}

// ---- pictures -----------------------------------------------------------------------------------

func jpegOf(w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{c.R, uint8(y * 255 / h), uint8(x * 255 / w), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 92}); err != nil {
		panic(err)
	}
	return b.Bytes()
}

func pngOf(w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// withEXIF puts an APP1 segment with a recognisable GPS block right after the JPEG's start marker.
func withEXIF(jpg []byte, sentinel string) []byte {
	var tiff bytes.Buffer
	tiff.WriteString("II*\x00\x08\x00\x00\x00")
	tiff.Write([]byte{1, 0}) // one entry: the GPS pointer
	var ent [12]byte
	binary.LittleEndian.PutUint16(ent[0:], 0x8825)
	binary.LittleEndian.PutUint16(ent[2:], 4)
	binary.LittleEndian.PutUint32(ent[4:], 1)
	binary.LittleEndian.PutUint32(ent[8:], 26)
	tiff.Write(ent[:])
	tiff.Write([]byte{0, 0, 0, 0})
	tiff.WriteString(sentinel + " GPSLatitude 51.5N GPSLongitude 0.12W")
	seg := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// jpegHeader is the start of a JPEG that says it has w by h pixels and n components: enough for the size and the colour
// model to be read, which is all the caps and the CMYK check read.
func jpegHeader(w, h, n int) []byte {
	out := []byte{0xFF, 0xD8}
	if n == 4 {
		out = append(out, 0xFF, 0xEE, 0, 14, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, 0)
	}
	sof := []byte{8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(n)}
	for i := 0; i < n; i++ {
		sof = append(sof, byte(i+1), 0x11, 0)
	}
	out = append(out, 0xFF, 0xC0, byte((len(sof)+2)>>8), byte(len(sof)+2))
	out = append(out, sof...)
	sos := []byte{byte(n)}
	for i := 0; i < n; i++ {
		sos = append(sos, byte(i+1), 0)
	}
	sos = append(sos, 0, 63, 0)
	out = append(out, 0xFF, 0xDA, byte((len(sos)+2)>>8), byte(len(sos)+2))
	return append(out, sos...)
}

// pngHeader is a PNG that says it has w by h pixels (an IHDR and nothing else).
func pngHeader(w, h int) []byte {
	out := []byte("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = 8, 6
	chunk := append([]byte("IHDR"), ihdr...)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], 13)
	out = append(out, l[:]...)
	out = append(out, chunk...)
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], crc32.ChecksumIEEE(chunk))
	return append(out, c[:]...)
}

func (e *env) stored(attID string) []byte {
	e.t.Helper()
	var sha, ext string
	if err := e.d.Read().QueryRow(`SELECT sha256, ext FROM attachments WHERE id = ?`, attID).Scan(&sha, &ext); err != nil {
		e.t.Fatal(err)
	}
	p, err := attachments.BlobPath(e.dir, attachments.OwnerProject, e.pid, sha, ext)
	if err != nil {
		e.t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// ---- tests -------------------------------------------------------------------------------------

func TestUploadCleansAPictureOfItsEXIFAndGPS(t *testing.T) {
	e := newEnv(t)
	raw := withEXIF(jpegOf(600, 900, color.RGBA{R: 40}), "SCRIOBH-GPS-SENTINEL")
	if !bytes.Contains(raw, []byte("SCRIOBH-GPS-SENTINEL")) || !bytes.Contains(raw, []byte("Exif")) {
		t.Fatal("the fixture has no EXIF to remove")
	}
	if _, err := jpeg.Decode(bytes.NewReader(raw)); err != nil {
		t.Fatalf("the fixture is not a JPEG: %v", err)
	}
	out := e.upload("front", raw)
	b := e.stored(out.AttachmentID)
	for _, bad := range []string{"SCRIOBH-GPS-SENTINEL", "Exif", "GPSLatitude", "GPSLongitude"} {
		if bytes.Contains(b, []byte(bad)) {
			t.Errorf("the stored picture still holds %q", bad)
		}
	}
	if _, err := jpeg.Decode(bytes.NewReader(b)); err != nil {
		t.Errorf("the cleaned picture is not a JPEG: %v", err)
	}
	if out.Width != 600 || out.Height != 900 || out.Mime != "image/jpeg" || out.Trim != "6x9" || out.DPI != 100 {
		t.Errorf("%+v", out)
	}
	if len(out.Warnings) == 0 || out.Warnings[0].Code != "cover_low_resolution" {
		t.Errorf("100 dpi at 6x9 is a warning: %+v", out.Warnings)
	}
	var clean int
	_ = e.d.Read().QueryRow(`SELECT cover_clean FROM attachments WHERE id = ?`, out.AttachmentID).Scan(&clean)
	if clean != 1 {
		t.Error("the attachment is not flagged cover_clean")
	}
	// the same bytes again are the same attachment
	if again := e.upload("front", raw); !again.Duplicate || again.AttachmentID != out.AttachmentID {
		t.Errorf("a repeat: %+v", again)
	}
}

func TestUploadRefusesWhatACoverMayNotBe(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct {
		name string
		body []byte
		want string
	}{
		{"a CMYK JPEG", jpegHeader(600, 900, 4), "CMYK"},
		{"an SVG", []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><script>alert(1)</script></svg>`), "SVG"},
		{"an SVG with an XML header", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"/>`), "SVG"},
		{"a JPEG over 24 megapixels", jpegHeader(8000, 4000, 3), "32 million pixels"},
		{"a PNG over 16 megapixels", pngHeader(5000, 4000), "Send it as a JPEG"},
		{"a PDF", []byte("%PDF-1.4\n1 0 obj<<>>endobj\n"), "PNG or a JPEG picture"},
		{"text", []byte("not a picture"), "PNG or a JPEG picture"},
	} {
		_, err := e.uploadRaw(e.owner, e.pid, "front", nil, c.body)
		ae := ops.AsError(err)
		if err == nil || ae.Code != ops.CodeInvalid || !strings.Contains(ae.Message, c.want) {
			t.Errorf("%s: %v, want a refusal that says %q", c.name, err, c.want)
		}
	}
	var n int
	_ = e.d.Read().QueryRow(`SELECT count(*) FROM attachments WHERE project_id = ?`, e.pid).Scan(&n)
	if n != 0 {
		t.Errorf("%d attachments were stored for refused pictures", n)
	}
	if _, err := e.uploadRaw(e.owner, e.pid, "middle", nil, jpegOf(10, 10, color.RGBA{})); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("an unknown part: %v", err)
	}
}

func sides(front, back coverlayout.Side, alt string) coverlayout.SetIn {
	return coverlayout.SetIn{Shape: "sides", Front: &front, Back: &back, Alt: alt}
}

func TestSetRefusesPicturesThatAreNotCoverCleanOrAreForeign(t *testing.T) {
	e := newEnv(t)
	// an ordinary attachment of the project
	ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(pngOf(60, 90, color.RGBA{A: 255})), Length: int64(len(pngOf(60, 90, color.RGBA{A: 255})))})
	out, err := e.reg.Call(ctx, e.owner, "project.attachment.upload", attachments.UploadIn{ID: e.pid, Filename: "p.png"})
	if err != nil {
		t.Fatal(err)
	}
	ordinary := out.(attachments.Item).Item.ID
	in := sides(coverlayout.Side{Mode: "image", Attachment: ordinary}, coverlayout.Side{}, "A cover")
	in.ID = e.pid
	if ae := e.fails("project.cover_layout.set", in, ops.CodeInvalid); !strings.Contains(ae.Message, "cover upload made") {
		t.Errorf("the refusal says: %s", ae.Message)
	}
	// a cover picture of another project
	other := e.call("project.create", projects.CreateIn{Title: "Other"}).(projects.Item).Item.ID
	up, err := e.uploadRaw(e.owner, other, "front", nil, jpegOf(300, 450, color.RGBA{}))
	if err != nil {
		t.Fatal(err)
	}
	in = sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A cover")
	in.ID = e.pid
	e.fails("project.cover_layout.set", in, ops.CodeInvalid)
	// an id that is not there
	in = sides(coverlayout.Side{Mode: "image", Attachment: "att_00000000000000000000000000"}, coverlayout.Side{}, "A cover")
	in.ID = e.pid
	e.fails("project.cover_layout.set", in, ops.CodeInvalid)
	if e.get() != nil || e.coverRefs() != 0 {
		t.Error("a refused set left a layout or a reference behind")
	}
}

func TestSetValidatesTheLayout(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(600, 900, color.RGBA{}))
	img := coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}
	for name, in := range map[string]coverlayout.SetIn{
		"no alt":                  sides(img, coverlayout.Side{}, ""),
		"a bad trim":              {Shape: "sides", Trim: "7x10", Front: &img, Alt: "x"},
		"no front picture":        sides(coverlayout.Side{Mode: "image"}, coverlayout.Side{}, "x"),
		"a page option on image":  sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID, Fit: "contain"}, coverlayout.Side{}, "x"),
		"a free colour":           sides(coverlayout.Side{Mode: "page", Attachment: up.AttachmentID, Background: "#123456"}, coverlayout.Side{}, "x"),
		"a scale of 5":            sides(coverlayout.Side{Mode: "page", Attachment: up.AttachmentID, Scale: 5}, coverlayout.Side{}, "x"),
		"a margin of 3":           sides(coverlayout.Side{Mode: "page", Attachment: up.AttachmentID, MarginIn: 3}, coverlayout.Side{}, "x"),
		"a position off the grid": sides(coverlayout.Side{Mode: "page", Attachment: up.AttachmentID, Position: "middle"}, coverlayout.Side{}, "x"),
		"a front of none":         sides(coverlayout.Side{Mode: "none"}, coverlayout.Side{}, "x"),
		"a wrap with sides":       {Shape: "wrap", Front: &img, Wrap: &coverlayout.Wrap{Attachment: up.AttachmentID}, Alt: "x"},
		"no shape":                {Front: &img, Alt: "x"},
		"alt too long":            sides(img, coverlayout.Side{}, strings.Repeat("a", 501)),
	} {
		in.ID = e.pid
		if _, err := e.callAs(e.owner, "project.cover_layout.set", in); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// the defaults are filled: a page side with a picture only
	in := sides(coverlayout.Side{Mode: "page", Attachment: up.AttachmentID}, coverlayout.Side{}, "A cover")
	in.ID = e.pid
	v := e.call("project.cover_layout.set", in).(coverlayout.View)
	f := v.Front
	if f.Fit != "contain" || f.Scale != 100 || f.Position != "center" || f.Background != "#7A4B2A" || v.Trim != "6x9" || v.Back.Mode != "none" {
		t.Errorf("%+v back %+v", f, v.Back)
	}
}

func TestSetWritesRefsEventsAndTheHashAndClearRemovesThem(t *testing.T) {
	e := newEnv(t)
	a := e.upload("front", jpegOf(600, 900, color.RGBA{R: 1}))
	b := e.upload("back", jpegOf(600, 900, color.RGBA{R: 2}))
	in := sides(coverlayout.Side{Mode: "image", Attachment: a.AttachmentID}, coverlayout.Side{Mode: "image", Attachment: b.AttachmentID}, "A red door in snow")
	in.ID = e.pid

	// a preview shows the warnings and writes nothing
	prev, err := e.reg.CallDry(context.Background(), e.owner, "project.cover_layout.set", in)
	if err != nil {
		t.Fatal(err)
	}
	if v := prev.Out.(coverlayout.View); len(v.Warnings) == 0 || v.Revision != 1 {
		t.Errorf("preview: %+v", v)
	}
	if e.get() != nil || e.coverRefs() != 0 || e.events("cover_layout.updated") != 0 {
		t.Fatal("a preview wrote something")
	}

	v := e.call("project.cover_layout.set", in).(coverlayout.View)
	if v.Revision != 1 || v.Hash == "" || e.coverRefs() != 2 || e.events("cover_layout.updated") != 1 {
		t.Fatalf("revision %d refs %d events %d", v.Revision, e.coverRefs(), e.events("cover_layout.updated"))
	}
	// the same layout again is no change: no revision, no event
	in.Revision = 1
	if again := e.call("project.cover_layout.set", in).(coverlayout.View); again.Revision != 1 || again.Hash != v.Hash || e.events("cover_layout.updated") != 1 {
		t.Errorf("a repeat: %+v events %d", again, e.events("cover_layout.updated"))
	}
	// the attachment now has a reference, so deleting it needs force
	e.fails("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: a.AttachmentID}, ops.CodeConflict)
	// dropping the back removes its reference and changes the hash
	in = sides(coverlayout.Side{Mode: "image", Attachment: a.AttachmentID}, coverlayout.Side{}, "A red door in snow")
	in.ID, in.Revision = e.pid, 1
	v2 := e.call("project.cover_layout.set", in).(coverlayout.View)
	if v2.Revision != 2 || v2.Hash == v.Hash || e.coverRefs() != 1 || e.events("cover_layout.updated") != 2 {
		t.Errorf("revision %d refs %d", v2.Revision, e.coverRefs())
	}
	// clear
	if out := e.call("project.cover_layout.clear", coverlayout.ProjectIn{ID: e.pid}).(coverlayout.ClearOut); !out.Cleared {
		t.Error("nothing was cleared")
	}
	if e.get() != nil || e.coverRefs() != 0 || e.events("cover_layout.cleared") != 1 {
		t.Error("the layout or its references are still there")
	}
	if out := e.call("project.cover_layout.clear", coverlayout.ProjectIn{ID: e.pid}).(coverlayout.ClearOut); out.Cleared || e.events("cover_layout.cleared") != 1 {
		t.Error("clearing nothing is a no-op")
	}
	// the pictures stay as attachments, and now delete freely
	e.call("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: a.AttachmentID})
}

func TestRevisionIsTheConcurrencyToken(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(600, 900, color.RGBA{}))
	mk := func(alt string) coverlayout.SetIn {
		in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, alt)
		in.ID = e.pid
		return in
	}
	// four writers who all read "no layout yet": one wins, the others are told the revision to start from
	var wg sync.WaitGroup
	results := make([]error, 4)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = e.callAs(e.owner, "project.cover_layout.set", mk("Cover "+string(rune('A'+i))))
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case ops.AsError(err).Code != ops.CodeConflict:
			t.Errorf("a loser got %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d writers won, want exactly one", won)
	}
	cur := e.get()
	if cur.Revision != 1 {
		t.Fatalf("revision %d", cur.Revision)
	}
	stale := mk("Another")
	ae := e.fails("project.cover_layout.set", stale, ops.CodeConflict)
	if ae.Data["revision"] != 1 {
		t.Errorf("the conflict names the revision to start from: %v", ae.Data)
	}
	stale.Revision = 1
	if v := e.call("project.cover_layout.set", stale).(coverlayout.View); v.Revision != 2 {
		t.Errorf("revision %d", v.Revision)
	}
}

func TestForcedDeleteReturnsTheSideToGenerated(t *testing.T) {
	e := newEnv(t)
	a := e.upload("front", jpegOf(600, 900, color.RGBA{R: 1}))
	b := e.upload("back", jpegOf(600, 900, color.RGBA{R: 2}))
	in := sides(coverlayout.Side{Mode: "image", Attachment: a.AttachmentID}, coverlayout.Side{Mode: "image", Attachment: b.AttachmentID}, "A door")
	in.ID = e.pid
	v := e.call("project.cover_layout.set", in).(coverlayout.View)
	e.call("project.cover", cover.In{ID: e.pid, Part: "back"}) // the front and the thumbnail were made when it was saved
	if len(e.kept()) != 3 {
		t.Fatalf("kept %v", e.kept())
	}
	del := e.call("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: b.AttachmentID, Force: true}).(attachments.DeleteOut)
	if !del.Deleted || del.Refs != 1 {
		t.Fatalf("%+v", del)
	}
	got := e.get()
	if got.Back.Mode != "none" || got.Back.Attachment != "" || got.Front.Mode != "image" || got.Revision != 2 || got.Hash == v.Hash || e.coverRefs() != 1 {
		t.Errorf("back %+v front %+v revision %d refs %d", got.Back, got.Front, got.Revision, e.coverRefs())
	}
	if e.events("cover_layout.updated") != 2 {
		t.Errorf("the forced delete is an update of the layout: %d events", e.events("cover_layout.updated"))
	}
	// what was made from the pictures is swept with the delete, the front's too (the layout has a new key)
	if k := e.kept(); len(k) != 0 {
		t.Errorf("pictures made from the old layout are still kept: %v", k)
	}
	e.call("project.cover", cover.In{ID: e.pid})
	if len(e.kept()) != 1 {
		t.Fatalf("kept %v", e.kept())
	}
	// deleting the front's picture: the front is generated again, which is the generated cover
	e.call("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: a.AttachmentID, Force: true})
	got = e.get()
	if got.Front.Mode != "generated" || got.FrontURL != "" || e.coverRefs() != 0 {
		t.Errorf("front %+v url %q", got.Front, got.FrontURL)
	}
	if k := e.kept(); len(k) != 0 {
		t.Errorf("the picture made from the deleted one is still kept: %v", k)
	}
	f := e.call("project.cover", cover.In{ID: e.pid}).(cover.File)
	if f.ContentType != "image/svg+xml" {
		t.Errorf("project.cover is %s", f.ContentType)
	}
}

// A WebP (and a GIF) is not a cover: a printer wants a PNG or a JPEG, and the PDF writer embeds neither.
func TestUploadRefusesAWebPAndAGIF(t *testing.T) {
	e := newEnv(t)
	webp, err := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==") // a real 1x1 lossless WebP
	if err != nil {
		t.Fatal(err)
	}
	gif := []byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;")
	for name, body := range map[string][]byte{"a WebP": webp, "a GIF": gif} {
		_, err := e.uploadRaw(e.owner, e.pid, "front", nil, body)
		ae := ops.AsError(err)
		if err == nil || ae.Code != ops.CodeInvalid || ae.Data["reason"] != "cover_picture_type" || !strings.Contains(ae.Message, "PNG or a JPEG") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A layout whose picture is gone does not break the cover: the generated cover stands in.
func TestAPictureThatIsGoneIsTheGeneratedCover(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(600, 900, color.RGBA{R: 7}))
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A door")
	in.ID = e.pid
	e.call("project.cover_layout.set", in)
	if f := e.call("project.cover", cover.In{ID: e.pid}).(cover.File); f.ContentType != "image/jpeg" {
		t.Fatalf("%s", f.ContentType)
	}
	var sha, ext string
	if err := e.d.Read().QueryRow(`SELECT sha256, ext FROM attachments WHERE id = ?`, up.AttachmentID).Scan(&sha, &ext); err != nil {
		t.Fatal(err)
	}
	p, _ := attachments.BlobPath(e.dir, attachments.OwnerProject, e.pid, sha, ext)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(e.coversDir()); err != nil {
		t.Fatal(err)
	}
	if f := e.call("project.cover", cover.In{ID: e.pid}).(cover.File); f.ContentType != "image/svg+xml" {
		t.Errorf("project.cover is %s, want the generated cover", f.ContentType)
	}
	// the layout is still shown, and says what is wrong
	if v := e.get(); v == nil {
		t.Fatal("the layout is gone")
	}
}

func TestAuthOfTheOps(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(600, 900, color.RGBA{}))
	outsider := ops.Principal{UserID: testutil.User(t, e.d, ops.RoleWriter, "x@t.local"), Role: ops.RoleWriter}
	reader := ops.Principal{UserID: testutil.User(t, e.d, ops.RoleReader, "r@t.local"), Role: ops.RoleReader}
	_, err := e.d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, e.pid, reader.UserID, "reader", db.Now())
	if err != nil {
		t.Fatal(err)
	}
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A door")
	in.ID = e.pid
	for _, op := range []struct {
		name string
		in   any
	}{{"project.cover_layout.get", coverlayout.ProjectIn{ID: e.pid}}, {"project.cover_layout.set", in}, {"project.cover_layout.clear", coverlayout.ProjectIn{ID: e.pid}},
		{"project.cover_layout.check", coverlayout.CheckIn{ID: e.pid}}} {
		if _, err := e.callAs(outsider, op.name, op.in); ops.AsError(err).Code != ops.CodeNotFound {
			t.Errorf("%s as an outsider: %v, want not found", op.name, err)
		}
	}
	if _, err := e.callAs(reader, "project.cover_layout.set", in); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("set as a reader: %v", err)
	}
	if _, err := e.callAs(reader, "project.cover_layout.get", coverlayout.ProjectIn{ID: e.pid}); err != nil {
		t.Errorf("get as a reader: %v", err)
	}
	if _, err := e.callAs(reader, "project.cover_layout.check", coverlayout.CheckIn{ID: e.pid}); err != nil {
		t.Errorf("check as a reader: %v", err)
	}
	if _, err := e.uploadRaw(reader, e.pid, "front", nil, jpegOf(10, 10, color.RGBA{})); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("upload as a reader: %v", err)
	}
	if _, err := e.uploadRaw(outsider, e.pid, "front", nil, jpegOf(10, 10, color.RGBA{})); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("upload as an outsider: %v", err)
	}
}

// A page side that cannot be drawn is refused by set (and by its dry run) with the codes PlacePage gives, whoever calls it:
// the cover form, REST, the CLI or MCP. Nothing is saved, so the derivative never fails later.
func TestSetRefusesAPagePlacementThatCannotBeDrawn(t *testing.T) {
	e := newEnv(t)
	wide := e.upload("front", pngOf(14500, 1090, color.RGBA{A: 255})) // over 8 times the page's width at its own size
	thin := e.upload("back", pngOf(3000, 1, color.RGBA{A: 255}))      // a line: under a pixel at a tenth of the fit
	ok := e.upload("front", jpegOf(600, 900, color.RGBA{}))
	reason := func(err error) string {
		t.Helper()
		ae := ops.AsError(err)
		if err == nil || ae.Code != ops.CodeInvalid {
			t.Errorf("not refused as invalid: %v", err)
			return ""
		}
		r, _ := ae.Data["reason"].(string)
		return r
	}
	good := coverlayout.Side{Mode: "page", Attachment: ok.AttachmentID}
	for name, c := range map[string]struct {
		in   coverlayout.SetIn
		want string
	}{
		"a front over eight times the page": {sides(coverlayout.Side{Mode: "page", Attachment: wide.AttachmentID, Fit: "cover"}, coverlayout.Side{}, "x"), "cover_picture_extreme"},
		"a front at actual size":            {sides(coverlayout.Side{Mode: "page", Attachment: wide.AttachmentID, Fit: "actual"}, coverlayout.Side{}, "x"), "cover_picture_extreme"},
		"a back under a pixel":              {sides(good, coverlayout.Side{Mode: "page", Attachment: thin.AttachmentID, Scale: 10, Position: "top-left"}, "x"), "cover_picture_extreme"},
		"a front under a pixel":             {sides(coverlayout.Side{Mode: "page", Attachment: thin.AttachmentID, Scale: 10, Position: "top-left"}, coverlayout.Side{}, "x"), "cover_picture_extreme"},
	} {
		in := c.in
		in.ID = e.pid
		_, err := e.callAs(e.owner, "project.cover_layout.set", in)
		if got := reason(err); got != c.want {
			t.Errorf("%s: set refused with %q, want %q", name, got, c.want)
		}
		_, err = e.reg.CallDry(context.Background(), e.owner, "project.cover_layout.set", in)
		if got := reason(err); got != c.want {
			t.Errorf("%s: the dry run refused with %q, want %q", name, got, c.want)
		}
	}
	if e.get() != nil || e.coverRefs() != 0 || e.events("cover_layout.updated") != 0 {
		t.Error("a refused set left a layout, a reference or an event behind")
	}
	// A placement that can be drawn is saved, and a stored layout is still shown whatever it holds.
	in := sides(coverlayout.Side{Mode: "page", Attachment: wide.AttachmentID, Fit: "contain"}, coverlayout.Side{}, "x")
	in.ID = e.pid
	e.call("project.cover_layout.set", in)
	if e.get() == nil {
		t.Error("a contained wide picture was not saved")
	}
}
