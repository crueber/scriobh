package coverlayout_test

import (
	"os"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
)

// TestMeasureCoverMemory is run by hand to measure what the real op path costs in memory (docs/ARCHITECTURE.md has the
// figures): build this package's test binary, then
//
//	SCRIOBH_COVER_RSS=/path/to/picture /usr/bin/time -l ./coverlayout.test -test.run TestMeasureCoverMemory -test.v
//
// and read "maximum resident set size". The picture is uploaded as a front image, the layout is set, and the front and the
// thumbnail are made, as a person's first look at the cover page would. SCRIOBH_COVER_PART=wrap uploads it as a wrap
// (at 6x9 with no bleed) instead. It does nothing unless the variable is set.
func TestMeasureCoverMemory(t *testing.T) {
	path := os.Getenv("SCRIOBH_COVER_RSS")
	if path == "" {
		t.Skip("set SCRIOBH_COVER_RSS to a picture to measure")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	start := time.Now()
	var in coverlayout.SetIn
	if os.Getenv("SCRIOBH_COVER_PART") == "wrap" {
		up, err := e.uploadRaw(e.owner, e.pid, "wrap", map[string]string{"trim": "letter", "bleed_in": "0"}, raw)
		if err != nil {
			t.Fatal(err)
		}
		zero := 0.0
		in = coverlayout.SetIn{Shape: "wrap", Trim: "letter", Wrap: &coverlayout.Wrap{Attachment: up.AttachmentID, BleedIn: &zero}, Alt: "x"}
	} else {
		up, err := e.uploadRaw(e.owner, e.pid, "front", nil, raw)
		if err != nil {
			t.Fatal(err)
		}
		in = sides(coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}, coverlayout.Side{}, "x")
	}
	t.Logf("uploaded in %v", time.Since(start))
	in.ID = e.pid
	e.call("project.cover_layout.set", in)
	start = time.Now()
	f := e.call("project.cover", cover.In{ID: e.pid}).(cover.File)
	t.Logf("front: %d bytes in %v", len(f.Body), time.Since(start))
	start = time.Now()
	th := e.call("project.cover", cover.In{ID: e.pid, Size: "thumb"}).(cover.File)
	t.Logf("thumb: %d bytes in %v", len(th.Body), time.Since(start))
}
