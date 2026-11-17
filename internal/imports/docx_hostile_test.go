package imports

import (
	"archive/zip"
	"bytes"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

const hwns = `xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`

func hdocx(parts map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range parts {
		w, _ := zw.Create(name)
		w.Write([]byte(b))
	}
	zw.Close()
	return buf.Bytes()
}

func hbody(inner string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><w:document ` + hwns + `><w:body>` + inner + `</w:body></w:document>`
}

func hnotes(inner string) string {
	return `<?xml version="1.0"?><w:footnotes ` + hwns + `>` + inner + `</w:footnotes>`
}

func hp(t string) string {
	return `<w:p><w:r><w:t xml:space="preserve">` + strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(t) + `</w:t></w:r></w:p>`
}

const hstyles = `<?xml version="1.0"?><w:styles ` + hwns + `><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>`

func convert(t *testing.T, parts map[string]string) []File {
	t.Helper()
	fs, _, err := docxToFiles(hdocx(parts), "x.docx", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

// kinds lists the block kinds a Markdown text makes.
func kinds(md string) (ks []text.NodeKind, d text.Doc) {
	d = text.Structure(md, text.StructOptions{})
	for _, n := range d.Nodes {
		ks = append(ks, n.Kind)
	}
	return
}

// A part with a million tiny pieces, or forty megabytes in one run, is refused
// quickly and without building it.
func TestDocxTextBombsAreRefusedCheaply(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<w:p><w:r><w:t>`)
	for i := 0; i < 320_000; i++ {
		b.WriteString(`ab<!---->`)
	}
	b.WriteString(`</w:t></w:r></w:p>`)
	data := hdocx(map[string]string{"word/document.xml": hbody(b.String())})
	start := time.Now()
	_, _, err := docxToFiles(data, "x.docx", "", "")
	took := time.Since(start)
	if err == nil {
		t.Errorf("pieces: %v, err %v (a %d byte file)", took, err, len(data))
	}
	t.Run("pieces time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second*hostileFactor {
			t.Errorf("pieces: %v, err %v (a %d byte file)", took, err, len(data))
		}
	})

	big := strings.Repeat("abcdefgh ", (40<<20)/9)
	data = hdocx(map[string]string{"word/document.xml": hbody(`<w:p><w:r><w:t>` + big + `</w:t></w:r></w:p>`)})
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	start = time.Now()
	_, _, err = docxToFiles(data, "x.docx", "", "")
	runtime.ReadMemStats(&m1)
	alloc := (m1.TotalAlloc - m0.TotalAlloc) >> 20
	t.Logf("40 MB run: %v, %d MB allocated, err %v", time.Since(start), alloc, err)
	// the race detector more than doubles what Go allocates and keeps
	if err == nil || alloc > uint64(300+30*(hostileFactor-1)) {
		t.Errorf("a 40 MB paragraph: err %v, %d MB allocated", err, alloc)
	}
}

func TestDocxManyNotesIsLinear(t *testing.T) {
	var nb, db strings.Builder
	db.WriteString(`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Chapter</w:t></w:r></w:p>`)
	pad := strings.Repeat("lorem ipsum ", 100)
	const n = 16000
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&nb, `<w:footnote w:id="%d"><w:p><w:r><w:t>n</w:t></w:r></w:p></w:footnote>`, i)
		fmt.Fprintf(&db, `<w:p><w:r><w:t>%s</w:t></w:r><w:r><w:footnoteReference w:id="%d"/></w:r></w:p>`, pad, i)
	}
	data := hdocx(map[string]string{"word/document.xml": hbody(db.String()), "word/footnotes.xml": hnotes(nb.String()), "word/styles.xml": hstyles})
	start := time.Now()
	fs, _, err := docxToFiles(data, "x.docx", "", "")
	took := time.Since(start)
	t.Logf("%d notes: %v, err %v", n, took, err)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 8*time.Second*hostileFactor {
			t.Errorf("%d notes took %v", n, took)
		}
	})
	if err == nil {
		got := string(fs[0].Data)
		if !strings.Contains(got, "[^16000]: n") || strings.Count(got, "[^1]") < 2 {
			t.Errorf("notes were not relabelled")
		}
	}
}

// Footnote ids are plain numbers: nothing a document puts in an id reaches the text.
func TestDocxNoteIDsCannotInject(t *testing.T) {
	id := `x]%%hidden%%&lt;scriobh-ghost&gt;[[Wiki]]`
	notes := hnotes(`<w:footnote w:id="` + id + `"><w:p><w:r><w:t>Note body</w:t></w:r></w:p></w:footnote><w:footnote w:id="7"><w:p><w:r><w:t>Seven</w:t></w:r><w:r><w:footnoteReference w:id="7"/></w:r></w:p></w:footnote><w:footnote w:id="9"><w:p><w:r><w:t># Nine</w:t></w:r></w:p></w:footnote>`)
	doc := hbody(`<w:p><w:r><w:t>Para</w:t></w:r><w:r><w:footnoteReference w:id="` + id + `"/></w:r></w:p>` +
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>cell</w:t></w:r><w:r><w:footnoteReference w:id="7"/></w:r></w:p></w:tc></w:tr></w:tbl>` +
		hp("Literal [^7] text") + `<w:p><w:r><w:footnoteReference w:id="9"/></w:r></w:p>`)
	fs := convert(t, map[string]string{"word/document.xml": doc, "word/footnotes.xml": notes})
	got := string(fs[0].Data)
	if strings.Contains(got, "hidden") || strings.Contains(got, "Wiki") || strings.Contains(got, "scriobh-ghost") {
		t.Errorf("a note id reached the text:\n%s", got)
	}
	// the note referenced only from a table cell has its definition
	if !strings.Contains(got, "[^1]: Seven") {
		t.Errorf("the table's note was lost:\n%s", got)
	}
	// a note inside a note is dropped; a note that starts with # is a note
	if strings.Contains(got, "[^2]: Seven [^") || !strings.Contains(got, `[^2]: \# Nine`) {
		t.Errorf("notes:\n%s", got)
	}
	// the literal bracket in the words stays text
	d := text.Structure(got, text.StructOptions{})
	if !strings.Contains(text.PlainText(d.Nodes), "Literal [^7] text") {
		t.Errorf("the literal text changed: %q", text.PlainText(d.Nodes))
	}
}

