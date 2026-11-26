package imports_test

import (
	"archive/zip"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// A book is someone else's file. Each case here is a way a file can be made to hurt, and what must
// happen instead: a refusal in plain words, a part left out and counted, or text that is only text.

func previewErr(e *env, book []byte) (imports.PreviewOut, error) {
	out, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "hostile.epub", Epub: book})
	if err != nil {
		return imports.PreviewOut{}, err
	}
	return out.(imports.PreviewOut), nil
}

func wantRefused(t *testing.T, e *env, name string, book []byte, words ...string) {
	t.Helper()
	_, err := previewErr(e, book)
	if err == nil {
		t.Errorf("%s: a refusal was expected", name)
		return
	}
	if oe := ops.AsError(err); oe.Code != ops.CodeInvalid && oe.Code != ops.CodeTooLarge {
		t.Errorf("%s: %v (code %s), want a 4xx refusal", name, err, oe.Code)
	}
	for _, w := range words {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(w)) {
			t.Errorf("%s: the message %q does not say %q", name, err.Error(), w)
		}
	}
}

func TestEpubRefusesWhatIsNotABook(t *testing.T) {
	e := newEnv(t)
	good := salt3(t)
	for name, c := range map[string]struct {
		book  []byte
		words []string
	}{
		"text, not a zip":             {[]byte("This is not an EPUB at all."), []string{"not an EPUB"}},
		"a cut zip":                   {good[:len(good)/2], []string{"not an EPUB"}},
		"a zip of nothing":            {zipEntries(t), nil},
		"no container":                {zipEntries(t, ze("mimetype", epubMime), ze("a.xhtml", "<p>x</p>")), []string{"container.xml"}},
		"a container with no package": {zipEntries(t, ze("mimetype", epubMime), ze("META-INF/container.xml", containerXML("OEBPS/nope.opf"))), []string{"container.xml"}},
		"a package that is not XML":   {zipEntries(t, ze("mimetype", epubMime), ze("META-INF/container.xml", containerXML("c.opf")), ze("c.opf", "{ \"json\": true }")), []string{"package"}},
		"an empty spine":              {zipEntries(t, ze("mimetype", epubMime), ze("META-INF/container.xml", containerXML("c.opf")), ze("c.opf", `<package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata/><manifest/><spine/></package>`)), []string{"spine"}},
	} {
		wantRefused(t, e, name, c.book, c.words...)
	}
	// Nothing was kept by any of them.
	if n := count(t, e, `SELECT (SELECT count(*) FROM projects) + (SELECT count(*) FROM editions)`); n != 0 {
		t.Errorf("%d rows", n)
	}
}

