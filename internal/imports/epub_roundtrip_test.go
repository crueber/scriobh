package imports_test

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// A book is made in a project, compiled to EPUB 3 by the exporter, imported, and compiled
// again. If nothing structural was lost, the second book's chapters are the first's.

type rtDoc struct {
	kind, title, body string
	kids              []rtDoc
}

type rtBook struct {
	name                              string
	title, subtitle, author, language string
	docs                              []rtDoc
	images                            map[string][]byte // {{img:key}} in a body
	opts                              publish.Options
	// titlesLost: the writer chose not to print the chapter titles ("Chapter 3" only), so the book does not hold them
	// and an import cannot give them back: the chapters come back numbered, and compile to the same book.
	titlesLost bool
}

func (e *env) makeProject(b rtBook) string {
	e.t.Helper()
	out := e.must(e.w, "project.create", projects.CreateIn{Title: b.title, Subtitle: b.subtitle, AuthorName: b.author, Language: b.language, Kind: "novel"}).(projects.Item)
	pid := out.Item.ID
	// A new project holds a starter chapter; the book is exactly what the test makes.
	for _, it := range e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut).Items {
		e.must(e.w, "doc.trash", docs.DocIn{ID: it.ID})
	}
	urls := map[string]string{}
	for k, data := range b.images {
		ctx := ops.WithRawBody(context.Background(), ops.RawBody{R: bytes.NewReader(data), Length: int64(len(data))})
		it, err := e.reg.Call(ctx, e.w, "project.attachment.upload", attachments.UploadIn{ID: pid, Filename: k + ".png"})
		if err != nil {
			e.t.Fatal(err)
		}
		urls["{{img:"+k+"}}"] = "/files/" + it.(attachments.Item).Item.ID
	}
	var add func(parent string, ds []rtDoc)
	add = func(parent string, ds []rtDoc) {
		for _, d := range ds {
			body := d.body
			for k, u := range urls {
				body = strings.ReplaceAll(body, k, u)
			}
			c := e.must(e.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: d.title, Kind: d.kind, ParentID: parent, Content: body}).(docs.Item)
			add(c.Item.ID, d.kids)
		}
	}
	add("", b.docs)
	return pid
}

func (e *env) compileEPUB(pid string) []byte { return e.compileEPUBWith(pid, publish.Options{}) }

func (e *env) compileEPUBWith(pid string, o publish.Options) []byte {
	e.t.Helper()
	out := e.must(e.w, "export.compile", compile.CompileIn{ProjectID: pid, Preset: "epub3", Options: o}).(compile.Out)
	return out.Body
}

func unzipAll(t testing.TB, b []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		d, _ := io.ReadAll(rc)
		rc.Close()
		m[f.Name] = d
	}
	return m
}

var (
	reModified  = regexp.MustCompile(`<meta property="dcterms:modified">[^<]*</meta>`)
	reUUID      = regexp.MustCompile(`urn:uuid:[0-9A-Za-z-]+`)
	reDataTitle = regexp.MustCompile(` data-title="[^"]*"`)
)

// roundTrip imports the EPUB as a new project and returns the project id and the second EPUB.
func (e *env) roundTrip(book []byte, name string, o publish.Options) (string, []byte, imports.PreviewOut, imports.ApplyOut) {
	e.t.Helper()
	pv := e.previewConv(imports.PreviewIn{Name: name, Epub: book})
	ap := e.applyConv(pv, imports.ApplyIn{Name: name, Epub: book})
	return ap.ProjectID, e.compileEPUBWith(ap.ProjectID, o), pv, ap
}

