package imports_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/testutil"
)

func (e *env) previewPdf(data []byte, in imports.PreviewIn) imports.PreviewOut {
	e.t.Helper()
	in.Pdf = data
	in.Name = "book.pdf"
	return e.must(e.w, "import.preview", in).(imports.PreviewOut)
}

func (e *env) applyPdf(data []byte, pv imports.PreviewOut, in imports.ApplyIn) imports.ApplyOut {
	e.t.Helper()
	in.Pdf, in.Token, in.Name = data, pv.Token, "book.pdf"
	return e.must(e.w, "import.apply", in).(imports.ApplyOut)
}

const saltWords = "the boat came in at dawn and the whole harbour smelled of salt and tar while gulls wheeled over the quay and old men mended nets on the wall nobody spoke of the winter that had passed she carried a small bag a letter folded into four and the name of a man she had never met the road ran east along the shore past the lime kilns and the drowned fields where the sea had taken back what it had lent"

func saltPara(n, from int) string {
	w := strings.Fields(saltWords)
	var b strings.Builder
	for i := 0; i < n; i++ {
		x := w[(from+i)%len(w)]
		if i == 0 {
			x = strings.ToUpper(x[:1]) + x[1:]
		}
		b.WriteString(x)
		if i < n-1 {
			b.WriteByte(' ')
		}
	}
	return b.String() + "."
}

// compiledBook makes a project with a part, three chapters, scene breaks,
// emphasis and a footnote, and compiles it as a print PDF.
func (e *env) compiledBook(t *testing.T, preset string, chapters int) (pid string, body []byte, texts map[string]string) {
	t.Helper()
	out := e.must(e.w, "project.create", projects.CreateIn{Title: "The Salt Road", Subtitle: "A novel", AuthorName: "Máire Ní Bhriain", Kind: "novel"}).(projects.Item)
	pid = out.Item.ID
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	for _, m := range lo.Items {
		e.must(e.w, "doc.trash", docs.DocIn{ID: m.ID})
	}
	texts = map[string]string{}
	mk := func(title, kind, parent, content string) string {
		d := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: title, Kind: kind, ParentID: parent, Content: content}).(docs.Item).Item
		texts[title] = content
		return d.ID
	}
	mk("Dedication", "front_matter", "", "For the ones who walked.")
	for c := 1; c <= chapters; c++ {
		title := fmt.Sprintf("Chapter Title %d", c)
		body := saltPara(120, c*7) + "\n\n" + saltPara(95, c*11) + "\n\n* * *\n\n" + "A *quiet* start with **weight** in it. " + saltPara(70, c*3) + "\n\n" + saltPara(60, c*13)
		if c == 1 {
			body += "\n\nA closing line with a note.[^1]\n\n[^1]: The note text of chapter one.\n"
		}
		mk(title, "chapter", "", body)
	}
	o := e.must(e.w, "export.compile", compile.CompileIn{ProjectID: pid, Preset: preset}).(compile.Out)
	if dir := os.Getenv("SCRIOBH_KEEP"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "roundtrip-"+preset+".pdf"), o.Body, 0o644)
	}
	return pid, o.Body, texts
}

func nodeTitles(pv imports.PreviewOut) string {
	var t []string
	for _, n := range pv.Nodes {
		t = append(t, n.Kind+":"+n.Title)
	}
	return strings.Join(t, " | ")
}

