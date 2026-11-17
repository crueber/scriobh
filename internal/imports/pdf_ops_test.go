package imports_test

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
	"github.com/crueber/scriobh/internal/testutil"
)

// tinyPDF is a three-page book with an outline, written by hand.
func tinyPDF(t *testing.T, extraCatalog string, opt pdftest.Opt) []byte {
	t.Helper()
	return tinyPDFBuild(t, extraCatalog, opt, "ordinary text")
}

func tinyPDFWith(t *testing.T, words string) []byte {
	t.Helper()
	return tinyPDFBuild(t, "", pdftest.Opt{}, words)
}

func tinyPDFBuild(t *testing.T, extraCatalog string, opt pdftest.Opt, words string) []byte {
	t.Helper()
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	var pgs []int
	for i, c := range []string{"One", "Two"} {
		var b strings.Builder
		fmt.Fprintf(&b, "BT /F1 20 Tf 72 700 Td (Chapter %s) Tj ET\n", c)
		for l := 0; l < 6; l++ {
			fmt.Fprintf(&b, "BT /F1 11 Tf 72 %d Td (Line %d of chapter %d is some %s) Tj ET\n", 660-14*l, l, i+1, words)
		}
		pgs = append(pgs, d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), b.String(), "Flate"))
	}
	root := d.Pages(pgs, extraCatalog)
	return d.Write(root, opt)
}

func TestPdfRefusalsAreSaidInPlainWords(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		pdf  []byte
		in   imports.PreviewIn
		want string
	}{
		{"not a pdf", []byte("PK\x03\x04 this is a zip"), imports.PreviewIn{}, "not a PDF"},
		{"empty garbage", bytes.Repeat([]byte("%PDF-1.4\n"), 4), imports.PreviewIn{}, "damaged"},
	}
	// scanned: pictures only
	d := pdftest.New()
	img := d.Stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /DCTDecode", []byte{0xff, 0xd8}, "")
	var pgs []int
	for i := 0; i < 4; i++ {
		pgs = append(pgs, d.Page(612, 792, fmt.Sprintf("/XObject << /Im1 %d 0 R >>", img), "q 612 0 0 792 0 0 cm /Im1 Do Q", ""))
	}
	cases = append(cases, struct {
		name string
		pdf  []byte
		in   imports.PreviewIn
		want string
	}{"scanned", d.Write(d.Pages(pgs, ""), pdftest.Opt{}), imports.PreviewIn{}, "no text layer"})
	// encrypted
	d2 := pdftest.New()
	f2 := d2.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	pg2 := d2.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f2), "BT /F1 12 Tf 72 700 Td (Secret) Tj ET", "")
	enc := d2.Add("<< /Filter /Standard /V 1 /R 2 /O (aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa) /U (bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb) /P -4 >>")
	cases = append(cases, struct {
		name string
		pdf  []byte
		in   imports.PreviewIn
		want string
	}{"encrypted", d2.Write(d2.Pages([]int{pg2}, ""), pdftest.Opt{Trailer: fmt.Sprintf("/Encrypt %d 0 R", enc)}), imports.PreviewIn{}, "encrypted"})
	for _, c := range cases {
		in := c.in
		in.Pdf = c.pdf
		_, err := e.call(e.w, "import.preview", in)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if oe := ops.AsError(err); oe.Code != ops.CodeInvalid {
			t.Errorf("%s: code %s", c.name, oe.Code)
		}
	}
	// two sources at once
	ok := tinyPDF(t, "", pdftest.Opt{})
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Pdf: ok, Docx: []byte("x")}); err == nil || !strings.Contains(err.Error(), "one of them") {
		t.Errorf("pdf and docx: %v", err)
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Pdf: ok, Files: files(map[string]string{"a.md": "A."})}); err == nil {
		t.Errorf("pdf and files accepted")
	}
}

func TestPdfProvenanceEditsAppendAndTheToken(t *testing.T) {
	e := newEnv(t)
	data := tinyPDF(t, "", pdftest.Opt{})
	pv := e.previewPdf(data, imports.PreviewIn{})
	if pv.Layout != "pdf" || len(pv.Nodes) != 2 || pv.Nodes[0].Title != "Chapter One" || pv.Words < 50 {
		t.Fatalf("preview: %s %+v", nodeTitles(pv), pv.Project)
	}
	rep, _ := pv.Report["pdf"].(map[string]any)
	if rep == nil || rep["report"] == nil {
		t.Fatalf("no fidelity report: %v", pv.Report)
	}
	// another file's token does not apply
	other := tinyPDFWith(t, "Different words")
	if _, err := e.call(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Pdf: other, Name: "book.pdf"}); err == nil {
		t.Error("a token of one PDF applied to another")
	}
	// edits: rename one, skip the other
	title := "The Opening"
	ap := e.applyPdf(data, pv, imports.ApplyIn{Edits: []imports.Edit{{Key: pv.Nodes[0].Key, Title: &title}, {Key: pv.Nodes[1].Key, Skip: true}}})
	if ap.Documents != 1 {
		t.Errorf("documents %d", ap.Documents)
	}
	var kind, hash, meta, path string
	if err := e.d.Read().QueryRow(`SELECT kind, source_hash, metadata_json, source_path FROM project_sources WHERE project_id = ?`, ap.ProjectID).Scan(&kind, &hash, &meta, &path); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if kind != "pdf" || hash != hex.EncodeToString(sum[:]) || path != "book.pdf" || !strings.Contains(meta, `"pdf"`) || !strings.Contains(meta, `"confidence"`) || len(meta) > 64<<10 {
		t.Errorf("provenance: %s %s %s %d bytes", kind, hash, path, len(meta))
	}
	// the same file added to the project again
	apv := e.must(e.w, "import.append_preview", imports.AppendPreviewIn{ProjectID: ap.ProjectID, Pdf: data, Name: "book.pdf"}).(imports.PreviewOut)
	out := e.must(e.w, "import.append", imports.AppendIn{ProjectID: ap.ProjectID, Token: apv.Token, Pdf: data, Name: "book.pdf"}).(imports.ApplyOut)
	if out.Documents != 2 {
		t.Errorf("appended %d documents", out.Documents)
	}
}

