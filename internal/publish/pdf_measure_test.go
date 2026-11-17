package publish

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

// A page whose margins leave no room to set text is refused in plain words.
func TestNarrowMeasureIsRefused(t *testing.T) {
	for name, o := range map[string]Options{
		"margins":  {Format: "pdf", PageSize: "5x8", MarginInside: F(2), MarginOutside: F(1.2)},
		"gutter":   {Format: "pdf", PageSize: "5x8", Gutter: F(1.5), MarginInside: F(1), MarginOutside: F(1)},
		"height":   {Format: "pdf", PageSize: "5x8", MarginTop: F(3), MarginBottom: F(2.5)},
		"indent":   {Format: "pdf", PageSize: "5x8", IndentInches: F(2)},
		"htmlpage": {Format: "html-paged", PageSize: "5x8", MarginInside: F(2), MarginOutside: F(1.5)},
	} {
		err := o.Validate()
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), "inches") {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, o := range map[string]Options{
		"print": print6x9(),
		"small": {Format: "pdf", PageSize: "5x8", MarginInside: F(1), MarginOutside: F(0.75)},
		"docx":  {Format: "docx", PageSize: "5x8", MarginInches: 2}, // not a print layout: the DOCX has its own rules
	} {
		if err := o.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// If such options get to the layout anyway the indent is held to a third of the
// measure and no initial takes more than a third.
func TestIndentAndDropCapAreClamped(t *testing.T) {
	o := print6x9()
	o.PageSize = "5x8"
	o.IndentInches = F(2) // refused by Validate; the layout must still cope
	o.DropCap = True()
	l := mustLayout(t, bookFrom("Clamp", strings.Repeat("Alpha beta gamma delta epsilon zeta eta theta. ", 60)), o)
	if l.pr.indent > l.pr.textW/3 {
		t.Errorf("indent %d of a measure of %d", l.pr.indent, l.pr.textW)
	}
	for _, sh := range l.sheets {
		for _, p := range sh.items {
			if p.it.par != nil && p.it.par.dropCap != nil && p.it.par.dropCap.width > p.it.w/3 {
				t.Errorf("a drop cap of %d in a measure of %d", p.it.par.dropCap.width, p.it.w)
			}
		}
	}
}

// A title is never broken in the middle of a word: the type shrinks instead.
func TestTitleWordsAreNotBroken(t *testing.T) {
	long := "Pneumonoultramicroscopicsilicovolcanoconiosis"
	b := bookFrom(long+" and "+long+" "+strings.Repeat("W", 40), "Words.")
	b.Subtitle = "Subtitle " + long
	b.Byline = "An " + long
	out, _, err := WritePDF(b, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	page := doc.Pages[0]
	// the title has the long word twice, the subtitle and byline once each, whole
	if n := strings.Count(page.Text(), long); n != 4 || !strings.Contains(page.Text(), strings.Repeat("W", 40)) {
		t.Errorf("a title word was broken (%d whole): %q", n, page.Text())
	}
	// and the chapter opener too
	b2 := bookFrom("Plain", "Words.")
	b2.Sections[0].Title = long + " " + long
	out, _, _ = WritePDF(b2, print6x9())
	doc, _ = check.ParsePDF(out)
	var found int
	for _, pg := range doc.Pages {
		for _, ln := range pg.Lines {
			if strings.Contains(ln.Text, long) {
				found++
			}
		}
	}
	if found < 2 {
		t.Errorf("the chapter title's long words were broken (%d whole)", found)
	}
}

func TestRTLLanguages(t *testing.T) {
	for _, l := range []string{"ar", "ar-EG", "he", "iw", "fa", "ur", "yi", "ps", "sd", "ug", "dv", "ckb", "ku-Arab", "az-Arab", "en-Hebr", "AR"} {
		if !IsRTL(l) {
			t.Errorf("%s is right to left", l)
		}
	}
	for _, l := range []string{"", "en", "en-GB", "ga", "ku", "ku-Latn", "ku-TR", "tr", "de", "zh-Hans", "ar-Latn"} {
		if IsRTL(l) {
			t.Errorf("%s is not right to left", l)
		}
	}
}
