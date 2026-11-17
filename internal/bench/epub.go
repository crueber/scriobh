package bench

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"strings"
)

// SyntheticEPUB makes the book the EPUB import is held to: a reflowable EPUB 3 of the
// given number of chapters, each with a dozen or so paragraphs of about fifty words, a
// footnote for every fourth paragraph in an aside at the chapter's end, a scene break, and
// the given number of JPEG pictures of the size a photograph in an e-book has (about 25 KB).
// 60 chapters and 120 pictures are 300 pages, about 76,000 words and 4 MB. It is the same
// bytes every time.
func SyntheticEPUB(chapters, images int) ([]byte, error) {
	rng := rand.New(rand.NewSource(Seed))
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
		s := b.String()
		return strings.ToUpper(s[:1]) + s[1:] + "."
	}
	type ent struct {
		name  string
		data  []byte
		store bool
	}
	var ents []ent
	var man, spine, nav strings.Builder
	perChapter := max(1, images/max(chapters, 1))
	img := 0
	for c := 0; c < chapters; c++ {
		var body, notes strings.Builder
		fmt.Fprintf(&body, `<section epub:type="chapter"><h2>Chapter %d</h2>`, c+1)
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
		page := `<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Chapter ` +
			fmt.Sprint(c+1) + `</title></head><body>` + body.String() + `</body></html>`
		ents = append(ents, ent{name: "OEBPS/" + name, data: []byte(page)})
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
		if err := jpeg.Encode(&b, im, &jpeg.Options{Quality: 80}); err != nil {
			return nil, err
		}
		ents = append(ents, ent{name: fmt.Sprintf("OEBPS/images/p%03d.jpg", i), data: b.Bytes(), store: true})
		fmt.Fprintf(&man, `<item id="p%d" href="images/p%03d.jpg" media-type="image/jpeg"/>`+"\n", i, i)
	}
	opf := `<?xml version="1.0" encoding="utf-8"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">urn:uuid:99999999-2222-3333-4444-555555555555</dc:identifier><dc:title>The Long Book</dc:title><dc:language>en</dc:language><dc:creator>Bench Author</dc:creator></metadata><manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>` +
		man.String() + `</manifest><spine>` + spine.String() + `</spine></package>`
	navDoc := `<?xml version="1.0" encoding="utf-8"?><html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>Contents</title></head><body><nav epub:type="toc"><ol>` + nav.String() + `</ol></nav></body></html>`
	all := append([]ent{{"mimetype", []byte("application/epub+zip"), true},
		{"META-INF/container.xml", []byte(`<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`), false},
		{"OEBPS/content.opf", []byte(opf), false}, {"OEBPS/nav.xhtml", []byte(navDoc), false}}, ents...)
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, e := range all {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.store {
			h.Method = zip.Store
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