func TestEpubRefusesDRMAndFixedLayout(t *testing.T) {
	e := newEnv(t)
	enc := func(alg string) string {
		return `<?xml version="1.0" encoding="UTF-8"?><encryption xmlns="urn:oasis:names:tc:opendocument:xmlns:container" xmlns:enc="http://www.w3.org/2001/04/xmlenc#"><enc:EncryptedData><enc:EncryptionMethod Algorithm="` + alg + `"/><enc:CipherData><enc:CipherReference URI="OEBPS/text/ch01.xhtml"/></enc:CipherData></enc:EncryptedData></encryption>`
	}
	good := salt3(t)
	withEntry := func(name, body string) []byte {
		zr, _ := zip.NewReader(bytes.NewReader(good), int64(len(good)))
		var ents []zent
		for _, f := range zr.File {
			rc, _ := f.Open()
			var b bytes.Buffer
			b.ReadFrom(rc)
			rc.Close()
			ents = append(ents, zent{name: f.Name, data: b.Bytes()})
		}
		return zipEntries(t, append(ents, ze(name, body))...)
	}
	wantRefused(t, e, "an Adobe-encrypted chapter", withEntry("META-INF/encryption.xml", enc("http://www.w3.org/2001/04/xmlenc#aes128-cbc")), "DRM")
	wantRefused(t, e, "a rights file", withEntry("META-INF/rights.xml", `<rights/>`), "DRM")
	wantRefused(t, e, "an encryption file nobody can read", withEntry("META-INF/encryption.xml", "garbage <"), "DRM")
	// Obfuscated fonts are not DRM.
	pv, err := previewErr(e, withEntry("META-INF/encryption.xml", enc("http://www.idpf.org/2008/embedding")))
	if err != nil || pv.Documents != 6 {
		t.Errorf("a book with obfuscated fonts: %v", err)
	}
	// Fixed layout: the whole book, or pages of it.
	fixed := replaceInZip(t, good, "OEBPS/content.opf", `<meta property="dcterms:modified">`, `<meta property="rendition:layout">pre-paginated</meta><meta property="dcterms:modified">`)
	wantRefused(t, e, "a fixed-layout book", fixed, "fixed-layout")
	apple := replaceInZip(t, good, "OEBPS/content.opf", `<meta name="cover"`, `<meta name="fixed-layout" content="true"/><meta name="cover"`)
	wantRefused(t, e, "an Apple fixed-layout book", apple, "fixed-layout")
	mixed := replaceInZip(t, good, "OEBPS/content.opf", `<itemref idref="ep"/>`, `<itemref idref="ep" properties="rendition:layout-pre-paginated"/>`)
	pv, err = previewErr(e, mixed)
	if err != nil || pv.Documents != 5 || !strings.Contains(strings.Join(pv.Warnings, "\n"), "fixed-layout page") {
		t.Errorf("a book with one fixed page: %v %d %v", err, pv.Documents, pv.Warnings)
	}
}

func TestEpubXMLTricks(t *testing.T) {
	e := newEnv(t)
	good := salt3(t)
	bomb := `<?xml version="1.0"?><!DOCTYPE lolz [<!ENTITY lol "lol"><!ENTITY lol2 "&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;&lol;"><!ENTITY lol3 "&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;&lol2;">]><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="&lol3;" media-type="application/oebps-package+xml"/></rootfiles></container>`
	xxe := `<?xml version="1.0"?><!DOCTYPE c [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="&xxe;" media-type="application/oebps-package+xml"/></rootfiles></container>`
	for name, c := range map[string]string{"expansion": bomb, "an external entity": xxe} {
		book := replaceInZip(t, good, "META-INF/container.xml", containerXML("OEBPS/content.opf"), c)
		t0 := time.Now()
		wantRefused(t, e, name+" in the container", book, "DOCTYPE")
		d := time.Since(t0)
		t.Run("time", func(t *testing.T) {
			testutil.Timing(t)
			if d > 2*time.Second*timingFactor {
				t.Errorf("%s took %v", name, d)
			}
		})
	}
	opfBomb := strings.Replace(`<?xml version="1.0"?><!DOCTYPE package [<!ENTITY a "aaaaaaaaaa">]>`, "<?xml", "<?xml", 1)
	book := replaceInZip(t, good, "OEBPS/content.opf", `<?xml version="1.0" encoding="utf-8"?>`, opfBomb)
	wantRefused(t, e, "an entity in the package", book, "DOCTYPE")
	// An internal subset on a content file defines nothing: the tokenizer knows no entity but HTML's own.
	xhtml := `<?xml version="1.0"?><!DOCTYPE html [<!ENTITY xxe SYSTEM "file:///etc/passwd">]><html xmlns="http://www.w3.org/1999/xhtml"><head><title>x</title></head><body><h1>One</h1><p>&xxe; and &lt;b&gt; and &#x41; &amp; &unknown;</p></body></html>`
	book = replaceInZip(t, good, "OEBPS/text/ch02.xhtml", `<section epub:type="chapter"><h2>Chapter 2<br/>The Wall</h2><p>Rain.</p><p class="scenebreak">* * *</p><p>More rain.</p></section>`, `<section><h2>x</h2></section>`)
	book = replaceXHTML(t, book, "OEBPS/text/ch02.xhtml", xhtml)
	pv := e.previewConv(imports.PreviewIn{Name: "xxe.epub", Epub: book})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "xxe.epub", Epub: book})
	var c string
	_ = e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = 'The Wall'`, ap.ProjectID).Scan(&c)
	lacks(t, c, "root:", "/bin/")
	has(t, c, "A \\& ")
}

func replaceXHTML(t testing.TB, book []byte, name, with string) []byte {
	t.Helper()
	zr, _ := zip.NewReader(bytes.NewReader(book), int64(len(book)))
	var ents []zent
	for _, f := range zr.File {
		rc, _ := f.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		d := b.Bytes()
		if f.Name == name {
			d = []byte(with)
		}
		ents = append(ents, zent{name: f.Name, data: d, store: f.Method == zip.Store})
	}
	return zipEntries(t, ents...)
}

func TestEpubContentIsOnlyText(t *testing.T) {
	e := newEnv(t)
	body := `<h1>One</h1>