// sameBook compares two EPUBs the exporter made: every content file byte for byte, the
// navigation, and the package document without the parts that name the project and its date.
func sameBook(t *testing.T, a, b []byte, ignoreTitles bool) {
	t.Helper()
	ma, mb := unzipAll(t, a), unzipAll(t, b)
	var names []string
	for n := range ma {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if n == "OEBPS/images/cover.svg" || n == "OEBPS/text/cover.xhtml" {
			continue // drawn from the project's own fields (its kind and colour), not from the text
		}
		got, ok := mb[n]
		if !ok {
			t.Errorf("the second book has no %s", n)
			continue
		}
		x, y := string(ma[n]), string(got)
		if ignoreTitles { // the numbered chapters came back titled "Chapter N", which the next compile prints as it printed them
			x, y = reDataTitle.ReplaceAllString(x, ""), reDataTitle.ReplaceAllString(y, "")
		}
		if n == "OEBPS/content.opf" || n == "OEBPS/toc.ncx" {
			x, y = reModified.ReplaceAllString(reUUID.ReplaceAllString(x, "ID"), "M"), reModified.ReplaceAllString(reUUID.ReplaceAllString(y, "ID"), "M")
		}
		if x != y {
			t.Errorf("%s differs:\n--- first\n%s\n--- second\n%s", n, firstDiff(x, y, 400), firstDiff(y, x, 400))
		}
	}
	for n := range mb {
		if _, ok := ma[n]; !ok {
			t.Errorf("the second book has an extra file %s", n)
		}
	}
}

// firstDiff shows a few hundred bytes of a around the first place it differs from b.
func firstDiff(a, b string, n int) string {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	lo := max(0, i-60)
	return a[lo:min(len(a), lo+n)]
}

// shape lists a project's documents in reading order as depth, kind and title. A scene of the
// book that was compiled has no document of its own once imported (it is part of its chapter), so
// scenes are left out of the source's shape.
func (e *env) shape(pid string, dropScenes bool) []string {
	e.t.Helper()
	l := e.must(e.w, "doc.list", docs.ListIn{ProjectID: pid}).(docs.ListOut)
	by := map[string]docs.Meta{}
	for _, m := range l.Items {
		by[m.ID] = m
	}
	var out []string
	for _, id := range l.Order {
		m := by[id]
		if dropScenes && m.Kind == "scene" {
			continue
		}
		d := 0
		for p := m.ParentID; p != ""; p = by[p].ParentID {
			if dropScenes && by[p].Kind == "scene" {
				continue
			}
			d++
		}
		out = append(out, strings.Repeat("  ", d)+m.Kind+":"+m.Title)
	}
	return out
}

func rtBooks() []rtBook {
	img1, img2 := pngBytes(30, 20, 3), pngBytes(24, 24, 77)
	return []rtBook{
		{name: "parts and scenes", title: "The Salt Road", subtitle: "A Novel", author: "Ada Fixture", language: "en",
			images: map[string][]byte{"map": img1, "plate": img2},
			docs: []rtDoc{
				{kind: "front_matter", title: "Dedication", body: "*For the marsh.*\n"},
				{kind: "part", title: "Part One", body: "The water keeps what it is given.\n", kids: []rtDoc{
					{kind: "chapter", title: "The Road", body: "They came at dusk.[^1]\n\n![A map](" + "{{img:map}}" + ")\n\n[^1]: A first note, with *emphasis*.\n", kids: []rtDoc{
						{kind: "scene", title: "Dusk", body: "A scene of its own.\n"},
						{kind: "scene", title: "Dawn", body: "Morning.[^1]\n\n[^1]: Another note.\n"},
					}},
					{kind: "chapter", title: "The Wall", body: "Rain.\n\n* * *\n\nMore rain.\n\n## A heading inside\n\nUnder it.\n"},
				}},
				{kind: "part", title: "Part Two", kids: []rtDoc{
					{kind: "chapter", title: "Ashes", body: "Smoke & ash — “quoted” and ‘single’.\n\n![](" + "{{img:plate}}" + ")\n"},
					{kind: "chapter", title: "The Empty One"},
				}},
				{kind: "back_matter", title: "Acknowledgments", body: "Thanks.\n"},
			}},
		{name: "no parts", title: "Plain", author: "B. Writer", language: "en-GB",
			docs: []rtDoc{
				{kind: "chapter", title: "One", body: "First.\n\n* * *\n\nSecond.\n"},
				{kind: "chapter", title: "Two", body: "Hello **bold** and *italic* and `code` and [a link](https://example.com/x) and ~~gone~~.\n\n> A quote.\n\n- one\n- two\n\n1. first\n2. second\n"},
				{kind: "chapter", title: "Three", body: "| a | b |\n| --- | --- |\n| 1 | 2 |\n"},
			}},
		{name: "right to left and unicode", title: "كتاب الملح", author: "عادل", language: "ar",
			docs: []rtDoc{
				{kind: "chapter", title: "الطريق", body: "جاؤوا عند الغسق.\n\nقالت: «لا شيء».\n"},
				{kind: "chapter", title: "Ünïcödé ✦ 漢字", body: "Ça va très bien — naïve café. 日本語のテキスト。\n"},
			}},
	}
}

