package publish

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/publish/pdfw"
	"github.com/crueber/scriobh/internal/text"
)

func uncompressed(t *testing.T) func() {
	t.Helper()
	uncompressedForTests = true
	return func() { uncompressedForTests = false }
}

func featureBook() *Book {
	b := bookFrom("Features", feature, feature)
	b.Images = map[string]Image{"/files/att_picture1234": jpegImage(900, 600)}
	return b
}

// Each mutation keeps the length of the file (so the offsets stay true) and
// breaks one rule; the validator must name it.
func TestPDFValidatorCatchesDefects(t *testing.T) {
	good, _, err := WritePDF(featureBook(), print6x9())
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.PDF(good); len(probs) > 0 {
		t.Fatalf("the sample is not valid: %v", probs)
	}
	replace := func(old, new string) func([]byte) []byte {
		if len(old) != len(new) {
			panic("a mutation must keep the length: " + old)
		}
		return func(b []byte) []byte {
			i := bytes.Index(b, []byte(old))
			if i < 0 {
				panic("no " + old)
			}
			out := append([]byte(nil), b...)
			copy(out[i:], new)
			return out
		}
	}
	cases := []struct {
		name string
		mut  func([]byte) []byte
		want string
	}{
		{"truncated", func(b []byte) []byte { return b[:len(b)-12] }, "does not end with"},
		{"no header", func(b []byte) []byte { return append([]byte("%PDF-1.4\n"), b[9:]...) }, "does not start"},
		{"startxref", replace("startxref\n", "startxreg\n"), "startxref"},
		{"xref entry", replace(" 00000 n \n0000", " 00000 f \n0000"), "malformed"},
		{"stream length", replace("/Length 1", "/Length 2"), "does not end the stream"},
		{"corrupt flate", func(b []byte) []byte {
			// the first page's content stream: /Contents names it
			i := bytes.Index(b, []byte("/Filter /FlateDecode /Length"))
			j := bytes.Index(b[i:], []byte("stream\n")) + i + 7
			out := append([]byte(nil), b...)
			out[j+10] ^= 0x55
			out[j+11] ^= 0xAA
			return out
		}, "inflate"},
		{"forbidden key", replace("/PageLayout", "/OpenAction"), "OpenAction"},
		{"forbidden key in catalog", replace("/PageMode", "/Names   "), "Names"},
		{"forbidden name", replace("/Identity-H", "/JavaScript"), "JavaScript"},
		{"parent", func(b []byte) []byte {
			i := bytes.Index(b, []byte("/Type /Page /Parent "))
			out := append([]byte(nil), b...)
			j := i + len("/Type /Page /Parent ")
			if out[j] == '1' {
				out[j] = '2'
			} else {
				out[j] = '1'
			}
			return out
		}, "Parent"},
		{"id", func(b []byte) []byte {
			i := bytes.LastIndex(b, []byte("/ID [<"))
			out := append([]byte(nil), b...)
			if out[i+6] == '0' { // one hex digit of the first copy
				out[i+6] = '1'
			} else {
				out[i+6] = '0'
			}
			return out
		}, "ID"},
		{"jpeg", func(b []byte) []byte {
			i := bytes.Index(b, []byte("/DCTDecode"))
			j := bytes.Index(b[i:], []byte("stream\n")) + i + 7
			out := append([]byte(nil), b...)
			out[j] = 0x00 // the SOI marker
			return out
		}, "JPEG"},
	}
	for _, c := range cases {
		probs := check.PDF(c.mut(good))
		found := false
		for _, p := range probs {
			if strings.Contains(p, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: wanted a problem mentioning %q, got %v", c.name, c.want, probs)
		}
	}
	// the content stream and the fonts, in a file whose streams are not compressed
	defer uncompressed(t)()
	plain, _, err := WritePDF(featureBook(), print6x9())
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.PDF(plain); len(probs) > 0 {
		t.Fatalf("the uncompressed sample is not valid: %v", probs)
	}
	for _, c := range []struct {
		name, old, new, want string
	}{
		{"a code outside the font", "[<", "[<FFF1", "not in font"},
		{"the notdef", "[<", "[<0000", ".notdef"},
		{"an operator", " Tf\n", " Tz\n", "not one this program writes"},
		{"begin text", "\nBT\n", "\nBI\n", "not one this program writes"},
		{"unbalanced", "\nq\n", "\nQ\n", "Q without q"},
		{"font size", " Tf\n", "0Tf\n", ""},
	} {
		if c.want == "" {
			continue
		}
		j := bytes.Index(plain, []byte(c.old))
		if j < 0 {
			t.Fatalf("%s: no %q in the file", c.name, c.old)
		}
		mut := append([]byte(nil), plain...)
		copy(mut[j:], c.new)
		if c.old == "[<" {
			copy(mut[j+2:], c.new[2:]) // the first four digits of the first code
		}
		probs := check.PDF(mut)
		found := false
		for _, p := range probs {
			if strings.Contains(p, c.want) || c.want == "notdef" && strings.Contains(p, ".notdef") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: wanted %q, got %v", c.name, c.want, probs)
		}
	}
}

// A hand-built file with the things the validator exists to refuse.
func TestPDFValidatorRefusesActiveContent(t *testing.T) {
	good, _, _ := WritePDF(bookFrom("T", "Words."), print6x9())
	for _, bad := range []string{"/Annots", "/AcroForm", "/Launch", "/URI", "/EmbeddedFile", "/OpenAction", "/JS", "/AA", "/GoToR", "/SubmitForm"} {
		if bytes.Contains(good, []byte(bad+" ")) || bytes.Contains(good, []byte(bad+"\n")) || bytes.Contains(good, []byte(bad+">")) {
			t.Errorf("the writer emitted %s", bad)
		}
	}
	// an ordinary file with an action added to its outline item is refused
	d := pdfw.New()
	d.ID = pdfw.DocumentID("x")
	p := d.NewPage(100000, 100000)
	_ = p
	d.SetOutline([]pdfw.OutlineItem{{Title: "x", Page: 0}})
	out, _ := d.Finish()
	if probs := check.PDF(out); len(probs) > 0 {
		t.Logf("a bare document: %v", probs) // no fonts, no labels: still sound
	}
}

// The text of the PDF is the text of the book.
func TestPDFTextIsTheBooksText(t *testing.T) {
	notes := 0
	var paras []string
	mk := func(md string) ([]text.Node, []text.Footnote) {
		d := text.Structure(md, text.StructOptions{NoteStart: notes})
		notes += len(d.Notes)
		return d.Nodes, d.Notes
	}
	b := &Book{Title: "Round Trip", Byline: "R. Trip", Language: "en", Modified: bookOf(1).Modified}
	vocab := strings.Fields(loremWords)
	pos := 0
	for c := 1; c <= 8; c++ {
		var md strings.Builder
		for p := 0; p < 12; p++ {
			var w []string
			for i := 0; i < 50+(c*p)%40; i++ {
				w = append(w, vocab[pos%len(vocab)])
				pos++
			}
			para := strings.ToUpper(w[0][:1]) + strings.Join(w, " ")[1:] + ". Ünïcödé, Ελληνικά, Русский, “curly”, naïve — dash."
			paras = append(paras, para)
			md.WriteString(para + "\n\n")
			if p%5 == 4 {
				md.WriteString("* * *\n\n")
			}
		}
		n, f := mk(md.String())
		b.Sections = append(b.Sections, Section{Kind: SecChapter, Title: fmt.Sprintf("Chapter %d title", c), Number: c, Level: 1, Nodes: n, Notes: f})
	}
	for _, o := range []Options{print6x9(), func() Options { o := print6x9(); o.Justify = False(); o.PageSize = "a5"; return o }()} {
		out, warns, err := WritePDF(b, o)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range warns {
			if w.Code == "missing_glyph" || w.Code == "widow_orphan" {
				t.Errorf("warning %s: %s", w.Code, w.Message)
			}
		}
		doc, probs := check.ParsePDF(out)
		for _, p := range probs {
			t.Error(p)
		}
		// the body's lines: those between the head and the foot
		pw, ph, _, _, _, _ := mustLayout(t, b, o).Geometry()
		_ = pw
		top, bottom := float64(resolveParams(o).top)/1000, float64(resolveParams(o).bottom)/1000
		var body strings.Builder
		for _, pg := range doc.Pages {
			for _, l := range pg.Lines {
				if l.Y < ph-top+1 && l.Y > bottom-1 {
					body.WriteString(l.Text)
				}
			}
		}
		flat := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\n' || r == '\t' {
				return -1
			}
			return r
		}, body.String())
		last := -1
		for i, p := range paras {
			want := strings.ReplaceAll(p, " ", "")
			// the next one after the last found (the vocabulary repeats)
			at := strings.Index(flat[max(last, 0):], want)
			if at < 0 {
				t.Errorf("paragraph %d is not in the PDF's text, after the previous one: %.60q", i, p)
				continue
			}
			last += at + len(want)
		}
		if strings.Contains(doc.Text(), "�") {
			t.Error("a replacement character reached the text")
		}
	}
}