<script>alert(1)</script><script src="x.js"/><p>After a self-closed script.</p>
<style>p{display:none}</style><style/><p>After styles.</p>
<svg:svg xmlns:svg="http://www.w3.org/2000/svg"><svg:script>steal()</svg:script></svg:svg><p>After a prefixed svg.</p>
<xhtml:script xmlns:xhtml="http://www.w3.org/1999/xhtml">bad()</xhtml:script>
<iframe src="http://evil.example/x"></iframe><object data="x.swf"><param name="a" value="b"/></object><embed src="y"/>
<form action="http://evil.example/"><input name="q" value="secret"/><button>Go</button><textarea>area</textarea></form>
<audio src="a.mp3">no audio</audio><video src="v.mp4">no video</video><canvas>no canvas</canvas><math><mi>x</mi></math>
<p><a href="javascript:alert(1)">js link</a> <a href="data:text/html;base64,PHNjcmlwdD4=">data link</a> <a href="http://example.com/ok">web link</a> <a href="mailto:a@b.example">mail</a> <a href="file:///etc/passwd">file link</a> <a href="vbscript:x">vb</a></p>
<p><img src="http://evil.example/track.gif" alt="remote"/> <img src="data:image/png;base64,iVBORw0KGgo=" alt="inline"/> <img src="../../../../etc/passwd" alt="climb"/> <img src="missing.png" alt="gone"/></p>
<p onclick="evil()" style="background:url(http://evil.example/x)">Event attributes are nothing.</p>
<p>&lt;scriobh-ghost&gt; %%private%% [[Wiki]] ::: directive <!-- scriobh:alts --></p>
<!-- a comment <script>bad()</script> --><p>End.</p>`
	book := miniBook(t, "Hostile", []mbChapter{{file: "a.xhtml", title: "One", body: body}})
	pv := e.previewConv(imports.PreviewIn{Name: "h.epub", Epub: book})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "h.epub", Epub: book})
	var c string
	_ = e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ? AND title = 'One'`, ap.ProjectID).Scan(&c)
	has(t, c, "After a self-closed script.", "After styles.", "After a prefixed svg.", "End.", "js link", "[web link](http://example.com/ok)", "[mail](mailto:a@b.example)", `\[image: remote\]`, `\[image: inline\]`)
	lacks(t, c, "alert", "steal", "bad()", "secret", "area", "no audio", "no video", "no canvas", "evil", "javascript:", "data:text", "file:///", "vbscript", "etc/passwd", "display:none", "<script", "<iframe", "onclick")
	// What the book says that would be a construct of this program is text, escaped.
	has(t, c, `\<scriobh-ghost\>`, `\[\[Wiki\]\]`)
	lacks(t, c, "\n%%private%%", "<!-- scriobh:alts")
	if n := pv.Report["dropped_active_content"]; n == nil {
		t.Errorf("the preview does not say that active content was dropped: %v", pv.Report)
	}
	if pv.Pictures != 0 {
		t.Errorf("a picture that is not in the book was counted: %d", pv.Pictures)
	}
}

