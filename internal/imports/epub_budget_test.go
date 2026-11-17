package imports_test

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/testutil"
)

// bigBook is a 300-page book (about 90,000 words in 60 chapters in three parts, with
// footnotes, scene breaks and 120 pictures of the size a photograph in an e-book is). It is
// the book of the Phase 7 exit criterion: importing it takes under a minute.
func bigBook(t testing.TB, chapters, images int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(7))
	words := strings.Fields("the marsh water road wall rain dusk dawn bus crossroads lamp quay harbour tide salt reed heron mud boat rope lantern shore")
	para := func() string {
		var b strings.Builder
		n := 40 + rng.Intn(60)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(words[rng.Intn(len(words))])
		}
		return strings.ToUpper(b.String()[:1]) + b.String()[1:] + "."
	}
	var ents []zent
	var man, spine, nav strings.Builder
	perChapter := max(1, images/max(chapters, 1))
	img := 0
	for c := 0; c < chapters; c++ {
		var body strings.Builder
		fmt.Fprintf(&body, `<section epub:type="chapter"><h2>Chapter %d</h2>`, c+1)
		var notes strings.Builder
		for p := 0; p < 18; p++ {
			fmt.Fprintf(&body, "<p>%s", para())
			if p%4 == 1 {
				fmt.Fprintf(&body, `<a epub:type="noteref" id="r%d_%d" href="#n%d_%d"><sup>%d</sup></a>`, c, p, c, p, p)
				fmt.Fprintf(&notes, `<aside epub:type="footnote" id="n%d_%d"><p>A note about %s.</p></aside>`, c, p, words[rng.Intn(len(words))])
			}
			body.WriteString("</p>")
			if p == 5 {
				body.WriteString(`<p class="scenebreak">* * *</p>`)
			}
			if p%6 == 3 && img < images {
				for k := 0; k < perChapter/2+1 && img < images; k++ {
					fmt.Fprintf(&body, `<p><img src="../images/p%03d.jpg" alt="Plate %d"/></p>`, img, img)
					img++
				}
			}
		}
		body.WriteString(`<section class="footnotes" epub:type="footnotes">` + notes.String() + `</section></section>`)
		name := fmt.Sprintf("text/c%03d.xhtml", c)
		ents = append(ents, ze("OEBPS/"+name, page(fmt.Sprintf("Chapter %d", c+1), "", body.String())))
		fmt.Fprintf(&man, `<item id="c%d" href="%s" media-type="application/xhtml+xml"/>`+"\n", c, name)
		fmt.Fprintf(&spine, `<itemref idref="c%d"/>`+"\n", c)
		fmt.Fprintf(&nav, `<li><a href="%s">Chapter %d</a></li>`+"\n", name, c+1)
	}
	for i := 0; i < img; i++ {
		im := image.NewRGBA(image.Rect(0, 0, 360, 260))
		for x := 0; x < 360; x++ {
			for y := 0; y < 260; y++ {
				im.Set(x, y, color.RGBA{uint8(x/2 + rng.Intn(60)), uint8(y/2 + rng.Intn(60)), uint8(i*3 + rng.Intn(80)), 255})
			}
		}
		var b bytes.Buffer
		_ = jpeg.Encode(&b, im, &jpeg.Options{Quality: 80})
		ents = append(ents, zent{name: fmt.Sprintf("OEBPS/images/p%03d.jpg", i), data: b.Bytes(), store: true})
		fmt.Fprintf(&man, `<item id="p%d" href="images/p%03d.jpg" media-type="image/jpeg"/>`+"\n", i, i)
	}
	opf := `<?xml version="1.0" encoding="utf-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">urn:uuid:99999999-2222-3333-4444-555555555555</dc:identifier><dc:title>The Long Book</dc:title><dc:language>en</dc:language><dc:creator>Bench Author</dc:creator></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>` + man.String() + `</manifest><spine>` + spine.String() + `</spine></package>`
	all := []zent{ze("mimetype", epubMime), ze("META-INF/container.xml", containerXML("OEBPS/content.opf")), ze("OEBPS/content.opf", opf),
		ze("OEBPS/nav.xhtml", page("Contents", "", `<nav epub:type="toc"><ol>`+nav.String()+`</ol></nav>`))}
	return zipEntries(t, append(all, ents...)...)
}

// The budget: a 300-page book with its pictures imports (preview and apply, which is what a person
// waits for) in under a minute on the target hardware. The test states the budget and a margin for
// a loaded machine; the measured time is logged.
func TestEpubImportOfAThreeHundredPageBookIsUnderAMinute(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 10 MB book")
	}
	e, _ := newEpubEnv(t)
	book := bigBook(t, 60, 120)
	t.Logf("the book: %.1f MB", float64(len(book))/1e6)
	t0 := time.Now()
	pv := e.previewConv(imports.PreviewIn{Name: "long.epub", Epub: book})
	tp := time.Since(t0)
	t1 := time.Now()
	ap := e.applyConv(pv, imports.ApplyIn{Name: "long.epub", Epub: book})
	ta := time.Since(t1)
	t.Logf("preview %v, apply %v, %d documents, %d words, %d pictures, %d footnotes", tp.Round(time.Millisecond), ta.Round(time.Millisecond), ap.Documents, ap.Words, ap.Pictures, pv.Epub.Footnotes)
	if ap.Documents != 60 || ap.Pictures != 120 || pv.Epub.Footnotes != 60*5 {
		t.Errorf("what was read: %d documents, %d pictures, %d footnotes", ap.Documents, ap.Pictures, pv.Epub.Footnotes)
	}
	if ap.Words < 60_000 {
		t.Errorf("only %d words", ap.Words)
	}
	total := tp + ta
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if total > 60*time.Second*timingFactor {
			t.Errorf("preview and apply took %v; the budget is 60 s", total)
		}
	})
}
