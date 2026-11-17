package publish

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/text"
)

// bookFrom builds a book from Markdown chapters.
func bookFrom(title string, chapters ...string) *Book {
	notes := 0
	b := &Book{Title: title, Byline: "A. Writer", Language: "en", Identifier: "id", Modified: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), InputHash: "h"}
	for i, md := range chapters {
		d := text.Structure(md, text.StructOptions{NoteStart: notes})
		notes += len(d.Notes)
		b.Sections = append(b.Sections, Section{Kind: SecChapter, Title: fmt.Sprintf("Chapter title %d", i+1), Number: i + 1, Level: 1, Nodes: d.Nodes, Notes: d.Notes})
	}
	return b
}

func jpegImage(w, h int) Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 120, 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return Image{Data: buf.Bytes(), Mime: "image/jpeg", Ext: "jpg", Width: w, Height: h, Hash: fmt.Sprintf("jpg%d", w)}
}

const feature = `Alpha *beta* **gamma** ~~delta~~ ` + "`code`" + ` epsilon[^1] zeta eta theta. ` + "Iota kappa lambda mu nu xi omicron pi rho sigma tau upsilon phi chi psi omega. " +
	`Alpha *beta* **gamma** delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron pi rho sigma tau upsilon phi chi psi omega.

A table follows:

| Name | Qty | Note |
|:-----|----:|:----:|
| Salt | 12 | the *good* kind |
| Tar | 3 | black and sticky and entirely too long to fit in one line of a narrow column |
| Nets | 40 | |

![A picture](/files/att_picture1234)

` + "```" + `
func main() {
	fmt.Println("hello, a code line that is long enough to need wrapping across the measure of the page")
}
` + "```" + `

1. First
2. Second
   - nested bullet
   - another
3. Third

> A quotation that runs over a couple of lines so that its indent can be seen in the proof.
>
> And a second paragraph of the quotation.

* * *

Closing paragraph with a note.[^2]

[^1]: First note.
[^2]: Second note, which is rather longer than the first so that it wraps onto a second line at the foot of the page and shows the hanging number.
`

func TestFeaturesPDF(t *testing.T) {
	b := bookFrom("Features", feature, feature, feature)
	b.Images = map[string]Image{"/files/att_picture1234": jpegImage(900, 600)}
	for _, o := range []Options{print6x9(), func() Options {
		o := print6x9()
		o.DropCap, o.ChapterOrnament, o.CropMarks, o.Justify = True(), True(), True(), False()
		return o
	}(), func() Options {
		o := print6x9()
		o.Footnotes = "chapter"
		return o
	}(), func() Options {
		o := print6x9()
		o.Footnotes = "book"
		o.PageNumbers = "header"
		o.HeaderUpper = True()
		return o
	}()} {
		out, warns, err := WritePDF(b, o)
		if err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("feat-%s-%v-%v.pdf", o.Footnotes, Set(o.DropCap), Set(o.CropMarks))
		keep(t, name, out)
		doc, probs := check.ParsePDF(out)
		for _, p := range probs {
			t.Errorf("%s: %s", name, p)
		}
		for _, w := range warns {
			t.Logf("%s: warning %s: %s", name, w.Code, w.Message)
		}
		txt := doc.Text()
		for _, want := range []string{"Alpha beta gamma delta", "Salt", "the good kind", "nested bullet", "A quotation that runs", "First note.", "func main()"} {
			if !strings.Contains(strings.ReplaceAll(txt, "\n", " "), want) {
				t.Errorf("%s: %q is not in the text", name, want)
			}
		}
	}
}
