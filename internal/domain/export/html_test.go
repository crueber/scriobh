package export_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func (f *fx) html(p ops.Principal, in export.HTMLIn) (export.File, error) {
	in.ProjectID = f.pid
	out, err := f.reg.Call(f.ctx, p, "export.html", in)
	if err != nil {
		return export.File{}, err
	}
	return out.(export.File), nil
}

func (f *fx) mustHTML(in export.HTMLIn) export.File {
	f.t.Helper()
	file, err := f.html(f.owner, in)
	if err != nil {
		f.t.Fatal(err)
	}
	return file
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	p := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (run with -update to accept):\n%s", name, firstDiff(string(got), string(want)))
	}
}

func firstDiff(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) && i < len(w); i++ {
		if g[i] != w[i] {
			return "line " + itoa(i+1) + "\n got: " + g[i] + "\nwant: " + w[i]
		}
	}
	return "lengths differ: " + itoa(len(g)) + " vs " + itoa(len(w)) + " lines"
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// tagsOf returns the names of every element in the HTML (user text is escaped,
// so every '<' opens a real tag).
func tagsOf(h string) map[string]int {
	out := map[string]int{}
	for _, m := range regexp.MustCompile(`<([A-Za-z][A-Za-z0-9]*)`).FindAllStringSubmatch(h, -1) {
		out[strings.ToLower(m[1])]++
	}
	return out
}

var onAttr = regexp.MustCompile(`<[^>]*\son[a-z]+\s*=`)

// assertInertPage fails when a page could run code, load anything or submit
// anything: elements outside the shell's, an event attribute, a remote address.
func assertInertPage(t *testing.T, where, h string) {
	t.Helper()
	allowed := map[string]bool{"html": true, "head": true, "meta": true, "title": true, "style": true, "body": true, "main": true, "header": true,
		"figure": true, "svg": true, "rect": true, "text": true, "a": true, "p": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"nav": true, "ol": true, "ul": true, "li": true, "section": true, "article": true, "em": true, "strong": true, "del": true, "code": true, "pre": true,
		"span": true, "br": true, "hr": true, "blockquote": true, "table": true, "thead": true, "tr": true, "th": true, "td": true, "sup": true, "img": true}
	for tag := range tagsOf(h) {
		if !allowed[tag] {
			t.Errorf("%s: element <%s> is not allowed", where, tag)
		}
	}
	if n := tagsOf(h)["style"]; n != 1 {
		t.Errorf("%s: %d style elements", where, n)
	}
	if onAttr.MatchString(h) {
		t.Errorf("%s: an event attribute is present", where)
	}
	if !strings.Contains(h, `http-equiv="Content-Security-Policy"`) || !strings.Contains(h, "default-src &#39;none&#39;") {
		t.Errorf("%s: no strict Content-Security-Policy", where)
	}
	for _, bad := range []string{"<script", "<link", "<form", "<iframe", "<object", "<embed"} {
		if strings.Contains(h, bad) {
			t.Errorf("%s: contains %q", where, bad)
		}
	}
	// User text is escaped, so a '<' always opens one of our tags. No attribute
	// holds an address except the SVG namespace, and the stylesheet loads nothing.
	for _, tag := range regexp.MustCompile(`<[^>]*>`).FindAllString(h, -1) {
		for _, a := range regexp.MustCompile(`\s([a-z-]+)="([^"]*)"`).FindAllStringSubmatch(tag, -1) {
			v := strings.ToLower(a[2])
			if a[1] == "xmlns" && a[2] == "http://www.w3.org/2000/svg" {
				continue
			}
			if strings.Contains(v, "http:") || strings.Contains(v, "https:") || strings.HasPrefix(v, "//") || strings.Contains(v, "javascript:") {
				t.Errorf("%s: attribute %s=%q holds an address", where, a[1], a[2])
			}
		}
	}
	css := h[strings.Index(h, "<style>")+7 : strings.Index(h, "</style>")]
	if strings.Contains(css, "@import") || strings.Contains(css, "url(") || strings.Contains(css, "//") {
		t.Errorf("%s: the stylesheet loads something", where)
	}
}