func TestPdfActiveContentIsReportedAndNeverKept(t *testing.T) {
	e := newEnv(t)
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	js := d.Add("<< /S /JavaScript /JS (app.alert\\(\\(JSMARK\\)\\)) >>")
	ef := d.Add("<< /Type /Filespec /F (EMBEDMARK.exe) >>")
	var b strings.Builder
	b.WriteString("BT /F1 20 Tf 72 700 Td (Chapter One) Tj ET\n")
	for l := 0; l < 6; l++ {
		fmt.Fprintf(&b, "BT /F1 11 Tf 72 %d Td (Line %d of ordinary text) Tj ET\n", 660-14*l, l)
	}
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), b.String(), "")
	root := d.Pages([]int{pg}, fmt.Sprintf("/OpenAction %d 0 R /Names << /JavaScript << /Names [(a) %d 0 R] >> /EmbeddedFiles << /Names [(EMBEDMARK.exe) %d 0 R] >> >>", js, js, ef))
	data := d.Write(root, pdftest.Opt{})
	pv := e.previewPdf(data, imports.PreviewIn{})
	w := strings.Join(pv.Warnings, "\n")
	if !strings.Contains(w, "JavaScript") || !strings.Contains(w, "attached to the PDF were not extracted") {
		t.Errorf("warnings: %s", w)
	}
	ap := e.applyPdf(data, pv, imports.ApplyIn{})
	var n int
	e.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE project_id = ? AND (content LIKE '%JSMARK%' OR content LIKE '%EMBEDMARK%' OR title LIKE '%MARK%')`, ap.ProjectID).Scan(&n)
	var meta string
	e.d.Read().QueryRow(`SELECT metadata_json FROM project_sources WHERE project_id = ?`, ap.ProjectID).Scan(&meta)
	if n != 0 || strings.Contains(meta, "JSMARK") || strings.Contains(meta, "EMBEDMARK") {
		t.Errorf("active content reached the project (%d documents, meta %q)", n, meta)
	}
}

func TestPdfHostileFilesAreBoundedThroughTheOp(t *testing.T) {
	e := newEnv(t)
	// a stream that inflates to 300 MB
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	zeros := make([]byte, 1<<20)
	for i := 0; i < 300; i++ {
		zw.Write(zeros)
	}
	zw.Close()
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	bomb := d.Alloc()
	d.Raw(bomb, append([]byte(fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n", buf.Len())), append(buf.Bytes(), []byte("\nendstream")...)...))
	var pgs []int
	for i := 0; i < 3; i++ {
		var c strings.Builder
		for l := 0; l < 8; l++ {
			fmt.Fprintf(&c, "BT /F1 11 Tf 72 %d Td (Ordinary line %d on page %d of the book) Tj ET\n", 700-14*l, l, i)
		}
		pgs = append(pgs, d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), c.String(), ""))
	}
	pgBomb := d.Add(fmt.Sprintf("<< /Type /Page /Parent 0 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", f, bomb))
	pgs = append(pgs, pgBomb)
	data := d.Write(d.Pages(pgs, ""), pdftest.Opt{})
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	pv := e.previewPdf(data, imports.PreviewIn{})
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	mb := (after.TotalAlloc - before.TotalAlloc) >> 20
	t.Logf("a 300 MB stream in %d KB: preview allocated %d MB in %v", len(data)>>10, mb, took)
	if mb > 80*uint64(timingFactor)/uint64(max(1, timingFactor/3)) {
		t.Errorf("allocated %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second*timingFactor {
			t.Errorf("allocated %d MB in %v", mb, took)
		}
	})
	if !strings.Contains(strings.Join(pv.Warnings, "\n"), "A limit was reached on page(s) 4") {
		t.Errorf("the refused stream was not reported: %v", pv.Warnings)
	}
	// an unbounded number of tiny files cannot hold the reader: a huge page tree
	var kids strings.Builder
	d2 := pdftest.New()
	f2 := d2.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	pg := d2.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f2), "BT /F1 12 Tf 72 700 Td (A page of text that repeats across the file.) Tj ET", "")
	for i := 0; i < 60_000; i++ {
		fmt.Fprintf(&kids, "%d 0 R ", pg)
	}
	pn := d2.Add(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count 60000 >>", kids.String()))
	cat := d2.Add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pn))
	start = time.Now()
	pv2, err := e.call(e.w, "import.preview", imports.PreviewIn{Pdf: d2.Write(cat, pdftest.Opt{})})
	if err != nil {
		t.Fatal(err)
	}
	took = time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 20*time.Second*timingFactor {
			t.Errorf("a huge page tree took %v", took)
		}
	})
	if w := strings.Join(pv2.(imports.PreviewOut).Warnings, "\n"); !strings.Contains(w, "import limit") {
		t.Errorf("page tree cap not reported: %s", w)
	}
}
