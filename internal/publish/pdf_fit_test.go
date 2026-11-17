package publish

import "testing"

// Nothing on a page runs into the footnotes below it, or off the page.
func TestBodyAndNotesFitThePage(t *testing.T) {
	l := mustLayout(t, bookOf(12000), print6x9())
	withNotes := 0
	for i, sh := range l.sheets {
		var end mpt
		for _, p := range sh.items {
			if e := p.y + p.it.h; e > end {
				end = e
			}
		}
		var lines mpt
		for _, n := range sh.noteLines() {
			lines += n.h
		}
		if lines > 0 {
			withNotes++
			if want := lines + l.t.lead*2; sh.noteH != want {
				t.Errorf("page %d: noteH %d, lines and rule %d", i+1, sh.noteH, want)
			}
		}
		if end+sh.noteH > l.pr.textH {
			t.Errorf("page %d: the body ends at %d and the notes need %d of %d", i+1, end, sh.noteH, l.pr.textH)
		}
	}
	if withNotes == 0 {
		t.Error("no page had notes")
	}
}
