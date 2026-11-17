package publish

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

func epubOptions() Options {
	return Options{Format: "epub", Ghosts: "omit", FrontMatter: True(), BackMatter: True(), TitlePage: True(), PartPages: True(),
		ChapterHeading: "number-title", ChapterLabel: "Chapter", SceneBreak: "stars", Cover: True(), IndentInches: F(1.5), Justify: False()}
}

func TestEPUBIsValid(t *testing.T) {
	data, err := WriteEPUB(sampleBook(), epubOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range check.EPUB(data) {
		t.Error(p)
	}
	files := unzipTo(t, data)
	for _, want := range []string{"mimetype", "META-INF/container.xml", "OEBPS/content.opf", "OEBPS/nav.xhtml", "OEBPS/toc.ncx", "OEBPS/css/book.css", "OEBPS/images/cover.svg", "OEBPS/text/s003.xhtml"} {
		if _, ok := files[want]; !ok {
			t.Errorf("missing %s", want)
		}
	}
	ch := files["OEBPS/text/s003.xhtml"]
	for _, want := range []string{`<p class="first">The boat came in at <em>dawn</em>`, `<p class="scenebreak">* * *</p>`, `epub:type="noteref"`, `<aside epub:type="footnote" id="fn-1">`,
		`<blockquote>`, `<table>`, `<th>a</th>`, `<th class="ac">b</th>`, `<pre><code>fn main() {}</code></pre>`, `<del>nothing</del>`, `<a href="https://example.com/a?b=1&amp;c=2">link</a>`, `<ol start="3">`} {
		if !strings.Contains(ch, want) {
			t.Errorf("chapter lacks %s:\n%s", want, ch)
		}
	}
	// raw HTML in the text is text
	if raw := files["OEBPS/text/s004.xhtml"]; !strings.Contains(raw, "&lt;b&gt;raw&lt;/b&gt;") || strings.Contains(raw, "<b>") {
		t.Errorf("raw html was not escaped: %s", raw)
	}
	if dir := os.Getenv("SCRIOBH_KEEP"); dir != "" {
		os.WriteFile(filepath.Join(dir, "sample.epub"), data, 0o644)
	}
}

// xmllint, when it is installed, agrees that every XML part is well-formed.
func TestEPUBPartsAreWellFormedByXmllint(t *testing.T) {
	lint, err := exec.LookPath("xmllint")
	if err != nil {
		t.Skip("xmllint is not installed")
	}
	data, _ := WriteEPUB(sampleBook(), epubOptions())
	docx, _ := WriteDOCX(sampleBook(), shunnOptions())
	dir := t.TempDir()
	n := 0
	for _, zipData := range [][]byte{data, docx} {
		for name, body := range unzipTo(t, zipData) {
			if !(strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".xhtml") || strings.HasSuffix(name, ".opf") || strings.HasSuffix(name, ".ncx") || strings.HasSuffix(name, ".rels") || strings.HasSuffix(name, ".svg")) {
				continue
			}
			n++
			p := filepath.Join(dir, strings.NewReplacer("/", "_", "[", "", "]", "").Replace(name)+itoa(n))
			os.WriteFile(p, []byte(body), 0o644)
			if out, err := exec.Command(lint, "--noout", p).CombinedOutput(); err != nil {
				t.Errorf("%s: %v %s", name, err, out)
			}
		}
	}
}

// The raw title of a section is written into the file only where the file prints it: a chapter whose title the writer
// chose not to print ("Chapter 1" only) must not have it in any file of the book.
func TestEPUBDoesNotWriteATitleItDoesNotPrint(t *testing.T) {
	all := func(o Options) string {
		data, err := WriteEPUB(sampleBook(), o)
		if err != nil {
			t.Fatal(err)
		}
		zr, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		var sb strings.Builder
		for _, f := range zr.File {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			sb.Write(b)
		}
		return sb.String()
	}
	o := epubOptions()
	if s := all(o); !strings.Contains(s, `data-title="The Arrival"`) {
		t.Error("a printed title is not marked")
	}
	o.ChapterHeading = "number"
	s := all(o)
	if strings.Contains(s, "The Arrival") || strings.Contains(s, "data-title=\"The Arrival\"") {
		t.Error("a title the heading does not print is in the book")
	}
	if !strings.Contains(s, `data-title="Dedication"`) || !strings.Contains(s, `data-title="Part One"`) {
		t.Error("the titles of front matter and parts are printed and marked")
	}
	o.ChapterHeading = "none" // the contents show the title when the heading is empty
	if s := all(o); !strings.Contains(s, `data-title="The Arrival"`) {
		t.Error("a title the contents print is not marked")
	}
}