func TestEpubSizeLimits(t *testing.T) {
	e := newEnv(t)
	good := salt3(t)
	// A content file that inflates to far more than may be read is left out and said so, quickly.
	big := bytes.Repeat([]byte("<p>"+strings.Repeat("a", 1000)+"</p>\n"), 20_000) // 20 MB
	book := miniBook(t, "Big", []mbChapter{{file: "a.xhtml", title: "A", body: `<p>small</p>`}, {file: "b.xhtml", title: "B", body: `<p>also small</p>`}})
	book = replaceXHTML(t, book, "OEBPS/b.xhtml", page("B", "", string(big)))
	t0 := time.Now()
	pv, err := previewErr(e, book)
	if err != nil {
		t.Fatalf("a book with one very large file: %v", err)
	}
	d := time.Since(t0)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 3*time.Second*timingFactor {
			t.Errorf("took %v", d)
		}
	})
	var skipped bool
	for _, s := range pv.Skipped {
		if strings.HasSuffix(s.Path, "b.xhtml") && strings.Contains(s.Reason, "larger") {
			skipped = true
		}
	}
	if !skipped {
		t.Errorf("the large file is not said to be left out: %+v", pv.Skipped)
	}
	// Many files that each fit but together do not.
	var ents []zent
	var chs []mbChapter
	for i := 0; i < 20; i++ {
		chs = append(chs, mbChapter{file: fmt.Sprintf("c%02d.xhtml", i), title: fmt.Sprintf("C%d", i), body: "<p>x</p>"})
	}
	many := miniBook(t, "Many", chs)
	zr, _ := zip.NewReader(bytes.NewReader(many), int64(len(many)))
	for _, f := range zr.File {
		rc, _ := f.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		d := b.Bytes()
		if strings.HasPrefix(f.Name, "OEBPS/c") {
			d = []byte(page("c", "", strings.Repeat("<p>"+strings.Repeat("w", 1000)+"</p>", 7000))) // 7 MB each, 140 MB all
		}
		ents = append(ents, zent{name: f.Name, data: d})
	}
	t0 = time.Now()
	wantRefused(t, e, "twenty files of 7 MB", zipEntries(t, ents...), "limit")
	d = time.Since(t0)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 6*time.Second*timingFactor {
			t.Errorf("twenty big files took %v", d)
		}
	})
	// More entries than a book holds.
	var many2 []zent
	many2 = append(many2, ze("mimetype", epubMime))
	for i := 0; i < 10_500; i++ {
		many2 = append(many2, ze(fmt.Sprintf("x/f%05d.txt", i), "x"))
	}
	wantRefused(t, e, "10,500 entries", zipEntries(t, many2...), "entries")
	_ = good
}

func TestEpubManyChaptersAndPictures(t *testing.T) {
	e := newEnv(t)
	var chs []mbChapter
	for i := 0; i < 2300; i++ {
		chs = append(chs, mbChapter{file: fmt.Sprintf("c%04d.xhtml", i), title: fmt.Sprintf("Chapter %d", i+1), body: fmt.Sprintf("<h1>Chapter %d</h1><p>Text.</p>", i+1)})
	}
	book := miniBook(t, "Enormous", chs)
	t0 := time.Now()
	pv, err := previewErr(e, book)
	if err != nil {
		t.Fatal(err)
	}
	if pv.Documents > imports.MaxMarkdown || !strings.Contains(strings.Join(pv.Warnings, "\n"), "left out") {
		t.Errorf("%d documents; warnings %v", pv.Documents, pv.Warnings)
	}
	if len(pv.Epub.Files) > 5000 {
		t.Errorf("the preview lists %d files", len(pv.Epub.Files))
	}
	d := time.Since(t0)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 10*time.Second*timingFactor {
			t.Errorf("2,300 chapters took %v", d)
		}
	})
}