func TestHTMLSingle(t *testing.T) {
	f := newFx(t, "The Long Tide")
	f.novel()
	file := f.mustHTML(export.HTMLIn{})
	if file.Filename != "the-long-tide.html" || file.ContentType != "text/html; charset=utf-8" {
		t.Errorf("%s %s", file.Filename, file.ContentType)
	}
	h := string(file.Body)
	assertInertPage(t, "single", h)
	for _, want := range []string{
		"<h1 class=\"title\">The Long Tide</h1>", "<p class=\"subtitle\">A sub *title*</p>", "<p class=\"author\">Máire Ní Bhriain</p>",
		"<figure class=\"cover\"><svg", "<nav class=\"toc\"", "frontmark", "ch1mark", "s1mark", "s2mark", "ch2mark", "ch3mark", "backmark",
		"<h2>Part One</h2>", "<h3>The Arrival</h3>", "<h4>Harbour</h4>", "<h3>Máire&#39;s Boat: *a* [story] #</h3>",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Not in the file: notes (not asked for), the trashed chapter, the alternatives comment.
	for _, absent := range []string{"n1mark", "n2mark", "gonemark", "Research", "Plot Notes", "scriobh:alts", "other wording"} {
		if strings.Contains(h, absent) {
			t.Errorf("the export holds %q", absent)
		}
	}
	if file.Documents != 9 {
		t.Errorf("documents = %d, want 9", file.Documents)
	}
	// Every contents link, and every in-page link, lands on an id in the file.
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(h, -1) {
		if ids[m[1]] {
			t.Errorf("id %q is used twice", m[1])
		}
		ids[m[1]] = true
	}
	links := regexp.MustCompile(`<a href="#([^"]+)"`).FindAllStringSubmatch(h, -1)
	if len(links) < 8 {
		t.Errorf("only %d in-page links", len(links))
	}
	for _, m := range links {
		if !ids[m[1]] {
			t.Errorf("link to #%s has no target", m[1])
		}
	}
	// The headings are in reading order.
	var last int
	for _, title := range []string{"Front Matter", "Part One", "The Arrival", "Harbour", "Part Two", "The End", "Afterword"} {
		i := strings.Index(h, ">"+title+"</h")
		if i < last {
			t.Errorf("%q is out of order", title)
		}
		last = i
	}
}

func TestHTMLNotesAndPrivateNotes(t *testing.T) {
	f := newFx(t, "Notes")
	d := f.novel()
	body := "Public line one. %%A private aside, privmark.%% Public line two.\n"
	rev := d["c3"].Revision
	if _, err := f.reg.Call(f.ctx, f.owner, "doc.update", docs.UpdateIn{ID: d["c3"].ID, BaseRevision: rev, Content: &body}); err != nil {
		t.Fatal(err)
	}
	h := string(f.mustHTML(export.HTMLIn{}).Body)
	if strings.Contains(h, "privmark") || strings.Contains(h, "private-note\"") && strings.Contains(h, "privmark") {
		t.Error("a private note is in the default export")
	}
	if !strings.Contains(h, "Public line one.  Public line two.") {
		t.Errorf("the text around the note is wrong")
	}
	h = string(f.mustHTML(export.HTMLIn{PrivateNotes: true}).Body)
	if !strings.Contains(h, `<span class="private-note">A private aside, privmark.</span>`) {
		t.Errorf("private notes asked for but not marked")
	}
	h = string(f.mustHTML(export.HTMLIn{Notes: true}).Body)
	i := strings.Index(h, `id="notes"`)
	if i < 0 || strings.Index(h, "n1mark") < i || strings.Index(h, "n2mark") < i || strings.Contains(h[:i], "n1mark") {
		t.Errorf("notes are not in their own section after the manuscript")
	}
	if !strings.Contains(h, "<h2 class=\"toc-notes\">Notes</h2>") {
		t.Error("the notes are not in the contents")
	}
}

func TestHTMLIsDeterministic(t *testing.T) {
	f := newFx(t, "Same Bytes")
	f.novel()
	for _, mode := range []string{"single", "bundle"} {
		a, b := f.mustHTML(export.HTMLIn{Mode: mode, Notes: true}), f.mustHTML(export.HTMLIn{Mode: mode, Notes: true})
		if a.SHA256 != b.SHA256 || !bytes.Equal(a.Body, b.Body) {
			t.Errorf("%s: two exports of one project differ", mode)
		}
		if a.ExportID == b.ExportID {
			t.Errorf("%s: two exports share an id", mode)
		}
	}
	// A second project with the same text and the same fields has the same page
	// (nothing of the id, the user or the clock reaches the file).
	g := newFx(t, "Same Bytes")
	g.novel()
	if !bytes.Equal(f.mustHTML(export.HTMLIn{}).Body, g.mustHTML(export.HTMLIn{}).Body) {
		t.Error("two projects with the same content export different single files")
	}
}

func TestHTMLBundle(t *testing.T) {
	f := newFx(t, "The Long Tide")
	f.novel()
	file := f.mustHTML(export.HTMLIn{Mode: "bundle"})
	if file.Filename != "the-long-tide-html.zip" || file.ContentType != "application/zip" {
		t.Errorf("%s %s", file.Filename, file.ContentType)
	}
	names, files, _ := unzip(t, file.Body)
	want := []string{
		"index.html",
		"manuscript/00-front-matter.html",
		"manuscript/01-part-one/00-part-one.html",
		"manuscript/01-part-one/01-the-arrival/00-the-arrival.html",
		"manuscript/01-part-one/01-the-arrival/01-harbour.html",
		"manuscript/01-part-one/01-the-arrival/02-untitled.html",
		"manuscript/01-part-one/02-maire-s-boat-a-story.html",
		"manuscript/02-part-two/00-part-two.html",
		"manuscript/02-part-two/01-the-end.html",
		"manuscript/03-afterword.html",
		"scriobh.json",
	}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Fatalf("bundle entries\n got:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
	for name, data := range files {
		if strings.HasSuffix(name, ".html") {
			assertInertPage(t, name, string(data))
		}
	}
	// Every link in every page lands on a file of the bundle.
	hrefs := regexp.MustCompile(`href="([^"#]+)(?:#[^"]*)?"`)
	for name, data := range files {
		if !strings.HasSuffix(name, ".html") {
			continue
		}
		for _, m := range hrefs.FindAllStringSubmatch(string(data), -1) {
			if _, ok := files[path.Clean(path.Join(path.Dir(name), m[1]))]; !ok {
				t.Errorf("%s links to %s, which is not in the bundle", name, m[1])
			}
		}
	}
	idx := string(files["index.html"])
	for _, w := range []string{"<h1 class=\"title\">The Long Tide</h1>", `href="manuscript/01-part-one/01-the-arrival/01-harbour.html"`, "<figure class=\"cover\">"} {
		if !strings.Contains(idx, w) {
			t.Errorf("index.html lacks %s", w)
		}
	}
	page := string(files["manuscript/01-part-one/01-the-arrival/01-harbour.html"])
	for _, w := range []string{"s1mark", `href="../../../index.html"`, `rel="prev"`, `rel="next"`, "<h1>Harbour</h1>"} {
		if !strings.Contains(page, w) {
			t.Errorf("the page lacks %s", w)
		}
	}
	if strings.Contains(page, "scriobh:alts") {
		t.Error("the alternatives comment is on the page")
	}
	// The manifest lists the pages with the hash of the Markdown each was built from.
	var m struct {
		Format    string
		Documents []struct{ ID, Path, SHA256 string }
	}
	if err := json.Unmarshal(files["scriobh.json"], &m); err != nil {
		t.Fatal(err)
	}
	if m.Format != "scriobh-html-bundle" || len(m.Documents) != 9 {
		t.Errorf("manifest %+v", m)
	}
	for _, x := range m.Documents {
		if _, ok := files[x.Path]; !ok || !strings.HasSuffix(x.Path, ".html") {
			t.Errorf("manifest path %s", x.Path)
		}
	}
}

func TestHTMLWikiLinksResolve(t *testing.T) {
	f := newFx(t, "Links")
	a := f.doc("The Arrival", "chapter", "", "First, then [[The Departure]] and [[the arrival|here again]] and [[Nowhere]].")
	f.doc("The Departure", "chapter", "", "See [[The Arrival]].")
	h := string(f.mustHTML(export.HTMLIn{}).Body)
	if !regexp.MustCompile(`<a class="wiki" href="#d\d+-the-departure">The Departure</a>`).MatchString(h) ||
		!regexp.MustCompile(`<a class="wiki" href="#d\d+-the-arrival">here again</a>`).MatchString(h) ||
		!strings.Contains(h, `<span class="wiki">Nowhere</span>`) {
		t.Errorf("wiki links:\n%s", h)
	}
	_ = a
	_, files, _ := unzip(t, f.mustHTML(export.HTMLIn{Mode: "bundle"}).Body)
	p := string(files["manuscript/01-the-arrival.html"])
	if !strings.Contains(p, `<a class="wiki" href="02-the-departure.html">The Departure</a>`) {
		t.Errorf("bundle wiki link: %s", p)
	}
}

func TestHTMLHostileContentIsInert(t *testing.T) {
	f := newFx(t, "<script>alert('title')</script>")
	// Hostile project fields, document titles and bodies.
	sub, au := "<img src=x onerror=alert(a)>\"><script>alert(b)</script>", "'; </svg><script>alert(c)</script>"
	if _, err := f.reg.Call(f.ctx, f.owner, "project.update", projects.UpdateIn{ID: f.pid, Subtitle: &sub, AuthorName: &au}); err != nil {
		t.Fatal(err)
	}
	hostile := "<script>alert(1)</script>\n\n<img src=x onerror=alert(2)>\n\n[click](javascript:alert(3)) ![t](http://tracker.example/p.gif) <a href=\"javascript:alert(4)\">x</a>\n\n" +
		"<iframe src=\"//evil.example\"></iframe> <style>body{background:url(http://evil.example/x)}</style>\n\n```\"><script>alert(5)</script>\n```\n\n" +
		"[[<script>alert(6)</script>]] text[^1]\n\n[^1]: <img src=x onerror=alert(7)>\n\n---\ntitle: <script>alert(8)</script>\n---\n"
	f.doc("<img src=x onerror=alert(9)>\"onmouseover=\"alert(10)", "chapter", "", hostile)
	f.doc("</title><script>alert(11)</script>", "scene", "", hostile)
	f.doc("Note <script>", "note", "", hostile)
	for _, mode := range []string{"single", "bundle"} {
		file := f.mustHTML(export.HTMLIn{Mode: mode, Notes: true, PrivateNotes: true})
		if mode == "single" {
			assertInertPage(t, "single", string(file.Body))
			continue
		}
		_, files, _ := unzip(t, file.Body)
		for name, data := range files {
			if strings.HasSuffix(name, ".html") {
				assertInertPage(t, name, string(data))
			}
		}
	}
}

func TestHTMLAccessAndOptions(t *testing.T) {
	f := newFx(t, "Access")
	f.novel()
	if _, err := f.html(f.reader, export.HTMLIn{}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reader exporting: %v", err)
	}
	if _, err := f.html(f.outsider, export.HTMLIn{}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("an outsider exporting: %v", err)
	}
	if _, err := f.html(f.owner, export.HTMLIn{Mode: "pdf"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a bad mode: %v", err)
	}
}

func TestHTMLIsRecordedWithAManifest(t *testing.T) {
	f := newFx(t, "Recorded")
	f.novel()
	file := f.mustHTML(export.HTMLIn{Mode: "bundle", PrivateNotes: true})
	var format, preset, sum, manifest string
	var bytes int
	if err := f.d.Read().QueryRow(`SELECT format, preset, sha256, bytes, manifest_json FROM exports WHERE id = ?`, file.ExportID).Scan(&format, &preset, &sum, &bytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if format != "html" || preset != "bundle" || sum != file.SHA256 || bytes != file.Bytes {
		t.Errorf("row %s %s %s %d", format, preset, sum, bytes)
	}
	var m struct {
		Documents    []struct{ ID, SHA256 string }
		IncludesNote bool `json:"includes_notes"`
		PrivateNotes bool `json:"private_notes"`
	}
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Documents) != 9 || !m.PrivateNotes || m.IncludesNote {
		t.Errorf("manifest %s", manifest)
	}
}

// The golden files pin the exact bytes of a small project: the shell, the
// stylesheet and its hash, the title page, the contents and the sections.
func TestHTMLGolden(t *testing.T) {
	f := newFx(t, "The Long Tide")
	f.doc("Opening", "front_matter", "", "For everyone who waited.\n")
	p := f.doc("Part One", "part", "", "")
	c := f.doc("The Arrival", "chapter", p.ID, "She came *late* on the **last** ferry.\n\n* * *\n\nA second scene, with a [link](https://example.org/a?b=1&c=2) and a note.[^1]\n\n[^1]: The note.\n\n<!-- scriobh:alts {\"v\":1} -->\n")
	f.doc("Harbour", "scene", c.ID, "> Quoted words.\n\n- one\n- two\n\nSee [[The Arrival]].\n")
	golden(t, "html-single.golden.html", f.mustHTML(export.HTMLIn{}).Body)
	_, files, _ := unzip(t, f.mustHTML(export.HTMLIn{Mode: "bundle"}).Body)
	golden(t, "html-bundle-index.golden.html", files["index.html"])
	golden(t, "html-bundle-page.golden.html", files["manuscript/01-part-one/01-the-arrival/01-harbour.html"])
}
