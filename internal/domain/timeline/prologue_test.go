package timeline_test

import "testing"

func outOfOrder(x *h) (n int) {
	for _, f := range x.check(x.owner).Findings {
		if f.Kind == "narrated_out_of_order" {
			n++
		}
	}
	return n
}

// A prologue that looks ahead is one step back in time, not a reason to flag
// every scene after it.
func TestAPrologueIsOneFindingNotOnePerScene(t *testing.T) {
	x := newH(t)
	x.scene("Prologue", "2020")
	for _, d := range []string{"1990", "1991", "1992", "1993", "1994"} {
		x.scene("Year "+d, d)
	}
	if n := outOfOrder(x); n != 1 {
		t.Errorf("a 2020 prologue before 1990-94 gave %d findings, want 1", n)
	}
	// Plain chronological order gives none.
	y := newH(t)
	for _, d := range []string{"1990", "1991", "1992", "1993", "1994", "2020"} {
		y.scene("Year "+d, d)
	}
	if n := outOfOrder(y); n != 0 {
		t.Errorf("chronological order gave %d findings", n)
	}
	// Consecutive steps back are one run; a later, separate flashback is another.
	z := newH(t)
	for _, d := range []string{"2000", "1990", "1980", "1985", "2001", "1999"} {
		z.scene("Year "+d, d)
	}
	// 2000>1990>1980 is one run, 1980<1985<2001 goes forward, 2001>1999 is a second.
	if n := outOfOrder(z); n != 2 {
		t.Errorf("a run of steps back and one more flashback should be 2 findings, got %d", n)
	}
}