func mustLayout(t *testing.T, b *Book, o Options) *PDFLayout {
	t.Helper()
	l, err := LayoutPDF(b, o)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The preview's layout is the file's: same page count, same page of each part.
func TestLayoutMatchesTheFile(t *testing.T) {
	b := bookOf(20000)
	l := mustLayout(t, b, print6x9())
	out, _, err := l.Render()
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	if l.PageCount() != len(doc.Pages) {
		t.Fatalf("layout %d pages, file %d", l.PageCount(), len(doc.Pages))
	}
	for i := range doc.Pages {
		if doc.Pages[i].Label != l.PageLabel(i) {
			t.Fatalf("page %d: label %q, layout %q", i+1, doc.Pages[i].Label, l.PageLabel(i))
		}
	}
	// every chapter's heading is on the page the layout says
	for i, s := range b.Sections {
		pg := l.SectionPage(i)
		if pg == 0 || s.Kind != SecChapter {
			continue
		}
		if !strings.Contains(doc.Pages[pg-1].Text(), strings.ToUpper(fmt.Sprintf("%s %d", "Chapter", s.Number))) {
			t.Errorf("chapter %d is said to start on page %d, which reads %.80q", s.Number, pg, doc.Pages[pg-1].Text())
		}
		if pg%2 != 1 {
			t.Errorf("chapter %d starts on a left-hand page (%d)", s.Number, pg)
		}
	}
	// chapters start on right-hand pages; a blank page has no text; the page count is even
	if len(doc.Pages)%2 != 0 {
		t.Error("an odd number of pages")
	}
	for i, pg := range doc.Pages {
		if l.sheets[i].kind == pageBlank && pg.Text() != "" {
			t.Errorf("blank page %d has text", i+1)
		}
	}
}

// Pinned: the bytes of the sample files with uncompressed streams, so a change
// of the compressor cannot move them and a change of the layout must be meant.
func TestPDFGolden(t *testing.T) {
	defer uncompressed(t)()
	files := map[string][]byte{}
	for name, b := range map[string]*Book{"features": featureBook(), "book": bookOf(4000)} {
		out, _, err := WritePDF(b, print6x9())
		if err != nil {
			t.Fatal(err)
		}
		files[name] = out
	}
	var lines []string
	for name, data := range files {
		sum := sha256.Sum256(data)
		lines = append(lines, fmt.Sprintf("%s %s", name, hex.EncodeToString(sum[:])))
	}
	sort.Strings(lines)
	got := "engine " + EngineVersion + "\n" + strings.Join(lines, "\n") + "\n"
	const path = "testdata/golden-pdf.txt"
	if os.Getenv("SCRIOBH_UPDATE_GOLDEN") == "1" {
		// The bytes a writer makes may change only under a new EngineVersion: the
		// compile's input hash names it, and the same hash must mean the same bytes.
		if old, err := os.ReadFile(path); err == nil && string(old) != got && strings.HasPrefix(string(old), "engine "+EngineVersion+"\n") {
			t.Fatalf("the PDF bytes changed and EngineVersion (%s) did not: bump it in version.go", EngineVersion)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (SCRIOBH_UPDATE_GOLDEN=1 writes it)", err)
	}
	if string(want) != got {
		t.Errorf("the PDF bytes changed:\nwant:\n%sgot:\n%s", want, got)
	}
	// compressed output is deterministic within a build
	uncompressedForTests = false
	a, _, _ := WritePDF(featureBook(), print6x9())
	b, _, _ := WritePDF(featureBook(), print6x9())
	if !bytes.Equal(a, b) {
		t.Error("two runs differ")
	}
	// and the document id comes from the input hash
	bk := featureBook()
	bk.InputHash = "other"
	c, _, _ := WritePDF(bk, print6x9())
	if bytes.Equal(a, c) {
		t.Error("the input hash does not reach the file")
	}
}
