package compile_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/publish/check"
)

// outline[].page is the physical page; label is the number printed on it.
func TestPreviewOutlineHasThePrintedLabel(t *testing.T) {
	f := newFx(t, "Labels")
	f.novel()
	p := f.preview("print-6x9", publish.Options{})
	out := f.compile("print-6x9", publish.Options{})
	doc, _ := check.ParsePDF(out.Body)
	checked := 0
	for _, o := range p.Outline {
		if o.Page == 0 {
			continue
		}
		checked++
		if want := doc.Pages[o.Page-1].Label; o.Label != want {
			t.Errorf("%q starts on physical page %d, printed %q; the preview says %q", o.Title, o.Page, want, o.Label)
		}
	}
	if checked == 0 {
		t.Fatal("nothing checked")
	}
	// the front matter is numbered in roman, so page and label differ there
	for _, o := range p.Outline {
		if o.Kind == "part" && o.Label == "" {
			t.Error("a part has no label")
		}
	}
}
