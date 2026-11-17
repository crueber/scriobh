package coverlayout_test

import (
	"bytes"
	"context"
	"image/color"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/ops"
)

// The reviewer's scenario: A reads revision 1, B clears the cover and makes one again, and A's change must not land on it.
// A cleared layout keeps its revision counting upward, so the second layout is never revision 1 again.
func TestRevisionNeverComesBackAfterAClear(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(600, 900, color.RGBA{R: 1}))
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A door")
	in.ID = e.pid
	first := e.call("project.cover_layout.set", in).(coverlayout.View)
	if first.Revision != 1 {
		t.Fatalf("revision %d", first.Revision)
	}
	// B clears it and makes it again
	e.call("project.cover_layout.clear", coverlayout.ProjectIn{ID: e.pid})
	if e.get() != nil {
		t.Fatal("a cleared layout is still there")
	}
	b := in
	b.Alt, b.Revision = "B's door", 0 // B saw none
	again := e.call("project.cover_layout.set", b).(coverlayout.View)
	if again.Revision != 3 {
		t.Errorf("made again after a clear: revision %d, want 3 (1, cleared as 2)", again.Revision)
	}
	// A, who read revision 1, is told the cover changed
	a := in
	a.Alt, a.Revision = "A's door", 1
	ae := e.fails("project.cover_layout.set", a, ops.CodeConflict)
	if ae.Data["revision"] != 3 {
		t.Errorf("the conflict names the revision to start from: %v", ae.Data)
	}
	if got := e.get(); got.Alt != "B's door" {
		t.Errorf("A's change landed: %q", got.Alt)
	}
	// and the tombstone is nowhere else: the list says none while cleared, a clear of nothing is a no-op
	e.call("project.cover_layout.clear", coverlayout.ProjectIn{ID: e.pid})
	if out := e.call("project.cover_layout.clear", coverlayout.ProjectIn{ID: e.pid}).(coverlayout.ClearOut); out.Cleared {
		t.Error("clearing a cleared layout is not a no-op")
	}
	if coverlayout.Hash(context.Background(), e.d.Read(), e.pid) != "" {
		t.Error("a cleared layout has a hash")
	}
	// a writer who read the cleared state (no layout: 0, or the cleared revision) may make one
	c := in
	c.Alt, c.Revision = "C's door", 4
	if v := e.call("project.cover_layout.set", c).(coverlayout.View); v.Revision != 5 {
		t.Errorf("revision %d", v.Revision)
	}
}

