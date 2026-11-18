package publish

import (
	"bytes"
	"image"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

// The cover_page option puts the front first and a blank page behind it, so the book's own pages keep their sides, their
// labels and their outline; without the option the file is what it was.
func TestCoverPageIsFirstAndLeavesTheBookAlone(t *testing.T) {
	plain, _, err := WritePDF(featureBook(), print6x9())
	if err != nil {
		t.Fatal(err)
	}
	base, probs := check.ParsePDF(plain)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	front := testJPEG(t, 1900, 2800)
	for _, crop := range []bool{false, true} {
		for _, shape := range []string{"image", "page", "generated"} {
			t.Run(shape, func(t *testing.T) {
				b := featureBook()
				o := print6x9()
				cs := &CoverSpec{Title: "Features", Author: "A. Writer", Kind: "novel", Color: "#2F5D50", Language: "en"}
				switch shape {
				case "image":
					cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelImage, Image: front}}
				case "page":
					cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelPage, Image: front, Background: "#2F5D50",
						Place: &CoverPlacement{CanvasW: 1707, CanvasH: 2560, Box: image.Rect(0, 0, 1707, 2560), Dst: image.Rect(0, -100, 1707, 2660)}}}
				}
				b.PrintCover = cs
				if bb, _, _ := WritePDF(b, o); !bytes.Equal(bb, plain) {
					t.Error("a cover on the book changed the file with the option off")
				}
				o.CoverPage = True()
				if crop {
					o.CropMarks = True()
				}
				out, ws, err := WritePDF(b, o)
				if err != nil {
					t.Fatal(err)
				}
				doc, probs := check.ParsePDF(out)
				for _, p := range probs {
					t.Error("the checker:", p)
				}
				ref := base
				if crop {
					po := print6x9()
					po.CropMarks = True()
					pl, _, _ := WritePDF(featureBook(), po)
					ref, _ = check.ParsePDF(pl)
				}
				if len(doc.Pages) != len(ref.Pages)+2 {
					t.Fatalf("%d pages, want %d and the cover and its blank", len(doc.Pages), len(ref.Pages))
				}
				if doc.Pages[0].Label != "Cover" || doc.Pages[1].Label != "Cover" || doc.Pages[2].Label != ref.Pages[0].Label {
					t.Errorf("labels %q %q %q, want Cover, Cover, %q", doc.Pages[0].Label, doc.Pages[1].Label, doc.Pages[2].Label, ref.Pages[0].Label)
				}
				for i := range ref.Pages {
					if doc.Pages[i+2].Text() != ref.Pages[i].Text() || doc.Pages[i+2].Label != ref.Pages[i].Label {
						t.Errorf("page %d is not the book's page %d", i+3, i+1)
						break
					}
				}
				if strings.Join(doc.Outline, "|") != strings.Join(ref.Outline, "|") {
					t.Errorf("the outline changed")
				}
				c0, c1 := doc.Pages[0], doc.Pages[1]
				if c0.W != ref.Pages[0].W || c0.H != ref.Pages[0].H || c1.W != c0.W {
					t.Errorf("the cover is %vx%v, the book's pages %vx%v", c0.W, c0.H, ref.Pages[0].W, ref.Pages[0].H)
				}
				if len(c1.Lines) != 0 || len(c1.Draws) != 0 || len(c1.Fills) != 0 {
					t.Errorf("the page behind the cover is not blank: %+v", c1)
				}
				if shape == "generated" {
					if !strings.Contains(c0.Text(), "Features") {
						t.Errorf("generated front: %q", c0.Text())
					}
					return
				}
				if len(c0.Draws) != 1 || c0.Draws[0].Image.W != 1900 || c0.Draws[0].Clip == nil {
					t.Fatalf("the cover page: %+v", c0.Draws)
				}
				clip := c0.Draws[0].Clip
				if crop { // the art runs into the bleed: the clip is the trim and 1/8 in more
					if !near(clip[2]-clip[0], 6*72+18, 0.01) || len(c0.Trim) != 4 {
						t.Errorf("clip %v trim %v", clip, c0.Trim)
					}
				} else if !near(clip[2]-clip[0], 6*72, 0.01) || !near(clip[3]-clip[1], 9*72, 0.01) {
					t.Errorf("clip %v", clip)
				}
				_ = ws
			})
		}
	}
}