func TestEpubDeepAndHugeMarkup(t *testing.T) {
	e := newEnv(t)
	deep := strings.Repeat("<div>", 200_000) + "<p>bottom</p>" + strings.Repeat("</div>", 200_000)
	nav := `<nav epub:type="toc"><ol>` + strings.Repeat("<li><a href=\"a.xhtml\">x</a><ol>", 20_000) + strings.Repeat("</ol></li>", 20_000) + `</ol></nav>`
	book := miniBook(t, "Deep", []mbChapter{{file: "a.xhtml", title: "A", body: "<h1>Deep</h1>" + deep}})
	book = replaceXHTML(t, book, "OEBPS/nav.xhtml", page("Contents", "", nav))
	t0 := time.Now()
	pv, err := previewErr(e, book)
	if err != nil {
		t.Fatalf("deep markup: %v", err)
	}
	d := time.Since(t0)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 5*time.Second*timingFactor {
			t.Errorf("deep markup took %v", d)
		}
	})
	if len(pv.Nodes) == 0 {
		t.Error("nothing came of it")
	}
	// An NCX nested far past what XML here may be.
	ncx := `<?xml version="1.0"?><ncx xmlns="http://www.daisy.org/z3986/2005/ncx/"><navMap>` + strings.Repeat(`<navPoint id="n"><navLabel><text>x</text></navLabel><content src="a.xhtml"/>`, 5000) + strings.Repeat("</navPoint>", 5000) + `</navMap></ncx>`
	book2 := miniBook(t, "Deep NCX", []mbChapter{{file: "a.xhtml", title: "A", body: "<h1>A</h1><p>x</p>", skipNav: true}, {file: "b.xhtml", title: "B", body: "<h1>B</h1><p>y</p>", skipNav: true}})
	book2 = replaceInZip(t, book2, "OEBPS/content.opf", `<spine>`, `<spine toc="ncx">`)
	book2 = replaceInZip(t, book2, "OEBPS/content.opf", `</manifest>`, `<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/></manifest>`)
	book2 = addToZip(t, book2, "OEBPS/toc.ncx", ncx)
	book2 = replaceInZip(t, book2, "OEBPS/content.opf", ` properties="nav"`, ``) // no navigation document: the NCX is all there is
	pv, err = previewErr(e, book2)
	if err != nil || len(pv.Nodes) != 2 {
		t.Errorf("a book with an unusable NCX: %v %v", err, outline(pv.Nodes))
	}
}

func addToZip(t testing.TB, book []byte, name, body string) []byte {
	t.Helper()
	zr, _ := zip.NewReader(bytes.NewReader(book), int64(len(book)))
	var ents []zent
	for _, f := range zr.File {
		rc, _ := f.Open()
		var b bytes.Buffer
		b.ReadFrom(rc)
		rc.Close()
		ents = append(ents, zent{name: f.Name, data: b.Bytes(), store: f.Method == zip.Store})
	}
	return zipEntries(t, append(ents, ze(name, body))...)
}