// A progressive JPEG that would take more memory to decode than a cover may is refused at the door with the way out.
func TestAProgressiveJPEGOverTheMemoryBudgetIsRefused(t *testing.T) {
	prog := func(w, h int) []byte {
		b := jpegHeader(w, h, 3)
		b[3] = 0xC2 // SOF2: progressive
		for i := range b {
			if b[i] == 0xFF && i+1 < len(b) && b[i+1] == 0xC0 {
				b[i+1] = 0xC2
			}
		}
		return b
	}
	if err := coverlayout.CheckPicture(prog(5000, 4200)); err == nil { // 21 MP at 15 bytes a pixel: 315 MB
		t.Error("a 21-megapixel progressive 4:4:4 JPEG was accepted")
	} else if r, ok := err.(*coverlayout.Refusal); !ok || r.Code != "cover_picture_progressive" || !contains(r.Message, "baseline") {
		t.Errorf("%v", err)
	}
	if err := coverlayout.CheckPicture(prog(4000, 4500)); err != nil { // 18 MP: 270 MB, within
		t.Errorf("an 18-megapixel progressive JPEG: %v", err)
	}
	if err := coverlayout.CheckPicture(jpegHeader(5000, 4200, 3)); err != nil { // the same size, baseline: 63 MB
		t.Errorf("a baseline JPEG of the same size: %v", err)
	}
	if err := coverlayout.CheckPicture(jpegHeader(6000, 4001, 3)); err == nil {
		t.Error("a picture over 24 megapixels was accepted")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A picture deleted while its derivative is being made must not leave a file made from it: the derivative is kept only if the
// layout is still the one it was made for, and the sweep that follows the delete waits for the one under way.
func TestADerivativeInTheMakingIsNotKeptWhenItsPictureIsDeleted(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(600, 900, color.RGBA{R: 9}))
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A door")
	in.ID = e.pid
	e.call("project.cover_layout.set", in)
	Sweep(t, e) // nothing kept: the next look makes it
	var wg sync.WaitGroup
	hooked := false
	coverlayout.SetAfterBuild(func() {
		if hooked {
			return
		}
		hooked = true
		wg.Add(1)
		go func() { // the picture is deleted (forced) while this derivative is made
			defer wg.Done()
			e.call("project.attachment.delete", attachments.DeleteIn{ID: e.pid, AttID: up.AttachmentID, Force: true})
		}()
		deadline := time.Now().Add(10 * time.Second)
		for e.get().Front.Mode != "generated" { // wait for the delete to commit; its sweep then waits for this derivation's lock
			if time.Now().After(deadline) {
				t.Error("the delete did not commit")
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	})
	t.Cleanup(func() { coverlayout.SetAfterBuild(nil) })
	f := e.call("project.cover", cover.In{ID: e.pid}).(cover.File)
	wg.Wait()
	if f.ContentType != "image/jpeg" {
		t.Errorf("the request that was being answered: %s", f.ContentType)
	}
	if k := e.kept(); len(k) != 0 {
		t.Errorf("a file made from the deleted picture is still kept: %v", k)
	}
}

// Sweep removes everything kept for the project, as a clear does.
func Sweep(t *testing.T, e *env) {
	t.Helper()
	coverlayout.Sweep(e.dir, e.pid, "")
	if k := e.kept(); len(k) != 0 {
		t.Fatalf("kept after a sweep: %v", k)
	}
}

// A stale temporary file (a write that was cut short) goes with the next sweep, and the sweep takes the project's lock, so
// it never removes one that a derivation is writing.
func TestASweepRemovesTheTemporaryFilesAStoppedWriteLeft(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(600, 900, color.RGBA{R: 5}))
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A door")
	in.ID = e.pid
	v := e.call("project.cover_layout.set", in).(coverlayout.View)
	if len(e.kept()) != 2 {
		t.Fatalf("kept %v", e.kept())
	}
	writeFile(t, e.coversDir()+"/tmp-123", "half a file")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(e.coversDir()+"/tmp-123", old, old); err != nil {
		t.Fatal(err)
	}
	writeFile(t, e.coversDir()+"/tmp-456", "a write under way") // fresh: never removed by a sweep
	in.Revision, in.Alt = v.Revision, "A door again"
	e.call("project.cover_layout.set", in)
	var left []string
	for _, k := range e.kept() {
		if len(k) > 3 && k[:4] == "tmp-" {
			left = append(left, k)
		}
	}
	if len(left) != 1 || left[0] != "tmp-456" {
		t.Errorf("the stale temporary file should be gone and the fresh one kept: %v", e.kept())
	}
}

// exifTurned is an APP1 segment that says the picture is to be turned (orientation 6), put after the start of the JPEG.
func exifTurned(jpg []byte) []byte {
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00")
	seg := append([]byte("Exif\x00\x00"), tiff...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// A rotated JPEG is decoded at upload to be turned, which takes its share of the decode memory like a derivative; with none
// to be had the upload is told the server is busy (not that the picture is damaged), and one that needs no decoding is not
// held up. The colour profile goes across the turn.
func TestTheUploadsRotationTakesItsShareOfTheDecodeMemory(t *testing.T) {
	e := newEnv(t)
	turned := exifTurned(withProfile(jpegOf(600, 900, color.RGBA{R: 21}), "Display P3"))
	upright := jpegOf(600, 900, color.RGBA{R: 22})
	release := coverlayout.HoldDecodeMemory()
	if _, err := e.uploadRaw(e.owner, e.pid, "front", nil, upright); err != nil {
		t.Errorf("an upright picture decodes nothing and is not held up: %v", err)
	}
	_, err := e.uploadRaw(e.owner, e.pid, "front", nil, turned)
	if ae := ops.AsError(err); err == nil || ae.Code != ops.CodeRateLimited {
		t.Errorf("a rotated picture with no decode memory to be had: %v", err)
	}
	release()
	up, err := e.uploadRaw(e.owner, e.pid, "front", nil, turned)
	if err != nil {
		t.Fatal(err)
	}
	if up.Width != 900 || up.Height != 600 || !up.Rotated {
		t.Errorf("turned: %+v", up)
	}
	for _, w := range up.Warnings {
		if w.Code == "cover_profile_dropped" {
			t.Errorf("the profile did not go across the turn: %+v", up.Warnings)
		}
	}
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A cover")
	in.ID = e.pid
	e.call("project.cover_layout.set", in)
	if f := e.cover(cover.In{}); !bytes.Contains(f.Body, []byte("Display P3")) {
		t.Error("the front of a turned Display P3 picture carries no profile")
	}
}

// One order everywhere (the lock, then a slot): a caller already inside the compile gate takes no lock, so a derivative
// that is being made under the lock never makes it wait; and a sweep waits for the lock however long that takes.
func TestACallerInsideTheGateNeverWaitsForTheProjectLock(t *testing.T) {
	e := newEnv(t)
	up := e.upload("front", jpegOf(600, 900, color.RGBA{R: 30}))
	in := sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "A door")
	in.ID = e.pid
	e.call("project.cover_layout.set", in)
	Sweep(t, e)
	var stopped atomic.Bool
	entered, proceed := make(chan struct{}), make(chan struct{})
	coverlayout.SetAfterBuild(func() {
		if stopped.CompareAndSwap(false, true) { // the first derivation (it holds the lock) stops here; the others go on
			close(entered)
			<-proceed
		}
	})
	t.Cleanup(func() { coverlayout.SetAfterBuild(nil) })
	first := make(chan error, 1)
	go func() {
		_, _, err := coverlayout.Make(context.Background(), e.reg.Env(), e.pid, coverlayout.NameFront, false)
		first <- err
	}()
	<-entered
	// a compile holding its slot asks for the same cover: it neither waits for the lock nor is refused
	d, _, err := coverlayout.Make(context.Background(), e.reg.Env(), e.pid, coverlayout.NameFront, true)
	if err != nil || d == nil {
		t.Errorf("a caller inside the gate: %v %v", d != nil, err)
	}
	select {
	case <-first:
		t.Error("the caller inside the gate waited for the derivation that holds the lock")
	default:
	}
	// a sweep waits for the lock, and removes what the one that holds it keeps
	swept := make(chan struct{})
	go func() { coverlayout.Sweep(e.dir, e.pid, ""); close(swept) }()
	select {
	case <-swept:
		t.Error("a sweep went on under a lock that was held")
	case <-time.After(150 * time.Millisecond):
	}
	close(proceed)
	if err := <-first; err != nil {
		t.Errorf("the derivation that held the lock: %v", err)
	}
	select {
	case <-swept:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweep never ran")
	}
	if k := e.kept(); len(k) != 0 {
		t.Errorf("kept after the sweep: %v", k)
	}
}