// A link address cannot end its link, start a paragraph, a wiki link or a private note.
func TestDocxLinkTargetsCannotInject(t *testing.T) {
	rels := `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://e.com/a%%b&#10;&#10;# Inject [[Wiki]] ) (x" TargetMode="External"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://ok.example/a%20b?q=1" TargetMode="External"/></Relationships>`
	doc := hbody(`<w:p><w:hyperlink r:id="rId1"><w:r><w:t>link</w:t></w:r></w:hyperlink><w:r><w:t xml:space="preserve"> secret%%</w:t></w:r></w:p>` +
		`<w:p><w:hyperlink r:id="rId2"><w:r><w:t>fine</w:t></w:r></w:hyperlink></w:p>`)
	fs := convert(t, map[string]string{"word/document.xml": doc, "word/_rels/document.xml.rels": rels})
	got := string(fs[0].Data)
	ks, d := kinds(got)
	for _, k := range ks {
		if k != text.NPara {
			t.Errorf("a link address made a block (kind %d):\n%q", k, got)
		}
	}
	plain := text.PlainText(d.Nodes)
	if !strings.Contains(plain, "secret%%") || strings.Contains(plain, "Inject") && !strings.Contains(plain, "link") {
		t.Errorf("plain %q from %q", plain, got)
	}
	if strings.Contains(got, "[[Wiki]]") || len(d.Nodes) != 2 {
		t.Errorf("injection: %q (%d blocks)", got, len(d.Nodes))
	}
	if !strings.Contains(got, "(https://ok.example/a%20b?q=1)") {
		t.Errorf("a good address was changed: %q", got)
	}
}

// Leading white space does not make a line a heading, a list item, code or a setext underline.
func TestDocxLeadingSpaceCannotBecomeBlocks(t *testing.T) {
	doc := hbody(hp(" # Injected heading") + hp("  - injected item") + hp("    indented code") + hp("   1. ordered") +
		`<w:p><w:r><w:t>Line one</w:t></w:r><w:r><w:br/></w:r><w:r><w:t xml:space="preserve"> ===</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>Line two</w:t></w:r><w:r><w:br/></w:r><w:r><w:t xml:space="preserve">    # in</w:t></w:r></w:p>`)
	fs := convert(t, map[string]string{"word/document.xml": doc})
	ks, d := kinds(string(fs[0].Data))
	for i, k := range ks {
		if k != text.NPara {
			t.Errorf("block %d is kind %d in %q", i, k, fs[0].Data)
		}
	}
	plain := text.PlainText(d.Nodes)
	for _, want := range []string{"# Injected heading", "- injected item", "indented code", "1. ordered", "===", "# in"} {
		if !strings.Contains(plain, want) {
			t.Errorf("%q missing from %q", want, plain)
		}
	}
}

func TestDocxEscapesInsideHeadingsListsAndTables(t *testing.T) {
	doc := hbody(`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Head %%priv%% [[Wiki]] &lt;scriobh-ghost&gt;</w:t></w:r></w:p>` +
		`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>a|b</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>c</w:t></w:r></w:p></w:tc></w:tr><w:tr><w:tc><w:p><w:r><w:t>d</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>e</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
		`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="9"/></w:numPr></w:pPr><w:r><w:t># not heading</w:t></w:r></w:p>`)
	fs := convert(t, map[string]string{"word/document.xml": doc, "word/styles.xml": hstyles})
	got := string(fs[0].Data)
	d := text.Structure(got, text.StructOptions{})
	var table *text.Node
	var list *text.Node
	for i := range d.Nodes {
		switch d.Nodes[i].Kind {
		case text.NTable:
			table = &d.Nodes[i]
		case text.NList:
			list = &d.Nodes[i]
		}
	}
	if table == nil || len(table.Rows) != 2 || len(table.Rows[0].Cells) != 2 || text.InlinePlain(table.Rows[0].Cells[0].Inl) != "a|b" {
		t.Errorf("the table: %q", got)
	}
	if list == nil || len(list.Items) != 1 || list.Items[0][0].Kind != text.NPara || text.PlainText(list.Items[0]) != "# not heading" {
		t.Errorf("the list item became a block: %q", got)
	}
	if plain := text.PlainText(d.Nodes); strings.Contains(plain, "priv") && !strings.Contains(plain, "%%priv%%") {
		t.Errorf("a %%%% in a heading was taken for a private note: %q", plain)
	}
}

// A negative list level does not panic, here or in the command line.
func TestDocxNegativeListLevel(t *testing.T) {
	doc := hbody(`<w:p><w:pPr><w:numPr><w:ilvl w:val="-3"/><w:numId w:val="5"/></w:numPr></w:pPr><w:r><w:t>item</w:t></w:r></w:p>`)
	fs := convert(t, map[string]string{"word/document.xml": doc})
	if !strings.Contains(string(fs[0].Data), "- item") {
		t.Errorf("%q", fs[0].Data)
	}
}
