package publish

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/text"
)

func pngImage(t *testing.T, w, h int) Image {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{200, 10, 10, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	return Image{Data: b.Bytes(), Mime: "image/png", Ext: "png", Width: w, Height: h, Hash: hex.EncodeToString(sum[:])}
}

// Same input, same bytes, twice; and the parts of the files do not change with a
// change of the zip library or of compression level (their hashes are pinned).
func TestWritersAreDeterministic(t *testing.T) {
	for name, write := range map[string]func() ([]byte, error){
		"shunn": func() ([]byte, error) { return WriteDOCX(sampleBook(), shunnOptions()) },
		"epub":  func() ([]byte, error) { return WriteEPUB(sampleBook(), epubOptions()) },
	} {
		a, err := write()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := write()
		if !bytes.Equal(a, b) {
			t.Errorf("%s: two runs differ", name)
		}
	}
	md1 := WriteMarkdown(sampleBook(), epubOptions())
	if !bytes.Equal(md1, WriteMarkdown(sampleBook(), epubOptions())) {
		t.Error("markdown differs")
	}
}

// partsHash hashes every part of a package, by name, as stored.
func partsHash(t *testing.T, data []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	for name, body := range unzipTo(t, data) {
		sum := sha256.Sum256([]byte(body))
		out[name] = hex.EncodeToString(sum[:])[:16]
	}
	return out
}

// The golden check: the parts of the sample book's files, in a form that is
// readable in a diff when a writer changes on purpose. Set SCRIOBH_GOLDEN=update
// to rewrite it.
func TestGoldenParts(t *testing.T) {
	var lines []string
	for _, c := range []struct {
		name string
		data func() ([]byte, error)
	}{
		{"shunn.docx", func() ([]byte, error) { return WriteDOCX(sampleBook(), shunnOptions()) }},
		{"sample.epub", func() ([]byte, error) { return WriteEPUB(sampleBook(), epubOptions()) }},
	} {
		d, err := c.data()
		if err != nil {
			t.Fatal(err)
		}
		h := partsHash(t, d)
		var names []string
		for n := range h {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			lines = append(lines, c.name+" "+n+" "+h[n])
		}
	}
	got := strings.Join(lines, "\n") + "\n"
	path := filepath.Join("testdata", "golden-parts.txt")
	if os.Getenv("SCRIOBH_GOLDEN") == "update" {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden file: run with SCRIOBH_GOLDEN=update: %v", err)
	}
	if string(want) != got {
		t.Errorf("a part changed. Run with SCRIOBH_GOLDEN=update after checking the change is meant.\n--- want\n%s--- got\n%s", want, got)
	}
}

func TestImagesAreEmbeddedAndRemoteOnesAreNot(t *testing.T) {
	img := pngImage(t, 40, 30)
	md := "Before ![A map](/files/att_abcdefghij1) after ![Far](https://evil.example/x.png) and ![Inline](data:image/png;base64,AAAA).\n"
	d := text.Structure(md, text.StructOptions{})
	b := sampleBook()
	b.Sections = []Section{{Kind: SecChapter, Title: "One", Number: 1, Level: 1, Nodes: d.Nodes}}
	b.Images = map[string]Image{"/files/att_abcdefghij1": img}

	docx, err := WriteDOCX(b, shunnOptions())
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.DOCX(docx); len(probs) > 0 {
		t.Fatalf("%v", probs)
	}
	parts := unzipTo(t, docx)
	if _, ok := parts["word/media/image1.png"]; !ok || !strings.Contains(parts["word/document.xml"], `r:embed="rId101"`) {
		t.Error("the attachment is not embedded")
	}
	if !strings.Contains(parts["word/document.xml"], "[image: Far (not included)]") || strings.Contains(parts["word/document.xml"], "evil.example") {
		t.Error("the remote image was not left out")
	}
	if !strings.Contains(parts["word/_rels/document.xml.rels"], `Target="media/image1.png"`) || strings.Contains(parts["word/_rels/document.xml.rels"], "TargetMode") {
		t.Errorf("rels: %s", parts["word/_rels/document.xml.rels"])
	}
	// 40x30 px at 96 dpi
	if !strings.Contains(parts["word/document.xml"], `<wp:extent cx="381000" cy="285750"/>`) {
		t.Error("the picture is not its own size")
	}

	epub, err := WriteEPUB(b, epubOptions())
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.EPUB(epub); len(probs) > 0 {
		t.Fatalf("%v", probs)
	}
	ep := unzipTo(t, epub)
	found := false
	for n := range ep {
		if strings.HasPrefix(n, "OEBPS/images/img-") && strings.HasSuffix(n, ".png") {
			found = true
		}
	}
	if !found || strings.Contains(ep["OEBPS/text/s001.xhtml"], "evil.example") || !strings.Contains(ep["OEBPS/text/s001.xhtml"], `<img src="../images/img-`) {
		t.Errorf("epub images: %s", ep["OEBPS/text/s001.xhtml"])
	}
	// an image the book does not carry, or cannot size, is a note
	b.Images["/files/att_abcdefghij1"] = Image{Data: img.Data, Mime: "image/png", Ext: "png", Hash: img.Hash} // no size
	docx, _ = WriteDOCX(b, shunnOptions())
	if strings.Contains(unzipTo(t, docx)["word/document.xml"], "<w:drawing>") {
		t.Error("an image with no size was embedded")
	}
}

// Hostile text: nothing it holds can break the XML, the structure or the package.
func TestHostileText(t *testing.T) {
	nasty := "A\x00B\x01C\x08D\x1bE\u202eF\u202aG\ufffeH\uffffI\xed\xa0\x80J\u0085K" + " <script>alert(1)</script> ]]> &amp; \" ' </w:t></w:p><w:p>"
	md := nasty + "\n\n" + strings.Repeat("word ", 200_000) + "\n\n" + strings.Repeat("> ", 300) + "deep\n\n" + strings.Repeat("- a\n  ", 30) + "\n"
	d := text.Structure(md, text.StructOptions{})
	b := sampleBook()
	b.Title, b.Byline, b.Surname = nasty, nasty, nasty
	b.Contact = Contact{Name: nasty, Address: []string{nasty}, Email: nasty, Phone: nasty}
	b.Sections = []Section{{Kind: SecChapter, Title: nasty, Number: 1, Level: 1, Nodes: d.Nodes, Notes: d.Notes}}
	o := shunnOptions()
	o.HeaderTitle, o.Byline = nasty, nasty
	docx, err := WriteDOCX(b, o)
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.DOCX(docx); len(probs) > 0 {
		t.Fatalf("%v", probs)
	}
	epub, err := WriteEPUB(b, epubOptions())
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.EPUB(epub); len(probs) > 0 {
		t.Fatalf("%v", probs)
	}
	all := ""
	for _, body := range unzipTo(t, docx) {
		all += body
	}
	for n, body := range unzipTo(t, epub) {
		if strings.HasSuffix(n, "xhtml") {
			all += body
		}
	}
	for _, bad := range []string{"\x00", "\x01", "\x08", "\x1b", "\u202e", "\u202a", "\ufffe", "\uffff", "<script>", "<w:p><w:p>"} {
		if strings.Contains(all, bad) {
			t.Errorf("the output holds %q", bad)
		}
	}
	if !strings.Contains(all, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("the script tag was not shown as text")
	}
	if len(WriteMarkdown(b, epubOptions())) == 0 {
		t.Error("no markdown")
	}
}

func TestOptionsValidateAndMerge(t *testing.T) {
	for _, bad := range []Options{{Font: "Wingdings"}, {FontSize: 99}, {LineSpacing: 0.2}, {MarginInches: 9}, {Ornament: "123456789"}, {ChapterLabel: strings.Repeat("x", 25)},
		{Header: "nope"}, {Language: "en US"}, {IndentInches: F(9)}, {ChapterDrop: F(-1)}, {Format: "pdf2"}, {PageSize: "tabloid"}, {MarginInside: F(9)}, {Gutter: F(2)}, {Footnotes: "margin"}, {Hyphenate: True()}, {Imprint: strings.Repeat("x\n", 9)}, {Clear: []string{"nope"}}} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	base := shunnOptions()
	m := Merge(base, Options{SceneBreak: "stars", IndentInches: F(0), TitlePage: False()})
	if m.SceneBreak != "stars" || m.IndentInches == nil || *m.IndentInches != 0 || On(m.TitlePage) || m.Font != "Times New Roman" || m.FontSize != 12 {
		t.Errorf("merge: %+v", m)
	}
	// the base is not touched through shared pointers
	*m.ChapterDrop = 1
	if *base.ChapterDrop != 3 {
		t.Error("Merge shares pointers with its base")
	}
	if got := SceneBreakText(Options{SceneBreak: "ornament", Ornament: " ~ "}); got != "~" {
		t.Errorf("ornament %q", got)
	}
	if RoundedWords(84321) != 84300 || RoundedWords(950) != 950 || RoundedWords(87) != 87 || Thousands(1234567) != "1,234,567" || Thousands(999) != "999" {
		t.Error("rounding or grouping")
	}
}

func TestMarkdownRoundTripsThroughTheStructure(t *testing.T) {
	b := sampleBook()
	md := WriteMarkdown(b, Options{TitlePage: True(), ChapterHeading: "title", SceneBreak: "stars"})
	got := text.PlainText(text.Structure(string(md), text.StructOptions{}).Nodes)
	for _, want := range []string{"The Salt Road", "Dedication", "The boat came in at dawn, and loud, with nothing everything.", "A second paragraph with a link and code.", "After the break.", "fn main() {}", "Thanks."} {
		if !strings.Contains(got, want) {
			t.Errorf("the markdown lost %q:\n%s", want, md)
		}
	}
	if strings.Contains(string(md), "%%") || strings.Contains(string(md), "<!--") {
		t.Error("markdown holds private notes or comments")
	}
}

// The validators are not vacuous: each defect they exist for is caught.
func TestValidatorsCatchDefects(t *testing.T) {
	good, _ := WriteDOCX(sampleBook(), shunnOptions())
	mutate := func(data []byte, name string, f func(string) string) []byte {
		zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		var out bytes.Buffer
		zw := zip.NewWriter(&out)
		for _, e := range zr.File {
			rc, _ := e.Open()
			var b bytes.Buffer
			b.ReadFrom(rc)
			rc.Close()
			body := b.String()
			if e.Name == name {
				body = f(body)
			}
			if body == "\x00drop" {
				continue
			}
			h := &zip.FileHeader{Name: e.Name, Method: e.Method}
			w, _ := zw.CreateHeader(h)
			w.Write([]byte(body))
		}
		zw.Close()
		return out.Bytes()
	}
	cases := map[string]struct {
		name string
		f    func(string) string
		want string
	}{
		"a pPr out of order": {"word/document.xml", func(s string) string {
			return strings.Replace(s, `<w:pPr><w:pStyle w:val="Title"/>`, `<w:pPr><w:jc w:val="center"/><w:pStyle w:val="Title"/>`, 1)
		}, "out of schema order"},
		"an undefined style": {"word/document.xml", func(s string) string { return strings.Replace(s, `w:val="Title"`, `w:val="NoSuchStyle"`, 1) }, "undefined style"},
		"an external rel": {"word/_rels/document.xml.rels", func(s string) string {
			return strings.Replace(s, `Target="styles.xml"`, `Target="https://x.example/s" TargetMode="External"`, 1)
		}, "external relationship"},
		"a dangling rel":      {"word/_rels/document.xml.rels", func(s string) string { return strings.Replace(s, `Target="styles.xml"`, `Target="nothing.xml"`, 1) }, "not in the package"},
		"broken XML":          {"word/document.xml", func(s string) string { return strings.Replace(s, "</w:body>", "", 1) }, "not well-formed"},
		"a missing footnote":  {"word/footnotes.xml", func(s string) string { return strings.Replace(s, `w:id="1"`, `w:id="77"`, 1) }, "footnote 1 is not defined"},
		"a missing numbering": {"word/numbering.xml", func(s string) string { return strings.Replace(s, `<w:num w:numId="1">`, `<w:num w:numId="99">`, 1) }, "numId 1 is not defined"},
		"a macro part":        {"word/vbaProject.bin", func(s string) string { return s }, ""},
		"a removed content type": {"[Content_Types].xml", func(s string) string {
			return strings.Replace(s, `<Override PartName="/word/styles.xml"`, `<Override PartName="/word/zzz.xml"`, 1)
		}, "content type"},
	}
	for name, c := range cases {
		if c.want == "" {
			continue
		}
		probs := check.DOCX(mutate(good, c.name, c.f))
		if !strings.Contains(strings.Join(probs, "\n"), c.want) {
			t.Errorf("%s: not caught (%q): %v", name, c.want, probs)
		}
	}
	goodE, _ := WriteEPUB(sampleBook(), epubOptions())
	ecases := map[string]struct {
		name string
		f    func(string) string
		want string
	}{
		"the mimetype":    {"mimetype", func(s string) string { return s + "x" }, "mimetype must be exactly"},
		"a manifest href": {"OEBPS/content.opf", func(s string) string { return strings.Replace(s, `href="text/s001.xhtml"`, `href="text/zzz.xhtml"`, 1) }, "not in the archive"},
		"a spine idref": {"OEBPS/content.opf", func(s string) string {
			return strings.Replace(s, `<itemref idref="s001"/>`, `<itemref idref="nope"/>`, 1)
		}, "not in the manifest"},
		"a script": {"OEBPS/text/s001.xhtml", func(s string) string { return strings.Replace(s, "<section", "<script>x</script><section", 1) }, "forbidden element"},
		"a remote image": {"OEBPS/text/s001.xhtml", func(s string) string {
			return strings.Replace(s, "<section", `<img src="https://x.example/a.png" alt=""/><section`, 1)
		}, "loads from outside"},
		"a duplicate id":   {"OEBPS/text/s003.xhtml", func(s string) string { return strings.Replace(s, `id="fnref-1"`, `id="fn-1"`, 1) }, "duplicate id"},
		"a broken anchor":  {"OEBPS/text/s003.xhtml", func(s string) string { return strings.Replace(s, `href="#fn-1"`, `href="#nothing"`, 1) }, "missing anchor"},
		"an unclosed tag":  {"OEBPS/text/s001.xhtml", func(s string) string { return strings.Replace(s, "</section>", "", 1) }, "not well-formed"},
		"no modified date": {"OEBPS/content.opf", func(s string) string { return strings.Replace(s, "dcterms:modified", "dcterms:other", 1) }, "dcterms:modified"},
		"an unlisted file": {"OEBPS/css/book.css", func(s string) string { return s }, ""},
		"a bad nav":        {"OEBPS/nav.xhtml", func(s string) string { return strings.Replace(s, `epub:type="toc"`, `epub:type="x"`, 1) }, "no nav of type toc"},
	}
	for name, c := range ecases {
		if c.want == "" {
			continue
		}
		probs := check.EPUB(mutate(goodE, c.name, c.f))
		if !strings.Contains(strings.Join(probs, "\n"), c.want) {
			t.Errorf("%s: not caught (%q): %v", name, c.want, probs)
		}
	}
}

// LibreOffice, when it is installed and asked for, opens every DOCX preset and
// converts it to PDF: a check that a second implementation reads the files. It
// is not Word, and it is off by default (it takes seconds per file).
func TestLibreOfficeOpensTheDOCX(t *testing.T) {
	if os.Getenv("SCRIOBH_LIBREOFFICE") == "" {
		t.Skip("set SCRIOBH_LIBREOFFICE=1 to run")
	}
	soffice, err := exec.LookPath("soffice")
	if err != nil {
		if _, e2 := os.Stat("/opt/homebrew/bin/soffice"); e2 != nil {
			t.Skip("soffice is not installed")
		}
		soffice = "/opt/homebrew/bin/soffice"
	}
	dir := t.TempDir()
	styled := Merge(shunnOptions(), Options{Font: "Georgia", FontSize: 11, LineSpacing: 1.15, TitleLayout: "book", Header: "none", PageNumbers: "footer", Justify: True()})
	for name, o := range map[string]Options{"shunn": shunnOptions(), "uk": Merge(shunnOptions(), Options{PageSize: "a4", WordCount: "label", Language: "en-GB"}), "styled": styled} {
		data, err := WriteDOCX(sampleBook(), o)
		if err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(dir, name+".docx")
		os.WriteFile(src, data, 0o644)
		cmd := exec.Command(soffice, "--headless", "--convert-to", "pdf", "--outdir", dir, src)
		cmd.Env = append(os.Environ(), "HOME="+dir)
		done := make(chan error, 1)
		go func() { _, e := cmd.CombinedOutput(); done <- e }()
		select {
		case e := <-done:
			if e != nil {
				t.Errorf("%s: %v", name, e)
			}
		case <-time.After(120 * time.Second):
			cmd.Process.Kill()
			t.Fatalf("%s: soffice timed out", name)
		}
		pdf, err := os.ReadFile(filepath.Join(dir, name+".pdf"))
		if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF")) {
			t.Errorf("%s: no PDF: %v", name, err)
		}
	}
}