func TestEpubPathsThatClimb(t *testing.T) {
	e := newEnv(t)
	good := salt3(t)
	// Entries whose names climb, are absolute or use backslashes are ignored; the book still reads.
	book := addToZip(t, good, "../evil.xhtml", "<p>x</p>")
	book = addToZip(t, book, "/abs/evil.xhtml", "<p>x</p>")
	book = addToZip(t, book, "OEBPS\\..\\..\\evil.xhtml", "<p>x</p>")
	pv, err := previewErr(e, book)
	if err != nil || pv.Documents != 6 {
		t.Fatalf("%v %d", err, pv.Documents)
	}
	// A package that names a rootfile above the root is not followed.
	bad := replaceInZip(t, good, "META-INF/container.xml", `OEBPS/content.opf`, `../OEBPS/content.opf`)
	wantRefused(t, e, "a rootfile above the root", bad, "container.xml")
	// A navigation entry that climbs names nothing.
	nav := replaceInZip(t, good, "OEBPS/nav.xhtml", `href="text/ch01.xhtml"`, `href="../../../text/ch01.xhtml"`)
	pv, err = previewErr(e, nav)
	if err != nil || pv.Documents == 0 {
		t.Fatalf("%v", err)
	}
	// Every path a document records is inside the book.
	pg := e.previewConv(imports.PreviewIn{Name: "x.epub", Epub: good})
	e.applyConv(pg, imports.ApplyIn{Name: "x.epub", Epub: good})
	rows, _ := e.d.Read().Query(`SELECT source_path FROM document_sources`)
	defer rows.Close()
	for rows.Next() {
		var p string
		rows.Scan(&p)
		if strings.Contains(p, "..") || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
			t.Errorf("a recorded source path climbs: %q", p)
		}
	}
}

func TestEpubNotesThatLoopOrMultiply(t *testing.T) {
	e := newEnv(t)
	// A hundred thousand references to one note, a note that refers to itself, two notes that refer to
	// each other, and a reference to a note that is the whole chapter.
	var refs strings.Builder
	for i := 0; i < 100_000; i++ {
		refs.WriteString(`<a epub:type="noteref" href="#n1">1</a> `)
	}
	body := `<h1>Notes</h1><p>Start ` + refs.String() + `</p>
<p>Self <a epub:type="noteref" id="s" href="#s">*</a>.</p>
<p>Ping <a epub:type="noteref" id="p1" href="#n2">2</a>, pong <a epub:type="noteref" id="p2" href="#n3">3</a>.</p>
<p>Whole <a epub:type="noteref" href="#all">all</a>.</p>
<aside epub:type="footnote" id="n1"><p>One note, read once.</p></aside>
<aside epub:type="footnote" id="n2"><p>Two sees <a epub:type="noteref" href="#n3">3</a>.</p></aside>
<aside epub:type="footnote" id="n3"><p>Three sees <a epub:type="noteref" href="#n2">2</a>.</p></aside>`
	book := miniBook(t, "Loops", []mbChapter{{file: "a.xhtml", title: "Notes", body: `<div id="all">` + body + `</div>`}})
	t0 := time.Now()
	pv, err := previewErr(e, book)
	if err != nil {
		t.Fatal(err)
	}
	d := time.Since(t0)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 5*time.Second*timingFactor {
			t.Errorf("took %v", d)
		}
	})
	ap := e.applyConv(pv, imports.ApplyIn{Name: "n.epub", Epub: book})
	var c string
	_ = e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ?`, ap.ProjectID).Scan(&c)
	if strings.Count(c, "One note, read once.") != 1 {
		t.Errorf("the note was written %d times", strings.Count(c, "One note, read once."))
	}
	if len(c) > 2<<20 {
		t.Errorf("the chapter is %d bytes", len(c))
	}
}

func TestEpubPicturesThatAreNotPictures(t *testing.T) {
	e := newEnv(t)
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script><rect width="1" height="1"/></svg>`
	html := `<!DOCTYPE html><html><body><h1>Not a picture</h1><p>A page that says it is an image.</p></body></html>`
	bomb := pngBytes(4, 4, 1)
	copy(bomb[16:24], []byte{0x00, 0x00, 0xFF, 0xFF, 0x00, 0x00, 0xFF, 0xFF}) // a header that says 65,535 x 65,535
	book := miniBook(t, "Pictures", []mbChapter{{file: "a.xhtml", title: "A", body: `<h1>A</h1><p><img src="evil.svg" alt="svg"/><img src="page.png" alt="html"/><img src="bomb.png" alt="bomb"/><img src="ok.png" alt="ok"/></p>`}},
		ze("OEBPS/evil.svg", svg), ze("OEBPS/page.png", html), zent{name: "OEBPS/bomb.png", data: bomb}, zent{name: "OEBPS/ok.png", data: pngBytes(8, 8, 3)})
	pv := e.previewConv(imports.PreviewIn{Name: "p.epub", Epub: book})
	if pv.Pictures != 1 {
		t.Errorf("pictures counted: %d, want the one real one", pv.Pictures)
	}
	ap := e.applyConv(pv, imports.ApplyIn{Name: "p.epub", Epub: book})
	var c string
	_ = e.d.Read().QueryRow(`SELECT content FROM documents WHERE project_id = ?`, ap.ProjectID).Scan(&c)
	has(t, c, "![ok](/files/att_", `\[image: svg\]`, `\[image: html\]`, `\[image: bomb\]`)
	if n := count(t, e, `SELECT count(*) FROM attachments`); n != 1 {
		t.Errorf("attachments: %d", n)
	}
	// An SVG page in the reading order is not text.
	svgPage := miniBook(t, "SVG page", []mbChapter{{file: "a.xhtml", title: "A", body: `<h1>A</h1><p>Text.</p>`}}, ze("OEBPS/p.svg", svg))
	svgPage = replaceInZip(t, svgPage, "OEBPS/content.opf", `</manifest>`, `<item id="svgp" href="p.svg" media-type="image/svg+xml"/></manifest>`)
	svgPage = replaceInZip(t, svgPage, "OEBPS/content.opf", `</spine>`, `<itemref idref="svgp"/></spine>`)
	pv = e.previewConv(imports.PreviewIn{Name: "s.epub", Epub: svgPage})
	if pv.Documents != 1 {
		t.Errorf("documents: %d", pv.Documents)
	}
}