// A book compiled to PDF by this program re-imports with its chapters, its
// paragraphs' words, its scene breaks and its emphasis recovered.
func TestPdfRoundTrip(t *testing.T) {
	e := newEnv(t)
	_, pdf, texts := e.compiledBook(t, "print-6x9", 3)
	pv := e.previewPdf(pdf, imports.PreviewIn{})
	if pv.Layout != "pdf" || pv.Project.Title != "The Salt Road" || pv.Project.Author != "Máire Ní Bhriain" {
		t.Fatalf("preview: layout %q project %+v", pv.Layout, pv.Project)
	}
	t.Logf("nodes: %s", nodeTitles(pv))
	t.Logf("warnings:\n%s", strings.Join(pv.Warnings, "\n"))
	rep := pv.Report["pdf"].(map[string]any)["report"].(map[string]any)
	if d, ok := rep["page_detail"]; ok {
		t.Logf("page detail: %v", d)
	}
	ap := e.applyPdf(pdf, pv, imports.ApplyIn{})
	for _, title := range []string{"Chapter Title 1", "Chapter Title 2", "Chapter Title 3"} {
		got := e.docContent(ap.ProjectID, title)
		want := texts[title]
		if w, g := plainWords(want), plainWords(got); w != g {
			t.Errorf("%s: words differ\n got: %.300s\nwant: %.300s", title, g, w)
		}
		if strings.Count(got, "* * *") != strings.Count(want, "* * *") {
			t.Errorf("%s: %d scene breaks, want %d", title, strings.Count(got, "* * *"), strings.Count(want, "* * *"))
		}
		if strings.Contains(want, "*quiet*") && (!strings.Contains(got, "*quiet*") || !strings.Contains(got, "**weight**")) {
			t.Errorf("%s: emphasis lost: %.400s", title, got)
		}
		if got != want {
			i := 0
			for i < len(got) && i < len(want) && got[i] == want[i] {
				i++
			}
			t.Logf("%s: first difference at byte %d of %d/%d:\n got: %q\nwant: %q", title, i, len(got), len(want), got[max(0, i-40):min(len(got), i+80)], want[max(0, i-40):min(len(want), i+80)])
		}
	}
}

// plainWords is a document's words with the Markdown taken off: what must survive.
func plainWords(md string) string {
	var out []string
	for _, line := range strings.Split(md, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || l == "* * *" || strings.HasPrefix(l, "[^") && strings.Contains(l, "]:") {
			continue
		}
		l = strings.NewReplacer("*", "", "\\", "", "[^1]", "").Replace(l)
		out = append(out, strings.Fields(l)...)
	}
	return strings.ToLower(strings.Join(out, " "))
}

// A 300-page text book previews and imports well inside a minute.
func TestPdfThreeHundredPagesWithinTheBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 300-page book")
	}
	e := newEnv(t)
	out := e.must(e.w, "project.create", projects.CreateIn{Title: "Long Book", AuthorName: "A. Writer", Kind: "novel"}).(projects.Item)
	pid := out.Item.ID
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	for _, m := range lo.Items {
		e.must(e.w, "doc.trash", docs.DocIn{ID: m.ID})
	}
	words := 0
	for c := 1; c <= 30; c++ {
		var b strings.Builder
		for p := 0; p < 12; p++ {
			b.WriteString(saltPara(430+p*3, c*13+p*5) + "\n\n")
			words += 430 + p*3
			if p == 5 {
				b.WriteString("* * *\n\n")
			}
		}
		e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: fmt.Sprintf("Chapter Number %d", c), Kind: "chapter", Content: b.String()})
	}
	o := e.must(e.w, "export.compile", compile.CompileIn{ProjectID: pid, Preset: "print-6x9"}).(compile.Out)
	t.Logf("the PDF is %d KB, %d words; compile warnings: %+v", len(o.Body)>>10, words, o.Warnings)
	var peak uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			}
		}
	}()
	runtime.GC()
	start := time.Now()
	pv := e.previewPdf(o.Body, imports.PreviewIn{})
	tPreview := time.Since(start)
	start = time.Now()
	ap := e.applyPdf(o.Body, pv, imports.ApplyIn{})
	tApply := time.Since(start)
	close(stop)
	<-done
	t.Logf("peak heap while previewing and applying a 304-page PDF: %d MB", peak>>20)
	if peak>>20 > 400 {
		t.Errorf("peak heap %d MB", peak>>20)
	}
	rep := pv.Report["pdf"].(map[string]any)["report"].(map[string]any)
	t.Logf("pages %v; preview %v; apply %v (a preview and an apply that follows it read the file once); %d documents; %d words", rep["pages"], tPreview, tApply, ap.Documents, ap.Words)
	if pages := int(rep["pages"].(float64)); pages < 295 {
		t.Errorf("only %d pages", pages)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if tPreview > 60*time.Second*time.Duration(timingFactor) || tApply > 60*time.Second*time.Duration(timingFactor) {
			t.Errorf("preview %v apply %v", tPreview, tApply)
		}
	})
	chapters := 0
	for _, n := range pv.Nodes {
		if n.Kind == "chapter" && strings.HasPrefix(n.Title, "Chapter Number") {
			chapters++
		}
	}
	if chapters != 30 {
		t.Errorf("%d chapters recovered, want 30", chapters)
	}
	if d := ap.Words - words; d > words/100 || -d > words/100 {
		t.Errorf("words: imported %d, written %d", ap.Words, words)
	}
}

