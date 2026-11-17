package publish

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

func longNote(tag string, words int) string {
	var b strings.Builder
	for i := 0; i < words; i++ {
		fmt.Fprintf(&b, "%s%d ", tag, i)
	}
	return b.String()
}

// A footnote longer than the room left on a chapter opener does not push the
// first paragraph off the page: the note is split and its rest runs on.
func TestLongNoteDoesNotEmptyAChapterOpener(t *testing.T) {
	md := "Opening paragraph with a reference.[^1] " + strings.Repeat("More words in the first paragraph so that it has a few lines of text. ", 3) +
		"\n\n" + strings.Repeat("Another paragraph of ordinary running text for the page. ", 12) +
		"\n\n[^1]: " + longNote("opennote", 700) + "\n"
	b := bookFrom("Opener", md)
	l := mustLayout(t, b, print6x9())
	for _, w := range l.Warnings {
		if w.Code == "widow_orphan" {
			t.Errorf("a rule gave way: %s", w.Message)
		}
	}
	// the opener page holds the heading and body text, not just the heading
	var first *sheet
	for _, sh := range l.sheets {
		for _, p := range sh.items {
			if p.it.sec > 0 {
				first = sh
			}
		}
		if first != nil {
			break
		}
	}
	if first == nil {
		t.Fatal("no opener")
	}
	withRef := false
	for _, p := range first.items {
		if len(p.it.notes) > 0 && len(p.nl) > 0 {
			withRef = true
		}
	}
	if !withRef {
		t.Error("the reference is not on the opener page: the long note pushed the paragraph away and left the opener empty")
	}
	if len(first.items) < 4 {
		t.Errorf("the opener page has %d lines", len(first.items))
	}
	// the note is longer than a page's room: its rest is on the next page
	split := false
	for _, sh := range l.sheets {
		if len(sh.carried) > 0 {
			split = true
		}
	}
	if !split {
		t.Error("the note was not split across pages")
	}
	out, _, err := l.Render()
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	txt := doc.Text()
	for _, w := range []string{"opennote0", "opennote350", "opennote699"} {
		if !strings.Contains(txt, w) {
			t.Errorf("%s is not in the PDF: the note was cut", w)
		}
	}
	// a continued note has no rule of its own, and nothing overlaps
	for i, sh := range l.sheets {
		var end mpt
		for _, p := range sh.items {
			end = max(end, p.y+p.it.h)
		}
		if end+sh.noteH > l.pr.textH {
			t.Errorf("page %d: body and notes overlap", i+1)
		}
	}
}

// In the middle of a chapter a long note splits across the page where it would
// not fit, and no page is left with a hole at its foot.
func TestLongNotesSplitMidChapter(t *testing.T) {
	var md strings.Builder
	var notes strings.Builder
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&md, "Paragraph %d starts here and carries a reference.[^n%d] %s\n\n", i, i, strings.Repeat("It then runs on for a few more lines of ordinary text in the measure. ", 4))
		fmt.Fprintf(&notes, "[^n%d]: %s\n\n", i, longNote(fmt.Sprintf("note%dw", i), 90))
	}
	b := bookFrom("Mid", md.String()+notes.String())
	l := mustLayout(t, b, print6x9())
	for _, w := range l.Warnings {
		if w.Code == "widow_orphan" {
			t.Errorf("a rule gave way: %s", w.Message)
		}
	}
	carried := 0
	for i, sh := range l.sheets {
		if len(sh.items) == 0 {
			continue
		}
		var end mpt
		for _, p := range sh.items {
			end = max(end, p.y+p.it.h)
		}
		if end+sh.noteH > l.pr.textH {
			t.Errorf("page %d: body and notes overlap", i+1)
		}
		if len(sh.carried) > 0 {
			carried++
		}
		// a hole: a page of the chapter that is not its last has more than a few
		// lines of room unused
		if i >= l.secPage[0] && i < len(l.sheets)-2 && sh.kind == pageBody {
			if free := l.pr.textH - end - sh.noteH; free > 6*l.t.lead && len(sh.items) > 0 {
				t.Errorf("page %d has a hole of %.1f lines", i+1, float64(free)/float64(l.t.lead))
			}
		}
	}
	if carried == 0 {
		t.Error("no note was ever split: the case was not exercised")
	}
	out, _, err := l.Render()
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	txt := doc.Text()
	for i := 0; i < 40; i++ {
		for _, w := range []string{fmt.Sprintf("note%dw0 ", i), fmt.Sprintf("note%dw89", i)} {
			if !strings.Contains(txt, w) {
				t.Errorf("%q is not in the PDF", w)
			}
		}
	}
}