func rtVariants() []rtBook {
	base := rtBooks()[0]
	var out []rtBook
	for _, v := range []struct {
		name string
		o    publish.Options
	}{
		{"heading is the number alone", publish.Options{ChapterHeading: "number"}},
		{"heading is the title alone", publish.Options{ChapterHeading: "title"}},
		{"no chapter heading", publish.Options{ChapterHeading: "none"}},
		{"another word for chapter", publish.Options{ChapterLabel: "Capítulo"}},
		{"the end", publish.Options{TheEnd: publish.True()}},
		{"stars for a scene break", publish.Options{SceneBreak: "stars"}},
		{"an ornament", publish.Options{SceneBreak: "ornament", Ornament: "◆ ◆"}},
		{"scene titles", publish.Options{SceneTitles: publish.True()}},
		{"no title page and no cover", publish.Options{TitlePage: publish.False(), Cover: publish.False()}},
	} {
		b := base
		b.name, b.opts, b.titlesLost = v.name, v.o, v.o.ChapterHeading == "number"
		out = append(out, b)
	}
	return out
}

func TestRoundTripOfTheExporter(t *testing.T) {
	for _, b := range append(rtBooks(), rtVariants()...) {
		t.Run(b.name, func(t *testing.T) {
			t0 := t
			e := newEnv(t)
			src := e.makeProject(b)
			first := e.compileEPUBWith(src, b.opts)
			pid, second, pv, ap := e.roundTrip(first, b.name+".epub", b.opts)
			sameBook(t, first, second, b.titlesLost)
			m := unzipAll(t, second)
			t.Logf("%d files, outline %v", len(m), outline(pv.Nodes))

			if b.name == "parts and scenes" {
				var imgs, notes, breaks int
				for n, d := range m {
					if strings.HasPrefix(n, "OEBPS/images/img-") {
						imgs++
					}
					if strings.HasSuffix(n, ".xhtml") {
						notes += strings.Count(string(d), `epub:type="footnote"`)
						breaks += strings.Count(string(d), `class="scenebreak"`)
					}
				}
				if imgs != 2 || notes != 2 || breaks < 3 {
					t.Errorf("images %d, notes %d, breaks %d", imgs, notes, breaks)
				}
			}
			if pv.Epub == nil || pv.Epub.Strategy != "toc" {
				t.Errorf("strategy: %+v", pv.Epub)
			}
			// The project's own facts come back.
			if pv.Project.Title != b.title || pv.Project.Subtitle != b.subtitle || pv.Project.Author != b.author || pv.Project.Language != b.language {
				t.Errorf("project facts: %+v", pv.Project)
			}
			if !strings.HasPrefix(pv.Epub.Identifier, "urn:uuid:") || pv.Epub.Date == "" && false {
				t.Errorf("identifier %q", pv.Epub.Identifier)
			}
			want := e.shape(src, true)
			if b.titlesLost {
				// The titles the writer did not print are not in the book (and so not given back): the chapters come back numbered.
				for _, t := range []string{"The Road", "The Wall", "Ashes", "The Empty One"} {
					for name, d := range unzipAll(t0, first) {
						if strings.Contains(string(d), t) {
							t0.Errorf("%s holds the title %q the writer chose not to print", name, t)
						}
					}
				}
				n := 0
				for i, l := range want {
					if k := strings.Index(l, "chapter:"); k >= 0 {
						n++
						want[i] = l[:k] + "chapter:Chapter " + strconv.Itoa(n)
					}
				}
			}
			if got := e.shape(pid, false); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("the tree changed:\n%s\nwas:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			// The edition is kept: the file as it was, and its hash.
			if ap.EditionID == "" {
				t.Fatal("no edition was recorded")
			}
		})
	}
}
