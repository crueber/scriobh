package bench

import (
	"archive/zip"
	"bytes"
	"fmt"
	"math/rand"
	"strings"
)

// SyntheticDocx makes a Word document of the given number of chapters, each a Heading 1 and paragraphs of about wordsPerChapter
// words, text only. 60 chapters of 2,500 words are 150,000 words and about 180 KB. It is the same bytes every time.
func SyntheticDocx(chapters, wordsPerChapter int) ([]byte, error) {
	return SyntheticDocxKind("h1", chapters, wordsPerChapter)
}

// SyntheticDocxKind is SyntheticDocx with the chapters marked one of three ways: "h1" (Heading 1), "labels" (a bold, centred
// "Chapter N" line after a page break, with no heading style at all) or "none" (nothing marks a chapter: one long text).
func SyntheticDocxKind(kind string, chapters, wordsPerChapter int) ([]byte, error) {
	rng := rand.New(rand.NewSource(Seed))
	words := strings.Fields("the marsh water road wall rain dusk dawn bus crossroads lamp quay harbour tide salt reed heron mud boat rope lantern shore")
	var body strings.Builder
	for c := 1; c <= chapters; c++ {
		switch kind {
		case "labels":
			fmt.Fprintf(&body, `<w:p><w:pPr><w:pageBreakBefore/><w:jc w:val="center"/></w:pPr><w:r><w:rPr><w:b/></w:rPr><w:t>Chapter %d</w:t></w:r></w:p>`, c)
		case "none":
		default:
			fmt.Fprintf(&body, `<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Chapter %d</w:t></w:r></w:p>`, c)
		}
		for left := wordsPerChapter; left > 0; {
			n := min(left, 60+rng.Intn(60))
			left -= n
			body.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
			for i := 0; i < n; i++ {
				if i > 0 {
					body.WriteByte(' ')
				}
				body.WriteString(words[rng.Intn(len(words))])
			}
			body.WriteString(`.</w:t></w:r></w:p>`)
		}
	}
	const ns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"`
	parts := []struct{ name, data string }{
		{"[Content_Types].xml", `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/></Types>`},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8"?><w:document ` + ns + `><w:body>` + body.String() + `</w:body></w:document>`},
		{"word/styles.xml", `<?xml version="1.0"?><w:styles ` + ns + `><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>`},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write([]byte(p.data)); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