func TestEpubNeverReachesForAnything(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	e := newEnv(t)
	book := miniBook(t, "Remote", []mbChapter{{file: "a.xhtml", title: "A", body: fmt.Sprintf(`<h1>A</h1><p><img src="%[1]s/a.png"/><a href="%[1]s/x">link</a></p><link rel="stylesheet" href="%[1]s/s.css"/><iframe src="%[1]s/f"></iframe><video src="%[1]s/v.mp4"></video><object data="%[1]s/o"></object></p>`, srv.URL)}})
	book = replaceInZip(t, book, "OEBPS/content.opf", `<dc:title>Remote</dc:title>`, `<dc:title>Remote</dc:title><dc:source>`+srv.URL+`/src</dc:source><dc:relation>`+srv.URL+`/rel</dc:relation>`)
	pv := e.previewConv(imports.PreviewIn{Name: "r.epub", Epub: book})
	e.applyConv(pv, imports.ApplyIn{Name: "r.epub", Epub: book})
	if n := hits.Load(); n != 0 {
		t.Errorf("the importer made %d requests", n)
	}
}

// Each count a book can be made to run up has a cap, and what is over it is said, not silently cut.
func TestEpubCountsAreCapped(t *testing.T) {
	e := newEnv(t)
	good := miniBook(t, "Caps", []mbChapter{{file: "a.xhtml", title: "A", body: `<h1>A</h1><p>Text.</p>`}})
	// 20,001 itemrefs, and a manifest of 40,001 items.
	var refs, items strings.Builder
	for i := 0; i < 20_001; i++ {
		refs.WriteString(`<itemref idref="i0"/>`)
	}
	for i := 0; i < 40_001; i++ {
		fmt.Fprintf(&items, `<item id="x%d" href="x%d.png" media-type="image/png"/>`, i, i)
	}
	book := replaceInZip(t, good, "OEBPS/content.opf", `</spine>`, refs.String()+`</spine>`)
	book = replaceInZip(t, book, "OEBPS/content.opf", `</manifest>`, items.String()+`</manifest>`)
	pv, err := previewErr(e, book)
	if err != nil || pv.Documents != 1 {
		t.Fatalf("%v %d", err, pv.Documents)
	}
	w := strings.Join(pv.Warnings, "\n")
	has(t, w, "more than 20000 items", "more than 40000 items")
	// 50,001 entries in the navigation.
	nav := `<nav epub:type="toc"><ol>` + strings.Repeat(`<li><a href="a.xhtml">x</a></li>`, 50_001) + `</ol></nav>`
	if _, err := previewErr(e, replaceXHTML(t, good, "OEBPS/nav.xhtml", page("Contents", "", nav))); err != nil {
		t.Errorf("a navigation of 50,001 entries: %v", err)
	}
	// More elements in one file than are recorded.
	huge := miniBook(t, "Dense", []mbChapter{{file: "a.xhtml", title: "A", body: `<h1>A</h1><p>` + strings.Repeat("<i/>", 450_000) + `Text.</p>`}})
	pv, err = previewErr(e, huge)
	if err != nil {
		t.Fatal(err)
	}
	has(t, strings.Join(pv.Warnings, "\n"), "too intricate")
	// 2,100 pictures: 2,000 are kept and the rest are said not to be.
	var imgs strings.Builder
	var ents []zent
	for i := 0; i < 2100; i++ {
		fmt.Fprintf(&imgs, `<img src="p%d.png" alt="p%d"/>`, i, i)
		ents = append(ents, zent{name: fmt.Sprintf("OEBPS/p%d.png", i), data: pngBytes(2, 2, uint8(i)), store: true})
	}
	pics := miniBook(t, "Pictures", []mbChapter{{file: "a.xhtml", title: "A", body: `<h1>A</h1><p>` + imgs.String() + `</p>`}}, ents...)
	pv = e.previewConv(imports.PreviewIn{Name: "p.epub", Epub: pics})
	if pv.Pictures > 2000 || pv.Pictures < 1000 {
		t.Errorf("pictures kept: %d", pv.Pictures)
	}
	if pv.Report["pictures_not_kept"] == nil {
		t.Errorf("the pictures over the limit are not said to be left out: %v", pv.Report)
	}
	// A stylesheet over 1 MiB is not read, and the book still is.
	css := miniBook(t, "Css", []mbChapter{{file: "a.xhtml", title: "A", body: `<h1>A</h1><p class="it">Text.</p>`}}, ze("OEBPS/s.css", ".it{font-style:italic}"+strings.Repeat(" ", 2<<20)))
	css = replaceInZip(t, css, "OEBPS/content.opf", `</manifest>`, `<item id="css" href="s.css" media-type="text/css"/></manifest>`)
	pv = e.previewConv(imports.PreviewIn{Name: "c.epub", Epub: css})
	if pv.Documents != 1 {
		t.Errorf("documents %d", pv.Documents)
	}
}

// The 32 MB the book is kept at is the limit of the preview too: a book that cannot be kept is not previewed as if it could.
func TestEpubOverTheSizeThatIsKeptIsRefusedAtThePreview(t *testing.T) {
	e := newEnv(t)
	filler := bytes.Repeat([]byte{0xA5, 0x5A, 0x33, 0xCC}, 9<<20) // 36 MB that does not shrink much
	rng := make([]byte, len(filler))
	for i := range rng {
		rng[i] = filler[i] ^ byte(i*7+i>>8)
	}
	book := miniBook(t, "Large", []mbChapter{{file: "a.xhtml", title: "A", body: `<h1>A</h1><p>Text.</p>`}}, zent{name: "OEBPS/blob.bin", data: rng, store: true})
	if len(book) <= 32<<20 {
		t.Fatalf("the test book is only %d bytes", len(book))
	}
	wantRefused(t, e, "a 36 MB book", book, "32 MB")
}
