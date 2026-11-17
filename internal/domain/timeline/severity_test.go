package timeline_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/testutil"
)

// Two events on the same date can be hours apart, so day against day is
// something to look at, not an error; a clock time or an explicit end on both
// makes it a contradiction.
func TestLocationConflictSeverity(t *testing.T) {
	x := newH(t)
	x.ev("Dublin all day", "1998-04-20", "", x.dublin, "Elara")
	x.ev("Cork all day", "1998-04-20", "", x.cork, "Elara")
	out := x.check(x.owner)
	if len(out.Findings) != 1 || out.Findings[0].Kind != "location_conflict" || out.Findings[0].Severity != "warning" || out.Errors != 0 || out.Warnings != 1 {
		t.Fatalf("day against day should be one warning: %+v", out)
	}
	y := newH(t)
	y.ev("Dublin morning", "1998-04-20T09:00", "", y.dublin, "Elara")
	y.ev("Cork morning", "1998-04-20T09:00", "", y.cork, "Elara")
	if out := y.check(y.owner); len(out.Findings) != 1 || out.Findings[0].Severity != "error" {
		t.Errorf("two timed events at once: %+v", out.Findings)
	}
	// One timed and one all-day is still only a warning.
	z := newH(t)
	z.ev("Dublin all day", "1998-04-20", "", z.dublin, "Elara")
	z.ev("Cork at nine", "1998-04-20T09:00", "", z.cork, "Elara")
	if out := z.check(z.owner); len(out.Findings) != 1 || out.Findings[0].Severity != "warning" {
		t.Errorf("all-day against timed: %+v", out.Findings)
	}
}

// Hundreds of long, overlapping events in different places must not produce
// thousands of findings or an answer that takes seconds: each kind is capped
// and the check says how many it left out.
func TestFindingsAreCappedAndTheScanIsBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("creates 300 events")
	}
	x := newH(t)
	for i := 0; i < 300; i++ {
		loc := x.dublin
		if i%2 == 1 {
			loc = x.cork
		}
		x.ev(fmt.Sprintf("Long stay %03d", i), "1900", "2000", loc, "Elara")
	}
	s := time.Now()
	out := x.check(x.owner)
	took := time.Since(s)
	n := 0
	for _, f := range out.Findings {
		if f.Kind == "location_conflict" {
			n++
		}
	}
	if n != timeline.MaxFindingsPerKind {
		t.Errorf("%d location findings, want the cap of %d", n, timeline.MaxFindingsPerKind)
	}
	if out.Truncated["location_conflict"] == 0 {
		t.Errorf("the check should say it left findings out: %+v", out.Truncated)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		bound(t, "check with 300 overlapping events", took, 2*time.Second)
	})
}

func bound(t *testing.T, name string, got, limit time.Duration) {
	t.Helper()
	t.Logf("%s: %v (limit %v)", name, got.Round(time.Millisecond), limit*timingFactor)
	if got > limit*timingFactor {
		t.Errorf("%s took %v, over %v", name, got, limit*timingFactor)
	}
}
