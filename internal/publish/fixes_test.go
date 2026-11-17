package publish

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/text"
)

// A footnote has no relationships of its own, so a picture in one is a note in
// the text, not a drawing the package cannot resolve.
func TestAnImageInAFootnoteIsANote(t *testing.T) {
	img := pngImage(t, 20, 10)
	d := text.Structure("Text.[^1]\n\n[^1]: See ![A map](/files/att_abcdefghij1) here.\n", text.StructOptions{})
	b := sampleBook()
	b.Sections = []Section{{Kind: SecChapter, Title: "One", Number: 1, Level: 1, Nodes: d.Nodes, Notes: d.Notes}}
	b.Images = map[string]Image{"/files/att_abcdefghij1": img}
	docx, err := WriteDOCX(b, shunnOptions())
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.DOCX(docx); len(probs) > 0 {
		t.Fatalf("%v", probs)
	}
	parts := unzipTo(t, docx)
	if strings.Contains(parts["word/footnotes.xml"], "<w:drawing>") || !strings.Contains(parts["word/footnotes.xml"], "[image: A map (not included)]") {
		t.Errorf("footnote: %s", parts["word/footnotes.xml"])
	}
	// and an image with no bytes (a preview that did not read the file) is never embedded
	d = text.Structure("![A map](/files/att_abcdefghij1)\n", text.StructOptions{})
	b.Sections = []Section{{Kind: SecChapter, Title: "One", Number: 1, Level: 1, Nodes: d.Nodes}}
	b.Images = map[string]Image{"/files/att_abcdefghij1": {Mime: "image/png", Ext: "png", Width: 20, Height: 10, Hash: img.Hash}}
	docx, _ = WriteDOCX(b, shunnOptions())
	if probs := check.DOCX(docx); len(probs) > 0 {
		t.Fatalf("%v", probs)
	}
	parts = unzipTo(t, docx)
	if _, ok := parts["word/media/image1.png"]; ok || strings.Contains(parts["word/document.xml"], "<w:drawing>") {
		t.Error("an image with no bytes was embedded")
	}
	epub, _ := WriteEPUB(b, epubOptions())
	if probs := check.EPUB(epub); len(probs) > 0 {
		t.Fatalf("%v", probs)
	}
}