// What a print PDF of a book with every kind of block gives back: asserted
// exactly, so a change in what is recovered is a change to the test and to the
// notes.
func TestPdfRoundTripRichBook(t *testing.T) {
	e := newEnv(t)
	out := e.must(e.w, "project.create", projects.CreateIn{Title: "Rich Book", Subtitle: "Every feature", AuthorName: "A. Writer", Kind: "novel"}).(projects.Item)
	pid := out.Item.ID
	lo := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	for _, m := range lo.Items {
		e.must(e.w, "doc.trash", docs.DocIn{ID: m.ID})
	}
	mk := func(title, kind, parent, content string) string {
		return e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: title, Kind: kind, ParentID: parent, Content: content}).(docs.Item).Item.ID
	}
	mk("Dedication", "front_matter", "", "For the ones who walked.")
	p1 := mk("Part One", "part", "", "")
	body := saltPara(120, 1) + "\n\n> A block quotation that is set apart from the text around it, " + saltPara(40, 2) + "\n\n- first item of a list\n- second item of a list\n\n1. numbered one\n2. numbered two\n\n" +
		"| Name | Age |\n| --- | --- |\n| Ann | 31 |\n| Bo | 42 |\n\n```\ncode line one\ncode line two\n```\n\nA [link](https://example.com/x) and `inline code` and a footnote.[^1] And a second one.[^2]\n\n" + saltPara(60, 3) + "\n\n[^1]: First note text.\n\n[^2]: Second note text, a little longer than the first one.\n"
	mk("The Arrival", "chapter", p1, body)
	mk("The Road", "chapter", p1, saltPara(100, 5)+"\n\n* * *\n\n"+saltPara(100, 6))
	p2 := mk("Part Two", "part", "", "")
	mk("The End", "chapter", p2, saltPara(100, 7))
	o := e.must(e.w, "export.compile", compile.CompileIn{ProjectID: pid, Preset: "print-6x9"}).(compile.Out)
	pv := e.previewPdf(o.Body, imports.PreviewIn{})
	// the tree: the copyright page, the dedication, two parts with their chapters
	if got := nodeTitles(pv); got != "front_matter:Front Matter | front_matter:Dedication | part:Part One | chapter:The Arrival | chapter:The Road | part:Part Two | chapter:The End" {
		t.Errorf("tree: %s", got)
	}
	ap := e.applyPdf(o.Body, pv, imports.ApplyIn{})
	text := e.docContent(ap.ProjectID, "The Arrival")
	for _, want := range []string{
		"\n\n> A block quotation that is set apart from the text around it, ",
		"\n\n- first item of a list\n- second item of a list\n\n1. numbered one\n2. numbered two\n\n",
		"\n\n```\ncode line one\ncode line two\n```\n\n",
		"A link and `inline code` and a footnote.[^1] And a second one.[^2]",
		"[^1]: First note text.\n[^2]: Second note text, a little longer than the first one.\n",
		"**Name \\| Age**\n\nAnn \\| 31\n\nBo \\| 42\n", // a table: its rows, flattened
	} {
		if !strings.Contains(text, want) {
			t.Errorf("chapter text lacks %q:\n%s", want, text)
		}
	}
	// what is not recovered: the link's address, a running head seen on fewer than three pages
	if strings.Contains(text, "example.com") {
		t.Error("a link address cannot be recovered from the text layer")
	}
	// the road's scene break (an ornament in the PDF) is the canonical one
	if road := e.docContent(ap.ProjectID, "The Road"); !strings.Contains(road, "\n\n* * *\n\n") {
		t.Errorf("scene break lost:\n%s", road)
	}
	w := strings.Join(pv.Warnings, "\n")
	for _, want := range []string{"6 of 6 bookmarks", "6 line(s) of a table of contents", "3 table row(s) were flattened", "2 footnote(s) were found: 2 linked"} {
		if !strings.Contains(w, want) {
			t.Errorf("warnings lack %q:\n%s", want, w)
		}
	}
}
